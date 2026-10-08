package impl

import (
	"context"
	"strings"
	"testing"

	flowdef "dif/flows/definition"
	"dif/message"
	stepdef "dif/steps/definition"
)

// newLooper creates the loop step uri in a position of kind with the links.
func newLooper(kind, uri string, opts map[string]any, links ...stepdef.Link) (stepdef.Looper, error) {
	p, err := steps.Processor(&flowdef.Node{ID: "test-step", Kind: kind, URI: uri, Options: opts, Links: links, Next: make([]*flowdef.Node, len(links))})
	if err != nil {
		return nil, err
	}
	return p.(stepdef.Looper), nil
}

// runLoop runs the rounds of l as the engine does. Every link appends its
// index and the message's loop.index to the body.
func runLoop(t *testing.T, l stepdef.Looper, in message.Message) message.Message {
	t.Helper()
	prev := in
	for round := 0; round < 100; round++ {
		routes, last, err := l.Round(context.Background(), in, prev, round)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range routes {
			m := r.Message
			m[message.Body] = text(m[message.Body]) + " " + text(r.Next) + ":" + text(m[loopIndex])
			prev = m
		}
		if last {
			return prev
		}
	}
	t.Fatal("the loop did not end")
	return nil
}

func TestLoop(t *testing.T) {
	loop := stepdef.Link{Rule: "loop", Language: "simple", Expression: "${header.n}"}
	for _, tt := range []struct {
		name  string
		kind  string
		opts  map[string]any
		links []stepdef.Link
		n     string
		want  string
	}{
		{"action", stepdef.Action, nil, []stepdef.Link{loop}, "3", "in 0:0 0:1 0:2"},
		{"action copy", stepdef.Action, map[string]any{"copy": true}, []stepdef.Link{loop}, "3", "in 0:2"},
		{"router", stepdef.Router, nil, []stepdef.Link{loop, {}}, "2", "in 0:0 0:1 1:1"},
		{"router, loop link second", stepdef.Router, nil, []stepdef.Link{{}, loop}, "2", "in 1:0 1:1 0:1"},
		{"no rounds", stepdef.Router, nil, []stepdef.Link{loop, {}}, "0", "in 1:"},
		{"no rounds, action", stepdef.Action, nil, []stepdef.Link{loop}, "0", "in"},
		{"expression from options", stepdef.Action, map[string]any{"expression": "2", "language": "constant"}, []stepdef.Link{{Rule: "loop"}}, "", "in 0:0 0:1"},
		{"default once", stepdef.Action, nil, []stepdef.Link{{Rule: "loop"}}, "", "in 0:0"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			l, err := newLooper(tt.kind, "loop", tt.opts, tt.links...)
			if err != nil {
				t.Fatal(err)
			}
			in := message.New("in")
			in["n"] = tt.n
			out := runLoop(t, l, in)
			if out[message.Body] != tt.want {
				t.Errorf("body = %q, want %q", out[message.Body], tt.want)
			}
			if tt.n != "0" && in[message.Body] != "in" { // with no rounds, the message itself goes on
				t.Errorf("the entering message changed: %v", in)
			}
		})
	}

	l, _ := newLooper(stepdef.Action, "loop", nil, loop)
	m := message.New("in")
	m["n"] = "2"
	routes, _, _ := l.Round(context.Background(), m, m, 1)
	if r := routes[0].Message; r[loopIndex] != 1 || r[loopSize] != 2 {
		t.Errorf("headers = %v, want loop.index 1 and loop.size 2", r)
	}
	m["n"] = "many"
	if _, _, err := l.Round(context.Background(), m, m, 0); err == nil || !strings.Contains(err.Error(), `number of rounds "many" is not an integer`) {
		t.Errorf("err = %v", err)
	}
}

func TestDoWhile(t *testing.T) {
	while := stepdef.Link{Rule: "dowhile", Expression: "${body} != 'in 0:0 0:1'"}
	for _, tt := range []struct {
		name  string
		kind  string
		opts  map[string]any
		links []stepdef.Link
		want  string
	}{
		{"action", stepdef.Action, nil, []stepdef.Link{while}, "in 0:0 0:1"},
		{"router", stepdef.Router, nil, []stepdef.Link{while, {}}, "in 0:0 0:1 1:1"},
		{"max loops", stepdef.Action, map[string]any{"maxLoops": "1"}, []stepdef.Link{while}, "in 0:0"},
		{"false at once", stepdef.Router, nil, []stepdef.Link{{Rule: "dowhile", Expression: "${body} == 'other'"}, {}}, "in 1:"},
		{"condition from options", stepdef.Action, map[string]any{"expression": "${body} == 'in'", "copy": true}, []stepdef.Link{{Rule: "dowhile"}}, "in 0:0"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			l, err := newLooper(tt.kind, "dowhile", tt.opts, tt.links...)
			if err != nil {
				t.Fatal(err)
			}
			if out := runLoop(t, l, message.New("in")); out[message.Body] != tt.want {
				t.Errorf("body = %q, want %q", out[message.Body], tt.want)
			}
		})
	}
}

func TestLoopInvalid(t *testing.T) {
	for _, tt := range []struct {
		uri   string
		opts  map[string]any
		links []stepdef.Link
		want  string
	}{
		{"loop", nil, []stepdef.Link{{}, {}}, "needs a link with rule loop and at most one other link, has 2 links"},
		{"loop", nil, []stepdef.Link{{Rule: "loop"}, {Rule: "loop"}}, "outbound links 0 and 1 are both loop links"},
		{"loop", nil, []stepdef.Link{{Rule: "loop", Language: "xpath", Expression: "count(//a)"}}, `language "xpath" is not supported for the number of rounds`},
		{"loop", map[string]any{"language": "groovy"}, []stepdef.Link{{Rule: "loop"}}, "option language"},
		{"loop", nil, []stepdef.Link{{Rule: "loop", Expression: "${exchangeId}"}}, "unsupported simple expression"},
		{"dowhile", nil, []stepdef.Link{{Rule: "dowhile"}}, "no condition"},
		{"dowhile", nil, []stepdef.Link{{Rule: "dowhile", Language: "groovy", Expression: "true"}}, `language "groovy" is not supported`},
		{"dowhile", map[string]any{"maxLoops": 0.0}, []stepdef.Link{{Rule: "dowhile", Expression: "true"}}, "0 is less than 1"},
	} {
		if _, err := newLooper(stepdef.Router, tt.uri, tt.opts, tt.links...); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s %v: err = %v, want containing %q", tt.uri, tt.links, err, tt.want)
		}
	}
}

func TestIf(t *testing.T) {
	links := []stepdef.Link{ // as in testdata/examples/experimental/ifelse.json
		{Rule: "if", Language: "simple", Expression: "${body} contains 'Test'"},
		{},
	}
	for body, want := range map[string]string{"a Test": "0:a Test", "other": "1:other"} {
		if got := summary(route(t, "if", nil, links, message.New(body))); got != want {
			t.Errorf("body %q: routes = %s, want %s", body, got, want)
		}
	}
	if got := route(t, "if", nil, links[:1], message.New("other")); len(got) != 0 {
		t.Errorf("as an action: routes = %s, want none", summary(got))
	}
}
