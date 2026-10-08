package impl

import (
	"bytes"
	"fmt"
	"net/http"
	"strings"
)

// The message disposition notification (MDN, RFC 3798 and RFC 4130 section 7),
// the receipt that answers an AS2 message in the same HTTP exchange.

// mdnWanted is what the sender of a message asked for in its headers.
type mdnWanted struct {
	receipt bool    // Disposition-Notification-To is there
	signed  bool    // a signed receipt is asked for
	micalg  cmsHash // the digest the receipt's MIC and signature use
}

// parseMDNRequest reads Disposition-Notification-To and -Options:
//
//	signed-receipt-protocol=required, pkcs7-signature; signed-receipt-micalg=required, sha-256, sha1
func parseMDNRequest(h http.Header) mdnWanted {
	w := mdnWanted{receipt: h.Get("Disposition-Notification-To") != "", micalg: cmsHashes["SHA256"]}
	opts := strings.ToLower(h.Get("Disposition-Notification-Options"))
	if strings.Contains(opts, "signed-receipt-protocol") {
		w.signed = true
	}
	if _, after, ok := strings.Cut(opts, "signed-receipt-micalg="); ok {
		// "required, sha-256, sha1": the first of the list we know, after the importance
		for _, name := range strings.FieldsFunc(strings.SplitN(after, ";", 2)[0], func(r rune) bool { return r == ',' || r == ' ' }) {
			if hash, ok := cmsHashByMicalg(name); ok {
				w.micalg = hash
				break
			}
		}
	}
	return w
}

// mdnReport is the content of a receipt.
type mdnReport struct {
	text            string
	reportingUA     string
	recipient       string // the AS2 id of the one that received the message
	originalID      string // Message-ID of the message
	disposition     string
	mic, micAlgName string // Received-Content-MIC
}

const mdnSent = "automatic-action/MDN-sent-automatically; "

// dispositionProcessed and dispositionError are the values of Disposition.
func dispositionProcessed() string { return mdnSent + "processed" }
func dispositionError(modifier string) string {
	return mdnSent + "processed/Error: " + modifier
}

// entity writes the report as multipart/report.
func (r mdnReport) entity() mimeEntity {
	boundary := newBoundary()
	var b strings.Builder
	b.WriteString("--" + boundary + "\r\nContent-Type: text/plain; charset=us-ascii\r\nContent-Transfer-Encoding: 7bit\r\n\r\n" + r.text + "\r\n")
	b.WriteString("--" + boundary + "\r\nContent-Type: message/disposition-notification\r\nContent-Transfer-Encoding: 7bit\r\n\r\n")
	b.WriteString("Reporting-UA: " + r.reportingUA + "\r\n")
	b.WriteString("Original-Recipient: rfc822; " + r.recipient + "\r\n")
	b.WriteString("Final-Recipient: rfc822; " + r.recipient + "\r\n")
	b.WriteString("Original-Message-ID: " + r.originalID + "\r\n")
	b.WriteString("Disposition: " + r.disposition + "\r\n")
	if r.mic != "" {
		b.WriteString("Received-Content-MIC: " + r.mic + ", " + r.micAlgName + "\r\n")
	}
	b.WriteString("\r\n--" + boundary + "--\r\n")
	return mimeEntity{
		headers: [][2]string{{"Content-Type", fmt.Sprintf(`multipart/report; report-type=disposition-notification; boundary="%s"`, boundary)}},
		body:    []byte(b.String()),
	}
}

// parseMDN reads the fields of the message/disposition-notification part.
func parseMDN(e mimeEntity) (mdnReport, error) {
	var r mdnReport
	mt, params := mediaTypeOf(e)
	if mt != "multipart/report" || params["boundary"] == "" {
		return r, fmt.Errorf("the answer is no MDN (it is %s)", mt)
	}
	for _, part := range splitMultipart(e.body, params["boundary"]) {
		pe, err := parseEntity(part)
		if err != nil {
			continue
		}
		if pmt, _ := mediaTypeOf(pe); pmt != "message/disposition-notification" {
			continue
		}
		fields, err := parseEntity(append(pe.body, "\r\n\r\n"...)) // the fields are headers
		if err != nil {
			return r, fmt.Errorf("the MDN fields: %w", err)
		}
		r.reportingUA = fields.header("Reporting-UA")
		r.originalID = fields.header("Original-Message-ID")
		r.recipient = strings.TrimSpace(strings.TrimPrefix(fields.header("Final-Recipient"), "rfc822;"))
		r.disposition = fields.header("Disposition")
		r.mic, r.micAlgName, _ = strings.Cut(fields.header("Received-Content-MIC"), ",")
		r.mic, r.micAlgName = strings.TrimSpace(r.mic), strings.TrimSpace(r.micAlgName)
		if r.disposition == "" {
			return r, fmt.Errorf("the MDN has no Disposition")
		}
		return r, nil
	}
	return r, fmt.Errorf("the MDN has no message/disposition-notification part")
}

// splitMultipart cuts a multipart body into its parts.
func splitMultipart(body []byte, boundary string) [][]byte {
	var parts [][]byte
	rest := body
	delim := []byte("--" + boundary)
	var cur []byte
	started := false
	for len(rest) > 0 {
		line := rest
		if i := bytes.IndexByte(rest, '\n'); i >= 0 {
			line, rest = rest[:i+1], rest[i+1:]
		} else {
			rest = nil
		}
		trimmed := trimFinalBreak(line)
		if string(trimmed) == string(delim) || string(trimmed) == string(delim)+"--" {
			if started {
				parts = append(parts, trimFinalBreak(cur))
			}
			started, cur = true, nil
			if string(trimmed) == string(delim)+"--" {
				break
			}
			continue
		}
		if started {
			cur = append(cur, line...)
		}
	}
	return parts
}

// ok tells if a disposition reports a message as received and processed.
func (r mdnReport) ok() bool {
	_, after, found := strings.Cut(r.disposition, ";")
	if !found {
		after = r.disposition
	}
	d := strings.ToLower(strings.TrimSpace(after))
	return strings.HasPrefix(d, "processed") && !strings.Contains(d, "error") && !strings.Contains(d, "failure")
}
