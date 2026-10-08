package impl

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/smtp"
	"net/textproto"
	"strings"
	"time"

	"dif/keystore"
	"dif/message"
	stepdef "dif/steps/definition"
)

// smtpAction sends the message as an email and passes it on unchanged. The
// body is the email's text, or, with emailBody set or exchangeBodyAs
// attachment, an attachment to the text emailBody. A header subject overrides
// the option subject. With scheme smtp the connection must be upgraded with
// STARTTLS; smtps uses TLS from the start. It logs in when a password (PLAIN)
// or access token (XOAUTH2) is set.
type smtpAction struct {
	addr, host  string
	implicitTLS bool
	tls         *tls.Config

	username, password, token string
	from, replyTo, subject    string
	to                        []string
	emailBody, contentType    string
	attach                    bool // the body is an attachment, also when emailBody is empty
	timeout                   time.Duration
}

// newSMTPAction returns the constructor of the smtp step, or with implicitTLS
// of the smtps step.
func newSMTPAction(implicitTLS bool) func(string, stepdef.Params) (stepdef.Processor, error) {
	return func(_ string, p stepdef.Params) (stepdef.Processor, error) {
		return smtpFromParams(implicitTLS, p)
	}
}

func smtpFromParams(implicitTLS bool, p stepdef.Params) (stepdef.Processor, error) {
	scheme := "smtp"
	if implicitTLS {
		scheme = "smtps"
	}
	addr, _ := p["path"].(string)
	host, port, err := net.SplitHostPort(addr)
	if err != nil || host == "" {
		return nil, fmt.Errorf("uri: want %s:<host>:<port>, got %q", scheme, addr)
	}
	a := smtpAction{
		addr:        net.JoinHostPort(host, port),
		host:        host,
		implicitTLS: implicitTLS,
		attach:      p["exchangeBodyAs"] == "attachment",
		username:    p["username"].(string),
		password:    p["password"].(string),
		token:       p["accessToken"].(string),
		from:        p["from"].(string),
		replyTo:     p["replyTo"].(string),
		subject:     p["subject"].(string),
		emailBody:   p["emailBody"].(string),
		contentType: p["contentType"].(string),
		timeout:     time.Duration(p["timeout"].(int)) * time.Millisecond,
	}
	if a.password == "" {
		a.password, _, err = environmentSecret("DIF_SMTP_PASSWORD")
		if err != nil {
			return nil, err
		}
	}
	if a.from == "" {
		a.from = a.username
	}
	for _, addr := range strings.FieldsFunc(p["to"].(string), func(r rune) bool { return r == ',' || r == ';' }) {
		if addr = strings.TrimSpace(addr); addr != "" {
			a.to = append(a.to, addr)
		}
	}
	switch {
	case len(a.to) == 0:
		return nil, fmt.Errorf("option to: no recipients")
	case a.from == "":
		return nil, fmt.Errorf("option from: no sender (set from or username)")
	}

	var pool *x509.CertPool // nil: the system's roots
	if file := p["trustStoreFile"].(string); file != "" {
		pw, given, err := trustStorePassword.get(p)
		if err != nil {
			return nil, err
		}
		if pool, err = keystore.LoadTrustPool(file, pw); err != nil {
			return nil, trustStorePassword.explain("trust store", err, given)
		}
	}
	a.tls = &tls.Config{ServerName: host, RootCAs: pool, MinVersion: tls.VersionTLS12}
	return a, nil
}

func (a smtpAction) Process(ctx context.Context, m message.Message) (message.Message, error) {
	data, err := a.compose(m)
	if err != nil {
		return nil, err
	}
	if err := a.send(ctx, data); err != nil {
		return nil, fmt.Errorf("smtp %s: %w", a.addr, err)
	}
	return m, nil
}

