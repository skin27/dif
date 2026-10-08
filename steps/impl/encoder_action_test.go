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
		{"ISO8859_1", "UTF-8", []byte("No\xebl"), "Noël"},
		{"CP1252", "UTF-8", []byte("\x80 \x93q\x94 \x99 \xe9"), "€ “q” ™ é"},
		{"UTF-8", "CP1252", "€ “q” ™ é ✓", "\x80 \x93q\x94 \x99 \xe9 ?"},
		{"windows-1252", "UTF-8", []byte("\x81\x8d\x8f\x90\x9d"), "\ufffd\ufffd\ufffd\ufffd\ufffd"},
		{"UTF-8", "windows-1252", "\ufffd", "?"},
	}
	for _, tt := range tests {
		out := process(t, "encoder", map[string]any{"originCharset": tt.from, "targetCharset": tt.to}, message.New(tt.body))
		if got := string(out[message.Body].([]byte)); got != tt.want {
			t.Errorf("%s -> %s of %q = %q, want %q", tt.from, tt.to, tt.body, got, tt.want)
		}
	}
}

// TestEncoderWindows1252 checks every byte of windows-1252 against its character
// and back, with the five undefined bytes decoding to U+FFFD.
func TestEncoderWindows1252(t *testing.T) {
	undefined := map[byte]bool{0x81: true, 0x8D: true, 0x8F: true, 0x90: true, 0x9D: true}
	for b := 0; b < 256; b++ {
		in := message.New([]byte{byte(b)})
		utf := string(process(t, "encoder", map[string]any{"originCharset": "CP1252"}, in)[message.Body].([]byte))
		if undefined[byte(b)] {
			if utf != "\ufffd" {
				t.Errorf("byte %#x = %q, want U+FFFD", b, utf)
			}
			continue
		}
		back := process(t, "encoder", map[string]any{"targetCharset": "CP1252"}, message.New(utf))[message.Body].([]byte)
		if len(back) != 1 || back[0] != byte(b) {
			t.Errorf("byte %#x -> %q -> %v, want it back", b, utf, back)
		}
	}
}

func TestEncoderInvalid(t *testing.T) {
	wantInvalid(t, stepdef.Action, "encoder", map[string]any{"originCharset": "EBCDIC"}, `option originCharset: charset "EBCDIC" is not supported`)
	wantInvalid(t, stepdef.Action, "encoder", map[string]any{"targetCharset": "UTF-16"}, `option targetCharset: charset "UTF-16" is not supported`)
}
