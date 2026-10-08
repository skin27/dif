package impl

import (
	"fmt"
	"strings"
	"testing"

	"dif/message"
)

func crlf(s string) []byte { return []byte(strings.ReplaceAll(s, "\n", "\r\n")) }

func TestMailMessage(t *testing.T) {
	const head = "From: =?utf-8?q?Jos=C3=A9?= <jose@example.com>\nTo: a@example.com, b@example.com\nCc: c@example.com\nSubject: =?iso-8859-1?q?Caf=E9_menu?=\nDate: Tue, 21 Mar 2023 10:30:05 +0100\nMessage-ID: <abc.123@example.com>\n"
	for _, c := range []struct {
		name string
		raw  string
		both bool
		body string
		ct   string
		// the headers the message must have
		want map[string]any
	}{
		{"plain", head + "\nHello\nworld\n", false, "Hello\r\nworld\r\n", "text/plain; charset=utf-8", map[string]any{
			"Subject": "Café menu", "From": "José <jose@example.com>", "To": "a@example.com, b@example.com", "Cc": "c@example.com",
			"Date": "Tue, 21 Mar 2023 10:30:05 +0100", "mail.messageId": "abc.123@example.com", "mail.uid": 7}},
		{"no headers but a subject", "Subject: only\n\nbody", false, "body", "text/plain; charset=utf-8", map[string]any{"Subject": "only"}},
		{"quoted printable latin1", head + "Content-Type: text/plain; charset=ISO-8859-1\nContent-Transfer-Encoding: quoted-printable\n\nCaf=E9 =\ncr=E8me\n", false, "Café crème\r\n", "", nil},
		{"base64 utf8", head + "Content-Type: text/plain; charset=utf-8\nContent-Transfer-Encoding: base64\n\nSGVsbG8g4oKs\nIQ==\n", false, "Hello €!", "", nil},
		{"html only", head + "Content-Type: text/html\n\n<p>Hi</p>", false, "<p>Hi</p>", "text/html; charset=utf-8", nil},
		{"alternative", head + "Content-Type: multipart/alternative; boundary=XX\n\n--XX\nContent-Type: text/html\n\n<p>html</p>\n--XX\nContent-Type: text/plain\n\ntext\n--XX--\n", false, "text", "text/plain; charset=utf-8", nil},
		{"mixed with attachments, body only", head + "Content-Type: multipart/mixed; boundary=XX\n\n--XX\nContent-Type: text/plain\n\nSee attached\n--XX\nContent-Type: application/pdf; name=\"report.pdf\"\nContent-Disposition: attachment; filename=\"report.pdf\"\nContent-Transfer-Encoding: base64\n\nUERGLTEuNA==\n--XX--\n", false, "See attached", "", map[string]any{"mail.uid": 7}},
		{"mixed with attachments, both", head + "Content-Type: multipart/mixed; boundary=XX\n\n--XX\nContent-Type: multipart/alternative; boundary=YY\n\n--YY\nContent-Type: text/plain\n\nSee attached\n--YY\nContent-Type: text/html\n\n<p>See attached</p>\n--YY--\n--XX\nContent-Type: application/pdf; name=\"report.pdf\"\nContent-Disposition: attachment; filename=\"report.pdf\"\nContent-Transfer-Encoding: base64\n\nUERGLTEuNA==\n--XX\nContent-Type: image/png\nContent-Disposition: inline; filename=\"=?utf-8?q?caf=C3=A9.png?=\"\nContent-Transfer-Encoding: base64\n\niVBORw==\n--XX\nContent-Type: text/plain; name=\"notes.txt\"\nContent-Disposition: attachment; filename=notes.txt\n\nsecond text\n--XX--\n", true, "See attached", "",
			map[string]any{"attachments": "café.png,notes.txt,report.pdf", "attachment.report.pdf": []byte("PDF-1.4"), "attachment.café.png": []byte("\x89PNG"), "attachment.notes.txt": []byte("second text")}},
		{"attached email", head + "Content-Type: multipart/mixed; boundary=XX\n\n--XX\nContent-Type: text/plain\n\nForwarded\n--XX\nContent-Type: message/rfc822\n\nSubject: inner\n\ninner body\n--XX--\n", true, "Forwarded", "", map[string]any{"attachments": "attachment1"}},
		{"nothing to read", head + "Content-Type: application/zip\nContent-Transfer-Encoding: base64\n\nUEsDBA==\n", false, "", "", nil},
		{"unknown charset is read as utf-8", head + "Content-Type: text/plain; charset=klingon\n\ncaf\xc3\xa9 \xff", false, "café �", "", nil},
		{"bad content type is text", head + "Content-Type: ???\n\nstill text", false, "still text", "", nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			m, err := mailMessage(crlf(c.raw), 7, c.both)
			if err != nil {
				t.Fatal(err)
			}
			if got := m[message.Body]; got != c.body {
				t.Errorf("body = %q, want %q", got, c.body)
			}
			if c.ct != "" && m[message.ContentType] != c.ct {
				t.Errorf("content type = %v, want %v", m[message.ContentType], c.ct)
			}
			for k, want := range c.want {
				if fmt.Sprint(m[k]) != fmt.Sprint(want) {
					t.Errorf("header %s = %v, want %v", k, m[k], want)
				}
			}
			if !c.both {
				for k := range m {
					if strings.HasPrefix(k, "attachment") {
						t.Errorf("header %s with content body", k)
					}
				}
			}
			// The identity of DIF is not the email's.
			if m[message.MessageID] == "abc.123@example.com" || m[message.ReplyTo] != nil {
				t.Errorf("the email's own headers overwrote the identity: %v", m)
			}
		})
	}
}

func TestMailMessageRefusesWhatIsNoEmail(t *testing.T) {
	for _, raw := range []string{"", "no header at all", "\x00\x01\x02"} {
		if _, err := mailMessage([]byte(raw), 1, false); err == nil {
			t.Errorf("%q: no error", raw)
		}
	}
	var b strings.Builder
	b.WriteString("Content-Type: multipart/mixed; boundary=XX\n\n")
	for range maxMailParts + 5 {
		b.WriteString("--XX\nContent-Type: text/plain; name=a.txt\n\nx\n")
	}
	b.WriteString("--XX--\n")
	if _, err := mailMessage(crlf(b.String()), 1, true); err == nil || !strings.Contains(err.Error(), "parts") {
		t.Errorf("err = %v, want the limit on parts", err)
	}
	deep := "Content-Type: multipart/mixed; boundary=B0\n\n"
	for i := range 30 {
		deep += fmt.Sprintf("--B%d\nContent-Type: multipart/mixed; boundary=B%d\n\n", i, i+1)
	}
	if _, err := mailMessage(crlf(deep), 1, false); err == nil || !strings.Contains(err.Error(), "levels") {
		t.Errorf("err = %v, want the limit on levels", err)
	}
}
