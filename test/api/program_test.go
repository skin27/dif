package api_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"dif/api"
	"dif/engine"
	"dif/message"
	stepdef "dif/steps/definition"
)

func programFlow(id, input string, actions ...map[string]any) map[string]any {
	nodes := []map[string]any{{"id": "in", "type": "source", "uri": "message"}}
	nodes = append(nodes, actions...)
	nodes = append(nodes, map[string]any{"id": "out", "type": "sink", "uri": "passthrough"})
	for i, n := range nodes {
		if n["id"] == nil {
			n["id"] = fmt.Sprint("action", i)
		}
		links := []map[string]any{}
		if i > 0 {
			links = append(links, map[string]any{"id": fmt.Sprint(i), "bound": "in"})
		}
		if i < len(nodes)-1 {
			links = append(links, map[string]any{"id": fmt.Sprint(i + 1), "bound": "out"})
		}
		n["links"] = map[string]any{"link": links}
	}
	return map[string]any{"id": id, "options": map[string]any{"inputMessage": input}, "steps": map[string]any{"step": nodes}}
}

func TestProgramSchemaDiagnosticsOmitValues(t *testing.T) {
	f := programFlow("invalid", "")
	nodes := f["steps"].(map[string]any)["step"].([]map[string]any)
	nodes[0]["uri"] = "timer"
	nodes[0]["options"] = map[string]any{"period": "PRIVATE-VALUE"}
	data := programJSON(t, nil, nil, f)
	_, err := api.PrepareProgram(context.Background(), []api.ProgramSource{{Data: data}}, api.ProgramLimits{})
	if err == nil || strings.Contains(err.Error(), "PRIVATE-VALUE") || !strings.Contains(err.Error(), "option period") {
		t.Fatalf("unsafe or unhelpful schema diagnostic: %v", err)
	}
	// The single-flow adapter retains its existing diagnostic format.
	if _, err := api.InspectBytes(data); err == nil || !strings.Contains(err.Error(), "PRIVATE-VALUE") {
		t.Fatalf("legacy diagnostic changed: %v", err)
	}
}
func programJSON(t *testing.T, core any, imports any, flows ...map[string]any) []byte {
	t.Helper()
	dil := map[string]any{}
	if core != nil {
		dil["core"] = core
	}
	if imports != nil {
		dil["imports"] = map[string]any{"import": imports}
	}
	if len(flows) > 0 {
		dil["integrations"] = map[string]any{"integration": map[string]any{"id": "integration", "flows": map[string]any{"flow": flows}}}
	}
	b, err := json.Marshal(map[string]any{"dil": dil})
	if err != nil {
		t.Fatal(err)
	}
	return b
}
func saveProgram(t *testing.T, dir, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}
func coreMessage(name, body string) map[string]any {
	return map[string]any{"messages": map[string]any{"message": map[string]any{"name": name, "body": body}}}
}