// send delivers data to the recipients, over TLS.
func (a smtpAction) send(ctx context.Context, data []byte) error {
	d := net.Dialer{Timeout: a.timeout}
	var conn net.Conn
	var err error
	if a.implicitTLS {
		conn, err = (&tls.Dialer{NetDialer: &d, Config: a.tls}).DialContext(ctx, "tcp", a.addr)
	} else {
		conn, err = d.DialContext(ctx, "tcp", a.addr)
	}
	if err != nil {
		return err
	}
	conn.SetDeadline(time.Now().Add(a.timeout))
	c, err := smtp.NewClient(conn, a.host)
	if err != nil {
		conn.Close()
		return err
	}
	defer c.Close()

	if !a.implicitTLS {
		if ok, _ := c.Extension("STARTTLS"); !ok {
			return fmt.Errorf("server does not support STARTTLS")
		}
		if err := c.StartTLS(a.tls); err != nil {
			return err
		}
	}
	switch {
	case a.token != "":
		err = c.Auth(xoauth2{a.username, a.token})
	case a.password != "":
		err = c.Auth(smtp.PlainAuth("", a.username, a.password, a.host))
	}
	if err != nil {
		return err
	}
	if err := c.Mail(address(a.from)); err != nil {
		return err
	}
	for _, to := range a.to {
		if err := c.Rcpt(address(to)); err != nil {
			return err
		}
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write(data); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}
	return c.Quit()
}

// address returns the address in "Name <addr>", or s itself.
func address(s string) string {
	if i, j := strings.LastIndexByte(s, '<'), strings.LastIndexByte(s, '>'); i >= 0 && j > i {
		return s[i+1 : j]
	}
	return s
}

// compose returns the email for m: headers and a text body, or a text and
// the message body as attachment.
func (a smtpAction) compose(m message.Message) ([]byte, error) {
	subject := a.subject
	if s, ok := m["subject"].(string); ok {
		subject = s
	}
	var b bytes.Buffer
	header := func(k, v string) { fmt.Fprintf(&b, "%s: %s\r\n", k, v) }
	header("From", a.from)
	header("To", strings.Join(a.to, ", "))
	if a.replyTo != "" {
		header("Reply-To", a.replyTo)
	}
	header("Subject", mime.QEncoding.Encode("utf-8", subject))
	header("Date", time.Now().Format(time.RFC1123Z))
	header("Message-ID", "<"+newUUID()+"@"+a.host+">")
	header("MIME-Version", "1.0")

	ct := a.contentType
	if ct == "" {
		ct, _ = m[message.ContentType].(string)
	}
	if !a.attach && a.emailBody == "" {
		if ct == "" {
			ct = "text/plain; charset=utf-8"
		}
		header("Content-Type", ct)
		header("Content-Transfer-Encoding", "quoted-printable")
		b.WriteString("\r\n")
		writeQuotedPrintable(&b, bytesOf(m[message.Body]))
		return b.Bytes(), nil
	}

	w := multipart.NewWriter(&b)
	header("Content-Type", "multipart/mixed; boundary="+w.Boundary())
	b.WriteString("\r\n")
	text, err := w.CreatePart(textproto.MIMEHeader{
		"Content-Type":              {"text/plain; charset=utf-8"},
		"Content-Transfer-Encoding": {"quoted-printable"},
	})
	if err != nil {
		return nil, err
	}
	writeQuotedPrintable(text, []byte(a.emailBody))

	name, _ := m[FileName].(string)
	if name == "" {
		name = "attachment"
	}
	if ct == "" {
		ct = "application/octet-stream"
	}
	att, err := w.CreatePart(textproto.MIMEHeader{
		"Content-Type":              {ct},
		"Content-Disposition":       {mime.FormatMediaType("attachment", map[string]string{"filename": name})},
		"Content-Transfer-Encoding": {"base64"},
	})
	if err != nil {
		return nil, err
	}
	enc := base64.StdEncoding.EncodeToString(bytesOf(m[message.Body]))
	for len(enc) > 76 {
		att.Write([]byte(enc[:76] + "\r\n"))
		enc = enc[76:]
	}
	att.Write([]byte(enc + "\r\n"))
	if err := w.Close(); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func writeQuotedPrintable(w interface{ Write([]byte) (int, error) }, data []byte) {
	qp := quotedprintable.NewWriter(w)
	qp.Write(data)
	qp.Close()
}

// xoauth2 is the SMTP authentication with an OAuth2 access token.
type xoauth2 struct{ user, token string }

func (x xoauth2) Start(*smtp.ServerInfo) (string, []byte, error) {
	return "XOAUTH2", []byte("user=" + x.user + "\x01auth=Bearer " + x.token + "\x01\x01"), nil
}

func (x xoauth2) Next(fromServer []byte, more bool) ([]byte, error) {
	if more {
		return nil, fmt.Errorf("XOAUTH2 failed: %s", fromServer)
	}
	return nil, nil
}
