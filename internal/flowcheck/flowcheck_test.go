package flowcheck

import (
	"strings"
	"testing"

	flowdef "dif/flows/definition"
	stepdef "dif/steps/definition"
)

// graph builds test flows: steps by id, effects by step id.
type graph struct {
	nodes   map[string]*flowdef.Node
	effects map[string]*Effect
}

func newGraph() *graph {
	return &graph{nodes: map[string]*flowdef.Node{}, effects: map[string]*Effect{}}
}

// step adds a step with effect e (nil: unknown) and returns its id.
func (g *graph) step(id, uri string, e *Effect) string {
	kind := flowdef.Action
	if len(g.nodes) == 0 {
		kind = flowdef.Source
	}
	g.nodes[id] = &flowdef.Node{ID: id, Kind: kind, URI: uri}
	g.effects[id] = e
	return id
}

// link links from to the steps to, with link attributes if any.
func (g *graph) link(from string, to string, l ...stepdef.Link) {
	n := g.nodes[from]
	n.Next = append(n.Next, g.nodes[to])
	if len(l) > 0 {
		for len(n.Links) < len(n.Next)-1 {
			n.Links = append(n.Links, stepdef.Link{})
		}
		n.Links = append(n.Links, l[0])
	}
}

// chain links the steps one after the other.
func (g *graph) chain(ids ...string) {
	for i := 1; i < len(ids); i++ {
		g.link(ids[i-1], ids[i])
	}
}

func (g *graph) check(f *flowdef.Flow) string {
	if f.ID == "" {
		f.ID = "f"
	}
	if f.Source == nil {
		f.Source = g.nodes["in"]
	}
	return show(Check(f, func(n *flowdef.Node) *Effect { return g.effects[n.ID] }))
}

func show(ds []Diagnostic) string {
	var out []string
	for _, d := range ds {
		out = append(out, d.Severity+" "+d.Code+" "+d.Step)
	}
	return strings.Join(out, "; ")
}

var (
	closedSource = &Effect{Headers: stepdef.HeadersKeep}
	keep         = &Effect{KeepBody: true, Headers: stepdef.HeadersKeep}
	toJSON       = &Effect{Headers: stepdef.HeadersKeep, Requires: stepdef.BodyType{Family: stepdef.FamilyXML}, Body: stepdef.BodyType{Family: stepdef.FamilyJSON, Media: "application/json"}}
	toXML        = &Effect{Headers: stepdef.HeadersKeep, Requires: stepdef.BodyType{Family: stepdef.FamilyJSON}, Body: stepdef.BodyType{Family: stepdef.FamilyXML, Media: "application/xml"}}
	toCSV        = &Effect{Headers: stepdef.HeadersKeep, Body: stepdef.BodyType{Family: stepdef.FamilyCSV}}
	toText       = &Effect{Headers: stepdef.HeadersKeep, Body: stepdef.BodyType{Family: stepdef.FamilyText}}
	needsXML     = &Effect{KeepBody: true, Headers: stepdef.HeadersKeep, Requires: stepdef.BodyType{Family: stepdef.FamilyXML}}
	needsJSON    = &Effect{KeepBody: true, Headers: stepdef.HeadersKeep, Requires: stepdef.BodyType{Family: stepdef.FamilyJSON}}
	tolerantXML  = &Effect{Headers: stepdef.HeadersKeep, Requires: stepdef.BodyType{Family: stepdef.FamilyXML}, Tolerant: true}
)

func reads(names ...string) *Effect {
	e := *keep
	for _, n := range names {
		e.Reads = append(e.Reads, []string{n})
	}
	return &e
}

func sets(names ...string) *Effect {
	e := *keep
	e.Sets = names
	return &e
}

func mapper(in, inMedia, inFormat, out, outMedia, outFormat string) *Effect {
	return &Effect{Headers: stepdef.HeadersKeep,
		Requires: stepdef.BodyType{Family: in, Media: inMedia, Format: inFormat},
		Body:     stepdef.BodyType{Family: out, Media: outMedia, Format: outFormat}}
}

