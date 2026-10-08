package impl

import (
	"strings"
	"testing"

	"dif/message"
	stepdef "dif/steps/definition"
)

// splitBodies are the bodies of the parts a split sends along its split link.
func splitBodies(t *testing.T, opts map[string]any, links []stepdef.Link, m message.Message) []string {
	t.Helper()
	var bodies []string
	for _, r := range route(t, "split", opts, links, m) {
		if r.Message[SplitIndex] != nil {
			bodies = append(bodies, text(r.Message[message.Body]))
		}
	}
	return bodies
}

func TestSplitLanguages(t *testing.T) {
	links := []stepdef.Link{{Rule: "split"}, {}}
	for _, tt := range []struct {
		lang, expr, body string
		want             string
	}{
		{"tokenize", "TOKEN", "TextBeforeTOKENTextAfter", "TextBefore|TextAfter"},
		{"tokenize", "##", "[1]\n##\n[2,3]\n##\n", "[1]|[2,3]"}, // trimmed; no empty part
		{"xtokenize", "//product", `<l><product>a</product><x><product>b</product></x></l>`, "<product>a</product>|<product>b</product>"},
		{"xtokenize", "veld", `<l><veld>a</veld><veld>b</veld><other/></l>`, "<veld>a</veld>|<veld>b</veld>"},
		{"xtokenize", "/l/veld", `<l><veld>a</veld><x><veld>b</veld></x></l>`, "<veld>a</veld>"},
		{"simple", "${body.split(',')}", "a,b,c", "a|b|c"},
		{"simple", "${body}", "a,b,,c,", "a|b|c"},
		{"simple", "${header.list}", "x", "1|2"},
		{"simple", "${header.missing}", "a,b", ""}, // nothing to split
		{"jsonpath", "$.film", `{"film":[{"t":"a"},{"t":"b"}]}`, `{"t":"a"}|{"t":"b"}`},
		{"jsonpath", "$[*]", `["x","y"]`, "x|y"},
		{"jsonpath", "$.store.book[?(@.price < 10)].title", `{"store":{"book":[{"title":"A","price":5},{"title":"B","price":50}]}}`, "A"},
		{"jsonpath", "${header.expression}", `{"a":[1,2]}`, "1|2"},
		{"xpath", "${header.xp}", `<r><i>1</i><i>2</i></r>`, "<i>1</i>|<i>2</i>"},
		{"xpath", "//i[. > 1]", `<r><i>1</i><i>2</i><i>3</i></r>`, "<i>2</i>|<i>3</i>"},
	} {
		m := message.New(tt.body)
		m["list"] = "1,2"
		m["expression"] = "$.a"
		m["xp"] = "//i"
		got := strings.Join(splitBodies(t, map[string]any{"language": tt.lang, "expression": tt.expr}, links, m), "|")
		if got != tt.want {
			t.Errorf("%s %s on %.30q = %q, want %q", tt.lang, tt.expr, tt.body, got, tt.want)
		}
	}

	if _, err := newRouter(stepdef.Router, "split", map[string]any{"language": "tokenize", "expression": ""}, links...); err == nil || !strings.Contains(err.Error(), "tokenize needs the text to split at") {
		t.Errorf("err = %v", err)
	}
}

func TestSplitExpressionFromHeaderIsPerMessage(t *testing.T) {
	links := []stepdef.Link{{Rule: "split"}, {}}
	opts := map[string]any{"language": "jsonpath", "expression": "${header.expression}"}
	r, err := newRouter(stepdef.Router, "split", opts, links...)
	if err != nil {
		t.Fatal(err)
	}
	for expr, want := range map[string]int{"$.a": 2, "$.b": 3, "$.a[0]": 1} {
		m := message.New(`{"a":[1,2],"b":[3,4,5]}`)
		m["expression"] = expr
		routes, err := r.Route(t.Context(), m)
		if err != nil {
			t.Fatalf("%s: %v", expr, err)
		}
		parts := 0
		for _, rt := range routes {
			if rt.Message[SplitIndex] != nil {
				parts++
			}
		}
		if expr == "$.a[0]" && parts != 1 || expr != "$.a[0]" && parts != want {
			t.Errorf("%s: %d parts, want %d", expr, parts, want)
		}
	}
	m := message.New(`{"a":[1]}`)
	m["expression"] = "$.a["
	if _, err := r.Route(t.Context(), m); err == nil || !strings.Contains(err.Error(), `jsonpath "$.a["`) {
		t.Errorf("an invalid expression from a header: err = %v", err)
	}
}

