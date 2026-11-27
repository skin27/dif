package stomp

import (
	"bufio"
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

func encode(f *Frame) []byte {
	var buf bytes.Buffer
	w := bufio.NewWriter(&buf)
	writeFrame(w, f)
	w.Flush()
	return buf.Bytes()
}

func TestFrameRoundTripEscapesHeaders(t *testing.T) {
	in := &Frame{Command: "SEND", Headers: []Header{{"destination", "/queue/a"}, {"we:ird\\name", "line\nbreak\rcarriage:colon\\slash"}}, Body: []byte("hello")}
	data := encode(in)
	if !bytes.Contains(data, []byte(`we\cird\\name:line\nbreak\rcarriage\ccolon\\slash`)) {
		t.Fatalf("not escaped: %q", data)
	}
	out, err := newReader(bytes.NewReader(data), maxLine, maxHeaders, 1024).read()
	if err != nil {
		t.Fatal(err)
	}
	if out.Command != "SEND" || len(out.Headers) != 2 || out.Headers[1] != in.Headers[1] || string(out.Body) != "hello" {
		t.Fatalf("%+v", out)
	}
}

func TestFrameConnectHeadersAreNotEscaped(t *testing.T) {
	data := encode(&Frame{Command: "CONNECT", Headers: []Header{{"passcode", `a\b:c`}}})
	if !bytes.Contains(data, []byte(`passcode:a\b:c`)) {
		t.Fatalf("%q", data)
	}
	f, err := newReader(bytes.NewReader(data), maxLine, maxHeaders, 1024).read()
	if err != nil || f.Value("passcode") != `a\b:c` {
		t.Fatal(f, err)
	}
}

func TestFrameContentLengthBodyWithNULAndCRLF(t *testing.T) {
	data := "\r\n\nSEND\r\ndestination:/queue/a\r\ncontent-length:5\r\nx:1\r\nx:2\r\n\r\na\x00b\x00c\x00\r\n\nSEND\ndestination:/queue/b\n\nplain\x00"
	r := newReader(strings.NewReader(data), maxLine, maxHeaders, 1024)
	f, err := r.read()
	if err != nil {
		t.Fatal(err)
	}
	if f.Value("destination") != "/queue/a" || f.Value("x") != "1" || string(f.Body) != "a\x00b\x00c" {
		t.Fatalf("%+v", f)
	}
	f, err = r.read()
	if err != nil || f.Value("destination") != "/queue/b" || string(f.Body) != "plain" {
		t.Fatal(f, err)
	}
	if _, err := r.read(); err != io.EOF {
		t.Fatal(err)
	}
}

func TestFrameMalformedAndOversized(t *testing.T) {
	for name, data := range map[string]string{
		"no colon":        "SEND\nnocolon\n\n\x00",
		"bad escape":      "SEND\na:b\\t\n\n\x00",
		"trailing escape": "SEND\na:b\\\n\n\x00",
		"no NUL":          "SEND\ncontent-length:2\n\nabc\x00",
		"bad length":      "SEND\ncontent-length:-1\n\n\x00",
		"long body":       "SEND\ncontent-length:11\n\n01234567890\x00",
		"long unsized":    "SEND\n\n01234567890\x00",
		"long line":       "SEND\nh:" + strings.Repeat("x", 100) + "\n\n\x00",
		"many headers":    "SEND\n" + strings.Repeat("h:v\n", 4) + "\n\x00",
	} {
		_, err := newReader(strings.NewReader(data), 64, 3, 10).read()
		var fe frameError
		if !errors.As(err, &fe) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := newReader(strings.NewReader("SEND\na:b\n"), 64, 3, 10).read(); err != io.ErrUnexpectedEOF {
		t.Fatal("truncated frame:", err)
	}
	if _, err := newReader(strings.NewReader("SEND\n\n0123456789\x00"), 64, 3, 10).read(); err != nil {
		t.Fatal("body at the limit:", err)
	}
}
