package impl

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	flowdef "dif/flows/definition"
	"dif/internal/mapping"
	stepdef "dif/steps/definition"
)

func TestProcessorMarkers(t *testing.T) {
	marker := map[string]any{"$param": "x"}
	v, e := walkOptions(map[string]any{"nested": []any{marker, map[string]any{"$literal": marker}}}, map[string]any{"x": true}, 0)
	if e != nil {
		t.Fatal(e)
	}
	encoded, _ := json.Marshal(v)
	if string(encoded) != `{"nested":[true,{"$param":"x"}]}` {
		t.Fatal(string(encoded))
	}
	if _, e = walkOptions(marker, map[string]any{}, 0); e == nil {
		t.Fatal("unknown parameter")
	}
	var nested any = marker
	for i := 0; i < 130; i++ {
		nested = []any{nested}
	}
	if _, e = walkOptions(nested, map[string]any{"x": 1}, 0); e == nil {
		t.Fatal("option depth not bounded")
	}
}
func TestFunctionCycleAndDepthDiagnostics(t *testing.T) {
	for _, src := range []string{
		`{"dil":{"core":{"functions":{"function":[{"name":"a","parameters":[],"returns":"any","language":"difmap/1","expression":"function(\"b\")"},{"name":"b","parameters":[],"returns":"any","language":"difmap/1","expression":"function(\"a\")"}]}}}}`,
		`{"dil":{"core":{"functions":{"function":{"name":"a","parameters":[],"returns":"any","language":"difmap/1","expression":"if (false) function(\"a\") else 1"}}}}}`,
	} {
		d, e := DecodeDocument([]byte(src))
		if e != nil {
			t.Fatal(e)
		}
		d.BindScope("lib.json", nil)
		e = d.PrepareFunctions(context.Background(), mapping.Limits{})
		if e == nil || !strings.Contains(e.Error(), "function cycle:") || !strings.Contains(e.Error(), "lib.json:a") {
			t.Fatal(e)
		}
	}
	src := `{"dil":{"core":{"functions":{"function":[{"name":"b","parameters":[],"returns":"number","language":"difmap/1","expression":"1"},{"name":"a","parameters":[],"returns":"number","language":"difmap/1","expression":"function(\"b\")"}]}}}}`
	d, e := DecodeDocument([]byte(src))
	if e != nil {
		t.Fatal(e)
	}
	if e = d.PrepareFunctions(context.Background(), mapping.Limits{CallDepth: 1}); e == nil {
		t.Fatal("cached callee bypassed depth")
	}
}
func FuzzProcessorBinding(f *testing.F) {
	f.Add(`{"x":{"$param":"x"}}`)
	f.Add(`{"x":{"$literal":{"$param":"x"}}}`)
	f.Fuzz(func(t *testing.T, src string) {
		if len(src) > 4096 {
			return
		}
		var v any
		if json.Unmarshal([]byte(src), &v) != nil {
			return
		}
		_, _ = walkOptions(v, map[string]any{"x": 1}, 0)
	})
}

func TestProcessorOptionsAreDecodedOnce(t *testing.T) {
	src := `{"dil":{"core":{"processors":{"processor":{"name":"set","parameters":[],"action":{"uri":"settenantvariable","options":{"name":"example","value":"UVE9PQ=="}}}}},"integrations":{"integration":{"flows":{"flow":{"id":"decode-once","steps":{"step":[{"id":"in","type":"source","uri":"message","links":{"link":{"id":"1","bound":"out"}}},{"id":"action","type":"action","uri":"processor:set","links":{"link":[{"id":"1","bound":"in"},{"id":"2","bound":"out"}]}},{"id":"out","type":"sink","uri":"passthrough","links":{"link":{"id":"2","bound":"in"}}}]}}}}}}}`
	var got any
	_, e := Parse([]byte(src), func(n *flowdef.Node) (stepdef.Processor, error) {
		if n.ID == "action" {
			got = n.Options["value"]
		}
		return nil, nil
	})
	if e != nil {
		t.Fatal(e)
	}
	if got != "QQ==" {
		t.Fatalf("option decoded twice: %v", got)
	}
}
