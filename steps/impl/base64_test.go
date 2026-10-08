package impl

import (
	"context"
	"strings"
	"testing"

	"dif/message"
	stepdef "dif/steps/definition"
)

func TestBase64RoundTrip(t *testing.T) {
	for _, body := range []any{"<persons>\n\t<person>John Doe</person>\n</persons>", "", "é ✓", []byte{0x68, 0x69}, 12345} {
		encoded := process(t, "texttobase64", nil, message.New(body))
		if s := encoded[message.Body].(string); strings.ContainsAny(s, "\r\n") {
			t.Errorf("encoded %q has line breaks", s)
		}
		decoded := process(t, "base64totext", nil, encoded)
		if want := string(bytesOf(body)); decoded[message.Body] != want {
			t.Errorf("round trip of %v = %q, want %q", body, decoded[message.Body], want)
		}
	}
}

func TestBase64Encoding(t *testing.T) {
	if got := process(t, "texttobase64", nil, message.New("12345"))[message.Body]; got != "MTIzNDU=" {
		t.Errorf("texttobase64(12345) = %v, want MTIzNDU=", got)
	}
	for _, encoded := range []string{"MTIzNDU=", "MTIzNDU", "MTIz\r\nNDU=\n", " MTIzNDU= "} {
		if got := process(t, "base64totext", nil, message.New(encoded))[message.Body]; got != "12345" {
			t.Errorf("base64totext(%q) = %v, want 12345", encoded, got)
		}
	}
}

func TestBase64ToTextInvalid(t *testing.T) {
	p := mustProcessor(t, stepdef.Action, "base64totext", nil).(stepdef.ActionProcessor)
	if _, err := p.Process(context.Background(), message.New("not base64!")); err == nil || !strings.Contains(err.Error(), "body is not valid base64") {
		t.Errorf("err = %v, want invalid base64", err)
	}
	wantInvalid(t, stepdef.Action, "base64totext", map[string]any{"charset": "utf-8"}, "unknown option charset")
	wantInvalid(t, stepdef.Action, "texttobase64", map[string]any{"lineLength": 76}, "unknown option lineLength")
}

func TestBase64ToBinaryKeepsTheBytes(t *testing.T) {
	want := []byte{0x25, 0x50, 0x44, 0x46, 0xff, 0xfe, 0x00, 0x80} // "%PDF" and bytes that are no UTF-8
	encoded := process(t, "binarytobase64", nil, message.New(want))
	if encoded[message.Body] != "JVBERv/+AIA=" {
		t.Fatalf("binarytobase64 = %v", encoded[message.Body])
	}
	for _, in := range []string{"JVBERv/+AIA=", "JVBERv/+AIA", "JVBE\r\nRv/+AIA=\n"} {
		got, ok := process(t, "base64tobinary", nil, message.New(in))[message.Body].([]byte)
		if !ok || string(got) != string(want) {
			t.Errorf("base64tobinary(%q) = %v, want the bytes %v", in, got, want)
		}
	}
	p := mustProcessor(t, stepdef.Action, "base64tobinary", nil).(stepdef.ActionProcessor)
	if _, err := p.Process(context.Background(), message.New("not base64!")); err == nil || !strings.Contains(err.Error(), "body is not valid base64") {
		t.Errorf("err = %v, want invalid base64", err)
	}
}

func TestSetBodyAsString(t *testing.T) {
	for _, tt := range []struct {
		in   any
		want string
	}{
		{[]byte("<a/>"), "<a/>"},
		{"already", "already"},
		{nil, ""},
		{map[string]any{"a": 1.0}, `{"a":1}`},
		{[]any{"x", 2.0}, `["x",2]`},
	} {
		m := message.New(nil)
		m[message.Body] = tt.in
		if got := process(t, "setbodyasstring", nil, m)[message.Body]; got != tt.want {
			t.Errorf("setbodyasstring(%v) = %#v, want %q", tt.in, got, tt.want)
		}
	}
}
