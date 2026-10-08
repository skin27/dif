package impl

import (
	"testing"

	"dif/message"
	stepdef "dif/steps/definition"
)

func TestReplace(t *testing.T) {
	const body = "<name>John Doe</name>\n<name>john smith</name>"
	tests := []struct {
		name string
		opts map[string]any
		want string
	}{
		{"case-sensitive", map[string]any{"regex": "John", "replaceWith": "Norman"}, "<name>Norman Doe</name>\n<name>john smith</name>"},
		{"flags m,i", map[string]any{"regex": "John", "flags": "m,i", "replaceWith": "Norman", "group": "0"}, "<name>Norman Doe</name>\n<name>Norman smith</name>"},
		{"multiline anchors", map[string]any{"regex": "^<name>", "flags": "m"}, "John Doe</name>\njohn smith</name>"},
		{"dot matches newline", map[string]any{"regex": "Doe.*smith", "flags": "s", "replaceWith": "-"}, "<name>John -</name>"},
		{"expand groups", map[string]any{"regex": `(\w+) (\w+)`, "replaceWith": "$2, $1"}, "<name>Doe, John</name>\n<name>smith, john</name>"},
		{"replace one group", map[string]any{"regex": `<name>(\w+) (\w+)</name>`, "flags": "i", "replaceWith": "X", "group": 2}, "<name>John X</name>\n<name>john X</name>"},
		{"delete", map[string]any{"regex": "</?name>"}, "John Doe\njohn smith"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := process(t, "replace", tt.opts, message.New(body))[message.Body]; got != tt.want {
				t.Errorf("body = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestReplaceInvalid(t *testing.T) {
	wantInvalid(t, stepdef.Action, "replace", nil, "missing required option regex")
	wantInvalid(t, stepdef.Action, "replace", map[string]any{"regex": "a("}, "option regex: error parsing regexp")
	wantInvalid(t, stepdef.Action, "replace", map[string]any{"regex": "a", "flags": "g"}, `option flags: unknown flag "g"`)
	wantInvalid(t, stepdef.Action, "replace", map[string]any{"regex": "(a)", "group": 2}, "option group: 2, but regex has 1 groups")
}
