package impl

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"dif/message"
	stepdef "dif/steps/definition"
)

// encoderAction converts the body from one charset to another. Bytes the
// origin charset cannot decode become U+FFFD; characters the target charset
// cannot hold become '?', as in Java.
type encoderAction struct {
	from, to charset
}

// charset is one of the charsets the encoder supports. The standard library
// has no charset tables, so DIF supports the ones that need none.
type charset int

const (
	utf8Charset charset = iota
	latin1Charset
	asciiCharset
)

func parseCharset(name string) (charset, error) {
	switch strings.ToUpper(name) {
	case "UTF-8", "UTF8":
		return utf8Charset, nil
	case "ISO-8859-1", "ISO8859-1", "ISO_8859_1", "LATIN1":
		return latin1Charset, nil
	case "US-ASCII", "ASCII":
		return asciiCharset, nil
	}
	return 0, fmt.Errorf("charset %q is not supported; use UTF-8, ISO-8859-1 or US-ASCII", name)
}

func newEncoderAction(_ string, p stepdef.Params) (stepdef.Processor, error) {
	from, err := parseCharset(p["originCharset"].(string))
	if err != nil {
		return nil, fmt.Errorf("option originCharset: %w", err)
	}
	to, err := parseCharset(p["targetCharset"].(string))
	if err != nil {
		return nil, fmt.Errorf("option targetCharset: %w", err)
	}
	return encoderAction{from, to}, nil
}

func (a encoderAction) Process(_ context.Context, m message.Message) (message.Message, error) {
	data := bytesOf(m[message.Body])
	out := make([]byte, 0, len(data))
	for len(data) > 0 {
		r, n := a.from.decode(data)
		out = a.to.append(out, r)
		data = data[n:]
	}
	m[message.Body] = out
	return m, nil
}

// decode returns the first character of data and its length in bytes.
func (c charset) decode(data []byte) (rune, int) {
	switch {
	case c == utf8Charset:
		return utf8.DecodeRune(data)
	case c == asciiCharset && data[0] >= utf8.RuneSelf:
		return utf8.RuneError, 1
	}
	return rune(data[0]), 1
}

// append appends r encoded in c to b.
func (c charset) append(b []byte, r rune) []byte {
	switch {
	case c == utf8Charset:
		return utf8.AppendRune(b, r)
	case c == latin1Charset && r <= 0xFF, c == asciiCharset && r < utf8.RuneSelf:
		return append(b, byte(r))
	}
	return append(b, '?')
}

// text decodes data in c.
func (c charset) text(data []byte) string {
	if c == utf8Charset {
		return string(data)
	}
	out := make([]byte, 0, len(data))
	for len(data) > 0 {
		r, n := c.decode(data)
		out = utf8.AppendRune(out, r)
		data = data[n:]
	}
	return string(out)
}
