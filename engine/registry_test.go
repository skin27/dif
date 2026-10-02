package engine

import (
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"dif/message"
)

// newFlow returns a runner for flow id without a source; messages arrive through Send.
func newFlow(id string, results chan result) *Runner {
	f := flow(nil)
	f.ID = id
	return NewRunner(f, func(res *Result, err error) {
		if results != nil {
			results <- result{res, err}
		}
	})
}

// newEngine registers a flow for every id. All flows report to results, which may be nil.
func newEngine(t *testing.T, results chan result, ids ...string) *Engine {
	t.Helper()
	e := New()
	for _, id := range ids {
		must(t, e.Add(newFlow(id, results)))
	}
	t.Cleanup(func() { e.Shutdown() })
	return e
}

func wantFlow(t *testing.T, e *Engine, id string, want State) {
	t.Helper()
	r, err := e.GetFlow(id)
	must(t, err)
	wantState(t, r, want)
}

// sendTo sends a message to flow id and waits until it has been processed.
func sendTo(t *testing.T, e *Engine, results chan result, id, body string) {
	t.Helper()
	r, err := e.GetFlow(id)
	must(t, err)
	must(t, r.Send(message.New(body)))
	if got := next(t, results); got.err != nil || got.res.Message[message.Body] != body {
		t.Fatalf("result = %+v, want body %s", got, body)
	}
}

func TestMultipleFlows(t *testing.T) {
	results := make(chan result, 10)
	e := newEngine(t, results, "flow1", "flow2")

	must(t, e.StartFlow("flow1"))
	must(t, e.StartFlow("flow2"))
	wantFlow(t, e, "flow1", Started)
	wantFlow(t, e, "flow2", Started)
	sendTo(t, e, results, "flow1", "a")
	sendTo(t, e, results, "flow2", "b")
}

func TestIndependentLifecycle(t *testing.T) {
	results := make(chan result, 10)
	e := newEngine(t, results, "flow1", "flow2")
	must(t, e.StartFlow("flow1"))
	must(t, e.StartFlow("flow2"))

	must(t, e.PauseFlow("flow1"))
	wantFlow(t, e, "flow1", Paused)
	wantFlow(t, e, "flow2", Started)
	sendTo(t, e, results, "flow2", "still running")

	must(t, e.StopFlow("flow2"))
	wantFlow(t, e, "flow1", Paused)
	wantFlow(t, e, "flow2", Stopped)
}

func TestRestartPausedFlow(t *testing.T) {
	src := make(chanSource)
	results := make(chan result, 10)
	f := flow(src)
	f.ID = "flow1"
	r := NewRunner(f, func(res *Result, err error) {
		results <- result{res, err}
	})
	e := New()
	must(t, e.Add(r))
	defer e.Shutdown()

	must(t, e.StartFlow("flow1"))
	run := r.done // identifies the run, and so its goroutine
	must(t, e.PauseFlow("flow1"))
	src <- message.New("held") // waits at the pause gate

	must(t, e.StartFlow("flow1"))
	wantState(t, r, Started)
	if r.done != run {
		t.Fatal("start after pause began a new run; want the paused run to continue")
	}
	if got := next(t, results); got.res.Message[message.Body] != "held" {
		t.Fatalf("result = %+v, want body held", got)
	}
}

