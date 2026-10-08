package impl

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"dif/message"
	stepdef "dif/steps/definition"
)

func TestTopicFanoutOrderAndCopies(t *testing.T) {
	topic := topicNamed(t.Name())
	a := topic.subscribe(context.Background())
	b := topic.subscribe(context.Background())
	defer topic.unsubscribe(a)
	defer topic.unsubscribe(b)
	producer := mustProcessor(t, stepdef.Action, "topic:"+t.Name(), nil).(stepdef.ActionProcessor)
	for i := range 3 {
		m := message.New(i)
		out, err := producer.Process(context.Background(), m)
		if err != nil || out[message.Body] != i {
			t.Fatalf("publish = %v, %v", out, err)
		}
		m[message.Body] = "sender changed"
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for i := range 3 {
		left, err := a.q.take(ctx)
		if err != nil || left.m[message.Body] != i {
			t.Fatalf("subscriber a = %v, %v", left, err)
		}
		left.m[message.Body] = "subscriber changed"
		right, err := b.q.take(ctx)
		if err != nil || right.m[message.Body] != i || right.m[message.MessageID] != left.m[message.MessageID] {
			t.Fatalf("subscriber b = %v, %v", right, err)
		}
	}
	if queueNamed(t.Name()).len() != 0 {
		t.Fatal("topic delivered to a queue with the same name")
	}
}

func TestTopicOverflowIsAtomic(t *testing.T) {
	topic := topicNamed(t.Name())
	a := topic.subscribe(context.Background())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := topic.subscribe(ctx)
	defer topic.unsubscribe(a)
	defer topic.unsubscribe(b)
	for range queueCapacity {
		if err := b.q.put(message.New(nil)); err != nil {
			t.Fatal(err)
		}
	}
	if err := topic.publish(context.Background(), message.New("rejected")); err == nil {
		t.Fatal("expected overflow")
	}
	if a.q.len() != 0 || b.q.len() != queueCapacity {
		t.Fatal("failed publication changed buffers")
	}
	if _, err := b.q.take(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := topic.publish(context.Background(), message.New("accepted")); err != nil {
		t.Fatal(err)
	}
	if a.q.len() != 1 || b.q.len() != queueCapacity {
		t.Fatal("successful retry did not reach both subscribers")
	}
	// A cancelled subscriber must not block publication while its Run unwinds.
	cancel()
	if err := topic.publish(context.Background(), message.New("after stop")); err != nil {
		t.Fatal(err)
	}
	if a.q.len() != 2 || b.q.len() != queueCapacity {
		t.Fatal("cancelled subscription received a message")
	}
}

func TestTopicSubscriptionLifecycle(t *testing.T) {
	topic := topicNamed(t.Name())
	if err := topic.publish(context.Background(), message.New("before subscription")); err != nil {
		t.Fatal(err)
	}
	src := mustProcessor(t, stepdef.Source, "topic:"+t.Name(), nil).(stepdef.ReadySourceProcessor)
	for range 2 {
		ctx, cancel := context.WithCancel(context.Background())
		ready, done := make(chan struct{}), make(chan error, 1)
		got := make(chan message.Message, 1)
		go func() {
			done <- src.RunReady(ctx, func(m message.Message, reply func(message.Message, error)) error {
				got <- m
				<-ctx.Done() // model a paused flow which never takes this message
				return ctx.Err()
			}, func() { close(ready) })
		}()
		<-ready
		if err := topic.publish(context.Background(), message.New("current")); err != nil {
			t.Fatal(err)
		}
		select {
		case m := <-got:
			if m[message.Body] != "current" {
				t.Fatalf("replayed old message: %v", m)
			}
		case <-time.After(time.Second):
			t.Fatal("source did not emit")
		}
		if err := topic.publish(context.Background(), message.New("discard on stop")); err != nil {
			t.Fatal(err)
		}
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(time.Second):
			t.Fatal("source did not stop")
		}
		if len(topic.subs) != 0 {
			t.Fatal("stopped source left a subscription")
		}
	}
}

func TestTopicConcurrentPublicationAndMembership(t *testing.T) {
	topic := topicNamed(t.Name())
	a := topic.subscribe(context.Background())
	b := topic.subscribe(context.Background())
	defer topic.unsubscribe(a)
	defer topic.unsubscribe(b)
	var wg sync.WaitGroup
	for producer := range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 100 {
				if err := topic.publish(context.Background(), message.New(producer*100+i)); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range 100 {
			ctx, cancel := context.WithCancel(context.Background())
			sub := topic.subscribe(ctx)
			cancel()
			topic.unsubscribe(sub)
		}
	}()
	wg.Wait()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	seen := map[any]bool{}
	for range 400 {
		left, err := a.q.take(ctx)
		if err != nil {
			t.Fatal(err)
		}
		right, err := b.q.take(ctx)
		if err != nil || left.m[message.Body] != right.m[message.Body] || seen[left.m[message.Body]] {
			t.Fatalf("inconsistent publication order or duplicate: %v, %v, %v", left, right, err)
		}
		seen[left.m[message.Body]] = true
	}
}

func TestTopicInvalidAndCancelled(t *testing.T) {
	for _, kind := range []string{stepdef.Source, stepdef.Action} {
		wantInvalid(t, kind, "topic", nil, "missing required option path")
		wantInvalid(t, kind, "topic", map[string]any{"path": ""}, "empty topic name")
		wantInvalid(t, kind, "topic:x", map[string]any{"exchangePattern": "InOut"}, "unknown option")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := topicNamed(t.Name()).publish(ctx, message.New(nil)); !errors.Is(err, context.Canceled) {
		t.Fatalf("publish = %v", err)
	}
}
