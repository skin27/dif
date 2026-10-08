package impl

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
	"github.com/emersion/go-sasl"

	"dif/keystore"
	"dif/message"
	stepdef "dif/steps/definition"
)

// imapTestServer is an IMAP server over TLS with one user, whose password is
// pass and whose OAuth2 access token is token (XOAUTH2).
type imapTestServer struct {
	addr  string
	user  *imapmemserver.User
	token string
}

const imapUser, imapPass = "ann@example.com", "app-password"

// oauthSession gives the session of the in-memory server the SASL mechanism XOAUTH2.
type oauthSession struct {
	imapserver.Session
	srv *imapTestServer
}

func (s oauthSession) AuthenticateMechanisms() []string { return []string{"PLAIN", "XOAUTH2"} }

func (s oauthSession) Authenticate(mech string) (sasl.Server, error) {
	switch mech {
	case "PLAIN":
		return sasl.NewPlainServer(func(_, user, pass string) error { return s.Login(user, pass) }), nil
	case "XOAUTH2":
		return xoauthServer{s}, nil
	}
	return nil, imapserver.ErrAuthFailed
}

type xoauthServer struct{ s oauthSession }

func (x xoauthServer) Next(resp []byte) ([]byte, bool, error) {
	want := "user=" + imapUser + "\x01auth=Bearer " + x.s.srv.token + "\x01\x01"
	if string(resp) != want {
		return nil, false, imapserver.ErrAuthFailed
	}
	return nil, true, x.s.Login(imapUser, imapPass)
}

func newIMAPTestServer(t *testing.T) *imapTestServer {
	t.Helper()
	cert, err := keystore.LoadIdentity(testIdentity, testPassword)
	if err != nil {
		t.Fatal(err)
	}
	mem := imapmemserver.New()
	user := imapmemserver.NewUser(imapUser, imapPass)
	if err := user.Create("INBOX", nil); err != nil {
		t.Fatal(err)
	}
	if err := user.Create("Archive", nil); err != nil {
		t.Fatal(err)
	}
	mem.AddUser(user)
	s := &imapTestServer{user: user, token: "ya29.token-1"}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	tlsLn := tls.NewListener(ln, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12})
	srv := imapserver.New(&imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return oauthSession{mem.NewSession(), s}, nil, nil
		},
		Caps: imap.CapSet{imap.CapIMAP4rev1: {}},
	})
	go srv.Serve(tlsLn)
	t.Cleanup(func() { srv.Close() })
	s.addr = ln.Addr().String()
	return s
}

type testLiteral struct{ *bytes.Reader }

func (l testLiteral) Size() int64 { return int64(l.Reader.Len()) }

// add puts an email in a mailbox; seen marks it as seen.
func (s *imapTestServer) add(t *testing.T, mailbox, subject, from, body string, seen bool) {
	t.Helper()
	raw := fmt.Sprintf("From: %s\r\nTo: ann@example.com\r\nSubject: %s\r\nDate: Tue, 21 Mar 2023 10:30:05 +0100\r\nMessage-ID: <%s@example.com>\r\n\r\n%s\r\n", from, subject, strings.ReplaceAll(subject, " ", "."), body)
	opts := &imap.AppendOptions{}
	if seen {
		opts.Flags = []imap.Flag{imap.FlagSeen}
	}
	if _, err := s.user.Append(mailbox, testLiteral{bytes.NewReader([]byte(raw))}, opts); err != nil {
		t.Fatal(err)
	}
}

func (s *imapTestServer) opts(extra map[string]any) map[string]any {
	o := map[string]any{
		"username": imapUser, "password": imapPass, "initialDelay": 0, "delay": 10, "socketTimeout": 3000,
		"trustStoreFile": testTrustStore, "trustStorePassword": testPassword,
	}
	for k, v := range extra {
		o[k] = v
	}
	return o
}

