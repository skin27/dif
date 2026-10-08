package impl

import (
	"bytes"
	"context"
	"errors"
	"log"
	"strings"
	"testing"

	"dif/message"
	stepdef "dif/steps/definition"
)

var enrichLinks = []stepdef.Link{{}, {Rule: "enrich"}} // as in testdata/examples/enrich.json

// enrich runs the enrich router of enrichType on body, with the enrich route
// turning its copy into the outcome, and returns the message it sends on.
func enrich(t *testing.T, opts map[string]any, body string, outcome stepdef.Outcome) message.Message {
	t.Helper()
	r, err := newRouter(stepdef.Router, "enrich", opts, enrichLinks...)
	if err != nil {
		t.Fatal(err)
	}
	m := message.New(body)
	m["h"] = "original"
	routes, err := r.Route(context.Background(), m)
	if err != nil {
		t.Fatal(err)
	}
	if len(routes) != 1 || routes[0].Next != 1 || sameMessage(routes[0].Message, m) {
		t.Fatalf("routes = %+v, want a copy along the enrich link", routes)
	}
	next, err := r.(stepdef.Gatherer).Gather(context.Background(), m, []stepdef.Outcome{outcome})
	if err != nil {
		t.Fatal(err)
	}
	if len(next) != 1 || next[0].Next != 0 {
		t.Fatalf("routes = %+v, want the message along the main link", next)
	}
	return next[0].Message
}

func TestEnrichOverride(t *testing.T) {
	e := message.New("enriched")
	e["h"] = "from enrichment"
	out := enrich(t, map[string]any{"enrichType": "override"}, "<a/>", stepdef.Outcome{Message: e})
	if out[message.Body] != "enriched" || out["h"] != "from enrichment" {
		t.Errorf("message = %v, want the enrichment", out)
	}
}

// TestEnrichOlderNames checks that enrichMethod and enrichFileType, which the
// designer writes, choose the merge as enrichType does.
func TestEnrichOlderNames(t *testing.T) {
	for _, opts := range []map[string]any{
		{"enrichMethod": "override"},
		{"enrichFileType": "OVERRIDE"},
		{"enrichType": "xml", "enrichMethod": "override"},
		{"enrichType": "json", "enrichFileType": "xml", "enrichMethod": "override"},
	} {
		e := message.New("enriched")
		out := enrich(t, opts, "<a/>", stepdef.Outcome{Message: e})
		if out[message.Body] != "enriched" {
			t.Errorf("%v: body = %v, want the enrichment to replace the message", opts, out[message.Body])
		}
	}
	// Alone, or with the same value, they change nothing.
	out := enrich(t, map[string]any{"enrichMethod": "xml"}, "<a/>", stepdef.Outcome{Message: message.New("<b/>")})
	if out[message.Body] != "<a><b/></a>" {
		t.Errorf("enrichMethod xml: body = %v", out[message.Body])
	}
	if _, err := newRouter(stepdef.Router, "enrich", map[string]any{"enrichMethod": "attachment"}, enrichLinks...); err == nil || !strings.Contains(err.Error(), `option enrichMethod: "attachment" is not one of`) {
		t.Errorf("attachment: err = %v", err)
	}
}

func TestEnrichXML(t *testing.T) {
	tests := []struct{ body, enrichment, want string }{
		{"<persons><person>A</person></persons>", `<?xml version="1.0"?>` + "\n<person>B</person>\n",
			"<persons><person>A</person><person>B</person></persons>"},
		{`<?xml version="1.0"?><p:root xmlns:p="urn:p" a="1"/>`, "<x/>", `<?xml version="1.0"?><p:root xmlns:p="urn:p" a="1"><x/></p:root>`},
		{"<root />\n", "<x>1</x>", "<root><x>1</x></root>\n"},
		{"<a><b/></a><!-- end -->", "<c><d/></c>", "<a><b/><c><d/></c></a><!-- end -->"},
	}
	for _, tt := range tests {
		out := enrich(t, nil, tt.body, stepdef.Outcome{Message: message.New(tt.enrichment)})
		if out[message.Body] != tt.want || out["h"] != "original" {
			t.Errorf("%s + %s = %v, want %s", tt.body, tt.enrichment, out[message.Body], tt.want)
		}
	}
}

