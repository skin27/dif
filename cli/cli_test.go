package cli

import (
	"bytes"
	"strings"
	"testing"
)

func TestRun(t *testing.T) {
	tests := []struct {
		name  string
		args  []string
		stdin string
		code  int
		out   []string
	}{
		{"hello", []string{"start", "../examples/hello.json"}, "send\nsend bye\nexit\n", 0, []string{
			"DIF CLI: flow ../examples/hello.json is started.",
			"> send\n",
			`  message 1: {`, `"body":"HELLO WORLD"`,
			"    trail: source:hello-source -> action:hello-action -> sink:hello-sink",
			`"body":"bye"`,
			"> exit\n  exit: 2 messages processed, 0 failed\n",
		}},
		{"stop keeps the CLI", []string{"start", "../examples/hello.json"}, "stop\nstatus\nsend\nstart\nsend\nexit\n", 0, []string{
			"> stop\n  flow stopped\n",
			"> status\n  flow is stopped: 0 messages processed, 0 failed\n",
			"> send\n  error: cannot send: flow is stopped\n",
			"> start\n  flow started\n",
			`"body":"HELLO WORLD"`,
			"exit: 1 messages processed, 0 failed",
		}},
		{"pause and status", []string{"start", "../examples/hello.json"}, "pause\nsend\nstatus\nresume\nexit\n", 0, []string{
			"> pause\n  flow paused\n",
			"> send\n  error: cannot send: flow is paused\n",
			"> status\n  flow is paused: 0 messages processed, 0 failed\n",
			"> resume\n  flow started\n",
		}},
		{"help and unknown", []string{"start", "../examples/hello.json"}, "help\nquit\nexit\n", 0, []string{
			"> help\n  commands:\n",
			"    send [body]",
			"    stop ",
			"    exit ",
			"> quit\n  unknown command \"quit\"",
		}},
		{"timer", []string{"start", "../examples/timer.json"}, "status\nexit\n", 0, []string{
			"> status\n  flow is started",
			"exit:",
		}},
		{"no args", nil, "", 2, nil},
		{"run removed", []string{"run", "../examples/hello.json"}, "", 2, nil},
		{"missing file", []string{"start", "nope.json"}, "", 1, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := Run(tt.args, strings.NewReader(tt.stdin), &stdout, &stderr); code != tt.code {
				t.Errorf("exit code = %d, want %d (stderr: %s)", code, tt.code, stderr.String())
			}
			for _, want := range tt.out {
				if !strings.Contains(stdout.String(), want) {
					t.Errorf("stdout = %q\nwant containing %q", stdout.String(), want)
				}
			}
		})
	}
}
