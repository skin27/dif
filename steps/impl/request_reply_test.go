package impl

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	flowdef "dif/flows/definition"
	"dif/internal/channels"
	"dif/message"
	stepdef "dif/steps/definition"
)

func TestAsyncIntakeContinuesDuringResultProcessing(t *testing.T) {
	r := asyncChannels(t, channels.Config{})
	a := asyncProcessor(t, r, stepdef.Action, "request:jobs", map[string]any{"replyTo": "results"}).(stepdef.ActionProcessor)
	sink := asyncProcessor(t, r, stepdef.Sink, "reply", nil).(replySink)
	source := asyncProcessor(t, r, stepdef.Source, "reply:results", nil).(replySource)
	requests := make([]message.Message, 2)
	root := message.New("document")
	for i := range requests {
		if _, err := a.Process(context.Background(), root); err != nil {
			t.Fatal(err)
		}
		e, err := r.queue("jobs").take(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		requests[i] = e.m
		if e.release != nil {
			e.release()
		}
	}
	type delivery struct {
		m        message.Message
		complete func(error) error
	}
	deliveries := make(chan delivery, 2)
	ctx, cancel := context.WithCancel(context.Background())
	done, ready := make(chan error, 1), make(chan struct{})
	go func() {
		done <- source.RunDelivery(ctx, func(m message.Message, _ func(message.Message, error), complete func(error) error) error {
			deliveries <- delivery{m, complete}
			return nil
		}, func() { close(ready) })
	}()
	<-ready
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(time.Second):
			t.Error("source leaked")
		}
	})
	waitDelivery := func() delivery {
		t.Helper()
		select {
		case d := <-deliveries:
			return d
		case <-time.After(time.Second):
			t.Fatal("no result")
			return delivery{}
		}
	}
	if err := sink.Consume(ctx, requests[0]); err != nil {
		t.Fatal(err)
	}
	first := waitDelivery()
	if err := sink.Consume(ctx, requests[1]); err != nil {
		t.Fatal(err)
	}
	// A following unmatched message is a barrier proving intake passed reply 2.
	if err := r.queue("results").put(message.New("barrier")); err != nil {
		t.Fatal(err)
	}
	wait, stop := context.WithTimeout(ctx, time.Second)
	defer stop()
	e, err := r.queue("results.unmatched").take(wait)
	if err != nil {
		t.Fatal(err)
	}
	if e.release != nil {
		e.release()
	}
	second, err := r.RequestOutcome("results", time.Now().Add(time.Hour))
	if err != nil || second[message.RequestID] != requests[1][message.RequestID] || second[message.ReplyStatus] != "success" {
		t.Fatalf("intake blocked: %v %v", second, err)
	}
	if err := r.FinishRequest(second[message.RequestID].(string), false, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := first.complete(nil); err != nil {
		t.Fatal(err)
	}
	last := waitDelivery()
	if err := last.complete(nil); err != nil {
		t.Fatal(err)
	}
}

