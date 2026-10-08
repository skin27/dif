package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"dif/api"
	"dif/message"
	stepdef "dif/steps/definition"
)

func writeFlow(t *testing.T, dir, file, id, source string, sourceOptions map[string]any, actions []map[string]any) string {
	t.Helper()
	nodes := []map[string]any{{"id": "source", "type": "source", "uri": source, "options": sourceOptions}}
	nodes = append(nodes, actions...)
	nodes = append(nodes, map[string]any{"id": "sink", "type": "sink", "uri": "passthrough"})
	for i, n := range nodes {
		var links []map[string]any
		if i > 0 {
			links = append(links, map[string]any{"id": fmt.Sprint(i), "bound": "in"})
		}
		if i < len(nodes)-1 {
			links = append(links, map[string]any{"id": fmt.Sprint(i + 1), "bound": "out"})
		}
		n["links"] = map[string]any{"link": links}
	}
	doc := map[string]any{"dil": map[string]any{"integrations": map[string]any{"integration": map[string]any{"flows": map[string]any{"flow": map[string]any{"id": id, "steps": map[string]any{"step": nodes}}}}}}}
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, file)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

var processorID atomic.Int64

func register(t *testing.T, kind string, p stepdef.Processor) string {
	t.Helper()
	name := fmt.Sprintf("servicetest%d", processorID.Add(1))
	err := api.RegisterStep(stepdef.Definition{Name: name, Kind: kind, Schema: []byte(`{"type":"object","properties":{},"additionalProperties":false}`), New: func(string, stepdef.Params) (stepdef.Processor, error) { return p, nil }})
	if err != nil {
		t.Fatal(err)
	}
	return name
}

type burstSource int

func (s burstSource) Run(ctx context.Context, emit stepdef.Emit) error {
	return s.RunReady(ctx, emit, func() {})
}
func (s burstSource) RunReady(ctx context.Context, emit stepdef.Emit, ready func()) error {
	ready()
	for i := 0; i < int(s); i++ {
		if err := emit(message.New(i), nil); err != nil {
			return nil
		}
	}
	return nil
}

type controlledSource struct {
	fail         <-chan struct{}
	startupError bool
}

func (s controlledSource) Run(ctx context.Context, emit stepdef.Emit) error {
	return s.RunReady(ctx, emit, func() {})
}
func (s controlledSource) RunReady(ctx context.Context, _ stepdef.Emit, ready func()) error {
	if s.startupError {
		return errors.New("initialization failed")
	}
	ready()
	select {
	case <-s.fail:
		return errors.New("runtime failure")
	case <-ctx.Done():
		return nil
	}
}

type countAction struct {
	count   *atomic.Int64
	entered chan struct{}
	release <-chan struct{}
}

