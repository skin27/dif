package api_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dif/api"
)

// step is a step of a test flow; next are the ids of the steps its outbound
// links go to, with rules and conditions for a router's links.
type step struct {
	id, typ, uri string
	options      map[string]any
	next         []string
	rules        []string
	conditions   []string
}

// dilFlow returns a DIL document with one flow of the steps, and core holds
// what goes in dil.core, if anything.
func dilFlow(t *testing.T, core map[string]any, steps ...step) []byte {
	t.Helper()
	links := map[string][]map[string]any{}
	for _, s := range steps {
		for i, next := range s.next {
			out := map[string]any{"id": s.id + "-" + next, "bound": "out"}
			if i < len(s.rules) && s.rules[i] != "" {
				out["rule"] = s.rules[i]
			}
			if i < len(s.conditions) && s.conditions[i] != "" {
				out["language"], out["expression"] = "simple", s.conditions[i]
			}
			links[s.id] = append(links[s.id], out)
			links[next] = append(links[next], map[string]any{"id": s.id + "-" + next, "bound": "in"})
		}
	}
	var list []map[string]any
	for _, s := range steps {
		m := map[string]any{"id": s.id, "type": s.typ, "uri": s.uri, "options": s.options}
		if l := links[s.id]; l != nil {
			m["links"] = map[string]any{"link": l}
		}
		list = append(list, m)
	}
	doc := map[string]any{"dil": map[string]any{
		"integrations": map[string]any{"integration": map[string]any{"flows": map[string]any{"flow": map[string]any{
			"id": "check", "name": "check", "type": "esb", "steps": map[string]any{"step": list},
		}}}},
		"core": core,
	}}
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// codes returns the diagnostics as "severity code step" lines.
func codes(ds []api.Diagnostic) string {
	var out []string
	for _, d := range ds {
		out = append(out, d.Severity+" "+d.Code+" "+d.Step)
	}
	return strings.Join(out, "\n")
}

// identityMap is a core map Copy from and to the JSON format In.
var identityMap = map[string]any{
	"formats": map[string]any{"format": []any{map[string]any{"name": "In", "mediaType": "application/json"}}},
	"maps":    map[string]any{"map": []any{map[string]any{"name": "Copy", "input": "In", "output": "In", "language": "identity"}}},
}

func TestInspectReportsDiagnostics(t *testing.T) {
	timer := step{id: "in", typ: "source", uri: "timer:t", next: []string{"a"}}
	tests := []struct {
		name  string
		core  map[string]any
		steps []step
		want  string
	}{
		{
			name: "xslt after xmltojson",
			core: map[string]any{"resources": map[string]any{"resource": map[string]any{"name": "sheet", "content": `<xsl:stylesheet version="1.0" xmlns:xsl="http://www.w3.org/1999/XSL/Transform"/>`}}},
			steps: []step{timer,
				{id: "a", typ: "action", uri: "xmltojson", next: []string{"b"}},
				{id: "b", typ: "action", uri: "xslt:ref:sheet", next: []string{"c"}},
				{id: "c", typ: "sink", uri: "log"}},
			want: "error body-mismatch b",
		},
		{
			name: "jsonvalidator after jsontoxml",
			core: map[string]any{"resources": map[string]any{"resource": map[string]any{"name": "schema", "content": `{"type": "object"}`}}},
			steps: []step{timer,
				{id: "a", typ: "action", uri: "jsontoxml", next: []string{"b"}},
				{id: "b", typ: "action", uri: "jsonvalidator:ref:schema", next: []string{"c"}},
				{id: "c", typ: "sink", uri: "log"}},
			want: "error body-mismatch b",
		},
		{
			name: "converters that fit",
			steps: []step{timer,
				{id: "a", typ: "action", uri: "jsontoxml", next: []string{"b"}},
				{id: "b", typ: "action", uri: "xmltojson", next: []string{"c"}},
				{id: "c", typ: "sink", uri: "log"}},
		},
		{
			name: "header nothing sets, with a hint",
			steps: []step{timer,
				{id: "a", typ: "action", uri: "setheader", options: map[string]any{"name": "orderId", "value": "1"}, next: []string{"b"}},
				{id: "b", typ: "action", uri: "setbody", options: map[string]any{"language": "simple", "expression": "${header.orderid} ${header.ordrId}"}, next: []string{"c"}},
				{id: "c", typ: "sink", uri: "log"}},
			want: "warning header-not-set b",
		},
		{
			name: "content router without otherwise",
			steps: []step{timer,
				{id: "a", typ: "router", uri: "content", next: []string{"b", "c"}, conditions: []string{"${body} == 1", "${body} == 2"}},
				{id: "b", typ: "sink", uri: "log"},
				{id: "c", typ: "sink", uri: "log"}},
			want: "warning no-otherwise a",
		},
		{
			name: "mapper after a converter to another media type",
			core: identityMap,
			steps: []step{timer,
				{id: "a", typ: "action", uri: "jsontoxml", next: []string{"b"}},
				{id: "b", typ: "action", uri: "mapper:Copy", next: []string{"c"}},
				{id: "c", typ: "sink", uri: "log"}},
			want: "error content-type-mismatch b",
		},
		{
			name: "mapper after a converter to its media type",
			core: identityMap,
			steps: []step{timer,
				{id: "a", typ: "action", uri: "xmltojson", next: []string{"b"}},
				{id: "b", typ: "action", uri: "mapper:Copy", next: []string{"c"}},
				{id: "c", typ: "action", uri: "xslt", options: map[string]any{"resource": `<xsl:stylesheet version="1.0" xmlns:xsl="http://www.w3.org/1999/XSL/Transform"/>`}, next: []string{"d"}},
				{id: "d", typ: "sink", uri: "log"}},
			want: "error body-mismatch c", // the map makes JSON
		},
		{
			name: "an open source knows nothing",
			steps: []step{{id: "in", typ: "source", uri: "message:m", next: []string{"a"}},
				{id: "a", typ: "action", uri: "setbody", options: map[string]any{"language": "simple", "expression": "${header.anything}"}, next: []string{"b"}},
				{id: "b", typ: "sink", uri: "log"}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			info, err := api.InspectBytes(dilFlow(t, tc.core, tc.steps...))
			if err != nil {
				t.Fatal(err)
			}
			if got := codes(info.Diagnostics); got != tc.want {
				t.Errorf("diagnostics:\n%s\nwant:\n%s\n(%v)", got, tc.want, info.Diagnostics)
			}
		})
	}
}

func TestInspectHeaderHint(t *testing.T) {
	data := dilFlow(t, nil,
		step{id: "in", typ: "source", uri: "timer:t", next: []string{"a"}},
		step{id: "a", typ: "action", uri: "setheader", options: map[string]any{"name": "orderId", "value": "1"}, next: []string{"b"}},
		step{id: "b", typ: "action", uri: "setbody", options: map[string]any{"language": "simple", "expression": "${header.ordrId}"}, next: []string{"c"}},
		step{id: "c", typ: "sink", uri: "log"})
	info, err := api.InspectBytes(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(info.Diagnostics) != 1 || !strings.Contains(info.Diagnostics[0].Message, `did you mean "orderId"?`) {
		t.Errorf("diagnostics = %v, want one with a hint", info.Diagnostics)
	}
}

// TestInspectProgramDiagnostics checks that a program's info holds the
// diagnostics of its flows, and that a flow with an error still loads: the
// checks never block loading.
func TestInspectProgramDiagnostics(t *testing.T) {
	data := dilFlow(t, nil,
		step{id: "in", typ: "source", uri: "timer:t", next: []string{"a"}},
		step{id: "a", typ: "action", uri: "jsontoxml", next: []string{"b"}},
		step{id: "b", typ: "action", uri: "xmltocsv", next: []string{"c"}},
		step{id: "c", typ: "action", uri: "xmltojson", next: []string{"d"}},
		step{id: "d", typ: "sink", uri: "log"})
	path := filepath.Join(t.TempDir(), "flow.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := api.InspectProgram(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := codes(info.Diagnostics); got != "warning body-mismatch c" {
		t.Errorf("program diagnostics = %q", got)
	}
	if got := codes(info.Flows[0].Flow.Diagnostics); got != "warning body-mismatch c" {
		t.Errorf("flow diagnostics = %q", got)
	}
	if _, err := api.LoadProgram(path, nil); err != nil {
		t.Errorf("load: %v", err)
	}
}
