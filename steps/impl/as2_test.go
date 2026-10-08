package impl

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"dif/message"
	stepdef "dif/steps/definition"
)

func (p cmsParty) pem(withKey bool) string {
	out := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: p.cert.Raw}))
	if withKey {
		der, _ := x509.MarshalPKCS8PrivateKey(p.key)
		out += string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
	}
	return out
}

func freePort(t testing.TB) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// as2Env is two trading partners and an AS2 endpoint of the second.
type as2Env struct {
	sender, partner, stranger cmsParty
	port                      int
}

func newAS2Env(t *testing.T) as2Env {
	return as2Env{cmsTestParty(t, "sender"), cmsTestParty(t, "partner"), cmsTestParty(t, "stranger"), freePort(t)}
}

// sourceOpts are the options of an endpoint of the partner that trusts the sender.
func (e as2Env) sourceOpts(extra map[string]any) map[string]any {
	o := map[string]any{
		"serverPortNumber": e.port, "address": "127.0.0.1", "requestUriPattern": "/as2",
		"decryptingPrivateKey": e.partner.pem(true), "signingPrivateKey": e.partner.pem(true),
		"validateSigningCertificateChain": e.sender.pem(false),
	}
	for k, v := range extra {
		if v == nil {
			delete(o, k)
		} else {
			o[k] = v
		}
	}
	return o
}

// actionOpts are the options of a sender to the endpoint.
func (e as2Env) actionOpts(structure string, extra map[string]any) map[string]any {
	o := map[string]any{
		"hostName": "127.0.0.1", "targetPortNumber": strconv.Itoa(e.port), "requestUri": "/as2",
		"as2From": "SENDER", "as2To": "RECEIVER", "subject": "Invoice", "ediMessageContentType": "application/edifact",
		"as2MessageStructure": structure, "certificateForSigning": e.sender.pem(true), "certificateForEncrypt": e.partner.pem(false),
	}
	for k, v := range extra {
		if v == nil {
			delete(o, k)
		} else {
			o[k] = v
		}
	}
	return o
}

// as2Endpoint is a running source with the messages it got.
type as2Endpoint struct {
	msgs   chan message.Message
	cancel context.CancelFunc
	done   chan error
	logged *lockedBuffer
}

