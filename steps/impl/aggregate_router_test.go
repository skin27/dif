package impl

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"dif/message"
	stepdef "dif/steps/definition"
)

// part returns a split part: body with the split headers.
func part(body string, i, n int) message.Message {
	m := message.New(body)
	m[SplitIndex], m[SplitSize], m[SplitComplete] = i, n, i == n-1
	return m
}

// feed passes the messages to the aggregator and returns, per message, the body
// of the aggregate it released ("-" for none). A message that comes in carries
// on as it came, so the aggregate is a detached route.
func feed(t *testing.T, r stepdef.RouterProcessor, ms ...message.Message) []string {
	t.Helper()
	var out []string
	for _, m := range ms {
		routes, err := r.Route(context.Background(), m)
		if err != nil {
			t.Fatal(err)
		}
		switch len(routes) {
		case 0:
			out = append(out, "-")
		case 1:
			if !routes[0].Detached || routes[0].Next != 0 {
				t.Fatalf("route = %+v, want a detached one along the link", routes[0])
			}
			out = append(out, routes[0].Message[message.Body].(string))
		default:
			t.Fatalf("routes = %+v", routes)
		}
	}
	return out
}

func newAggregator(t *testing.T, opts map[string]any) stepdef.RouterProcessor {
	t.Helper()
	r, err := newRouter(stepdef.Action, "aggregate", opts, stepdef.Link{})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestAggregateSplit(t *testing.T) {
	r := newAggregator(t, nil)
	last := part(`<person>C</person>`, 2, 3)
	got := feed(t, r, part(`<?xml version="1.0"?><person>A</person>`, 0, 3), part("<person>B</person>", 1, 3), last)
	if want := "- - " + aggregateStart + "<person>A</person><person>B</person><person>C</person></Aggregated>"; strings.Join(got, " ") != want {
		t.Errorf("passed on %q, want %q", got, want)
	}
	if last[message.Body] != "<person>C</person>" || last[SplitIndex] != 2 || last[SplitComplete] != true {
		t.Errorf("the message that came in changed: %v", last)
	}

	// The aggregate is a message of its own, made from the last, without the split headers.
	m := part("<a/>", 0, 1)
	routes, _ := r.Route(context.Background(), m)
	agg := routes[0].Message
	if agg[SplitIndex] != nil || agg[SplitSize] != nil || agg[SplitComplete] != nil || agg[aggregateTypeKey] != nil {
		t.Errorf("aggregate = %v, want no split headers", agg)
	}
	if agg[message.MessageID] == m[message.MessageID] || agg[message.CausationID] != m[message.MessageID] || agg[message.CorrelationID] != m[message.CorrelationID] {
		t.Errorf("aggregate = %v, want a new message caused by the last, in its correlation", agg)
	}

	// A split that failed halfway leaves parts; the next split starts afresh.
	got = feed(t, r, part("<a/>", 0, 3), part("<b/>", 1, 3), part("<c/>", 0, 2), part("<d/>", 1, 2))
	if want := "- - - " + aggregateStart + "<c/><d/></Aggregated>"; strings.Join(got, " ") != want {
		t.Errorf("passed on %q, want %q", got, want)
	}
}

func TestAggregateSizeAndJSON(t *testing.T) {
	r := newAggregator(t, map[string]any{"aggregateType": "application/json", "completionSize": "2"})
	got := feed(t, r, message.New(`{"a":1}`), message.New(`"x"`), message.New("[]"), message.New("2"))
	if want := `- [{"a":1},"x"] - [[],2]`; strings.Join(got, " ") != want {
		t.Errorf("passed on %q, want %q", got, want)
	}
}

func TestAggregateMarksTheType(t *testing.T) {
	r := newAggregator(t, map[string]any{"aggregateType": "json", "completionSize": 5})
	m := message.New("1")
	if _, err := r.Route(context.Background(), m); err != nil || m[aggregateTypeKey] != "json" {
		t.Errorf("message = %v, %v, want the type for a splitandaggregate around it", m, err)
	}
}

func TestAggregateInvalid(t *testing.T) {
	// A part that cannot be aggregated fails when it comes in, not when the group is complete.
	r := newAggregator(t, nil)
	if _, err := r.Route(context.Background(), part("<a/>", 0, 2)); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Route(context.Background(), part("not xml", 1, 2)); err == nil || !strings.Contains(err.Error(), "aggregate: body is not XML") {
		t.Errorf("err = %v", err)
	}
	if _, err := r.Route(context.Background(), message.New("")); err == nil || !strings.Contains(err.Error(), "aggregate: body is not XML") {
		t.Errorf("an empty body: err = %v", err)
	}
	r = newAggregator(t, map[string]any{"aggregateType": "json", "completionInterval": 1000})
	if _, err := r.Route(context.Background(), message.New("<a/>")); err == nil || !strings.Contains(err.Error(), "aggregate: body is not JSON") {
		t.Errorf("err = %v", err)
	}

	for _, tt := range []struct {
		opts  map[string]any
		links []stepdef.Link
		want  string
	}{
		{map[string]any{"completionTimeout": -1}, []stepdef.Link{{}}, "option completionTimeout"},
		{map[string]any{"completionInterval": "x"}, []stepdef.Link{{}}, "option completionInterval"},
		{map[string]any{"aggregateType": "csv"}, []stepdef.Link{{}}, `option aggregateType: "csv" is not one of`},
		{nil, []stepdef.Link{{}, {}}, "needs one outbound link, has 2"},
	} {
		if _, err := newRouter(stepdef.Action, "aggregate", tt.opts, tt.links...); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%v: err = %v, want containing %q", tt.opts, err, tt.want)
		}
	}
}

