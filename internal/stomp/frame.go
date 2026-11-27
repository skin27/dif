package stomp

import (
	"bufio"
	"io"
	"strconv"
	"strings"
)

// Header is one frame header. A frame may repeat a header; the first counts.
type Header struct{ Name, Value string }

// Frame is one STOMP frame: a command, headers and a body.
type Frame struct {
	Command string
	Headers []Header
	Body    []byte
}

// Get returns the first value of the header name.
func (f *Frame) Get(name string) (string, bool) {
	for _, h := range f.Headers {
		if h.Name == name {
			return h.Value, true
		}
	}
	return "", false
}

// Value returns the first value of the header name, or "".
func (f *Frame) Value(name string) string { v, _ := f.Get(name); return v }

func (f *Frame) add(name, value string) { f.Headers = append(f.Headers, Header{name, value}) }

// frameError is a frame the reader rejects; the server answers it with an
// ERROR frame. Its text never contains what the client sent.
type frameError string

func (e frameError) Error() string { return string(e) }

// reader reads frames within bounds: the length of a line (the command or one
// header), the number of headers and the size of a body. It skips the EOLs
// between frames, which are heart-beats.
type reader struct {
	r          *bufio.Reader
	maxHeaders int
	maxBody    int
}

func newReader(r io.Reader, maxLine, maxHeaders, maxBody int) *reader {
	return &reader{r: bufio.NewReaderSize(r, maxLine), maxHeaders: maxHeaders, maxBody: maxBody}
}

// line reads one line without its LF or CRLF.
func (r *reader) line() (string, error) {
	b, err := r.r.ReadSlice('\n')
	if err == bufio.ErrBufferFull {
		return "", frameError("frame line too long")
	}
	if err != nil {
		if err == io.EOF && len(b) > 0 {
			err = io.ErrUnexpectedEOF
		}
		return "", err
	}
	b = b[:len(b)-1]
	if n := len(b); n > 0 && b[n-1] == '\r' {
		b = b[:n-1]
	}
	return string(b), nil
}

// read returns the next frame. It returns io.EOF when the stream ends
// between frames, and a frameError for a malformed or oversized frame.
func (r *reader) read() (*Frame, error) {
	var command string
	for command == "" {
		var err error
		if command, err = r.line(); err != nil {
			return nil, err
		}
	}
	f := &Frame{Command: command}
	escaped := command != "CONNECT" && command != "CONNECTED"
	for {
		line, err := r.line()
		if err != nil {
			return nil, unexpected(err)
		}
		if line == "" {
			break
		}
		if len(f.Headers) == r.maxHeaders {
			return nil, frameError("too many headers")
		}
		name, value, ok := strings.Cut(line, ":")
		if !ok {
			return nil, frameError("malformed header")
		}
		if escaped {
			if name, ok = unescape(name); ok {
				value, ok = unescape(value)
			}
			if !ok {
				return nil, frameError("undefined escape sequence in header")
			}
		}
		f.add(name, value)
	}
	if length, ok := f.Get("content-length"); ok {
		n, err := strconv.ParseUint(length, 10, 31)
		if err != nil {
			return nil, frameError("invalid content-length")
		}
		if int(n) > r.maxBody {
			return nil, frameError("frame body too large")
		}
		f.Body = make([]byte, n)
		if _, err := io.ReadFull(r.r, f.Body); err != nil {
			return nil, unexpected(err)
		}
		b, err := r.r.ReadByte()
		if err != nil {
			return nil, unexpected(err)
		}
		if b != 0 {
			return nil, frameError("frame not terminated by NUL")
		}
		return f, nil
	}
	var body []byte
	for {
		chunk, err := r.r.ReadSlice(0)
		if len(body)+len(chunk) > r.maxBody+1 {
			return nil, frameError("frame body too large")
		}
		body = append(body, chunk...)
		if err == nil {
			break
		}
		if err != bufio.ErrBufferFull {
			return nil, unexpected(err)
		}
	}
	f.Body = body[:len(body)-1]
	return f, nil
}

func unexpected(err error) error {
	if err == io.EOF {
		return io.ErrUnexpectedEOF
	}
	return err
}

var (
	escaper   = strings.NewReplacer(`\`, `\\`, "\r", `\r`, "\n", `\n`, ":", `\c`)
	unescaper = map[byte]byte{'r': '\r', 'n': '\n', 'c': ':', '\\': '\\'}
)

func unescape(s string) (string, bool) {
	if !strings.Contains(s, `\`) {
		return s, true
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' {
			b.WriteByte(s[i])
			continue
		}
		i++
		if i == len(s) {
			return "", false
		}
		c, ok := unescaper[s[i]]
		if !ok {
			return "", false
		}
		b.WriteByte(c)
	}
	return b.String(), true
}

// writeFrame writes f to w; the caller flushes. Header names and values are
// escaped, except in CONNECT and CONNECTED frames, as the specification says.
func writeFrame(w *bufio.Writer, f *Frame) {
	w.WriteString(f.Command)
	w.WriteByte('\n')
	escape := f.Command != "CONNECT" && f.Command != "CONNECTED"
	for _, h := range f.Headers {
		if escape {
			escaper.WriteString(w, h.Name)
			w.WriteByte(':')
			escaper.WriteString(w, h.Value)
		} else {
			w.WriteString(h.Name)
			w.WriteByte(':')
			w.WriteString(h.Value)
		}
		w.WriteByte('\n')
	}
	w.WriteByte('\n')
	w.Write(f.Body)
	w.WriteByte(0)
}
