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

// chanSource emits every message sent on it, so a test decides when messages arrive.
type chanSource chan message.Message

func (c chanSource) Run(ctx context.Context, emit func(message.Message) error) error {
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

func (failOn) Process(_ context.Context, m message.Message) (message.Message, error) {
	if m[message.Body] == "fail" {
		return nil, errors.New("boom")
	}
	return m, nil
}

// flow builds source -> failOn sink; src may be nil for a flow fed only by Send.
func flow(src stepdef.SourceProcessor) *flowdef.Flow {
	sink := &flowdef.Node{ID: "f", Kind: flowdef.Sink, Processor: failOn{}}
	return &flowdef.Flow{Source: &flowdef.Node{ID: "s", Kind: flowdef.Source, Processor: src, Next: []*flowdef.Node{sink}}}
}

type result struct {
	res *Result
	err error
}

func setup() (*Runner, chanSource, chan result) {
	src := make(chanSource)
	results := make(chan result, 10)
	r := NewRunner(flow(src), func(res *Result, err error) {
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
	if got := next(t, results); got.err != nil || got.res.Message[message.Body] != "a" {
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
	if got := next(t, results); got.res.Message[message.Body] != "b" {
		t.Fatalf("result = %+v, want body b", got)
	}

	must(t, r.Stop())
	wantState(t, r, Stopped)
	must(t, r.Wait())
}

func TestStartWhilePausedResumes(t *testing.T) {
	r, src, results := setup()
	must(t, r.Start())
	must(t, r.Pause())
	src <- message.New("held")

	must(t, r.Start())
	wantState(t, r, Started)
	if got := next(t, results); got.res.Message[message.Body] != "held" {
		t.Fatalf("result = %+v, want body held", got)
	}
	must(t, r.Stop())
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
	if got := next(t, results); got.res.Message[message.Body] != "after" {
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
	r := NewRunner(flow(nil), func(res *Result, err error) {
		results <- result{res, err}
	})

	if r.Send(message.New("x")) == nil {
		t.Error("send while stopped: want error")
	}

	must(t, r.Start())
	must(t, r.Send(message.New("a")))
	if got := next(t, results); got.res.Message[message.Body] != "a" {
		t.Fatalf("result = %+v, want body a", got)
	}

	must(t, r.Pause())
	if r.Send(message.New("x")) == nil {
		t.Error("send while paused: want error")
	}
	must(t, r.Resume())
	must(t, r.Send(message.New("b")))
	if got := next(t, results); got.res.Message[message.Body] != "b" {
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

// waitFor blocks until release is closed, then fails if its context was cancelled.
type waitFor chan struct{}

func (w waitFor) Process(ctx context.Context, m message.Message) (message.Message, error) {
	<-w
	return m, ctx.Err()
}

func TestStopCompletesTakenMessage(t *testing.T) {
	release := make(waitFor)
	results := make(chan result, 1)
	f := flow(nil)
	f.Source.Next[0].Processor = release
	r := NewRunner(f, func(res *Result, err error) { results <- result{res, err} })

	must(t, r.Start())
	must(t, r.Send(message.New("taken")))
	stopped := make(chan struct{})
	go func() {
		must(t, r.Stop())
		close(stopped)
	}()

	select {
	case <-stopped:
		t.Fatal("stop returned before the taken message completed")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	<-stopped
	if got := next(t, results); got.err != nil || got.res.Message[message.Body] != "taken" {
		t.Errorf("result = %+v, want the taken message completed", got)
	}
}

// untilCancelled blocks until its context is cancelled, like a slow processor honouring ctx.
type untilCancelled struct{ started chan struct{} }

func (u untilCancelled) Process(ctx context.Context, m message.Message) (message.Message, error) {
	close(u.started)
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestForceStopAbortsTakenMessage(t *testing.T) {
	started := make(chan struct{})
	results := make(chan result, 1)
	f := flow(nil)
	f.Source.Next[0].Processor = untilCancelled{started}
	r := NewRunner(f, func(res *Result, err error) { results <- result{res, err} })

	must(t, r.Start())
	must(t, r.Send(message.New("lost")))
	<-started

	stopped := make(chan error)
	go func() { stopped <- r.ForceStop() }()
	select {
	case err := <-stopped:
		must(t, err)
	case <-time.After(time.Second):
		t.Fatal("force stop did not stop the message in progress")
	}
	wantState(t, r, Stopped)
	if got := next(t, results); got.err == nil || !strings.Contains(got.err.Error(), "aborted by forced stop") {
		t.Errorf("result err = %v, want aborted by forced stop", got.err)
	}
	if r.ForceStop() == nil {
		t.Error("force stop while stopped: want error")
	}

	must(t, r.Start()) // the next run is not aborted
	defer r.Stop()
	f.Source.Next[0].Processor = failOn{}
	must(t, r.Send(message.New("ok")))
	if got := next(t, results); got.err != nil {
		t.Errorf("after restart: %v", got.err)
	}
}

// logs writes "processed <body>" to the flow's logger.
type logs struct{}

func (logs) Process(ctx context.Context, m message.Message) (message.Message, error) {
	stepdef.Logger(ctx).Printf("processed %v", m[message.Body])
	return m, nil
}

func TestSetLogger(t *testing.T) {
	var buf bytes.Buffer
	results := make(chan result, 1)
	f := flow(nil)
	f.Source.Next[0].Processor = logs{}
	r := NewRunner(f, func(res *Result, err error) { results <- result{res, err} })
	r.SetLogger(log.New(&buf, "", 0))

	must(t, r.Start())
	must(t, r.Send(message.New("a")))
	next(t, results)
	must(t, r.Stop())
	if buf.String() != "processed a\n" {
		t.Errorf("log = %q, want processed a", buf.String())
	}
}
