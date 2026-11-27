package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConvertCommand(t *testing.T) {
	dir := t.TempDir()
	text, back := filepath.Join(dir, "hello.dil"), filepath.Join(dir, "hello.json")
	run := func(want int, args ...string) string {
		t.Helper()
		var out, diagnostic bytes.Buffer
		if code := Run(args, nil, &out, &diagnostic); code != want {
			t.Fatalf("%v: code %d, want %d: %s%s", args, code, want, out.String(), diagnostic.String())
		}
		return out.String() + diagnostic.String()
	}

	// JSON to text on the screen, by default; the extension .json is the default.
	if out := run(0, "convert", "../testdata/hello"); !strings.Contains(out, "flow hello(name: \"hello\") {") || !strings.Contains(out, "on error as hello-error") {
		t.Fatalf("convert to text:\n%s", out)
	}
	// To a new file, which convert never overwrites.
	if out := run(0, "convert", "../testdata/hello.json", "--output", text); !strings.Contains(out, "Created "+text) {
		t.Fatal(out)
	}
	if out := run(1, "convert", "../testdata/hello.json", "--output", text); !strings.Contains(out, "Error:") {
		t.Fatal(out)
	}
	// The text validates and converts back to JSON.
	run(0, "validate", text)
	run(0, "convert", text, "--to", "json", "--output", back)
	data, err := os.ReadFile(back)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil || doc["dil"] == nil {
		t.Fatalf("converted JSON: %v\n%s", err, data)
	}
	run(0, "validate", back)
	// A document is not converted to its own format; --to is dil or json.
	if out := run(1, "convert", text, "--to", "dil"); !strings.Contains(out, "is DIL text already") {
		t.Fatal(out)
	}
	if out := run(1, "convert", back, "--to", "json"); !strings.Contains(out, "is DIL JSON already") {
		t.Fatal(out)
	}
	if out := run(2, "convert", back, "--to", "xml"); !strings.Contains(out, "--to must be dil or json") {
		t.Fatal(out)
	}
	// Errors in text name the file, line and column, never a value.
	broken := filepath.Join(dir, "broken.dil")
	if err := os.WriteFile(broken, []byte("flow f {\n  timer(period: secret-value) as t\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out := run(1, "convert", broken); !strings.Contains(out, broken+":2:17: expected a value") || strings.Contains(out, "secret-value") {
		t.Fatal(out)
	}
}

func TestDILTextCommands(t *testing.T) {
	path := "../api/testdata/dil/orders.dil"
	for _, args := range [][]string{{"validate", path}, {"validate", "--dir=../api/testdata/dil"}, {"describe", path}} {
		var out, diagnostic bytes.Buffer
		if code := Run(args, nil, &out, &diagnostic); code != 0 {
			t.Fatalf("%v: %s", args, diagnostic.String())
		}
	}
	useLogDir(t)
	dir := t.TempDir()
	var out, diagnostic bytes.Buffer
	input := "run " + path + "\nrequest submit-order\nrequest submit-out-of-stock\n" +
		"convert ../testdata/hello.json --output " + filepath.Join(dir, "hello.dil") + "\nrun " + filepath.Join(dir, "hello.dil") + "\nrequest hello\nexit\n"
	if code := Run(nil, strings.NewReader(input), &out, &diagnostic); code != 0 || strings.Contains(out.String(), "Error:") {
		t.Fatalf("shell: %s %s", out.String(), diagnostic.String())
	}
	for _, want := range []string{"ORD-BOOK-1", "ACCEPTED", "OUT_OF_STOCK", "Created " + filepath.Join(dir, "hello.dil"), "reply from flow hello:", "HELLO WORLD"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %s: %s", want, out.String())
		}
	}
	for input, want := range map[string]string{"convert x --to ": "dil", "convert --o": "--output", "conv": "convert"} {
		suggestions, err := completionSuggestions(input)
		if err != nil || !strings.Contains(strings.Join(suggestions, " "), want) {
			t.Fatalf("%s: %v %v", input, suggestions, err)
		}
	}
}