// released runs the aggregator's timers until the test ends and returns the
// bodies of the messages it releases.
func released(t *testing.T, r stepdef.RouterProcessor) <-chan string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	out := make(chan string, 10)
	done := make(chan struct{})
	go func() {
		defer close(done)
		err := r.(stepdef.Releaser).Release(ctx, func(m message.Message) error {
			out <- m[message.Body].(string)
			return nil
		})
		if err != nil {
			t.Errorf("Release: %v", err)
		}
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("Release did not return when its context was done")
		}
	})
	return out
}

func nothingWithin(t *testing.T, out <-chan string, d time.Duration) {
	t.Helper()
	select {
	case got := <-out:
		t.Fatalf("released %q too early", got)
	case <-time.After(d):
	}
}

func releasedWithin(t *testing.T, out <-chan string) string {
	t.Helper()
	select {
	case got := <-out:
		return got
	case <-time.After(5 * time.Second):
		t.Fatal("nothing released")
		return ""
	}
}

func TestAggregateCompletionTimeout(t *testing.T) {
	r := newAggregator(t, map[string]any{"aggregateType": "json", "completionSize": 10, "completionTimeout": 400})
	out := released(t, r)
	nothingWithin(t, out, 500*time.Millisecond) // no group, nothing to complete

	// Every message waits for the next one for the whole timeout.
	for _, b := range []string{"1", "2", "3"} {
		got := feed(t, r, message.New(b))
		if got[0] != "-" {
			t.Fatalf("passed on %q", got)
		}
		nothingWithin(t, out, 100*time.Millisecond)
	}
	if got := releasedWithin(t, out); got != "[1,2,3]" {
		t.Errorf("released %q", got)
	}
	nothingWithin(t, out, 600*time.Millisecond) // once

	// A group the size completes is not completed again by the timeout.
	r = newAggregator(t, map[string]any{"aggregateType": "json", "completionSize": 2, "completionTimeout": 200})
	out = released(t, r)
	if got := feed(t, r, message.New("1"), message.New("2")); got[1] != "[1,2]" {
		t.Fatalf("passed on %q", got)
	}
	nothingWithin(t, out, 500*time.Millisecond)
}

func TestAggregateCompletionInterval(t *testing.T) {
	r := newAggregator(t, map[string]any{"aggregateType": "xml", "completionInterval": 300})
	out := released(t, r)
	nothingWithin(t, out, 350*time.Millisecond) // an empty group is not released

	for _, m := range []message.Message{message.New("<a/>"), message.New("<b/>")} {
		if got := feed(t, r, m); got[0] != "-" {
			t.Fatalf("passed on %q", got)
		}
	}
	if got := releasedWithin(t, out); got != aggregateStart+"<a/><b/></Aggregated>" {
		t.Errorf("released %q", got)
	}
	nothingWithin(t, out, 100*time.Millisecond)

	// The group of the next interval is its own.
	feed(t, r, message.New("<c/>"))
	if got := releasedWithin(t, out); got != aggregateStart+"<c/></Aggregated>" {
		t.Errorf("released %q", got)
	}
}

