package engine

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	flowdef "dif/flows/definition"
	"dif/message"
	stepdef "dif/steps/definition"
)

// holder is a router that holds every message it gets, and releases the
// messages the test puts on out.
type holder struct {
	out      chan message.Message
	released atomic.Bool // set once Release has returned
}

func (*holder) Route(context.Context, message.Message) ([]stepdef.Route, error) { return nil, nil }

func (h *holder) Release(ctx context.Context, send func(message.Message) error) error {
	defer h.released.Store(true)
	for {
		select {
		case <-ctx.Done():
			return nil
		case m := <-h.out:
			if err := send(m); err != nil {
				return err
			}
		}
	}
}

// passes is a step that never fails.
type passes struct{}

func (passes) Process(_ context.Context, m message.Message) (message.Message, error) { return m, nil }

// holderFlow builds source -> holder -> failOn sink (-> error route, if errRoute).
func holderFlow(h *holder, errRoute bool) *flowdef.Flow {
	sink := &flowdef.Node{ID: "f", Kind: flowdef.Sink, Processor: failOn{}}
	hold := &flowdef.Node{ID: "h", Kind: flowdef.Router, Processor: h, Next: []*flowdef.Node{sink}}
	f := &flowdef.Flow{Source: &flowdef.Node{ID: "s", Kind: flowdef.Source, Processor: chanSource(nil), Next: []*flowdef.Node{hold}}}
	if errRoute {
		f.Error = &flowdef.ErrorHandler{ID: "e", Route: &flowdef.Node{ID: "r", Kind: flowdef.Action, Processor: passes{}}}
	}
	return f
}

func releaserRunner(h *holder, errRoute bool) (*Runner, chan result) {
	results := make(chan result, 10)
	return NewRunner(holderFlow(h, errRoute), func(res *Result, err error) { results <- result{res, err} }), results
}

func TestReleaserMessagesEnterAfterTheirStep(t *testing.T) {
	h := &holder{out: make(chan message.Message)}
	r, results := releaserRunner(h, false)
	must(t, r.Start())
	defer r.Stop()

	h.out <- message.New("one")
	got := next(t, results)
	if got.err != nil || got.res.Message[message.Body] != "one" {
		t.Fatalf("result = %+v, %v", got.res, got.err)
	}
	if want := []string{"router:h", "sink:f"}; len(got.res.Trail) != 2 || got.res.Trail[0] != want[0] || got.res.Trail[1] != want[1] {
		t.Errorf("trail = %v, want %v: the message enters after the step that released it", got.res.Trail, want)
	}
	if got.res.Message[message.Body] != "one" || got.res.Message[message.MessageID] == nil {
		t.Errorf("message = %v, want its identity ensured", got.res.Message)
	}
	if n := r.Status().Completed; n != 1 {
		t.Errorf("completed = %d, want 1: a released message counts as one", n)
	}

	// A failing one fails alone; the flow and the releaser go on.
	h.out <- message.New("fail")
	if got := next(t, results); got.err == nil {
		t.Errorf("result = %+v, want the failure", got.res)
	}
	h.out <- message.New("two")
	if got := next(t, results); got.err != nil || got.res.Message[message.Body] != "two" {
		t.Errorf("result = %+v, %v", got.res, got.err)
	}
}

func TestReleaserFailureTakesTheErrorRoute(t *testing.T) {
	h := &holder{out: make(chan message.Message)}
	r, results := releaserRunner(h, true)
	must(t, r.Start())
	defer r.Stop()

	h.out <- message.New("fail")
	got := next(t, results)
	if got.err != nil || got.res.Err == nil || got.res.Message[ErrorStep] != "f" {
		t.Errorf("result = %+v, %v, want the failure handled by the error route", got.res, got.err)
	}
}

func TestReleaserWaitsWhilePaused(t *testing.T) {
	h := &holder{out: make(chan message.Message)}
	r, results := releaserRunner(h, false)
	must(t, r.Start())
	defer r.Stop()
	must(t, r.Pause())

	go func() { h.out <- message.New("late") }()
	select {
	case got := <-results:
		t.Fatalf("a paused flow processed %+v", got.res)
	case <-time.After(50 * time.Millisecond):
	}
	must(t, r.Resume())
	if got := next(t, results); got.res.Message[message.Body] != "late" {
		t.Errorf("result = %+v", got.res)
	}
}

func TestStopWaitsForTheReleaser(t *testing.T) {
	h := &holder{out: make(chan message.Message)}
	r, _ := releaserRunner(h, false)
	must(t, r.Start())
	must(t, r.Stop())
	if !h.released.Load() {
		t.Error("Stop returned before Release did")
	}
}

func TestReleasersAreFoundOnce(t *testing.T) {
	h := &holder{out: make(chan message.Message)}
	f := holderFlow(h, true)
	hold := f.Source.Next[0]
	hold.Next = append(hold.Next, hold) // a loop must not hang the walk nor release twice
	if got := releasers(f); len(got) != 1 || got[0] != hold {
		t.Errorf("releasers = %v", got)
	}
}
