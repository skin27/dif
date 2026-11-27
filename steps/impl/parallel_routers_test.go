package impl

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"dif/engine"
	flowdef "dif/flows/definition"
	"dif/message"
	stepdef "dif/steps/definition"
)

type testActionFunc func(context.Context, message.Message) (message.Message, error)

func (f testActionFunc) Process(ctx context.Context, m message.Message) (message.Message, error) {
	return f(ctx, m)
}

func TestParallelSplitRunsPartsConcurrentlyBeforeMain(t *testing.T) {
	r, err := newRouter(stepdef.Router, "split", map[string]any{
		"language":           "tokenize",
		"expression":         ",",
		"parallelProcessing": true,
		"maxConcurrency":     3,
	}, stepdef.Link{Rule: "split"}, stepdef.Link{})
	if err != nil {
		t.Fatal(err)
	}
	var arrived atomic.Int32
	var done atomic.Int32
	allArrived := make(chan struct{})
	var closeOnce sync.Once

	partNode := &flowdef.Node{
		ID:   "part",
		Kind: flowdef.Action,
		URI:  "test",
		Processor: testActionFunc(func(_ context.Context, m message.Message) (message.Message, error) {
			if arrived.Add(1) == 3 {
				closeOnce.Do(func() { close(allArrived) })
			}
			select {
			case <-allArrived:
			case <-time.After(2 * time.Second):
				return nil, errors.New("parts did not execute in parallel")
			}
			done.Add(1)
			return m, nil
		}),
	}
	mainNode := &flowdef.Node{
		ID:   "main",
		Kind: flowdef.Action,
		URI:  "test",
		Processor: testActionFunc(func(_ context.Context, m message.Message) (message.Message, error) {
			if got := done.Load(); got != 3 {
				return nil, fmt.Errorf("main ran before all split parts completed: %d/3", got)
			}
			m[message.Body] = "after-split"
			return m, nil
		}),
	}
	routerNode := &flowdef.Node{
		ID:        "sp",
		Kind:      flowdef.Router,
		URI:       "split",
		Next:      []*flowdef.Node{partNode, mainNode},
		Processor: r,
	}
	src := &flowdef.Node{ID: "src", Kind: flowdef.Source, URI: "message:in", Next: []*flowdef.Node{routerNode}}
	res, err := engine.Run(context.Background(), &flowdef.Flow{ID: "par-split", Source: src}, message.New("a,b,c"))
	if err != nil {
		t.Fatalf("engine.Run: %v", err)
	}
	if res.Message[message.Body] != "after-split" {
		t.Errorf("body = %v, want after-split", res.Message[message.Body])
	}
}

func TestParallelSplitWithNamespace(t *testing.T) {
	r, err := newRouter(stepdef.Router, "splitwithnamespace", map[string]any{
		"expression":         "/ns:items/ns:item",
		"nsprefix":           "ns",
		"namespace":          "urn:test",
		"parallelProcessing": true,
	}, stepdef.Link{Rule: "split"}, stepdef.Link{})
	if err != nil {
		t.Fatal(err)
	}
	var count atomic.Int32
	partNode := &flowdef.Node{
		ID:   "part",
		Kind: flowdef.Action,
		URI:  "test",
		Processor: testActionFunc(func(_ context.Context, m message.Message) (message.Message, error) {
			count.Add(1)
			return m, nil
		}),
	}
	mainNode := &flowdef.Node{
		ID:   "main",
		Kind: flowdef.Action,
		URI:  "test",
		Processor: testActionFunc(func(_ context.Context, m message.Message) (message.Message, error) {
			if count.Load() != 2 {
				return nil, fmt.Errorf("main ran before parts finished: %d", count.Load())
			}
			return m, nil
		}),
	}
	routerNode := &flowdef.Node{ID: "sp", Kind: flowdef.Router, URI: "splitwithnamespace", Next: []*flowdef.Node{partNode, mainNode}, Processor: r}
	src := &flowdef.Node{ID: "src", Kind: flowdef.Source, URI: "message:in", Next: []*flowdef.Node{routerNode}}
	_, err = engine.Run(context.Background(), &flowdef.Flow{ID: "par-spns", Source: src}, message.New(`<ns:items xmlns:ns="urn:test"><ns:item>1</ns:item><ns:item>2</ns:item></ns:items>`))
	if err != nil {
		t.Fatalf("engine.Run: %v", err)
	}
}

