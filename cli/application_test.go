package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProgramCommands(t *testing.T) {
	path := "../api/testdata/program/orders.json"
	for _, args := range [][]string{
		{"validate", path},
		{"validate", "--dir=../api/testdata/program"},
		{"describe", path, "--output=json"},
		{"validate", "../api/testdata/program/common.json"},
	} {
		var out, diagnostic bytes.Buffer
		if code := Run(args, nil, &out, &diagnostic); code != 0 {
			t.Fatalf("%v: %d: %s", args, code, diagnostic.String())
		}
		if args[0] == "describe" {
			var info struct {
				Documents []any `json:"documents"`
				Flows     []any `json:"flows"`
			}
			if err := json.Unmarshal(out.Bytes(), &info); err != nil || len(info.Flows) != 4 || len(info.Documents) != 3 {
				t.Fatalf("group description: %s (%v)", out.String(), err)
			}
		}
	}
	useLogDir(t)
	var out, diagnostic bytes.Buffer
	input := "run " + path + "\nrequest program-orders\nrequest program-ping\nexit\n"
	if code := Run(nil, strings.NewReader(input), &out, &diagnostic); code != 0 {
		t.Fatalf("shell: %d: %s", code, diagnostic.String())
	}
	for _, want := range []string{"reply from flow program-orders:", `"origin":"shared-core"`, `"body":"pong"`} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %s in %s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "Error:") {
		t.Fatal(out.String())
	}
}

func TestMappingCommands(t *testing.T) {
	path := "../api/testdata/mapping/orders.json"
	for _, args := range [][]string{{"validate", path}, {"validate", "--dir=../api/testdata/mapping"}, {"describe", path}, {"describe", path, "--output=json"}} {
		var out, diagnostic bytes.Buffer
		if code := Run(args, nil, &out, &diagnostic); code != 0 {
			t.Fatalf("%v: %s", args, diagnostic.String())
		}
		if args[0] == "describe" && (!strings.Contains(out.String(), "NormalizeOrder") || strings.Contains(out.String(), "trim(")) {
			t.Fatalf("mapping description: %s", out.String())
		}
	}
	useLogDir(t)
	var out, diagnostic bytes.Buffer
	input := "run " + path + "\nrequest map-order\nrequest map-csv\nexit\n"
	if code := Run(nil, strings.NewReader(input), &out, &diagnostic); code != 0 || strings.Contains(out.String(), "Error:") {
		t.Fatalf("mapping shell: %s %s", out.String(), diagnostic.String())
	}
	for _, want := range []string{"reply from flow map-order:", "9007199254740993", "reply from flow map-csv:", "text/csv"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %s: %s", want, out.String())
		}
	}
}

func TestServiceCommands(t *testing.T) {
	path := "../api/testdata/services/orders.json"
	contracts := "../api/testdata/services/contracts.json"
	for _, args := range [][]string{
		{"validate", path},
		{"validate", contracts},
		{"validate", "--dir=../api/testdata/services"},
		{"describe", path},
		{"describe", path, "--output=json"},
	} {
		var out, diagnostic bytes.Buffer
		if code := Run(args, nil, &out, &diagnostic); code != 0 {
			t.Fatalf("%v: %s", args, diagnostic.String())
		}
		if args[0] == "describe" && len(args) == 2 {
			text := out.String()
			for _, want := range []string{
				"SERVICE: PlaceOrder (InOut); OrderIn -> OrderOut",
				"SERVICE: RecordAudit (InOnly); AuditNote -> none",
				"REFERENCE: service orders:PlaceOrder (step: place-order)",
				"REFERENCE: service contracts:PlaceOrder (step: in)",
			} {
				if !strings.Contains(text, want) {
					t.Fatalf("missing %q in describe:\n%s", want, text)
				}
			}
		}
		if args[0] == "describe" && len(args) == 3 {
			if strings.Contains(out.String(), "OUT_OF_STOCK") {
				t.Fatalf("describe leaked expression literal:\n%s", out.String())
			}
		}
	}
	useLogDir(t)
	var out, diagnostic bytes.Buffer
	input := "run " + path + "\nrequest submit-order\nrequest submit-out-of-stock\nexit\n"
	if code := Run(nil, strings.NewReader(input), &out, &diagnostic); code != 0 || strings.Contains(out.String(), "Error:") {
		t.Fatalf("service shell: %s %s", out.String(), diagnostic.String())
	}
	for _, want := range []string{"reply from flow submit-order:", "ORD-BOOK-1", "ACCEPTED", "reply from flow submit-out-of-stock:", "OUT_OF_STOCK"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %s: %s", want, out.String())
		}
	}
}

