package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	flowdef "dif/flows/definition"
	"dif/message"
	stepdef "dif/steps/definition"
)

// returning is an action that gives the sender body as its reply through
// stepdef.Return, and passes the message on unchanged. took tells whether the
// reply was taken.
type returning struct {
	body   string
	err    error
	detach bool
	took   *bool
}

func (a returning) Process(ctx context.Context, m message.Message) (message.Message, error) {
	r := m.Copy()
	r[message.Body] = a.body
	took := stepdef.Return(ctx, r, a.err, a.detach)
	if a.took != nil {
		*a.took = took
	}
	return m, nil
}

// failing is an action that always fails.
type failing struct{}

func (failing) Process(context.Context, message.Message) (message.Message, error) {
	return nil, errors.New("boom")
}

// linked builds source a -> the actions in order, with the ids n0, n1, ...
func linked(procs ...stepdef.ActionProcessor) *flowdef.Flow {
	var next *flowdef.Node
	for i := len(procs) - 1; i >= 0; i-- {
		n := &flowdef.Node{ID: fmt.Sprint("n", i), Kind: flowdef.Action, Processor: procs[i]}
		if next != nil {
			n.Next = []*flowdef.Node{next}
		}
		next = n
	}
	return &flowdef.Flow{Source: &flowdef.Node{ID: "a", Kind: flowdef.Source, Next: []*flowdef.Node{next}}}
}

// bodyOf is the body of m as text, "" for no message.
func bodyOf(m message.Message) string {
	s, _ := m[message.Body].(string)
	return s
}

// TestReturnIsGivenAtTheEnd: the reply is fixed at the return step, what the
// steps after it do to the message does not change it, and the sender gets it
// only once the flow is done.
func TestReturnIsGivenAtTheEnd(t *testing.T) {
	var ran []string
	f := linked(returning{body: "value"}, tagger{&ran, "x", nil})

	res, err := execute(context.Background(), f, message.New("-"), func(m message.Message, err error) {
		ran = append(ran, "reply:"+bodyOf(m))
		if err != nil {
			t.Errorf("reply error = %v", err)
		}
	})
	if err != nil || bodyOf(res.Message) != "-x" {
		t.Fatalf("outcome = %v, %v; want the flow to end with -x", res, err)
	}
	if got := strings.Join(ran, " "); got != "x reply:value" {
		t.Errorf("order = %q, want the reply after the last step", got)
	}
}

// TestReturnDetachRepliesNow: the sender gets the reply at the return step; a
// failure after it goes to the flow's result, not to the sender.
func TestReturnDetachRepliesNow(t *testing.T) {
	release := make(chan struct{})
	f := linked(returning{body: "early", detach: true}, gate{release, errors.New("boom")})
	results := make(chan result, 1)
	r := started(t, f, func(res *Result, err error) { results <- result{res, err} })

	out, err := r.Request(context.Background(), message.New("x")) // returns while the gate still waits
	if err != nil || bodyOf(out) != "early" {
		t.Fatalf("reply = %v, %v; want early", out, err)
	}
	close(release)
	if res := next(t, results); res.err == nil || !strings.Contains(res.err.Error(), "step n1: boom") {
		t.Errorf("result = %v, want the flow to go on and fail after the reply", res.err)
	}
}

// TestReturnFailureAfterwards: a failure of the flow after a return that waits
// replaces the returned value.
func TestReturnFailureAfterwards(t *testing.T) {
	var out message.Message
	var replyErr error
	_, err := execute(context.Background(), linked(returning{body: "value"}, failing{}), message.New("-"),
		func(m message.Message, err error) { out, replyErr = m, err })
	if err == nil || replyErr == nil || !strings.Contains(replyErr.Error(), "step n1: boom") || out != nil {
		t.Errorf("reply = %v, %v; want the failure of the flow", out, replyErr)
	}
}

// TestReturnHandledFailureAfterwards: when the error route handles the
// failure, its outcome is the reply, not the returned value.
func TestReturnHandledFailureAfterwards(t *testing.T) {
	var ran []string
	f := linked(returning{body: "value"}, failing{})
	f.Error = &flowdef.ErrorHandler{ID: "e", Route: &flowdef.Node{ID: "h", Kind: flowdef.Action, Processor: tagger{&ran, "h", nil}}}

	var out message.Message
	res, err := execute(context.Background(), f, message.New("-"), func(m message.Message, _ error) { out = m })
	if err != nil || res.Err == nil || bodyOf(out) != "-h" {
		t.Errorf("reply = %q, result = %v, %v; want the error route's outcome -h", bodyOf(out), res, err)
	}
}

// TestReturnInErrorRoute: the error route may return its own reply, such as a
// failure; the flow has still handled the error.
func TestReturnInErrorRoute(t *testing.T) {
	for _, detach := range []bool{false, true} {
		t.Run(fmt.Sprint("detach ", detach), func(t *testing.T) {
			f := linked(returning{body: "value"}, failing{})
			f.Error = &flowdef.ErrorHandler{ID: "e", Route: &flowdef.Node{ID: "h", Kind: flowdef.Action,
				Processor: returning{body: "sorry", err: errors.New("denied"), detach: detach}}}

			var out message.Message
			var replyErr error
			res, err := execute(context.Background(), f, message.New("-"), func(m message.Message, err error) { out, replyErr = m, err })
			if err != nil || res.Err == nil {
				t.Fatalf("flow = %v, %v; want the failure handled", res, err)
			}
			if replyErr == nil || replyErr.Error() != "denied" || bodyOf(out) != "sorry" {
				t.Errorf("reply = %q, %v; want sorry with the error denied", bodyOf(out), replyErr)
			}
		})
	}
}

