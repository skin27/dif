package api_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"dif/api"
	"dif/engine"
	"dif/internal/dilsyntax"
)

// TestDILTextFlowRepliesAsItsJSONTwin loads testdata/hello.dil, the text twin
// of hello.json, through the public API and checks that it replies the same.
func TestDILTextFlowRepliesAsItsJSONTwin(t *testing.T) {
	reply := func(load func() (*api.Flow, error)) api.Message {
		t.Helper()
		f, err := load()
		if err != nil {
			t.Fatal(err)
		}
		if err := f.Start(); err != nil {
			t.Fatal(err)
		}
		defer f.Stop()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		m, err := f.Request(ctx, f.NewMessage())
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	want := reply(func() (*api.Flow, error) { return api.Load("../../testdata/hello.json", nil) })
	text, err := os.ReadFile("../../testdata/hello.dil")
	if err != nil {
		t.Fatal(err)
	}
	for name, load := range map[string]func() (*api.Flow, error){
		"Load":      func() (*api.Flow, error) { return api.Load("../../testdata/hello.dil", nil) },
		"LoadBytes": func() (*api.Flow, error) { return api.LoadBytes(text, nil) },
	} {
		got := reply(load)
		for _, key := range []string{api.Body, "greeting", "metadata.trail", "metadata.step"} {
			if got[key] != want[key] {
				t.Errorf("%s: %s = %v, want %v", name, key, got[key], want[key])
			}
		}
	}
	if _, err := api.Inspect("../../testdata/hello.dil"); err != nil {
		t.Fatal(err)
	}
	if _, err := api.InspectBytes(text); err != nil {
		t.Fatal(err)
	}
}

// TestDILTextProgramRunsAsItsJSONTwin runs api/testdata/dil, the services
// program written as text, and the JSON program, and compares the replies.
func TestDILTextProgramRunsAsItsJSONTwin(t *testing.T) {
	want := runOrders(t, "../../api/testdata/services/orders.json")
	got := runOrders(t, "../../api/testdata/dil/orders.dil")
	for id := range want {
		for _, key := range []string{api.Body, "Order-Status", "Content-Type", "error.body"} {
			if got[id][key] != want[id][key] {
				t.Errorf("%s: %s = %v, want %v", id, key, got[id][key], want[id][key])
			}
		}
	}
}

// runOrders runs the order program at path and returns the replies of its two
// client flows.
func runOrders(t *testing.T, path string) map[string]api.Message {
	t.Helper()
	rt, err := api.NewRuntime(api.ChannelConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	prog, err := rt.LoadProgram(path, nil)
	if err != nil {
		t.Fatalf("%s: %v", path, err)
	}
	runners := []*engine.Runner{}
	byID := map[string]*api.Flow{}
	for _, f := range prog.Flows {
		runners = append(runners, f.Runner)
		byID[f.ID()] = f
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := engine.StartGroup(ctx, runners); err != nil {
		t.Fatal(err)
	}
	defer func() {
		for _, f := range prog.Flows {
			_ = f.StopContext(ctx)
		}
	}()
	replies := map[string]api.Message{}
	for _, id := range []string{"submit-order", "submit-out-of-stock"} {
		m, err := byID[id].Request(ctx, byID[id].NewMessage())
		if err != nil {
			t.Fatalf("%s %s: %v", path, id, err)
		}
		replies[id] = m
	}
	return replies
}

var importLine = regexp.MustCompile(`(?m)^(import "[^"]*)\.json(" as )`)

// convertDir writes each DIL JSON file of dir as DIL text into a new
// directory, with imports of .json files changed to the .dil files.
func convertDir(t *testing.T, dir string) string {
	t.Helper()
	out := t.TempDir()
	files, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("%s: %v", dir, err)
	}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		text, err := dilsyntax.Print(data)
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		text = importLine.ReplaceAll(text, []byte("${1}.dil${2}"))
		name := strings.TrimSuffix(filepath.Base(f), ".json") + ".dil"
		if err := os.WriteFile(filepath.Join(out, name), text, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

// inspection returns the description of the program at path, or its error,
// as JSON in which the document names do not depend on the directory or the
// extension.
func inspection(t *testing.T, path string) string {
	t.Helper()
	info, err := api.InspectProgram(path)
	var text string
	if err != nil {
		text = "error: " + err.Error()
	} else {
		data, err := json.MarshalIndent(info, "", " ")
		if err != nil {
			t.Fatal(err)
		}
		text = string(data)
	}
	dir := filepath.Dir(path)
	for _, d := range []string{dir, evalSymlinks(dir)} {
		escaped, _ := json.Marshal(d)
		text = strings.ReplaceAll(text, strings.Trim(string(escaped), `"`), "DIR")
		text = strings.ReplaceAll(text, d, "DIR")
	}
	return strings.NewReplacer(".json", ".DOC", ".dil", ".DOC").Replace(text)
}

func evalSymlinks(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

// TestDILTextProgramsInspectAsTheirJSONTwins converts every program fixture
// to DIL text, imports and all, and checks that DIF describes each document
// of it as it describes the JSON.
func TestDILTextProgramsInspectAsTheirJSONTwins(t *testing.T) {
	for _, name := range []string{"program", "services", "mapping", "functions", "connections"} {
		dir, err := filepath.Abs(filepath.Join("../../api/testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		textDir := convertDir(t, dir)
		files, _ := filepath.Glob(filepath.Join(dir, "*.json"))
		for _, f := range files {
			want := inspection(t, f)
			got := inspection(t, filepath.Join(textDir, strings.TrimSuffix(filepath.Base(f), ".json")+".dil"))
			if got != want {
				t.Errorf("%s/%s: the text describes otherwise\n got %s\nwant %s", name, filepath.Base(f), got, want)
			}
		}
	}
}

// TestMixedProgram imports DIL JSON from DIL text and DIL text from DIL JSON.
func TestMixedProgram(t *testing.T) {
	src, err := filepath.Abs("../../api/testdata/services")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	copyFile := func(name string, edit func([]byte) []byte) {
		data, err := os.ReadFile(filepath.Join(src, name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), edit(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	same := func(b []byte) []byte { return b }
	copyFile("contracts.json", same)
	// orders.json imports provider.dil, which imports contracts.json.
	copyFile("orders.json", func(b []byte) []byte {
		return []byte(strings.Replace(string(b), `"provider.json"`, `"provider.dil"`, 1))
	})
	data, err := os.ReadFile(filepath.Join(src, "provider.json"))
	if err != nil {
		t.Fatal(err)
	}
	text, err := dilsyntax.Print(data)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(text), `import "contracts.json" as contracts`) {
		t.Fatalf("provider.dil:\n%s", text)
	}
	if err := os.WriteFile(filepath.Join(dir, "provider.dil"), text, 0o644); err != nil {
		t.Fatal(err)
	}
	if got, want := inspection(t, filepath.Join(dir, "orders.json")), inspection(t, filepath.Join(src, "orders.json")); got != want {
		t.Fatalf("mixed program describes otherwise\n got %s\nwant %s", got, want)
	}
	replies := runOrders(t, filepath.Join(dir, "orders.json"))
	if body := replies["submit-order"][api.Body]; !strings.Contains(body.(string), "ORD-BOOK-1") {
		t.Fatalf("submit-order: %v", body)
	}
}

// TestDILTextErrorsNameThePosition checks that a program with an error in an
// imported text file names that file, line and column.
func TestDILTextErrorsNameThePosition(t *testing.T) {
	dir := t.TempDir()
	write := func(name, text string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("main.dil", "import \"lib.dil\" as lib\nflow f { message -> passthrough }\n")
	write("lib.dil", "message m {\n  body: secret-body\n}\n")
	_, err := api.InspectProgram(filepath.Join(dir, "main.dil"))
	if err == nil || !strings.Contains(err.Error(), "lib.dil:2:9: expected a value") || strings.Contains(err.Error(), "secret-body") {
		t.Fatalf("error: %v", err)
	}
}
