package engine

import (
	"context"
	"errors"
	"testing"
	"time"

	flowdef "dif/flows/definition"
	"dif/message"
	stepdef "dif/steps/definition"
)

type completionProbe struct {
	replied         chan struct{}
	completed       chan error
	settlementError error
}

func (s completionProbe) Run(context.Context, stepdef.Emit) error {
	return errors.New("completion source was run without completion support")
}
func (s completionProbe) RunDelivery(ctx context.Context, emit stepdef.EmitDelivery, ready func()) error {
	ready()
	if err := emit(message.New("work"), func(message.Message, error) { close(s.replied) }, func(err error) error { s.completed <- err; return s.settlementError }); err != nil {
		return err
	}
	<-ctx.Done()
	return nil
}

func TestTerminalCompletionAfterEarlyReplyAndErrorRoute(t *testing.T) {
	for _, handled := range []bool{false, true} {
		t.Run(map[bool]string{false: "unhandled", true: "handled"}[handled], func(t *testing.T) {
			probe := completionProbe{replied: make(chan struct{}), completed: make(chan error, 1)}
			release := make(chan struct{})
			boom := errors.New("late failure")
			last := &flowdef.Node{ID: "last", Kind: flowdef.Action, Processor: gate{release, boom}}
			early := &flowdef.Node{ID: "early", Kind: flowdef.Action, Processor: oneWay{"early"}, Next: []*flowdef.Node{last}}
			f := &flowdef.Flow{ID: "completion", Source: &flowdef.Node{ID: "source", Kind: flowdef.Source, Processor: probe, Next: []*flowdef.Node{early}}}
			if handled {
				f.Error = &flowdef.ErrorHandler{ID: "error", Route: &flowdef.Node{ID: "handled", Kind: flowdef.Action, Processor: oneWay{"handled"}}}
			}
			r := started(t, f, nil)
			select {
			case <-probe.replied:
			case <-time.After(time.Second):
				t.Fatal("no early reply")
			}
			select {
			case err := <-probe.completed:
				t.Fatal("premature completion", err)
			default:
			}
			close(release)
			select {
			case err := <-probe.completed:
				if handled && err != nil || !handled && !errors.Is(err, boom) {
					t.Fatalf("handled=%v completion=%v", handled, err)
				}
			case <-time.After(time.Second):
				t.Fatal("no terminal completion")
			}
			must(t, r.Stop())
		})
	}
}

func TestSettlementErrorIsSourceFailure(t *testing.T) {
	probe := completionProbe{replied: make(chan struct{}), completed: make(chan error, 1), settlementError: errors.New("ack failed")}
	r := started(t, &flowdef.Flow{Source: &flowdef.Node{ID: "source", Kind: flowdef.Source, Processor: probe}}, nil)
	select {
	case <-r.SourceFailed():
		if !errors.Is(r.SourceError(), probe.settlementError) {
			t.Fatal(r.SourceError())
		}
	case <-time.After(time.Second):
		t.Fatal("settlement failure was not reported")
	}
}