func (s *imapTestServer) source(t *testing.T, extra map[string]any) stepdef.CompletionSourceProcessor {
	t.Helper()
	return mustProcessor(t, stepdef.Source, "imaps:"+s.addr, s.opts(extra)).(stepdef.CompletionSourceProcessor)
}

func TestIMAPSSourceTakesUnseenEmailsOnce(t *testing.T) {
	s := newIMAPTestServer(t)
	s.add(t, "INBOX", "first", "bob@example.com", "one", false)
	s.add(t, "INBOX", "already read", "bob@example.com", "old", true)
	s.add(t, "INBOX", "second", "bob@example.com", "two", false)
	s.add(t, "Archive", "elsewhere", "bob@example.com", "archived", false)

	r := startRemote(t, s.source(t, nil), nil)
	got := r.take(t, 2)
	if got[0][message.Body] != "one\r\n" || got[1][message.Body] != "two\r\n" {
		t.Errorf("bodies = %q, %q, want the unseen emails, oldest first", got[0][message.Body], got[1][message.Body])
	}
	if got[0]["Subject"] != "first" || got[0]["From"] != "bob@example.com" || got[0]["mail.messageId"] != "first@example.com" ||
		got[0]["Date"] != "Tue, 21 Mar 2023 10:30:05 +0100" || got[0]["mail.uid"] == nil {
		t.Errorf("headers = %v", got[0])
	}
	// They are marked as seen: nothing comes again. A new email comes.
	time.Sleep(100 * time.Millisecond)
	s.add(t, "INBOX", "third", "bob@example.com", "three", false)
	if more := r.take(t, 1); more[0]["Subject"] != "third" {
		t.Errorf("next email = %v, want the new one", more[0]["Subject"])
	}
	r.stop(t)
	if n := len(r.msgs); n != 0 {
		t.Errorf("%d emails came again", n)
	}
}

func TestIMAPSSourceFetchSizeAndSearch(t *testing.T) {
	s := newIMAPTestServer(t)
	for i := 1; i <= 5; i++ {
		s.add(t, "INBOX", fmt.Sprintf("Invoice %d", i), "billing@shop.example", "pay", false)
	}
	s.add(t, "INBOX", "Greetings", "bob@example.com", "hello there", false)
	s.add(t, "INBOX", "Other invoice", "eve@example.com", "pay", false)

	// fetchSize 2: the two oldest that match, then the next two.
	r := startRemote(t, s.source(t, map[string]any{"fetchSize": 2, "searchSubject": "Invoice", "searchFrom": "billing@shop"}), nil)
	var subjects []string
	for _, m := range r.take(t, 5) {
		subjects = append(subjects, m["Subject"].(string))
	}
	r.stop(t)
	if strings.Join(subjects, ",") != "Invoice 1,Invoice 2,Invoice 3,Invoice 4,Invoice 5" {
		t.Errorf("subjects = %v", subjects)
	}
	if n := len(r.msgs); n != 0 {
		t.Errorf("%d more emails than match", n)
	}

	r = startRemote(t, s.source(t, map[string]any{"searchBody": "hello", "unseen": true}), nil)
	if m := r.take(t, 1); m[0]["Subject"] != "Greetings" {
		t.Errorf("by body: %v", m[0]["Subject"])
	}
	r.stop(t)
}

func TestIMAPSSourceLeavesAFailedEmailUnseen(t *testing.T) {
	s := newIMAPTestServer(t)
	s.add(t, "INBOX", "poison", "bob@example.com", "x", false)
	fails := 0
	r := startRemote(t, s.source(t, nil), func(m message.Message) error {
		if fails++; fails <= 2 {
			return fmt.Errorf("flow failed")
		}
		return nil
	})
	got := r.take(t, 3) // twice failed, and once processed
	r.stop(t)
	if got[2]["Subject"] != "poison" {
		t.Fatalf("got %v", got)
	}
	time.Sleep(50 * time.Millisecond)
	if n := len(r.msgs); n != 0 {
		t.Errorf("%d emails after it was processed", n)
	}
}