func TestDuplicateStart(t *testing.T) {
	e := newEngine(t, nil, "flow1")
	r, _ := e.GetFlow("flow1")

	// A run (and its goroutine) begins only when Start returns nil.
	var wg sync.WaitGroup
	var mu sync.Mutex
	started := 0
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if e.StartFlow("flow1") == nil {
				mu.Lock()
				started++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if started != 1 {
		t.Fatalf("%d starts succeeded, want 1", started)
	}
	run := r.done
	if err := e.StartFlow("flow1"); err == nil || !strings.Contains(err.Error(), "flow is started") {
		t.Fatalf("start of a started flow: err = %v, want 'flow is started'", err)
	}
	if r.done != run {
		t.Fatal("second start began a new run")
	}
}

func TestListFlows(t *testing.T) {
	e := newEngine(t, nil, "flow3", "flow1", "flow2", "flow4")
	before := time.Now()
	must(t, e.StartFlow("flow1"))
	must(t, e.StartFlow("flow2"))
	must(t, e.StartFlow("flow3"))
	must(t, e.PauseFlow("flow2"))

	tests := []struct {
		state State
		want  []string // "id state"
	}{
		{"", []string{"flow1 started", "flow2 paused", "flow3 started", "flow4 stopped"}},
		{Started, []string{"flow1 started", "flow3 started"}},
		{Paused, []string{"flow2 paused"}},
		{Stopped, []string{"flow4 stopped"}},
	}
	for _, tt := range tests {
		var got []string
		for _, f := range e.ListFlows(tt.state) {
			got = append(got, f.ID+" "+string(f.State))
			// A run's start time is set while the flow runs (also when paused).
			if running := f.State != Stopped; running != !f.Since.IsZero() || running && f.Since.Before(before) {
				t.Errorf("flow %s %s: start time %v", f.ID, f.State, f.Since)
			}
		}
		if !reflect.DeepEqual(got, tt.want) {
			t.Errorf("ListFlows(%q) = %v, want %v", tt.state, got, tt.want)
		}
	}
}

func TestStartTime(t *testing.T) {
	e := newEngine(t, nil, "flow1")
	r, _ := e.GetFlow("flow1")
	must(t, e.StartFlow("flow1"))
	first := r.Status().Since

	must(t, e.PauseFlow("flow1"))
	must(t, e.StartFlow("flow1"))
	if got := r.Status().Since; !got.Equal(first) {
		t.Errorf("start after pause: start time = %v, want %v (same run)", got, first)
	}

	must(t, e.StopFlow("flow1"))
	if got := r.Status().Since; !got.IsZero() {
		t.Errorf("stopped flow: start time = %v, want zero", got)
	}
	must(t, e.StartFlow("flow1"))
	// Stop reset it, so the new run set it again (the clock may not have advanced).
	if got := r.Status().Since; got.IsZero() || got.Before(first) {
		t.Errorf("restart after stop: start time = %v, want set, not before %v", got, first)
	}
}

func TestUnknownAndDuplicateFlow(t *testing.T) {
	e := newEngine(t, nil, "flow1")
	for name, op := range map[string]func(string) error{
		"start": e.StartFlow, "pause": e.PauseFlow, "resume": e.ResumeFlow, "stop": e.StopFlow,
	} {
		if err := op("nope"); err == nil || err.Error() != "flow nope not found" {
			t.Errorf("%s nope: err = %v, want flow nope not found", name, err)
		}
	}
	if e.Add(newFlow("flow1", nil)) == nil {
		t.Error("add of a duplicate id: want error")
	}
	if e.Add(newFlow("", nil)) == nil {
		t.Error("add without id: want error")
	}
}

func TestShutdown(t *testing.T) {
	e := New() // no Cleanup: Shutdown is what is tested
	for _, id := range []string{"flow1", "flow2", "flow3"} {
		must(t, e.Add(newFlow(id, nil)))
	}
	must(t, e.StartFlow("flow1"))
	must(t, e.StartFlow("flow2"))
	must(t, e.PauseFlow("flow2"))

	must(t, e.Shutdown())
	if got := e.ListFlows(Stopped); len(got) != 3 {
		t.Fatalf("stopped flows after shutdown = %v, want all 3", got)
	}
}

// TestConcurrentAccess exercises the engine from many goroutines at once.
// Its value is in running it with -race.
func TestConcurrentAccess(t *testing.T) {
	ids := []string{"flow1", "flow2", "flow3"}
	e := New()
	for _, id := range ids {
		must(t, e.Add(newFlow(id, nil)))
	}

	var wg sync.WaitGroup
	for w := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range 200 {
				id := ids[(w+i)%len(ids)]
				// Errors are expected: the operations race on purpose.
				switch i % 6 {
				case 0:
					e.StartFlow(id)
				case 1:
					e.PauseFlow(id)
				case 2:
					e.ResumeFlow(id)
				case 3:
					if r, err := e.GetFlow(id); err == nil {
						r.Send(r.NewMessage())
					}
				case 4:
					e.ListFlows(Started)
				case 5:
					if i%60 == 5 {
						e.StopFlow(id)
					}
				}
			}
		}()
	}
	wg.Add(1)
	go func() { // registering while flows run
		defer wg.Done()
		for i := range 20 {
			e.Add(newFlow(fmt.Sprintf("extra%d", i), nil))
		}
	}()
	wg.Wait()

	must(t, e.Shutdown())
	if got := e.ListFlows(""); len(got) != len(ids)+20 {
		t.Fatalf("got %d flows, want %d", len(got), len(ids)+20)
	}
	if got := e.ListFlows(Stopped); len(got) != len(ids)+20 {
		t.Fatalf("not all flows stopped after shutdown: %v", e.ListFlows(""))
	}
}
