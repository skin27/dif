package engine

import (
	"context"
	"errors"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	flowdef "dif/flows/definition"
	"dif/message"
	stepdef "dif/steps/definition"
)

type traceActionFn func(context.Context, message.Message) (message.Message, error)

func (f traceActionFn) Process(ctx context.Context, m message.Message) (message.Message, error) {
	return f(ctx, m)
}

type traceSinkFn func(context.Context, message.Message) error

func (f traceSinkFn) Consume(ctx context.Context, m message.Message) error {
	return f(ctx, m)
}

type traceParallelRouter struct {
	branches int
}

func (r traceParallelRouter) Route(_ context.Context, m message.Message) ([]stepdef.Route, error) {
	routes := make([]stepdef.Route, r.branches)
	for i := 0; i < r.branches; i++ {
		routes[i] = stepdef.Route{Next: i, Message: m.Copy()}
	}
	return routes, nil
}

func (r traceParallelRouter) ParallelPolicy() stepdef.ParallelPolicy {
	return stepdef.ParallelPolicy{Parallel: true, MaxConcurrency: r.branches}
}

func TestTraceStepDiffs(t *testing.T) {
	sink := &flowdef.Node{
		ID:   "done",
		Kind: flowdef.Sink,
		URI:  "passthrough",
		Processor: traceSinkFn(func(_ context.Context, m message.Message) error {
			return nil
		}),
	}
	step2 := &flowdef.Node{
		ID:   "mutate",
		Kind: flowdef.Action,
		URI:  "custom",
		Processor: traceActionFn(func(_ context.Context, m message.Message) (message.Message, error) {
			m[message.Body] = "AFTER"
			m["a"] = "changed"
			delete(m, "removeMe")
			m["added"] = 42
			m[message.MetadataPrefix+"internal"] = "hidden"
			return m, nil
		}),
		Next: []*flowdef.Node{sink},
	}
	step1 := &flowdef.Node{
		ID:   "prep",
		Kind: flowdef.Action,
		URI:  "custom",
		Processor: traceActionFn(func(_ context.Context, m message.Message) (message.Message, error) {
			m["a"] = "initial"
			m["removeMe"] = true
			return m, nil
		}),
		Next: []*flowdef.Node{step2},
	}
	src := &flowdef.Node{ID: "src", Kind: flowdef.Source, URI: "message", Next: []*flowdef.Node{step1}}
	f := &flowdef.Flow{ID: "trace-demo", Source: src}

	// Verify TraceFrom(ctx) is nil when not attached and Run still works cleanly.
	if TraceFrom(context.Background()) != nil {
		t.Fatal("expected nil trace on background context")
	}
	res0, err := Run(context.Background(), f, message.New("before"))
	if err != nil || res0.Message[message.Body] != "AFTER" {
		t.Fatalf("untraced run failed: res=%v err=%v", res0, err)
	}

	// Run with trace attached.
	tr := NewTrace()
	ctx := WithTrace(context.Background(), tr)
	if TraceFrom(ctx) != tr {
		t.Fatal("TraceFrom did not return attached trace")
	}
	res, err := Run(ctx, f, message.New("before"))
	if err != nil {
		t.Fatalf("traced run failed: %v", err)
	}
	if res.Message[message.Body] != "AFTER" {
		t.Fatalf("expected body AFTER, got %v", res.Message[message.Body])
	}

	steps := tr.Snapshot()
	if len(steps) != 3 {
		t.Fatalf("expected 3 traced steps, got %d", len(steps))
	}

	// Step 1: prep
	if steps[0].StepID != "prep" || steps[0].Kind != flowdef.Action || steps[0].URI != "custom" {
		t.Fatalf("unexpected step 0 metadata: %+v", steps[0])
	}
	if steps[0].BodyBefore != "before" || steps[0].BodyAfter != "before" {
		t.Fatalf("unexpected step 0 body diff: %v -> %v", steps[0].BodyBefore, steps[0].BodyAfter)
	}
	if !reflect.DeepEqual(steps[0].HeadersAdded, map[string]any{"a": "initial", "removeMe": true}) {
		t.Fatalf("unexpected step 0 HeadersAdded: %+v", steps[0].HeadersAdded)
	}

	// Step 2: mutate
	if steps[1].BodyBefore != "before" || steps[1].BodyAfter != "AFTER" {
		t.Fatalf("unexpected step 1 body diff: %v -> %v", steps[1].BodyBefore, steps[1].BodyAfter)
	}
	if !reflect.DeepEqual(steps[1].HeadersAdded, map[string]any{"added": 42}) {
		t.Fatalf("unexpected step 1 HeadersAdded (metadata must be excluded): %+v", steps[1].HeadersAdded)
	}
	if !reflect.DeepEqual(steps[1].HeadersChanged, map[string]HeaderDiff{"a": {Before: "initial", After: "changed"}}) {
		t.Fatalf("unexpected step 1 HeadersChanged: %+v", steps[1].HeadersChanged)
	}
	if !reflect.DeepEqual(steps[1].HeadersRemoved, []string{"removeMe"}) {
		t.Fatalf("unexpected step 1 HeadersRemoved: %+v", steps[1].HeadersRemoved)
	}

	// Step 3: done (sink)
	if steps[2].StepID != "done" || steps[2].Kind != flowdef.Sink || len(steps[2].HeadersAdded) != 0 || len(steps[2].HeadersChanged) != 0 || len(steps[2].HeadersRemoved) != 0 {
		t.Fatalf("unexpected step 2: %+v", steps[2])
	}
}

