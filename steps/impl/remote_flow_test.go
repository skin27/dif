package impl_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"dif/api"
	"dif/steps/impl"
)

// remoteFlow writes a DIL flow of the steps, one after the other, and returns its path.
func remoteFlow(t *testing.T, steps ...[3]any) string {
	t.Helper()
	var list []map[string]any
	for i, s := range steps {
		id, uri, opts := s[0].(string), s[1].(string), s[2]
		kind := "action"
		switch i {
		case 0:
			kind = "source"
		case len(steps) - 1:
			kind = "sink"
		}
		var links []map[string]any
		if i > 0 {
			links = append(links, map[string]any{"id": id, "bound": "in"})
		}
		if i < len(steps)-1 {
			links = append(links, map[string]any{"id": steps[i+1][0], "bound": "out"})
		}
		list = append(list, map[string]any{"id": id, "type": kind, "uri": uri, "options": opts, "links": map[string]any{"link": links}})
	}
	doc := map[string]any{"dil": map[string]any{"integrations": map[string]any{"integration": map[string]any{
		"flows": map[string]any{"flow": map[string]any{"id": "remote-flow", "steps": map[string]any{"step": list}}},
	}}}}
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "remote-flow.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// A flow that fails on a file makes the source move it to moveFailed; the engine
// tells the source how processing went.
func TestRemoteSourceInAFlow(t *testing.T) {
	for _, tc := range []struct {
		scheme string
		fake   func(*testing.T) (host, root, user, password string)
	}{{"ftp", impl.FakeFTPForTest}, {"sftp", impl.FakeSFTPForTest}} {
		t.Run(tc.scheme, func(t *testing.T) {
			host, root, user, password := tc.fake(t)
			for name, content := range map[string]string{"good.txt": "good", "bad.txt": "bad"} {
				if err := os.MkdirAll(filepath.Join(root, "in"), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(root, "in", name), []byte(content), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			source := map[string]any{
				"userName": user, "password": password, "initialDelay": 0, "delay": 20, "maxMessagesPerPoll": 0,
				"strictHostKeyChecking": false, "move": "RAW(done)", "moveFailed": "RAW(failed)",
			}
			if tc.scheme == "ftp" {
				delete(source, "strictHostKeyChecking")
			}
			path := remoteFlow(t,
				[3]any{"src", tc.scheme + ":" + host + "/in", source},
				[3]any{"check", "simplevalidator", map[string]any{"expression": "${body} == 'good'"}},
				[3]any{"end", "wastebin", map[string]any{}},
			)

			results := make(chan error, 10)
			flow, err := api.Load(path, func(_ *api.Result, err error) { results <- err })
			if err != nil {
				t.Fatal(err)
			}
			if err := flow.Start(); err != nil {
				t.Fatal(err)
			}
			defer flow.Stop()

			deadline := time.After(10 * time.Second)
			for !exists(filepath.Join(root, "in", "done", "good.txt")) || !exists(filepath.Join(root, "in", "failed", "bad.txt")) {
				select {
				case <-deadline:
					t.Fatalf("good.txt: %v, bad.txt: %v; in has %v", exists(filepath.Join(root, "in", "done", "good.txt")), exists(filepath.Join(root, "in", "failed", "bad.txt")), dirNames(t, filepath.Join(root, "in")))
				case <-time.After(10 * time.Millisecond):
				}
			}
			var ok, failed int
			for len(results) > 0 {
				if <-results != nil {
					failed++
				} else {
					ok++
				}
			}
			if ok != 1 || failed != 1 {
				t.Errorf("%d messages succeeded and %d failed, want one each", ok, failed)
			}
		})
	}
}

func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}
