package program

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeDoc(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}
func importDoc(path, alias string) string {
	return fmt.Sprintf(`{"dil":{"imports":{"import":{"path":%q,"namespace":%q}}}}`, path, alias)
}

func TestDiamondAndDirectoryDiscovery(t *testing.T) {
	dir := t.TempDir()
	common := writeDoc(t, dir, "lib/common.json", `{"dil":{"core":{"messages":{"message":{"name":"sample","body":"secret"}}}}}`)
	left := writeDoc(t, dir, "lib/left.json", importDoc("common.json", "common"))
	right := writeDoc(t, dir, "right.json", importDoc("lib/common.json", "shared"))
	root := writeDoc(t, dir, "root.json", `{"dil":{"imports":{"import":[{"path":"lib/left.json","namespace":"left"},{"path":"right.json","namespace":"right"}]}}}`)
	for _, sources := range [][]Source{{{Path: root}}, {{Path: common, Discovered: true}, {Path: left, Discovered: true}, {Path: right, Discovered: true}, {Path: root, Discovered: true}}} {
		result, err := Resolve(context.Background(), sources, Limits{})
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Documents) != 4 {
			t.Fatalf("diamond loaded %d documents", len(result.Documents))
		}
	}
	if _, err := Resolve(context.Background(), []Source{{Path: root}, {Path: root}}, Limits{}); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate: %v", err)
	}
}

func TestCyclesLimitsAndCancellation(t *testing.T) {
	dir := t.TempDir()
	a := writeDoc(t, dir, "a.json", importDoc("b.json", "b"))
	b := writeDoc(t, dir, "b.json", importDoc("a.json", "a"))
	_, err := Resolve(context.Background(), []Source{{Path: a, Discovered: true}, {Path: b, Discovered: true}}, Limits{})
	if err == nil || !strings.Contains(err.Error(), "cycle") || !strings.Contains(err.Error(), "a.json") || !strings.Contains(err.Error(), "b.json") {
		t.Fatalf("cycle: %v", err)
	}
	writeDoc(t, dir, "b.json", importDoc("c.json", "c"))
	writeDoc(t, dir, "c.json", `{"dil":{}}`)
	for _, tc := range []struct {
		limits Limits
		want   string
	}{
		{Limits{MaxDepth: 1}, "depth"},
		{Limits{MaxFiles: 1}, "files"},
		{Limits{MaxBytes: 8}, "max-bytes"},
		{Limits{MaxTotalBytes: 8}, "total byte"},
		{Limits{MaxFiles: -1}, "positive"},
	} {
		_, err := Resolve(context.Background(), []Source{{Path: a}}, tc.limits)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Fatalf("%+v: %v", tc.limits, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Resolve(ctx, []Source{{Path: a}}, Limits{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
}

func TestSnapshotsRemoteAndAnonymousSources(t *testing.T) {
	dir := t.TempDir()
	path := writeDoc(t, dir, "root.json", `not the supplied snapshot`)
	data := []byte(`{"dil":{}}`)
	if _, err := Resolve(context.Background(), []Source{{Path: path, Data: data}}, Limits{}); err != nil {
		t.Fatal(err)
	}
	writeDoc(t, dir, "root.json", `{"dil":{}}`)
	if _, err := Resolve(context.Background(), []Source{{Path: path, Data: []byte{}}}, Limits{}); err == nil {
		t.Fatal("empty snapshot must fail instead of reading the valid file")
	}
	remote := []byte(importDoc("common.json", "common"))
	if _, err := Resolve(context.Background(), []Source{{Name: "URL #1", Data: remote}}, Limits{}); err == nil || !strings.Contains(err.Error(), "explicit local origin") {
		t.Fatalf("remote: %v", err)
	}
	writeDoc(t, dir, "root.json", importDoc("https://example.com/private?token=SECRET", "remote"))
	_, err := Resolve(context.Background(), []Source{{Path: path}}, Limits{})
	if err == nil || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("remote import diagnostic: %v", err)
	}
	writeDoc(t, dir, "root.json", importDoc("missing.json", "missing"))
	if _, err := Resolve(context.Background(), []Source{{Path: path}}, Limits{}); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("missing file: %v", err)
	}
}

func TestDepthLimitIncludesPreviouslyVisitedDependencies(t *testing.T) {
	dir := t.TempDir()
	writeDoc(t, dir, "leaf.json", `{"dil":{}}`)
	shared := writeDoc(t, dir, "shared.json", importDoc("leaf.json", "leaf"))
	writeDoc(t, dir, "middle.json", importDoc("shared.json", "shared"))
	root := writeDoc(t, dir, "root.json", importDoc("middle.json", "middle"))
	_, err := Resolve(context.Background(), []Source{{Path: shared}, {Path: root}}, Limits{MaxDepth: 2})
	if err == nil || !strings.Contains(err.Error(), "depth") {
		t.Fatalf("cached import bypassed depth limit: %v", err)
	}
}

func TestSymlinkAliasesDeduplicate(t *testing.T) {
	dir := t.TempDir()
	path := writeDoc(t, dir, "common.json", `{"dil":{}}`)
	alias := filepath.Join(dir, "alias.json")
	if err := os.Symlink(path, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	root := writeDoc(t, dir, "root.json", `{"dil":{"imports":{"import":[{"path":"common.json","namespace":"a"},{"path":"alias.json","namespace":"b"}]}}}`)
	r, err := Resolve(context.Background(), []Source{{Path: root}}, Limits{})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Documents) != 2 {
		t.Fatalf("alias loaded twice: %d", len(r.Documents))
	}
}
