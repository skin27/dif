package dilsyntax

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestRoundTrip prints every DIL JSON fixture of the repository as text,
// compiles the text back to JSON and checks that DIF reads the same document:
// the same imports, core declarations, integrations, flows, step options,
// link order, rules, conditions and error handlers. Link ids are generated
// and every list is an array, so documents are compared in canonical form.
func TestRoundTrip(t *testing.T) {
	var files []string
	for _, root := range []string{"../../testdata", "../../api/testdata"} {
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || filepath.Ext(p) != ".json" {
				return err
			}
			files = append(files, p)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	n := 0
	for _, p := range files {
		data, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		var doc map[string]json.RawMessage
		if json.Unmarshal(bytes.TrimPrefix(data, []byte("\xef\xbb\xbf")), &doc) != nil || doc["dil"] == nil {
			continue // not a DIL document, such as a service configuration
		}
		n++
		text, err := Print(data)
		if err != nil {
			t.Errorf("%s: print: %v", p, err)
			continue
		}
		back, err := Compile(p, text)
		if err != nil {
			t.Errorf("%s: compile the printed text: %v\n%s", p, err, text)
			continue
		}
		want, got := canonical(t, data), canonical(t, back)
		if !reflect.DeepEqual(want, got) {
			t.Errorf("%s: the round trip changes the document\nwant %v\ngot  %v", p, want, got)
			continue
		}
		// Printing is canonical: the text of the compiled text is the same.
		again, err := Print(back)
		if err != nil || !bytes.Equal(again, text) {
			t.Errorf("%s: printing the compiled text gives other text (%v)", p, err)
		}
	}
	if n < 100 {
		t.Fatalf("only %d DIL documents found", n)
	}
	t.Logf("%d DIL documents round-trip", n)
}

// canonical returns what DIF reads of a DIL JSON document, in a form that
// does not depend on link ids or on the order of steps, and with lists that
// DIL JSON may write as one object written as lists.
func canonical(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal(bytes.TrimPrefix(data, []byte("\xef\xbb\xbf")), &doc); err != nil {
		t.Fatal(err)
	}
	dil := asMap(doc["dil"])
	out := map[string]any{"imports": list(asMap(dil["imports"])["import"])}
	core := asMap(dil["core"])
	for kind, plural := range declarationKinds {
		items := list(asMap(core[plural])[kind])
		if kind == "message" {
			for i, x := range items {
				m := asMap(x)
				var headers []any
				for _, h := range list(asMap(m["headers"])["header"]) {
					hm := asMap(h)
					headers = append(headers, []string{text(hm["name"]), text(hm["value"]), text(hm["language"])})
				}
				items[i] = map[string]any{"name": m["name"], "body": m["body"], "headers": headers}
			}
		}
		out[plural] = items
	}
	var integrations []any
	for _, x := range list(asMap(dil["integrations"])["integration"]) {
		in := map[string]any{}
		for k, v := range asMap(x) {
			in[k] = v
		}
		var flows []any
		for _, f := range list(asMap(in["flows"])["flow"]) {
			flows = append(flows, canonicalFlow(asMap(f)))
		}
		in["flows"] = flows
		integrations = append(integrations, in)
	}
	out["integrations"] = integrations
	return out
}

func canonicalFlow(f map[string]any) map[string]any {
	options := map[string]any{}
	for k, v := range asMap(f["options"]) {
		if k != "id" && k != "name" {
			options[k] = v
		}
	}
	steps := map[string]any{}
	var errorSteps []string
	byIn := map[string]string{}
	all := list(asMap(f["steps"])["step"])
	for _, x := range all {
		s := asMap(x)
		for _, l := range list(asMap(s["links"])["link"]) {
			if lm := asMap(l); lm["bound"] == "in" {
				byIn[text(lm["id"])] = text(s["id"])
			}
		}
	}
	for _, x := range all {
		s := asMap(x)
		id := text(s["id"])
		if _, dup := steps[id]; dup {
			panic("duplicate step id " + id + " in a fixture")
		}
		var outs []string
		hasIn := false
		for _, l := range list(asMap(s["links"])["link"]) {
			lm := asMap(l)
			if lm["bound"] == "in" {
				hasIn = true
				continue
			}
			route := byIn[text(lm["id"])]
			if s["type"] != "error" {
				route = strings.Join([]string{route, text(lm["rule"]), text(lm["language"]), text(lm["expression"])}, "\x00")
			}
			outs = append(outs, route)
		}
		if s["type"] == "error" {
			errorSteps = append(errorSteps, id)
		}
		options := asMap(s["options"])
		if options == nil {
			options = map[string]any{}
		}
		steps[id] = map[string]any{"type": s["type"], "uri": s["uri"], "options": options, "in": hasIn, "outs": outs}
	}
	return map[string]any{"id": text(f["id"]), "name": f["name"], "options": options, "steps": steps, "errors": errorSteps}
}

func asMap(v any) map[string]any { m, _ := v.(map[string]any); return m }
func text(v any) string          { s, _ := v.(string); return s }
func list(v any) []any {
	switch x := v.(type) {
	case nil:
		return nil
	case []any:
		return x
	}
	return []any{v}
}
