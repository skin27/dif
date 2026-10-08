package impl

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dif/keystore"
	"dif/message"
	stepdef "dif/steps/definition"
)

const (
	testIdentity   = "../../keystore/testdata/server-identity.p12"
	testTrustStore = "../../keystore/testdata/truststore.p12"
	testPassword   = "changeit"
)

// freeAddr returns a local address with a port that is free right now.
func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

// trustingClient trusts the test server identity.
func trustingClient(t *testing.T) *http.Client {
	t.Helper()
	pool, err := keystore.LoadTrustPool(testTrustStore, testPassword)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{
		Timeout:   5 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}},
	}
}

// serve runs an https source for uri until the test ends (or stop is called)
// and waits until it accepts connections. Run's error is sent on errs.
func serve(t *testing.T, uri string, opts map[string]any, emit stepdef.Emit) (stop func(), errs chan error) {
	t.Helper()
	all := map[string]any{"serverIdentityFile": testIdentity, "serverIdentityPassword": testPassword}
	for k, v := range opts {
		all[k] = v
	}
	src := mustProcessor(t, stepdef.Source, uri, all).(stepdef.SourceProcessor)

	ctx, cancel := context.WithCancel(context.Background())
	errs = make(chan error, 1)
	done := make(chan struct{})
	go func() {
		errs <- src.Run(ctx, emit)
		close(done)
	}()
	stop = func() {
		cancel()
		<-done
	}
	t.Cleanup(stop)
	return stop, errs
}

// waitServing waits until a request to url gets an HTTP response.
func waitServing(t *testing.T, c *http.Client, url string) {
	t.Helper()
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if resp, err := c.Get(url); err == nil {
			resp.Body.Close()
			return
		}
	}
	t.Fatalf("%s is not served", url)
}

