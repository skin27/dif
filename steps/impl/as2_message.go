package impl

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"mime"
	"mime/quotedprintable"
	"strings"
)

// AS2 (RFC 4130) carries an EDI document, or any file, in an HTTP POST: the
// document is a MIME entity, which may be compressed (RFC 5402), signed
// (multipart/signed with a detached CMS signature) and encrypted (CMS
// EnvelopedData), in that order; the receiver answers with a receipt, the MDN.
// This file builds and reads the MIME of that; cms.go has the cryptography.

// as2Structure says what is done to a document, from the name Camel's AS2
// component gives it.
type as2Structure struct{ sign, compress, encrypt bool }

func parseAS2Structure(name string) (as2Structure, error) {
	switch strings.ToUpper(strings.TrimSpace(name)) {
	case "", "PLAIN":
		return as2Structure{}, nil
	case "SIGNED":
		return as2Structure{sign: true}, nil
	case "ENCRYPTED":
		return as2Structure{encrypt: true}, nil
	case "SIGNED_ENCRYPTED":
		return as2Structure{sign: true, encrypt: true}, nil
	case "PLAIN_COMPRESSED":
		return as2Structure{compress: true}, nil
	case "SIGNED_COMPRESSED":
		return as2Structure{sign: true, compress: true}, nil
	case "ENCRYPTED_COMPRESSED":
		return as2Structure{encrypt: true, compress: true}, nil
	case "ENCRYPTED_COMPRESSED_SIGNED", "SIGNED_ENCRYPTED_COMPRESSED":
		return as2Structure{sign: true, encrypt: true, compress: true}, nil
	}
	return as2Structure{}, fmt.Errorf("%q is not one of PLAIN, SIGNED, ENCRYPTED, SIGNED_ENCRYPTED, PLAIN_COMPRESSED, SIGNED_COMPRESSED, ENCRYPTED_COMPRESSED, ENCRYPTED_COMPRESSED_SIGNED", name)
}

// ---- MIME entities

// mimeEntity is a MIME entity: headers, in order, and a body.
type mimeEntity struct {
	headers [][2]string
	body    []byte
}

func (e mimeEntity) header(name string) string {
	for _, h := range e.headers {
		if strings.EqualFold(h[0], name) {
			return h[1]
		}
	}
	return ""
}

// bytes writes the entity, with CRLF line ends in the headers.
func (e mimeEntity) bytes() []byte {
	var b bytes.Buffer
	for _, h := range e.headers {
		b.WriteString(h[0] + ": " + h[1] + "\r\n")
	}
	b.WriteString("\r\n")
	b.Write(e.body)
	return b.Bytes()
}

// parseEntity reads headers and body, tolerating line feeds without carriage
// returns and folded headers.
func parseEntity(raw []byte) (mimeEntity, error) {
	var e mimeEntity
	rest := raw
	for {
		i := bytes.IndexByte(rest, '\n')
		if i < 0 {
			if len(bytes.TrimSpace(rest)) == 0 {
				return e, nil
			}
			return e, errors.New("the MIME entity has no empty line after its headers")
		}
		line := strings.TrimRight(string(rest[:i]), "\r")
		rest = rest[i+1:]
		if line == "" {
			e.body = rest
			return e, nil
		}
		if line[0] == ' ' || line[0] == '\t' {
			if len(e.headers) == 0 {
				return e, errors.New("bad header")
			}
			e.headers[len(e.headers)-1][1] += " " + strings.TrimSpace(line)
			continue
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok || strings.TrimSpace(name) == "" {
			return e, fmt.Errorf("bad header line %q", clip(line))
		}
		e.headers = append(e.headers, [2]string{strings.TrimSpace(name), strings.TrimSpace(value)})
	}
}

// decodeBody removes the content transfer encoding.
func (e mimeEntity) decoded() ([]byte, error) {
	switch strings.ToLower(e.header("Content-Transfer-Encoding")) {
	case "base64":
		clean := strings.Map(func(r rune) rune {
			if r == '\r' || r == '\n' || r == ' ' || r == '\t' {
				return -1
			}
			return r
		}, string(e.body))
		return base64.StdEncoding.DecodeString(clean)
	case "quoted-printable":
		var out bytes.Buffer
		_, err := out.ReadFrom(quotedprintable.NewReader(bytes.NewReader(e.body)))
		return out.Bytes(), err
	}
	return e.body, nil
}

func newBoundary() string {
	b := make([]byte, 12)
	rand.Read(b)
	return "dif_" + hex.EncodeToString(b)
}

func base64Lines(data []byte) []byte {
	enc := base64.StdEncoding.EncodeToString(data)
	var out bytes.Buffer
	for len(enc) > 64 {
		out.WriteString(enc[:64] + "\r\n")
		enc = enc[64:]
	}
	out.WriteString(enc)
	return out.Bytes()
}

// payloadEntity is the document itself.
func payloadEntity(contentType, fileName string, body []byte) mimeEntity {
	e := mimeEntity{headers: [][2]string{{"Content-Type", contentType}, {"Content-Transfer-Encoding", "binary"}}, body: body}
	if fileName != "" {
		e.headers = append(e.headers, [2]string{"Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": fileName})})
	}
	return e
}

