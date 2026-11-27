package service

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"dif/keystore"
	stepdef "dif/steps/definition"
)

func capability(kind, target string) stepdef.Capability {
	return stepdef.Capability{Kind: kind, Target: target}
}

func TestPermissionOptions(t *testing.T) {
	dir := t.TempDir()
	config := filepath.Join(dir, "service.json")
	getenv := func(string) string { return "" }
	write := func(text string) {
		if err := os.WriteFile(config, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}

	// Without permissions nothing is enforced.
	if p, err := options(t, "--file", "a.json").Policy(); p != nil || err != nil {
		t.Errorf("no permissions: %v, %v", p, err)
	}

	// --sandbox takes no value, before or after other flags.
	for _, args := range [][]string{{"--sandbox", "--file", "a.json"}, {"--file", "a.json", "--sandbox"}, {"--sandbox=true", "--file=a.json"}} {
		o, err := ParseOptions(args, getenv, false)
		if err != nil || !o.Sandbox || strings.Join(o.Files, ",") != "a.json" {
			t.Errorf("%v: %+v, %v", args, o, err)
			continue
		}
		if p, err := o.Policy(); p == nil || err != nil || p.Allows(capability("env", "HOME")) {
			t.Errorf("%v: the sandbox is %v, %v", args, p, err)
		}
	}
	if o, err := ParseOptions([]string{"--sandbox=false", "--file", "a.json"}, getenv, false); err != nil || o.Sandbox {
		t.Errorf("--sandbox=false: %+v, %v", o, err)
	}

	// An empty permissions object denies everything.
	write(`{"files": ["a.json"], "permissions": {}}`)
	if p, err := options(t, "--config", config).Policy(); p == nil || err != nil {
		t.Errorf("empty permissions: %v, %v", p, err)
	}

	// The flags add to the grants of the configuration, and imply the sandbox.
	write(`{"files": ["a.json"], "permissions": {"net.connect": ["api.example.com:443"], "env": ["DIF_SMTP_PASSWORD"]}}`)
	o := options(t, "--config", config, "--allow-net", "db.example.com:5432", "--allow-net=*.internal.example:*",
		"--allow-listen", "127.0.0.1:9002", "--allow-fs-read", "inbox", "--allow-fs-write", "outbox", "--allow-env", "DIF_SQL_PASSWORD")
	if got := fmt.Sprint(o.Permissions); got != "map[env:[DIF_SMTP_PASSWORD DIF_SQL_PASSWORD] fs.read:[inbox] fs.write:[outbox] net.connect:[api.example.com:443 db.example.com:5432 *.internal.example:*] net.listen:[127.0.0.1:9002]]" {
		t.Errorf("permissions: %s", got)
	}
	p, err := o.Policy()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range [][2]string{{"net.connect", "api.example.com:443"}, {"net.connect", "pg.internal.example:5432"}, {"env", "DIF_SQL_PASSWORD"}, {"net.listen", "127.0.0.1:9002"}} {
		if !p.Allows(capability(c[0], c[1])) {
			t.Errorf("%s %s is not granted", c[0], c[1])
		}
	}
	if o := options(t, "--file", "a.json", "--allow-env", "X"); !o.Sandbox {
		t.Error("--allow-env does not imply the sandbox")
	}

	// Mistakes are errors of the options.
	for _, text := range []string{
		`{"files": ["a.json"], "permissions": {"exec": ["ls"]}}`,
		`{"files": ["a.json"], "permissions": {"net.connect": ["api.example.com"]}}`,
		`{"files": ["a.json"], "permissions": {"net.connect": "api.example.com:443"}}`,
	} {
		write(text)
		if _, err := ParseOptions([]string{"--config", config}, getenv, false); err == nil {
			t.Errorf("%s: accepted", text)
		}
	}
	if _, err := ParseOptions([]string{"--file", "a.json", "--allow-net", "nohost"}, getenv, false); err == nil || !strings.Contains(err.Error(), "net.connect grant") {
		t.Errorf("--allow-net nohost: %v", err)
	}
}

// TestServiceRefusesProgramOutsidePolicy: run refuses to start a program whose
// manifest the policy does not grant, naming flow, step, kind and target.
func TestServiceRefusesProgramOutsidePolicy(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "outbox")
	path := writeFlow(t, dir, "a.json", "writer", "message:manual", nil, []map[string]any{action("file:"+out, nil)})

	err := New(options(t, "--file", path, "--sandbox"), io.Discard).Run(context.Background())
	want := "error not-permitted: flow writer, step action: fs.write " + out + " is not granted"
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("sandbox: %v, want %q", err, want)
	}

	cancel, done := startService(t, New(options(t, "--file", path, "--allow-fs-write", out), io.Discard))
	cancel()
	if err := finish(t, done); err != nil {
		t.Fatalf("granted: %v", err)
	}
}

