package api

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestTopicFlowsReadyPauseAndRestart(t *testing.T) {
	name := t.Name()
	consumer := func(id string) (*Flow, chan outcome) {
		return start(t, dil(t, id,
			step{"in", "source", "topic:" + name, nil},
			step{"out", "sink", "passthrough", nil},
		), nil)
	}
	a, resultsA := consumer("subscriber-a")
	_, resultsB := consumer("subscriber-b")
	publisher, _ := start(t, dil(t, "publisher",
		step{"in", "source", "message", nil},
		step{"publish", "sink", "topic:" + name, nil},
	), nil)
	send := func(body string) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		out, err := publisher.Request(ctx, Message{Body: body})
		if err != nil || out[Body] != body {
			t.Fatalf("publish = %v, %v", out, err)
		}
	}
	want := func(results chan outcome, body string) {
		t.Helper()
		if got := await(t, results).Message[Body]; got != body {
			t.Fatalf("body = %v, want %q", got, body)
		}
	}
	// No sleep or polling: Start must register the subscriptions before returning.
	send("immediate")
	want(resultsA, "immediate")
	want(resultsB, "immediate")
	if err := a.Pause(); err != nil {
		t.Fatal(err)
	}
	send("paused")
	want(resultsB, "paused")
	if err := a.Resume(); err != nil {
		t.Fatal(err)
	}
	want(resultsA, "paused")
	if err := a.Stop(); err != nil {
		t.Fatal(err)
	}
	send("while stopped")
	want(resultsB, "while stopped")
	for range 5 {
		if err := a.Start(); err != nil {
			t.Fatal(err)
		}
		send("after restart")
		want(resultsA, "after restart")
		want(resultsB, "after restart")
		if err := a.Stop(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestQueueFlowEnqueueBeforeConsumerStarts(t *testing.T) {
	name := t.Name()
	publisher, _ := start(t, dil(t, "publisher",
		step{"in", "source", "message", nil},
		step{"enqueue", "sink", "queue:" + name, map[string]any{"delivery": "enqueue"}},
	), nil)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := publisher.Request(ctx, Message{Body: "buffered"}); err != nil {
		t.Fatal(err)
	}
	_, results := start(t, dil(t, "consumer",
		step{"in", "source", "queue:" + name, nil},
		step{"out", "sink", "passthrough", nil},
	), nil)
	if got := await(t, results).Message[Body]; got != "buffered" {
		t.Fatalf("body = %v", got)
	}
}

func TestChannelExamplesLoad(t *testing.T) {
	files, err := filepath.Glob("../examples/channels/*.json")
	if err != nil || len(files) != 8 {
		t.Fatalf("examples = %v, %v", files, err)
	}
	for _, path := range files {
		t.Run(filepath.Base(path), func(t *testing.T) {
			if _, err := Load(path, nil); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestQueuedTapContinuesWhileConsumerPaused(t *testing.T) {
	consumer, results := start(t, "../examples/channels/tap-consumer.json", nil)
	if err := consumer.Pause(); err != nil {
		t.Fatal(err)
	}
	publisher, _ := start(t, "../examples/channels/tap-producer.json", nil)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	out, err := publisher.Request(ctx, Message{Body: "audit me"})
	if err != nil || out[Body] != "audit me" {
		t.Fatalf("main path = %v, %v", out, err)
	}
	select {
	case <-results:
		t.Fatal("paused audit flow processed the message")
	default:
	}
	if err := consumer.Resume(); err != nil {
		t.Fatal(err)
	}
	if got := await(t, results).Message[Body]; got != "audit me" {
		t.Fatalf("audit body = %v", got)
	}
}