func call(t *testing.T, c *http.Client, method, url, body string) (int, string, http.Header) {
	t.Helper()
	req, err := http.NewRequest(method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Test", "yes")
	resp, err := c.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(data), resp.Header
}

// pong replies to every message with "pong <body>" and passes the message to seen.
func pong(seen chan message.Message) stepdef.Emit {
	return func(m message.Message, reply func(message.Message, error)) error {
		if seen != nil {
			seen <- m
		}
		out := message.New("pong " + text(m[message.Body]))
		out["Content-Type"] = "text/x-pong"
		reply(out, nil)
		return nil
	}
}

func TestHTTPSSourceRequestReply(t *testing.T) {
	addr, c := freeAddr(t), trustingClient(t)
	seen := make(chan message.Message, 10)
	serve(t, "https://"+addr+"/in", nil, pong(seen))
	waitServing(t, c, "https://"+addr+"/in")
	<-seen // the probe

	status, body, h := call(t, c, http.MethodPost, "https://"+addr+"/in", "ping")
	if status != 200 || body != "pong ping" || h.Get("Content-Type") != "text/x-pong" {
		t.Errorf("reply = %d %q %q, want 200 \"pong ping\" text/x-pong", status, body, h.Get("Content-Type"))
	}
	m := <-seen
	if m["X-Test"] != "yes" || m[message.TraceID] == nil {
		t.Errorf("message = %v, want the request headers and metadata", m)
	}
	if _, ok := m["http.method"]; ok {
		t.Error("http.method set without preserveHttpHeaders")
	}

	if status, _, _ := call(t, c, http.MethodGet, "https://"+addr+"/other", ""); status != 404 {
		t.Errorf("other path: status %d, want 404", status)
	}
	if status, _, _ := call(t, c, http.MethodGet, "https://"+addr+"/in/sub", ""); status != 404 {
		t.Errorf("sub path without matchPrefix: status %d, want 404", status)
	}
}

func TestHTTPSSourceOptions(t *testing.T) {
	addr, c := freeAddr(t), trustingClient(t)
	seen := make(chan message.Message, 10)
	serve(t, "https://"+addr+"/api", map[string]any{"matchPrefix": true, "preserveHttpHeaders": "true"}, pong(seen))
	waitServing(t, c, "https://"+addr+"/api")
	<-seen

	status, body, _ := call(t, c, http.MethodPut, "https://"+addr+"/api/orders?id=7", "x")
	if status != 200 || body != "pong x" {
		t.Errorf("prefix match: %d %q", status, body)
	}
	m := <-seen
	for k, want := range map[string]string{"http.method": "PUT", "http.path": "/api/orders", "http.query": "id=7", "http.uri": "/api/orders?id=7"} {
		if m[k] != want {
			t.Errorf("%s = %v, want %q", k, m[k], want)
		}
	}
}

func TestHTTPSSourceOneWay(t *testing.T) {
	addr, c := freeAddr(t), trustingClient(t)
	seen := make(chan message.Message, 10)
	emit := func(m message.Message, reply func(message.Message, error)) error {
		if reply != nil {
			t.Error("one-way source waits for a reply")
		}
		seen <- m
		return nil
	}
	serve(t, "https://"+addr+"/in", map[string]any{"exchangePattern": "InOnly", "matchOnUriPrefix": true, "authenticationPreemptive": true}, emit)
	waitServing(t, c, "https://"+addr+"/in")
	<-seen

	status, body, _ := call(t, c, http.MethodPost, "https://"+addr+"/in/sub", "ping")
	if status != 200 || body != "ping" {
		t.Errorf("reply = %d %q, want 200 \"ping\" (the request)", status, body)
	}
	if m := <-seen; m[message.Body] != "ping" {
		t.Errorf("message body = %v, want ping", m[message.Body])
	}
}

func TestHTTPSSourceErrors(t *testing.T) {
	addr, c := freeAddr(t), trustingClient(t)
	serve(t, "https://"+addr+"/fail", nil, func(m message.Message, reply func(message.Message, error)) error {
		reply(nil, errors.New("step x: boom"))
		return nil
	})
	serve(t, "https://"+addr+"/stopping", nil, func(message.Message, func(message.Message, error)) error {
		return errors.New("flow is stopping")
	})
	waitServing(t, c, "https://"+addr+"/fail")

	if status, body, _ := call(t, c, http.MethodPost, "https://"+addr+"/fail", ""); status != 500 || !strings.Contains(body, "step x: boom") {
		t.Errorf("failed message: %d %q, want 500 with the error", status, body)
	}
	if status, _, _ := call(t, c, http.MethodPost, "https://"+addr+"/stopping", ""); status != 503 {
		t.Errorf("stopping flow: %d, want 503", status)
	}
}

func TestHTTPSSourceSharedPort(t *testing.T) {
	addr, c := freeAddr(t), trustingClient(t)
	stopA, _ := serve(t, "https://"+addr+"/a", nil, pong(nil))
	stopB, _ := serve(t, "https://"+addr+"/b", nil, pong(nil))
	waitServing(t, c, "https://"+addr+"/a")
	waitServing(t, c, "https://"+addr+"/b")

	// The same path on the same address belongs to one flow only.
	_, errs := serve(t, "https://"+addr+"/a", nil, pong(nil))
	if err := <-errs; err == nil || !strings.Contains(err.Error(), "path /a on "+addr+" is already served") {
		t.Errorf("duplicate path: err = %v", err)
	}

	// All routes at one address share its certificate.
	other := filepath.Join(t.TempDir(), "other.p12")
	data, err := os.ReadFile(testIdentity)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(other, data, 0o644); err != nil {
		t.Fatal(err)
	}
	_, errs = serve(t, "https://"+addr+"/c", map[string]any{"serverIdentityFile": other}, pong(nil))
	if err := <-errs; err == nil || !strings.Contains(err.Error(), "already serves with server identity") {
		t.Errorf("other identity: err = %v", err)
	}

	stopA()
	if status, body, _ := call(t, c, http.MethodPost, "https://"+addr+"/b", "still"); status != 200 || body != "pong still" {
		t.Errorf("b after a stopped: %d %q", status, body)
	}
	stopB()
	if _, err := c.Get("https://" + addr + "/b"); err == nil {
		t.Error("the server still runs after its last flow stopped")
	}
	httpsServers.Lock()
	defer httpsServers.Unlock()
	if httpsServers.m[addr] != nil {
		t.Error("the server is still registered")
	}
}

func TestHTTPSSourceInvalid(t *testing.T) {
	ok := map[string]any{"serverIdentityFile": testIdentity, "serverIdentityPassword": testPassword}
	with := func(k string, v any) map[string]any {
		m := map[string]any{k: v}
		for k, v := range ok {
			if _, set := m[k]; !set {
				m[k] = v
			}
		}
		return m
	}
	wantInvalid(t, stepdef.Source, "https://localhost:9001/x", with("serverIdentityPassword", "wrong"), "server identity: ")
	wantInvalid(t, stepdef.Source, "https://localhost:9001/x", with("serverIdentityPassword", "wrong"), "wrong password")
	wantInvalid(t, stepdef.Source, "https://localhost:9001/x", with("serverIdentityFile", "missing.p12"), "server identity: open missing.p12")
	wantInvalid(t, stepdef.Source, "https://localhost:9001/x", with("serverIdentityFile", testTrustStore), "holds 0 private keys")
	wantInvalid(t, stepdef.Source, "https:nohost", ok, "want https://host:port/path")
	wantInvalid(t, stepdef.Source, "https://localhost/x", with("exchangePattern", "InOptionalOut"), "exchangePattern")

	// The password falls back to the environment.
	t.Setenv("DIF_SERVER_IDENTITY_PASSWORD", testPassword)
	if _, err := newProcessor(stepdef.Source, "https://localhost/x", map[string]any{"serverIdentityFile": testIdentity}); err != nil {
		t.Errorf("password from the environment: %v", err)
	}
}

func TestSplitAddress(t *testing.T) {
	for in, want := range map[string][2]string{
		"//0.0.0.0:9001/_new2/httpsinbound": {"0.0.0.0:9001", "/_new2/httpsinbound"},
		"//localhost/x":                     {"localhost:443", "/x"},
		"//localhost:8443":                  {"localhost:8443", "/"},
		"//[::1]:9001/x":                    {"[::1]:9001", "/x"},
	} {
		addr, path, err := splitAddress(in)
		if err != nil || addr != want[0] || path != want[1] {
			t.Errorf("splitAddress(%q) = %q, %q, %v; want %q, %q", in, addr, path, err, want[0], want[1])
		}
	}
}

// testServer is a TLS server with the test identity, which the test trust store trusts.
func testServer(t *testing.T, h http.HandlerFunc) *httptest.Server {
	t.Helper()
	cert, err := keystore.LoadIdentity(testIdentity, testPassword)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(h)
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv
}

func newHTTPSActionT(t *testing.T, url string, opts map[string]any) stepdef.ActionProcessor {
	t.Helper()
	all := map[string]any{"trustStoreFile": testTrustStore, "trustStorePassword": testPassword}
	for k, v := range opts {
		all[k] = v
	}
	return mustProcessor(t, stepdef.Action, url, all).(stepdef.ActionProcessor)
}

func TestHTTPSAction(t *testing.T) {
	type request struct {
		method, body string
		header       http.Header
	}
	got := make(chan request, 1)
	srv := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got <- request{r.Method, string(b), r.Header}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(201)
		io.WriteString(w, `{"ok":true}`)
	})

	for _, method := range []string{"GET", "POST"} {
		m := message.New("payload")
		m["X-Custom"] = "1"
		m["Accept-Encoding"] = "gzip" // from an inbound request; not passed on
		m["http.method"] = "PUT"
		m["count"] = 3    // not a string: not sent
		m["bad"] = "a\nb" // not a valid header value: not sent
		m["Content-Type"] = "text/plain"

		out, err := newHTTPSActionT(t, srv.URL+"/orders", map[string]any{"httpMethod": method}).Process(context.Background(), m)
		if err != nil {
			t.Fatal(err)
		}
		r := <-got
		wantBody := "payload"
		if method == "GET" {
			wantBody = ""
		}
		if r.method != method || r.body != wantBody {
			t.Errorf("%s: server got %s %q, want body %q", method, r.method, r.body, wantBody)
		}
		if r.header.Get("X-Custom") != "1" || r.header.Get("Count") != "" || r.header.Get("Bad") != "" ||
			r.header.Get("Http.method") != "" || r.header.Get("Metadata.traceid") != "" {
			t.Errorf("%s: server got headers %v", method, r.header)
		}
		if out[message.Body] != `{"ok":true}` || out["http.status"] != 201 || out["Content-Type"] != "application/json" {
			t.Errorf("%s: message = %v", method, out)
		}
	}
}

