package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"dif/api"
	stepdef "dif/steps/definition"
)

func functionDef(name, expression, returns string, params ...map[string]any) map[string]any {
	if params == nil {
		params = []map[string]any{}
	}
	return map[string]any{"name": name, "parameters": params, "returns": returns, "language": "difmap/1", "expression": expression}
}
func action(uri string, opts map[string]any) map[string]any {
	return map[string]any{"type": "action", "uri": uri, "options": opts}
}
func runFunctionFlow(t *testing.T, f *api.Flow, m api.Message) api.Message {
	t.Helper()
	if e := f.Start(); e != nil {
		t.Fatal(e)
	}
	defer f.ForceStop()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	out, e := f.Request(ctx, m)
	if e != nil {
		t.Fatal(e)
	}
	return out
}
func TestSharedFunctionsAcrossImportedMappingsAndProcessors(t *testing.T) {
	dir := t.TempDir()
	param := map[string]any{"name": "v", "type": "string"}
	private := functionDef("private", `upper(trim(v))`, "string", param)
	exposed := functionDef("normal", `function("private",v)`, "string", param)
	delete(exposed, "expression")
	exposed["sourceRef"] = "code"
	lib := mappingCore(`{id:function("normal",payload.id)}`)
	lib["functions"] = map[string]any{"function": []any{private, exposed}}
	lib["resources"] = map[string]any{"resource": map[string]any{"name": "code", "content": `function("private",v)`}}
	lib["processors"] = map[string]any{"processor": []any{
		map[string]any{"name": "normalize", "parameters": []any{}, "action": map[string]any{"uri": "setbody", "options": map[string]any{"language": "simple", "expression": `${function('normal', ${header.id})}`}}},
		map[string]any{"name": "mapIt", "parameters": []any{}, "action": map[string]any{"uri": "mapper:map", "options": map[string]any{}}},
	}}
	file := saveProgram(t, dir, "lib.json", programJSON(t, lib, nil))
	caller := map[string]any{"functions": map[string]any{"function": functionDef("private", `"WRONG"`, "string", param)}}
	root := saveProgram(t, dir, "root.json", programJSON(t, caller, map[string]any{"path": "lib.json", "namespace": "lib"},
		programFlow("normal", "", action("processor:lib:normalize", nil)), mappingFlow("mapping", "lib:map"), programFlow("wrapped-map", "", action("processor:lib:mapIt", nil)),
		programFlow("simple", "", action("setheader", map[string]any{"name": "normalized", "language": "simple", "value": `${function('lib:normal', ${header.id})}`}), action("simplevalidator", map[string]any{"expression": `${function('lib:normal', ${header.id})} == 'ABC'`}), action("setbody", map[string]any{"language": "simple", "expression": `${header.normalized}`})),
	))
	prepared, e := api.PrepareProgram(context.Background(), []api.ProgramSource{{Path: root}}, api.ProgramLimits{})
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(file, []byte("changed after prepare"), 0600); e != nil {
		t.Fatal(e)
	}
	info := prepared.Info()
	info.Documents[0].Functions[0].Parameters[0].Name = "mutated"
	if prepared.Info().Documents[0].Functions[0].Parameters[0].Name == "mutated" {
		t.Fatal("mutable metadata")
	}
	data, _ := json.Marshal(prepared.Info())
	if strings.Contains(string(data), "upper(") || strings.Contains(string(data), "WRONG") {
		t.Fatal("source leaked")
	}
	for n := 0; n < 2; n++ {
		p, e := prepared.Load(context.Background(), nil, nil, nil)
		if e != nil {
			t.Fatal(e)
		}
		for _, f := range p.Flows {
			out := runFunctionFlow(t, f, api.Message{api.Body: `{"id":" abc "}`, "id": " abc "})
			want := "ABC"
			if f.ID() == "mapping" || f.ID() == "wrapped-map" {
				want = `{"id":"ABC"}`
			}
			if out[api.Body] != want {
				t.Fatalf("%s: %v", f.ID(), out)
			}
		}
	}
}
func TestProcessorArgumentsAndLocalFunctions(t *testing.T) {
	core := map[string]any{
		"functions":  map[string]any{"function": functionDef("f", `upper(v)`, "string", map[string]any{"name": "v", "type": "string"})},
		"processors": map[string]any{"processor": map[string]any{"name": "set", "parameters": []any{map[string]any{"name": "text", "type": "string", "default": "default"}}, "action": map[string]any{"uri": "setbody", "options": map[string]any{"language": "constant", "expression": map[string]any{"$param": "text"}}}}},
	}
	for _, args := range []map[string]any{nil, {"text": "value"}} {
		options := map[string]any{}
		if args != nil {
			options["arguments"] = args
		}
		data := programJSON(t, core, nil, programFlow("local", "", action("processor:set", options)))
		f, e := api.LoadBytes(data, nil)
		if e != nil {
			t.Fatal(e)
		}
		out := runFunctionFlow(t, f, api.Message{})
		want := "default"
		if args != nil {
			want = "value"
		}
		if out[api.Body] != want {
			t.Fatal(out)
		}
	}
	for _, args := range []map[string]any{{"text": nil}, {"text": 1}, {"typo": "x"}} {
		data := programJSON(t, core, nil, programFlow("invalid", "", action("processor:set", map[string]any{"arguments": args})))
		if _, e := api.LoadBytes(data, nil); e == nil {
			t.Fatalf("accepted %#v", args)
		}
	}
	data := programJSON(t, core, nil, programFlow("dynamic", "", action("simplereplace", nil)))
	f, e := api.LoadBytes(data, nil)
	if e != nil {
		t.Fatal(e)
	}
	out := runFunctionFlow(t, f, api.Message{api.Body: `${function('f', 'yes')}`})
	if out[api.Body] != "YES" {
		t.Fatal(out)
	}
}
func TestFunctionPreparationRejectsUnusedAndInvalidDefinitions(t *testing.T) {
	cases := []map[string]any{
		{"functions": map[string]any{"function": functionDef("f", `function("f")`, "any")}},
		{"functions": map[string]any{"function": []any{functionDef("a", `function("b")`, "any"), functionDef("b", `function("a")`, "any")}}},
		{"functions": map[string]any{"function": functionDef("f", `payload`, "any")}},
		{"functions": map[string]any{"function": functionDef("f", `function("missing")`, "any")}},
		{"functions": map[string]any{"function": []any{functionDef("f", `1`, "number"), functionDef("f", `2`, "number")}}},
	}
	for _, uri := range []string{"processor:other", "timer", "filter", "deadletter", "missing"} {
		cases = append(cases, map[string]any{"processors": map[string]any{"processor": map[string]any{"name": "unused", "parameters": []any{}, "action": map[string]any{"uri": uri, "options": map[string]any{}}}}})
	}
	cases = append(cases, map[string]any{"processors": map[string]any{"processor": map[string]any{"name": "unused", "parameters": []any{}, "action": map[string]any{"uri": "setbody", "options": map[string]any{"typo": "PRIVATE"}}}}})
	for i, core := range cases {
		data := programJSON(t, core, nil, programFlow("invalid", ""))
		if _, e := api.PrepareProgram(context.Background(), []api.ProgramSource{{Data: data}}, api.ProgramLimits{}); e == nil || strings.Contains(e.Error(), "PRIVATE") {
			t.Fatalf("case %d: %v", i, e)
		}
		if _, e := api.LoadBytes(data, nil); e == nil {
			t.Fatalf("local accepted case %d", i)
		}
	}
	data := programJSON(t, nil, nil, programFlow("invalid-call", "", action("setbody", map[string]any{"language": "simple", "expression": `${function('missing', 'PRIVATE')}`})))
	if _, e := api.PrepareProgram(context.Background(), []api.ProgramSource{{Data: data}}, api.ProgramLimits{}); e == nil || strings.Contains(e.Error(), "PRIVATE") {
		t.Fatal(e)
	}
}
func TestFunctionLocalColonPrecedenceAndProgramLimits(t *testing.T) {
	dir := t.TempDir()
	saveProgram(t, dir, "lib.json", programJSON(t, map[string]any{"functions": map[string]any{"function": functionDef("f", `"imported"`, "string")}}, nil))
	core := map[string]any{"functions": map[string]any{"function": functionDef("lib:f", `"local"`, "string")}}
	root := saveProgram(t, dir, "root.json", programJSON(t, core, map[string]any{"namespace": "lib", "path": "lib.json"}, programFlow("precedence", "", action("setbody", map[string]any{"language": "simple", "expression": `${function('lib:f')}`}))))
	p, e := api.LoadProgram(root, nil)
	if e != nil {
		t.Fatal(e)
	}
	if out := runFunctionFlow(t, p.Flows[0], api.Message{}); out[api.Body] != "local" {
		t.Fatal(out)
	}
	for _, limits := range []api.MappingLimits{{Functions: 1}, {TotalSourceBytes: 2}, {CallDepth: -1}} {
		if _, e := api.PrepareProgram(context.Background(), []api.ProgramSource{{Path: root}}, api.ProgramLimits{Mapping: limits}); e == nil {
			t.Fatal("ignored limit", limits)
		}
	}
}
func TestConstantFunctionTextRemainsLiteral(t *testing.T) {
	data := programJSON(t, nil, nil, programFlow("constant", "", action("setbody", map[string]any{"expression": `${function('undefined','literal')}`})))
	f, e := api.LoadBytes(data, nil)
	if e != nil {
		t.Fatal(e)
	}
	out := runFunctionFlow(t, f, api.Message{})
	if out[api.Body] != `${function('undefined','literal')}` {
		t.Fatal(out)
	}
	core := map[string]any{"functions": map[string]any{"function": functionDef("n", `v`, "number", map[string]any{"name": "v", "type": "number"})}}
	data = programJSON(t, core, nil, programFlow("bad-literal", "", action("setbody", map[string]any{"language": "simple", "expression": `${function('n','SECRET')}`})))
	if _, e := api.PrepareProgram(context.Background(), []api.ProgramSource{{Data: data}}, api.ProgramLimits{}); e == nil || strings.Contains(e.Error(), "SECRET") {
		t.Fatal(e)
	}
}

