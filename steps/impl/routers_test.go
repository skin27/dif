package impl

import (
	"context"
	"strings"
	"testing"

	"dif/message"
	stepdef "dif/steps/definition"
)

// summary renders routes as "next:body" (with "~" for a detached route),
// space-separated.
func summary(routes []stepdef.Route) string {
	var s []string
	for _, r := range routes {
		d := ""
		if r.Detached {
			d = "~"
		}
		s = append(s, d+string(rune('0'+r.Next))+":"+text(r.Message[message.Body]))
	}
	return strings.Join(s, " ")
}

// sameMessage reports whether a and b are the same map, not just equal.
func sameMessage(a, b message.Message) bool {
	a["same?"] = true
	defer delete(a, "same?")
	return b["same?"] == true
}

func TestWireTap(t *testing.T) {
	m := message.New("x")
	routes := route(t, "wiretap", nil, []stepdef.Link{{}, {Rule: "wiretap"}}, m)
	if got := summary(routes); got != "~1:x 0:x" {
		t.Errorf("routes = %s, want the tap (detached) then the main link", got)
	}
	if sameMessage(routes[0].Message, m) || !sameMessage(routes[1].Message, m) {
		t.Error("the tap must get a copy and the main link the message")
	}

	for _, links := range [][]stepdef.Link{{{}}, {{Rule: "wiretap"}}, {{}, {}, {Rule: "wiretap"}}, {{Rule: "wiretap"}, {Rule: "wiretap"}, {}}} {
		if _, err := newRouter(stepdef.Router, "wiretap", nil, links...); err == nil || !strings.Contains(err.Error(), "needs one outbound link with rule wiretap and one without") {
			t.Errorf("links %v: err = %v", links, err)
		}
	}
}

func TestRecipient(t *testing.T) {
	m := message.New("x")
	routes := route(t, "recipient", nil, []stepdef.Link{{}, {}, {}}, m)
	if got := summary(routes); got != "0:x 1:x 2:x" {
		t.Errorf("routes = %s", got)
	}
	for i, r := range routes {
		if sameMessage(r.Message, m) {
			t.Errorf("route %d shares the message", i)
		}
	}
}

func TestContentRouter(t *testing.T) {
	links := []stepdef.Link{ // as in examples/contentrouter.json
		{},
		{Rule: "234", Language: "jsonpath", Expression: "$.store.book[*].author"},
		{Rule: "123", Language: "simple", Expression: "${bodyAs(String)} == '123'"},
		{Rule: "x", Expression: "${header.kind} == 'x'"}, // language defaults to simple
	}
	for body, want := range map[string]string{
		"123":   "2:123",
		store:   "1:" + store,
		"other": "0:other",
	} {
		if got := summary(route(t, "content", nil, links, message.New(body))); got != want {
			t.Errorf("body %.10q: routes = %.20s, want %.20s", body, got, want)
		}
	}
	m := message.New("123")
	m["kind"] = "x"
	if got := summary(route(t, "content", nil, links, m)); got != "2:123" {
		t.Errorf("routes = %s, want the first link whose condition holds", got)
	}

	if got := route(t, "content", nil, links[1:], message.New("other")); len(got) != 0 {
		t.Errorf("routes = %s, want none without an otherwise link", summary(got))
	}

	for _, tt := range []struct {
		links []stepdef.Link
		want  string
	}{
		{[]stepdef.Link{{}, {Rule: "b"}}, "outbound links 0 and 1 both have no condition"},
		{[]stepdef.Link{{Rule: "r", Language: "groovy", Expression: "true"}}, `outbound link 0 (rule r): language "groovy" is not supported`},
		{[]stepdef.Link{{Rule: "r", Language: "xpath", Expression: "//a["}}, `outbound link 0 (rule r): xpath "//a["`},
	} {
		if _, err := newRouter(stepdef.Router, "content", nil, tt.links...); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("err = %v, want containing %q", err, tt.want)
		}
	}
}

func TestFilter(t *testing.T) {
	r, err := newRouter(stepdef.Action, "filter", map[string]any{"language": "xpath", "expression": "/persons/person/name= 'John Doe'"}, stepdef.Link{})
	if err != nil {
		t.Fatal(err)
	}
	for body, want := range map[string]string{persons: "0:" + persons, "<persons/>": "", "not xml": ""} {
		routes, err := r.Route(context.Background(), message.New(body))
		if err != nil {
			t.Fatal(err)
		}
		if got := summary(routes); got != want {
			t.Errorf("body %.10q: routes = %.20q, want %.20q", body, got, want)
		}
	}

	if got := summary(route(t, "filter", map[string]any{"expression": "${header.ok} == 'yes'"}, []stepdef.Link{{}}, message.Message{"ok": "yes", "body": "b"})); got != "0:b" {
		t.Errorf("simple filter: routes = %s", got)
	}

	wantInvalid(t, stepdef.Action, "filter", nil, "missing required option expression")
	wantInvalid(t, stepdef.Action, "filter", map[string]any{"language": "groovy", "expression": "true"}, `option language: "groovy" is not one of`)
	if _, err := newRouter(stepdef.Action, "filter", map[string]any{"language": "xpath", "expression": "//a["}, stepdef.Link{}); err == nil || !strings.Contains(err.Error(), `option expression: xpath "//a["`) {
		t.Errorf("err = %v", err)
	}
	if _, err := newRouter(stepdef.Router, "filter", map[string]any{"expression": "true"}, stepdef.Link{}, stepdef.Link{}); err == nil || !strings.Contains(err.Error(), "needs one outbound link, has 2") {
		t.Errorf("err = %v", err)
	}
}