func (a countAction) Process(ctx context.Context, m message.Message) (message.Message, error) {
	if a.entered != nil {
		select {
		case a.entered <- struct{}{}:
		default:
		}
	}
	if a.release != nil {
		select {
		case <-a.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	a.count.Add(1)
	return m, nil
}

func action(uri string, opts map[string]any) map[string]any {
	return map[string]any{"id": "action", "type": "action", "uri": uri, "options": opts}
}

func await(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for !condition() {
		select {
		case <-ticker.C:
		case <-deadline:
			t.Fatal("condition timed out")
		}
	}
}

func startService(t *testing.T, s *Service) (context.CancelFunc, <-chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()
	t.Cleanup(cancel)
	return cancel, done
}

func finish(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(4 * time.Second):
		t.Fatal("service did not exit")
		return nil
	}
}

func TestConsumersReadyBeforeProducerAndDrainQueuedWork(t *testing.T) {
	for _, channel := range []string{"queue", "topic"} {
		t.Run(channel, func(t *testing.T) {
			dir := t.TempDir()
			name := fmt.Sprintf("%s-%d", channel, processorID.Add(1))
			var count atomic.Int64
			entered := make(chan struct{}, 1)
			release := make(chan struct{})
			counter := register(t, stepdef.Action, countAction{&count, entered, release})
			source := register(t, stepdef.Source, burstSource(20))
			// The producer sorts before the consumer, deliberately.
			writeFlow(t, dir, "a.json", "producer", source, nil, []map[string]any{action(channel+":"+name, map[string]any{"delivery": "enqueue"})})
			if channel == "topic" {
				writeFlow(t, dir, "a.json", "producer", source, nil, []map[string]any{action(channel+":"+name, nil)})
			}
			writeFlow(t, dir, "z.json", "consumer", channel+":"+name, nil, []map[string]any{action(counter, nil)})
			s := New(options(t, "--dir", dir, "--shutdown-timeout=2s"), io.Discard)
			cancel, done := startService(t, s)
			await(t, func() bool { ready, _, _, _ := s.snapshot(); return ready })
			<-entered
			await(t, func() bool {
				_, _, _, flows := s.snapshot()
				for _, f := range flows {
					if f.ID == "producer" {
						return f.Completed == 20
					}
				}
				return false
			})
			cancel()
			await(t, func() bool { ready, _, stopping, _ := s.snapshot(); return !ready && stopping })
			select {
			case err := <-done:
				t.Fatalf("exited before consumer drained: %v", err)
			default:
			}
			close(release)
			if err := finish(t, done); err != nil {
				t.Fatal(err)
			}
			if count.Load() != 20 {
				t.Fatalf("processed %d messages", count.Load())
			}
			a, q := s.work.Counts()
			if a != 0 || q != 0 {
				t.Fatalf("work leaked: %d %d", a, q)
			}
		})
	}
}

func TestDrainPreservesSynchronousFlowlinkRequest(t *testing.T) {
	dir := t.TempDir()
	var count atomic.Int64
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	counter := register(t, stepdef.Action, countAction{&count, entered, release})
	source := register(t, stepdef.Source, burstSource(1))
	id := fmt.Sprintf("target%d", processorID.Add(1))
	writeFlow(t, dir, "a.json", "producer", source, nil, []map[string]any{action("flowlink", map[string]any{"targetFlowId": id, "transport": "sync", "exchangePattern": "InOut"})})
	writeFlow(t, dir, "b.json", id, "flowlink", map[string]any{"flowId": id}, []map[string]any{action(counter, nil)})
	s := New(options(t, "--dir", dir, "--shutdown-timeout=2s"), io.Discard)
	cancel, done := startService(t, s)
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("request did not arrive")
	}
	cancel()
	close(release)
	if err := finish(t, done); err != nil {
		t.Fatal(err)
	}
	_, _, _, flows := s.snapshot()
	for _, f := range flows {
		if f.Failed != 0 || f.Completed != 1 {
			t.Fatal(f)
		}
	}
}

func TestSourceFailureAndPartialStartupCleanup(t *testing.T) {
	for _, startup := range []bool{true, false} {
		t.Run(fmt.Sprint(startup), func(t *testing.T) {
			dir := t.TempDir()
			failure := make(chan struct{})
			source := register(t, stepdef.Source, controlledSource{failure, startup})
			writeFlow(t, dir, "a.json", "first", "topic:cleanup"+fmt.Sprint(processorID.Add(1)), nil, nil)
			writeFlow(t, dir, "b.json", "failure", source, nil, nil)
			s := New(options(t, "--dir", dir, "--shutdown-timeout=1s"), io.Discard)
			_, done := startService(t, s)
			if !startup {
				await(t, func() bool { ready, _, _, _ := s.snapshot(); return ready })
				close(failure)
			}
			if err := finish(t, done); err == nil {
				t.Fatal("source failure reported success")
			}
			ready, _, _, flows := s.snapshot()
			if ready {
				t.Fatal("failed service is ready")
			}
			for _, f := range flows {
				if f.State != api.Stopped {
					t.Fatal(f)
				}
			}
		})
	}
}

func TestShutdownDeadlineCancelsActiveWork(t *testing.T) {
	dir := t.TempDir()
	var count atomic.Int64
	entered := make(chan struct{}, 1)
	counter := register(t, stepdef.Action, countAction{&count, entered, make(chan struct{})})
	source := register(t, stepdef.Source, burstSource(1))
	writeFlow(t, dir, "a.json", "timeout", source, nil, []map[string]any{action(counter, nil)})
	s := New(options(t, "--dir", dir, "--shutdown-timeout=30ms"), io.Discard)
	cancel, done := startService(t, s)
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("message did not start")
	}
	cancel()
	if err := finish(t, done); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("shutdown: %v", err)
	}
}

