package engine

import (
	"context"
	"errors"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"

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
//	Stopped --Start--> Started --Pause--> Paused --Start|Resume--> Started
//	Started|Paused --Stop|ForceStop--> Stopped
//
// A runner has at most one run at a time: one goroutine executing messages,
// plus one for its source, if it has one.
type Runner struct {
	flow     *flowdef.Flow
	source   stepdef.SourceProcessor // nil when messages only arrive through Send
	onResult func(*Result, error)    // called for every message, success or failure
	inbox    chan envelope

	// Message counts over the runner's lifetime; a restart does not reset them.
	completed, failed atomic.Int64
	inFlight          atomic.Int64
	durationNanos     atomic.Int64

	mu           sync.Mutex
	logger       *log.Logger // the flow's logger for processors; nil for the standard logger
	state        State
	ctx          context.Context // of the current run; done when stopping
	cancel       context.CancelFunc
	abort        context.CancelFunc // cancels the context messages run with; see ForceStop
	resume       chan struct{}      // non-nil while paused; closed on Resume
	done         chan struct{}      // closed when the run has ended
	since        time.Time          // when the current run started; zero when stopped
	err          error              // error returned by the source
	sourceCancel context.CancelFunc
	sourceDone   chan struct{}
	sourceFailed chan struct{}
	sourceErr    error
	sourceState  string
	accepting    bool
}

// NewRunner returns a Stopped runner for f. Messages come from the source
// node's SourceProcessor, if it has one, and from Send. onResult may be nil.
func NewRunner(f *flowdef.Flow, onResult func(*Result, error)) *Runner {
	src, _ := f.Source.Processor.(stepdef.SourceProcessor)
	return &Runner{flow: f, source: src, onResult: onResult, inbox: make(chan envelope), state: Stopped}
}

// envelope is a message waiting to be processed, with the source's reply callback (nil if none).
type envelope struct {
	msg      message.Message
	reply    func(message.Message, error)
	release  func()
	complete func(error) error
}

// ID returns the flow's id.
func (r *Runner) ID() string { return r.flow.ID }

// NewMessage returns a new message built from the flow's configured input
// message, or an empty message if there is none. Its metadata is always fresh.
func (r *Runner) NewMessage() message.Message {
	m := message.New(nil)
	delete(m, message.CorrelationID) // default after applying configured identity headers
	for k, v := range r.flow.Input {
		if !message.IsMetadata(k) {
			m[k] = v
		}
	}
	m.EnsureIdentity()
	return m
}

// Start runs the flow in the background until Stop is called.
// Starting a paused flow resumes it; its run continues. For a ReadySourceProcessor,
// Start waits until the source signals readiness or exits (see Wait for errors).
func (r *Runner) Start() error {
	return r.start(context.Background(), false)
}

// StartContext waits for source readiness or failure, bounded by ctx. Its
// cancellation stops startup, not accepted messages. Context values (including
// a shared WorkTracker) propagate to the source and processors for this run.
// Legacy Start retains its historical initialization-error behavior.
func (r *Runner) StartContext(ctx context.Context) error {
	return r.start(ctx, true)
}

func (r *Runner) start(wait context.Context, strict bool) error {
	if err := wait.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	switch r.state {
	case Paused:
		r.unpause()
		r.mu.Unlock()
		return nil
	case Started:
		err := r.invalid("start")
		r.mu.Unlock()
		return err
	}

	ctx := context.WithoutCancel(wait)
	if r.logger != nil {
		ctx = stepdef.WithLogger(ctx, r.logger)
	}
	// Stop cancels ctx: the source stops and no new message is taken. A message
	// the flow has taken runs with msgCtx, which only ForceStop cancels, so by
	// default it completes and is never lost halfway.
	ctx, cancel := context.WithCancel(ctx)
	sourceCtx, sourceCancel := context.WithCancel(ctx)
	msgCtx, abort := context.WithCancel(context.WithoutCancel(ctx))
	if strict {
		sourceCtx = stepdef.WithShutdownContext(sourceCtx, msgCtx)
	}
	done := make(chan struct{})
	r.state, r.ctx, r.cancel, r.abort, r.done, r.err, r.since = Started, ctx, cancel, abort, done, nil, time.Now()
	r.sourceCancel, r.sourceDone, r.sourceErr, r.sourceState, r.accepting = sourceCancel, make(chan struct{}), nil, "starting", true
	r.sourceFailed = make(chan struct{})
	var failureOnce sync.Once
	sourceCtx = stepdef.WithSourceFailureReporter(sourceCtx, func(err error) {
		failureOnce.Do(func() {
			r.mu.Lock()
			r.sourceErr, r.sourceState = err, "failed"
			close(r.sourceFailed)
			r.mu.Unlock()
		})
	})
	var ready chan struct{}
	_, hasReady := r.source.(stepdef.ReadySourceProcessor)
	_, hasCompletion := r.source.(stepdef.CompletionSourceProcessor)
	if hasReady || hasCompletion {
		ready = make(chan struct{})
	}
	go r.loop(ctx, sourceCtx, msgCtx, done, ready)
	r.mu.Unlock()
	if ready != nil {
		select {
		case <-ready:
		case <-wait.Done():
			r.Cancel()
			return wait.Err()
		}
	}
	if strict {
		if err := wait.Err(); err != nil {
			r.Cancel()
			return err
		}
		r.mu.Lock()
		err := r.sourceErr
		r.mu.Unlock()
		return err
	}
	return nil
}

// StopSource prevents new direct sends and stops the source while preserving
// accepted message processing. Internal consumers should be stopped last.
func (r *Runner) StopSource() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.accepting = false
	if r.sourceCancel != nil {
		r.sourceCancel()
	}
}

