package service

import (
	"context"
	"crypto/tls"
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
	"dif/internal/secret"
	"dif/keystore"
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
	stomp                    *api.STOMPServer
}

// STOMPPasswordEnv holds the passcode of the STOMP login; STOMPPasswordEnv+"_FILE"
// names a file with it.
const STOMPPasswordEnv = "DIF_STOMP_PASSWORD"

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
	defer func() { resultErr = errors.Join(resultErr, runtime.Close()) }()
	stompServer, stompErr := s.stompServer(runtime)
	if stompErr != nil {
		return stompErr
	}
	s.mu.Lock()
	s.runtime = runtime
	s.stomp = stompServer
	s.mu.Unlock()
	var stompDone <-chan struct{}
	if stompServer != nil {
		defer stompServer.Close()
		stompDone = stompServer.Done()
	}
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
	if err == nil && stompServer != nil {
		err = s.startSTOMP(stompServer)
	}
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
		case <-stompDone:
			err = fmt.Errorf("STOMP listener failed: %w", stompServer.Err())
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
	if stompServer != nil {
		// Clients go first: their unacknowledged deliveries return to the
		// channels before the flows drain them.
		stompServer.Close()
	}
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
	loaded, err := docs[0].Program.Load(ctx, s.runtime, func(id string, res *api.Result, err error) {
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
	}, nil)
	if err != nil {
		return nil, err
	}
	for _, flow := range loaded.Flows {
		flow.SetLogger(slog.NewLogLogger(s.logger.With("flow_id", flow.ID()).Handler(), slog.LevelInfo))
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
				// STOMP clients may subscribe to a served topic; with none,
				// publication delivers nothing, as for any topic.
				if s.runtime != nil && strings.HasPrefix(target, "topic:") && s.runtime.ServedTopic(strings.TrimPrefix(target, "topic:")) {
					continue
				}
				return nil, fmt.Errorf("flow %q requires a local consumer for %q", r.ID(), target)
			}
		}
	}
	return runners, ctx.Err()
}

// stompServer prepares the listener of the served channels, or returns nil
// when there is none: the passcode comes from DIF_STOMP_PASSWORD (or its
// _FILE companion) and a TLS identity from its keystore. It binds nothing.
func (s *Service) stompServer(runtime *api.Runtime) (*api.STOMPServer, error) {
	srv := runtime.NewSTOMPServer()
	if srv == nil {
		return nil, nil
	}
	passcode, _, err := secret.Env(STOMPPasswordEnv)
	if err != nil {
		return nil, fmt.Errorf("stomp: %w", err)
	}
	if passcode == "" {
		return nil, fmt.Errorf("stomp: set %s or %s_FILE to the passcode of the STOMP login", STOMPPasswordEnv, STOMPPasswordEnv)
	}
	srv.Passcode = passcode
	if t := srv.Config.TLS; t != nil {
		password, given, err := secret.Env("DIF_SERVER_IDENTITY_PASSWORD")
		if err != nil {
			return nil, fmt.Errorf("stomp: %w", err)
		}
		cert, err := keystore.LoadIdentity(t.Identity, password)
		if err != nil {
			if !given && errors.Is(err, keystore.ErrWrongPassword) {
				return nil, fmt.Errorf("stomp: server identity: %w; set DIF_SERVER_IDENTITY_PASSWORD", err)
			}
			return nil, fmt.Errorf("stomp: server identity: %w", err)
		}
		srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	}
	srv.Logger = s.logger
	return srv, nil
}

// startSTOMP binds the listener once the flows run. A message a client sends
// counts as queued work when a flow consumes its channel; a memory queue that
// only clients consume holds nothing the drain could finish.
func (s *Service) startSTOMP(srv *api.STOMPServer) error {
	inputs := map[string]bool{}
	for _, r := range s.runners {
		if input, _ := r.LocalChannels(); input != "" {
			inputs[input] = true
		}
	}
	tracked := stepdef.WithWorkTracker(context.Background(), &s.work)
	srv.Track = func(channel string) func() {
		if strings.HasPrefix(channel, "queue:") && !inputs[channel] {
			return func() {}
		}
		return stepdef.TrackWork(tracked, true)
	}
	if err := srv.Start(); err != nil {
		return fmt.Errorf("STOMP listener: %w", err)
	}
	s.logger.Info("STOMP listener started", "address", srv.Addr().String())
	return nil
}

func (s *Service) start(ctx context.Context) error {
	return engine.StartGroup(stepdef.WithWorkTracker(ctx, &s.work), s.runners)
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