func TestFunctionExamples(t *testing.T) {
	p, e := api.LoadProgram("../../api/testdata/functions/orders.json", nil)
	if e != nil {
		t.Fatal(e)
	}
	want := map[string]string{"function-simple": "ACME", "function-invoice": `{"customer":"ACME","total":0.3}`, "function-summary": `{"customer":"ACME","labels":["new","paid"]}`, "function-text": "Configured once, instantiated per step"}
	for _, f := range p.Flows {
		out := runFunctionFlow(t, f, f.NewMessage())
		if out[api.Body] != want[f.ID()] {
			t.Fatalf("%s: %v", f.ID(), out)
		}
	}
}

type templateCounter struct{ n atomic.Int64 }

func (p *templateCounter) Process(_ context.Context, m api.Message) (api.Message, error) {
	m[api.Body] = p.n.Add(1)
	return m, nil
}
func TestReusableActionInstancesAndValidation(t *testing.T) {
	name := fmt.Sprintf("templatecounter%d", programTestCounter.Add(1))
	var constructed atomic.Int64
	if e := api.RegisterStep(api.StepDefinition{Name: name, Kind: stepdef.Action, Schema: []byte(`{"type":"object","additionalProperties":false}`), New: func(string, stepdef.Params) (stepdef.Processor, error) {
		constructed.Add(1)
		return &templateCounter{}, nil
	}}); e != nil {
		t.Fatal(e)
	}
	core := map[string]any{"processors": map[string]any{"processor": map[string]any{"name": "counter", "parameters": []any{}, "action": map[string]any{"uri": name}}}}
	data := programJSON(t, core, nil, programFlow("counts", "", action("processor:counter", nil), action("processor:counter", nil)))
	prepared, e := api.PrepareProgram(context.Background(), []api.ProgramSource{{Data: data}}, api.ProgramLimits{})
	if e != nil {
		t.Fatal(e)
	}
	if constructed.Load() != 0 {
		t.Fatal("prepare constructed actions")
	}
	info := prepared.Info()
	if info.Flows[0].Flow.Steps[1].Processor == nil {
		t.Fatal("missing processor origin")
	}
	info.Flows[0].Flow.Steps[1].Processor.Name = "changed"
	if prepared.Info().Flows[0].Flow.Steps[1].Processor.Name != "counter" {
		t.Fatal("mutable processor metadata")
	}
	for i := 0; i < 2; i++ {
		p, e := prepared.Load(context.Background(), nil, nil, nil)
		if e != nil {
			t.Fatal(e)
		}
		out := runFunctionFlow(t, p.Flows[0], api.Message{})
		if out[api.Body] != int64(1) {
			t.Fatal("shared action state", out)
		}
	}
	if constructed.Load() != 4 {
		t.Fatal("expected independent action per step and load")
	}
	invalid := programJSON(t, core, nil, programFlow("good", "", action("processor:counter", nil)), programFlow("bad", "", action("no-such-action", nil)))
	if _, e := api.PrepareProgram(context.Background(), []api.ProgramSource{{Data: invalid}}, api.ProgramLimits{}); e == nil {
		t.Fatal("accepted invalid group")
	}
	if constructed.Load() != 4 {
		t.Fatal("failed preparation constructed actions")
	}
}