// startAS2 runs the source; fail decides, for each message, if the flow fails on it.
func startAS2(t *testing.T, opts map[string]any, fail func(message.Message) error) *as2Endpoint {
	t.Helper()
	src := mustProcessor(t, stepdef.Source, "as2", opts).(stepdef.ReadySourceProcessor)
	logged := &lockedBuffer{}
	ctx, cancel := context.WithCancel(stepdef.WithLogger(context.Background(), log.New(logged, "", 0)))
	ep := &as2Endpoint{msgs: make(chan message.Message, 50), cancel: cancel, done: make(chan error, 1), logged: logged}
	ready := make(chan struct{})
	go func() {
		ep.done <- src.RunReady(ctx, func(m message.Message, reply func(message.Message, error)) error {
			ep.msgs <- m
			var err error
			if fail != nil {
				err = fail(m)
			}
			reply(m, err)
			return nil
		}, func() { close(ready) })
	}()
	select {
	case <-ready:
	case err := <-ep.done:
		t.Fatalf("the source stopped: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("the source did not start")
	}
	t.Cleanup(func() { cancel(); <-ep.done })
	return ep
}

func (ep *as2Endpoint) take(t *testing.T) message.Message {
	t.Helper()
	select {
	case m := <-ep.msgs:
		return m
	case <-time.After(5 * time.Second):
		t.Fatal("no message came")
		return nil
	}
}

func sendAS2(t *testing.T, opts map[string]any, body string, headers map[string]any) (message.Message, error) {
	t.Helper()
	p := mustProcessor(t, stepdef.Action, "as2", opts).(stepdef.ActionProcessor)
	m := message.New(body)
	for k, v := range headers {
		m[k] = v
	}
	return p.Process(context.Background(), m)
}

const edi = "UNA:+.? 'UNB+UNOC:3+SENDER:14+RECEIVER:14+250527:0801+19542'UNH+1+DESADV:D:96B:UN'UNT+2+1'UNZ+1+19542'\r\n"

func TestAS2Structures(t *testing.T) {
	e := newAS2Env(t)
	for _, structure := range []string{"PLAIN", "SIGNED", "ENCRYPTED", "SIGNED_ENCRYPTED", "PLAIN_COMPRESSED", "SIGNED_COMPRESSED", "ENCRYPTED_COMPRESSED", "ENCRYPTED_COMPRESSED_SIGNED"} {
		t.Run(structure, func(t *testing.T) {
			ep := startAS2(t, e.sourceOpts(map[string]any{"as2MessageStructure": structure}), nil)
			st, _ := parseAS2Structure(structure)

			sent, err := sendAS2(t, e.actionOpts(structure, nil), edi, map[string]any{FileName: "desadv.edi", "keep": "me"})
			if err != nil {
				t.Fatal(err)
			}
			if sent[message.Body] != edi || sent["keep"] != "me" || sent["http.status"] != 200 {
				t.Errorf("the message changed: %v", sent)
			}
			if d, _ := sent["AS2-MDN-Disposition"].(string); !strings.HasSuffix(d, "processed") || sent["AS2-MDN-Signed"] != st.sign {
				t.Errorf("disposition = %q, signed = %v, want a processed receipt, signed %v", sent["AS2-MDN-Disposition"], sent["AS2-MDN-Signed"], st.sign)
			}
			if id, _ := sent["AS2-Message-Id"].(string); !strings.HasPrefix(id, "<dif-") {
				t.Errorf("message id = %q", id)
			}

			got := ep.take(t)
			if got[message.Body] != edi || got[message.ContentType] != "application/edifact" || got[FileName] != "desadv.edi" ||
				got["AS2-From"] != "SENDER" || got["AS2-To"] != "RECEIVER" || got["Subject"] != "Invoice" || got["AS2-Message-Id"] != sent["AS2-Message-Id"] {
				t.Errorf("message = %v", got)
			}
			if got["AS2-Signed"] != st.sign || got["AS2-Encrypted"] != st.encrypt || got["AS2-Compressed"] != st.compress {
				t.Errorf("signed %v, encrypted %v, compressed %v; want %+v", got["AS2-Signed"], got["AS2-Encrypted"], got["AS2-Compressed"], st)
			}
			if st.sign && !strings.Contains(got["AS2-Signer"].(string), "CN=sender") {
				t.Errorf("signer = %v", got["AS2-Signer"])
			}
		})
	}
}

func TestAS2BinaryAndOtherAlgorithms(t *testing.T) {
	e := newAS2Env(t)
	ep := startAS2(t, e.sourceOpts(nil), nil)
	for _, c := range []struct{ sign, enc string }{{"SHA1WITHRSA", "DES_EDE3_CBC"}, {"SHA384WITHRSA", "AES192_CBC"}, {"SHA512WITHRSA", "AES256_CBC"}} {
		bin := "\x00\x01\xff binary " + c.sign
		if _, err := sendAS2(t, e.actionOpts("SIGNED_ENCRYPTED", map[string]any{"signingAlgorithm": c.sign, "encryptingAlgorithm": c.enc, "ediMessageContentType": "application/octet-stream"}), bin, nil); err != nil {
			t.Fatalf("%v: %v", c, err)
		}
		got := ep.take(t)
		if b, ok := got[message.Body].([]byte); !ok || string(b) != bin {
			t.Errorf("%v: body = %#v", c, got[message.Body])
		}
	}
}

func TestAS2AReceiptThatSaysNo(t *testing.T) {
	e := newAS2Env(t)
	for _, c := range []struct {
		name      string
		source    map[string]any
		action    map[string]any
		structure string
		want      string
	}{
		{"encrypted for someone else", nil, map[string]any{"certificateForEncrypt": e.stranger.pem(false)}, "ENCRYPTED", "decryption-failed"},
		{"a signer that is not trusted", nil, map[string]any{"certificateForSigning": e.stranger.pem(true)}, "SIGNED_ENCRYPTED", "authentication-failed"},
		{"a signature is required", map[string]any{"as2MessageStructure": "SIGNED"}, nil, "PLAIN", "insufficient-message-security"},
		{"encryption is required", map[string]any{"as2MessageStructure": "ENCRYPTED"}, nil, "SIGNED", "insufficient-message-security"},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := e
			e.port = freePort(t)
			ep := startAS2(t, e.sourceOpts(c.source), nil)
			_, err := sendAS2(t, e.actionOpts(c.structure, c.action), edi, nil)
			if err == nil || !strings.Contains(err.Error(), "did not accept the message") || !strings.Contains(err.Error(), c.want) {
				t.Errorf("err = %v, want a receipt with %s", err, c.want)
			}
			select {
			case m := <-ep.msgs:
				t.Errorf("the flow got the message: %v", m)
			default:
			}
			if !strings.Contains(ep.logged.String(), "message <dif-") {
				t.Errorf("log = %q", ep.logged.String())
			}
		})
	}
}

