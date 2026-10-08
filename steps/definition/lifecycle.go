package definition

import "context"

// InternalSourceProcessor consumes work produced inside the process. Service
// shutdown keeps these sources alive until accepted work has drained.
type InternalSourceProcessor interface {
	SourceProcessor
	LocalChannel() string
}

// LocalProducer identifies the in-process destinations a processor writes to.
// A service deployment must include a consumer for each destination.
type LocalProducer interface {
	LocalTargets() []string
}

// WorkTracker accounts for accepted work across asynchronous hand-offs. BeginWork
// returns an idempotent release function. queued distinguishes buffered messages
// from messages being handed to or executed by a runner.
type WorkTracker interface {
	BeginWork(queued bool) func()
}

type trackerKey struct{}
type activationKey struct{}
type shutdownKey struct{}
type failureKey struct{}

// ReportSourceFailure makes a fatal background-listener error observable before
// potentially slow resource cleanup completes.
func ReportSourceFailure(ctx context.Context, err error) {
	if report, ok := ctx.Value(failureKey{}).(func(error)); ok && err != nil {
		report(err)
	}
}

func WithSourceFailureReporter(ctx context.Context, report func(error)) context.Context {
	return context.WithValue(ctx, failureKey{}, report)
}

// WithActivation defers message admission until all sources have initialized.
func WithActivation(ctx context.Context, ready <-chan struct{}) context.Context {
	return context.WithValue(ctx, activationKey{}, ready)
}

func AwaitActivation(ctx context.Context) error {
	if ready, ok := ctx.Value(activationKey{}).(<-chan struct{}); ok {
		select {
		case <-ready:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return ctx.Err()
}

func WithShutdownContext(ctx, shutdown context.Context) context.Context {
	return context.WithValue(ctx, shutdownKey{}, shutdown)
}

// ShutdownContext lets a source use the service's drain budget for cleanup.
// Interactive callers may supply their own fallback timeout.
func ShutdownContext(ctx context.Context) context.Context {
	shutdown, _ := ctx.Value(shutdownKey{}).(context.Context)
	return shutdown
}

func WithWorkTracker(ctx context.Context, tracker WorkTracker) context.Context {
	return context.WithValue(ctx, trackerKey{}, tracker)
}

// TrackWork is a no-op for ordinary embedded and interactive flows.
func TrackWork(ctx context.Context, queued bool) func() {
	if tracker, ok := ctx.Value(trackerKey{}).(WorkTracker); ok {
		return tracker.BeginWork(queued)
	}
	return func() {}
}