// TestServiceGuardsDynamicTargets: a target known only per message passes the
// load check, and the service's guard refuses it when the step runs, unless
// it is granted.
func TestServiceGuardsDynamicTargets(t *testing.T) {
	cert, err := keystore.LoadIdentity("../../keystore/testdata/server-identity.p12", "changeit")
	if err != nil {
		t.Fatal(err)
	}
	var hits atomic.Int64
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{cert}}
	srv.StartTLS()
	defer srv.Close()

	trust := "../../keystore/testdata/truststore.p12"
	dir := t.TempDir()
	path := writeFlow(t, dir, "a.json", "caller", "timer:t", map[string]any{"period": 10, "repeatCount": "1"}, []map[string]any{
		{"id": "url", "type": "action", "uri": "setheader", "options": map[string]any{"name": "url", "language": "constant", "value": srv.URL}},
		{"id": "call", "type": "action", "uri": "https:${header.url}", "options": map[string]any{"trustStoreFile": trust, "trustStorePassword": "changeit"}},
	})

	for _, tc := range []struct {
		grant string
		hits  int64
		log   string
	}{
		{"127.0.0.1:1", 0, `"step_id":"call"`},
		{srv.Listener.Addr().String(), 1, `"msg":"source completed"`},
	} {
		hits.Store(0)
		logs := &lockedBuffer{}
		cancel, done := startService(t, New(options(t, "--file", path, "--allow-net", tc.grant, "--allow-fs-read", trust), logs))
		await(t, func() bool {
			return strings.Contains(logs.String(), tc.log) && (tc.hits == 0 || hits.Load() == tc.hits)
		})
		cancel()
		if err := finish(t, done); err != nil {
			t.Fatalf("grant %s: %v", tc.grant, err)
		}
		if hits.Load() != tc.hits {
			t.Errorf("grant %s: the server was called %d times, want %d", tc.grant, hits.Load(), tc.hits)
		}
	}
}

type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// TestPermissionsExample: the example configuration grants exactly what the
// example flow can do; the sandbox grants none of it.
func TestPermissionsExample(t *testing.T) {
	t.Chdir(filepath.Join("..", ".."))
	for _, tc := range []struct {
		args []string
		want int
	}{
		{[]string{"--config", "testdata/reliable/permissions.json"}, 0},
		{[]string{"--file", "testdata/examples/permissions.json", "--sandbox"}, 4},
		{[]string{"--file", "testdata/examples/permissions.json", "--allow-net", "api.example.com:443", "--allow-fs-read", "inbox"}, 2},
	} {
		o, err := ParseOptions(tc.args, func(string) string { return "" }, true)
		if err != nil {
			t.Fatal(err)
		}
		docs, err := Resolve(context.Background(), o)
		if err != nil {
			t.Fatal(err)
		}
		p, err := o.Policy()
		if err != nil || p == nil {
			t.Fatalf("%v: policy %v, %v", tc.args, p, err)
		}
		if ds := docs[0].Program.Info().CheckPolicy(p); len(ds) != tc.want {
			t.Errorf("%v: %d diagnostics, want %d: %v", tc.args, len(ds), tc.want, ds)
		}
	}
}
