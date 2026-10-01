package engine

import (
	"errors"
	"strings"
	"testing"

	flowdef "dif/flows/definition"
	"dif/message"
)

// stub records that it ran and optionally fails.
type stub struct {
	ran *[]string
	id  string
	err error
}

func (s stub) Execute(m *message.Message) (*message.Message, error) {
	*s.ran = append(*s.ran, s.id)
	return m, s.err
}

// linear builds source -> action -> sink; the action fails with actionErr.
func linear(ran *[]string, actionErr error) *flowdef.Flow {
	sink := &flowdef.Node{ID: "c", Kind: flowdef.Sink, Step: stub{ran, "c", nil}}
	action := &flowdef.Node{ID: "b", Kind: flowdef.Action, Step: stub{ran, "b", actionErr}, Next: []*flowdef.Node{sink}}
	source := &flowdef.Node{ID: "a", Kind: flowdef.Source, Step: stub{ran, "a", nil}, Next: []*flowdef.Node{action}}
	return &flowdef.Flow{Source: source}
}

func TestRun(t *testing.T) {
	var ran []string
	in := message.New("x")

	res, err := Run(linear(&ran, nil), in)
	if err != nil {
		t.Fatal(err)
	}
	if res.Message != in {
		t.Error("message was not passed through")
	}
	if got := strings.Join(res.Trail, " "); got != "source:a action:b sink:c" {
		t.Errorf("trail = %q", got)
	}
	if got := strings.Join(ran, ""); got != "abc" {
		t.Errorf("ran = %q, want abc", got)
	}
}

func TestRunStopsOnError(t *testing.T) {
	var ran []string
	boom := errors.New("boom")

	_, err := Run(linear(&ran, boom), message.New(nil))
	if !errors.Is(err, boom) || !strings.Contains(err.Error(), "step b") {
		t.Errorf("err = %v, want wrapped boom for step b", err)
	}
	if got := strings.Join(ran, ""); got != "ab" {
		t.Errorf("ran = %q, want ab (sink must not run)", got)
	}
}