// TestChains checks flows that are one chain of steps: source, the effects in
// order, each a step s1, s2, ...
func TestChains(t *testing.T) {
	tests := []struct {
		name    string
		source  *Effect
		effects []*Effect
		want    string
	}{
		// A: the body family.
		{"xml step after json", closedSource, []*Effect{toJSON, needsXML}, "error body-mismatch s2"},
		{"xml step after xml", closedSource, []*Effect{toXML, needsXML}, ""},
		{"json step after xml", closedSource, []*Effect{toXML, needsJSON}, "error body-mismatch s2"},
		{"unknown body", closedSource, []*Effect{needsXML}, ""},
		{"an unknown step forgets", closedSource, []*Effect{toJSON, nil, needsXML}, ""},
		{"a tolerant step only warns", closedSource, []*Effect{toJSON, tolerantXML}, "warning body-mismatch s2"},
		{"csv is not certain", closedSource, []*Effect{toCSV, needsJSON}, "warning body-mismatch s2"},
		{"text is anything", closedSource, []*Effect{toText, needsJSON}, ""},
		{"a validator makes the family known", closedSource, []*Effect{needsJSON, needsXML}, "error body-mismatch s2"},
		{"binary is no xml", closedSource, []*Effect{{Headers: stepdef.HeadersKeep, Body: stepdef.BodyType{Family: stepdef.FamilyBinary}}, needsXML}, "error body-mismatch s2"},
		{"mapper on another media type", closedSource, []*Effect{toJSON, mapper("text", "text/plain", "", "", "", "")}, "error content-type-mismatch s2"},
		{"mapper on its media type", closedSource, []*Effect{toJSON, mapper("json", "application/json", "", "", "", "")}, ""},
		{"setheader forgets the content type", closedSource, []*Effect{toJSON, sets("content-type"), mapper("text", "text/plain", "", "", "", "")}, ""},
		{"other format, same family", closedSource, []*Effect{mapper("", "", "", "json", "application/json", "Order"), mapper("json", "application/json", "Invoice", "", "", "")}, "warning format-mismatch s2"},
		{"same format", closedSource, []*Effect{mapper("", "", "", "json", "application/json", "Order"), mapper("json", "application/json", "Order", "", "", "")}, ""},

		// B: headers that nothing sets.
		{"read of an unset header", closedSource, []*Effect{reads("x")}, "warning header-not-set s1"},
		{"read of a set header, in another case", closedSource, []*Effect{sets("X"), reads("x")}, ""},
		{"read of a header that may be set", closedSource, []*Effect{{KeepBody: true, Headers: stepdef.HeadersKeep, MaySet: []string{"x"}}, reads("x")}, ""},
		{"read in an open flow", &Effect{Headers: stepdef.HeadersAdd}, []*Effect{reads("x")}, ""},
		{"read after an unknown step", closedSource, []*Effect{nil, reads("x")}, ""},
		{"read after a step that adds headers", closedSource, []*Effect{{Headers: stepdef.HeadersAdd}, reads("x")}, ""},
		{"read of headers the engine provides", closedSource, []*Effect{reads("Message-Id", "correlation-id", "metadata.step", "error.message")}, ""},
		{"read after removeheaders", closedSource, []*Effect{sets("x"), {KeepBody: true, Headers: stepdef.HeadersRemove}, reads("x")}, ""},
		{"a source sets it", &Effect{Headers: stepdef.HeadersKeep, Sets: []string{"file.name"}}, []*Effect{reads("file.name")}, ""},
		{"a read of either name", closedSource, []*Effect{sets("a"), {KeepBody: true, Headers: stepdef.HeadersKeep, Reads: [][]string{{"a.b", "a"}}}}, ""},

		// C: a step that stops every message, at the end.
		{"wastebin at the end", closedSource, []*Effect{{Stops: true}}, ""},

		// Unknown produces nothing.
		{"nothing known", nil, []*Effect{nil, reads("x"), needsXML}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g := newGraph()
			ids := []string{g.step("in", "timer:t", tc.source)}
			for i, e := range tc.effects {
				ids = append(ids, g.step("s"+string(rune('1'+i)), "step", e))
			}
			g.chain(ids...)
			if got := g.check(&flowdef.Flow{}); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestHeaderHint(t *testing.T) {
	g := newGraph()
	g.chain(g.step("in", "timer:t", closedSource), g.step("a", "setheader", sets("orderId")), g.step("b", "setbody", reads("ordrId")))
	ds := Check(&flowdef.Flow{ID: "f", Source: g.nodes["in"]}, func(n *flowdef.Node) *Effect { return g.effects[n.ID] })
	if len(ds) != 1 || !strings.Contains(ds[0].Message, `did you mean "orderId"?`) {
		t.Errorf("got %v, want a hint", ds)
	}
}

func TestConfiguredMessageHeaders(t *testing.T) {
	g := newGraph()
	g.chain(g.step("in", "timer:t", closedSource), g.step("a", "setbody", reads("greeting")))
	f := &flowdef.Flow{Input: map[string]any{"greeting": "hello", "body": "x"}}
	if got := g.check(f); got != "" {
		t.Errorf("got %q, want nothing: send gives the configured message's headers", got)
	}
}

// TestRouters checks link effects: a split sets its headers only on its split link.
func TestRouters(t *testing.T) {
	split := &Effect{KeepBody: true, Headers: stepdef.HeadersKeep, Reply: true, Links: []*Effect{
		{Headers: stepdef.HeadersKeep, Sets: []string{"split.index"}},
	}}
	g := newGraph()
	g.step("in", "timer:t", closedSource)
	g.step("split", "split", split)
	g.step("part", "log", reads("split.index"))
	g.step("after", "log", reads("split.index"))
	g.link("in", "split")
	g.link("split", "part", stepdef.Link{Rule: "split"})
	g.link("split", "after", stepdef.Link{})
	if got := g.check(&flowdef.Flow{}); got != "warning header-not-set after" {
		t.Errorf("got %q", got)
	}
}

func TestContentRouterOtherwise(t *testing.T) {
	for _, tc := range []struct {
		name  string
		links []stepdef.Link
		want  string
	}{
		{"all links have a condition", []stepdef.Link{{Expression: "${body} == 1"}, {Expression: "${body} == 2"}}, "warning no-otherwise r"},
		{"one link has none", []stepdef.Link{{Expression: "${body} == 1"}, {}}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := newGraph()
			g.step("in", "timer:t", closedSource)
			g.step("r", "content", &Effect{KeepBody: true, Headers: stepdef.HeadersKeep, Otherwise: true, Reply: true})
			g.step("a", "log", keep)
			g.step("b", "log", keep)
			g.link("in", "r")
			g.link("r", "a", tc.links[0])
			g.link("r", "b", tc.links[1])
			if got := g.check(&flowdef.Flow{}); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestUnreachable(t *testing.T) {
	g := newGraph()
	g.chain(g.step("in", "timer:t", closedSource), g.step("bin", "wastebin", &Effect{Stops: true}), g.step("after", "log", needsXML))
	if got := g.check(&flowdef.Flow{}); got != "warning unreachable bin" {
		t.Errorf("got %q", got)
	}
}

// TestJoin checks a step two paths reach: it knows what both paths agree on.
func TestJoin(t *testing.T) {
	for _, tc := range []struct {
		name       string
		left, rght *Effect
		want       string
	}{
		{"paths disagree", toJSON, toXML, ""},
		{"paths agree", toJSON, toJSON, "error body-mismatch end"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := newGraph()
			g.step("in", "timer:t", closedSource)
			g.step("r", "recipient", keep)
			g.step("l", "x", tc.left)
			g.step("rr", "y", tc.rght)
			g.step("end", "z", needsXML)
			g.link("in", "r")
			g.link("r", "l")
			g.link("r", "rr")
			g.link("l", "end")
			g.link("rr", "end")
			if got := g.check(&flowdef.Flow{}); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestErrorHandlers(t *testing.T) {
	handler := func(id string, patterns map[string]string) *flowdef.ErrorHandler {
		return &flowdef.ErrorHandler{ID: id, Patterns: patterns}
	}
	for _, tc := range []struct {
		name     string
		handlers []*flowdef.ErrorHandler
		want     string
	}{
		{"errorStep matches a step", []*flowdef.ErrorHandler{handler("h", map[string]string{"errorStep": "ca*"})}, ""},
		{"errorStep matches no step", []*flowdef.ErrorHandler{handler("h", map[string]string{"errorStep": "nope*"})}, "warning handler-matches-nothing h"},
		{"errorUri matches a scheme", []*flowdef.ErrorHandler{handler("h", map[string]string{"errorUri": "https"})}, ""},
		{"errorUri matches no scheme", []*flowdef.ErrorHandler{handler("h", map[string]string{"errorUri": "sql*"})}, "warning handler-matches-nothing h"},
		{"same match options", []*flowdef.ErrorHandler{handler("h1", map[string]string{"errorClass": "*x"}), handler("h2", map[string]string{"errorClass": "*x"})}, "warning handler-shadowed h2"},
		{"other match options", []*flowdef.ErrorHandler{handler("h1", map[string]string{"errorClass": "*x"}), handler("h2", map[string]string{"errorClass": "*y"})}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := newGraph()
			g.chain(g.step("in", "timer:t", closedSource), g.step("call", "https://example.com/x", keep))
			if got := g.check(&flowdef.Flow{Handlers: tc.handlers}); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// TestErrorRoute checks that an error route starts from an unknown message.
func TestErrorRoute(t *testing.T) {
	g := newGraph()
	g.chain(g.step("in", "timer:t", closedSource), g.step("a", "log", keep))
	g.step("dlq", "log", reads("x"))
	f := &flowdef.Flow{Error: &flowdef.ErrorHandler{ID: "err", Route: g.nodes["dlq"]}}
	if got := g.check(f); got != "" {
		t.Errorf("got %q, want nothing", got)
	}
}

func TestReply(t *testing.T) {
	contract := func() *Effect {
		return &Effect{Headers: stepdef.HeadersKeep, Body: stepdef.BodyType{Family: stepdef.FamilyJSON, Media: "application/json"},
			ReplyNeeds: stepdef.BodyType{Family: stepdef.FamilyXML, Media: "application/xml"}}
	}
	for _, tc := range []struct {
		name    string
		effects []*Effect
		want    string
	}{
		{"the reply is json", []*Effect{keep}, "error reply-mismatch s1"},
		{"the reply is xml", []*Effect{toXML}, ""},
		{"the reply is unknown", []*Effect{nil}, ""},
		{"a step chooses the reply", []*Effect{{KeepBody: true, Headers: stepdef.HeadersKeep, Reply: true}, keep}, ""},
		{"the source alone", nil, "error reply-mismatch in"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			g := newGraph()
			ids := []string{g.step("in", "https:x", contract())}
			for i, e := range tc.effects {
				ids = append(ids, g.step("s"+string(rune('1'+i)), "step", e))
			}
			g.chain(ids...)
			if got := g.check(&flowdef.Flow{}); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestOrderIsDeterministic(t *testing.T) {
	g := newGraph()
	g.chain(g.step("in", "timer:t", closedSource), g.step("a", "x", toJSON), g.step("b", "y", &Effect{KeepBody: true, Headers: stepdef.HeadersKeep,
		Requires: stepdef.BodyType{Family: stepdef.FamilyXML}, Reads: [][]string{{"z"}, {"y"}}}))
	want := "error body-mismatch b; warning header-not-set b; warning header-not-set b"
	for range 5 {
		if got := g.check(&flowdef.Flow{}); got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	}
}
