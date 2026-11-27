package api_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dif/api"
)

func mappingCore(expression string) map[string]any {
	return map[string]any{
		"formats": map[string]any{"format": map[string]any{"name": "json", "mediaType": "application/json"}},
		"maps":    map[string]any{"map": map[string]any{"name": "map", "input": "json", "output": "json", "language": "difmap/1", "expression": expression}},
	}
}
func mappingFlow(name, ref string) map[string]any {
	return programFlow(name, "", map[string]any{"type": "action", "uri": "mapper:" + ref})
}
func TestMappingExampleAndDescriptions(t *testing.T) {
	path := "../../api/testdata/mapping/orders.json"
	p, e := api.LoadProgram(path, nil)
	if e != nil {
		t.Fatal(e)
	}
	if len(p.Flows) != 2 || len(p.Info.Documents) != 3 {
		t.Fatal("missing mapping group")
	}
	for _, f := range p.Flows {
		if e = f.Start(); e != nil {
			t.Fatal(e)
		}
		defer f.ForceStop()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		out, e := f.Request(ctx, f.NewMessage())
		cancel()
		if e != nil {
			t.Fatal(e)
		}
		if f.ID() == "map-order" {
			if out[api.Body] != `{"id":"9007199254740993","customer":{"name":"Ada","country":"NL"},"lines":[{"sku":"PART-A","quantity":2}]}` {
				t.Fatalf("mapping result: %v", out[api.Body])
			}
		} else if out[api.Body] != "sku,quantity\nPART-A,2\n" || out["Content-Type"] != "text/csv" {
			t.Fatalf("CSV mapping: %v", out)
		}
	}
	encoded, _ := json.Marshal(p.Info)
	if strings.Contains(string(encoded), "trim(") || strings.Contains(string(encoded), "Ada") || strings.Contains(string(encoded), "required") {
		t.Fatalf("description exposed values: %s", encoded)
	}
	if !strings.Contains(string(encoded), `"language":"difmap/1"`) || !strings.Contains(string(encoded), `"inputOrigin"`) {
		t.Fatal("missing mapping metadata")
	}
}
func TestMappingSingleFlowCompatibilityAndFailureAtomicity(t *testing.T) {
	data := programJSON(t, mappingCore(`{a:payload.a, b:number(payload.bad)}`), nil, mappingFlow("mapping-failure", "map"))
	info, e := api.InspectBytes(data)
	if e != nil || info.Steps[1].Mapping == nil {
		t.Fatalf("inspect: %v", e)
	}
	f, e := api.LoadBytes(data, nil)
	if e != nil {
		t.Fatal(e)
	}
	if e = f.Start(); e != nil {
		t.Fatal(e)
	}
	defer f.ForceStop()
	msg := api.Message{api.Body: `{"a":1,"bad":"PRIVATE"}`, "Content-Type": "application/json", "original": "keep"}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, e = f.Request(ctx, msg)
	if e == nil || strings.Contains(e.Error(), "PRIVATE") {
		t.Fatalf("unsafe mapping error: %v", e)
	}
	if msg[api.Body] != `{"a":1,"bad":"PRIVATE"}` || msg["Content-Type"] != "application/json" || msg["original"] != "keep" {
		t.Fatalf("partially mutated message: %v", msg)
	}
	core := mappingCore(`{wrong:1}`)
	core["formats"].(map[string]any)["format"].(map[string]any)["schemaRef"] = "schema"
	core["resources"] = map[string]any{"resource": map[string]any{"name": "schema", "content": `{"required":["needed"]}`}}
	data = programJSON(t, core, nil, mappingFlow("output-schema-failure", "map"))
	f, e = api.LoadBytes(data, nil)
	if e != nil {
		t.Fatal(e)
	}
	if e = f.Start(); e != nil {
		t.Fatal(e)
	}
	defer f.ForceStop()
	msg = api.Message{api.Body: `{"needed":1}`, "Content-Type": "application/json"}
	_, e = f.Request(ctx, msg)
	if e == nil || !strings.Contains(e.Error(), "output format") || msg[api.Body] != `{"needed":1}` {
		t.Fatalf("output schema failure was not atomic: %v, %v", e, msg)
	}
}
func TestImportedMappingKeepsPrivateScopeAndSnapshots(t *testing.T) {
	dir := t.TempDir()
	contracts := saveProgram(t, dir, "contracts.json", programJSON(t, map[string]any{
		"formats":   map[string]any{"format": map[string]any{"name": "json", "mediaType": "application/json", "schemaRef": "shape"}},
		"resources": map[string]any{"resource": map[string]any{"name": "shape", "content": `{"type":"object"}`}},
	}, nil))
	lib := mappingCore(`{result:payload.value}`)
	delete(lib, "formats")
	m := lib["maps"].(map[string]any)["map"].(map[string]any)
	m["input"] = "contracts:json"
	m["output"] = "contracts:json"
	delete(m, "expression")
	m["sourceRef"] = "source"
	lib["resources"] = map[string]any{"resource": map[string]any{"name": "source", "content": `{result:payload.value}`}}
	library := saveProgram(t, dir, "library.json", programJSON(t, lib, map[string]any{"path": "contracts.json", "namespace": "contracts"}))
	caller := mappingCore(`{caller:true}`)
	caller["resources"] = map[string]any{"resource": map[string]any{"name": "source", "content": "INVALID"}}
	root := saveProgram(t, dir, "root.json", programJSON(t, caller, map[string]any{"path": "library.json", "namespace": "lib"}, mappingFlow("scope", "lib:map")))
	prepared, e := api.PrepareProgram(context.Background(), []api.ProgramSource{{Path: root}}, api.ProgramLimits{})
	if e != nil {
		t.Fatal(e)
	}
	for _, file := range []string{contracts, library} {
		if e = os.WriteFile(file, []byte("changed"), 0600); e != nil {
			t.Fatal(e)
		}
	}
	info := prepared.Info()
	info.Flows[0].Flow.Steps[1].Mapping.Name = "mutated"
	if prepared.Info().Flows[0].Flow.Steps[1].Mapping.Name != "map" {
		t.Fatal("mapping metadata is mutable")
	}
	for i := 0; i < 2; i++ {
		loaded, e := prepared.Load(context.Background(), nil, nil, nil)
		if e != nil {
			t.Fatal(e)
		}
		f := loaded.Flows[0]
		if e = f.Start(); e != nil {
			t.Fatal(e)
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		out, e := f.Request(ctx, api.Message{api.Body: `{"value":7}`})
		cancel()
		f.ForceStop()
		if e != nil || out[api.Body] != `{"result":7}` {
			t.Fatalf("scope/snapshot: %v %v", out, e)
		}
	}
}
func TestMappingDefinitionValidation(t *testing.T) {
	cases := []struct {
		name   string
		modify func(map[string]any)
		want   string
	}{
		{"missing format", func(c map[string]any) { delete(c, "formats") }, "format"},
		{"unknown format field", func(c map[string]any) { c["formats"].(map[string]any)["format"].(map[string]any)["unknown"] = true }, "invalid"},
		{"unknown map field", func(c map[string]any) { c["maps"].(map[string]any)["map"].(map[string]any)["code"] = "bad" }, "invalid"},
		{"duplicate map", func(c map[string]any) { ms := c["maps"].(map[string]any); ms["map"] = []any{ms["map"], ms["map"]} }, "duplicate map"},
		{"duplicate format", func(c map[string]any) {
			fs := c["formats"].(map[string]any)
			fs["format"] = []any{fs["format"], fs["format"]}
		}, "duplicate format"},
		{"source conflict", func(c map[string]any) { c["maps"].(map[string]any)["map"].(map[string]any)["sourceRef"] = "missing" }, "exactly one"},
		{"unknown language", func(c map[string]any) { c["maps"].(map[string]any)["map"].(map[string]any)["language"] = "dataweave" }, "unsupported"},
		{"unused invalid expression", func(c map[string]any) {
			c["maps"].(map[string]any)["map"].(map[string]any)["expression"] = "notAVariable"
		}, "unknown variable"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := mappingCore(`payload`)
			tc.modify(c)
			_, e := api.PrepareProgram(context.Background(), []api.ProgramSource{{Data: programJSON(t, c, nil)}}, api.ProgramLimits{})
			if e == nil || !strings.Contains(e.Error(), tc.want) {
				t.Fatalf("%v, want %s", e, tc.want)
			}
		})
	}
	core := mappingCore(`payload`)
	m := core["maps"].(map[string]any)["map"].(map[string]any)
	m["name"] = "lib:map"
	if _, e := api.LoadBytes(programJSON(t, core, nil, mappingFlow("colon", "lib:map")), nil); e != nil {
		t.Fatal(e)
	}
	for _, name := range []string{"ref:map", "message:map"} {
		m["name"] = name
		if _, e := api.LoadBytes(programJSON(t, core, nil, mappingFlow("colon-reserved", name)), nil); e != nil {
			t.Fatalf("local map name %s: %v", name, e)
		}
	}
	action := map[string]any{"type": "action", "uri": "mapper:map", "options": map[string]any{"dif.mapping.plan": "forged"}}
	if _, e := api.LoadBytes(programJSON(t, mappingCore(`payload`), nil, programFlow("forged", "", action)), nil); e == nil {
		t.Fatal("accepted forged binding")
	}
	if _, e := api.PrepareProgram(context.Background(), []api.ProgramSource{{Data: programJSON(t, mappingCore(`payload`), nil)}}, api.ProgramLimits{Mapping: api.MappingLimits{SourceBytes: 2}}); e == nil {
		t.Fatal("host source limit ignored")
	}
	// A map's private imports are not public aliases in the caller's scope.
	dir := t.TempDir()
	saveProgram(t, dir, "lib.json", programJSON(t, mappingCore(`payload`), nil))
	path := saveProgram(t, dir, "root.json", programJSON(t, nil, map[string]any{"path": "lib.json", "namespace": "lib"}, mappingFlow("wrong-ref", "lib:private:map")))
	if _, e := api.InspectProgram(filepath.Clean(path)); e == nil {
		t.Fatal("accepted missing map")
	}
}

