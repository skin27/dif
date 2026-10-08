package api

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"dif/message"
	stepdef "dif/steps/definition"
)

type runtimeAction func(context.Context, Message) (Message, error)

func (f runtimeAction) Process(ctx context.Context, m Message) (Message, error) { return f(ctx, m) }

var runtimeStepID atomic.Int64

func runtimeStep(t *testing.T, f runtimeAction) string {
	t.Helper()
	name := fmt.Sprintf("runtimetest%d", runtimeStepID.Add(1))
	if err := RegisterStep(StepDefinition{Name: name, Kind: stepdef.Action, Schema: []byte(`{"type":"object","properties":{}}`), New: func(string, stepdef.Params) (stepdef.Processor, error) { return f, nil }}); err != nil {
		t.Fatal(err)
	}
	return name
}
func runtimeFlow(t *testing.T, r *Runtime, path string) (*Flow, chan outcome) {
	t.Helper()
	results := make(chan outcome, 30)
	f, err := r.Load(path, func(res *Result, err error) { results <- outcome{res, err} })
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Stop() })
	return f, results
}
func runtimeConfig(t *testing.T) ChannelConfig {
	return ChannelConfig{Directory: t.TempDir(), Queues: map[string]QueueConfig{"jobs": {Durable: true, MaxDeliveries: 2, RetryDelayMS: 1}}, Idempotency: map[string]IdempotencyConfig{"orders": {Durable: true}}}
}
func runtimeNew(t *testing.T, c ChannelConfig) *Runtime {
	t.Helper()
	r, err := NewRuntime(c)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	return r
}
func runtimeRequest(t *testing.T, f *Flow, m Message) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := f.Request(ctx, m); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeDurableEnqueueRestartAndIdentity(t *testing.T) {
	c := runtimeConfig(t)
	r := runtimeNew(t, c)
	publisher, _ := runtimeFlow(t, r, dil(t, "producer", step{"in", "source", "message", nil}, step{"out", "sink", "queue:jobs", map[string]any{"delivery": "enqueue"}}))
	m := message.New([]byte("persisted"))
	runtimeRequest(t, publisher, m)
	publisher.Stop()
	r.Close()
	r = runtimeNew(t, c)
	consumer, results := runtimeFlow(t, r, dil(t, "consumer", step{"in", "source", "queue:jobs", nil}, step{"out", "sink", "passthrough", nil}))
	got := await(t, results).Message
	if string(got[Body].([]byte)) != "persisted" || got[message.MessageID] != m[message.MessageID] {
		t.Fatal(got)
	}
	consumer.Stop()
	r.Close()
	r = runtimeNew(t, c)
	for _, q := range r.ChannelStatus().Queues {
		if q.Name == "jobs" && (q.Waiting != 0 || q.InFlight != 0) {
			t.Fatal(q)
		}
	}
}

func TestRuntimeEarlyReplyDoesNotAcknowledge(t *testing.T) {
	r := runtimeNew(t, runtimeConfig(t))
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	defer close(release)
	var calls atomic.Int64
	action := runtimeStep(t, func(ctx context.Context, m Message) (Message, error) {
		calls.Add(1)
		entered <- struct{}{}
		select {
		case <-release:
			return m, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	})
	publisher, _ := runtimeFlow(t, r, dil(t, "producer", step{"in", "source", "message", nil}, step{"out", "sink", "queue:jobs", map[string]any{"delivery": "enqueue"}}))
	consumer, _ := runtimeFlow(t, r, dil(t, "consumer", step{"in", "source", "queue:jobs", nil}, step{"early", "action", "setoneway", nil}, step{"work", "sink", action, nil}))
	runtimeRequest(t, publisher, message.New("accepted"))
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("not processed")
	}
	found := false
	for _, q := range r.ChannelStatus().Queues {
		if q.Name == "jobs" {
			found = q.InFlight == 1
		}
	}
	if !found {
		t.Fatal("early acknowledgement", r.ChannelStatus())
	}
	// Forced cancellation must leave the delivery recoverable, not acknowledge it.
	consumer.Cancel()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	consumer.StopContext(ctx)
	for _, q := range r.ChannelStatus().Queues {
		if q.Name == "jobs" && q.Waiting+q.InFlight != 1 {
			t.Fatal(q)
		}
	}
}