func TestHTTPSActionFailures(t *testing.T) {
	srv := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/slow" {
			time.Sleep(300 * time.Millisecond)
		}
		http.Error(w, "nope", http.StatusNotFound)
	})

	// An error status is a normal response unless throwExceptionOnFailure is set.
	out, err := newHTTPSActionT(t, srv.URL+"/x", nil).Process(context.Background(), message.New(nil))
	if err != nil || out["http.status"] != 404 {
		t.Errorf("404 = %v, %v; want http.status 404 and no error", out, err)
	}
	_, err = newHTTPSActionT(t, srv.URL+"/x", map[string]any{"throwExceptionOnFailure": true}).Process(context.Background(), message.New(nil))
	if err == nil || !strings.Contains(err.Error(), "404 Not Found") {
		t.Errorf("throwExceptionOnFailure: err = %v", err)
	}

	_, err = newHTTPSActionT(t, srv.URL+"/slow", map[string]any{"socketTimeout": 50}).Process(context.Background(), message.New(nil))
	if err == nil || !strings.Contains(err.Error(), "Timeout") {
		t.Errorf("timeout: err = %v", err)
	}

	// A server outside the trust store is rejected.
	untrusted := httptest.NewTLSServer(http.NotFoundHandler())
	defer untrusted.Close()
	_, err = newHTTPSActionT(t, untrusted.URL, nil).Process(context.Background(), message.New(nil))
	if err == nil || !strings.Contains(err.Error(), "certificate") {
		t.Errorf("untrusted server: err = %v", err)
	}
}

