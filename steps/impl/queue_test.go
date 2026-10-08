package impl

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"dif/message"
	stepdef "dif/steps/definition"
)

// Queues are shared by the whole process, so every test uses its own names.

func TestQueueOrderAndCapacity(t *testing.T) {
	q := queueNamed(t.Name())
	if queueNamed(t.Name()) != q {
		t.Fatal("a name gives one queue")
	}
	for i := range queueCapacity {
		if err := q.put(message.New(i)); err != nil {
			t.Fatal(err)
		}
	}
	if err := q.put(message.New("one too many")); err == nil || !strings.Contains(err.Error(), "is full (10000 messages)") {
		t.Errorf("err = %v, want full", err)
	}
	for i := range 3 {
		if e, _ := q.take(context.Background()); e.m[message.Body] != i {
			t.Fatalf("took %v, want %d", e.m[message.Body], i)
		}
	}
	q.putBack(queued{m: message.New("back")})
	if e, _ := q.take(context.Background()); e.m[message.Body] != "back" {
		t.Errorf("took %v, want the message put back first", e.m[message.Body])
	}
	if q.len() != queueCapacity-3 {
		t.Errorf("len = %d", q.len())
	}
}

func TestQueueTakeWaits(t *testing.T) {
	q := queueNamed(t.Name())
	go func() {
		time.Sleep(20 * time.Millisecond)
		q.put(message.New("late"))
	}()
	if e, err := q.take(context.Background()); err != nil || e.m[message.Body] != "late" {
		t.Errorf("took %v, %v", e.m, err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := q.take(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want the context's error", err)
	}
}

func TestDeadLetterToQueueSource(t *testing.T) {
	name := "DLQ:" + t.Name()
	sink := mustProcessor(t, stepdef.Sink, "deadletter", map[string]any{"deadLetterQueue": name, "connectionFactory": "x"}).(stepdef.SinkProcessor)
	m := message.New("failed")
	m["error.message"] = "boom"
	if err := sink.Consume(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	m[message.Body] = "changed later" // the queue holds a copy

	src := mustProcessor(t, stepdef.Source, "queue:"+name, nil).(stepdef.SourceProcessor)
	ctx, cancel := context.WithCancel(context.Background())
	var got []message.Message
	done := make(chan error)
	go func() {
		done <- src.Run(ctx, func(m message.Message, _ func(message.Message, error)) error {
			got = append(got, m)
			cancel()
			return nil
		})
	}()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0][message.Body] != "failed" || got[0]["error.message"] != "boom" || got[0][message.TraceID] != m[message.TraceID] {
		t.Errorf("emitted %v, want the dead letter as it was put", got)
	}
}

func TestQueueActionToQueueSource(t *testing.T) {
	src := mustProcessor(t, stepdef.Source, "queue:"+t.Name(), map[string]any{"transport": "activemq"}).(stepdef.SourceProcessor)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go src.Run(ctx, func(m message.Message, reply func(message.Message, error)) error {
		out := m.Copy()
		out[message.Body] = "reply to " + text(m[message.Body])
		reply(out, nil)
		return nil
	})

	for pattern, want := range map[string]string{"InOut": "reply to hi", "InOnly": "hi"} {
		a := mustProcessor(t, stepdef.Action, "queue", map[string]any{"targetQueueId": t.Name(), "exchangePattern": pattern, "requestTimeout": "1000"}).(stepdef.ActionProcessor)
		out, err := a.Process(context.Background(), message.New("hi"))
		if err != nil || out[message.Body] != want {
			t.Errorf("%s: %v, %v, want body %q", pattern, out[message.Body], err, want)
		}
	}

	a := mustProcessor(t, stepdef.Action, "queue", map[string]any{"targetQueueId": t.Name() + "-nobody", "requestTimeout": 20}).(stepdef.ActionProcessor)
	if _, err := a.Process(context.Background(), message.New("hi")); err == nil || !strings.Contains(err.Error(), "did not reply within 20ms") {
		t.Errorf("err = %v, want a timeout", err)
	}
}

func TestQueueSourceKeepsMessageWhenFlowStops(t *testing.T) {
	q := queueNamed(t.Name())
	q.put(message.New("kept"))
	src := mustProcessor(t, stepdef.Source, "queue:"+t.Name(), nil).(stepdef.SourceProcessor)
	src.Run(context.Background(), func(message.Message, func(message.Message, error)) error {
		return errors.New("flow is stopping")
	})
	if q.len() != 1 {
		t.Errorf("queue holds %d messages, want the one the flow did not take", q.len())
	}
}

func TestQueueInvalid(t *testing.T) {
	wantInvalid(t, stepdef.Sink, "deadletter", map[string]any{"deadLetterQueue": ""}, "option deadLetterQueue: empty queue name")
	wantInvalid(t, stepdef.Source, "queue", nil, "missing required option path")
	wantInvalid(t, stepdef.Source, "queue:x", map[string]any{"connectionFactory": "x"}, "unknown option connectionFactory")
	wantInvalid(t, stepdef.Action, "queue", nil, "missing required option targetQueueId")
	wantInvalid(t, stepdef.Action, "queue", map[string]any{"targetQueueId": ""}, "option targetQueueId: empty queue id")
	wantInvalid(t, stepdef.Action, "queue", map[string]any{"targetQueueId": "q", "exchangePattern": "InOptionalOut"}, "exchangePattern")
}

func TestQueueEnqueueWithoutConsumer(t *testing.T) {
	a := mustProcessor(t, stepdef.Action, "queue:"+t.Name(), map[string]any{"delivery": "enqueue"}).(stepdef.ActionProcessor)
	m := message.New("original")
	out, err := a.Process(context.Background(), m)
	if err != nil || out[message.Body] != "original" {
		t.Fatalf("enqueue = %v, %v", out, err)
	}
	m[message.Body] = "changed"
	q := queueNamed(t.Name())
	e, err := q.take(context.Background())
	if err != nil || e.m[message.Body] != "original" || e.reply != nil || e.wait != nil {
		t.Fatalf("queued = %v, %v", e, err)
	}
	for range queueCapacity {
		if _, err := a.Process(context.Background(), m); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := a.Process(context.Background(), m); err == nil {
		t.Fatal("expected full queue error")
	}
}

func TestQueueCompetingConsumers(t *testing.T) {
	name := t.Name()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	got := make(chan int, 200)
	var wg sync.WaitGroup
	for range 2 {
		src := mustProcessor(t, stepdef.Source, "queue:"+name, nil).(stepdef.SourceProcessor)
		wg.Add(1)
		go func() {
			defer wg.Done()
			src.Run(ctx, func(m message.Message, _ func(message.Message, error)) error {
				got <- m[message.Body].(int)
				return nil
			})
		}()
	}
	a := mustProcessor(t, stepdef.Action, "queue:"+name, map[string]any{"delivery": "enqueue"}).(stepdef.ActionProcessor)
	for i := range 200 {
		if _, err := a.Process(ctx, message.New(i)); err != nil {
			t.Fatal(err)
		}
	}
	seen := map[int]bool{}
	for range 200 {
		select {
		case i := <-got:
			if seen[i] {
				t.Fatalf("duplicate delivery %d", i)
			}
			seen[i] = true
		case <-time.After(time.Second):
			t.Fatal("missing message")
		}
	}
	cancel()
	wg.Wait()
	if len(got) != 0 {
		t.Fatal("extra deliveries")
	}
}

func TestQueueDeliveryValidation(t *testing.T) {
	wantInvalid(t, stepdef.Action, "queue:x", map[string]any{"targetQueueId": "y"}, "different queues")
	wantInvalid(t, stepdef.Action, "queue:x", map[string]any{"delivery": "enqueue", "exchangePattern": "InOut"}, "does not support")
	wantInvalid(t, stepdef.Action, "queue:x", map[string]any{"delivery": "unknown"}, "is not one of")
	wantInvalid(t, stepdef.Source, "queue", map[string]any{"path": ""}, "empty queue name")
	mustProcessor(t, stepdef.Action, "queue:x", map[string]any{"targetQueueId": "x"})
	a := mustProcessor(t, stepdef.Action, "queue:"+t.Name(), nil).(flowLinkAction)
	if !a.wait {
		t.Fatal("default queue delivery must still wait for processing")
	}
}
