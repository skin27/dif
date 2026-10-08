package impl

import (
	"testing"

	"dif/message"
	stepdef "dif/steps/definition"
)

func TestEncoder(t *testing.T) {
	tests := []struct {
		from, to string
		body     any
		want     string
	}{
		{"UTF-8", "UTF-8", "Joyeux Noël ", "Joyeux Noël "},
		{"UTF-8", "ISO-8859-1", "Noël ✓", "No\xebl ?"},
		{"iso-8859-1", "utf-8", []byte("No\xebl"), "Noël"},
		{"UTF-8", "US-ASCII", "Noël", "No?l"},
		{"US-ASCII", "UTF-8", []byte("No\xebl"), "No�l"},
		{"UTF-8", "UTF-8", []byte("a\xffb"), "a�b"},
		{"latin1", "ascii", 12345, "12345"},
	}
	for _, tt := range tests {
		out := process(t, "encoder", map[string]any{"originCharset": tt.from, "targetCharset": tt.to}, message.New(tt.body))
		if got := string(out[message.Body].([]byte)); got != tt.want {
			t.Errorf("%s -> %s of %q = %q, want %q", tt.from, tt.to, tt.body, got, tt.want)
		}
	}
}

func TestEncoderInvalid(t *testing.T) {
	wantInvalid(t, stepdef.Action, "encoder", map[string]any{"originCharset": "EBCDIC"}, `option originCharset: charset "EBCDIC" is not supported`)
	wantInvalid(t, stepdef.Action, "encoder", map[string]any{"targetCharset": "UTF-16"}, `option targetCharset: charset "UTF-16" is not supported`)
}
