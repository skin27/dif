package engine

import (
	"bytes"
	"context"
	"errors"
	"log"
	"strings"
	"testing"
	"time"

	flowdef "dif/flows/definition"
	"dif/message"
	stepdef "dif/steps/definition"
)

// flaky is an action that fails its first fails calls.
type flaky struct {
	calls *int
	fails int
}

func (f flaky) Process(_ context.Context, m message.Message) (message.Message, error) {
	*f.calls++
	if *f.calls <= f.fails {
		return nil, errors.New("flaky")
	}
	m[message.Body] = "ok"
	return m, nil
}

// withError builds source a -> x -> sink y, with error handler h whose route
// is z (an action appending "z" to the body).
func withError(ran *[]string, x stepdef.Processor, redeliveries int) *flowdef.Flow {
	y := &flowdef.Node{ID: "y", Kind: flowdef.Sink, Processor: sinkStub{stub{ran, "y", nil}}}
	xn := &flowdef.Node{ID: "x", Kind: flowdef.Action, Processor: x, Next: []*flowdef.Node{y}}
	z := &flowdef.Node{ID: "z", Kind: flowdef.Action, Processor: tagger{ran, "z", nil}}
	return &flowdef.Flow{
		Source: &flowdef.Node{ID: "a", Kind: flowdef.Source, Next: []*flowdef.Node{xn}},
		Error:  &flowdef.ErrorHandler{ID: "h", Redeliveries: redeliveries, RedeliveryDelay: time.Millisecond, Route: z},
	}
}

func TestRunErrorRoute(t *testing.T) {
	var ran []string
	boom := errors.New("boom")
	res, err := Run(context.Background(), withError(&ran, tagger{&ran, "x", boom}, 0), message.New("-"))
	if err != nil {
		t.Fatalf("the error route did not handle the failure: %v", err)
	}
	if !errors.Is(res.Err, boom) || !strings.Contains(res.Err.Error(), "step x: boom") {
		t.Errorf("Result.Err = %v, want step x: boom", res.Err)
	}
	m := res.Message
	if m[message.Body] != "-xz" || m[ErrorMessage] != "boom" || m[ErrorStep] != "x" {
		t.Errorf("message = %v, want the failed message through z with the error headers", m)
	}
	if m[ErrorClass] != "*errors.errorString" || m[ErrorStackTrace] != "step x: boom" {
		t.Errorf("error.class = %v, error.stacktrace = %v, want the type of the error and the step with the error", m[ErrorClass], m[ErrorStackTrace])
	}
	if got := strings.Join(res.Trail, " "); got != "source:a error:h action:z" {
		t.Errorf("trail = %q", got)
	}
	if got := strings.Join(ran, " "); got != "x z" {
		t.Errorf("ran = %q, want x z (the sink after x must not run)", got)
	}
}

func TestRunRedelivery(t *testing.T) {
	var logged bytes.Buffer
	ctx := stepdef.WithLogger(context.Background(), log.New(&logged, "", 0))

	var ran []string
	calls := 0
	res, err := Run(ctx, withError(&ran, flaky{&calls, 2}, 2), message.New("-"))
	if err != nil || res.Err != nil {
		t.Fatalf("err = %v, Result.Err = %v; want the third try to succeed", err, res.Err)
	}
	if calls != 3 || res.Message[message.Body] != "ok" || strings.Join(ran, " ") != "y" {
		t.Errorf("calls = %d, body = %v, ran = %v", calls, res.Message[message.Body], ran)
	}
	if want := "step x: redelivery 2 of 2 in 1ms after: flaky"; !strings.Contains(logged.String(), want) {
		t.Errorf("log = %q, want containing %q", logged.String(), want)
	}

	// Redeliveries exhausted: the error route takes over.
	ran, calls = nil, 0
	res, err = Run(ctx, withError(&ran, flaky{&calls, 5}, 2), message.New("-"))
	if err != nil || calls != 3 || res.Message[message.Body] != "-z" {
		t.Errorf("err = %v, calls = %d, body = %v; want 3 tries, then the error route", err, calls, res.Message[message.Body])
	}

	// Without an error route the message fails after the redeliveries.
	ran, calls = nil, 0
	f := withError(&ran, flaky{&calls, 5}, 1)
	f.Error.Route = nil
	if _, err := Run(ctx, f, message.New("-")); err == nil || !strings.Contains(err.Error(), "step x: flaky") || calls != 2 {
		t.Errorf("err = %v, calls = %d; want step x to fail after 2 tries", err, calls)
	}
}

func TestRunErrorRouteFails(t *testing.T) {
	var ran []string
	f := withError(&ran, tagger{&ran, "x", errors.New("boom")}, 0)
	f.Error.Route.Processor = tagger{&ran, "z", errors.New("bang")}
	_, err := Run(context.Background(), f, message.New("-"))
	if err == nil || err.Error() != "step x: boom; error route: step z: bang" {
		t.Errorf("err = %v", err)
	}
}

func TestRunErrorInBranch(t *testing.T) {
	var ran []string
	boom := errors.New("boom")
	f := routed(&ran, boom, func(m message.Message) []stepdef.Route {
		c := m.Copy()
		c[message.Body] = "branch-"
		return []stepdef.Route{{Next: 0, Message: c}, {Next: 1, Message: m}}
	})
	f.Error = &flowdef.ErrorHandler{ID: "h", Route: &flowdef.Node{ID: "e", Kind: flowdef.Action, Processor: tagger{&ran, "e", nil}}}
	res, err := Run(context.Background(), f, message.New("-"))
	if err != nil {
		t.Fatal(err)
	}
	if res.Message[message.Body] != "branch-xe" || res.Message[ErrorStep] != "x" {
		t.Errorf("message = %v, want the branch's message through the error route", res.Message)
	}
	if got := strings.Join(ran, " "); got != "x e" {
		t.Errorf("ran = %q, want x e (the other route must not run)", got)
	}
}

func TestRunNoErrorRouteWhenCancelled(t *testing.T) {
	var ran []string
	calls := 0
	f := withError(&ran, flaky{&calls, 5}, 3)
	f.Error.RedeliveryDelay = time.Minute
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	start := time.Now()
	if _, err := Run(ctx, f, message.New("-")); err == nil {
		t.Error("want an error")
	}
	if d := time.Since(start); d > time.Second || calls != 1 || len(ran) != 0 {
		t.Errorf("took %v, calls = %d, ran = %v; want one try, no wait, no error route", d, calls, ran)
	}
}
