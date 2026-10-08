package service

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"dif/api"
	stepdef "dif/steps/definition"
)

func TestReliableExamplePersistsDuplicateSuppression(t *testing.T) {
	root := filepath.Join("..", "..")
	o, err := ParseOptions([]string{"--config", filepath.Join(root, "examples", "reliable", "service.json")}, func(string) string { return "" }, false)
	if err != nil {
		t.Fatal(err)
	}
	for i, path := range o.Files {
		o.Files[i] = filepath.Join(root, path)
	}
	o.Channels.Directory = t.TempDir()
	for run := 0; run < 2; run++ {
		var logs bytes.Buffer
		s := New(o, &logs)
		cancel, done := startService(t, s)
		await(t, func() bool {
			_, _, _, flows := s.snapshot()
			for _, f := range flows {
				if f.ID == "reliable-consumer" {
					return f.Completed == 5
				}
			}
			return false
		})
		cancel()
		if err := finish(t, done); err != nil {
			t.Fatal(err)
		}
		want := 5
		if run == 1 {
			want = 0
		}
		if got := strings.Count(logs.String(), "body="); got != want {
			t.Fatalf("run %d: %d logged orders, want %d; %s", run, got, want, logs.String())
		}
	}
}

func TestDurableServiceBacklogWithoutConsumerAndRecovery(t *testing.T) {
	dir := t.TempDir()
	storage := t.TempDir()
	config := api.ChannelConfig{Directory: storage, Queues: map[string]api.QueueConfig{"jobs": {Durable: true, Capacity: 10}}}
	source := register(t, stepdef.Source, burstSource(3))
	producer := writeFlow(t, dir, "producer.json", "producer", source, nil, []map[string]any{action("queue:jobs", map[string]any{"delivery": "enqueue", "overflow": "block", "enqueueTimeout": 1000})})
	o := options(t, "--file", producer, "--shutdown-timeout=1s")
	o.Channels = &config
	s := New(o, io.Discard)
	cancel, done := startService(t, s)
	await(t, func() bool { _, _, _, flows := s.snapshot(); return len(flows) == 1 && flows[0].Completed == 3 })
	status := httptest.NewRecorder()
	s.Handler().ServeHTTP(status, httptest.NewRequest("GET", "/status", nil))
	if !strings.Contains(status.Body.String(), `"channels"`) || !strings.Contains(status.Body.String(), `"durable":true`) {
		t.Fatal(status.Body.String())
	}
	cancel()
	if err := finish(t, done); err != nil {
		t.Fatal("durable backlog blocked graceful drain", err)
	}
	var count atomic.Int64
	counter := register(t, stepdef.Action, countAction{count: &count})
	consumer := writeFlow(t, dir, "consumer.json", "consumer", "queue:jobs", nil, []map[string]any{action(counter, nil)})
	o = options(t, "--file", consumer, "--shutdown-timeout=1s")
	o.Channels = &config
	s = New(o, io.Discard)
	cancel, done = startService(t, s)
	await(t, func() bool { return count.Load() == 3 })
	cancel()
	if err := finish(t, done); err != nil {
		t.Fatal(err)
	}
	r, err := api.NewRuntime(config)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	for _, q := range r.ChannelStatus().Queues {
		if q.Name == "jobs" && q.Waiting+q.InFlight != 0 {
			t.Fatal(q)
		}
	}
}

func TestDurableServiceForcedStopRecovers(t *testing.T) {
	dir := t.TempDir()
	config := api.ChannelConfig{Directory: t.TempDir(), Queues: map[string]api.QueueConfig{"jobs": {Durable: true}}}
	entered := make(chan struct{}, 1)
	never := make(chan struct{})
	var count atomic.Int64
	blocked := register(t, stepdef.Action, countAction{count: &count, entered: entered, release: never})
	source := register(t, stepdef.Source, burstSource(1))
	writeFlow(t, dir, "a.json", "producer", source, nil, []map[string]any{action("queue:jobs", map[string]any{"delivery": "enqueue"})})
	writeFlow(t, dir, "b.json", "consumer", "queue:jobs", nil, []map[string]any{action(blocked, nil)})
	o := options(t, "--dir", dir, "--shutdown-timeout=40ms")
	o.Channels = &config
	s := New(o, io.Discard)
	cancel, done := startService(t, s)
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("work not started")
	}
	cancel()
	if err := finish(t, done); err == nil {
		t.Fatal("expected forced shutdown")
	}
	// The service may return before cancelled processors finish; the closed
	// runtime rejects their late settlement, preserving the journal entry.
	r, err := api.NewRuntime(config)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	found := false
	for _, q := range r.ChannelStatus().Queues {
		if q.Name == "jobs" {
			found = q.Waiting == 1
		}
	}
	if !found {
		t.Fatal(r.ChannelStatus())
	}
}

func TestChannelConfigurationValidationIsReadOnly(t *testing.T) {
	dir := t.TempDir()
	store := filepath.Join(dir, "must-not-be-created")
	config := map[string]any{"files": []string{"flow.json"}, "channels": map[string]any{"directory": store, "queues": map[string]any{"jobs": map[string]any{"durable": true, "capacity": 2}}}}
	data, _ := json.Marshal(config)
	path := filepath.Join(dir, "service.json")
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseOptions([]string{"--config", path}, func(string) string { return "" }, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(store); !os.IsNotExist(err) {
		t.Fatal("validation created storage", err)
	}
	config["channels"] = map[string]any{"queues": map[string]any{"q": map[string]any{"capacity": -1}}}
	data, _ = json.Marshal(config)
	os.WriteFile(path, data, 0600)
	if _, err := ParseOptions([]string{"--config", path}, func(string) string { return "" }, true); err == nil {
		t.Fatal("invalid capacity accepted")
	}
}

func TestRecoveredWorkRegisteredBeforeServiceDrain(t *testing.T) {
	dir := t.TempDir()
	config := api.ChannelConfig{Directory: t.TempDir(), Queues: map[string]api.QueueConfig{"jobs": {Durable: true}}}
	r, err := api.NewRuntime(config)
	if err != nil {
		t.Fatal(err)
	}
	p := writeFlow(t, dir, "seed.json", "seed", "message", nil, []map[string]any{action("queue:jobs", map[string]any{"delivery": "enqueue"})})
	f, err := r.Load(p, nil)
	if err != nil {
		t.Fatal(err)
	}
	f.Start()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := f.Request(ctx, api.Message{api.Body: "recovered"}); err != nil {
		t.Fatal(err)
	}
	f.Stop()
	r.Close()
	var count atomic.Int64
	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	counter := register(t, stepdef.Action, countAction{count: &count, entered: entered, release: release})
	path := writeFlow(t, dir, "consumer.json", "consumer", "queue:jobs", nil, []map[string]any{action(counter, nil)})
	o := options(t, "--file", path, "--shutdown-timeout=1s")
	o.Channels = &config
	s := New(o, io.Discard)
	stop, done := startService(t, s)
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("not recovered")
	}
	stop()
	close(release)
	if err := finish(t, done); err != nil {
		t.Fatal(err)
	}
	if count.Load() != 1 {
		t.Fatal(count.Load())
	}
}