func TestAsyncRejectedResultRemainsAvailable(t *testing.T) {
	r := asyncChannels(t, channels.Config{})
	a := asyncProcessor(t, r, stepdef.Action, "request:jobs", map[string]any{"replyTo": "results"}).(stepdef.ActionProcessor)
	if _, err := a.Process(context.Background(), message.New("document")); err != nil {
		t.Fatal(err)
	}
	e, err := r.queue("jobs").take(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if e.release != nil {
		e.release()
	}
	sink := asyncProcessor(t, r, stepdef.Sink, "reply", nil).(replySink)
	if err := sink.Consume(context.Background(), e.m); err != nil {
		t.Fatal(err)
	}
	source := asyncProcessor(t, r, stepdef.Source, "reply:results", nil).(replySource)
	want := errors.New("result unavailable")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := source.RunDelivery(ctx, func(message.Message, func(message.Message, error), func(error) error) error { return want }, func() {}); !errors.Is(err, want) {
		t.Fatalf("source failure: %v", err)
	}
	m, err := r.RequestOutcome("results", time.Now())
	if err != nil || m[message.RequestID] != e.m[message.RequestID] {
		t.Fatalf("lost rejected result: %v %v", m, err)
	}
}

func TestAsyncUnmatchedOverflowReturnsResponse(t *testing.T) {
	r := asyncChannels(t, channels.Config{Queues: map[string]channels.QueueConfig{"results.unmatched": {Capacity: 1}}})
	if err := r.queue("results.unmatched").put(message.New("full")); err != nil {
		t.Fatal(err)
	}
	if err := r.queue("results").put(message.New("unmatched")); err != nil {
		t.Fatal(err)
	}
	source := asyncProcessor(t, r, stepdef.Source, "reply:results", nil).(replySource)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := source.RunDelivery(ctx, nil, func() {}); err == nil || !strings.Contains(err.Error(), "full") {
		t.Fatalf("overflow: %v", err)
	}
	e, err := r.queue("results").take(ctx)
	if err != nil || e.m[message.Body] != "unmatched" {
		t.Fatalf("lost response: %v %v", e.m, err)
	}
}

func asyncChannels(t *testing.T, config channels.Config) *Channels {
	t.Helper()
	r, err := NewChannels(config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	return r
}

func asyncProcessor(t *testing.T, r *Channels, kind, uri string, options map[string]any) stepdef.Processor {
	t.Helper()
	p, err := steps.ProcessorWithParams(&flowdef.Node{ID: "async", Kind: kind, URI: uri, Options: options}, stepdef.Params{ChannelRuntimeKey: r})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestAsyncRequestAdmissionRollback(t *testing.T) {
	r := asyncChannels(t, channels.Config{Requests: channels.RequestConfig{Capacity: 1}, Queues: map[string]channels.QueueConfig{"jobs": {Capacity: 1}}})
	if err := r.queue("jobs").put(message.New("full")); err != nil {
		t.Fatal(err)
	}
	a := asyncProcessor(t, r, stepdef.Action, "request:jobs", map[string]any{"replyTo": "results"}).(stepdef.ActionProcessor)
	for range 2 {
		if _, err := a.Process(context.Background(), message.New("fail")); err == nil || !strings.Contains(err.Error(), "queue jobs is full") {
			t.Fatalf("admission: %v", err)
		}
	}
	if _, err := r.queue("jobs").take(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := a.Process(ctx, message.New("cancel")); err != context.Canceled {
		t.Fatalf("cancel: %v", err)
	}
	m := message.New("accepted")
	out, err := a.Process(context.Background(), m)
	if err != nil {
		t.Fatal(err)
	}
	if m[message.RequestID] != nil || out[message.RequestID] == nil {
		t.Fatal("receipt mutated original")
	}
	e, err := r.queue("jobs").take(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if e.reply != nil || e.wait != nil || e.m[message.RequestID] != out[message.RequestID] {
		t.Fatal("request retains synchronous sender")
	}
	if e.m[message.CausationID] != m[message.MessageID] {
		t.Fatal("request is not a child")
	}
	if e.release != nil {
		e.release()
	}
}

func TestAsyncReplyUnmatchedAndConsumerConflict(t *testing.T) {
	r := asyncChannels(t, channels.Config{})
	s := asyncProcessor(t, r, stepdef.Source, "reply:results", nil).(replySource)
	ctx, cancel := context.WithCancel(context.Background())
	done, ready := make(chan error, 1), make(chan struct{})
	go func() {
		done <- s.RunDelivery(ctx, func(message.Message, func(message.Message, error), func(error) error) error {
			t.Error("unexpected matched result")
			return nil
		}, func() { close(ready) })
	}()
	<-ready
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(time.Second):
			t.Error("reply source did not stop")
		}
	})
	ordinary := asyncProcessor(t, r, stepdef.Source, "queue:results", nil).(queueSource)
	if err := ordinary.RunReady(ctx, nil, func() { t.Error("conflicting source reported ready") }); err == nil {
		t.Fatal("ordinary consumer stole replies")
	}
	if err := s.RunDelivery(ctx, nil, func() { t.Error("duplicate source reported ready") }); err == nil {
		t.Fatal("two reply consumers")
	}
	for _, tc := range []struct {
		m   message.Message
		why string
	}{
		{message.New("malformed"), "malformed"},
		{message.Message{message.RequestID: "unknown", message.ReplyStatus: "success"}, "unknown"},
	} {
		if err := r.queue("results").put(tc.m); err != nil {
			t.Fatal(err)
		}
		wait, stop := context.WithTimeout(ctx, time.Second)
		e, err := r.queue("results.unmatched").take(wait)
		stop()
		if err != nil || e.m[message.ReplyReason] != tc.why {
			t.Fatalf("unmatched: %v %v", e.m, err)
		}
		if e.release != nil {
			e.release()
		}
	}
}

func TestAsyncValidation(t *testing.T) {
	wantInvalid(t, stepdef.Action, "request:jobs", nil, "replyTo")
	wantInvalid(t, stepdef.Action, "request:jobs", map[string]any{"replyTo": "jobs"}, "different")
	wantInvalid(t, stepdef.Action, "request:jobs", map[string]any{"replyTo": "results", "requestTimeout": 0}, "requestTimeout")
	wantInvalid(t, stepdef.Source, "reply:results", map[string]any{"unmatchedQueue": "results"}, "different")
	wantInvalid(t, stepdef.Sink, "reply", map[string]any{"status": "timeout"}, "is not one of")
	r := asyncChannels(t, channels.Config{})
	s := asyncProcessor(t, r, stepdef.Sink, "reply", nil).(replySink)
	if err := s.Consume(context.Background(), message.New(nil)); err == nil {
		t.Fatal("missing routing accepted")
	}
	durable := asyncChannels(t, channels.Config{Directory: t.TempDir(), Queues: map[string]channels.QueueConfig{"disk": {Durable: true}}})
	for _, tc := range []struct {
		kind, uri string
		options   map[string]any
	}{
		{stepdef.Action, "request:disk", map[string]any{"replyTo": "results"}},
		{stepdef.Action, "request:jobs", map[string]any{"replyTo": "disk"}},
		{stepdef.Source, "reply:disk", nil},
	} {
		_, err := steps.ProcessorWithParams(&flowdef.Node{Kind: tc.kind, URI: tc.uri, Options: tc.options}, stepdef.Params{ChannelRuntimeKey: durable})
		if err == nil || !strings.Contains(err.Error(), "memory") {
			t.Fatalf("durable mode: %v", err)
		}
	}
}