func TestProgramExampleRunsWithUnchangedIDs(t *testing.T) {
	path := "../../api/testdata/program/orders.json"
	p, err := api.LoadProgram(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Flows) != 4 || len(p.Info.Documents) != 3 {
		t.Fatalf("flows/documents: %d/%d", len(p.Flows), len(p.Info.Documents))
	}
	if p.Info.Documents[0].Integrations[1].Version != "2" {
		t.Fatal("integration metadata lost")
	}
	byID := map[string]*api.Flow{}
	runners := []*engine.Runner{}
	for _, f := range p.Flows {
		byID[f.ID()] = f
		runners = append(runners, f.Runner)
		t.Cleanup(func() { f.ForceStop() })
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := engine.StartGroup(ctx, runners); err != nil {
		t.Fatal(err)
	}
	if !api.FlowBindings(runners)["program-worker"] {
		t.Fatal("worker not recognized as a flowlink binding")
	}
	orders := byID["program-orders"]
	reply, err := orders.Request(ctx, orders.NewMessage())
	if err != nil {
		t.Fatal(err)
	}
	if reply["origin"] != "shared-core" || reply[api.Body] != `{"customer":"Alice"}` {
		t.Fatalf("reply: %#v", reply)
	}
	if byID["program-idle"].NewMessage()[api.Body] != "pong" {
		t.Fatal("document-local first message default lost")
	}
	if byID["program-worker"].NewMessage()[api.Body] != nil {
		t.Fatal("imported message became the worker default")
	}
	if _, err := api.Load(path, nil); err == nil || !strings.Contains(err.Error(), "program loader") {
		t.Fatalf("legacy imports: %v", err)
	}
	encoded, _ := json.Marshal(p.Info)
	if strings.Contains(string(encoded), "Alice") || strings.Contains(string(encoded), "dummy") || strings.Contains(string(encoded), "required\\") {
		t.Fatal("description exposed values")
	}
}

func TestProgramMultipleFlowsLegacyAdapterAndInputs(t *testing.T) {
	data := programJSON(t, coreMessage("default", "local"), nil, programFlow("a", ""), programFlow("b", "default"))
	if _, err := api.LoadBytes(data, nil); err == nil || !strings.Contains(err.Error(), "exactly one flow") {
		t.Fatalf("legacy multi-flow: %v", err)
	}
	p, err := api.PrepareProgram(context.Background(), []api.ProgramSource{{Name: "memory", Data: data}}, api.ProgramLimits{})
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := p.Load(context.Background(), nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range loaded.Flows {
		if f.NewMessage()[api.Body] != "local" {
			t.Fatal("input default")
		}
	}
	info := p.Info()
	info.Flows[0].Flow.ID = "changed"
	info.Documents[0].Integrations[0].ID = "changed"
	if p.Info().Flows[0].Flow.ID != "a" || p.Info().Documents[0].Integrations[0].ID != "integration" {
		t.Fatal("mutable inspection metadata")
	}
}

func TestProgramSnapshotScopeAndLocalColonPrecedence(t *testing.T) {
	dir := t.TempDir()
	child := saveProgram(t, dir, "child.json", programJSON(t, coreMessage("sample", "child"), nil, programFlow("child", "")))
	imports := map[string]any{"path": "child.json", "namespace": "common"}
	root := saveProgram(t, dir, "root.json", programJSON(t, coreMessage("common:sample", "parent"), imports, programFlow("parent", "common:sample")))
	p, err := api.PrepareProgram(context.Background(), []api.ProgramSource{{Path: root}}, api.ProgramLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(child, []byte("changed after preparation"), 0600); err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		loaded, err := p.Load(context.Background(), nil, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range loaded.Flows {
			want := "parent"
			if f.ID() == "child" {
				want = "child"
			}
			if f.NewMessage()[api.Body] != want {
				t.Fatalf("%s scope: %#v", f.ID(), f.NewMessage())
			}
		}
	}
}

func TestProgramReferenceFailuresAndImports(t *testing.T) {
	for _, tc := range []struct {
		name string
		data []byte
		want string
	}{
		{"missing input", programJSON(t, nil, nil, programFlow("a", "missing")), "inputMessage"},
		{"missing resource", programJSON(t, nil, nil, programFlow("a", "", map[string]any{"type": "action", "uri": "jsonvalidator:ref:missing"})), "resource"},
		{"wrong kind", programJSON(t, coreMessage("schema", "{}"), nil, programFlow("a", "", map[string]any{"type": "action", "uri": "jsonvalidator:ref:schema"})), "resource"},
		{"anonymous import", programJSON(t, nil, map[string]any{"path": "child.json", "namespace": "lib"}), "explicit local origin"},
		{"bad alias", programJSON(t, nil, map[string]any{"path": "child.json", "namespace": "bad:alias"}), "namespace"},
		{"unknown import option", programJSON(t, nil, map[string]any{"path": "child.json", "namespace": "lib", "typo": true}), "invalid import"},
		{"duplicate core", programJSON(t, map[string]any{"resources": map[string]any{"resource": []any{map[string]any{"name": "x"}, map[string]any{"name": "x"}}}}, nil), "duplicate resource"},
		{"duplicate flow", programJSON(t, nil, nil, programFlow("same", ""), programFlow("same", "")), "duplicate flow"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := api.PrepareProgram(context.Background(), []api.ProgramSource{{Data: tc.data}}, api.ProgramLimits{})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %s", err, tc.want)
			}
		})
	}
}

func TestProgramFlowlinkValidationAndHostBindings(t *testing.T) {
	action := map[string]any{"type": "action", "uri": "flowlink", "options": map[string]any{"targetFlowId": "external"}}
	p, err := api.PrepareProgram(context.Background(), []api.ProgramSource{{Data: programJSON(t, nil, nil, programFlow("caller", "", action))}}, api.ProgramLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if err := p.CheckFlowLinks(nil); err == nil {
		t.Fatal("missing target accepted")
	}
	if err := p.CheckFlowLinks(map[string]bool{"external": false}); err == nil {
		t.Fatal("incompatible target accepted")
	}
	if _, err := p.Load(context.Background(), nil, nil, map[string]bool{"external": true}); err != nil {
		t.Fatal(err)
	}
}

var programTestCounter atomic.Int64

type programPass struct{}

func (programPass) Process(_ context.Context, m message.Message) (message.Message, error) {
	return m, nil
}

func TestProgramValidatesAllBeforeConstructing(t *testing.T) {
	var calls atomic.Int64
	name := fmt.Sprintf("programconstruct%d", programTestCounter.Add(1))
	if err := api.RegisterStep(api.StepDefinition{Name: name, Kind: stepdef.Action, Schema: []byte(`{"type":"object","additionalProperties":false}`), New: func(string, stepdef.Params) (stepdef.Processor, error) { calls.Add(1); return programPass{}, nil }}); err != nil {
		t.Fatal(err)
	}
	valid := programFlow("valid", "", map[string]any{"type": "action", "uri": name})
	invalid := programFlow("invalid", "", map[string]any{"type": "action", "uri": "nonexistent-program-test"})
	if _, err := api.PrepareProgram(context.Background(), []api.ProgramSource{{Data: programJSON(t, nil, nil, valid, invalid)}}, api.ProgramLimits{}); err == nil {
		t.Fatal("invalid step accepted")
	}
	if calls.Load() != 0 {
		t.Fatal("validation constructed a processor")
	}
	p, err := api.PrepareProgram(context.Background(), []api.ProgramSource{{Data: programJSON(t, nil, nil, valid)}}, api.ProgramLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 {
		t.Fatal("valid inspection constructed a processor")
	}
	if _, err := p.Load(context.Background(), nil, nil, nil); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatal("processor not constructed at load")
	}
}

func TestProgramConnectionsAndCoreOnlyInspection(t *testing.T) {
	dir := t.TempDir()
	core := map[string]any{"connections": map[string]any{"connection": map[string]any{"id": "db", "type": "jdbc", "keys": map[string]any{"dbtype": "postgres", "host": "localhost", "dbname": "dummy", "password": "private-test-value"}}}}
	common := saveProgram(t, dir, "common.json", programJSON(t, core, nil))
	info, err := api.InspectProgram(common)
	if err != nil || len(info.Flows) != 0 {
		t.Fatalf("core inspection: %v", err)
	}
	if _, err := api.LoadProgram(common, nil); err == nil || !strings.Contains(err.Error(), "no flows") {
		t.Fatalf("core run: %v", err)
	}
	action := map[string]any{"type": "action", "uri": "sql2", "options": map[string]any{"dataSource": "shared:db", "query": "select 1"}}
	root := saveProgram(t, dir, "root.json", programJSON(t, nil, map[string]any{"namespace": "shared", "path": "common.json"}, programFlow("sql", "", action)))
	info, err = api.InspectProgram(root)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(info)
	if strings.Contains(string(b), "private-test-value") {
		t.Fatal("connection secret in description")
	}
	if !strings.Contains(string(b), "shared:db") {
		t.Fatal("missing connection reference metadata")
	}
}
