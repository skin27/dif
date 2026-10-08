package api

import (
	"context"
	"errors"
	"testing"
	"time"

	"dif/message"
)

func TestAsyncRequestReturnsBeforeWorkerAndDeliversReply(t *testing.T) {
	r := runtimeNew(t, ChannelConfig{})
	_, results := runtimeFlow(t, r, dil(t, "results",
		step{"in", "source", "reply:results", nil},
		step{"out", "sink", "passthrough", nil},
	))
	entered, release := make(chan struct{}), make(chan struct{})
	block := runtimeStep(t, func(ctx context.Context, m Message) (Message, error) {
		close(entered)
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		m[Body] = "processed"
		return m, nil
	})
	worker, _ := runtimeFlow(t, r, dil(t, "worker",
		step{"in", "source", "queue:jobs", nil},
		step{"early", "action", "setoneway", nil},
		step{"work", "action", block, nil},
		step{"out", "sink", "reply", nil},
	))
	defer worker.Cancel()
	publisher, _ := runtimeFlow(t, r, dil(t, "submit",
		step{"in", "source", "message", nil},
		step{"submit", "sink", "request:jobs", map[string]any{"replyTo": "results"}},
	))
	in := message.New("document")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	receipt, err := publisher.Request(ctx, in)
	if err != nil || receipt[Body] != "document" || receipt[message.RequestID] == nil {
		t.Fatalf("receipt: %v %v", receipt, err)
	}
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("worker not entered")
	}
	select {
	case got := <-results:
		t.Fatalf("premature result: %v", got)
	default:
	}
	close(release)
	got := await(t, results).Message
	if got[Body] != "processed" || got[message.ReplyStatus] != "success" || got[message.RequestID] != receipt[message.RequestID] {
		t.Fatalf("result: %v", got)
	}
	if got[message.CorrelationID] != in[message.CorrelationID] || got[message.CausationID] != receipt[message.RequestID] || got[message.MessageID] == receipt[message.RequestID] {
		t.Fatalf("identity: %v", got)
	}
	if got[message.ReplyTo] != nil || got[message.ReplyDeadline] != nil {
		t.Fatal("reply routing leaked")
	}
}

func TestAsyncTimeoutAndLateReply(t *testing.T) {
	r := runtimeNew(t, ChannelConfig{})
	_, results := runtimeFlow(t, r, dil(t, "results", step{"in", "source", "reply:results", nil}, step{"out", "sink", "passthrough", nil}))
	_, unmatched := runtimeFlow(t, r, dil(t, "unmatched", step{"in", "source", "queue:results.unmatched", nil}, step{"out", "sink", "passthrough", nil}))
	publisher, _ := runtimeFlow(t, r, dil(t, "submit", step{"in", "source", "message", nil}, step{"out", "sink", "request:jobs", map[string]any{"replyTo": "results", "requestTimeout": 1}}))
	runtimeRequest(t, publisher, message.New("document"))
	got := await(t, results).Message
	if got[message.ReplyStatus] != "timeout" {
		t.Fatalf("timeout: %v", got)
	}
	// Starting the worker only after the timeout makes lateness deterministic.
	runtimeFlow(t, r, dil(t, "worker", step{"in", "source", "queue:jobs", nil}, step{"out", "sink", "reply", nil}))
	late := await(t, unmatched).Message
	if late[message.ReplyReason] != "late" || late[message.RequestID] != got[message.RequestID] {
		t.Fatalf("late: %v", late)
	}
}

func TestAsyncResultRetryAndRestart(t *testing.T) {
	r := runtimeNew(t, ChannelConfig{})
	calls := 0
	flaky := runtimeStep(t, func(_ context.Context, m Message) (Message, error) {
		calls++
		if calls == 1 {
			return nil, errors.New("retry result")
		}
		return m, nil
	})
	resultFlow, results := runtimeFlow(t, r, dil(t, "results", step{"in", "source", "reply:results", nil}, step{"work", "sink", flaky, nil}))
	if err := resultFlow.Stop(); err != nil {
		t.Fatal(err)
	}
	runtimeFlow(t, r, dil(t, "worker", step{"in", "source", "queue:jobs", nil}, step{"out", "sink", "reply", map[string]any{"status": "error"}}))
	publisher, _ := runtimeFlow(t, r, dil(t, "submit", step{"in", "source", "message", nil}, step{"out", "sink", "request:jobs", map[string]any{"replyTo": "results"}}))
	runtimeRequest(t, publisher, message.New("document"))
	if err := resultFlow.Start(); err != nil {
		t.Fatal(err)
	}
	select {
	case first := <-results:
		if first.err == nil {
			t.Fatal("first result should fail")
		}
	case <-time.After(time.Second):
		t.Fatal("no first result")
	}
	got := await(t, results).Message
	if got[message.ReplyStatus] != "error" {
		t.Fatalf("retried result: %v", got)
	}
}

func TestAsyncResultEarlyReplyDoesNotComplete(t *testing.T) {
	r := runtimeNew(t, ChannelConfig{})
	entered, release := make(chan struct{}), make(chan struct{})
	block := runtimeStep(t, func(ctx context.Context, m Message) (Message, error) {
		close(entered)
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return m, nil
	})
	resultFlow, results := runtimeFlow(t, r, dil(t, "results", step{"in", "source", "reply:results", nil}, step{"early", "action", "setoneway", nil}, step{"block", "sink", block, nil}))
	defer resultFlow.Cancel()
	publisher, _ := runtimeFlow(t, r, dil(t, "submit", step{"in", "source", "message", nil}, step{"out", "sink", "request:jobs", map[string]any{"replyTo": "results"}}))
	runtimeFlow(t, r, dil(t, "worker", step{"in", "source", "queue:jobs", nil}, step{"out", "sink", "reply", nil}))
	runtimeRequest(t, publisher, message.New("document"))
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("no result processing")
	}
	if m, err := r.channels.RequestOutcome("results", time.Now()); err != nil || m != nil {
		t.Fatalf("early completion: %v %v", m, err)
	}
	close(release)
	await(t, results)
}
