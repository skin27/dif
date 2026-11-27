package engine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"testing"
	"time"

	flowdef "dif/flows/definition"
	"dif/message"
	stepdef "dif/steps/definition"
)

// handled builds source a -> action x (URI test:x) with the error handlers.
func handled(x stepdef.Processor, matched []*flowdef.ErrorHandler, def *flowdef.ErrorHandler) *flowdef.Flow {
	xn := &flowdef.Node{ID: "x", Kind: flowdef.Action, URI: "test:x", Processor: x}
	return &flowdef.Flow{Source: &flowdef.Node{ID: "a", Kind: flowdef.Source, Next: []*flowdef.Node{xn}}, Handlers: matched, Error: def}
}

// handlerTo is an error handler whose route appends its id to the body.
func handlerTo(id string, ran *[]string, match func(flowdef.Failure) bool) *flowdef.ErrorHandler {
	return &flowdef.ErrorHandler{ID: id, Match: match, Route: &flowdef.Node{ID: "route-" + id, Kind: flowdef.Action, Processor: tagger{ran, id, nil}}}
}

func always(flowdef.Failure) bool { return true }
func never(flowdef.Failure) bool  { return false }

func TestHandlerFirstMatchWins(t *testing.T) {
	var ran []string
	f := handled(failing{}, []*flowdef.ErrorHandler{
		handlerTo("n", &ran, never),
		handlerTo("1", &ran, always),
		handlerTo("2", &ran, always),
	}, handlerTo("d", &ran, nil))

	res, err := Run(context.Background(), f, message.New("-"))
	if err != nil || res.Err == nil || bodyOf(res.Message) != "-1" || strings.Join(ran, " ") != "1" {
		t.Errorf("res = %v, %v, ran = %v; want the first matching handler's route (-1)", res, err, ran)
	}
	if got := strings.Join(res.Trail, " "); got != "source:a error:1 action:route-1" {
		t.Errorf("trail = %q, want the handler that took the error", got)
	}
}

func TestHandlerDefaultTakesTheRest(t *testing.T) {
	var ran []string
	f := handled(failing{}, []*flowdef.ErrorHandler{handlerTo("n", &ran, never)}, handlerTo("d", &ran, nil))
	res, err := Run(context.Background(), f, message.New("-"))
	if err != nil || bodyOf(res.Message) != "-d" {
		t.Errorf("res = %v, %v; want the default handler's route (-d)", res, err)
	}
}

// Without a default handler, a failure no handler takes fails the message.
func TestHandlerNoneTakesTheError(t *testing.T) {
	var ran []string
	f := handled(failing{}, []*flowdef.ErrorHandler{handlerTo("n", &ran, never)}, nil)
	res, err := Run(context.Background(), f, message.New("-"))
	if err == nil || res != nil || len(ran) != 0 || !strings.Contains(err.Error(), "step x: boom") {
		t.Errorf("res = %v, err = %v, ran = %v; want the failure and no error route", res, err, ran)
	}
}

// The match gets the failure: step, URI, the type at the bottom of the chain
// and the error text.
func TestHandlerMatchSeesTheFailure(t *testing.T) {
	var seen flowdef.Failure
	var ran []string
	h := handlerTo("1", &ran, func(f flowdef.Failure) bool { seen = f; return true })
	boom := errors.New("boom")
	_, err := Run(context.Background(), handled(returning2{boom}, []*flowdef.ErrorHandler{h}, nil), message.New("-"))
	if err != nil {
		t.Fatal(err)
	}
	if want := (flowdef.Failure{Step: "x", URI: "test:x", Class: "*errors.errorString", Message: "wrapper: boom"}); seen != want {
		t.Errorf("failure = %+v, want %+v", seen, want)
	}
}

// returning2 fails with the error it holds, wrapped once.
type returning2 struct{ err error }

func (r returning2) Process(context.Context, message.Message) (message.Message, error) {
	return nil, fmt.Errorf("wrapper: %w", r.err)
}

// Redeliveries come from the handler that takes the failure: a step whose
// failure a handler takes is tried again, one the default takes is not.
func TestHandlerRedeliveryPerHandler(t *testing.T) {
	var ran []string
	retrying := handlerTo("1", &ran, always)
	retrying.Redeliveries, retrying.RedeliveryDelay = 2, time.Millisecond

	calls := 0
	res, err := Run(context.Background(), handled(flaky{&calls, 2}, []*flowdef.ErrorHandler{retrying}, handlerTo("d", &ran, nil)), message.New("-"))
	if err != nil || res.Err != nil || calls != 3 {
		t.Errorf("calls = %d, res = %v, %v; want the third try to succeed", calls, res, err)
	}

	calls = 0
	only := handlerTo("1", &ran, never)
	only.Redeliveries, only.RedeliveryDelay = 2, time.Millisecond
	res, err = Run(context.Background(), handled(flaky{&calls, 2}, []*flowdef.ErrorHandler{only}, handlerTo("d", &ran, nil)), message.New("-"))
	if err != nil || res.Err == nil || calls != 1 {
		t.Errorf("calls = %d, res = %v, %v; want one try, then the default handler", calls, res, err)
	}

	calls = 0
	_, err = Run(context.Background(), handled(flaky{&calls, 2}, []*flowdef.ErrorHandler{only}, nil), message.New("-"))
	if err == nil || calls != 1 {
		t.Errorf("calls = %d, err = %v; want one try and the failure when no handler takes it", calls, err)
	}
}