// With nothing to split, the splitter-aggregator passes the message on.
func TestSplitAndAggregateWithoutParts(t *testing.T) {
	links := []stepdef.Link{{}, {Rule: "split", Expression: "${header.nothing}"}}
	r, err := newRouter(stepdef.Router, "splitandaggregate", map[string]any{"language": "simple", "aggregateType": "xml"}, links...)
	if err != nil {
		t.Fatal(err)
	}
	m := message.New("a,b,c")
	routes, err := r.Route(t.Context(), m)
	if err != nil || len(routes) != 0 {
		t.Fatalf("routes = %v, %v", routes, err)
	}
	next, err := r.(stepdef.Gatherer).Gather(t.Context(), m, nil)
	if err != nil || len(next) != 1 || next[0].Next != 0 || next[0].Message[message.Body] != "a,b,c" {
		t.Errorf("gather = %v, %v; want the message on the link without a rule", next, err)
	}
}

func TestJSONPathInSteps(t *testing.T) {
	const doc = `{"store":{"book":[{"author":"Nigel Rees","price":8.95},{"author":"E","price":12.99}],"bicycle":{"color":"red"}}}`
	headers := `[{"name":"author","value":"$.store.book[0].author","language":"jsonpath"},` +
		`{"name":"price","value":"$..book[?(@.price<8.99)].price","language":"jsonpath"},` +
		`{"name":"color","value":"$..bicycle.color","language":"jsonpath"},` +
		`{"name":"all","value":"$..book[*].author","language":"jsonpath"},` +
		`{"name":"none","value":"$.store.nothing","language":"jsonpath"},` +
		`{"name":"obj","value":"$.store.bicycle","language":"jsonpath"}]`
	m := process(t, "setheaders", map[string]any{"headers": headers}, message.New(doc))
	for k, want := range map[string]string{"author": "Nigel Rees", "price": "8.95", "color": "red", "all": "[Nigel Rees, E]", "none": "", "obj": "{color=red}"} {
		if m[k] != want {
			t.Errorf("header %s = %q, want %q", k, m[k], want)
		}
	}
	m = process(t, "setheaders", map[string]any{"headers": headers, "writeAsString": true}, message.New(doc))
	for k, want := range map[string]string{"author": `"Nigel Rees"`, "price": "8.95", "color": `"red"`, "all": `["Nigel Rees","E"]`, "obj": `{"color":"red"}`} {
		if m[k] != want {
			t.Errorf("as a string, header %s = %q, want %q", k, m[k], want)
		}
	}

	process(t, "settenantvariable:lines", map[string]any{"language": "jsonpath", "value": "$..line", "tenantDbName": "jp"}, message.New(`[{"line":1}]`))
	if v, _ := tenantVariables.get("jp", "lines"); v != "1" {
		t.Errorf("variable = %q", v)
	}
	wantInvalid(t, stepdef.Action, "setheaders", map[string]any{"headers": `[{"name":"x","value":"$.a[","language":"jsonpath"}]`}, `header x: jsonpath "$.a["`)
	if _, err := mustProcessor(t, stepdef.Action, "setheaders", map[string]any{"headers": `[{"name":"x","value":"$.a","language":"jsonpath"}]`}).(stepdef.ActionProcessor).Process(t.Context(), message.New("not json")); err == nil || !strings.Contains(err.Error(), "body is not JSON") {
		t.Errorf("err = %v", err)
	}

	// The condition of a content router or a filter.
	for expr, want := range map[string]bool{"$.store.book[?(@.price < 10)]": true, "$.store.book[?(@.price < 1)]": false, "$..bicycle.color": true} {
		p, err := compilePredicate("jsonpath", expr)
		if err != nil {
			t.Fatal(err)
		}
		if got, _ := p(message.New(doc)); got != want {
			t.Errorf("%s = %v, want %v", expr, got, want)
		}
	}
	p, err := compilePredicate("jsonpath", "${header.expression}")
	if err != nil {
		t.Fatal(err)
	}
	m = message.New(doc)
	m["expression"] = "$.store.book[?(@.price < 10)]"
	if ok, err := p(m); !ok || err != nil {
		t.Errorf("an expression from a header: %v, %v", ok, err)
	}
}
