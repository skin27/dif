package engine

import (
	"bytes"
	"context"
	"errors"
	"log"
	"strings"
	"testing"

	flowdef "dif/flows/definition"
	"dif/message"
	stepdef "dif/steps/definition"
)

// routerStub returns the routes route gives for the message.
type routerStub struct {
	route func(m message.Message) []stepdef.Route
}

func (r routerStub) Route(_ context.Context, m message.Message) ([]stepdef.Route, error) {
	return r.route(m), nil
}

// tagger is an action that appends its id to the body.
type tagger struct {
	ran *[]string
	id  string
	err error
}

func (a tagger) Process(_ context.Context, m message.Message) (message.Message, error) {
	*a.ran = append(*a.ran, a.id)
	m[message.Body] = m[message.Body].(string) + a.id
	return m, a.err
}

// routed builds source a -> router r, whose links lead to x -> y and to z;
// x fails with xErr.
func routed(ran *[]string, xErr error, route func(m message.Message) []stepdef.Route) *flowdef.Flow {
	y := &flowdef.Node{ID: "y", Kind: flowdef.Sink, Processor: sinkStub{stub{ran, "y", nil}}}
	x := &flowdef.Node{ID: "x", Kind: flowdef.Action, Processor: tagger{ran, "x", xErr}, Next: []*flowdef.Node{y}}
	z := &flowdef.Node{ID: "z", Kind: flowdef.Action, Processor: tagger{ran, "z", nil}}
	r := &flowdef.Node{ID: "r", Kind: flowdef.Router, Processor: routerStub{route}, Next: []*flowdef.Node{x, z}}
	return &flowdef.Flow{Source: &flowdef.Node{ID: "a", Kind: flowdef.Source, Next: []*flowdef.Node{r}}}
}

func TestRunRoutes(t *testing.T) {
	tests := []struct {
		name      string
		route     func(m message.Message) []stepdef.Route
		ran, body string
		trail     string
	}{
		{"both, last is the outcome", func(m message.Message) []stepdef.Route {
			return []stepdef.Route{{Next: 0, Message: m.Copy()}, {Next: 1, Message: m}}
		}, "x y z", "-z", "source:a router:r action:x sink:y action:z"},
		{"detached route is not the outcome", func(m message.Message) []stepdef.Route {
			return []stepdef.Route{{Next: 1, Message: m}, {Next: 0, Message: m.Copy(), Detached: true}}
		}, "z x y", "-z", "source:a router:r action:z action:x sink:y"},
		{"one route", func(m message.Message) []stepdef.Route {
			return []stepdef.Route{{Next: 0, Message: m}}
		}, "x y", "-x", "source:a router:r action:x sink:y"},
		{"no routes stops the message", func(message.Message) []stepdef.Route { return nil }, "", "-", "source:a router:r"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var ran []string
			res, err := Run(context.Background(), routed(&ran, nil, tt.route), message.New("-"))
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Join(ran, " "); got != tt.ran {
				t.Errorf("ran = %q, want %q", got, tt.ran)
			}
			if res.Message[message.Body] != tt.body {
				t.Errorf("body = %v, want %q", res.Message[message.Body], tt.body)
			}
			if got := strings.Join(res.Trail, " "); got != tt.trail {
				t.Errorf("trail = %q, want %q", got, tt.trail)
			}
		})
	}
}