func TestExecutableHelpAndCommands(t *testing.T) {
	for _, name := range []string{"help", "--help", "-h"} {
		var out, diagnostic bytes.Buffer
		if code := Run([]string{name}, nil, &out, &diagnostic); code != 0 {
			t.Fatalf("help code %d", code)
		}
		if !strings.Contains(out.String(), "without arguments") || !strings.Contains(out.String(), "At the > prompt") {
			t.Fatal(out.String())
		}
	}
	for _, name := range []string{"start", "shell", "completion"} {
		var out, diagnostic bytes.Buffer
		if code := Run([]string{name}, nil, &out, &diagnostic); code != 2 {
			t.Fatalf("%s accepted outside shell: %d", name, code)
		}
		if out.Len() != 0 || !strings.Contains(diagnostic.String(), "interactive shell") {
			t.Fatal("missing startup guidance")
		}
	}
	for _, args := range [][]string{{"version"}, {"--version"}, {"catalog"}, {"run", "--help"}, {"validate", "../testdata/hello.json"}, {"describe", "../testdata/hello.json"}} {
		var out, diagnostic bytes.Buffer
		if code := Run(args, nil, &out, &diagnostic); code != 0 {
			t.Fatalf("%v: %d: %s", args, code, diagnostic.String())
		}
	}
	for _, args := range [][]string{{"run"}, {"validate"}, {"run", "--file=x", "--shutdown-timeout=0s"}} {
		var out, diagnostic bytes.Buffer
		if code := Run(args, nil, &out, &diagnostic); code != 2 {
			t.Fatalf("%v: %d", args, code)
		}
	}
}

func TestUtilityCommands(t *testing.T) {
	for _, tc := range []struct {
		args []string
		code int
		json bool
	}{
		{[]string{"version", "--help"}, 0, false},
		{[]string{"version", "--output", "json"}, 0, true},
		{[]string{"validate", "../testdata/hello.json", "--output=json"}, 0, true},
		{[]string{"describe", "--output", "json", "../testdata/hello.json"}, 0, true},
		{[]string{"catalog", "log", "--output=json"}, 0, true},
		{[]string{"validate", "../testdata/hello.json", "../testdata/hello.json", "--output=json"}, 1, true},
		{[]string{"validate", "missing.json", "--output=json"}, 1, true},
		{[]string{"describe", "../testdata/hello.json"}, 0, false},
		{[]string{"catalog", "missing-step"}, 1, false},
		{[]string{"validate"}, 2, false},
		{[]string{"version", "--output", "xml"}, 2, false},
		{[]string{"version", "--output"}, 2, false},
		{[]string{"version", "unexpected"}, 2, false},
		{[]string{"unknown"}, 2, false},
		{[]string{"completion", "--help"}, 0, false},
	} {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			var out, diagnostic bytes.Buffer
			code := runUtility(tc.args, &out, &diagnostic)
			if code != tc.code {
				t.Fatalf("code %d, want %d: %s", code, tc.code, diagnostic.String())
			}
			if tc.json && !json.Valid(out.Bytes()) {
				t.Fatalf("invalid JSON: %s", out.String())
			}
			if tc.code == 0 && diagnostic.Len() != 0 {
				t.Fatal(diagnostic.String())
			}
			if tc.code != 0 && diagnostic.Len() == 0 {
				t.Fatal("missing diagnostic")
			}
		})
	}
}

func TestInitTemplatesAndNoOverwrite(t *testing.T) {
	for _, name := range []string{"hello", "timer", "file", "http"} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "flow with spaces.json")
			var out, diagnostic bytes.Buffer
			if code := runUtility([]string{"init", path, "--template", name, "--id", "my-flow"}, &out, &diagnostic); code != 0 {
				t.Fatal(diagnostic.String())
			}
			original, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if code := runUtility([]string{"validate", path}, &out, &diagnostic); code != 0 {
				t.Fatal(diagnostic.String())
			}
			if code := runUtility([]string{"init", path}, &out, &diagnostic); code != 1 {
				t.Fatalf("overwrite code: %d", code)
			}
			after, _ := os.ReadFile(path)
			if !bytes.Equal(original, after) {
				t.Fatal("existing file changed")
			}
		})
	}
}