// Cancel requests immediate cancellation without waiting for cooperative code.
func (r *Runner) Cancel() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.accepting = false
	if r.abort != nil {
		r.abort()
	}
	if r.cancel != nil {
		r.cancel()
	}
}

func (r *Runner) SourceDone() <-chan struct{} {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sourceDone
}

func (r *Runner) SourceFailed() <-chan struct{} {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sourceFailed
}

func (r *Runner) SourceError() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sourceErr
}

// StopContext bounds the wait for graceful completion. On timeout it requests
// forced cancellation and returns even if a custom processor ignores it.
func (r *Runner) StopContext(ctx context.Context) error {
	r.mu.Lock()
	if r.state == Stopped {
		r.mu.Unlock()
		return nil
	}
	r.accepting = false
	r.cancel()
	done := r.done
	r.mu.Unlock()
	select {
	case <-done:
		err := r.Wait()
		if errors.Is(err, context.Canceled) && r.Status().Source == "cancelled" {
			return nil
		}
		return err
	case <-ctx.Done():
		r.Cancel()
		return ctx.Err()
	}
}

// SetLogger sets the logger the flow's processors log to, such as the log
// step; it applies from the next Start. Without one they use the standard logger.
func (r *Runner) SetLogger(l *log.Logger) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.logger = l
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
	r.unpause()
	return nil
}

// unpause opens the pause gate. r.mu must be held and the state Paused.
func (r *Runner) unpause() {
	close(r.resume)
	r.state, r.resume = Started, nil
}

// Stop ends the flow and waits until it has finished. A message the flow has
// already taken completes first.
func (r *Runner) Stop() error { return r.stop(false) }

// ForceStop ends the flow immediately: a message in progress is cancelled
// before its next step (and processors honouring the context stop at once),
// so it may be lost. It is reported as failed. ForceStop waits until the
// flow has finished.
func (r *Runner) ForceStop() error { return r.stop(true) }

func (r *Runner) stop(force bool) error {
	r.mu.Lock()
	if r.state == Stopped {
		defer r.mu.Unlock()
		return r.invalid("stop")
	}
	cancel, abort, done := r.cancel, r.abort, r.done
	r.mu.Unlock()

	if force {
		abort()
	}
	cancel()
	<-done
	return nil
}

// Send hands m to the running flow and returns once the flow has taken it:
// a one-way exchange. A paused or stopped flow does not accept messages.
func (r *Runner) Send(m message.Message) error {
	return r.send(context.Background(), envelope{msg: m})
}

// Request hands m to the running flow and waits for the reply: the message
// the flow ends with, or why it failed. A flow that makes the exchange
// one-way (message.InOnly) replies as soon as it does, with the message at
// that point. Waiting ends when ctx is done; the flow still processes m.
func (r *Runner) Request(ctx context.Context, m message.Message) (message.Message, error) {
	type outcome struct {
		m   message.Message
		err error
	}
	done := make(chan outcome, 1) // the flow never waits for a caller that gave up
	if err := r.send(ctx, envelope{msg: m, reply: func(out message.Message, err error) { done <- outcome{out, err} }}); err != nil {
		return nil, err
	}
	select {
	case o := <-done:
		return o.m, o.err
	case <-ctx.Done():
		return nil, fmt.Errorf("flow %s did not reply: %w", r.flow.ID, ctx.Err())
	}
}

// send hands e to the running flow, waiting until the flow takes it or ctx is done.
func (r *Runner) send(ctx context.Context, e envelope) error {
	r.mu.Lock()
	if r.state != Started || !r.accepting {
		defer r.mu.Unlock()
		return r.invalid("send")
	}
	runCtx := r.ctx
	r.mu.Unlock()
	e.release = stepdef.TrackWork(runCtx, false)

	select {
	case r.inbox <- e:
		return nil
	case <-runCtx.Done():
		e.release()
		return fmt.Errorf("cannot send: flow is stopping")
	case <-ctx.Done():
		e.release()
		return fmt.Errorf("cannot send: %w", ctx.Err())
	}
}

