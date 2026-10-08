package impl

import (
	"testing"

	"dif/message"
	stepdef "dif/steps/definition"
)

func TestSetHeader(t *testing.T) {
	tests := []struct {
		name   string
		opts   map[string]any
		header string
		want   string
	}{
		{"default is simple", map[string]any{"name": "copy", "value": "${body}"}, "copy", "hello"},
		{"constant", map[string]any{"name": "lastName", "language": "constant", "value": "${body}"}, "lastName", "${body}"},
		{"from other header", map[string]any{"name": "full", "value": "${header.greeting} world"}, "full", "hi world"},
		{"empty value", map[string]any{"name": "empty"}, "empty", ""},
		{"overwrites", map[string]any{"name": "greeting", "value": "bye"}, "greeting", "bye"},
		{"case-sensitive", map[string]any{"name": "Greeting", "value": "HI"}, "Greeting", "HI"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := message.New("hello")
			m["greeting"] = "hi"
			out := process(t, "setheader", tt.opts, m)
			if out[tt.header] != tt.want {
				t.Errorf("header %s = %#v, want %q", tt.header, out[tt.header], tt.want)
			}
			if out[message.Body] != "hello" {
				t.Errorf("body = %v, want it unchanged", out[message.Body])
			}
			if tt.header == "Greeting" && out["greeting"] != "hi" {
				t.Error("setting Greeting changed greeting")
			}
		})
	}
}

func TestSetHeaderInvalid(t *testing.T) {
	wantInvalid(t, stepdef.Action, "setheader", map[string]any{"value": "x"}, "missing required option name")
	wantInvalid(t, stepdef.Action, "setheader", map[string]any{"name": ""}, "header name is empty")
	wantInvalid(t, stepdef.Action, "setheader", map[string]any{"name": "body"}, `"body" is reserved for the body`)
	wantInvalid(t, stepdef.Action, "setheader", map[string]any{"name": "metadata.traceid"}, "reserved for metadata")
	wantInvalid(t, stepdef.Action, "setheader", map[string]any{"name": "x", "language": "groovy"}, "option language")
	wantInvalid(t, stepdef.Action, "setheader", map[string]any{"name": "x", "value": "${exchangeId}"}, "unsupported simple expression")
	wantInvalid(t, stepdef.Action, "setheader", map[string]any{"path": "x"}, "unknown option path")
}