func TestInteractiveUtilityDiscoveryAndExecution(t *testing.T) {
	useLogDir(t)
	path := filepath.Join(t.TempDir(), "starter with spaces.json")
	quoted := `"` + path + `"`
	input := strings.Join([]string{
		"run ../testdata/hello.json",
		"help", "help init", "help validate", "help describe", "help version", "help completion",
		"init " + quoted + " --id starter",
		"validate " + quoted + " --output json",
		"describe " + quoted + " --output json",
		"run " + quoted, "request starter",
		"version --output json", "catalog passthrough --output json",
		"completion val", "completion init --template t",
		"validate missing.json", `validate "unfinished`,
		"list", "exit", "",
	}, "\n")
	var out, diagnostic bytes.Buffer
	if code := Run(nil, strings.NewReader(input), &out, &diagnostic); code != 0 {
		t.Fatalf("code %d: %s", code, diagnostic.String())
	}
	text := out.String()
	for _, name := range []string{"init", "validate", "describe", "version", "completion"} {
		if !strings.Contains(text, "> help "+name+"\n\nUsage:\n  "+name) {
			t.Fatalf("missing %s help", name)
		}
	}
	for _, fragment := range []string{"Created " + path, `"valid": true`, `"id": "starter"`, "reply from flow starter:", `"body":"Hello from DIF"`, `"version":`, `"Name": "passthrough"`, "> completion val\n\nvalidate", "> completion init --template t\n\ntimer", "missing.json:", "unclosed quote", "\nhello ", "exit: 1 messages processed"} {
		if !strings.Contains(text, fragment) {
			t.Fatalf("missing %q in transcript:\n%s", fragment, text)
		}
	}
	for _, removed := range []string{"OPERATING SYSTEM TERMINAL", "dif run", "dif shell", "unknown command"} {
		if strings.Contains(text, removed) {
			t.Fatalf("obsolete interface %q in transcript", removed)
		}
	}
}

func TestCompletionSuggestions(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  string
	}{
		{"val", "validate"}, {"init --template t", "timer"}, {"describe --output j", "json"},
		{"catalog pass", "passthrough"}, {"help ver", "version"}, {"list st", "started"}, {"stop --f", "--force"},
	} {
		suggestions, err := completionSuggestions(tc.input)
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, s := range suggestions {
			if s == tc.want {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s: %v; want %s", tc.input, suggestions, tc.want)
		}
	}
}

func TestFlowFilenamesDefaultToJSONInShell(t *testing.T) {
	useLogDir(t)
	dir := t.TempDir()
	one, two, explicit := filepath.Join(dir, "first flow"), filepath.Join(dir, "second flow"), filepath.Join(dir, "explicit.dil")
	quote := func(s string) string { return `"` + s + `"` }
	input := strings.Join([]string{
		"init " + quote(one), "init " + quote(two), "init " + quote(explicit),
		"validate " + quote(one) + " " + quote(two) + " " + quote(explicit),
		"describe " + quote(one), "describe " + quote(explicit),
		"load " + quote(one), "run " + quote(two),
		"init " + quote(one+".json"),
		"list", "exit", "",
	}, "\n")
	var out, diagnostic bytes.Buffer
	if code := Run(nil, strings.NewReader(input), &out, &diagnostic); code != 0 {
		t.Fatalf("code %d: %s", code, diagnostic.String())
	}
	text := out.String()
	for _, fragment := range []string{
		"Created " + one + ".json", "Created " + two + ".json", "Created " + explicit,
		one + ".json: valid", two + ".json: valid", explicit + ": valid",
		"FLOW: first flow", "FLOW: explicit",
		"flow first flow loaded from " + one + ".json; it is stopped",
		"flow second flow started (loaded from " + two + ".json)",
	} {
		if !strings.Contains(text, fragment) {
			t.Fatalf("missing %q in transcript:\n%s", fragment, text)
		}
	}
	// The explicit .json name must refer to the same file and be refused by init.
	if !strings.Contains(block(t, text, "init "+quote(one+".json")), "Error:") {
		t.Fatal("init overwrote an existing flow")
	}
	for _, path := range []string{one, two, explicit + ".json"} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("unexpected file %s: %v", path, err)
		}
	}
}