func TestIMAPSSourceContentBoth(t *testing.T) {
	s := newIMAPTestServer(t)
	raw := "From: bob@example.com\r\nSubject: with file\r\nContent-Type: multipart/mixed; boundary=XX\r\n\r\n--XX\r\nContent-Type: text/plain\r\n\r\nSee attached\r\n--XX\r\nContent-Type: application/pdf\r\nContent-Disposition: attachment; filename=r.pdf\r\nContent-Transfer-Encoding: base64\r\n\r\nUERGLTEuNA==\r\n--XX--\r\n"
	if _, err := s.user.Append("INBOX", testLiteral{bytes.NewReader([]byte(raw))}, &imap.AppendOptions{}); err != nil {
		t.Fatal(err)
	}
	r := startRemote(t, s.source(t, map[string]any{"content": "both"}), nil)
	m := r.take(t, 1)[0]
	r.stop(t)
	if b, _ := m["attachment.r.pdf"].([]byte); string(b) != "PDF-1.4" || m["attachments"] != "r.pdf" || strings.TrimSpace(m[message.Body].(string)) != "See attached" {
		t.Errorf("message = %v", m)
	}
}

func TestIMAPSSourceFolder(t *testing.T) {
	s := newIMAPTestServer(t)
	s.add(t, "INBOX", "in the inbox", "bob@example.com", "x", false)
	s.add(t, "Archive", "in the archive", "bob@example.com", "y", false)
	r := startRemote(t, s.source(t, map[string]any{"folderName": "Archive"}), nil)
	if m := r.take(t, 1); m[0]["Subject"] != "in the archive" {
		t.Errorf("subject = %v", m[0]["Subject"])
	}
	r.stop(t)
}

func TestIMAPSSourceLogsAFailedPollAndKeepsPolling(t *testing.T) {
	s := newIMAPTestServer(t)
	s.add(t, "INBOX", "waiting", "bob@example.com", "x", false)
	var logged lockedBuffer
	ctx := stepdef.WithLogger(context.Background(), log.New(&logged, "", 0))

	// Wrong password, then a folder that is not there.
	r := startRemoteCtx(ctx, s.source(t, map[string]any{"password": "wrong", "delay": 200}), nil)
	waitForLog(t, &logged, "imaps 127.0.0.1")
	if strings.Contains(logged.String(), "wrong") {
		t.Errorf("the log shows the password: %s", logged.String())
	}
	r.stop(t)
	if len(r.msgs) != 0 {
		t.Error("an email came with a wrong password")
	}
	r = startRemoteCtx(ctx, s.source(t, map[string]any{"folderName": "Nowhere", "delay": 200}), nil)
	waitForLog(t, &logged, `folder "Nowhere"`)
	r.stop(t)

	// A server that is not there.
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := l.Addr().String()
	l.Close()
	down := mustProcessor(t, stepdef.Source, "imaps:"+addr, s.opts(nil)).(stepdef.CompletionSourceProcessor)
	logged = lockedBuffer{}
	r = startRemoteCtx(ctx, down, nil)
	waitForLog(t, &logged, "imaps 127.0.0.1")
	r.stop(t)
}

