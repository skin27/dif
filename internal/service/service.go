package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"dif/api"
	"dif/engine"
	"dif/message"
	stepdef "dif/steps/definition"
)

type Service struct {
	options                  Options
	logger                   *slog.Logger
	mu                       sync.RWMutex
	runners                  []*engine.Runner
	ready, started, stopping bool
	work                     engine.Work
	runtime                  *api.Runtime
}

func New(o Options, logs io.Writer) *Service {
	var handler slog.Handler = slog.NewJSONHandler(logs, nil)
	if o.LogFormat == "text" {
		handler = slog.NewTextHandler(logs, nil)
	}
	return &Service{options: o, logger: slog.New(handler)}
}

// Run owns startup, supervision and bounded shutdown. ctx signals termination;
// it never directly cancels an accepted message. The executable exits when this
// returns, even if third-party processor code ignores forced cancellation.
func (s *Service) Run(ctx context.Context) (resultErr error) {
	if err := s.options.Validate(); err != nil {
		return err
	}
	config := api.ChannelConfig{}
	if s.options.Channels != nil {
		config = *s.options.Channels
	}
	runtime, openErr := api.NewRuntime(config)
	if openErr != nil {
		return openErr
	}
	s.mu.Lock()
	s.runtime = runtime
	s.mu.Unlock()
	defer func() { resultErr = errors.Join(resultErr, runtime.Close()) }()
	startup, cancel := context.WithTimeout(ctx, duration(s.options.StartupTimeout))
	defer cancel()
	var monitor *http.Server
	monitorErrors := make(chan error, 1)
	if s.options.MonitorAddress != "" {
		ln, err := (&net.ListenConfig{}).Listen(startup, "tcp", s.options.MonitorAddress)
		if err != nil {
			return fmt.Errorf("monitor listener: %w", err)
		}
		monitor = &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 5 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10}
		go func() { monitorErrors <- monitor.Serve(ln) }()
	}
	// A failed monitoring listener also cancels startup, including remote fetches.
	monitorFailure := make(chan error, 1)
	monitorWatchDone := make(chan struct{})
	defer close(monitorWatchDone)
	go func() {
		select {
		case err := <-monitorErrors:
			if !errors.Is(err, http.ErrServerClosed) {
				monitorFailure <- err
				cancel()
			}
		case <-monitorWatchDone:
		}
	}()
	// Constructors are user-extensible and may not support cancellation. Bound
	// the wait without publishing or starting any partially constructed flows.
	type prepared struct {
		runners []*engine.Runner
		err     error
	}
	preparation := make(chan prepared, 1)
	go func() { runners, err := s.prepare(startup); preparation <- prepared{runners, err} }()
	var err error
	select {
	case result := <-preparation:
		err = result.err
		if err == nil {
			s.mu.Lock()
			s.runners = result.runners
			s.mu.Unlock()
			err = s.start(startup)
		}
	case <-startup.Done():
		err = startup.Err()
	}
	cancel()
	if err == nil {
		s.mu.Lock()
		s.started = true
		s.ready = true
		s.mu.Unlock()
		s.logger.Info("service ready", "flows", len(s.runners))
		failures := make(chan error, len(s.runners))
		watchDone := make(chan struct{})
		for _, r := range s.runners {
			go func() {
				select {
				case <-r.SourceFailed():
					failures <- fmt.Errorf("flow %q source failed", r.ID())
				case <-r.SourceDone():
					if r.SourceError() != nil {
						failures <- fmt.Errorf("flow %q source failed", r.ID())
					} else {
						s.logger.Info("source completed", "flow_id", r.ID())
					}
				case <-watchDone:
				}
			}()
		}
		select {
		case <-ctx.Done():
		case <-runtime.Failed():
			err = runtime.Err()
		case err = <-failures:
		case <-monitorFailure:
			err = errors.New("monitor listener failed")
		}
		close(watchDone)
	} else if ctx.Err() != nil {
		err = nil
	}
	select {
	case <-monitorFailure:
		err = errors.New("monitor listener failed")
	default:
	}
	s.mu.Lock()
	s.ready = false
	s.stopping = true
	started := s.started
	s.mu.Unlock()
	s.logger.Info("service draining")
	shutdown, stop := context.WithTimeout(context.Background(), duration(s.options.ShutdownTimeout))
	defer stop()
	drainErr := s.drain(shutdown, started)
	if monitor != nil {
		if closeErr := monitor.Shutdown(shutdown); closeErr != nil {
			monitor.Close()
			drainErr = errors.Join(drainErr, closeErr)
		}
	}
	if drainErr != nil {
		s.logger.Error("shutdown deadline or source cleanup failure")
	}
	s.logger.Info("service stopped")
	return errors.Join(err, drainErr)
}

