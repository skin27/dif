package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// checkFlow writes a DIL flow of a timer source, the step second and a log
// sink, and returns its path.
func checkFlow(t *testing.T, name string, second map[string]any) string {
	t.Helper()
	link := func(id, bound string) map[string]any { return map[string]any{"id": id, "bound": bound} }
	second["id"], second["type"] = "second", "action"
	second["links"] = map[string]any{"link": []any{link("in-second", "in"), link("second-log", "out")}}
	steps := []any{
		map[string]any{"id": "in", "type": "source", "uri": "timer:t", "links": map[string]any{"link": link("in-first", "out")}},
		map[string]any{"id": "first", "type": "action", "uri": "jsontoxml", "links": map[string]any{"link": []any{link("in-first", "in"), link("in-second", "out")}}},
		second,
		map[string]any{"id": "log", "type": "sink", "uri": "log", "links": map[string]any{"link": link("second-log", "in")}},
	}
	doc := map[string]any{"dil": map[string]any{"integrations": map[string]any{"integration": map[string]any{"flows": map[string]any{"flow": map[string]any{
		"id": name, "name": name, "type": "esb", "steps": map[string]any{"step": steps},
	}}}}}}
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), name+".json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestValidateReportsDiagnostics(t *testing.T) {
	// A JSON validator after jsontoxml fails every message: an error.
	failing := checkFlow(t, "failing", map[string]any{"uri": "validate", "options": map[string]any{"schema": `{"type": "object"}`}})
	// A header nothing sets: a warning.
	warning := checkFlow(t, "warning", map[string]any{"uri": "setbody", "options": map[string]any{"language": "simple", "expression": "${header.orderId}"}})
	clean := checkFlow(t, "clean", map[string]any{"uri": "xmltojson"})

	for _, tc := range []struct {
		args []string
		code int
		want []string
	}{
		{[]string{"validate", failing}, 1, []string{failing + ": invalid: the static checks found 1 error", "error body-mismatch: flow failing, step second: validate needs a JSON body, but the body is XML here (from step first)"}},
		{[]string{"validate", warning}, 0, []string{warning + ": valid (structure and option schemas); the static checks found 1 warning", `warning header-not-set: flow warning, step second: setbody reads header "orderId", which no step before it sets`}},
		{[]string{"validate", "--strict", warning}, 1, []string{warning + ": invalid: the static checks found 1 warning"}},
		{[]string{"validate", warning, "--strict=false"}, 0, []string{"valid"}},
		{[]string{"validate", "--strict", clean}, 0, []string{clean + ": valid (structure and option schemas)\n"}},
		{[]string{"validate", failing, "--output=json"}, 1, []string{`"valid": false`, `"code": "body-mismatch"`, `"severity": "error"`, `"error": "the static checks found 1 error"`}},
		{[]string{"validate", clean, "--output=json"}, 0, []string{`"valid": true`}},
	} {
		// The shell's validate and dif validate behave alike.
		for _, run := range []struct {
			name string
			run  func([]string, *bytes.Buffer, *bytes.Buffer) int
		}{
			{"shell", func(args []string, out, diagnostic *bytes.Buffer) int { return runUtility(args, out, diagnostic) }},
			{"executable", func(args []string, out, diagnostic *bytes.Buffer) int { return Run(args, nil, out, diagnostic) }},
		} {
			t.Run(run.name+" "+strings.Join(tc.args[1:], " "), func(t *testing.T) {
				var out, diagnostic bytes.Buffer
				code := run.run(tc.args, &out, &diagnostic)
				if code != tc.code {
					t.Fatalf("code %d, want %d: %s %s", code, tc.code, out.String(), diagnostic.String())
				}
				for _, want := range tc.want {
					if !strings.Contains(out.String(), want) {
						t.Errorf("missing %q in:\n%s", want, out.String())
					}
				}
				if (code != 0) != strings.Contains(diagnostic.String(), "Error: the static checks found") {
					t.Errorf("stderr: %q", diagnostic.String())
				}
				if tc.args[len(tc.args)-1] == "--output=json" && !json.Valid(out.Bytes()) {
					t.Errorf("invalid JSON: %s", out.String())
				}
			})
		}
	}
}

func TestStrictFlag(t *testing.T) {
	for _, tc := range []struct {
		args   []string
		rest   string
		strict bool
		err    bool
	}{
		{[]string{"a.json"}, "a.json", false, false},
		{[]string{"--strict", "a.json"}, "a.json", true, false},
		{[]string{"a.json", "-strict=false"}, "a.json", false, false},
		{[]string{"--strict=maybe"}, "", false, true},
		{[]string{"--", "--strict"}, "-- --strict", false, false},
	} {
		rest, strict, err := strictFlag(tc.args)
		if strings.Join(rest, " ") != tc.rest || strict != tc.strict || (err != nil) != tc.err {
			t.Errorf("strictFlag(%v) = %v, %v, %v", tc.args, rest, strict, err)
		}
	}
}