func TestAS2TheFlowFails(t *testing.T) {
	e := newAS2Env(t)
	startAS2(t, e.sourceOpts(nil), func(message.Message) error { return fmt.Errorf("the flow could not take it") })
	_, err := sendAS2(t, e.actionOpts("SIGNED", nil), edi, nil)
	if err == nil || !strings.Contains(err.Error(), "unexpected-processing-error") {
		t.Errorf("err = %v, want a receipt that tells the flow failed", err)
	}
}

func TestAS2WithoutSignatureTrustAnySigner(t *testing.T) {
	e := newAS2Env(t)
	ep := startAS2(t, e.sourceOpts(map[string]any{"validateSigningCertificateChain": nil}), nil)
	if _, err := sendAS2(t, e.actionOpts("SIGNED", map[string]any{"certificateForSigning": e.stranger.pem(true)}), edi, nil); err != nil {
		t.Fatal(err)
	}
	if m := ep.take(t); !strings.Contains(m["AS2-Signer"].(string), "stranger") {
		t.Errorf("signer = %v", m["AS2-Signer"])
	}
}

func TestAS2NoReceipt(t *testing.T) {
	e := newAS2Env(t)
	ep := startAS2(t, e.sourceOpts(nil), nil)
	// mdn none asks for no receipt: the message arrives, and nothing is checked.
	sent, err := sendAS2(t, e.actionOpts("PLAIN", map[string]any{"mdn": "none"}), edi, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, has := sent["AS2-MDN-Disposition"]; has {
		t.Errorf("a disposition without a receipt: %v", sent)
	}
	ep.take(t)
}

// A partner that answers as the test says.
func fakePartner(t *testing.T, handler http.HandlerFunc) (host, port string) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	h, p, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	return h, p
}

func TestAS2BadAnswers(t *testing.T) {
	e := newAS2Env(t)
	// receipt is an answer with an MDN of this disposition and MIC.
	receipt := func(disposition, mic string) func() (string, []byte) {
		return func() (string, []byte) {
			r := mdnReport{text: "x", reportingUA: "test", recipient: "RECEIVER", originalID: "<id>", disposition: disposition, mic: mic, micAlgName: "sha-256"}.entity()
			return r.header("Content-Type"), r.body
		}
	}
	for _, c := range []struct {
		name   string
		status int
		answer func() (contentType string, body []byte)
		want   string
	}{
		{"a server error", 500, func() (string, []byte) { return "text/plain", []byte("boom") }, "500 Internal Server Error: boom"},
		{"no body", 200, func() (string, []byte) { return "", nil }, "answered without an MDN"},
		{"no MDN", 200, func() (string, []byte) { return "text/plain", []byte("thanks") }, "no MDN"},
		{"another MIC", 200, receipt(dispositionProcessed(), "AAAA"), "received something else than was sent"},
		{"an error disposition", 200, receipt(dispositionError("decompression-failed"), ""), "decompression-failed"},
		{"a failed disposition", 200, receipt("automatic-action/MDN-sent-automatically; failed/failure: sorry", ""), "did not accept"},
		{"a receipt without disposition", 200, func() (string, []byte) {
			ct, body := receipt("x", "")()
			return ct, bytes.ReplaceAll(body, []byte("Disposition: x\r\n"), nil)
		}, "no Disposition"},
	} {
		t.Run(c.name, func(t *testing.T) {
			host, port := fakePartner(t, func(w http.ResponseWriter, r *http.Request) {
				io.Copy(io.Discard, r.Body)
				ct, body := c.answer()
				if ct != "" {
					w.Header().Set("Content-Type", ct)
				}
				w.WriteHeader(c.status)
				w.Write(body)
			})
			_, err := sendAS2(t, e.actionOpts("PLAIN", map[string]any{"hostName": host, "targetPortNumber": port}), edi, nil)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("err = %v, want containing %q", err, c.want)
			}
		})
	}
}

