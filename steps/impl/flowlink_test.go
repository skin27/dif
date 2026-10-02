package impl

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"dif/message"
	stepdef "dif/steps/definition"
)

// target runs a flowlink source for flow id; handle makes the outcome of
// each message it emits. It stops when the test ends.
func target(t *testing.T, id string, handle func(m message.Message) (message.Message, error)) {
	t.Helper()
	src := mustProcessor(t, stepdef.Source, "flowlink", map[string]any{"flowId": id, "transport": "async"}).(stepdef.SourceProcessor)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	t.Cleanup(func() { cancel(); <-done })
	go func() {
		defer close(done)
		src.Run(ctx, func(m message.Message, reply func(message.Message, error)) error {
			out, err := handle(m)
			if reply != nil {
				reply(out, err)
			}
			return nil
		})
	}()
}

func link(t *testing.T, opts map[string]any) stepdef.ActionProcessor {
	t.Helper()
	return mustProcessor(t, stepdef.Action, "flowlink", opts).(stepdef.ActionProcessor)
}

func TestFlowLinkAsync(t *testing.T) {
	id := t.Name()
	m := message.New("Test")
	out, err := link(t, map[string]any{"targetFlowId": id, "transport": "async", "exchangePattern": "InOnly"}).Process(context.Background(), m)
	if err != nil || !sameMessage(out, m) {
		t.Fatalf("out = %v, err = %v; want the message on at once", out, err)
	}
	m[message.Body] = "changed later"

	// The target flow was not started yet: the message waits for it.
	got := make(chan message.Message, 1)
	target(t, id, func(m message.Message) (message.Message, error) { got <- m; return m, nil })
	select {
	case r := <-got:
		if r[message.Body] != "Test" || r[message.TraceID] != m[message.TraceID] {
			t.Errorf("target got %v, want a copy with the trace id", r)
		}
	case <-time.After(time.Second):
		t.Fatal("the target flow got nothing")
	}
}

func TestFlowLinkInOut(t *testing.T) {
	id := t.Name()
	target(t, id, func(m message.Message) (message.Message, error) {
		m[message.Body] = "reply to " + m[message.Body].(string)
		return m, nil
	})
	for _, transport := range []string{"sync", "async"} {
		m := message.New("ping")
		out, err := link(t, map[string]any{"targetFlowId": id, "transport": transport, "exchangePattern": "InOut"}).Process(context.Background(), m)
		if err != nil || out[message.Body] != "reply to ping" || m[message.Body] != "ping" {
			t.Errorf("%s: out = %v, err = %v; want the target's outcome", transport, out, err)
		}
	}
}

func TestFlowLinkSyncInOnly(t *testing.T) {
	id := t.Name()
	boom := errors.New("boom")
	target(t, id, func(m message.Message) (message.Message, error) {
		if m[message.Body] == "bad" {
			return nil, boom
		}
		m[message.Body] = "changed by the target"
		return m, nil
	})
	a := link(t, map[string]any{"targetFlowId": id, "exchangePattern": "InOnly"}) // transport sync
	m := message.New("good")
	if out, err := a.Process(context.Background(), m); err != nil || out[message.Body] != "good" {
		t.Errorf("out = %v, err = %v; want the message on unchanged", out, err)
	}
	if _, err := a.Process(context.Background(), message.New("bad")); !errors.Is(err, boom) || !strings.Contains(err.Error(), "flow "+id+": boom") {
		t.Errorf("err = %v, want the target's failure", err)
	}
}

func TestFlowLinkTimeout(t *testing.T) {
	id := t.Name()
	a := link(t, map[string]any{"targetFlowId": id, "requestTimeout": 30})
	if _, err := a.Process(context.Background(), message.New("x")); err == nil || err.Error() != "flow "+id+" did not reply within 30ms" {
		t.Errorf("err = %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if e, err := linkEndpoint(id).take(ctx); err == nil {
		t.Errorf("took %v, want the message dropped once its sender gave up", e.m)
	}

	ctx, cancel = context.WithCancel(context.Background())
	go func() { time.Sleep(20 * time.Millisecond); cancel() }()
	if _, err := link(t, map[string]any{"targetFlowId": id}).Process(ctx, message.New("x")); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want the context's error", err)
	}
}

func TestFlowLinkInvalid(t *testing.T) {
	wantInvalid(t, stepdef.Action, "flowlink", nil, "missing required option targetFlowId")
	wantInvalid(t, stepdef.Action, "flowlink", map[string]any{"targetFlowId": ""}, "option targetFlowId: empty flow id")
	wantInvalid(t, stepdef.Action, "flowlink", map[string]any{"targetFlowId": "x", "transport": "activemq"}, `option transport: "activemq" is not one of`)
	wantInvalid(t, stepdef.Source, "flowlink", nil, "missing required option flowId")
	wantInvalid(t, stepdef.Source, "flowlink", map[string]any{"flowId": ""}, "option flowId: empty flow id")
}