func compressEntity(inner mimeEntity) (mimeEntity, error) {
	z, err := cmsCompress(inner.bytes())
	if err != nil {
		return mimeEntity{}, err
	}
	return mimeEntity{headers: [][2]string{
		{"Content-Type", `application/pkcs7-mime; smime-type=compressed-data; name=smime.p7z`},
		{"Content-Transfer-Encoding", "binary"},
		{"Content-Disposition", `attachment; filename=smime.p7z`},
	}, body: z}, nil
}

func signEntity(inner mimeEntity, signer keyMaterial, h cmsHash) (mimeEntity, error) {
	cert := signer.ownCertificate()
	if signer.key == nil || cert == nil {
		return mimeEntity{}, errors.New("signing needs a private key and its certificate")
	}
	content := inner.bytes()
	sig, err := cmsSign(content, cert, signer.key, h, signer.certs)
	if err != nil {
		return mimeEntity{}, err
	}
	boundary := newBoundary()
	var body bytes.Buffer
	body.WriteString("--" + boundary + "\r\n")
	body.Write(content)
	body.WriteString("\r\n--" + boundary + "\r\n")
	body.WriteString("Content-Type: application/pkcs7-signature; name=smime.p7s\r\nContent-Transfer-Encoding: base64\r\nContent-Disposition: attachment; filename=smime.p7s\r\n\r\n")
	body.Write(base64Lines(sig))
	body.WriteString("\r\n--" + boundary + "--\r\n")
	return mimeEntity{headers: [][2]string{
		{"Content-Type", fmt.Sprintf(`multipart/signed; protocol="application/pkcs7-signature"; micalg=%s; boundary="%s"`, h.micalg, boundary)},
	}, body: body.Bytes()}, nil
}

func encryptEntity(inner mimeEntity, recipient *x509.Certificate, c cmsCipher) (mimeEntity, error) {
	enc, err := cmsEncrypt(inner.bytes(), recipient, c)
	if err != nil {
		return mimeEntity{}, err
	}
	return mimeEntity{headers: [][2]string{
		{"Content-Type", `application/pkcs7-mime; smime-type=enveloped-data; name=smime.p7m`},
		{"Content-Transfer-Encoding", "binary"},
		{"Content-Disposition", `attachment; filename=smime.p7m`},
	}, body: enc}, nil
}

// micOf is the message integrity check of content: the base64 of its digest.
func micOf(content []byte, h cmsHash) string {
	return base64.StdEncoding.EncodeToString(h.sum(content))
}

// ---- reading what arrives

// as2Failure is a problem with a received message, with the word the receipt
// gives for it (RFC 4130, 7.5.1 and RFC 3798).
type as2Failure struct {
	modifier string // authentication-failed, decryption-failed, decompression-failed, insufficient-message-security, unexpected-processing-error
	err      error
}

func (f *as2Failure) Error() string { return f.err.Error() }
func (f *as2Failure) Unwrap() error { return f.err }

func failure(modifier, format string, args ...any) *as2Failure {
	return &as2Failure{modifier, fmt.Errorf(format, args...)}
}

// as2Received is a received document, and what was done to it.
type as2Received struct {
	payload                       mimeEntity
	signed, encrypted, compressed bool
	signer                        *x509.Certificate
	mic                           []byte // what the message integrity check covers
}

// as2Receiver is what is needed to read a message.
type as2Receiver struct {
	decryptKey  *rsa.PrivateKey
	decryptCert *x509.Certificate // may be nil
	trusted     *x509.CertPool    // the certificates a signer must be one of or be issued by; nil trusts any
	known       []*x509.Certificate
}

