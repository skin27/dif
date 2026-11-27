package cli

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// TestValidateWithPolicy: dif validate reports what a policy does not grant
// as not-permitted errors, in text and JSON; without a policy nothing changes.
func TestValidateWithPolicy(t *testing.T) {
	path := checkFlow(t, "writer", map[string]any{"uri": "file:outbox"})
	refused := "error not-permitted: flow writer, step second: fs.write outbox is not granted"
	for _, tc := range []struct {
		args []string
		code int
		want []string
	}{
		{[]string{"validate", path}, 0, []string{path + ": valid (structure and option schemas)\n"}},
		{[]string{"validate", path, "--sandbox"}, 1, []string{path + ": invalid: the static checks found 1 error", refused}},
		{[]string{"validate", "--sandbox", path, "--output=json"}, 1, []string{`"valid": false`, `"code": "not-permitted"`, `"step": "second"`, `"message": "fs.write outbox is not granted"`}},
		{[]string{"validate", path, "--allow-fs-write", "outbox"}, 0, []string{path + ": valid (structure and option schemas)\n"}},
		{[]string{"validate", path, "--allow-fs-write=inbox", "--output=json"}, 1, []string{`"code": "not-permitted"`}},
		{[]string{"validate", path, "--allow-fs-write=outbox", "--output=json"}, 0, []string{`"valid": true`}},
	} {
		t.Run(strings.Join(tc.args[2:], " "), func(t *testing.T) {
			var out, diagnostic bytes.Buffer
			code := Run(tc.args, nil, &out, &diagnostic)
			if code != tc.code {
				t.Fatalf("code %d, want %d: %s %s", code, tc.code, out.String(), diagnostic.String())
			}
			for _, want := range tc.want {
				if !strings.Contains(out.String(), want) {
					t.Errorf("missing %q in:\n%s", want, out.String())
				}
			}
			if strings.Contains(strings.Join(tc.args, " "), "--output=json") && !json.Valid(out.Bytes()) {
				t.Errorf("invalid JSON: %s", out.String())
			}
		})
	}
}

// TestDescribeCapabilities: describe shows the capability manifest, one line
// per capability, and in JSON on each step.
func TestDescribeCapabilities(t *testing.T) {
	path := checkFlow(t, "caller", map[string]any{"uri": "https:${header.url}", "options": map[string]any{"trustStoreFile": ""}})
	var out, diagnostic bytes.Buffer
	if code := Run([]string{"describe", path}, nil, &out, &diagnostic); code != 0 {
		t.Fatalf("describe: %d %s", code, diagnostic.String())
	}
	if want := "\nCAPABILITIES (outside the process):\n  second: net.connect * (dynamic)\n"; !strings.Contains(out.String(), want) {
		t.Errorf("missing %q in:\n%s", want, out.String())
	}

	out.Reset()
	if code := Run([]string{"describe", path, "--output=json"}, nil, &out, &diagnostic); code != 0 {
		t.Fatalf("describe json: %d %s", code, diagnostic.String())
	}
	if want := `"capabilities": [`; !strings.Contains(out.String(), want) || !strings.Contains(out.String(), `"dynamic": true`) {
		t.Errorf("missing capabilities in:\n%s", out.String())
	}

	pure := checkFlow(t, "pure", map[string]any{"uri": "xmltojson"})
	out.Reset()
	Run([]string{"describe", pure}, nil, &out, &diagnostic)
	if want := "CAPABILITIES (outside the process):\n  None: the flow touches nothing outside the process.\n"; !strings.Contains(out.String(), want) {
		t.Errorf("missing %q in:\n%s", want, out.String())
	}
}