// State returns the current lifecycle state.
func (r *Runner) State() State {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.state
}

// Status returns the flow's id, state, the time its current run started and its message counts.
func (r *Runner) Status() FlowStatus {
	r.mu.Lock()
	defer r.mu.Unlock()
	return FlowStatus{ID: r.flow.ID, State: r.state, Since: r.since, Completed: r.completed.Load(), Failed: r.failed.Load(), Source: r.sourceState, InFlight: r.inFlight.Load(), DurationNanos: r.durationNanos.Load()}
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
func (r *Runner) loop(ctx, sourceCtx, msgCtx context.Context, done, ready chan struct{}) {
	srcErr := make(chan error, 1)
	finishSource := func(err error) {
		// Publish completion only after logging, so Stop/Wait also wait for the
		// final source log write and callers can safely inspect their log buffer.
		if err != nil {
			stepdef.Logger(ctx).Printf("source stopped: %v", err)
		}
		if err != nil && sourceCtx.Err() == nil {
			stepdef.ReportSourceFailure(sourceCtx, err)
		}
		r.mu.Lock()
		if r.sourceErr == nil {
			r.sourceErr = err
		}
		switch {
		case r.sourceState == "failed": // preserve a failure reported before cleanup
		case sourceCtx.Err() != nil:
			r.sourceState = "cancelled"
		case err != nil:
			r.sourceState = "failed"
		default:
			r.sourceState = "completed"
		}
		close(r.sourceDone)
		r.mu.Unlock()
		srcErr <- err
	}
	if r.source == nil {
		finishSource(nil)
	} else {
		go func() {
			emit := func(m message.Message, reply func(message.Message, error)) error {
				return r.emit(sourceCtx, envelope{msg: m, reply: reply})
			}
			var err error
			_, readySource := r.source.(stepdef.ReadySourceProcessor)
			_, completionSource := r.source.(stepdef.CompletionSourceProcessor)
			if readySource || completionSource {
				var once sync.Once
				signal := func() {
					once.Do(func() {
						r.mu.Lock()
						if r.sourceErr == nil {
							r.sourceState = "ready"
						}
						r.mu.Unlock()
						close(ready)
					})
				}
				if src, ok := r.source.(stepdef.CompletionSourceProcessor); ok {
					err = src.RunDelivery(sourceCtx, func(m message.Message, reply func(message.Message, error), complete func(error) error) error {
						return r.emit(sourceCtx, envelope{msg: m, reply: reply, complete: complete})
					}, signal)
				} else {
					err = r.source.(stepdef.ReadySourceProcessor).RunReady(sourceCtx, emit, signal)
				}
				finishSource(err)
				once.Do(func() { close(ready) }) // failure must release Start after recording its outcome
			} else {
				r.mu.Lock()
				r.sourceState = "running"
				r.mu.Unlock()
				err = r.source.Run(sourceCtx, emit)
				finishSource(err)
			}
		}()
	}

	for {
		select {
		case e := <-r.inbox:
			r.inFlight.Add(1)
			started := time.Now()
			res, err := execute(msgCtx, r.flow, e.msg, e.reply) // replies to the sender
			if err != nil && msgCtx.Err() != nil {
				err = fmt.Errorf("aborted by forced stop: %w", err)
			}
			if e.complete != nil {
				completionErr := e.complete(err)
				if completionErr != nil {
					stepdef.ReportSourceFailure(sourceCtx, completionErr)
				}
				err = errors.Join(err, completionErr)
			}
			if err != nil {
				r.failed.Add(1)
			} else {
				r.completed.Add(1) // also when an error route handled it
			}
			if r.onResult != nil {
				r.onResult(res, err) // a failing message is reported; the flow keeps going
			}
			r.durationNanos.Add(time.Since(started).Nanoseconds())
			r.inFlight.Add(-1)
			if e.release != nil {
				e.release()
			}
		case <-ctx.Done():
			err := <-srcErr
			r.mu.Lock()
			r.state, r.err, r.resume, r.since = Stopped, err, nil, time.Time{}
			r.abort() // release msgCtx
			r.mu.Unlock()
			close(done)
			return
		}
	}
}

// emit hands a message from the source to the flow, waiting while the flow is paused.
func (r *Runner) emit(ctx context.Context, e envelope) error {
	if err := stepdef.AwaitActivation(ctx); err != nil {
		return err
	}
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
	e.release = stepdef.TrackWork(ctx, false)
	select {
	case r.inbox <- e:
		return nil
	case <-ctx.Done():
		e.release()
		return ctx.Err()
	}
}

func (r *Runner) invalid(action string) error {
	return fmt.Errorf("cannot %s: flow is %s", action, r.state)
}