func TestParallelSplitAndAggregatePreservesOrder(t *testing.T) {
	r, err := newRouter(stepdef.Router, "splitandaggregate", map[string]any{
		"language":           "jsonpath",
		"expression":         "$.items[*]",
		"aggregateType":      "json",
		"parallelProcessing": true,
		"maxConcurrency":     4,
	}, stepdef.Link{Rule: "split"}, stepdef.Link{})
	if err != nil {
		t.Fatal(err)
	}
	var arrived atomic.Int32
	allArrived := make(chan struct{})
	var closeOnce sync.Once

	partNode := &flowdef.Node{
		ID:   "part",
		Kind: flowdef.Action,
		URI:  "test",
		Processor: testActionFunc(func(_ context.Context, m message.Message) (message.Message, error) {
			idx := m[SplitIndex].(int)
			if arrived.Add(1) == 4 {
				closeOnce.Do(func() { close(allArrived) })
			}
			select {
			case <-allArrived:
			case <-time.After(2 * time.Second):
				return nil, errors.New("parts did not run concurrently")
			}
			// Finish in reverse order so out-of-order completion is exercised.
			time.Sleep(time.Duration(4-idx) * 8 * time.Millisecond)
			m[message.Body] = fmt.Sprintf(`{"n":%s,"idx":%d}`, text(m[message.Body]), idx)
			return m, nil
		}),
	}
	mainNode := &flowdef.Node{
		ID:   "main",
		Kind: flowdef.Action,
		URI:  "test",
		Processor: testActionFunc(func(_ context.Context, m message.Message) (message.Message, error) {
			return m, nil
		}),
	}
	routerNode := &flowdef.Node{ID: "sa", Kind: flowdef.Router, URI: "splitandaggregate", Next: []*flowdef.Node{partNode, mainNode}, Processor: r}
	src := &flowdef.Node{ID: "src", Kind: flowdef.Source, URI: "message:in", Next: []*flowdef.Node{routerNode}}

	res, err := engine.Run(context.Background(), &flowdef.Flow{ID: "par-sa", Source: src}, message.New(`{"items":[10,20,30,40]}`))
	if err != nil {
		t.Fatalf("engine.Run: %v", err)
	}
	want := `[{"n":10,"idx":0},{"n":20,"idx":1},{"n":30,"idx":2},{"n":40,"idx":3}]`
	if res.Message[message.Body] != want {
		t.Errorf("aggregated body = %v, want %s", res.Message[message.Body], want)
	}
}

func TestParallelSplitAndAggregateWithNamespace(t *testing.T) {
	r, err := newRouter(stepdef.Router, "splitandaggregatewithnamespace", map[string]any{
		"expression":         "/ns:items/ns:item",
		"nsprefix":           "ns",
		"namespace":          "urn:test",
		"aggregateType":      "xml",
		"parallelProcessing": true,
	}, stepdef.Link{Rule: "split"}, stepdef.Link{})
	if err != nil {
		t.Fatal(err)
	}
	partNode := &flowdef.Node{
		ID:   "part",
		Kind: flowdef.Action,
		URI:  "test",
		Processor: testActionFunc(func(_ context.Context, m message.Message) (message.Message, error) {
			idx := m[SplitIndex].(int)
			time.Sleep(time.Duration(3-idx) * 8 * time.Millisecond)
			return m, nil
		}),
	}
	mainNode := &flowdef.Node{
		ID:        "main",
		Kind:      flowdef.Action,
		URI:       "test",
		Processor: testActionFunc(func(_ context.Context, m message.Message) (message.Message, error) { return m, nil }),
	}
	routerNode := &flowdef.Node{ID: "sans", Kind: flowdef.Router, URI: "splitandaggregatewithnamespace", Next: []*flowdef.Node{partNode, mainNode}, Processor: r}
	src := &flowdef.Node{ID: "src", Kind: flowdef.Source, URI: "message:in", Next: []*flowdef.Node{routerNode}}

	res, err := engine.Run(context.Background(), &flowdef.Flow{ID: "par-sans", Source: src}, message.New(`<ns:items xmlns:ns="urn:test"><ns:item>A</ns:item><ns:item>B</ns:item><ns:item>C</ns:item></ns:items>`))
	if err != nil {
		t.Fatalf("engine.Run: %v", err)
	}
	got := text(res.Message[message.Body])
	idxA, idxB, idxC := strings.Index(got, ">A<"), strings.Index(got, ">B<"), strings.Index(got, ">C<")
	if idxA < 0 || idxB < 0 || idxC < 0 || !(idxA < idxB && idxB < idxC) {
		t.Errorf("aggregated XML out of order: %s", got)
	}
}

