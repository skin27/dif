package dil_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"dif/api"
)

// TestStaticChecksHaveNoFalseErrors runs the static checks of `dif validate`
// over every flow of the repository: those in testdata and the programs in
// api/testdata. None of them fails at a step on every message, so an error
// diagnostic is a false one: fix the checker. Warnings are logged, by code.
func TestStaticChecksHaveNoFalseErrors(t *testing.T) {
	t.Chdir(workdir)
	files := append(flowFiles(t), programFiles(t)...)
	counts := map[string]int{}
	flows := 0
	for _, f := range files {
		// Flowlink targets in other files are not checked: they are not part of the document.
		p, err := api.PrepareProgram(context.Background(), []api.ProgramSource{{Name: f, Path: path(f)}}, api.ProgramLimits{})
		if err != nil {
			t.Errorf("%s: %v", f, err)
			continue
		}
		info := p.Info()
		flows += len(info.Flows)
		for _, d := range info.Diagnostics {
			counts[d.Severity+" "+d.Code]++
			if d.Severity == api.SeverityError {
				t.Errorf("%s: false error: %s", f, d)
			} else {
				t.Logf("%s: %s", f, d)
			}
		}
	}
	var codes []string
	for c, n := range counts {
		codes = append(codes, c+": "+itoa(n))
	}
	sort.Strings(codes)
	t.Logf("%d documents, %d flows; diagnostics: %s", len(files), flows, strings.Join(codes, ", "))
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}

// programFiles returns the DIL documents of api/testdata, which are programs
// with imports: each is inspected with what it imports.
func programFiles(t *testing.T) []string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(path("api/testdata"), func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Ext(p) != ".json" {
			return err
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		var doc map[string]json.RawMessage
		if json.Unmarshal(bytes.TrimPrefix(data, []byte("\xef\xbb\xbf")), &doc) == nil && doc["dil"] != nil {
			rel, _ := filepath.Rel(repoRoot, p)
			files = append(files, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(files)
	return files
}
