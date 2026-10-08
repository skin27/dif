package engine

import (
	"context"
	"errors"
	"testing"
	"time"

	stepdef "dif/steps/definition"
)

type readyTestSource struct {
	entered chan struct{}
	allow   chan struct{}
	err     error
}

func (s readyTestSource) Run(context.Context, stepdef.Emit) error {
	panic("engine must use RunReady")
}

func (s readyTestSource) RunReady(ctx context.Context, _ stepdef.Emit, ready func()) error {
	close(s.entered)
	if s.err != nil {
		return s.err
	}
	select {
	case <-s.allow:
		ready()
		ready() // a repeated notification is harmless
	case <-ctx.Done():
		return nil
	}
	<-ctx.Done()
	return nil
}

func TestStartWaitsForSourceReadiness(t *testing.T) {
	src := readyTestSource{entered: make(chan struct{}), allow: make(chan struct{})}
	r := NewRunner(flow(src), nil)
	started := make(chan error, 1)
	go func() { started <- r.Start() }()
	<-src.entered
	select {
	case <-started:
		t.Fatal("Start returned before source readiness")
	default:
	}
	close(src.allow)
	select {
	case err := <-started:
		must(t, err)
	case <-time.After(time.Second):
		t.Fatal("Start did not return")
	}
	must(t, r.Stop())
}

func TestStopDuringSourceStartup(t *testing.T) {
	src := readyTestSource{entered: make(chan struct{}), allow: make(chan struct{})}
	r := NewRunner(flow(src), nil)
	started, stopped := make(chan error, 1), make(chan error, 1)
	go func() { started <- r.Start() }()
	<-src.entered
	go func() { stopped <- r.Stop() }()
	for _, done := range []chan error{started, stopped} {
		select {
		case err := <-done:
			must(t, err)
		case <-time.After(time.Second):
			t.Fatal("lifecycle deadlocked during startup")
		}
	}
}

func TestSourceFailureReleasesStart(t *testing.T) {
	failed := errors.New("initialization failed")
	r := NewRunner(flow(readyTestSource{entered: make(chan struct{}), err: failed}), nil)
	started := make(chan error, 1)
	go func() { started <- r.Start() }()
	select {
	case err := <-started:
		must(t, err)
	case <-time.After(time.Second):
		t.Fatal("Start blocked after source failure")
	}
	must(t, r.Stop())
	if err := r.Wait(); !errors.Is(err, failed) {
		t.Fatalf("Wait = %v", err)
	}
}