// mediaTypeOf returns the media type and parameters of an entity.
func mediaTypeOf(e mimeEntity) (string, map[string]string) {
	mt, params, err := mime.ParseMediaType(e.header("Content-Type"))
	if err != nil {
		return strings.ToLower(strings.TrimSpace(strings.SplitN(e.header("Content-Type"), ";", 2)[0])), map[string]string{}
	}
	return mt, params
}

// unwrap peels the layers of an entity: it decrypts, decompresses and verifies
// until what is left is the document.
func (r as2Receiver) unwrap(top mimeEntity) (as2Received, error) {
	var out as2Received
	entity := top
	micFrom := top.bytes()
	for range 6 {
		mt, params := mediaTypeOf(entity)
		switch {
		case mt == "application/pkcs7-mime" || mt == "application/x-pkcs7-mime":
			data, err := entity.decoded()
			if err != nil {
				return out, failure("unexpected-processing-error", "the MIME of the message is damaged: %v", err)
			}
			kind, err := cmsType(data)
			if err != nil {
				return out, failure("unexpected-processing-error", "%v", err)
			}
			var inner []byte
			switch {
			case kind.Equal(oidEnveloped):
				if r.decryptKey == nil {
					return out, failure("decryption-failed", "the message is encrypted, and this endpoint has no decrypting private key")
				}
				if inner, err = cmsDecrypt(data, r.decryptCert, r.decryptKey); err != nil {
					return out, failure("decryption-failed", "%v", err)
				}
				out.encrypted = true
			case kind.Equal(oidCompressed):
				if inner, err = cmsDecompress(data); err != nil {
					return out, failure("decompression-failed", "%v", err)
				}
				out.compressed = true
			default:
				return out, failure("unexpected-processing-error", "%v is not a CMS content type this endpoint reads", kind)
			}
			if entity, err = parseEntity(inner); err != nil {
				return out, failure("unexpected-processing-error", "%v", err)
			}
			micFrom = inner
		case mt == "multipart/signed":
			first, sig, err := splitSigned(entity.body, params["boundary"])
			if err != nil {
				return out, failure("unexpected-processing-error", "%v", err)
			}
			sigEntity, err := parseEntity(sig)
			if err != nil {
				return out, failure("unexpected-processing-error", "%v", err)
			}
			sigData, err := sigEntity.decoded()
			if err != nil {
				return out, failure("authentication-failed", "the signature is damaged: %v", err)
			}
			v, err := cmsVerify(sigData, first, r.known, r.trusted)
			if err != nil {
				return out, failure("authentication-failed", "%v", err)
			}
			out.signed, out.signer = true, v.signer
			if entity, err = parseEntity(first); err != nil {
				return out, failure("unexpected-processing-error", "%v", err)
			}
			micFrom = first
		default:
			out.payload, out.mic = entity, micFrom
			return out, nil
		}
	}
	return out, failure("unexpected-processing-error", "the message is wrapped in more than 6 layers")
}

// splitSigned cuts a multipart/signed body into the exact bytes of its first
// part, which the signature covers, and its second.
func splitSigned(body []byte, boundary string) (first, sig []byte, err error) {
	if boundary == "" {
		return nil, nil, errors.New("multipart/signed has no boundary")
	}
	delim := []byte("--" + boundary)
	lineEnd := func(from int) int { // the start of the next line
		i := bytes.IndexByte(body[from:], '\n')
		if i < 0 {
			return len(body)
		}
		return from + i + 1
	}
	// A delimiter starts a line.
	next := func(from int) int {
		for i := from; ; {
			j := bytes.Index(body[i:], delim)
			if j < 0 {
				return -1
			}
			j += i
			if j == 0 || body[j-1] == '\n' {
				return j
			}
			i = j + len(delim)
		}
	}
	d1 := next(0)
	if d1 < 0 {
		return nil, nil, errors.New("multipart/signed has no first part")
	}
	start1 := lineEnd(d1)
	d2 := next(start1)
	if d2 < 0 {
		return nil, nil, errors.New("multipart/signed has no signature part")
	}
	first = trimFinalBreak(body[start1:d2])
	start2 := lineEnd(d2)
	d3 := next(start2)
	if d3 < 0 {
		d3 = len(body)
	}
	return first, trimFinalBreak(body[start2:d3]), nil
}

// trimFinalBreak removes the line break before a delimiter, which belongs to it:
// CRLF, or a line feed alone as OpenSSL writes it.
func trimFinalBreak(b []byte) []byte {
	if bytes.HasSuffix(b, []byte("\r\n")) {
		return b[:len(b)-2]
	}
	return bytes.TrimSuffix(b, []byte("\n"))
}
