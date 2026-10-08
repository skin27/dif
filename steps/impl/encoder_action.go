package impl

import (
	"context"
	"fmt"
	"slices"
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
// has no charset tables, so DIF supports the ones that need none, and
// windows-1252 with its one small table.
type charset int

const (
	utf8Charset charset = iota
	latin1Charset
	asciiCharset
	cp1252Charset
)

// cp1252High holds the characters of windows-1252 for the bytes 0x80 to 0x9F,
// where it differs from ISO-8859-1; U+FFFD marks the five bytes that have none.
var cp1252High = [32]rune{
	0x20AC, 0xFFFD, 0x201A, 0x0192, 0x201E, 0x2026, 0x2020, 0x2021,
	0x02C6, 0x2030, 0x0160, 0x2039, 0x0152, 0xFFFD, 0x017D, 0xFFFD,
	0xFFFD, 0x2018, 0x2019, 0x201C, 0x201D, 0x2022, 0x2013, 0x2014,
	0x02DC, 0x2122, 0x0161, 0x203A, 0x0153, 0xFFFD, 0x017E, 0x0178,
}

func parseCharset(name string) (charset, error) {
	switch strings.ToUpper(name) {
	case "UTF-8", "UTF8":
		return utf8Charset, nil
	case "ISO-8859-1", "ISO8859-1", "ISO8859_1", "ISO_8859_1", "LATIN1":
		return latin1Charset, nil
	case "US-ASCII", "ASCII":
		return asciiCharset, nil
	case "WINDOWS-1252", "WINDOWS1252", "CP1252", "CP-1252", "MS1252":
		return cp1252Charset, nil
	}
	return 0, fmt.Errorf("charset %q is not supported; use UTF-8, ISO-8859-1, US-ASCII or windows-1252", name)
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
	case c == cp1252Charset && data[0] >= 0x80 && data[0] <= 0x9F:
		return cp1252High[data[0]-0x80], 1
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
	case c == cp1252Charset && (r < 0x80 || r >= 0xA0 && r <= 0xFF):
		return append(b, byte(r))
	case c == cp1252Charset && r != utf8.RuneError:
		if i := slices.Index(cp1252High[:], r); i >= 0 {
			return append(b, byte(0x80+i))
		}
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