func TestMonitoringAndMissingConsumer(t *testing.T) {
	dir := t.TempDir()
	writeFlow(t, dir, "a.json", "missing", "message:manual", nil, []map[string]any{action("queue:absent-consumer", nil)})
	s := New(options(t, "--dir", dir), io.Discard)
	for path, want := range map[string]int{"/livez": 200, "/readyz": 503, "/startupz": 503, "/status": 200, "/metrics": 200} {
		r := httptest.NewRecorder()
		s.Handler().ServeHTTP(r, httptest.NewRequest(http.MethodGet, path, nil))
		if r.Code != want {
			t.Fatalf("%s: %d", path, r.Code)
		}
	}
	r := httptest.NewRecorder()
	s.Handler().ServeHTTP(r, httptest.NewRequest(http.MethodPost, "/status", nil))
	if r.Code != 405 {
		t.Fatal(r.Code)
	}
	if err := s.Run(context.Background()); err == nil || !strings.Contains(err.Error(), "local consumer") {
		t.Fatalf("missing consumer: %v", err)
	}
}

func TestStartupDeadlineIncludesProcessorConstruction(t *testing.T) {
	dir := t.TempDir()
	entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	name := fmt.Sprintf("slowconstructor%d", processorID.Add(1))
	if err := api.RegisterStep(stepdef.Definition{Name: name, Kind: stepdef.Source, Schema: []byte(`{"type":"object"}`), New: func(string, stepdef.Params) (stepdef.Processor, error) {
		close(entered)
		<-release
		close(finished)
		return burstSource(0), nil
	}}); err != nil {
		t.Fatal(err)
	}
	writeFlow(t, dir, "slow.json", "slow", name, nil, nil)
	s := New(options(t, "--dir", dir, "--startup-timeout=100ms", "--shutdown-timeout=100ms"), io.Discard)
	_, done := startService(t, s)
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("constructor did not start")
	}
	if err := finish(t, done); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("startup: %v", err)
	}
	close(release)
	<-finished
	_, started, _, flows := s.snapshot()
	if started || len(flows) != 0 {
		t.Fatal("partial preparation published")
	}
}

type failingCleanupSource struct{ fail <-chan struct{} }

func (s failingCleanupSource) Run(ctx context.Context, emit stepdef.Emit) error {
	return s.RunReady(ctx, emit, func() {})
}
func (s failingCleanupSource) RunReady(ctx context.Context, _ stepdef.Emit, ready func()) error {
	ready()
	select {
	case <-s.fail:
	case <-ctx.Done():
		return nil
	}
	err := errors.New("listener failed")
	stepdef.ReportSourceFailure(ctx, err)
	<-ctx.Done() // model cleanup that needs the supervisor to initiate shutdown
	return err
}

func TestFatalListenerReportedBeforeCleanup(t *testing.T) {
	dir := t.TempDir()
	failure := make(chan struct{})
	source := register(t, stepdef.Source, failingCleanupSource{failure})
	writeFlow(t, dir, "a.json", "failure", source, nil, nil)
	s := New(options(t, "--dir", dir, "--shutdown-timeout=1s"), io.Discard)
	_, done := startService(t, s)
	await(t, func() bool { ready, _, _, _ := s.snapshot(); return ready })
	close(failure)
	if err := finish(t, done); err == nil {
		t.Fatal("listener failure reported success")
	}
}

func TestFiniteSourceRemainsReadyAndMonitoringIsSanitized(t *testing.T) {
	dir := t.TempDir()
	source := register(t, stepdef.Source, burstSource(2))
	writeFlow(t, dir, "finite.json", "finite", source, nil, nil)
	s := New(options(t, "--dir", dir), io.Discard)
	cancel, done := startService(t, s)
	await(t, func() bool {
		ready, _, _, flows := s.snapshot()
		return ready && len(flows) == 1 && flows[0].Completed == 2 && flows[0].Source == "completed"
	})
	for _, path := range []string{"/readyz", "/startupz", "/status", "/metrics"} {
		response := httptest.NewRecorder()
		s.Handler().ServeHTTP(response, httptest.NewRequest("GET", path, nil))
		if response.Code != 200 {
			t.Fatalf("%s: %d", path, response.Code)
		}
		if strings.Contains(response.Body.String(), "options") || strings.Contains(response.Body.String(), "body") {
			t.Fatalf("unsafe status: %s", response.Body.String())
		}
		if path == "/metrics" && !strings.Contains(response.Body.String(), `dif_messages_completed_total{flow="finite"} 2`) {
			t.Fatal(response.Body.String())
		}
	}
	cancel()
	if err := finish(t, done); err != nil {
		t.Fatal(err)
	}
}