func TestSplitXML(t *testing.T) {
	m := message.New(persons)
	m["h"] = "v"
	links := []stepdef.Link{{}, {Rule: "split", Language: "xpath", Expression: "/persons/*[local-name() = 'person']"}} // as in examples/split.json
	routes := route(t, "split", map[string]any{"expression": "/persons/*[local-name() = 'person']", "exchangePattern": "InOnly"}, links, m)
	if len(routes) != 3 {
		t.Fatalf("routes = %s, want 2 parts and the message", summary(routes))
	}
	for i, want := range []string{`<person id="1"><name>John Doe</name></person>`, `<p:person xmlns:p="urn:p"><name>Jane <b>Doe</b></name></p:person>`} {
		p := routes[i].Message
		if routes[i].Next != 1 || p[message.Body] != want || p["h"] != "v" || p[SplitIndex] != i || p[SplitSize] != 2 || p[SplitComplete] != (i == 1) {
			t.Errorf("part %d = %+v", i, routes[i])
		}
	}
	if routes[2].Next != 0 || !sameMessage(routes[2].Message, m) || m[SplitIndex] != nil {
		t.Errorf("last route = %+v, want the unchanged message on link 0", routes[2])
	}
}

func TestSplitJSONAndActionPosition(t *testing.T) {
	r, err := newRouter(stepdef.Action, "split", map[string]any{"language": "jsonpath", "expression": "$.store.book"}, stepdef.Link{})
	if err != nil {
		t.Fatal(err)
	}
	routes, err := r.Route(context.Background(), message.New(store))
	if err != nil {
		t.Fatal(err)
	}
	want := `0:{"author":"Nigel Rees","price":8.95,"title":"Sayings"} 0:{"author":"Evelyn Waugh","isbn":null,"price":12.99,"title":"Sword"}`
	if got := summary(routes); got != want {
		t.Errorf("routes = %s\nwant     %s", got, want)
	}

	got := summary(route(t, "split", map[string]any{"language": "jsonpath", "expression": "$.store.book[*].author"}, []stepdef.Link{{Rule: "split"}}, message.New(store)))
	if got != "0:Nigel Rees 0:Evelyn Waugh" {
		t.Errorf("routes = %s", got)
	}
	if got := route(t, "split", map[string]any{"expression": "/persons/none"}, []stepdef.Link{{Rule: "split"}}, message.New(persons)); len(got) != 0 {
		t.Errorf("routes = %s, want none", summary(got))
	}
}

func TestSplitInvalid(t *testing.T) {
	r, _ := newRouter(stepdef.Router, "split", map[string]any{"expression": "/a"}, stepdef.Link{Rule: "split"})
	if _, err := r.Route(context.Background(), message.New("not xml")); err == nil || !strings.Contains(err.Error(), "body is not XML") {
		t.Errorf("err = %v", err)
	}
	r, _ = newRouter(stepdef.Router, "split", map[string]any{"language": "jsonpath", "expression": "$.a"}, stepdef.Link{Rule: "split"})
	if _, err := r.Route(context.Background(), message.New("not json")); err == nil || !strings.Contains(err.Error(), "body is not JSON") {
		t.Errorf("err = %v", err)
	}

	for _, tt := range []struct {
		opts  map[string]any
		links []stepdef.Link
		want  string
	}{
		{map[string]any{"expression": "/a"}, []stepdef.Link{{}, {}}, "needs one outbound link with rule split and at most one other"},
		{map[string]any{"expression": "/a"}, []stepdef.Link{}, "needs an outbound link with rule split"},
		{map[string]any{"expression": "/a"}, []stepdef.Link{{Rule: "split"}, {Rule: "split"}}, "needs one outbound link with rule split and at most one other"},
		{map[string]any{"expression": "/a"}, []stepdef.Link{{Rule: "split"}, {}, {}}, "needs one outbound link with rule split and at most one other"},
		{map[string]any{"expression": "//a["}, []stepdef.Link{{Rule: "split"}}, `option expression: xpath "//a["`},
		{map[string]any{"language": "simple", "expression": "${body}"}, []stepdef.Link{{Rule: "split"}}, `option language: "simple" is not one of`},
		{nil, []stepdef.Link{{Rule: "split"}}, "missing required option expression"},
	} {
		if _, err := newRouter(stepdef.Router, "split", tt.opts, tt.links...); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%v %v: err = %v, want containing %q", tt.opts, tt.links, err, tt.want)
		}
	}
}

// TestContentRouterAcceptsDesignerOptions checks the options that the designer
// writes on a content router: the conditions are the ones on the links, so the
// step's expression changes nothing.
func TestContentRouterAcceptsDesignerOptions(t *testing.T) {
	opts := map[string]any{"expression": "${header.config}", "namespace": "urn:x", "exchangePattern": "InOut"}
	links := []stepdef.Link{{}, {Expression: "${header.config} == 'A'", Language: "simple"}}
	for config, want := range map[string]int{"A": 1, "B": 0} {
		m := message.New("x")
		m["config"] = config
		routes := route(t, "content", opts, links, m)
		if len(routes) != 1 || routes[0].Next != want {
			t.Errorf("config %s: routes = %v, want link %d", config, routes, want)
		}
	}
}
