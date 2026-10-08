package engine

import (
	"context"
	"errors"
	"testing"

	"dif/message"
	stepdef "dif/steps/definition"
)

func TestRunRecordsMetadata(t *testing.T) {
	var ran []string
	f := linear(&ran, nil)
	f.ID = "f"
	in := message.New("x")
	in[message.Trail] = "flow:prev sink:q" // as a message from another flow has it

	res, err := Run(context.Background(), f, in)
	if err != nil {
		t.Fatal(err)
	}
	m := res.Message
	if got := m[message.Trail]; got != "flow:prev sink:q flow:f source:a action:b sink:c" {
		t.Errorf("trail = %q, want it continued with this flow", got)
	}
	if m[message.Step] != "c" || m[message.OriginalBody] != "x" {
		t.Errorf("step = %v, original body = %v, want c and x", m[message.Step], m[message.OriginalBody])
	}
}

func TestRunTrailIsPerBranch(t *testing.T) {
	var ran []string
	both := func(m message.Message) []stepdef.Route {
		return []stepdef.Route{{Next: 0, Message: m.Copy()}, {Next: 1, Message: m}}
	}
	res, err := Run(context.Background(), routed(&ran, nil, both), message.New("-"))
	if err != nil {
		t.Fatal(err)
	}
	// The copy that went through x and y does not add to the trail of the outcome.
	if got := res.Message[message.Trail]; got != "source:a router:r action:z" {
		t.Errorf("trail = %q", got)
	}
}

func TestRunTrailThroughErrorRoute(t *testing.T) {
	var ran []string
	res, err := Run(context.Background(), withError(&ran, tagger{&ran, "x", errors.New("boom")}, 0), message.New("-"))
	if err != nil {
		t.Fatal(err)
	}
	m := res.Message
	if got := m[message.Trail]; got != "source:a action:x error:h action:z" || m[message.Step] != "z" {
		t.Errorf("trail = %q, step = %v, want the failed step, the error handler and z", got, m[message.Step])
	}
	if m[message.OriginalBody] != "-" {
		t.Errorf("original body = %v, want -", m[message.OriginalBody])
	}
}