// A receipt that is signed by another than the partner is refused.
func TestAS2ReceiptSignedByAStranger(t *testing.T) {
	e := newAS2Env(t)
	ep := startAS2(t, e.sourceOpts(map[string]any{"signingPrivateKey": e.stranger.pem(true)}), nil)
	_, err := sendAS2(t, e.actionOpts("SIGNED", nil), edi, nil)
	if err == nil || !strings.Contains(err.Error(), "the MDN") || !strings.Contains(err.Error(), "not trusted") {
		t.Errorf("err = %v", err)
	}
	ep.take(t) // the message itself was received
}

func TestAS2Endpoint(t *testing.T) {
	e := newAS2Env(t)
	startAS2(t, e.sourceOpts(nil), nil)
	base := "http://127.0.0.1:" + strconv.Itoa(e.port)
	post := func(path string, h map[string]string, body string) *http.Response {
		req, _ := http.NewRequest("POST", base+path, strings.NewReader(body))
		for k, v := range h {
			req.Header[k] = []string{v}
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { resp.Body.Close() })
		return resp
	}
	if resp, _ := http.Get(base + "/as2"); resp.StatusCode != 405 || resp.Header.Get("Allow") != "POST" {
		t.Errorf("GET: %v", resp.Status)
	}
	if resp := post("/other", map[string]string{"AS2-From": "a", "AS2-To": "b"}, "x"); resp.StatusCode != 404 {
		t.Errorf("another path: %v", resp.Status)
	}
	if resp := post("/as2", nil, "x"); resp.StatusCode != 400 {
		t.Errorf("without AS2 headers: %v", resp.Status)
	}
	// Without Disposition-Notification-To the answer is empty.
	resp := post("/as2", map[string]string{"AS2-From": "a", "AS2-To": "b", "Content-Type": "application/edifact"}, "UNB")
	if data, _ := io.ReadAll(resp.Body); resp.StatusCode != 200 || len(data) != 0 {
		t.Errorf("no receipt wanted: %v %q", resp.Status, data)
	}
	// An asynchronous receipt is asked for: it comes in the response anyway.
	resp = post("/as2", map[string]string{"AS2-From": "a", "AS2-To": "b", "Message-ID": "<m1>", "Content-Type": "application/edifact", "Disposition-Notification-To": "a", "Receipt-Delivery-Option": "http://a.example/mdn"}, "UNB")
	data, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(data), "processed") || resp.Header.Get("AS2-From") != "b" || resp.Header.Get("AS2-To") != "a" || !strings.HasPrefix(resp.Header.Get("Content-Type"), "multipart/report") {
		t.Errorf("receipt: %v %s %v", resp.Status, data, resp.Header)
	}
}

