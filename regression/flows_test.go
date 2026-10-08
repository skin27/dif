package regression

import (
	"os"
	"strings"
	"testing"

	"dif/api"
	"dif/regression/sanitize"
)

// TestFlowsLoad builds every flow that is not skipped, as `dif run` does before
// it starts one, and compares the flows that build with loadable.json.
func TestFlowsLoad(t *testing.T) {
	skips := loadSkips(t)
	t.Chdir(workdir) // processors may create directories and read security/ relative to here

	var loaded []string
	failed := map[string]string{}
	var nSkipped int
	for _, f := range flowFiles(t) {
		if _, ok := skips[f]; ok {
			nSkipped++
			continue
		}
		data, err := os.ReadFile(path(f))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := api.LoadBytes(data, nil); err != nil {
			failed[f] = err.Error()
			continue
		}
		loaded = append(loaded, f)
	}
	t.Logf("%d flows build, %d do not, %d are skipped", len(loaded), len(failed), nSkipped)
	ratchet(t, "loadable.json", loaded, failed)
}

// TestSkipManifest keeps skip.json honest: every entry names a flow that exists
// and says why it is skipped.
func TestSkipManifest(t *testing.T) {
	files := map[string]bool{}
	for _, f := range flowFiles(t) {
		files[f] = true
	}
	categories := map[string]bool{"groovy": true, "custom-step": true, "broken-fixture": true}
	for f, s := range loadSkips(t) {
		if !files[f] {
			t.Errorf("skip.json names %s, which does not exist", f)
		}
		if !categories[s.Category] || strings.TrimSpace(s.Reason) == "" {
			t.Errorf("skip.json: %s needs a category (groovy, custom-step or broken-fixture) and a reason", f)
		}
	}
}

// TestFixturesHaveNoCredentials fails when a fixture holds a credential, so
// that a new import cannot be committed with one. Fix it with:
//
//	go run ./regression/cmd/sanitize
func TestFixturesHaveNoCredentials(t *testing.T) {
	found, err := sanitize.Run([]string{path("regressionTests"), path("postman")}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) > 0 {
		var b strings.Builder
		for i, f := range found {
			if i == 20 {
				b.WriteString("  ...\n")
				break
			}
			b.WriteString("  " + strings.TrimPrefix(f.String(), repoRoot+"/") + "\n")
		}
		t.Fatalf("%d credentials in the fixtures; run: go run ./regression/cmd/sanitize\n%s", len(found), b.String())
	}
}
