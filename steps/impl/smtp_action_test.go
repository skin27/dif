package impl

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/textproto"
	"strings"
	"testing"

	"dif/keystore"
	"dif/message"
	stepdef "dif/steps/definition"
)

// fakeMail is what the fake SMTP server received.
type fakeMail struct {
	auth     string // the AUTH line, decoded
	from     string
	to       []string
	data     string
	tlsAfter bool // the mail came over TLS
}

// fakeSMTP runs an SMTP server that offers STARTTLS (when startTLS) and
// accepts any login; it sends every mail it receives on the channel.
func fakeSMTP(t *testing.T, startTLS bool) (string, chan fakeMail) {
	t.Helper()
	cert, err := keystore.LoadIdentity(testIdentity, testPassword)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	mails := make(chan fakeMail, 10)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go serveFakeSMTP(conn, cert, startTLS, mails)
		}
	}()
	return ln.Addr().String(), mails
}

func serveFakeSMTP(conn net.Conn, cert tls.Certificate, startTLS bool, mails chan fakeMail) {
	defer func() { conn.Close() }()
	tp := textproto.NewConn(conn)
	var m fakeMail
	tp.PrintfLine("220 fake ESMTP")
	for {
		line, err := tp.ReadLine()
		if err != nil {
			return
		}
		verb, arg, _ := strings.Cut(line, " ")
		switch strings.ToUpper(verb) {
		case "EHLO":
			if startTLS && !m.tlsAfter {
				tp.PrintfLine("250-fake\r\n250-STARTTLS\r\n250 AUTH PLAIN XOAUTH2")
			} else {
				tp.PrintfLine("250-fake\r\n250 AUTH PLAIN XOAUTH2")
			}
		case "STARTTLS":
			tp.PrintfLine("220 go ahead")
			tc := tls.Server(conn, &tls.Config{Certificates: []tls.Certificate{cert}})
			if tc.Handshake() != nil {
				return
			}
			conn, tp, m.tlsAfter = tc, textproto.NewConn(tc), true
		case "AUTH":
			mech, resp, _ := strings.Cut(arg, " ")
			dec, _ := base64.StdEncoding.DecodeString(resp)
			m.auth = mech + " " + string(dec)
			tp.PrintfLine("235 ok")
		case "MAIL":
			m.from = arg
			tp.PrintfLine("250 ok")
		case "RCPT":
			m.to = append(m.to, arg)
			tp.PrintfLine("250 ok")
		case "DATA":
			tp.PrintfLine("354 go")
			data, _ := tp.ReadDotBytes()
			m.data = string(data)
			tp.PrintfLine("250 queued")
			mails <- m
		case "QUIT":
			tp.PrintfLine("221 bye")
			return
		default:
			tp.PrintfLine("502 no")
		}
	}
}

func smtpOpts(addr string, extra map[string]any) map[string]any {
	all := map[string]any{"to": "ann@example.com; Bob <bob@example.com>", "username": "me@example.com", "subject": "Hello ü",
		"trustStoreFile": testTrustStore, "trustStorePassword": testPassword}
	for k, v := range extra {
		all[k] = v
	}
	return all
}

