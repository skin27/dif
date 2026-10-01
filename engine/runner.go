package engine

import (
	"context"
	"fmt"
	"sync"

	flowdef "dif/flows/definition"
	"dif/message"
	stepdef "dif/steps/definition"
)

// State is the lifecycle state of a flow.
type State string

const (
	Stopped State = "stopped"
	Started State = "started"
	Paused  State = "paused"
)

// Runner runs a flow as a long-running task. Once started, it processes
// messages one at a time, from its source or from Send, until it is stopped.
//
//	Stopped --Start--> Started --Pause--> Paused --Resume--> Started
//	Started|Paused --Stop--> Stopped
type Runner struct {
	flow     *flowdef.Flow
	source   stepdef.Source       // nil when messages only arrive through Send
	onResult func(*Result, error) // called for every message, success or failure
	inbox    chan *message.Message

	mu     sync.Mutex
	state  State
	ctx    context.Context // of the current run; done when stopping
	cancel context.CancelFunc
	resume chan struct{} // non-nil while paused; closed on Resume
	done   chan struct{} // closed when the run has ended
	err    error         // error returned by the source
}

// NewRunner returns a Stopped runner. src and onResult may be nil.
func NewRunner(f *flowdef.Flow, src stepdef.Source, onResult func(*Result, error)) *Runner {
	return &Runner{flow: f, source: src, onResult: onResult, inbox: make(chan *message.Message), state: Stopped}
}

// Start runs the flow in the background until Stop is called.
func (r *Runner) Start() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.state != Stopped {
		return r.invalid("start")
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	r.state, r.ctx, r.cancel, r.done, r.err = Started, ctx, cancel, done, nil
	go r.loop(ctx, done)
	return nil
}

// Pause stops the flow from taking new messages; a message in progress completes.
func (r *Runner) Pause() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.state != Started {
		return r.invalid("pause")
	}
	r.state, r.resume = Paused, make(chan struct{})
	return nil
}

// Resume lets a paused flow take messages again.
func (r *Runner) Resume() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.state != Paused {
		return r.invalid("resume")
	}
	close(r.resume)
	r.state, r.resume = Started, nil
	return nil
}

// Stop ends the flow and waits until it has finished.
func (r *Runner) Stop() error {
	r.mu.Lock()
	if r.state == Stopped {
		defer r.mu.Unlock()
		return r.invalid("stop")
	}
	cancel, done := r.cancel, r.done
	r.mu.Unlock()

	cancel()
	<-done
	return nil
}

// Send hands m to the running flow and returns once the flow has taken it.
// A paused or stopped flow does not accept messages.
func (r *Runner) Send(m *message.Message) error {
	r.mu.Lock()
	if r.state != Started {
		defer r.mu.Unlock()
		return r.invalid("send")
	}
	ctx := r.ctx
	r.mu.Unlock()

	select {
	case r.inbox <- m:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("cannot send: flow is stopping")
	}
}

// State returns the current lifecycle state.
func (r *Runner) State() State {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.state
}

// Wait blocks until the flow is stopped and returns the source's error, if any.
func (r *Runner) Wait() error {
	r.mu.Lock()
	done := r.done
	r.mu.Unlock()
	if done != nil {
		<-done
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	return r.err
}

// loop processes messages until the run is stopped. The source runs beside it;
// a source that finishes does not end the flow.
func (r *Runner) loop(ctx context.Context, done chan struct{}) {
	srcErr := make(chan error, 1)
	if r.source == nil {
		srcErr <- nil
	} else {
		go func() {
			srcErr <- r.source.Run(ctx, func(m *message.Message) error { return r.emit(ctx, m) })
		}()
	}

	for {
		select {
		case m := <-r.inbox:
			res, err := Run(r.flow, m)
			if r.onResult != nil {
				r.onResult(res, err) // a failing message is reported; the flow keeps going
			}
		case <-ctx.Done():
			err := <-srcErr
			r.mu.Lock()
			r.state, r.err, r.resume = Stopped, err, nil
			r.mu.Unlock()
			close(done)
			return
		}
	}
}

// emit hands a message from the source to the flow, waiting while the flow is paused.
func (r *Runner) emit(ctx context.Context, m *message.Message) error {
	r.mu.Lock()
	resume := r.resume
	r.mu.Unlock()

	if resume != nil {
		select {
		case <-resume:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	select {
	case r.inbox <- m:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *Runner) invalid(action string) error {
	return fmt.Errorf("cannot %s: flow is %s", action, r.state)
}
