package impl

import (
	"context"
	"errors"
	"strings"
	"testing"

	"dif/message"
	stepdef "dif/steps/definition"
)

// part returns a split part: body with the split headers.
func part(body string, i, n int) message.Message {
	m := message.New(body)
	m[SplitIndex], m[SplitSize], m[SplitComplete] = i, n, i == n-1
	return m
}

// feed passes the messages to the aggregator and returns, per message, the
// body it passed on ("-" for none).
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
	if want := "- - <Aggregated><person>A</person><person>B</person><person>C</person></Aggregated>"; strings.Join(got, " ") != want {
		t.Errorf("passed on %q, want %q", got, want)
	}
	if last[SplitIndex] != nil || last[SplitSize] != nil || last[SplitComplete] != nil {
		t.Errorf("split headers kept: %v", last)
	}

	// A split that failed halfway leaves parts; the next split starts afresh.
	got = feed(t, r, part("<a/>", 0, 3), part("<b/>", 1, 3), part("<c/>", 0, 2), part("<d/>", 1, 2))
	if want := "- - - <Aggregated><c/><d/></Aggregated>"; strings.Join(got, " ") != want {
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

func TestAggregateInvalid(t *testing.T) {
	r := newAggregator(t, nil)
	if _, err := r.Route(context.Background(), part("<a/>", 0, 2)); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Route(context.Background(), part("not xml", 1, 2)); err == nil || !strings.Contains(err.Error(), "aggregate part 2: body is not XML") {
		t.Errorf("err = %v", err)
	}
	r = newAggregator(t, map[string]any{"aggregateType": "json", "completionSize": 1})
	if _, err := r.Route(context.Background(), message.New("<a/>")); err == nil || !strings.Contains(err.Error(), "aggregate part 1: body is not JSON") {
		t.Errorf("err = %v", err)
	}

	for _, tt := range []struct {
		opts  map[string]any
		links []stepdef.Link
		want  string
	}{
		{map[string]any{"completionTimeout": 5000}, []stepdef.Link{{}}, "option completionTimeout: completing by time is not supported yet"},
		{map[string]any{"completionInterval": "100"}, []stepdef.Link{{}}, "option completionInterval: completing by time is not supported yet"},
		{map[string]any{"aggregateType": "csv"}, []stepdef.Link{{}}, `option aggregateType: "csv" is not one of`},
		{nil, []stepdef.Link{{}, {}}, "needs one outbound link, has 2"},
	} {
		if _, err := newRouter(stepdef.Action, "aggregate", tt.opts, tt.links...); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%v: err = %v, want containing %q", tt.opts, err, tt.want)
		}
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
	if len(next) != 1 || next[0].Next != 0 || !sameMessage(next[0].Message, m) || m[message.Body] != "<Aggregated><x>1</x><x>2</x></Aggregated>" || m["h"] != "v" {
		t.Errorf("routes = %+v, want the message with the aggregate along the main link", next)
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