func TestIMAPSSourceOAuth(t *testing.T) {
	s := newIMAPTestServer(t)
	s.add(t, "INBOX", "for the token", "bob@example.com", "x", false)
	tenant := t.Name()
	tenantVariables.set(tenant, "GMailToken", "stale-token")
	t.Cleanup(func() { tenantVariables.remove(tenant, "GMailToken") })

	var logged lockedBuffer
	ctx := stepdef.WithLogger(context.Background(), log.New(&logged, "", 0))
	src := mustProcessor(t, stepdef.Source, "imaps:"+s.addr, s.opts(map[string]any{
		"authenticationType": "oauth", "accessToken": "@{GMailToken}", "tenantDbName": tenant, "password": nil,
	})).(stepdef.CompletionSourceProcessor)
	r := startRemoteCtx(ctx, src, nil)

	// The token is looked up for every poll: while it is stale the server
	// refuses it, when an oauth2token step renews it the email comes.
	waitForLog(t, &logged, "imaps 127.0.0.1")
	if len(r.msgs) != 0 {
		t.Error("an email came with a stale token")
	}
	tenantVariables.set(tenant, "GMailToken", s.token)
	if m := r.take(t, 1); m[0]["Subject"] != "for the token" {
		t.Errorf("subject = %v", m[0]["Subject"])
	}
	r.stop(t)

	// A token variable that is not set is a failed poll too.
	logged = lockedBuffer{}
	src = mustProcessor(t, stepdef.Source, "imaps:"+s.addr, s.opts(map[string]any{
		"authenticationType": "oauth", "accessToken": "@{OauthTokenNotAvailable}", "tenantDbName": tenant,
	})).(stepdef.CompletionSourceProcessor)
	r = startRemoteCtx(ctx, src, nil)
	waitForLog(t, &logged, `tenant variable "OauthTokenNotAvailable"`)
	r.stop(t)
}

func TestIMAPSXOAuth2(t *testing.T) {
	mech, ir, err := imapXOAuth2{"ann@example.com", "tok"}.Start()
	if err != nil || mech != "XOAUTH2" || string(ir) != "user=ann@example.com\x01auth=Bearer tok\x01\x01" {
		t.Errorf("Start = %q %q %v", mech, ir, err)
	}
	if _, err := (imapXOAuth2{}).Next([]byte(`{"status":"401"}`)); err == nil || !strings.Contains(err.Error(), `{"status":"401"}`) {
		t.Errorf("Next: %v", err)
	}
}

func TestIMAPSSourcePasswordFromTheEnvironment(t *testing.T) {
	s := newIMAPTestServer(t)
	s.add(t, "INBOX", "env", "bob@example.com", "x", false)
	t.Setenv("DIF_IMAPS_PASSWORD", imapPass)
	r := startRemote(t, s.source(t, map[string]any{"password": nil}), nil)
	if m := r.take(t, 1); m[0]["Subject"] != "env" {
		t.Errorf("subject = %v", m[0]["Subject"])
	}
	r.stop(t)
}

func TestIMAPSSourceInvalidOptions(t *testing.T) {
	ok := map[string]any{"username": "u", "password": "p"}
	with := func(k string, v any) map[string]any {
		o := map[string]any{}
		for kk, vv := range ok {
			o[kk] = vv
		}
		o[k] = v
		return o
	}
	mustProcessor(t, stepdef.Source, "imaps:imap.gmail.com:993", ok)
	mustProcessor(t, stepdef.Source, "imaps:imap.gmail.com", ok)
	mustProcessor(t, stepdef.Source, "imaps://[::1]:993", ok)
	wantInvalid(t, stepdef.Source, "imaps:", ok, "missing required option path")
	wantInvalid(t, stepdef.Source, "imaps:user@host", ok, "uri: want imaps:<host>[:<port>]")
	wantInvalid(t, stepdef.Source, "imaps:host", map[string]any{"password": "p"}, "missing required option username")
	wantInvalid(t, stepdef.Source, "imaps:host", with("authenticationType", "oauth"), "option accessToken: required with authenticationType oauth")
	wantInvalid(t, stepdef.Source, "imaps:host", with("authenticationType", "kerberos"), "authenticationType")
	wantInvalid(t, stepdef.Source, "imaps:host", with("content", "all"), "content")
	wantInvalid(t, stepdef.Source, "imaps:host", with("bridgeErrorHandler", true), "unknown option")

	// The addresses.
	for in, want := range map[string]string{"imap.gmail.com:993": "imap.gmail.com:993", "imap.example.com": "imap.example.com:993", "mail.example.com:1993": "mail.example.com:1993", "//[::1]:993": "[::1]:993"} {
		src := mustProcessor(t, stepdef.Source, "imaps:"+in, ok).(*imapsSource)
		if src.addr != want {
			t.Errorf("%s: addr = %s, want %s", in, src.addr, want)
		}
	}
}