func TestHTTPSActionInvalid(t *testing.T) {
	trust := map[string]any{"trustStoreFile": testTrustStore, "trustStorePassword": testPassword}
	wantInvalid(t, stepdef.Action, "https://localhost/x", map[string]any{"trustStoreFile": testTrustStore, "trustStorePassword": "wrong"}, "trust store: ")
	wantInvalid(t, stepdef.Action, "https://localhost/x", map[string]any{"trustStoreFile": "missing.p12"}, "trust store: open missing.p12")
	wantInvalid(t, stepdef.Action, "https:nohost", trust, "want https://host[:port]/path")
	wantInvalid(t, stepdef.Action, "https://localhost/x", map[string]any{"httpMethod": "FETCH"}, `option httpMethod: "FETCH" is not one of`)
	wantInvalid(t, stepdef.Action, "https://localhost/x", map[string]any{"socketTimeout": 0}, "option socketTimeout: 0 is less than 1")

	// As a sink, the https step is the action.
	p, err := newProcessor(stepdef.Sink, "https://localhost/x", trust)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := p.(stepdef.ActionProcessor); !ok {
		t.Errorf("sink processor = %T, want the https action", p)
	}
}

func TestKeystorePasswordHint(t *testing.T) {
	for _, env := range []string{"DIF_SERVER_IDENTITY_PASSWORD", "DIF_TRUSTSTORE_PASSWORD"} {
		t.Setenv(env, "") // restored after the test
		os.Unsetenv(env)
	}

	wantInvalid(t, stepdef.Source, "https://localhost/x", map[string]any{"serverIdentityFile": testIdentity},
		"server identity: "+testIdentity+": wrong password or corrupt keystore; no password was given: set the serverIdentityPassword option or the DIF_SERVER_IDENTITY_PASSWORD environment variable")
	wantInvalid(t, stepdef.Action, "https://localhost/x", map[string]any{"trustStoreFile": testTrustStore},
		"trust store: "+testTrustStore+": wrong password or corrupt keystore; no password was given: set the trustStorePassword option or the DIF_TRUSTSTORE_PASSWORD environment variable")

	// A password that was given but is wrong gets no hint.
	_, err := newProcessor(stepdef.Source, "https://localhost/x", map[string]any{"serverIdentityFile": testIdentity, "serverIdentityPassword": "wrong"})
	if err == nil || strings.Contains(err.Error(), "no password was given") {
		t.Errorf("wrong password: err = %v, want no hint", err)
	}
	t.Setenv("DIF_TRUSTSTORE_PASSWORD", "wrong")
	_, err = newProcessor(stepdef.Action, "https://localhost/x", map[string]any{"trustStoreFile": testTrustStore})
	if err == nil || strings.Contains(err.Error(), "no password was given") {
		t.Errorf("wrong password from the environment: err = %v, want no hint", err)
	}
}

// TestHTTPSSourceStopsWithIdleConnection checks that a connection that never
// sent a request (Go clients open such spare connections) does not hold up
// stopping the flow; http.Server.Shutdown alone would wait 5 seconds for it.
func TestHTTPSSourceStopsWithIdleConnection(t *testing.T) {
	addr, c := freeAddr(t), trustingClient(t)
	stop, _ := serve(t, "https://"+addr+"/in", nil, pong(nil))
	waitServing(t, c, "https://"+addr+"/in")

	conn, err := tls.Dial("tcp", addr, c.Transport.(*http.Transport).TLSClientConfig)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	start := time.Now()
	stop()
	if d := time.Since(start); d > time.Second {
		t.Errorf("stopping took %v, want well under a second", d)
	}
}
