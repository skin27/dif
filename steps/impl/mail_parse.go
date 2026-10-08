package impl

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"sort"
	"strings"

	"dif/message"
)

// An email, as the imaps source makes a message of it. The body is the text of
// the message: the first text/plain part, else the first text/html part, in
// UTF-8. The headers are
//
//	Subject, From, To, Cc, Date    as the email has them, decoded (a header the email lacks is not set)
//	mail.messageId, mail.uid       the Message-ID without <> and the UID in the mailbox
//	attachments                    with content "both", the file names, comma separated
//	attachment.<file name>         with content "both", the content of an attachment, as bytes
//
// (The email's own Message-ID and Reply-To would collide with the identity
// headers of DIF, which is why there is no Message-Id header.)
// What is an attachment: a part with the disposition attachment, a part that
// is no text and has a file name, and an attached email (message/rfc822).

type mailAttachment struct {
	name string
	data []byte
}

type parsedMail struct {
	text, html  string
	hasText     bool
	hasHTML     bool
	attachments []mailAttachment
}

// maxMailParts bounds the parts of an email, nested or not.
const maxMailParts = 1000

// parseMail reads an email in the RFC 5322 format.
func parseMail(raw []byte) (hdr mail.Header, parsed parsedMail, err error) {
	msg, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return nil, parsed, fmt.Errorf("not an email: %w", err)
	}
	n := 0
	err = parsed.walk(textHeader(msg.Header), msg.Body, 0, &n)
	return msg.Header, parsed, err
}

// textHeader is the part of a header that decides what a part is.
type textHeader map[string][]string

func (h textHeader) get(k string) string {
	if v := h[k]; len(v) > 0 {
		return v[0]
	}
	return ""
}

func (p *parsedMail) walk(h textHeader, body io.Reader, depth int, n *int) error {
	if *n++; *n > maxMailParts || depth > 20 {
		return fmt.Errorf("an email of more than %d parts or %d levels", maxMailParts, 20)
	}
	mediaType, params, err := mime.ParseMediaType(h.get("Content-Type"))
	if err != nil {
		mediaType, params = "text/plain", map[string]string{}
	}
	disposition, dparams, _ := mime.ParseMediaType(h.get("Content-Disposition"))

	if strings.HasPrefix(mediaType, "multipart/") && params["boundary"] != "" {
		mr := multipart.NewReader(body, params["boundary"])
		for {
			part, err := mr.NextRawPart()
			if err == io.EOF {
				return nil
			}
			if err != nil {
				return fmt.Errorf("multipart: %w", err)
			}
			if err := p.walk(textHeader(part.Header), part, depth+1, n); err != nil {
				return err
			}
		}
	}

	data, err := io.ReadAll(io.LimitReader(decodeTransfer(h.get("Content-Transfer-Encoding"), body), maxBodySize+1))
	if err != nil {
		return err
	}
	if len(data) > maxBodySize {
		return fmt.Errorf("a part of more than %d bytes", maxBodySize)
	}
	name := dparams["filename"]
	if name == "" {
		name = params["name"]
	}
	switch {
	case strings.EqualFold(disposition, "attachment") || mediaType == "message/rfc822" ||
		name != "" && !strings.HasPrefix(mediaType, "text/") || name != "" && strings.EqualFold(disposition, "attachment"):
		if name == "" {
			name = "attachment" + fmt.Sprint(len(p.attachments)+1)
		}
		p.attachments = append(p.attachments, mailAttachment{decodeMailWord(name), data})
	case mediaType == "text/plain" && !p.hasText:
		p.text, p.hasText = decodeMailText(data, params["charset"]), true
	case mediaType == "text/html" && !p.hasHTML:
		p.html, p.hasHTML = decodeMailText(data, params["charset"]), true
	case name != "": // a second text part with a file name
		p.attachments = append(p.attachments, mailAttachment{decodeMailWord(name), data})
	}
	return nil
}

// decodeTransfer decodes the Content-Transfer-Encoding of a part.
func decodeTransfer(encoding string, r io.Reader) io.Reader {
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "base64":
		return base64.NewDecoder(base64.StdEncoding, &skipSpace{r})
	case "quoted-printable":
		return quotedprintable.NewReader(r)
	}
	return r
}

// skipSpace drops the line breaks and spaces base64 may hold, and tolerates
// missing padding by leaving it to the decoder.
type skipSpace struct{ r io.Reader }

func (s *skipSpace) Read(p []byte) (int, error) {
	n, err := s.r.Read(p)
	out := p[:0]
	for _, c := range p[:n] {
		if c != '\r' && c != '\n' && c != ' ' && c != '\t' {
			out = append(out, c)
		}
	}
	return len(out), err
}

// decodeMailText returns text in the charset as UTF-8. A charset DIF does not
// know is taken for UTF-8, with what is not valid UTF-8 replaced.
func decodeMailText(data []byte, name string) string {
	if name != "" {
		if cs, err := parseCharset(name); err == nil && cs != utf8Charset {
			return cs.text(data)
		}
	}
	return strings.ToValidUTF8(string(data), "�")
}

// mailWords decodes RFC 2047 words (=?utf-8?q?...?=) in headers.
var mailWords = &mime.WordDecoder{CharsetReader: func(name string, input io.Reader) (io.Reader, error) {
	cs, err := parseCharset(name)
	if err != nil {
		return nil, err
	}
	data, err := io.ReadAll(input)
	if err != nil {
		return nil, err
	}
	return strings.NewReader(cs.text(data)), nil
}}

func decodeMailWord(s string) string {
	if d, err := mailWords.DecodeHeader(s); err == nil {
		return d
	}
	return s
}

// mailMessage makes a message of an email. With both the attachments are
// headers too.
func mailMessage(raw []byte, uid uint32, both bool) (message.Message, error) {
	hdr, parsed, err := parseMail(raw)
	if err != nil {
		return nil, err
	}
	m := message.New(nil)
	switch {
	case parsed.hasText:
		m[message.Body] = parsed.text
		m[message.ContentType] = "text/plain; charset=utf-8"
	case parsed.hasHTML:
		m[message.Body] = parsed.html
		m[message.ContentType] = "text/html; charset=utf-8"
	default:
		m[message.Body] = ""
	}
	for _, name := range []string{"Subject", "From", "To", "Cc"} {
		if v := hdr.Get(name); v != "" {
			m[name] = decodeMailWord(v)
		}
	}
	if v := hdr.Get("Date"); v != "" {
		m["Date"] = v
	}
	if v := strings.Trim(strings.TrimSpace(hdr.Get("Message-ID")), "<>"); v != "" {
		m["mail.messageId"] = v
	}
	m["mail.uid"] = int(uid)
	if both && len(parsed.attachments) > 0 {
		names := make([]string, 0, len(parsed.attachments))
		for _, a := range parsed.attachments {
			names = append(names, a.name)
			m["attachment."+a.name] = a.data
		}
		sort.Strings(names)
		m["attachments"] = strings.Join(names, ",")
	}
	return m, nil
}