func TestAS2PatternsAndOptions(t *testing.T) {
	s := &as2Source{pattern: "/in/*"}
	for path, want := range map[string]bool{"/in/a": true, "/in/": true, "/in": false, "/out": false} {
		if s.matches(path) != want {
			t.Errorf("/in/* matches %q: %v", path, !want)
		}
	}
	s.pattern = "*"
	if !s.matches("/anything/at/all") {
		t.Error("* does not match everything")
	}
	s.pattern = "/exact"
	if !s.matches("/exact") || s.matches("/exact/more") {
		t.Error("an exact pattern")
	}

	e := newAS2Env(t)
	wantInvalid(t, stepdef.Source, "as2", map[string]any{"as2MessageStructure": "SIGNED_ENCRYPTED"}, "option decryptingPrivateKey: required")
	wantInvalid(t, stepdef.Source, "as2", map[string]any{"as2MessageStructure": "SIGNED_AND_SEALED"}, "option as2MessageStructure")
	wantInvalid(t, stepdef.Source, "as2", map[string]any{"serverPortNumber": 70000}, "is not a port")
	wantInvalid(t, stepdef.Source, "as2", map[string]any{"serverIdentityFile": "nope.p12"}, "server identity")
	mustProcessor(t, stepdef.Source, "as2", map[string]any{"messageStructure": "SIGNED", "keyAlias": "x", "alias": "y", "flowNameAsEndpoint": true})

	wantInvalid(t, stepdef.Action, "as2", map[string]any{"as2From": "a", "as2To": "b"}, "missing required option hostName")
	wantInvalid(t, stepdef.Action, "as2", map[string]any{"hostName": "h"}, "missing required option as2From")
	ok := map[string]any{"hostName": "h.example", "as2From": "a", "as2To": "b"}
	with := func(k string, v any) map[string]any {
		o := map[string]any{}
		for kk, vv := range ok {
			o[kk] = vv
		}
		o[k] = v
		return o
	}
	wantInvalid(t, stepdef.Action, "as2", with("as2MessageStructure", "SIGNED"), "option certificateForSigning: required with as2MessageStructure SIGNED")
	wantInvalid(t, stepdef.Action, "as2", with("as2MessageStructure", "ENCRYPTED"), "option certificateForEncrypt: required")
	wantInvalid(t, stepdef.Action, "as2", with("signingAlgorithm", "MD5WITHRSA"), "option signingAlgorithm")
	wantInvalid(t, stepdef.Action, "as2", with("encryptingAlgorithm", "RC2_CBC"), "option encryptingAlgorithm")
	wantInvalid(t, stepdef.Action, "as2", with("hostName", "https://h.example"), "without http:// or https://")
	wantInvalid(t, stepdef.Action, "as2", with("hostName", "h.example/path"), "option hostName")
	wantInvalid(t, stepdef.Action, "as2", with("mdn", "async"), "mdn")
	p := mustProcessor(t, stepdef.Action, "as2", with("targetPortNumber", "443")).(*as2Action)
	if p.target != "https://h.example:443/" {
		t.Errorf("target = %s", p.target)
	}
	p = mustProcessor(t, stepdef.Action, "as2", map[string]any{"hostName": "h.example", "port": "8080", "uri": "/test/inbound_as2", "as2From": "a", "as2To": "b"}).(*as2Action)
	if p.target != "http://h.example:8080/test/inbound_as2" {
		t.Errorf("target = %s", p.target)
	}
	_ = e
}

