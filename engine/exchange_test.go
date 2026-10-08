package engine

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	flowdef "dif/flows/definition"
	"dif/message"
	stepdef "dif/steps/definition"
)

// oneWay sets the body and makes the exchange one-way, as setoneway does.
type oneWay struct{ body string }

func (o oneWay) Process(_ context.Context, m message.Message) (message.Message, error) {
	m[message.Body], m[message.ExchangePattern] = o.body, message.InOnly
	return m, nil
}

// gate waits until release is closed, then sets the body to "late" and fails with err.
type gate struct {
	release chan struct{}
	err     error
}

func (g gate) Process(ctx context.Context, m message.Message) (message.Message, error) {
	select {
	case <-g.release:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	m[message.Body] = "late"
	return m, g.err
}

func started(t *testing.T, f *flowdef.Flow, onResult func(*Result, error)) *Runner {
	t.Helper()
	r := NewRunner(f, onResult)
	must(t, r.Start())
	t.Cleanup(func() { r.ForceStop() })
	return r
}

func TestRequest(t *testing.T) {
	r := started(t, flow(nil), nil)
	out, err := r.Request(context.Background(), message.New("ok"))
	if err != nil || out[message.Body] != "ok" || out[message.Trail] != "source:s sink:f" {
		t.Errorf("reply = %v, %v; want the message at the end of the flow", out, err)
	}
	if _, err := r.Request(context.Background(), message.New("fail")); err == nil || !strings.Contains(err.Error(), "step f: boom") {
		t.Errorf("err = %v, want the failure", err)
	}

	must(t, r.Stop())
	if _, err := r.Request(context.Background(), message.New("x")); err == nil || err.Error() != "cannot send: flow is stopped" {
		t.Errorf("err = %v, want cannot send", err)
	}
}

func TestRequestTimeout(t *testing.T) {
	release := make(chan struct{})
	defer close(release)
	g := &flowdef.Node{ID: "g", Kind: flowdef.Action, Processor: gate{release, nil}}
	r := started(t, &flowdef.Flow{ID: "slow", Source: &flowdef.Node{ID: "s", Kind: flowdef.Source, Next: []*flowdef.Node{g}}}, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := r.Request(ctx, message.New("x")); !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "flow slow did not reply") {
		t.Errorf("err = %v, want a timeout", err)
	}
}

// TestRequestOneWay: the sender gets its reply where the exchange becomes
// one-way, and what happens to the message after that does not reach it.
func TestRequestOneWay(t *testing.T) {
	release := make(chan struct{})
	g := &flowdef.Node{ID: "g", Kind: flowdef.Action, Processor: gate{release, errors.New("boom")}}
	o := &flowdef.Node{ID: "o", Kind: flowdef.Action, Processor: oneWay{"early"}, Next: []*flowdef.Node{g}}
	results := make(chan result, 1)
	r := started(t, &flowdef.Flow{Source: &flowdef.Node{ID: "s", Kind: flowdef.Source, Next: []*flowdef.Node{o}}},
		func(res *Result, err error) { results <- result{res, err} })

	out, err := r.Request(context.Background(), message.New("x")) // returns while g still waits
	if err != nil || out[message.Body] != "early" {
		t.Fatalf("reply = %v, %v; want the message at the one-way step", out, err)
	}
	close(release)
	if res := next(t, results); res.err == nil || !strings.Contains(res.err.Error(), "step g: boom") {
		t.Errorf("result = %v, want the flow to go on and fail after the reply", res.err)
	}
	if out[message.Body] != "early" {
		t.Errorf("reply body = %v; the flow changed the reply", out[message.Body])
	}
}

func TestOneWayFirstBranchReplies(t *testing.T) {
	x := &flowdef.Node{ID: "x", Kind: flowdef.Action, Processor: oneWay{"x"}}
	z := &flowdef.Node{ID: "z", Kind: flowdef.Action, Processor: oneWay{"z"}}
	both := routerStub{func(m message.Message) []stepdef.Route {
		return []stepdef.Route{{Next: 0, Message: m.Copy()}, {Next: 1, Message: m}}
	}}
	rt := &flowdef.Node{ID: "r", Kind: flowdef.Router, Processor: both, Next: []*flowdef.Node{x, z}}
	f := &flowdef.Flow{Source: &flowdef.Node{ID: "a", Kind: flowdef.Source, Next: []*flowdef.Node{rt}}}

	var replies []string
	res, err := execute(context.Background(), f, message.New("-"), func(m message.Message, _ error) {
		replies = append(replies, m[message.Body].(string))
	})
	if err != nil || res.Message[message.Body] != "z" {
		t.Fatalf("outcome = %v, %v", res, err)
	}
	if strings.Join(replies, " ") != "x" {
		t.Errorf("replies = %v, want one, from the first branch", replies)
	}
}

func TestExchangePatternIsPerFlow(t *testing.T) {
	var ran []string
	z := &flowdef.Node{ID: "z", Kind: flowdef.Action, Processor: tagger{&ran, "z", nil}}
	x := &flowdef.Node{ID: "x", Kind: flowdef.Action, Processor: tagger{&ran, "x", nil}, Next: []*flowdef.Node{z}}
	f := &flowdef.Flow{Source: &flowdef.Node{ID: "a", Kind: flowdef.Source, Next: []*flowdef.Node{x}}}

	in := message.New("-")
	in[message.ExchangePattern] = message.InOnly // from an exchange in another flow
	var reply message.Message
	if _, err := execute(context.Background(), f, in, func(m message.Message, _ error) { reply = m }); err != nil {
		t.Fatal(err)
	}
	if reply[message.Body] != "-xz" {
		t.Errorf("reply body = %v, want -xz: the message's earlier exchange pattern must not apply here", reply[message.Body])
	}
}