// TestReturnError: a return with an error gives the sender that failure,
// with the message, at once or at the end; the flow itself has not failed.
func TestReturnError(t *testing.T) {
	for _, detach := range []bool{false, true} {
		t.Run(fmt.Sprint("detach ", detach), func(t *testing.T) {
			f := linked(returning{body: "value", err: errors.New("denied"), detach: detach})
			var out message.Message
			var replyErr error
			res, err := execute(context.Background(), f, message.New("-"), func(m message.Message, err error) { out, replyErr = m, err })
			if err != nil || res.Err != nil {
				t.Fatalf("flow = %v, %v; want it not to fail", res, err)
			}
			if replyErr == nil || replyErr.Error() != "denied" || bodyOf(out) != "value" {
				t.Errorf("reply = %q, %v; want value with the error denied", bodyOf(out), replyErr)
			}
		})
	}
}

// TestReturnFirstWins: with several returns, the first one gives the reply.
func TestReturnFirstWins(t *testing.T) {
	var tookX, tookZ bool
	x := &flowdef.Node{ID: "x", Kind: flowdef.Action, Processor: returning{body: "x", took: &tookX}}
	z := &flowdef.Node{ID: "z", Kind: flowdef.Action, Processor: returning{body: "z", took: &tookZ}}
	both := routerStub{func(m message.Message) []stepdef.Route {
		return []stepdef.Route{{Next: 0, Message: m.Copy()}, {Next: 1, Message: m}}
	}}
	rt := &flowdef.Node{ID: "r", Kind: flowdef.Router, Processor: both, Next: []*flowdef.Node{x, z}}
	f := &flowdef.Flow{Source: &flowdef.Node{ID: "a", Kind: flowdef.Source, Next: []*flowdef.Node{rt}}}

	var replies []string
	if _, err := execute(context.Background(), f, message.New("-"), func(m message.Message, _ error) { replies = append(replies, bodyOf(m)) }); err != nil {
		t.Fatal(err)
	}
	if strings.Join(replies, " ") != "x" || !tookX || tookZ {
		t.Errorf("replies = %v, took x %v, z %v; want one reply, from x", replies, tookX, tookZ)
	}
}

// TestReturnWithoutSender: nobody waits, so the return step takes nothing and
// the flow runs as it does without it.
func TestReturnWithoutSender(t *testing.T) {
	var took bool
	var ran []string
	f := linked(returning{body: "value", took: &took}, tagger{&ran, "x", nil})

	for name, run := range map[string]func() (*Result, error){
		"Run":     func() (*Result, error) { return Run(context.Background(), f, message.New("-")) },
		"execute": func() (*Result, error) { return execute(context.Background(), f, message.New("-"), nil) },
	} {
		took = true
		res, err := run()
		if err != nil || took || bodyOf(res.Message) != "-x" {
			t.Errorf("%s: took = %v, outcome = %v, %v; want nothing taken and the flow's own outcome", name, took, res, err)
		}
	}
}

// TestReturnThenOneWay: a one-way step after a return that waits gives the
// sender the returned value at once, not the message.
func TestReturnThenOneWay(t *testing.T) {
	var ran []string
	f := linked(returning{body: "value"}, oneWay{"late"}, tagger{&ran, "x", nil})

	_, err := execute(context.Background(), f, message.New("-"), func(m message.Message, _ error) { ran = append(ran, "reply:"+bodyOf(m)) })
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(ran, " "); got != "reply:value x" {
		t.Errorf("order = %q, want the returned value at the one-way step", got)
	}
}

// TestReturnAfterOneWay: once a one-way step has replied there is nothing left
// to return.
func TestReturnAfterOneWay(t *testing.T) {
	var took bool
	f := linked(oneWay{"early"}, returning{body: "value", took: &took})

	var replies []string
	if _, err := execute(context.Background(), f, message.New("-"), func(m message.Message, _ error) { replies = append(replies, bodyOf(m)) }); err != nil {
		t.Fatal(err)
	}
	if took || strings.Join(replies, " ") != "early" {
		t.Errorf("replies = %v, took %v; want only early", replies, took)
	}
}

// TestReturnDetachDoesNotComplete: a source that acknowledges when the flow is
// done (a durable queue) is not told early by a detached return, which only
// answers the sender.
func TestReturnDetachDoesNotComplete(t *testing.T) {
	probe := completionProbe{replied: make(chan struct{}), completed: make(chan error, 1)}
	release := make(chan struct{})
	boom := errors.New("late failure")
	last := &flowdef.Node{ID: "last", Kind: flowdef.Action, Processor: gate{release, boom}}
	early := &flowdef.Node{ID: "early", Kind: flowdef.Action, Processor: returning{body: "early", detach: true}, Next: []*flowdef.Node{last}}
	f := &flowdef.Flow{ID: "completion", Source: &flowdef.Node{ID: "source", Kind: flowdef.Source, Processor: probe, Next: []*flowdef.Node{early}}}
	r := started(t, f, nil)

	select {
	case <-probe.replied:
	case <-time.After(time.Second):
		t.Fatal("no early reply")
	}
	select {
	case err := <-probe.completed:
		t.Fatal("premature completion", err)
	default:
	}
	close(release)
	select {
	case err := <-probe.completed:
		if !errors.Is(err, boom) {
			t.Fatalf("completion = %v, want the late failure", err)
		}
	case <-time.After(time.Second):
		t.Fatal("no terminal completion")
	}
	must(t, r.Stop())
}