func TestAggregateTimedMessageIsMadeFromTheLast(t *testing.T) {
	r := newAggregator(t, map[string]any{"aggregateType": "json", "completionInterval": 300})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	got := make(chan message.Message, 1)
	go r.(stepdef.Releaser).Release(ctx, func(m message.Message) error { got <- m; return nil })

	first, last := message.New("1"), message.New("2")
	first["h"], last["h"] = "first", "last"
	last[SplitIndex] = 1
	if _, err := r.Route(ctx, first); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Route(ctx, last); err != nil {
		t.Fatal(err)
	}
	last["h"] = "changed after it came in" // the flow goes on with it; the group has its own copy
	select {
	case m := <-got:
		if m["h"] != "last" || m[SplitIndex] != nil || m[message.CausationID] != last[message.MessageID] || m[message.Body] != "[1,2]" {
			t.Errorf("released %v", m)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("nothing released")
	}
}

func TestAggregateReleaseWithoutTimersReturns(t *testing.T) {
	r := newAggregator(t, nil)
	if err := r.(stepdef.Releaser).Release(context.Background(), nil); err != nil {
		t.Errorf("Release: %v", err)
	}
}

func TestSplitAndAggregate(t *testing.T) {
	// As in examples/splitAndAggregate.json: the expression is on the split link.
	links := []stepdef.Link{{}, {Rule: "split", Expression: "/persons/*[local-name() = 'person']"}}
	r, err := newRouter(stepdef.Router, "splitandaggregate", map[string]any{"language": "xpath", "aggregateType": "xml", "exchangePattern": "InOut"}, links...)
	if err != nil {
		t.Fatal(err)
	}
	m := message.New(persons)
	m["h"] = "v"
	routes, err := r.Route(context.Background(), m)
	if err != nil {
		t.Fatal(err)
	}
	if got := summary(routes); got != `1:<person id="1"><name>John Doe</name></person> 1:<p:person xmlns:p="urn:p"><name>Jane <b>Doe</b></name></p:person>` {
		t.Errorf("routes = %s, want the parts only", got)
	}

	outcomes := []stepdef.Outcome{{Message: message.New("<x>1</x>")}, {Message: message.New("<x>2</x>")}}
	next, err := r.(stepdef.Gatherer).Gather(context.Background(), m, outcomes)
	if err != nil {
		t.Fatal(err)
	}
	if len(next) != 1 || next[0].Next != 0 || !sameMessage(next[0].Message, m) || m[message.Body] != aggregateStart+"<x>1</x><x>2</x></Aggregated>" || m["h"] != "v" {
		t.Errorf("routes = %+v, want the message with the aggregate along the main link", next)
	}

	// An aggregate in the split route decides the type.
	j := message.New("1")
	j[aggregateTypeKey] = "json"
	next, err = r.(stepdef.Gatherer).Gather(context.Background(), message.New("x"), []stepdef.Outcome{{Message: j}, {Message: message.New("2")}})
	if err != nil || next[0].Message[message.Body] != "[1,2]" {
		t.Errorf("routes = %+v, %v, want the parts as JSON, as the aggregate in the route says", next, err)
	}

	boom := errors.New("boom")
	if _, err := r.(stepdef.Gatherer).Gather(context.Background(), m, []stepdef.Outcome{{Message: message.New("<x/>")}, {Err: boom}}); err != boom {
		t.Errorf("err = %v, want the failed part's error as is", err)
	}

	r, _ = newRouter(stepdef.Router, "splitandaggregate", map[string]any{"language": "jsonpath", "expression": "$.store.book[*].author", "aggregateType": "json"}, stepdef.Link{Rule: "split"}, stepdef.Link{})
	routes, _ = r.Route(context.Background(), message.New(store))
	if got := summary(routes); got != "0:Nigel Rees 0:Evelyn Waugh" {
		t.Errorf("routes = %s", got)
	}

	for _, tt := range []struct {
		opts  map[string]any
		links []stepdef.Link
		want  string
	}{
		{nil, []stepdef.Link{{}, {Rule: "split"}}, "needs an expression: the option expression or the split link's"},
		{map[string]any{"expression": "/a"}, []stepdef.Link{{Rule: "split"}}, "needs an outbound link without a rule for the aggregate"},
		{map[string]any{"expression": "/a["}, []stepdef.Link{{}, {Rule: "split"}}, `option expression: xpath "/a["`},
	} {
		if _, err := newRouter(stepdef.Router, "splitandaggregate", tt.opts, tt.links...); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%v %v: err = %v, want containing %q", tt.opts, tt.links, err, tt.want)
		}
	}
}