func TestRunRouteFails(t *testing.T) {
	boom := errors.New("boom")
	both := func(detached bool) func(m message.Message) []stepdef.Route {
		return func(m message.Message) []stepdef.Route {
			return []stepdef.Route{{Next: 0, Message: m.Copy(), Detached: detached}, {Next: 1, Message: m}}
		}
	}

	var ran []string
	if _, err := Run(context.Background(), routed(&ran, boom, both(false)), message.New("-")); !errors.Is(err, boom) || !strings.Contains(err.Error(), "step x") {
		t.Errorf("err = %v, want boom from step x", err)
	}
	if got := strings.Join(ran, " "); got != "x" {
		t.Errorf("ran = %q, want x: a failed route ends the message", got)
	}

	var logged bytes.Buffer
	ctx := stepdef.WithLogger(context.Background(), log.New(&logged, "", 0))
	ran = nil
	res, err := Run(ctx, routed(&ran, boom, both(true)), message.New("-"))
	if err != nil {
		t.Fatalf("detached route failed the message: %v", err)
	}
	if res.Message[message.Body] != "-z" || strings.Join(ran, " ") != "x z" {
		t.Errorf("body = %v, ran = %v; want -z after x z", res.Message[message.Body], ran)
	}
	if want := "step r: detached route to link 0 failed: step x: boom"; !strings.Contains(logged.String(), want) {
		t.Errorf("log = %q, want containing %q", logged.String(), want)
	}
}

func TestRunInvalidRoutes(t *testing.T) {
	for _, tt := range []struct {
		route stepdef.Route
		want  string
	}{
		{stepdef.Route{Next: 2, Message: message.New("-")}, "step r: route to link 2, but the step has 2"},
		{stepdef.Route{Next: -1, Message: message.New("-")}, "step r: route to link -1"},
		{stepdef.Route{Next: 0}, "step r: route to link 0 has no message"},
	} {
		var ran []string
		f := routed(&ran, nil, func(message.Message) []stepdef.Route { return []stepdef.Route{tt.route} })
		if _, err := Run(context.Background(), f, message.New("-")); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("err = %v, want containing %q", err, tt.want)
		}
	}
}

func TestRunOnlyRoutersBranch(t *testing.T) {
	var ran []string
	f := routed(&ran, nil, nil)
	f.Source.Next[0].Processor = stub{&ran, "r", nil} // an action with two outbound links
	if _, err := Run(context.Background(), f, message.New("-")); err == nil || !strings.Contains(err.Error(), "step r: only a router can have more than one outbound link") {
		t.Errorf("err = %v", err)
	}
}

// looperStub runs link 0 (x -> y) rounds times with the message of the round
// before, then link 1 (z); it fails in round failAt (-1 never).
type looperStub struct {
	rounds, failAt int
	ins            *[]string
}

func (l looperStub) Round(_ context.Context, in, prev message.Message, round int) ([]stepdef.Route, bool, error) {
	*l.ins = append(*l.ins, in[message.Body].(string))
	if round == l.failAt {
		return nil, false, errors.New("round failed")
	}
	if round < l.rounds {
		m := prev
		if round == 0 {
			m = in.Copy()
		}
		return []stepdef.Route{{Next: 0, Message: m}}, false, nil
	}
	return []stepdef.Route{{Next: 1, Message: prev}}, true, nil
}

func TestRunLooper(t *testing.T) {
	var ran, ins []string
	f := routed(&ran, nil, nil)
	r := f.Source.Next[0]
	r.Processor = looperStub{rounds: 2, failAt: -1, ins: &ins}
	res, err := Run(context.Background(), f, message.New("-"))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(ran, " "); got != "x y x y z" {
		t.Errorf("ran = %q, want two rounds of x y, then z", got)
	}
	if res.Message[message.Body] != "-xxz" {
		t.Errorf("body = %v, want -xxz: every round gets the message of the round before", res.Message[message.Body])
	}
	if got := strings.Join(ins, " "); got != "- - -" {
		t.Errorf("in per round = %q, want the entering message every time", got)
	}
	if got := strings.Join(res.Trail, " "); got != "source:a router:r action:x sink:y action:x sink:y action:z" {
		t.Errorf("trail = %q", got)
	}

	ran, ins = nil, nil
	r.Processor = looperStub{rounds: 3, failAt: 1, ins: &ins}
	_, err = Run(context.Background(), f, message.New("-"))
	var se *StepError
	if !errors.As(err, &se) || se.Step != "r" || se.Message[message.Body] != "-x" || !strings.Contains(err.Error(), "round failed") {
		t.Errorf("err = %v, want a step error of r with the message of round 0", err)
	}
}