func TestSMTPText(t *testing.T) {
	addr, mails := fakeSMTP(t, true)
	p := mustProcessor(t, stepdef.Action, "smtp:"+addr, smtpOpts(addr, map[string]any{"password": "secret", "replyTo": "desk@example.com"})).(stepdef.ActionProcessor)
	m := message.New("Hi there,\nthis is a long line that quoted-printable keeps intact: " + strings.Repeat("x", 80))
	out, err := p.Process(context.Background(), m)
	if err != nil {
		t.Fatal(err)
	}
	if out[message.Body] != m[message.Body] {
		t.Error("the message changed")
	}

	got := <-mails
	if !got.tlsAfter || got.auth != "PLAIN \x00me@example.com\x00secret" || got.from != "FROM:<me@example.com>" ||
		strings.Join(got.to, " ") != "TO:<ann@example.com> TO:<bob@example.com>" {
		t.Errorf("envelope = %+v", got)
	}
	msg, err := mail.ReadMessage(strings.NewReader(got.data))
	if err != nil {
		t.Fatal(err)
	}
	subject, _ := new(mime.WordDecoder).DecodeHeader(msg.Header.Get("Subject"))
	if subject != "Hello ü" || msg.Header.Get("From") != "me@example.com" || msg.Header.Get("Reply-To") != "desk@example.com" ||
		msg.Header.Get("To") != "ann@example.com, Bob <bob@example.com>" || msg.Header.Get("Content-Type") != "text/plain; charset=utf-8" {
		t.Errorf("headers = %v (subject %q)", msg.Header, subject)
	}
	body, _ := io.ReadAll(msg.Body)
	if decoded := qpDecode(t, string(body)); decoded != m[message.Body] {
		t.Errorf("body = %q", decoded)
	}

	// The header subject overrides the option; an access token logs in with XOAUTH2.
	p = mustProcessor(t, stepdef.Action, "smtp:"+addr, smtpOpts(addr, map[string]any{"accessToken": "tok"})).(stepdef.ActionProcessor)
	m = message.New("x")
	m["subject"] = "From the header"
	if _, err := p.Process(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	got = <-mails
	if got.auth != "XOAUTH2 user=me@example.com\x01auth=Bearer tok\x01\x01" || !strings.Contains(got.data, "Subject: From the header") {
		t.Errorf("mail = %+v", got)
	}
}

func qpDecode(t *testing.T, s string) string {
	t.Helper()
	data, err := io.ReadAll(quotedprintable.NewReader(strings.NewReader(s)))
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSuffix(string(data), "\n") // the line end DATA adds
}

func TestSMTPAttachment(t *testing.T) {
	addr, mails := fakeSMTP(t, true)
	p := mustProcessor(t, stepdef.Action, "smtp:"+addr, smtpOpts(addr, map[string]any{"emailBody": "See the order."})).(stepdef.ActionProcessor)
	m := message.New("<order/>")
	m[FileName], m[message.ContentType] = "order.xml", "application/xml"
	if _, err := p.Process(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	got := <-mails
	if got.auth != "" {
		t.Errorf("logged in without a password: %q", got.auth)
	}
	msg, err := mail.ReadMessage(strings.NewReader(got.data))
	if err != nil {
		t.Fatal(err)
	}
	_, params, err := mime.ParseMediaType(msg.Header.Get("Content-Type"))
	if err != nil {
		t.Fatal(err)
	}
	r := multipart.NewReader(msg.Body, params["boundary"])
	var parts []string
	for {
		part, err := r.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(part)
		if part.Header.Get("Content-Transfer-Encoding") == "base64" {
			data, _ = base64.StdEncoding.DecodeString(strings.ReplaceAll(string(data), "\r\n", ""))
		}
		parts = append(parts, part.FileName()+"|"+part.Header.Get("Content-Type")+"|"+strings.TrimSpace(string(data)))
	}
	if strings.Join(parts, "\n") != "|text/plain; charset=utf-8|See the order.\norder.xml|application/xml|<order/>" {
		t.Errorf("parts = %q", parts)
	}
}

func TestSMTPFailures(t *testing.T) {
	addr, _ := fakeSMTP(t, false)
	p := mustProcessor(t, stepdef.Action, "smtp:"+addr, smtpOpts(addr, nil)).(stepdef.ActionProcessor)
	if _, err := p.Process(context.Background(), message.New("x")); err == nil || !strings.Contains(err.Error(), "server does not support STARTTLS") {
		t.Errorf("err = %v, want STARTTLS required", err)
	}

	wantInvalid(t, stepdef.Action, "smtp:mail.example.com", map[string]any{"to": "a@b.c"}, `uri: want smtp:<host>:<port>, got "mail.example.com"`)
	wantInvalid(t, stepdef.Action, "smtps:mail.example.com", map[string]any{"to": "a@b.c"}, "uri: want smtps:")
	wantInvalid(t, stepdef.Action, "smtp:h:25", map[string]any{"to": " ; "}, "option to: no recipients")
	wantInvalid(t, stepdef.Action, "smtp:h:25", map[string]any{"to": "a@b.c"}, "option from: no sender")
	wantInvalid(t, stepdef.Action, "smtp:h:25", map[string]any{"to": "a@b.c", "from": "x@y.z", "exchangeBodyAs": "link"}, "option exchangeBodyAs")
}