func TestAS2KeySources(t *testing.T) {
	e := newAS2Env(t)
	dir := t.TempDir()
	certFile, keyFile := e.partner.writePEM(t, dir, "partner")
	both := filepath.Join(dir, "both.pem")
	os.WriteFile(both, []byte(e.partner.pem(true)), 0o600)

	// from a file, and from an address
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/private_keys/1" || r.URL.Query().Get("key") != "privateKey" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(e.partner.pem(true)))
	}))
	defer srv.Close()
	ep := startAS2(t, e.sourceOpts(map[string]any{
		"decryptingPrivateKey": srv.URL + "/private_keys/1?version=0&key=privateKey",
		"signingPrivateKey":    keyFile, "signingCertificateChain": certFile,
		"validateSigningCertificateChain": e.sender.pem(false),
	}), nil)
	if _, err := sendAS2(t, e.actionOpts("SIGNED_ENCRYPTED", map[string]any{"certificateForEncrypt": certFile}), edi, nil); err != nil {
		t.Fatal(err)
	}
	ep.take(t)

	// problems: an address that is not there, text that is no key, an encrypted key
	e2 := e
	e2.port = freePort(t)
	startAS2(t, e2.sourceOpts(map[string]any{"decryptingPrivateKey": srv.URL + "/missing?key=privateKey&tenant=secret"}), nil)
	_, err := sendAS2(t, e2.actionOpts("ENCRYPTED", nil), edi, nil)
	if err == nil || !strings.Contains(err.Error(), "500") {
		t.Errorf("a source with unreadable keys: err = %v", err)
	}
	for ref, want := range map[string]string{
		"just words": "no such file",
		"-----BEGIN ENCRYPTED PRIVATE KEY-----\nAAAA\n-----END ENCRYPTED PRIVATE KEY-----": "encrypted private key",
		"-----BEGIN CERTIFICATE-----\nAAAA\n-----END CERTIFICATE-----":                     "certificate",
		"-----BEGIN PRIVATE KEY-----\nAAAA\n-----END PRIVATE KEY-----":                     "private key",
	} {
		if _, err := loadKeyMaterial(context.Background(), ref, "", http.DefaultClient); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err = %v, want %q", ref, err, want)
		}
	}
	if _, err := loadKeyMaterial(context.Background(), srv.URL+"/nothing?key=SECRET", "", http.DefaultClient); err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Errorf("the query is in the error: %v", err)
	}
	m, err := loadKeyMaterial(context.Background(), both, "", http.DefaultClient)
	if err != nil || m.key == nil || m.ownCertificate() == nil {
		t.Errorf("a PEM file with both: %+v, %v", m, err)
	}
}

func TestAS2Structures2(t *testing.T) {
	for in, want := range map[string]as2Structure{
		"": {}, "plain": {}, "SIGNED": {sign: true}, "Encrypted": {encrypt: true}, "SIGNED_ENCRYPTED": {sign: true, encrypt: true},
		"PLAIN_COMPRESSED": {compress: true}, "ENCRYPTED_COMPRESSED_SIGNED": {sign: true, encrypt: true, compress: true},
	} {
		if got, err := parseAS2Structure(in); err != nil || got != want {
			t.Errorf("%q: %+v, %v", in, got, err)
		}
	}
	for in, want := range map[string]string{"sha256withrsa": "sha-256", "SHA-1": "sha-1", "SHA512WithRSAEncryption": "sha-512"} {
		if h, err := as2Hash(in); err != nil || h.micalg != want {
			t.Errorf("%q: %v, %v", in, h.micalg, err)
		}
	}
	for in, want := range map[string]int{"aes128_cbc": 16, "AES256": 32, "3DES": 24, "DES_EDE3_CBC": 24} {
		if c, err := as2Cipher(in); err != nil || c.keyLen != want {
			t.Errorf("%q: %v, %v", in, c.keyLen, err)
		}
	}
}

func TestAS2ParallelMessages(t *testing.T) {
	e := newAS2Env(t)
	ep := startAS2(t, e.sourceOpts(nil), nil)
	p := mustProcessor(t, stepdef.Action, "as2", e.actionOpts("SIGNED_ENCRYPTED", nil)).(stepdef.ActionProcessor)
	var wg sync.WaitGroup
	for i := range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := p.Process(context.Background(), message.New(fmt.Sprintf("document %d", i))); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	seen := map[string]bool{}
	for range 6 {
		seen[ep.take(t)[message.Body].(string)] = true
	}
	if len(seen) != 6 {
		t.Errorf("documents = %v", seen)
	}
}

// ---- against OpenSSL: the MIME and CMS of the wire, read by software that is not ours

// recorder is a partner that keeps the request it gets and answers with nothing.
type recorder struct {
	mu      sync.Mutex
	headers http.Header
	body    []byte
}

func newRecorder(t *testing.T) (*recorder, string, string) {
	t.Helper()
	rec := &recorder{}
	host, port := fakePartner(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		rec.mu.Lock()
		rec.headers, rec.body = r.Header.Clone(), body
		rec.mu.Unlock()
	})
	return rec, host, port
}