func TestEnrichJSON(t *testing.T) {
	out := enrich(t, map[string]any{"enrichType": "json"}, `{"a":1,"b":{"c":2}}`, stepdef.Outcome{Message: message.New(`{"b":[3],"d":"<x>"}`)})
	if want := `{"a":1,"b":[3],"d":"<x>"}`; out[message.Body] != want {
		t.Errorf("body = %v, want %s", out[message.Body], want)
	}
}

func TestEnrichFails(t *testing.T) {
	r, err := newRouter(stepdef.Router, "enrich", map[string]any{"enrichType": "json"}, enrichLinks...)
	if err != nil {
		t.Fatal(err)
	}
	g := r.(stepdef.Gatherer)
	for _, tt := range []struct{ body, enrichment, want string }{
		{"not json", "{}", "enrichment: body is not JSON"},
		{"{}", "<x/>", "enrichment: enrichment body is not JSON"},
		{"[]", "{}", "json enrichment needs a JSON object"},
	} {
		if _, err := g.Gather(context.Background(), message.New(tt.body), []stepdef.Outcome{{Message: message.New(tt.enrichment)}}); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s + %s: err = %v, want containing %q", tt.body, tt.enrichment, err, tt.want)
		}
	}
	r, _ = newRouter(stepdef.Router, "enrich", nil, enrichLinks...)
	if _, err := r.(stepdef.Gatherer).Gather(context.Background(), message.New("<a/>"), []stepdef.Outcome{{Message: message.New("x")}}); err == nil || !strings.Contains(err.Error(), "enrichment body is not XML") {
		t.Errorf("err = %v", err)
	}

	// The enrich route failed: fail the message with that error (the default),
	// or continue without the enrichment.
	boom := errors.New("boom")
	if _, err := g.Gather(context.Background(), message.New("{}"), []stepdef.Outcome{{Err: boom}}); err != boom {
		t.Errorf("err = %v, want the enrich route's error as is", err)
	}
	var logged bytes.Buffer
	ctx := stepdef.WithLogger(context.Background(), log.New(&logged, "", 0))
	r, _ = newRouter(stepdef.Router, "enrich", map[string]any{"useErrorRoute": "false"}, enrichLinks...)
	m := message.New("{}")
	routes, err := r.(stepdef.Gatherer).Gather(ctx, m, []stepdef.Outcome{{Err: boom}})
	if err != nil || len(routes) != 1 || routes[0].Next != 0 || !sameMessage(routes[0].Message, m) {
		t.Errorf("routes = %+v, err = %v; want the message unenriched along the main link", routes, err)
	}
	if !strings.Contains(logged.String(), "step test-step: enrichment failed, the message continues without it: boom") {
		t.Errorf("log = %q", logged.String())
	}
}

func TestEnrichInvalid(t *testing.T) {
	for _, links := range [][]stepdef.Link{{{}}, {{Rule: "enrich"}}, {{}, {}}, {{Rule: "enrich"}, {Rule: "enrich"}, {}}} {
		if _, err := newRouter(stepdef.Router, "enrich", nil, links...); err == nil || !strings.Contains(err.Error(), "needs one outbound link with rule enrich and one without") {
			t.Errorf("links %v: err = %v", links, err)
		}
	}
	if _, err := newRouter(stepdef.Router, "enrich", map[string]any{"enrichType": "attachment"}, enrichLinks...); err == nil || !strings.Contains(err.Error(), `option enrichType: "attachment" is not one of`) {
		t.Errorf("err = %v", err)
	}
}