func TestRuntimeIdempotentGuardRetriesAndPersists(t *testing.T) {
	c := runtimeConfig(t)
	r := runtimeNew(t, c)
	var calls atomic.Int64
	action := runtimeStep(t, func(_ context.Context, m Message) (Message, error) {
		if calls.Add(1) == 1 {
			return nil, errors.New("temporary")
		}
		out := m.Copy()
		out[Body] = "changed"
		return out, nil
	})
	path := dil(t, "consumer", step{"in", "source", "queue:jobs", nil}, step{"guard", "action", "idempotent", map[string]any{"namespace": "orders", "key": "${header.orderId}"}}, step{"work", "sink", action, nil})
	publisherPath := dil(t, "producer", step{"in", "source", "message", nil}, step{"out", "sink", "queue:jobs", map[string]any{"delivery": "enqueue"}})
	consumer, results := runtimeFlow(t, r, path)
	publisher, _ := runtimeFlow(t, r, publisherPath)
	m := message.New("original")
	m["orderId"] = "order-1"
	runtimeRequest(t, publisher, m)
	select {
	case first := <-results:
		if first.err == nil {
			t.Fatal("expected first failure")
		}
	case <-time.After(time.Second):
		t.Fatal("missing failure")
	}
	if got := await(t, results).Message[Body]; got != "changed" {
		t.Fatal(got)
	}
	runtimeRequest(t, publisher, m)
	await(t, results)
	if calls.Load() != 2 {
		t.Fatal("duplicate executed", calls.Load())
	}
	consumer.Stop()
	publisher.Stop()
	r.Close()
	r = runtimeNew(t, c)
	_, results = runtimeFlow(t, r, path)
	publisher, _ = runtimeFlow(t, r, publisherPath)
	runtimeRequest(t, publisher, m)
	await(t, results)
	if calls.Load() != 2 {
		t.Fatal("persisted key ignored", calls.Load())
	}
}

func TestRuntimeConfigurationAndDeliveryValidation(t *testing.T) {
	r := runtimeNew(t, runtimeConfig(t))
	for _, opts := range []map[string]any{nil, {"delivery": "processed"}, {"delivery": "enqueue", "overflow": "block"}} {
		_, err := r.Load(dil(t, "invalid", step{"in", "source", "message", nil}, step{"out", "sink", "queue:jobs", opts}), nil)
		if err == nil {
			t.Fatal("invalid durable producer accepted", opts)
		}
	}
	if _, err := NewRuntime(ChannelConfig{Queues: map[string]QueueConfig{"q": {Durable: true}}}); err == nil {
		t.Fatal("missing directory accepted")
	}
	if _, err := NewRuntime(ChannelConfig{Directory: t.TempDir(), Queues: map[string]QueueConfig{"q": {Durable: true, DeadLetter: "dead"}, "dead": {Durable: false}}}); err == nil {
		t.Fatal("volatile dead letter accepted")
	}
	r.Close()
	if _, err := r.Load(dil(t, "closed", step{"in", "source", "message", nil}), nil); err == nil {
		t.Fatal("closed runtime loaded flow")
	}
}

func TestRuntimeCancelledIdempotentGuardReleasesClaim(t *testing.T) {
	r := runtimeNew(t, runtimeConfig(t))
	entered := make(chan struct{}, 1)
	var calls atomic.Int64
	action := runtimeStep(t, func(ctx context.Context, m Message) (Message, error) {
		if calls.Add(1) == 1 {
			entered <- struct{}{}
			<-ctx.Done()
			return nil, ctx.Err()
		}
		return m, nil
	})
	f, results := runtimeFlow(t, r, dil(t, "guard", step{"in", "source", "message", nil}, step{"guard", "action", "idempotent", map[string]any{"namespace": "orders", "claimTimeout": 50}}, step{"work", "sink", action, nil}))
	m := message.New("retry")
	if err := f.Send(m); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("not entered")
	}
	if err := f.ForceStop(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-results:
	case <-time.After(time.Second):
		t.Fatal("missing cancelled result")
	}
	if err := f.Start(); err != nil {
		t.Fatal(err)
	}
	runtimeRequest(t, f, m)
	await(t, results)
	if calls.Load() != 2 {
		t.Fatal("reservation leaked across stop", calls.Load())
	}
}