func TestAS2SentMessagesAreReadByOpenSSL(t *testing.T) {
	needOpenSSL(t)
	e := newAS2Env(t)
	dir := t.TempDir()
	senderCrt, _ := e.sender.writePEM(t, dir, "sender")
	partnerCrt, partnerKey := e.partner.writePEM(t, dir, "partner")
	payload := "Content-Type: application/edifact\r\nContent-Transfer-Encoding: binary\r\nContent-Disposition: attachment; filename=desadv.edi\r\n\r\n" + edi

	for _, structure := range []string{"SIGNED", "SIGNED_ENCRYPTED", "ENCRYPTED"} {
		t.Run(structure, func(t *testing.T) {
			rec, host, port := newRecorder(t)
			opts := e.actionOpts(structure, map[string]any{"hostName": host, "targetPortNumber": port, "mdn": "none", "encryptingAlgorithm": "AES256_CBC"})
			if _, err := sendAS2(t, opts, edi, map[string]any{FileName: "desadv.edi"}); err != nil {
				t.Fatal(err)
			}
			h, body := rec.headers, rec.body
			if h.Get("AS2-From") != "SENDER" || h.Get("AS2-To") != "RECEIVER" || h.Get("Subject") != "Invoice" || h.Get("AS2-Version") != "1.2" ||
				!strings.HasPrefix(h.Get("Message-ID"), "<dif-") || h.Get("Mime-Version") != "1.0" {
				t.Errorf("headers = %v", h)
			}
			// what the HTTP body is
			entity := "Content-Type: " + h.Get("Content-Type") + "\r\n\r\n" + string(body)
			if strings.HasPrefix(structure, "SIGNED_ENC") || structure == "ENCRYPTED" {
				if ct := h.Get("Content-Type"); !strings.Contains(ct, "enveloped-data") {
					t.Fatalf("content type = %s", ct)
				}
				encFile := filepath.Join(dir, structure+".der")
				os.WriteFile(encFile, body, 0o600)
				entity = string(openssl(t, "cms", "-decrypt", "-inform", "DER", "-in", encFile, "-recip", partnerCrt, "-inkey", partnerKey, "-binary"))
			}
			if structure == "ENCRYPTED" {
				if entity != payload {
					t.Errorf("openssl decrypted\n%q\nwant\n%q", entity, payload)
				}
				return
			}
			// signed: openssl verifies the multipart/signed
			signedFile, outFile := filepath.Join(dir, structure+".eml"), filepath.Join(dir, structure+".out")
			os.WriteFile(signedFile, []byte(entity), 0o600)
			out := openssl(t, "smime", "-verify", "-in", signedFile, "-CAfile", senderCrt, "-binary", "-out", outFile, "-purpose", "any")
			if !strings.Contains(string(out), "successful") {
				t.Errorf("openssl: %s", out)
			}
			if got, _ := os.ReadFile(outFile); string(got) != payload {
				t.Errorf("openssl verified\n%q\nwant\n%q", got, payload)
			}
			// ... and not for another signer
			other, _ := e.stranger.writePEM(t, dir, "stranger")
			if bad, err := exec.Command("openssl", "smime", "-verify", "-in", signedFile, "-CAfile", other, "-binary", "-out", os.DevNull, "-purpose", "any").CombinedOutput(); err == nil {
				t.Errorf("openssl verified it against a stranger's certificate: %s", bad)
			}
		})
	}
}

