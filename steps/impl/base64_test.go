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
