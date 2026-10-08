package impl

import (
	"context"
	"testing"

	"dif/message"
	stepdef "dif/steps/definition"
)

func TestPassthrough(t *testing.T) {
	m := message.New("x")
	if out := process(t, "passthrough", nil, m); out[message.Body] != "x" || out[message.TraceID] != m[message.TraceID] {
		t.Errorf("message = %v, want it unchanged", out)
	}
}

func TestMessageSourceEmitsNothing(t *testing.T) {
	src := mustProcessor(t, stepdef.Source, "message:hello", nil).(stepdef.SourceProcessor)
	err := src.Run(context.Background(), func(message.Message, func(message.Message, error)) error {
		t.Error("message source emitted a message")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
