package impl

import (
	"context"
	"testing"

	"dif/message"
	stepdef "dif/steps/definition"
)

// given is a reply a return step gave through stepdef.Return.
type given struct {
	msg    message.Message
	err    error
	detach bool
	count  int
}

// returnTo runs m through the return step with opts, in an exchange whose
// sender takes the first reply. It returns the message the step passes on.
func returnTo(t *testing.T, opts map[string]any, m message.Message) (message.Message, *given) {
	t.Helper()
	g := &given{}
	ctx := stepdef.WithReturn(context.Background(), func(m message.Message, err error, detach bool) bool {
		g.count++
		if g.count > 1 {
			return false
		}
		g.msg, g.err, g.detach = m, err, detach
		return true
	})
	p := mustProcessor(t, stepdef.Action, "return", opts).(stepdef.ActionProcessor)
	out, err := p.Process(ctx, m)
	if err != nil {
		t.Fatal(err)
	}
	return out, g
}

func TestReturnStep(t *testing.T) {
	tests := []struct {
		name   string
		opts   map[string]any
		body   string // the reply's body
		err    string // the reply's failure, "" if none
		detach bool
	}{
		{"the message as it is", nil, "hello", "", false},
		{"an expression", map[string]any{"expression": "got ${header.id}"}, "got 42", "", false},
		{"a constant", map[string]any{"language": "constant", "expression": "${header.id}"}, "${header.id}", "", false},
		{"blank expression", map[string]any{"expression": "  "}, "hello", "", false},
		{"detached", map[string]any{"detach": true, "expression": "now"}, "now", "", true},
		{"a failure with a text", map[string]any{"status": "error", "expression": "denied"}, "denied", "denied", false},
		{"a failure with the body as text", map[string]any{"status": "error"}, "hello", "hello", false},
		{"a failure without text", map[string]any{"status": "error", "expression": "${header.none}"}, "", "returned an error", false},
		{"a detached failure", map[string]any{"status": "error", "detach": true, "expression": "no"}, "no", "no", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := message.New("hello")
			m["id"] = "42"
			out, g := returnTo(t, tt.opts, m)

			if out[message.Body] != "hello" || out["id"] != "42" {
				t.Errorf("passed on %v; the message must pass on unchanged", out)
			}
			if g.count != 1 || g.msg == nil || g.msg[message.Body] != tt.body || g.msg["id"] != "42" || g.detach != tt.detach {
				t.Fatalf("reply = %v (detach %v, %d times); want body %q, detach %v, once", g.msg, g.detach, g.count, tt.body, tt.detach)
			}
			if tt.err == "" && g.err != nil || tt.err != "" && (g.err == nil || g.err.Error() != tt.err) {
				t.Errorf("reply error = %v, want %q", g.err, tt.err)
			}
		})
	}
}

// TestReturnStepReplyIsACopy: what later steps do to the message does not
// reach the reply.
func TestReturnStepReplyIsACopy(t *testing.T) {
	m := message.New("hello")
	out, g := returnTo(t, nil, m)
	out[message.Body] = "changed"
	out["new"] = true
	if g.msg[message.Body] != "hello" || g.msg["new"] != nil {
		t.Errorf("reply = %v; want it independent of the message", g.msg)
	}
}

// TestReturnStepWithoutSender: with nobody waiting, the step passes the message on.
func TestReturnStepWithoutSender(t *testing.T) {
	m := message.New("hello")
	out := process(t, "return", map[string]any{"expression": "x", "status": "error"}, m)
	if out[message.Body] != "hello" {
		t.Errorf("body = %v, want hello", out[message.Body])
	}
}

func TestReturnStepOptions(t *testing.T) {
	wantInvalid(t, stepdef.Action, "return", map[string]any{"status": "maybe"}, `option status: "maybe" is not one of "success", "error"`)
	wantInvalid(t, stepdef.Action, "return", map[string]any{"language": "groovy"}, "option language")
	wantInvalid(t, stepdef.Action, "return", map[string]any{"detach": "sometimes"}, "option detach")
	wantInvalid(t, stepdef.Action, "return", map[string]any{"value": "x"}, "unknown option value")
	wantInvalid(t, stepdef.Action, "return", map[string]any{"expression": "${body"}, "unclosed ${")
}
