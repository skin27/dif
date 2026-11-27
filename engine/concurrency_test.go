package engine

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	flowdef "dif/flows/definition"
	"dif/message"
	stepdef "dif/steps/definition"
)

type actionFn func(context.Context, message.Message) (message.Message, error)

func (f actionFn) Process(ctx context.Context, m message.Message) (message.Message, error) {
	return f(ctx, m)
}

func makeConcurrencyFlow(id string, concurrency int, keyFn func(context.Context, message.Message) (string, error), proc stepdef.Processor) *flowdef.Flow {
	src := &flowdef.Node{ID: "src", Kind: flowdef.Source, URI: "message:in"}
	act := &flowdef.Node{ID: "act", Kind: flowdef.Action, URI: "test", Processor: proc}
	src.Next = []*flowdef.Node{act}
	return &flowdef.Flow{
		ID:           id,
		Source:       src,
		Concurrency:  concurrency,
		PartitionKey: keyFn,
	}
}

func TestRunnerConcurrencyPool(t *testing.T) {
	const workers = 4
	var active, peak atomic.Int32
	arrived := make(chan struct{}, workers)
	release := make(chan struct{})

	f := makeConcurrencyFlow("pool", workers, nil, actionFn(func(_ context.Context, m message.Message) (message.Message, error) {
		cur := active.Add(1)
		for {
			old := peak.Load()
			if cur <= old || peak.CompareAndSwap(old, cur) {
				break
			}
		}
		arrived <- struct{}{}
		<-release
		active.Add(-1)
		return m, nil
	}))

	var results int
	done := make(chan struct{}, workers)
	r := NewRunner(f, func(_ *Result, err error) {
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		results++ // intentionally unguarded to prove Runner serializes onResult
		done <- struct{}{}
	})
	if err := r.Start(); err != nil {
		t.Fatal(err)
	}
	defer r.Stop()

	for i := range workers {
		if err := r.Send(message.New(fmt.Sprintf("msg-%d", i))); err != nil {
			t.Fatalf("send %d: %v", i, err)
		}
	}
	for range workers {
		select {
		case <-arrived:
		case <-time.After(2 * time.Second):
			t.Fatalf("expected %d messages to run concurrently, only %d arrived", workers, peak.Load())
		}
	}
	if got := r.Status().InFlight; got != workers {
		t.Errorf("inFlight = %d, want %d", got, workers)
	}
	close(release)
	for range workers {
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for completions")
		}
	}
	if peak.Load() != workers {
		t.Errorf("peak concurrency = %d, want %d", peak.Load(), workers)
	}
	if results != workers {
		t.Errorf("onResult calls = %d, want %d", results, workers)
	}
	if st := r.Status(); st.Completed != workers || st.Failed != 0 || st.InFlight != 0 {
		t.Errorf("status = %+v, want completed=%d failed=0 inFlight=0", st, workers)
	}
}