func TestHandlerRedeliveryLogsItsDelay(t *testing.T) {
	var logged bytes.Buffer
	ctx := stepdef.WithLogger(context.Background(), log.New(&logged, "", 0))
	var ran []string
	h := handlerTo("1", &ran, always)
	h.Redeliveries, h.RedeliveryDelay, h.Backoff = 2, time.Millisecond, 2

	calls := 0
	if _, err := Run(ctx, handled(flaky{&calls, 5}, []*flowdef.ErrorHandler{h}, nil), message.New("-")); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"step x: redelivery 1 of 2 in 1ms after: flaky", "step x: redelivery 2 of 2 in 2ms after: flaky"} {
		if !strings.Contains(logged.String(), want) {
			t.Errorf("log = %q, want containing %q", logged.String(), want)
		}
	}
}

func TestRedeliveryDelay(t *testing.T) {
	ms := time.Millisecond
	tests := []struct {
		name string
		h    flowdef.ErrorHandler
		want []time.Duration // for redeliveries 1, 2, 3, 4
	}{
		{"constant", flowdef.ErrorHandler{RedeliveryDelay: 10 * ms}, []time.Duration{10 * ms, 10 * ms, 10 * ms, 10 * ms}},
		{"no growth at 1", flowdef.ErrorHandler{RedeliveryDelay: 10 * ms, Backoff: 1}, []time.Duration{10 * ms, 10 * ms, 10 * ms, 10 * ms}},
		{"doubling", flowdef.ErrorHandler{RedeliveryDelay: 10 * ms, Backoff: 2}, []time.Duration{10 * ms, 20 * ms, 40 * ms, 80 * ms}},
		{"capped", flowdef.ErrorHandler{RedeliveryDelay: 10 * ms, Backoff: 2, MaxDelay: 30 * ms}, []time.Duration{10 * ms, 20 * ms, 30 * ms, 30 * ms}},
		{"cap on a constant delay", flowdef.ErrorHandler{RedeliveryDelay: 50 * ms, MaxDelay: 30 * ms}, []time.Duration{30 * ms, 30 * ms, 30 * ms, 30 * ms}},
	}
	for _, tt := range tests {
		for i, want := range tt.want {
			if got := redeliveryDelay(&tt.h, i+1); got != want {
				t.Errorf("%s: redelivery %d waits %v, want %v", tt.name, i+1, got, want)
			}
		}
	}
	// A multiplier cannot overflow the delay.
	big := flowdef.ErrorHandler{RedeliveryDelay: time.Hour, Backoff: 1e9}
	if got := redeliveryDelay(&big, 50); got <= 0 {
		t.Errorf("delay after 50 redeliveries = %v, want a positive duration", got)
	}
}

// An unhandled handler runs its error route, and the message still fails: the
// sender gets the failure, and the result carries both.
func TestHandlerUnhandled(t *testing.T) {
	var ran []string
	h := handlerTo("1", &ran, always)
	h.Unhandled = true

	var replyErr error
	var replied message.Message
	res, err := execute(context.Background(), handled(failing{}, []*flowdef.ErrorHandler{h}, nil), message.New("-"),
		func(m message.Message, err error) { replied, replyErr = m, err })
	if err == nil || !strings.Contains(err.Error(), "step x: boom") {
		t.Fatalf("err = %v, want the original failure", err)
	}
	if res == nil || bodyOf(res.Message) != "-1" || res.Err == nil || strings.Join(ran, " ") != "1" {
		t.Errorf("res = %v, ran = %v; want the error route's outcome (-1) and the failure", res, ran)
	}
	if replyErr == nil || bodyOf(replied) != "-1" {
		t.Errorf("reply = %q, %v; want the failure to reach the sender", bodyOf(replied), replyErr)
	}
}

// In a running flow an unhandled message counts as failed, and the result
// callback gets both the result and the failure.
func TestHandlerUnhandledInARunner(t *testing.T) {
	var ran []string
	h := handlerTo("1", &ran, always)
	h.Unhandled = true
	results := make(chan result, 1)
	r := started(t, handled(failing{}, []*flowdef.ErrorHandler{h}, nil), func(res *Result, err error) { results <- result{res, err} })

	if _, err := r.Request(context.Background(), message.New("-")); err == nil {
		t.Error("Request: want the failure")
	}
	if res := next(t, results); res.err == nil || res.res == nil || res.res.Err == nil {
		t.Errorf("result = %+v, want the result and the failure", res)
	}
	if s := r.Status(); s.Failed != 1 || s.Completed != 0 {
		t.Errorf("status = %+v, want one failed message", s)
	}
}

// A return in the route of an unhandled handler does not turn the failure into
// a success: the failure wins.
func TestHandlerUnhandledFailureWinsOverReturn(t *testing.T) {
	h := &flowdef.ErrorHandler{ID: "1", Unhandled: true, Match: always,
		Route: &flowdef.Node{ID: "route", Kind: flowdef.Action, Processor: returning{body: "sorry"}}}
	var replyErr error
	_, err := execute(context.Background(), handled(failing{}, []*flowdef.ErrorHandler{h}, nil), message.New("-"),
		func(_ message.Message, err error) { replyErr = err })
	if err == nil || replyErr == nil {
		t.Errorf("err = %v, reply error = %v; want the failure for both", err, replyErr)
	}
}
