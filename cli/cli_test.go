package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestRun(t *testing.T) {
	tests := []struct {
		name string
		args []string
		code int
		out  string
	}{
		{"hello", []string{"run", "../examples/hello.json"}, 0, "flow has been executed in"},
		{"no args", nil, 2, ""},
		{"missing file", []string{"run", "nope.json"}, 1, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := Run(tt.args, &stdout, &stderr); code != tt.code {
				t.Errorf("exit code = %d, want %d (stderr: %s)", code, tt.code, stderr.String())
			}
			if !strings.Contains(stdout.String(), tt.out) {
				t.Errorf("stdout = %q, want containing %q", stdout.String(), tt.out)
			}
		})
	}
}
