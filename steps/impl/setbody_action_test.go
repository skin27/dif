package impl

import (
	"testing"

	"dif/message"
	stepdef "dif/steps/definition"
)

func TestSetBody(t *testing.T) {
	tests := []struct {
		name string
		opts map[string]any
		body any
		want string
	}{
		{"default is constant", map[string]any{"expression": "${body}!"}, "x", "${body}!"},
		{"constant", map[string]any{"language": "constant", "expression": "${random(10)}"}, "x", "${random(10)}"},
		{"empty", nil, "x", ""},
		{"number from xml", map[string]any{"expression": 1234.0}, "x", "1234"},
		{"simple body", map[string]any{"language": "simple", "expression": "tick ${body}"}, 3, "tick 3"},
		{"simple headers", map[string]any{"language": "simple", "expression": "${header.first} ${headers.last}"}, nil, "John Doe"},
		{"simple missing header", map[string]any{"language": "simple", "expression": "[${header.none}]"}, nil, "[]"},
		{"simple json body", map[string]any{"language": "simple", "expression": "${body}"}, map[string]any{"a": 1.0}, `{"a":1}`},
		{"simple bytes body", map[string]any{"language": "simple", "expression": "${body}"}, []byte("raw"), "raw"},
		{"simple no references", map[string]any{"language": "simple", "expression": "plain"}, nil, "plain"},
		{"simple bodyAs string", map[string]any{"language": "simple", "expression": "Body: ${bodyAs(String)}"}, []byte("raw"), "Body: raw"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := message.New(tt.body)
			m["first"], m["last"] = "John", "Doe"
			out := process(t, "setbody", tt.opts, m)
			if out[message.Body] != tt.want {
				t.Errorf("body = %#v, want %q", out[message.Body], tt.want)
			}
			if out["first"] != "John" {
				t.Error("headers were changed")
			}
		})
	}
}

func TestSetBodyInvalid(t *testing.T) {
	wantInvalid(t, stepdef.Action, "setbody", map[string]any{"language": "groovy"}, `option language: "groovy" is not one of "constant", "simple"`)
	wantInvalid(t, stepdef.Action, "setbody", map[string]any{"language": "simple", "expression": "${bodyAs(Integer)}"}, "unsupported simple expression ${bodyAs(Integer)}")
	wantInvalid(t, stepdef.Action, "setbody", map[string]any{"language": "simple", "expression": "${header.}"}, "unsupported simple expression")
	wantInvalid(t, stepdef.Action, "setbody", map[string]any{"language": "simple", "expression": "${body"}, "unclosed ${")
	wantInvalid(t, stepdef.Action, "setbody", map[string]any{"value": "x"}, "unknown option value")
}
