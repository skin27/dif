package impl

import (
	"context"
	"errors"
	"strings"
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
		if m, _ := q.take(context.Background()); m[message.Body] != i {
			t.Fatalf("took %v, want %d", m[message.Body], i)
		}
	}
	q.putBack(message.New("back"))
	if m, _ := q.take(context.Background()); m[message.Body] != "back" {
		t.Errorf("took %v, want the message put back first", m[message.Body])
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
	if m, err := q.take(context.Background()); err != nil || m[message.Body] != "late" {
		t.Errorf("took %v, %v", m, err)
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
	wantInvalid(t, stepdef.Source, "queue:x", map[string]any{"transport": "activemq"}, "unknown option transport")
}
