package engine

import (
	"context"
	"errors"
	"strings"
	"testing"

	flowdef "dif/flows/definition"
	"dif/message"
	stepdef "dif/steps/definition"
)

// gatherStub sends copies along both links (the second detached), records
// the outcomes it gets and returns what gather makes of them.
type gatherStub struct {
	got    *[]stepdef.Outcome
	gather func(m message.Message, o []stepdef.Outcome) ([]stepdef.Route, error)
}

func (g gatherStub) Route(_ context.Context, m message.Message) ([]stepdef.Route, error) {
	return []stepdef.Route{{Next: 0, Message: m.Copy()}, {Next: 1, Message: m.Copy(), Detached: true}}, nil
}

func (g gatherStub) Gather(_ context.Context, m message.Message, o []stepdef.Outcome) ([]stepdef.Route, error) {
	*g.got = o
	return g.gather(m, o)
}

// gathered builds source a -> router r with links to x -> y and to z, where
// r is a gatherStub; x fails with xErr.
func gathered(ran *[]string, xErr error, got *[]stepdef.Outcome, gather func(message.Message, []stepdef.Outcome) ([]stepdef.Route, error)) *flowdef.Flow {
	f := routed(ran, xErr, nil)
	f.Source.Next[0].Processor = gatherStub{got, gather}
	return f
}

func TestRunGather(t *testing.T) {
	var ran []string
	var got []stepdef.Outcome
	f := gathered(&ran, nil, &got, func(m message.Message, o []stepdef.Outcome) ([]stepdef.Route, error) {
		m[message.Body] = "merged(" + o[0].Message[message.Body].(string) + ")"
		return []stepdef.Route{{Next: 1, Message: m}}, nil
	})
	res, err := Run(context.Background(), f, message.New("-"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Err != nil || got[0].Message[message.Body] != "-x" {
		t.Errorf("outcomes = %v, want only the route that is not detached", got)
	}
	if res.Message[message.Body] != "merged(-x)z" {
		t.Errorf("body = %v, want the gathered message through z", res.Message[message.Body])
	}
	if got := strings.Join(res.Trail, " "); got != "source:a router:r action:x sink:y action:z action:z" {
		t.Errorf("trail = %q", got)
	}
}

func TestRunGatherErrors(t *testing.T) {
	boom := errors.New("boom")

	// A failed route is an outcome, not the end of the message.
	var ran []string
	var got []stepdef.Outcome
	f := gathered(&ran, boom, &got, func(m message.Message, o []stepdef.Outcome) ([]stepdef.Route, error) {
		return []stepdef.Route{{Next: 1, Message: m}}, nil
	})
	res, err := Run(context.Background(), f, message.New("-"))
	if err != nil || len(got) != 1 || !errors.Is(got[0].Err, boom) || got[0].Message != nil {
		t.Fatalf("err = %v, outcomes = %v; want the failure as an outcome", err, got)
	}
	if res.Message[message.Body] != "-z" {
		t.Errorf("body = %v", res.Message[message.Body])
	}

	// Gather returning the route's error keeps the failed step and its message
	// for the error route.
	f = gathered(&ran, boom, &got, func(m message.Message, o []stepdef.Outcome) ([]stepdef.Route, error) {
		return nil, o[0].Err
	})
	f.Error = &flowdef.ErrorHandler{ID: "h", Route: &flowdef.Node{ID: "e", Kind: flowdef.Action, Processor: tagger{&ran, "e", nil}}}
	res, err = Run(context.Background(), f, message.New("-"))
	if err != nil || res.Message[ErrorStep] != "x" || res.Message[message.Body] != "-xe" {
		t.Errorf("err = %v, message = %v; want step x's message through the error route", err, res.Message)
	}

	// Any other Gather error is the router's.
	f = gathered(&ran, nil, &got, func(m message.Message, o []stepdef.Outcome) ([]stepdef.Route, error) {
		return nil, errors.New("cannot merge")
	})
	if _, err := Run(context.Background(), f, message.New("-")); err == nil || err.Error() != "step r: cannot merge" {
		t.Errorf("err = %v, want step r: cannot merge", err)
	}
}