func TestMappingFailuresUseExistingErrorRoute(t *testing.T) {
	f := mappingFlow("mapping-recovery", "map")
	nodes := f["steps"].(map[string]any)["step"].([]map[string]any)
	nodes = append(nodes,
		map[string]any{"id": "failed", "type": "error", "uri": "failedexchange", "options": map[string]any{"maximumRedeliveries": 1, "redeliveryDelay": 0}, "links": map[string]any{"link": map[string]any{"id": "recover", "bound": "out"}}},
		map[string]any{"id": "recover", "type": "action", "uri": "setbody", "options": map[string]any{"language": "simple", "expression": "recovered: ${body}"}, "links": map[string]any{"link": []any{map[string]any{"id": "recover", "bound": "in"}, map[string]any{"id": "recovered", "bound": "out"}}}},
		map[string]any{"id": "error-output", "type": "sink", "uri": "passthrough", "links": map[string]any{"link": map[string]any{"id": "recovered", "bound": "in"}}},
	)
	f["steps"].(map[string]any)["step"] = nodes
	flow, e := api.LoadBytes(programJSON(t, mappingCore(`{x: 1 / 0}`), nil, f), nil)
	if e != nil {
		t.Fatal(e)
	}
	if e = flow.Start(); e != nil {
		t.Fatal(e)
	}
	defer flow.ForceStop()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	out, e := flow.Request(ctx, api.Message{api.Body: `{"original":true}`})
	if e != nil || out[api.Body] != `recovered: {"original":true}` {
		t.Fatalf("mapping recovery: %v, %v", out, e)
	}
}
