package impl

import (
	"bytes"
	"context"
	"log"
	"maps"
	"reflect"
	"strings"
	"testing"

	"dif/message"
	stepdef "dif/steps/definition"
)

func TestLog(t *testing.T) {
	tests := []struct {
		name      string
		opts      map[string]any
		want, not []string
	}{
		{"defaults", nil,
			[]string{"step test-step: traceid=abc\n"}, []string{"headers=", "body="}},
		{"headers", map[string]any{"showHeaders": true},
			[]string{"traceid=abc headers={greeting=hello, metadata.timestamp=now, n=42}\n"}, []string{"body="}},
		{"body", map[string]any{"showBody": "true"},
			[]string{"traceid=abc body=hi\n"}, []string{"headers="}},
		{"both", map[string]any{"showHeaders": true, "showBody": true, "showException": true},
			[]string{"headers={greeting=hello, metadata.timestamp=now, n=42} body=hi\n"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buf := captureLog(t)
			in := message.Message{message.TraceID: "abc", message.Timestamp: "now", message.Body: "hi", message.OriginalBody: "hi", "greeting": "hello", "n": 42}
			want := maps.Clone(in)

			out := process(t, "log", tt.opts, in)

			if !reflect.DeepEqual(out, want) {
				t.Errorf("message = %v, want it unchanged: %v", out, want)
			}
			for _, w := range tt.want {
				if !strings.Contains(buf.String(), w) {
					t.Errorf("log = %q, want containing %q", buf, w)
				}
			}
			for _, n := range tt.not {
				if strings.Contains(buf.String(), n) {
					t.Errorf("log = %q, want not containing %q", buf, n)
				}
			}
		})
	}
}

func TestLogInvalid(t *testing.T) {
	wantInvalid(t, stepdef.Action, "log", map[string]any{"showBody": "yes"}, `option showBody: want boolean, got "yes"`)
	wantInvalid(t, stepdef.Action, "log", map[string]any{"level": "INFO"}, "unknown option level")
}

func TestLogUsesFlowLogger(t *testing.T) {
	global := captureLog(t)
	var flow bytes.Buffer
	ctx := stepdef.WithLogger(context.Background(), log.New(&flow, "", 0))

	p := mustProcessor(t, stepdef.Action, "log", map[string]any{"showBody": true}).(stepdef.ActionProcessor)
	if _, err := p.Process(ctx, message.Message{message.TraceID: "abc", message.Body: "hi"}); err != nil {
		t.Fatal(err)
	}
	if flow.String() != "step test-step: traceid=abc body=hi\n" {
		t.Errorf("flow log = %q", flow.String())
	}
	if global.Len() != 0 {
		t.Errorf("standard log = %q, want nothing", global.String())
	}
}
