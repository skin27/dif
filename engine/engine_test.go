package engine

import (
	"context"
	"errors"
	"strings"
	"testing"

	flowdef "dif/flows/definition"
	"dif/message"
)

// stub records that it ran and optionally fails. It is an action processor.
type stub struct {
	ran *[]string
	id  string
	err error
}

func (s stub) Process(_ context.Context, m message.Message) (message.Message, error) {
	*s.ran = append(*s.ran, s.id)
	return m, s.err
}

// sinkStub is a sink processor.
type sinkStub struct{ stub }

func (s sinkStub) Consume(ctx context.Context, m message.Message) error {
	_, err := s.Process(ctx, m)
	return err
}

// linear builds source -> action -> sink; the action fails with actionErr.
func linear(ran *[]string, actionErr error) *flowdef.Flow {
	sink := &flowdef.Node{ID: "c", Kind: flowdef.Sink, Processor: sinkStub{stub{ran, "c", nil}}}
	action := &flowdef.Node{ID: "b", Kind: flowdef.Action, Processor: stub{ran, "b", actionErr}, Next: []*flowdef.Node{sink}}
	source := &flowdef.Node{ID: "a", Kind: flowdef.Source, Next: []*flowdef.Node{action}}
	return &flowdef.Flow{Source: source}
}

func TestRun(t *testing.T) {
	var ran []string
	in := message.New("x")

	res, err := Run(context.Background(), linear(&ran, nil), in)
	if err != nil {
		t.Fatal(err)
	}
	if res.Message[message.Body] != "x" || res.Message[message.TraceID] != in[message.TraceID] {
		t.Error("message was not passed through")
	}
	if got := strings.Join(res.Trail, " "); got != "source:a action:b sink:c" {
		t.Errorf("trail = %q", got)
	}
	if got := strings.Join(ran, ""); got != "bc" {
		t.Errorf("ran = %q, want bc (the source is not executed)", got)
	}
}

func TestRunStopsOnError(t *testing.T) {
	var ran []string
	boom := errors.New("boom")

	_, err := Run(context.Background(), linear(&ran, boom), message.New(nil))
	if !errors.Is(err, boom) || !strings.Contains(err.Error(), "step b") {
		t.Errorf("err = %v, want wrapped boom for step b", err)
	}
	if got := strings.Join(ran, ""); got != "b" {
		t.Errorf("ran = %q, want b (sink must not run)", got)
	}
}

func TestRunSinkInTheMiddle(t *testing.T) {
	var ran []string
	f := linear(&ran, nil)
	action := f.Source.Next[0]
	sink := action.Next[0]
	// source -> sink processor in an action position -> action
	f.Source.Next = []*flowdef.Node{sink}
	sink.Next, action.Next = []*flowdef.Node{action}, nil

	if _, err := Run(context.Background(), f, message.New(nil)); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(ran, ""); got != "cb" {
		t.Errorf("ran = %q, want cb: the message passes a sink on to the next step", got)
	}
}

func TestRunCancelled(t *testing.T) {
	var ran []string
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := Run(ctx, linear(&ran, nil), message.New(nil)); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if len(ran) != 0 {
		t.Errorf("ran = %v, want nothing after cancel", ran)
	}
}

func TestRunUnsupportedProcessor(t *testing.T) {
	f := &flowdef.Flow{Source: &flowdef.Node{ID: "a", Kind: flowdef.Source, Next: []*flowdef.Node{{ID: "b", Kind: flowdef.Sink, Processor: 42}}}}
	if _, err := Run(context.Background(), f, message.New(nil)); err == nil || !strings.Contains(err.Error(), "step b") {
		t.Errorf("err = %v, want error for step b", err)
	}
}
