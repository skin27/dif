package service

import (
	"bytes"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// TestStoresExampleSurvivesRestart runs testdata/reliable/stores-service.json
// twice on one directory: the durable stores keep the counter and the tenant
// variable, and /status and /metrics show them.
func TestStoresExampleSurvivesRestart(t *testing.T) {
	root := filepath.Join("..", "..")
	o, err := ParseOptions([]string{"--config", filepath.Join(root, "testdata", "reliable", "stores-service.json")}, func(string) string { return "" }, false)
	if err != nil {
		t.Fatal(err)
	}
	for i, path := range o.Files {
		o.Files[i] = filepath.Join(root, path)
	}
	o.Channels.Directory = t.TempDir()
	for run := 1; run <= 2; run++ {
		var logs bytes.Buffer
		s := New(o, &logs)
		cancel, done := startService(t, s)
		await(t, func() bool {
			_, _, _, flows := s.snapshot()
			return len(flows) == 1 && flows[0].Completed == 1
		})
		status := httptest.NewRecorder()
		s.Handler().ServeHTTP(status, httptest.NewRequest("GET", "/status", nil))
		metrics := httptest.NewRecorder()
		s.Handler().ServeHTTP(metrics, httptest.NewRequest("GET", "/metrics", nil))
		cancel()
		if err := finish(t, done); err != nil {
			t.Fatal(err)
		}
		if want := "visit " + string(rune('0'+run)); !strings.Contains(logs.String(), want) {
			t.Errorf("run %d: logs lack %q: %s", run, want, logs.String())
		}
		if want := `"stores":[{"name":"tenant.default","durable":true,"keys":1},{"name":"visits","durable":true,"keys":1}]`; !strings.Contains(status.Body.String(), want) {
			t.Errorf("run %d: status lacks %s: %s", run, want, status.Body.String())
		}
		for _, want := range []string{"# TYPE dif_store_keys gauge\n", `dif_store_keys{store="visits"} 1`, `dif_store_keys{store="tenant.default"} 1`} {
			if !strings.Contains(metrics.Body.String(), want) {
				t.Errorf("run %d: metrics lack %q", run, want)
			}
		}
	}
}
