package engine

import (
	"context"
	"errors"
	"testing"
	"time"

	"dif/message"
	stepdef "dif/steps/definition"
)

func TestStartContextReportsSourceFailure(t *testing.T) {
	want := errors.New("bind failed")
	r := NewRunner(flow(readyTestSource{entered: make(chan struct{}), err: want}), nil)
	if err := r.StartContext(context.Background()); !errors.Is(err, want) {
		t.Fatalf("start: %v", err)
	}
	if r.Status().Source != "failed" {
		t.Fatal(r.Status())
	}
	if err := r.Stop(); err != nil {
		t.Fatal(err)
	}
}

func TestStartContextTimeoutCancelsSource(t *testing.T) {
	r := NewRunner(flow(readyTestSource{entered: make(chan struct{}), allow: make(chan struct{})}), nil)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := r.StartContext(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("start: %v", err)
	}
	select {
	case <-r.SourceDone():
	case <-time.After(time.Second):
		t.Fatal("source leaked")
	}
	stop, cancelStop := context.WithTimeout(context.Background(), time.Second)
	defer cancelStop()
	if err := r.StopContext(stop); err != nil {
		t.Fatal(err)
	}
}

type stubbornAction struct{ entered, release chan struct{} }

func (s stubbornAction) Process(_ context.Context, m message.Message) (message.Message, error) {
	close(s.entered)
	<-s.release
	return m, nil
}

func TestStopContextBoundsUncooperativeProcessor(t *testing.T) {
	s := stubbornAction{make(chan struct{}), make(chan struct{})}
	f := flow(nil)
	f.Source.Next[0].Processor = s
	r := NewRunner(f, nil)
	if err := r.StartContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := r.Send(message.New("work")); err != nil {
		t.Fatal(err)
	}
	<-s.entered
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := r.StopContext(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("stop: %v", err)
	}
	close(s.release)
	if err := r.Wait(); err != nil {
		t.Fatal(err)
	}
}

func TestActivationAndWorkAccounting(t *testing.T) {
	activate := make(chan struct{})
	var work Work
	ctx := stepdef.WithActivation(stepdef.WithWorkTracker(context.Background(), &work), activate)
	src := make(chanSource)
	r := NewRunner(flow(src), nil)
	if err := r.StartContext(ctx); err != nil {
		t.Fatal(err)
	}
	src <- message.New("queued at startup")
	if r.Status().Completed != 0 {
		t.Fatal("processed before activation")
	}
	close(activate)
	r.StopSource()
	<-r.SourceDone()
	stop, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := work.Wait(stop); err != nil {
		t.Fatal(err)
	}
	if err := r.StopContext(stop); err != nil {
		t.Fatal(err)
	}
	a, q := work.Counts()
	if a != 0 || q != 0 {
		t.Fatalf("work leaked: %d %d", a, q)
	}
}