func TestRunnerConcurrencyPartitionedFIFO(t *testing.T) {
	const lanes = 8
	// Find two keys that hash to distinct lanes to prove cross-key parallelism.
	keyA := "cust-A"
	keyB := ""
	laneA := partitionLane(keyA, lanes)
	for i := 0; i < 100; i++ {
		cand := fmt.Sprintf("cust-B-%d", i)
		if partitionLane(cand, lanes) != laneA {
			keyB = cand
			break
		}
	}
	if keyB == "" {
		t.Fatal("could not find a key hashing to a different lane")
	}

	// Part 1: prove distinct keys run concurrently via a barrier.
	barrierArrived := make(chan string, 2)
	barrierRelease := make(chan struct{})

	// Part 2: prove strict FIFO order and zero overlap per key.
	var mu sync.Mutex
	inKey := map[string]bool{}
	seenOrder := map[string][]int{}
	var overlapErr atomic.Value

	keyFn := func(_ context.Context, m message.Message) (string, error) {
		k, _ := m["key"].(string)
		return k, nil
	}

	f := makeConcurrencyFlow("part", lanes, keyFn, actionFn(func(_ context.Context, m message.Message) (message.Message, error) {
		k, _ := m["key"].(string)
		if m["mode"] == "barrier" {
			barrierArrived <- k
			<-barrierRelease
			return m, nil
		}
		seq := m["seq"].(int)
		mu.Lock()
		if inKey[k] {
			overlapErr.Store(fmt.Errorf("key %s overlapped concurrently", k))
		}
		inKey[k] = true
		mu.Unlock()

		// Slight jitter to provoke reordering if FIFO were violated.
		if seq%3 == 0 {
			time.Sleep(time.Millisecond)
		}

		mu.Lock()
		seenOrder[k] = append(seenOrder[k], seq)
		delete(inKey, k)
		mu.Unlock()
		return m, nil
	}))

	total := 2 + 6*15
	completed := make(chan struct{}, total)
	r := NewRunner(f, func(_ *Result, err error) {
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
		completed <- struct{}{}
	})
	if err := r.Start(); err != nil {
		t.Fatal(err)
	}
	defer r.Stop()

	// Cross-key concurrency barrier check.
	for _, k := range []string{keyA, keyB} {
		m := message.New(k)
		m["key"] = k
		m["mode"] = "barrier"
		if err := r.Send(m); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		select {
		case <-barrierArrived:
		case <-time.After(2 * time.Second):
			t.Fatal("distinct partition keys did not execute concurrently")
		}
	}
	close(barrierRelease)

	// Send 15 sequential messages for each of 6 keys interleaved.
	keys := []string{"k0", "k1", "k2", "k3", "k4", "k5"}
	for seq := range 15 {
		for _, k := range keys {
			m := message.New(seq)
			m["key"] = k
			m["seq"] = seq
			if err := r.Send(m); err != nil {
				t.Fatal(err)
			}
		}
	}
	for range total {
		select {
		case <-completed:
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for partitioned messages")
		}
	}
	if v := overlapErr.Load(); v != nil {
		t.Fatal(v)
	}
	want := []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14}
	mu.Lock()
	defer mu.Unlock()
	for _, k := range keys {
		if !slices.Equal(seenOrder[k], want) {
			t.Errorf("key %s order = %v, want %v", k, seenOrder[k], want)
		}
	}
}

func TestRunnerPartitionKeyErrorDoesNotStall(t *testing.T) {
	boom := errors.New("bad key")
	keyFn := func(_ context.Context, m message.Message) (string, error) {
		if m["bad"] == true {
			return "", boom
		}
		return "ok", nil
	}
	f := makeConcurrencyFlow("part-err", 4, keyFn, actionFn(func(_ context.Context, m message.Message) (message.Message, error) {
		m[message.Body] = "done"
		return m, nil
	}))
	var resultErrs []error
	r := NewRunner(f, func(_ *Result, err error) {
		resultErrs = append(resultErrs, err)
	})
	if err := r.Start(); err != nil {
		t.Fatal(err)
	}
	defer r.Stop()

	bad := message.New("x")
	bad["bad"] = true
	if _, err := r.Request(context.Background(), bad); !errors.Is(err, boom) {
		t.Fatalf("bad key err = %v, want wrapping %v", err, boom)
	}

	good := message.New("y")
	out, err := r.Request(context.Background(), good)
	if err != nil || out[message.Body] != "done" {
		t.Fatalf("subsequent request = %v, %v; want done", out, err)
	}
	if st := r.Status(); st.Failed != 1 || st.Completed != 1 || st.InFlight != 0 {
		t.Errorf("status = %+v, want failed=1 completed=1 inFlight=0", st)
	}
	if len(resultErrs) != 2 || !errors.Is(resultErrs[0], boom) || resultErrs[1] != nil {
		t.Errorf("onResult errs = %v", resultErrs)
	}
}