func TestAS2ReceivesMessagesFromOpenSSL(t *testing.T) {
	needOpenSSL(t)
	e := newAS2Env(t)
	dir := t.TempDir()
	senderCrt, senderKey := e.sender.writePEM(t, dir, "sender")
	partnerCrt, _ := e.partner.writePEM(t, dir, "partner")
	payload := filepath.Join(dir, "payload.eml")
	os.WriteFile(payload, []byte("Content-Type: application/edifact\r\nContent-Transfer-Encoding: binary\r\n\r\n"+edi), 0o600)

	signed := filepath.Join(dir, "signed.eml")
	openssl(t, "smime", "-sign", "-in", payload, "-signer", senderCrt, "-inkey", senderKey, "-md", "sha256", "-binary", "-outform", "SMIME", "-out", signed)
	enc := filepath.Join(dir, "encrypted.der")
	openssl(t, "cms", "-encrypt", "-in", signed, "-binary", "-aes128", "-outform", "DER", "-out", enc, partnerCrt)
	encOnly := filepath.Join(dir, "encrypted-only.der")
	openssl(t, "cms", "-encrypt", "-in", payload, "-binary", "-des3", "-outform", "DER", "-out", encOnly, partnerCrt)

	signedRaw, _ := os.ReadFile(signed)
	topHeaders, signedBody, _ := strings.Cut(string(signedRaw), "\n\n")
	signedType := ""
	for _, line := range strings.Split(topHeaders, "\n") {
		if strings.HasPrefix(line, "Content-Type:") {
			signedType = strings.TrimSpace(strings.TrimPrefix(line, "Content-Type:"))
		}
	}
	// a folded Content-Type
	if i := strings.Index(topHeaders, "Content-Type:"); i >= 0 && strings.Contains(topHeaders[i:], "\n\t") || strings.Contains(topHeaders, "\n ") {
		signedType = strings.Join(strings.Fields(strings.ReplaceAll(topHeaders[strings.Index(topHeaders, "Content-Type:")+13:], "\n", " ")), " ")
		if j := strings.Index(signedType, "MIME-Version"); j >= 0 {
			signedType = strings.TrimSpace(signedType[:j])
		}
	}
	encRaw, _ := os.ReadFile(enc)
	encOnlyRaw, _ := os.ReadFile(encOnly)

	for _, c := range []struct {
		name, contentType string
		body              []byte
		structure         string
		signed, encrypted bool
	}{
		{"signed", signedType, []byte(signedBody), "SIGNED", true, false},
		{"signed and encrypted", `application/pkcs7-mime; smime-type=enveloped-data; name=smime.p7m`, encRaw, "SIGNED_ENCRYPTED", true, true},
		{"encrypted", `application/pkcs7-mime; smime-type=enveloped-data; name=smime.p7m`, encOnlyRaw, "ENCRYPTED", false, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			e := e
			e.port = freePort(t)
			ep := startAS2(t, e.sourceOpts(map[string]any{"as2MessageStructure": c.structure}), nil)
			req, _ := http.NewRequest("POST", "http://127.0.0.1:"+strconv.Itoa(e.port)+"/as2", bytes.NewReader(c.body))
			req.Header.Set("Content-Type", c.contentType)
			setRaw(req.Header, "AS2-From", "OPENSSL")
			setRaw(req.Header, "AS2-To", "DIF")
			setRaw(req.Header, "Message-ID", "<openssl-1@example.com>")
			setRaw(req.Header, "Disposition-Notification-To", "openssl")
			setRaw(req.Header, "Disposition-Notification-Options", "signed-receipt-protocol=required, pkcs7-signature; signed-receipt-micalg=required, sha-256")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			defer resp.Body.Close()
			answer, _ := io.ReadAll(resp.Body)
			got := ep.take(t)
			if got[message.Body] != edi || got["AS2-Signed"] != c.signed || got["AS2-Encrypted"] != c.encrypted || got["AS2-From"] != "OPENSSL" {
				t.Errorf("message = %v", got)
			}

			// The receipt is a signed multipart/report that openssl verifies against the partner's certificate.
			if !strings.HasPrefix(resp.Header.Get("Content-Type"), "multipart/signed") {
				t.Fatalf("the receipt is %s: %s", resp.Header.Get("Content-Type"), answer)
			}
			mdn := filepath.Join(dir, strings.ReplaceAll(c.name, " ", "-")+".mdn")
			os.WriteFile(mdn, []byte("Content-Type: "+resp.Header.Get("Content-Type")+"\r\n\r\n"+string(answer)), 0o600)
			out := openssl(t, "smime", "-verify", "-in", mdn, "-CAfile", partnerCrt, "-binary", "-purpose", "any")
			if !strings.Contains(string(out), "message/disposition-notification") || !strings.Contains(string(out), "Disposition: automatic-action/MDN-sent-automatically; processed") ||
				!strings.Contains(string(out), "Original-Message-ID: <openssl-1@example.com>") {
				t.Errorf("the receipt, as openssl verified it: %s", out)
			}
		})
	}
}