func TestTraceErrorRouteAndDetached(t *testing.T) {
	afterObs := &flowdef.Node{
		ID:   "obs-after",
		Kind: flowdef.Action,
		URI:  "log",
		Processor: traceActionFn(func(_ context.Context, m message.Message) (message.Message, error) {
			m["observed"] = true
			return m, nil
		}),
	}
	onErrObs := &flowdef.Node{
		ID:   "obs-err",
		Kind: flowdef.Action,
		URI:  "log",
		Processor: traceActionFn(func(_ context.Context, m message.Message) (message.Message, error) {
			return m, nil
		}),
	}
	errRoute := &flowdef.Node{
		ID:   "recover",
		Kind: flowdef.Action,
		URI:  "setbody",
		Processor: traceActionFn(func(_ context.Context, m message.Message) (message.Message, error) {
			m[message.Body] = "recovered"
			return m, nil
		}),
	}
	failStep := &flowdef.Node{
		ID:   "boom",
		Kind: flowdef.Action,
		URI:  "https",
		Processor: traceActionFn(func(_ context.Context, m message.Message) (message.Message, error) {
			return nil, errors.New("upstream down")
		}),
		OnError: []*flowdef.Node{onErrObs},
	}
	okStep := &flowdef.Node{
		ID:   "ok",
		Kind: flowdef.Action,
		URI:  "setheader",
		Processor: traceActionFn(func(_ context.Context, m message.Message) (message.Message, error) {
			m["stage"] = "1"
			return m, nil
		}),
		After: []*flowdef.Node{afterObs},
		Next:  []*flowdef.Node{failStep},
	}
	src := &flowdef.Node{ID: "src", Kind: flowdef.Source, URI: "message", Next: []*flowdef.Node{okStep}}
	f := &flowdef.Flow{
		ID:     "err-flow",
		Source: src,
		Error:  &flowdef.ErrorHandler{ID: "eh", Route: errRoute},
	}

	tr := NewTrace()
	res, err := Run(WithTrace(context.Background(), tr), f, message.New("start"))
	if err != nil {
		t.Fatalf("expected handled error, got %v", err)
	}
	if res.Err == nil || res.Message[message.Body] != "recovered" {
		t.Fatalf("unexpected result: res=%+v", res)
	}

	steps := tr.Snapshot()
	if len(steps) != 5 {
		t.Fatalf("expected 5 traced steps (ok, obs-after, boom, obs-err, recover), got %d: %+v", len(steps), steps)
	}
	if steps[0].StepID != "ok" || steps[0].Detached {
		t.Fatalf("step 0: %+v", steps[0])
	}
	if steps[1].StepID != "obs-after" || !steps[1].Detached {
		t.Fatalf("step 1 should be detached after observer: %+v", steps[1])
	}
	if steps[2].StepID != "boom" || steps[2].Error != "upstream down" || steps[2].Detached {
		t.Fatalf("step 2 should record boom error: %+v", steps[2])
	}
	if steps[3].StepID != "obs-err" || !steps[3].Detached {
		t.Fatalf("step 3 should be detached onError observer: %+v", steps[3])
	}
	if steps[4].StepID != "recover" || steps[4].BodyAfter != "recovered" || steps[4].Detached {
		t.Fatalf("step 4 should be error route step: %+v", steps[4])
	}
}

func TestTraceParallelRouter(t *testing.T) {
	const branches = 8
	var ran atomic.Int32
	nexts := make([]*flowdef.Node, branches)
	for i := 0; i < branches; i++ {
		idx := i
		nexts[i] = &flowdef.Node{
			ID:   "branch-" + string(rune('a'+idx)),
			Kind: flowdef.Action,
			URI:  "work",
			Processor: traceActionFn(func(_ context.Context, m message.Message) (message.Message, error) {
				time.Sleep(time.Millisecond)
				ran.Add(1)
				m["branch"] = idx
				return m, nil
			}),
		}
	}
	router := &flowdef.Node{
		ID:        "fanout",
		Kind:      flowdef.Router,
		URI:       "multicast",
		Processor: traceParallelRouter{branches: branches},
		Next:      nexts,
	}
	src := &flowdef.Node{ID: "src", Kind: flowdef.Source, URI: "message", Next: []*flowdef.Node{router}}
	f := &flowdef.Flow{ID: "parallel-trace", Source: src}

	tr := NewTrace()
	_, err := Run(WithTrace(context.Background(), tr), f, message.New("go"))
	if err != nil {
		t.Fatalf("parallel run failed: %v", err)
	}
	if ran.Load() != branches {
		t.Fatalf("expected %d branches to run, got %d", branches, ran.Load())
	}
	steps := tr.Snapshot()
	if len(steps) != 1+branches {
		t.Fatalf("expected %d traced steps (router + %d branches), got %d", 1+branches, branches, len(steps))
	}
	if steps[0].StepID != "fanout" || steps[0].Kind != flowdef.Router {
		t.Fatalf("expected fanout router first, got %+v", steps[0])
	}
}