func (s *Service) prepare(ctx context.Context) ([]*engine.Runner, error) {
	docs, err := Resolve(ctx, s.options)
	if err != nil {
		return nil, err
	}
	var runners []*engine.Runner
	for _, doc := range docs {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		id := doc.Info.ID
		load := api.LoadBytes
		if s.runtime != nil {
			load = s.runtime.LoadBytes
		}
		flow, err := load(doc.Data, func(res *api.Result, err error) {
			attrs := []any{"flow_id", id}
			if res != nil {
				attrs = append(attrs, "duration_ms", res.Duration.Milliseconds(), "trace_id", res.Message[message.TraceID], "correlation_id", res.Message[message.CorrelationID])
			}
			if err != nil {
				var stepError *engine.StepError
				if errors.As(err, &stepError) {
					attrs = append(attrs, "step_id", stepError.Step, "trace_id", stepError.Message[message.TraceID])
				}
				s.logger.Error("message failed", attrs...)
				return
			}
			if res.Err != nil {
				s.logger.Warn("message error handled", attrs...)
			}
		})
		if err != nil {
			return nil, fmt.Errorf("%s: processor initialization failed: %w", doc.Name, err)
		}
		flow.SetLogger(slog.NewLogLogger(s.logger.With("flow_id", id).Handler(), slog.LevelInfo))
		runners = append(runners, flow.Runner)
	}
	consumers := map[string]bool{}
	for _, r := range runners {
		input, _ := r.LocalChannels()
		if input != "" {
			consumers[input] = true
		}
	}
	for _, r := range runners {
		_, targets := r.LocalChannels()
		for _, target := range targets {
			if !consumers[target] {
				// A durable queue can intentionally retain work for a later run,
				// including a dead-letter queue without a live consumer.
				if s.runtime != nil && strings.HasPrefix(target, "queue:") && s.runtime.DurableQueue(strings.TrimPrefix(target, "queue:")) {
					continue
				}
				return nil, fmt.Errorf("flow %q requires a local consumer for %q", r.ID(), target)
			}
		}
	}
	return runners, ctx.Err()
}

func (s *Service) start(ctx context.Context) error {
	runners := s.runners
	activate := make(chan struct{})
	ctx = stepdef.WithActivation(stepdef.WithWorkTracker(ctx, &s.work), activate)
	// Consumers initialize first. The admission barrier additionally prevents
	// processing until every source has initialized, including HTTP listeners.
	for _, internal := range []bool{true, false} {
		for _, r := range runners {
			input, _ := r.LocalChannels()
			if (input != "") != internal {
				continue
			}
			if err := r.StartContext(ctx); err != nil {
				return fmt.Errorf("flow %q startup failed: %w", r.ID(), err)
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, r := range runners {
		if r.SourceError() != nil {
			return fmt.Errorf("flow %q source failed during startup", r.ID())
		}
	}
	close(activate)
	return nil
}

func (s *Service) drain(ctx context.Context, started bool) error {
	if !started {
		for _, r := range s.runners {
			r.Cancel()
		}
	} else {
		for _, r := range s.runners {
			input, _ := r.LocalChannels()
			if input == "" {
				r.StopSource()
			}
		}
		for _, r := range s.runners {
			input, _ := r.LocalChannels()
			if input != "" {
				continue
			}
			select {
			case <-r.SourceDone():
			case <-ctx.Done():
				s.cancelAll()
				return ctx.Err()
			}
		}
		if err := s.work.Wait(ctx); err != nil {
			s.cancelAll()
			return err
		}
	}
	results := make(chan error, len(s.runners))
	for _, r := range s.runners {
		go func() { results <- r.StopContext(ctx) }()
	}
	var errs []error
	for range s.runners {
		select {
		case err := <-results:
			if err != nil {
				errs = append(errs, err)
			}
		case <-ctx.Done():
			s.cancelAll()
			return ctx.Err()
		}
	}
	return errors.Join(errs...)
}

func (s *Service) cancelAll() {
	for _, r := range s.runners {
		r.Cancel()
	}
}