func TestRunnerConcurrencyLifecyclePauseStopForceStop(t *testing.T) {
	for _, partitioned := range []bool{false, true} {
		name := "pool"
		var keyFn func(context.Context, message.Message) (string, error)
		if partitioned {
			name = "partitioned"
			keyFn = func(_ context.Context, m message.Message) (string, error) {
				s, _ := m["key"].(string)
				return s, nil
			}
		}
		t.Run(name+"/pause_resume_graceful_stop", func(t *testing.T) {
			entered := make(chan struct{}, 2)
			release := make(chan struct{})
			f := makeConcurrencyFlow("life", 4, keyFn, actionFn(func(_ context.Context, m message.Message) (message.Message, error) {
				if m["block"] == true {
					entered <- struct{}{}
					<-release
				}
				return m, nil
			}))
			r := NewRunner(f, nil)
			if err := r.Start(); err != nil {
				t.Fatal(err)
			}

			// Send two blocking messages.
			for i := range 2 {
				m := message.New(i)
				m["key"] = fmt.Sprintf("k-%d", i)
				m["block"] = true
				if err := r.Send(m); err != nil {
					t.Fatal(err)
				}
			}
			<-entered
			<-entered

			// Pause: new Send must be rejected while in-flight messages finish.
			if err := r.Pause(); err != nil {
				t.Fatal(err)
			}
			if err := r.Send(message.New("rejected")); err == nil || !strings.Contains(err.Error(), "paused") {
				t.Fatalf("send while paused err = %v, want paused error", err)
			}

			// Release blocked messages and start graceful Stop after Resume.
			if err := r.Resume(); err != nil {
				t.Fatal(err)
			}
			stopDone := make(chan error, 1)
			go func() { stopDone <- r.Stop() }()
			time.Sleep(15 * time.Millisecond)
			close(release)

			select {
			case err := <-stopDone:
				if err != nil {
					t.Fatalf("stop err = %v", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("graceful Stop did not complete after releasing in-flight messages")
			}
			if st := r.Status(); st.State != Stopped || st.Completed != 2 || st.InFlight != 0 {
				t.Errorf("status after graceful stop = %+v", st)
			}
		})

		t.Run(name+"/force_stop", func(t *testing.T) {
			entered := make(chan struct{}, 2)
			f := makeConcurrencyFlow("force", 4, keyFn, actionFn(func(ctx context.Context, m message.Message) (message.Message, error) {
				entered <- struct{}{}
				<-ctx.Done()
				return nil, ctx.Err()
			}))
			r := NewRunner(f, nil)
			if err := r.Start(); err != nil {
				t.Fatal(err)
			}
			for i := range 2 {
				m := message.New(i)
				m["key"] = fmt.Sprintf("k-%d", i)
				if err := r.Send(m); err != nil {
					t.Fatal(err)
				}
			}
			<-entered
			<-entered
			if err := r.ForceStop(); err != nil {
				t.Fatal(err)
			}
			if st := r.Status(); st.State != Stopped || st.Failed != 2 || st.InFlight != 0 {
				t.Errorf("status after force stop = %+v", st)
			}
		})
	}
}

// testParallelRouter is a minimal DSL-neutral ParallelRouter for engine tests.
type testParallelRouter struct {
	policy stepdef.ParallelPolicy
	route  func(context.Context, message.Message) ([]stepdef.Route, error)
}

func (p testParallelRouter) ParallelPolicy() stepdef.ParallelPolicy { return p.policy }
func (p testParallelRouter) Route(ctx context.Context, m message.Message) ([]stepdef.Route, error) {
	return p.route(ctx, m)
}

type testParallelGatherer struct {
	testParallelRouter
	gather func(context.Context, message.Message, []stepdef.Outcome) ([]stepdef.Route, error)
}

func (g testParallelGatherer) Gather(ctx context.Context, m message.Message, outcomes []stepdef.Outcome) ([]stepdef.Route, error) {
	return g.gather(ctx, m, outcomes)
}

func TestParallelRouterMaxConcurrencyAndOrder(t *testing.T) {
	const total = 12
	const limit = 3
	var active, peak atomic.Int32
	barrier := make(chan struct{}, limit)
	release := make(chan struct{})
	var releaseOnce sync.Once

	branch := &flowdef.Node{
		ID:   "work",
		Kind: flowdef.Action,
		URI:  "test",
		Processor: actionFn(func(_ context.Context, m message.Message) (message.Message, error) {
			cur := active.Add(1)
			for {
				old := peak.Load()
				if cur <= old || peak.CompareAndSwap(old, cur) {
					break
				}
			}
			idx := m["idx"].(int)
			if idx < limit {
				barrier <- struct{}{}
				<-release
			} else {
				time.Sleep(5 * time.Millisecond)
			}
			active.Add(-1)
			m[message.Body] = fmt.Sprintf("out-%d", idx)
			return m, nil
		}),
	}
	router := &flowdef.Node{
		ID:   "par",
		Kind: flowdef.Router,
		URI:  "test",
		Next: []*flowdef.Node{branch},
		Processor: testParallelRouter{
			policy: stepdef.ParallelPolicy{Parallel: true, MaxConcurrency: limit, FailFast: true},
			route: func(_ context.Context, m message.Message) ([]stepdef.Route, error) {
				routes := make([]stepdef.Route, total)
				for i := range total {
					c := m.Copy()
					c["idx"] = i
					routes[i] = stepdef.Route{Next: 0, Message: c}
				}
				return routes, nil
			},
		},
	}
	src := &flowdef.Node{ID: "src", Kind: flowdef.Source, URI: "message:in", Next: []*flowdef.Node{router}}
	f := &flowdef.Flow{ID: "maxconc", Source: src}

	go func() {
		for range limit {
			<-barrier
		}
		releaseOnce.Do(func() { close(release) })
	}()

	res, err := Run(context.Background(), f, message.New("in"))
	if err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if peak.Load() != limit {
		t.Errorf("peak concurrency = %d, want %d", peak.Load(), limit)
	}
	if res.Message[message.Body] != fmt.Sprintf("out-%d", total-1) {
		t.Errorf("outcome body = %v, want out-%d", res.Message[message.Body], total-1)
	}
	wantTrail := append([]string{"source:src", "router:par"}, slices.Repeat([]string{"action:work"}, total)...)
	if !slices.Equal(res.Trail, wantTrail) {
		t.Errorf("trail = %v, want %v", res.Trail, wantTrail)
	}
}

func TestParallelRouterBranchTimeout(t *testing.T) {
	var exited atomic.Bool
	branch := &flowdef.Node{
		ID:   "slow",
		Kind: flowdef.Action,
		URI:  "test",
		Processor: actionFn(func(ctx context.Context, m message.Message) (message.Message, error) {
			defer exited.Store(true)
			<-ctx.Done()
			return nil, ctx.Err()
		}),
	}
	router := &flowdef.Node{
		ID:   "par",
		Kind: flowdef.Router,
		URI:  "test",
		Next: []*flowdef.Node{branch},
		Processor: testParallelRouter{
			policy: stepdef.ParallelPolicy{Parallel: true, BranchTimeout: 30 * time.Millisecond, FailFast: true},
			route: func(_ context.Context, m message.Message) ([]stepdef.Route, error) {
				return []stepdef.Route{
					{Next: 0, Message: m.Copy()},
					{Next: 0, Message: m.Copy()},
				}, nil
			},
		},
	}
	src := &flowdef.Node{ID: "src", Kind: flowdef.Source, URI: "message:in", Next: []*flowdef.Node{router}}
	_, err := Run(context.Background(), &flowdef.Flow{ID: "timeout", Source: src}, message.New("in"))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
	if !exited.Load() {
		t.Fatal("branch goroutine was not joined before Run returned")
	}
}

func TestParallelRouterFailFastCancelsSiblingsAndJoins(t *testing.T) {
	for _, gather := range []bool{false, true} {
		name := "non-gather"
		if gather {
			name = "gather"
		}
		t.Run(name, func(t *testing.T) {
			boom := errors.New("branch-1 boom")
			var activeSiblings atomic.Int32
			siblingStarted := make(chan struct{}, 2)

			branch := &flowdef.Node{
				ID:   "work",
				Kind: flowdef.Action,
				URI:  "test",
				Processor: actionFn(func(ctx context.Context, m message.Message) (message.Message, error) {
					idx := m["idx"].(int)
					if idx == 1 {
						// Wait for at least sibling 0 to enter before failing.
						<-siblingStarted
						return nil, boom
					}
					activeSiblings.Add(1)
					defer activeSiblings.Add(-1)
					select {
					case siblingStarted <- struct{}{}:
					default:
					}
					<-ctx.Done()
					return nil, ctx.Err()
				}),
			}
			base := testParallelRouter{
				policy: stepdef.ParallelPolicy{Parallel: true, MaxConcurrency: 4, FailFast: true},
				route: func(_ context.Context, m message.Message) ([]stepdef.Route, error) {
					routes := make([]stepdef.Route, 4)
					for i := range 4 {
						c := m.Copy()
						c["idx"] = i
						routes[i] = stepdef.Route{Next: 0, Message: c}
					}
					return routes, nil
				},
			}
			var proc stepdef.Processor = base
			if gather {
				proc = testParallelGatherer{
					testParallelRouter: base,
					gather: func(_ context.Context, m message.Message, outcomes []stepdef.Outcome) ([]stepdef.Route, error) {
						t.Error("Gather should not be called after failFast abort")
						return nil, nil
					},
				}
			}
			router := &flowdef.Node{ID: "par", Kind: flowdef.Router, URI: "test", Next: []*flowdef.Node{branch}, Processor: proc}
			src := &flowdef.Node{ID: "src", Kind: flowdef.Source, URI: "message:in", Next: []*flowdef.Node{router}}

			_, err := Run(context.Background(), &flowdef.Flow{ID: "ff", Source: src}, message.New("in"))
			if !errors.Is(err, boom) {
				t.Fatalf("err = %v, want root-cause %v (not sibling context.Canceled)", err, boom)
			}
			if activeSiblings.Load() != 0 {
				t.Fatalf("leaked %d sibling goroutines after Run returned", activeSiblings.Load())
			}
		})
	}
}

func TestParallelRouterAfterParallelAndGatherDeterministicOrder(t *testing.T) {
	var partsDone atomic.Int32
	part := &flowdef.Node{
		ID:   "part",
		Kind: flowdef.Action,
		URI:  "test",
		Processor: actionFn(func(_ context.Context, m message.Message) (message.Message, error) {
			idx := m["idx"].(int)
			// Finish in reverse order: 0 slowest, 3 fastest.
			time.Sleep(time.Duration(4-idx) * 10 * time.Millisecond)
			partsDone.Add(1)
			m[message.Body] = fmt.Sprintf("p%d", idx)
			return m, nil
		}),
	}
	after := &flowdef.Node{
		ID:   "after",
		Kind: flowdef.Action,
		URI:  "test",
		Processor: actionFn(func(_ context.Context, m message.Message) (message.Message, error) {
			if got := partsDone.Load(); got != 4 {
				return nil, fmt.Errorf("after ran before all parts finished: %d/4", got)
			}
			m[message.Body] = "continued"
			return m, nil
		}),
	}
	router := &flowdef.Node{
		ID:   "split",
		Kind: flowdef.Router,
		URI:  "test",
		Next: []*flowdef.Node{part, after},
		Processor: testParallelRouter{
			policy: stepdef.ParallelPolicy{Parallel: true, FailFast: true},
			route: func(_ context.Context, m message.Message) ([]stepdef.Route, error) {
				routes := make([]stepdef.Route, 0, 5)
				for i := range 4 {
					c := m.Copy()
					c["idx"] = i
					routes = append(routes, stepdef.Route{Next: 0, Message: c})
				}
				routes = append(routes, stepdef.Route{Next: 1, Message: m, AfterParallel: true})
				return routes, nil
			},
		},
	}
	src := &flowdef.Node{ID: "src", Kind: flowdef.Source, URI: "message:in", Next: []*flowdef.Node{router}}
	res, err := Run(context.Background(), &flowdef.Flow{ID: "after-test", Source: src}, message.New("orig"))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Message[message.Body] != "continued" {
		t.Errorf("body = %v, want continued", res.Message[message.Body])
	}
	wantTrail := []string{"source:src", "router:split", "action:part", "action:part", "action:part", "action:part", "action:after"}
	if !slices.Equal(res.Trail, wantTrail) {
		t.Errorf("trail = %v, want %v", res.Trail, wantTrail)
	}

	// Gather deterministic ordering test.
	var gathered []string
	gatherRouter := &flowdef.Node{
		ID:   "sg",
		Kind: flowdef.Router,
		URI:  "test",
		Next: []*flowdef.Node{part, after},
		Processor: testParallelGatherer{
			testParallelRouter: testParallelRouter{
				policy: stepdef.ParallelPolicy{Parallel: true, FailFast: true},
				route: func(_ context.Context, m message.Message) ([]stepdef.Route, error) {
					routes := make([]stepdef.Route, 4)
					for i := range 4 {
						c := m.Copy()
						c["idx"] = i
						routes[i] = stepdef.Route{Next: 0, Message: c}
					}
					return routes, nil
				},
			},
			gather: func(_ context.Context, m message.Message, outcomes []stepdef.Outcome) ([]stepdef.Route, error) {
				for _, o := range outcomes {
					if o.Err != nil {
						return nil, o.Err
					}
					gathered = append(gathered, o.Message[message.Body].(string))
				}
				m[message.Body] = strings.Join(gathered, ",")
				return []stepdef.Route{{Next: 1, Message: m}}, nil
			},
		},
	}
	partsDone.Store(0)
	src2 := &flowdef.Node{ID: "src", Kind: flowdef.Source, URI: "message:in", Next: []*flowdef.Node{gatherRouter}}
	if _, err := Run(context.Background(), &flowdef.Flow{ID: "gather-order", Source: src2}, message.New("orig")); err != nil {
		t.Fatalf("gather Run: %v", err)
	}
	if !slices.Equal(gathered, []string{"p0", "p1", "p2", "p3"}) {
		t.Errorf("gathered order = %v, want [p0 p1 p2 p3]", gathered)
	}
}

func TestParallelRouterConcurrentReplySafety(t *testing.T) {
	branch := &flowdef.Node{
		ID:   "ret",
		Kind: flowdef.Action,
		URI:  "test",
		Processor: actionFn(func(ctx context.Context, m message.Message) (message.Message, error) {
			runtime.Gosched()
			stepdef.Return(ctx, m.Copy(), nil, true)
			m[message.ExchangePattern] = message.InOnly
			return m, nil
		}),
	}
	router := &flowdef.Node{
		ID:   "par",
		Kind: flowdef.Router,
		URI:  "test",
		Next: []*flowdef.Node{branch},
		Processor: testParallelRouter{
			policy: stepdef.ParallelPolicy{Parallel: true, MaxConcurrency: 8, FailFast: true},
			route: func(_ context.Context, m message.Message) ([]stepdef.Route, error) {
				routes := make([]stepdef.Route, 8)
				for i := range routes {
					c := m.Copy()
					c[message.Body] = fmt.Sprintf("reply-%d", i)
					routes[i] = stepdef.Route{Next: 0, Message: c}
				}
				return routes, nil
			},
		},
	}
	src := &flowdef.Node{ID: "src", Kind: flowdef.Source, URI: "message:in", Next: []*flowdef.Node{router}}
	f := &flowdef.Flow{ID: "reply-race", Source: src}

	var replies atomic.Int32
	_, err := execute(context.Background(), f, message.New("in"), func(_ message.Message, _ error) {
		replies.Add(1)
	})
	if err != nil {
		t.Fatalf("execute: %v", err)
	}
	if got := replies.Load(); got != 1 {
		t.Fatalf("reply called %d times, want exactly 1", got)
	}
}
