package impl

import (
	"maps"
	"slices"
	"strings"
	"testing"

	"dif/message"
	stepdef "dif/steps/definition"
)

func TestRemoveHeaders(t *testing.T) {
	tests := []struct {
		name string
		opts map[string]any
		want string // headers left, sorted
	}{
		{"wildcard with exclude", map[string]any{"pattern": "last*", "excludePattern": "lastName"}, "Content-Type Correlation-Id Message-Id firstName lastName"},
		{"exact, case-insensitive", map[string]any{"pattern": "content-type"}, "Correlation-Id Message-Id firstName lastName lastVersion"},
		{"regex", map[string]any{"pattern": "first.*|Content-.*"}, "Correlation-Id Message-Id lastName lastVersion"},
		{"regex exclude", map[string]any{"pattern": "*", "excludePattern": "last(Name|Version)"}, "lastName lastVersion"},
		{"everything", map[string]any{"pattern": "*"}, ""},
		{"no match", map[string]any{"pattern": "none"}, "Content-Type Correlation-Id Message-Id firstName lastName lastVersion"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := message.New("x")
			m["firstName"], m["lastName"], m["lastVersion"], m["Content-Type"] = "John", "Doe", "123", "text/plain"
			out := process(t, "removeheaders", tt.opts, m)
			var left []string
			for _, k := range slices.Sorted(maps.Keys(out)) {
				if k != message.Body && !message.IsMetadata(k) {
					left = append(left, k)
				}
			}
			if got := strings.Join(left, " "); got != tt.want {
				t.Errorf("headers left = %q, want %q", got, tt.want)
			}
			if out[message.Body] != "x" || out[message.TraceID] == nil || out[message.Timestamp] == nil {
				t.Errorf("body or metadata removed: %v", out)
			}
		})
	}
}

func TestRemoveHeadersInvalid(t *testing.T) {
	wantInvalid(t, stepdef.Action, "removeheaders", nil, "missing required option pattern")
	wantInvalid(t, stepdef.Action, "removeheaders", map[string]any{"pattern": "a("}, "option pattern: error parsing regexp")
	wantInvalid(t, stepdef.Action, "removeheaders", map[string]any{"pattern": "a", "excludePattern": "b["}, "option excludePattern: error parsing regexp")
}
