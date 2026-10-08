package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func options(t *testing.T, args ...string) Options {
	t.Helper()
	o, err := ParseOptions(args, func(string) string { return "" }, false)
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func TestOptionsPrecedenceAndStrictConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "service.json")
	os.WriteFile(path, []byte(`{"files":["config.json"],"shutdownTimeout":"3s","logFormat":"text"}`), 0600)
	env := map[string]string{"DIF_FILES": `["env.json"]`, "DIF_SHUTDOWN_TIMEOUT": "4s"}
	o, err := ParseOptions([]string{"--config", path, "--file=flag1.json", "--file=flag2.json", "--shutdown-timeout=5s"}, func(k string) string { return env[k] }, false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(o.Files, ",") != "flag1.json,flag2.json" || o.ShutdownTimeout != "5s" || o.LogFormat != "text" {
		t.Fatalf("precedence: %+v", o)
	}
	os.WriteFile(path, []byte(`{"files":["a.json"],"typo":true}`), 0600)
	if _, err := ParseOptions([]string{"--config", path}, func(string) string { return "" }, false); err == nil {
		t.Fatal("unknown config accepted")
	}
}

func TestResolveDirectorySnapshotsAndDuplicates(t *testing.T) {
	dir := t.TempDir()
	writeFlow(t, dir, "b.json", "b", "message:b", nil, nil)
	writeFlow(t, dir, "a.json", "a", "message:a", nil, nil)
	os.WriteFile(filepath.Join(dir, ".hidden.json"), []byte("invalid"), 0600)
	os.Mkdir(filepath.Join(dir, "sub.json"), 0700)
	o := options(t, "--dir", dir)
	docs, err := Resolve(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if len(docs) != 2 || docs[0].Info.ID != "a" || docs[1].Info.ID != "b" {
		t.Fatalf("documents: %+v", docs)
	}
	os.WriteFile(filepath.Join(dir, "a.json"), []byte("changed"), 0600)
	if !strings.Contains(string(docs[0].Data), `"id":"a"`) {
		t.Fatal("snapshot changed")
	}
	o.Files = []string{filepath.Join(dir, "b.json")}
	// Restore a valid first document so the duplicate is the failure.
	writeFlow(t, dir, "a.json", "a", "message:a", nil, nil)
	if _, err := Resolve(context.Background(), o); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate: %v", err)
	}
	if _, err := Resolve(context.Background(), options(t, "--dir", t.TempDir())); err == nil {
		t.Fatal("empty directory accepted")
	}
}

func TestRemoteInputLimitsRedirectsAndDigest(t *testing.T) {
	dir := t.TempDir()
	path := writeFlow(t, dir, "flow.json", "remote", "message:r", nil, nil)
	data, _ := os.ReadFile(path)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/raw":
			w.Write(data)
		case "/large":
			fmt.Fprint(w, strings.Repeat("x", 1025))
		case "/redirect":
			http.Redirect(w, r, "/raw", http.StatusFound)
		case "/downgrade":
			http.Redirect(w, r, "http://example.com/flow", http.StatusFound)
		case "/other":
			http.Redirect(w, r, "https://example.com/flow", http.StatusFound)
		case "/slow":
			<-r.Context().Done()
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	sum := sha256.Sum256(data)
	o := options(t, "--url", server.URL+"/redirect", "--sha256", hex.EncodeToString(sum[:]))
	if docs, err := resolve(context.Background(), o, server.Client().Transport); err != nil || len(docs) != 1 {
		t.Fatalf("fetch: %v", err)
	}
	for _, path := range []string{"/large", "/downgrade", "/other", "/missing", "/slow"} {
		t.Run(path, func(t *testing.T) {
			o := options(t, "--url", server.URL+path+"?token=private", "--fetch-timeout=30ms", "--max-bytes=1024")
			_, err := resolve(context.Background(), o, server.Client().Transport)
			if err == nil || strings.Contains(err.Error(), "private") {
				t.Fatalf("unsafe fetch result: %v", err)
			}
		})
	}
	o.SHA256 = strings.Repeat("0", 64)
	if _, err := resolve(context.Background(), o, server.Client().Transport); err == nil {
		t.Fatal("digest mismatch accepted")
	}
}
