package engine

import (
	"context"
	"errors"
	"testing"
	"time"

	flowdef "dif/flows/definition"
	"dif/message"
)

// chanSource emits every message sent on it, so a test decides when messages arrive.
type chanSource chan *message.Message

func (c chanSource) Run(ctx context.Context, emit func(*message.Message) error) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case m, ok := <-c:
			if !ok || emit(m) != nil {
				return nil
			}
		}
	}
}

// failOn fails messages whose body is "fail".
type failOn struct{}

func (failOn) Execute(m *message.Message) (*message.Message, error) {
	if m.Body == "fail" {
		return nil, errors.New("boom")
	}
	return m, nil
}

type result struct {
	res *Result
	err error
}

func setup() (*Runner, chanSource, chan result) {
	src := make(chanSource)
	results := make(chan result, 10)
	node := &flowdef.Node{ID: "s", Kind: flowdef.Source, Step: failOn{}}
	r := NewRunner(&flowdef.Flow{Source: node}, src, func(res *Result, err error) {
		results <- result{res, err}
	})
	return r, src, results
}

func next(t *testing.T, results chan result) result {
	t.Helper()
	select {
	case r := <-results:
		return r
	case <-time.After(time.Second):
		t.Fatal("no result within 1s")
		return result{}
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func wantState(t *testing.T, r *Runner, want State) {
	t.Helper()
	if got := r.State(); got != want {
		t.Fatalf("state = %s, want %s", got, want)
	}
}

func TestLifecycle(t *testing.T) {
	r, src, results := setup()
	wantState(t, r, Stopped)

	must(t, r.Start())
	wantState(t, r, Started)
	src <- message.New("a")
	if got := next(t, results); got.err != nil || got.res.Message.Body != "a" {
		t.Fatalf("result = %+v, want body a", got)
	}

	must(t, r.Pause())
	wantState(t, r, Paused)
	src <- message.New("b") // taken by the source, held at the pause gate
	select {
	case <-results:
		t.Fatal("message processed while paused")
	case <-time.After(50 * time.Millisecond):
	}

	must(t, r.Resume())
	wantState(t, r, Started)
	if got := next(t, results); got.res.Message.Body != "b" {
		t.Fatalf("result = %+v, want body b", got)
	}

	must(t, r.Stop())
	wantState(t, r, Stopped)
	must(t, r.Wait())
}

func TestInvalidTransitions(t *testing.T) {
	r, _, _ := setup()
	for name, op := range map[string]func() error{"pause": r.Pause, "resume": r.Resume, "stop": r.Stop} {
		if op() == nil {
			t.Errorf("%s while stopped: want error", name)
		}
	}

	must(t, r.Start())
	if r.Start() == nil {
		t.Error("start while started: want error")
	}
	if r.Resume() == nil {
		t.Error("resume while started: want error")
	}
	must(t, r.Pause())
	if r.Pause() == nil {
		t.Error("pause while paused: want error")
	}
	must(t, r.Stop())
}

func TestStopWhilePaused(t *testing.T) {
	r, src, results := setup()
	must(t, r.Start())
	must(t, r.Pause())
	src <- message.New("held")

	stopped := make(chan error)
	go func() { stopped <- r.Stop() }()
	select {
	case err := <-stopped:
		must(t, err)
	case <-time.After(time.Second):
		t.Fatal("stop while paused hangs")
	}
	wantState(t, r, Stopped)
	if len(results) != 0 {
		t.Error("held message was processed after stop")
	}
}

func TestSourceFinishedFlowKeepsRunning(t *testing.T) {
	r, src, results := setup()
	must(t, r.Start())
	close(src) // the source returns by itself

	must(t, r.Send(message.New("after")))
	if got := next(t, results); got.res.Message.Body != "after" {
		t.Fatalf("result = %+v, want body after", got)
	}
	wantState(t, r, Started)

	must(t, r.Stop())
	must(t, r.Wait())
	must(t, r.Start()) // a stopped flow can be started again
	must(t, r.Stop())
}

func TestSend(t *testing.T) {
	results := make(chan result, 10)
	node := &flowdef.Node{ID: "s", Kind: flowdef.Source, Step: failOn{}}
	r := NewRunner(&flowdef.Flow{Source: node}, nil, func(res *Result, err error) {
		results <- result{res, err}
	})

	if r.Send(message.New("x")) == nil {
		t.Error("send while stopped: want error")
	}

	must(t, r.Start())
	must(t, r.Send(message.New("a")))
	if got := next(t, results); got.res.Message.Body != "a" {
		t.Fatalf("result = %+v, want body a", got)
	}

	must(t, r.Pause())
	if r.Send(message.New("x")) == nil {
		t.Error("send while paused: want error")
	}
	must(t, r.Resume())
	must(t, r.Send(message.New("b")))
	if got := next(t, results); got.res.Message.Body != "b" {
		t.Fatalf("result = %+v, want body b", got)
	}

	// Without a source the flow still runs until it is stopped.
	wantState(t, r, Started)
	must(t, r.Stop())
	must(t, r.Wait())
}

func TestFailingMessageContinues(t *testing.T) {
	r, src, results := setup()
	must(t, r.Start())

	src <- message.New("fail")
	if got := next(t, results); got.err == nil {
		t.Fatal("want error for failing message")
	}
	src <- message.New("ok")
	if got := next(t, results); got.err != nil {
		t.Fatalf("next message: %v", got.err)
	}
	wantState(t, r, Started)
	must(t, r.Stop())
}