func TestParallelRecipientRouter(t *testing.T) {
	r, err := newRouter(stepdef.Router, "recipient", map[string]any{
		"parallelProcessing": true,
		"maxConcurrency":     3,
	}, stepdef.Link{}, stepdef.Link{}, stepdef.Link{})
	if err != nil {
		t.Fatal(err)
	}
	var arrived atomic.Int32
	allArrived := make(chan struct{})
	var closeOnce sync.Once

	makeBranch := func(id, label string) *flowdef.Node {
		return &flowdef.Node{
			ID:   id,
			Kind: flowdef.Action,
			URI:  "test",
			Processor: testActionFunc(func(_ context.Context, m message.Message) (message.Message, error) {
				if arrived.Add(1) == 3 {
					closeOnce.Do(func() { close(allArrived) })
				}
				select {
				case <-allArrived:
				case <-time.After(2 * time.Second):
					return nil, errors.New("recipient branches did not run concurrently")
				}
				m[message.Body] = label
				return m, nil
			}),
		}
	}
	routerNode := &flowdef.Node{
		ID:        "rec",
		Kind:      flowdef.Router,
		URI:       "recipient",
		Next:      []*flowdef.Node{makeBranch("b1", "first"), makeBranch("b2", "second"), makeBranch("b3", "third")},
		Processor: r,
	}
	src := &flowdef.Node{ID: "src", Kind: flowdef.Source, URI: "message:in", Next: []*flowdef.Node{routerNode}}
	res, err := engine.Run(context.Background(), &flowdef.Flow{ID: "par-rec", Source: src}, message.New("hi"))
	if err != nil {
		t.Fatalf("engine.Run: %v", err)
	}
	if res.Message[message.Body] != "third" {
		t.Errorf("outcome = %v, want third", res.Message[message.Body])
	}
}

func TestParallelEnrichBranchTimeout(t *testing.T) {
	runEnrich := func(useErrorRoute bool) (*engine.Result, error, string) {
		r, err := newRouter(stepdef.Router, "enrich", map[string]any{
			"enrichType":         "json",
			"useErrorRoute":      useErrorRoute,
			"parallelProcessing": true,
			"branchTimeout":      30,
		}, stepdef.Link{}, stepdef.Link{Rule: "enrich"})
		if err != nil {
			t.Fatal(err)
		}
		mainNode := &flowdef.Node{
			ID:        "main",
			Kind:      flowdef.Action,
			URI:       "test",
			Processor: testActionFunc(func(_ context.Context, m message.Message) (message.Message, error) { return m, nil }),
		}
		slowEnrich := &flowdef.Node{
			ID:   "slow",
			Kind: flowdef.Action,
			URI:  "test",
			Processor: testActionFunc(func(ctx context.Context, m message.Message) (message.Message, error) {
				<-ctx.Done()
				return nil, ctx.Err()
			}),
		}
		routerNode := &flowdef.Node{
			ID:        "enr",
			Kind:      flowdef.Router,
			URI:       "enrich",
			Next:      []*flowdef.Node{mainNode, slowEnrich},
			Processor: r,
		}
		src := &flowdef.Node{ID: "src", Kind: flowdef.Source, URI: "message:in", Next: []*flowdef.Node{routerNode}}
		var logged bytes.Buffer
		ctx := stepdef.WithLogger(context.Background(), log.New(&logged, "", 0))
		res, err := engine.Run(ctx, &flowdef.Flow{ID: "par-enrich", Source: src}, message.New(`{"a":1}`))
		return res, err, logged.String()
	}

	// useErrorRoute=true -> fails with context.DeadlineExceeded.
	if _, err, _ := runEnrich(true); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("useErrorRoute=true: err = %v, want context.DeadlineExceeded", err)
	}

	// useErrorRoute=false -> logs timeout and continues unenriched.
	res, err, logOut := runEnrich(false)
	if err != nil {
		t.Fatalf("useErrorRoute=false: unexpected err = %v", err)
	}
	if res.Message[message.Body] != `{"a":1}` {
		t.Errorf("body = %v, want original unenriched JSON", res.Message[message.Body])
	}
	if !strings.Contains(logOut, "enrichment failed") {
		t.Errorf("log = %q, want enrichment failed notice", logOut)
	}
}

func TestParallelRouterSchemaValidation(t *testing.T) {
	for _, routerName := range []string{"split", "splitwithnamespace", "splitandaggregate", "splitandaggregatewithnamespace", "recipient", "enrich"} {
		links := []stepdef.Link{{Rule: "split"}, {}}
		baseOpts := map[string]any{"expression": "//a"}
		switch routerName {
		case "recipient":
			links = []stepdef.Link{{}, {}}
			baseOpts = map[string]any{}
		case "enrich":
			links = []stepdef.Link{{Rule: "enrich"}, {}}
			baseOpts = map[string]any{}
		}
		t.Run(routerName+"/negative_maxConcurrency", func(t *testing.T) {
			opts := map[string]any{"maxConcurrency": -1}
			for k, v := range baseOpts {
				opts[k] = v
			}
			if _, err := newRouter(stepdef.Router, routerName, opts, links...); err == nil {
				t.Errorf("expected error for negative maxConcurrency on %s", routerName)
			}
		})
		t.Run(routerName+"/unknown_option", func(t *testing.T) {
			opts := map[string]any{"notAnOption": true}
			for k, v := range baseOpts {
				opts[k] = v
			}
			if _, err := newRouter(stepdef.Router, routerName, opts, links...); err == nil {
				t.Errorf("expected error for unknown option on %s", routerName)
			}
		})
	}
}
