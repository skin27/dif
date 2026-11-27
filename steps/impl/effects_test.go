package impl

import (
	"fmt"
	"testing"

	flowdef "dif/flows/definition"
	stepdef "dif/steps/definition"
	"dif/steps/registry"
)

func TestHeaderReads(t *testing.T) {
	tests := []struct {
		expr      string
		condition bool
		want      string
	}{
		{"${header.a}", false, "[[a]]"},
		{"${headers.a} ${header:b} ${header[c]} ${in.header.d}", false, "[[a] [b] [c] [d]]"},
		{"${header['quoted']}", false, "[[quoted]]"},
		{"${header.file.name}", false, "[[file.name file]]"},
		{"${header.a.trim()}", false, "[[a]]"},
		{"${uppercase(${header.a})}", false, "[[a]]"},
		{"${header.${header.which}}", false, "[[which]]"}, // the computed name is left out
		{"${headers}", false, "[]"},
		{"${headers.size}", false, "[]"},
		{"${variable.a} ${body}", false, "[]"},
		{"${header.n} > 10 && ${header.m} == 'x'", true, "[[n] [m]]"},
		{"${header.unclosed", false, "[]"},
	}
	for _, tc := range tests {
		if got := fmt.Sprint(headerReads(tc.expr, tc.condition)); got != tc.want {
			t.Errorf("headerReads(%q) = %s, want %s", tc.expr, got, tc.want)
		}
	}
	if got := readsIn("constant", "${header.a}", false); got != nil {
		t.Errorf("a constant reads %v, want nothing", got)
	}
	if got := fmt.Sprint(readsIn("xpath", "/a[@id='${header.id}']", true)); got != "[[id]]" {
		t.Errorf("an xpath template reads %s", got)
	}
}

// TestEffects checks the effects of a few built-in steps as the registry gives
// them for a node, from its validated options.
func TestEffects(t *testing.T) {
	r := registry.New()
	if err := Register(r); err != nil {
		t.Fatal(err)
	}
	node := func(kind, uri string, options map[string]any, links ...stepdef.Link) *flowdef.Node {
		n := &flowdef.Node{ID: "n", Kind: kind, URI: uri, Options: options, Links: links}
		for range links {
			n.Next = append(n.Next, &flowdef.Node{ID: "next"})
		}
		return n
	}

	if e := r.Effect(node(stepdef.Action, "setheader", map[string]any{"name": "a", "value": "${header.b}"})); e == nil ||
		fmt.Sprintf("%v %v %v %v", e.Sets, e.Reads, e.KeepBody, e.Headers) != "[a] [[b]] true keep" {
		t.Errorf("setheader: %+v", e)
	}
	headers := `[{"name": "a", "value": "${header.x}"}, {"name": "b", "value": "${header.A} ${header.y}"}, {"name": "c", "language": "constant", "value": "${header.z}"}]`
	if e := r.Effect(node(stepdef.Action, "setheaders", map[string]any{"headers": headers})); e == nil ||
		fmt.Sprintf("%v %v", e.Sets, e.Reads) != "[a b c] [[x] [y]]" {
		t.Errorf("setheaders: %+v", e) // b reads a, which a sets before it
	}
	if e := r.Effect(node(stepdef.Action, "setbody", map[string]any{"expression": "${header.b}"})); e == nil || e.Reads != nil || e.KeepBody {
		t.Errorf("setbody with a constant: %+v", e) // the default language is constant: the text is no expression
	}
	if e := r.Effect(node(stepdef.Action, "xmltojson", nil)); e == nil || e.Requires.Family != stepdef.FamilyXML || e.Body.Family != stepdef.FamilyJSON {
		t.Errorf("xmltojson: %+v", e)
	}
	if e := r.Effect(node(stepdef.Action, "jsontoxmlsimple", nil)); e == nil || e.Body.Family != "" {
		t.Errorf("jsontoxmlsimple without a root: %+v", e)
	}
	if e := r.Effect(node(stepdef.Action, "flowlink", map[string]any{"targetFlowId": "x"})); e != nil {
		t.Errorf("flowlink InOut: %+v, want unknown", e)
	}
	if e := r.Effect(node(stepdef.Action, "flowlink", map[string]any{"targetFlowId": "x", "exchangePattern": "InOnly"})); e == nil || !e.KeepBody {
		t.Errorf("flowlink InOnly: %+v", e)
	}
	split := r.Effect(node(stepdef.Router, "split", map[string]any{"language": "tokenize", "expression": ","}, stepdef.Link{Rule: "split"}, stepdef.Link{}))
	if split == nil || len(split.Links) != 2 || split.Links[0] == nil || split.Links[1] != nil || fmt.Sprint(split.Links[0].Sets) != "[split.index split.size split.complete]" {
		t.Errorf("split: %+v", split)
	}
	content := r.Effect(node(stepdef.Router, "content", nil, stepdef.Link{Expression: "${header.kind} == 'a'"}, stepdef.Link{}))
	if content == nil || !content.Otherwise || fmt.Sprint(content.Reads) != "[[kind]]" {
		t.Errorf("content: %+v", content)
	}
	if e := r.Effect(node(stepdef.Action, "wastebin", nil)); e == nil || !e.Stops {
		t.Errorf("wastebin as an action: %+v", e)
	}
	if e := r.Effect(node(stepdef.Action, "setheader", map[string]any{"unknown": 1})); e != nil {
		t.Errorf("invalid options: %+v, want nil", e)
	}
}
