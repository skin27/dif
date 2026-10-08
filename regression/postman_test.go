package regression

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"dif/api"
	"dif/internal/service"
	"dif/regression/postman"
)

const newmanVersion = "6.2.2"

// reporterJS is a Newman reporter that records, per request, its status and
// the outcome of its tests. Newman's own JSON reporter keeps every request and
// response body, which does not fit in a string for a collection this large.
const reporterJS = `"use strict";
const fs = require("fs");
module.exports = function (emitter, options) {
  const results = new Map();
  const get = (item) => {
    let r = results.get(item.id);
    if (!r) { r = { id: item.id, assertions: [] }; results.set(item.id, r); }
    return r;
  };
  const text = (e) => String((e && e.message) || e);
  emitter.on("request", (err, args) => {
    const r = get(args.item);
    if (err) r.requestError = text(err);
    else if (args.response) r.code = args.response.code;
  });
  emitter.on("assertion", (err, args) => {
    get(args.item).assertions.push({ name: args.assertion, error: err ? text(err) : "" });
  });
  emitter.on("script", (err, args) => {
    if (err) get(args.item).scriptError = text(err);
  });
  emitter.on("done", () => {
    fs.writeFileSync(options.export, JSON.stringify(Array.from(results.values())));
  });
};
`

// startable are the sources of flows that a Postman request can reach, or that
// other flows call. The other flows (timers, files, mailboxes, ...) are built
// by TestFlowsLoad but not started.
var startable = map[string]bool{"https": true, "rest": true, "flowlink": true, "flowlink-async": true, "queue": true, "topic": true, "reply": true}

// TestPostman starts DIF with the flows and runs the Postman requests against
// it with Newman, which runs the test scripts of the requests as Postman does.
// It runs only with -postman, as it takes minutes and installs Newman under
// regression/.cache, which needs Node, npm and network access:
//
//	go test ./regression -run Postman -postman -timeout 60m
func TestPostman(t *testing.T) {
	if !*runPostman {
		t.Skip("run with -postman: it takes minutes, and installs Newman with npm")
	}
	node, newman := ensureNewman(t)

	reqs, err := postman.Load(path("postman/postman"))
	if err != nil {
		t.Fatal(err)
	}
	skips := loadSkips(t)
	files := flowFiles(t)

	// Which flow answers which request: by the last path segment of the URL.
	live, skippedFlow := map[string]bool{}, map[string]bool{}
	for _, f := range files {
		data, err := os.ReadFile(path(f))
		if err != nil {
			t.Fatal(err)
		}
		if src, err := sourceOf(data); err == nil && src.Scheme == "https" {
			name := src.Path[strings.LastIndex(src.Path, "/")+1:]
			if _, ok := skips[f]; ok {
				skippedFlow[name] = true
			} else {
				live[name] = true
			}
		}
	}
	byCollection := map[string][]postman.Request{}
	var nAttachment, nSkipped, nNoFlow int
	for _, r := range reqs {
		switch {
		case r.Problem != "":
			nAttachment++
		case skippedFlow[r.Flow] && !live[r.Flow]:
			nSkipped++
		default:
			if !live[r.Flow] {
				nNoFlow++ // still run: the request may expect the 404, or a flow that is not an https flow
			}
			byCollection[r.Collection] = append(byCollection[r.Collection], r)
		}
	}

	t.Chdir(workdir)
	port := freePort(t)
	stagingDir := stageFlows(t, files, skips, port)
	stop := startService(t, stagingDir)
	defer stop()

	dir := path("postman/postman")
	env, err := postman.Variables(filepath.Join(dir, "environments", "next.environment.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	env["BASE"] = fmt.Sprintf("https://127.0.0.1:%d/regressiontests", port)
	globals, _ := postman.Variables(filepath.Join(dir, "globals", "workspace.globals.yaml"))
	envJSON, _ := postman.Environment("regression", env)
	globalsJSON, _ := postman.Environment("globals", globals)
	envFile, globalsFile := filepath.Join(workdir, "env.json"), filepath.Join(workdir, "globals.json")
	if err := os.WriteFile(envFile, envJSON, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(globalsFile, globalsJSON, 0o644); err != nil {
		t.Fatal(err)
	}

	// DIF_POSTMAN_COLLECTION=<text> runs only the collections whose name has the
	// text, to iterate on one; such a run does not touch the ratchet.
	only := os.Getenv("DIF_POSTMAN_COLLECTION")
	names := make([]string, 0, len(byCollection))
	for n := range byCollection {
		if strings.Contains(n, only) {
			names = append(names, n)
		}
	}
	sort.Strings(names)

	var passing []string
	failed := map[string]string{}
	total := 0
	for _, name := range names {
		rs := byCollection[name]
		coll, err := postman.Collection(name, rs)
		if err != nil {
			t.Fatal(err)
		}
		collFile, report := filepath.Join(workdir, "collection.json"), filepath.Join(workdir, "report.json")
		os.Remove(report)
		if err := os.WriteFile(collFile, coll, 0o644); err != nil {
			t.Fatal(err)
		}
		results := runNewman(t, node, newman, collFile, envFile, globalsFile, report)
		pass := 0
		for _, r := range rs {
			total++
			res, ok := results[r.ID]
			switch {
			case !ok:
				failed[r.ID] = "no result: Newman did not get to this request"
			case res.reason != "":
				failed[r.ID] = res.reason
			default:
				passing = append(passing, "postman/postman/"+r.ID)
				pass++
			}
		}
		t.Logf("%-52s %4d of %4d pass", name, pass, len(rs))
	}
	t.Logf("%d requests run; %d pass. Not run: %d with an attachment that is not in the repository, %d for skipped flows. Of those run, %d name no flow in regressionTests.",
		total, len(passing), nAttachment, nSkipped, nNoFlow)

	why := map[string]string{}
	var lines []string
	for id, reason := range failed {
		why["postman/postman/"+id] = reason
		lines = append(lines, id+"\n    "+reason)
	}
	sort.Strings(lines)
	cache := path("regression/.cache")
	if os.MkdirAll(cache, 0o755) == nil {
		_ = os.WriteFile(filepath.Join(cache, "postman-failures.txt"), []byte(strings.Join(lines, "\n")+"\n"), 0o644)
		t.Logf("why each failing request fails: regression/.cache/postman-failures.txt")
	}
	if only != "" {
		t.Logf("DIF_POSTMAN_COLLECTION=%q: a partial run, postman-passing.json is not checked", only)
		return
	}
	ratchet(t, "postman-passing.json", passing, why)
}

// ensureNewman returns Node and the Newman script, installing Newman under
// regression/.cache when it is not there. The test is skipped if that is not possible.
func ensureNewman(t *testing.T) (node, script string) {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed; it is needed to run Newman")
	}
	cache := path("regression/.cache/newman")
	script = filepath.Join(cache, "node_modules", "newman", "bin", "newman.js")
	if _, err := os.Stat(script); err == nil {
		installReporter(t, cache)
		return node, script
	}
	npm, err := exec.LookPath("npm")
	if err != nil {
		t.Skip("npm is not installed; it is needed to install Newman")
	}
	t.Logf("installing newman@%s under regression/.cache ...", newmanVersion)
	cmd := exec.Command(npm, "install", "--prefix", cache, "--no-audit", "--no-fund", "--loglevel=error", "newman@"+newmanVersion)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("cannot install newman (this needs network access): %v\n%s", err, out)
	}
	installReporter(t, cache)
	return node, script
}

// installReporter puts the reporter next to Newman, where Newman finds it.
func installReporter(t *testing.T, cache string) {
	t.Helper()
	dir := filepath.Join(cache, "node_modules", "newman-reporter-difcompact")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{"index.js": reporterJS, "package.json": `{"name":"newman-reporter-difcompact","version":"1.0.0","main":"index.js"}`} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

// stageFlows writes the flows to start into a directory, with their addresses
// pointed at the local port: those that are not skipped, build, have a source
// that other flows or requests can reach, and whose local channels have a
// consumer. It returns the directory.
func stageFlows(t *testing.T, files []string, skips map[string]skipped, port int) string {
	t.Helper()
	type candidate struct {
		file string
		data []byte
		in   string
		out  []string
	}
	var cands []candidate
	ids, paths := map[string]bool{}, map[string]bool{}
	reasons := map[string]int{}
	for _, f := range files {
		if _, ok := skips[f]; ok {
			continue
		}
		raw, err := os.ReadFile(path(f))
		if err != nil {
			t.Fatal(err)
		}
		data := overlay(raw, port)
		src, err := sourceOf(data)
		if err != nil || !startable[src.Scheme] {
			continue
		}
		flow, err := api.LoadBytes(data, nil)
		if err != nil {
			reasons["does not build"]++
			continue
		}
		if ids[src.FlowID] || (src.Path != "" && paths[src.Path]) {
			reasons["flow id or path used by another flow"]++
			continue
		}
		ids[src.FlowID] = true
		if src.Path != "" {
			paths[src.Path] = true
		}
		in, out := flow.LocalChannels()
		cands = append(cands, candidate{f, data, in, out})
	}
	for changed := true; changed; {
		changed = false
		consumers := map[string]bool{}
		for _, c := range cands {
			if c.in != "" {
				consumers[c.in] = true
			}
		}
		kept := cands[:0]
		for _, c := range cands {
			ok := true
			for _, o := range c.out {
				ok = ok && consumers[o]
			}
			if ok {
				kept = append(kept, c)
			} else {
				reasons["calls a local channel that no flow consumes"]++
				changed = true
			}
		}
		cands = kept
	}
	dir := filepath.Join(workdir, "flows")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for i, c := range cands {
		name := fmt.Sprintf("%04d-%s", i, filepath.Base(c.file))
		if err := os.WriteFile(filepath.Join(dir, name), c.data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("starting %d flows; left out: %v", len(cands), reasons)
	return dir
}

// startService runs the flows in dir as `dif run` does and waits until it is
// ready. The returned function stops it.
func startService(t *testing.T, dir string) (stop func()) {
	t.Helper()
	monitor := fmt.Sprintf("127.0.0.1:%d", freePort(t))
	opts, err := service.ParseOptions([]string{"--dir", dir, "--monitor-address", monitor, "--log-format", "text", "--startup-timeout", "120s", "--shutdown-timeout", "20s"}, os.Getenv, false)
	if err != nil {
		t.Fatal(err)
	}
	logs := &tail{max: 1 << 20}
	svc := service.New(opts, logs)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- svc.Run(ctx) }()
	stop = func() {
		cancel()
		select {
		case <-done:
		case <-time.After(40 * time.Second):
			t.Log("the service did not stop in time")
		}
	}
	deadline := time.Now().Add(150 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-done:
			t.Fatalf("the service stopped while starting: %v\n%s", err, logs)
		default:
		}
		if resp, err := http.Get("http://" + monitor + "/readyz"); err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return stop
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	stop()
	t.Fatalf("the service is not ready after 150s\n%s", logs)
	return nil
}

// tail keeps the end of what is written to it.
type tail struct {
	mu  sync.Mutex
	b   []byte
	max int
}

func (w *tail) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.b = append(w.b, p...)
	if len(w.b) > w.max {
		w.b = w.b[len(w.b)-w.max:]
	}
	return len(p), nil
}

func (w *tail) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return string(w.b)
}

type result struct{ reason string } // empty: the request passed

// runNewman runs a collection and returns the outcome per request ID.
func runNewman(t *testing.T, node, script, collection, env, globals, report string) map[string]result {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, node, "--max-old-space-size=4096", script, "run", collection, "-e", env, "-g", globals,
		"--insecure", "--timeout-request", "30000", "--color", "off", "--reporters", "difcompact", "--reporter-difcompact-export", report)
	cmd.Env = append(withoutProxies(os.Environ()), "NO_PROXY=127.0.0.1,localhost")
	var out bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &out
	runErr := cmd.Run() // Newman exits 1 when a test fails
	data, err := os.ReadFile(report)
	if err != nil {
		t.Fatalf("newman produced no report (%v):\n%s", runErr, tailOf(out.String(), 3000))
	}
	var rep []struct {
		ID           string `json:"id"`
		Code         int    `json:"code"`
		RequestError string `json:"requestError"`
		ScriptError  string `json:"scriptError"`
		Assertions   []struct {
			Name  string `json:"name"`
			Error string `json:"error"`
		} `json:"assertions"`
	}
	if err := json.Unmarshal(data, &rep); err != nil {
		t.Fatalf("newman report: %v", err)
	}
	results := make(map[string]result, len(rep))
	for _, e := range rep {
		var res result
		switch {
		case e.RequestError != "":
			res.reason = "request failed: " + oneLine(e.RequestError)
		case e.Code == 0:
			res.reason = "no response"
		case e.ScriptError != "":
			res.reason = "script error: " + oneLine(e.ScriptError)
		default:
			for _, a := range e.Assertions {
				if a.Error != "" {
					res.reason = fmt.Sprintf("%s: %s", a.Name, oneLine(a.Error))
					break
				}
			}
			// A request without tests only has to be answered, and not with an error.
			if res.reason == "" && len(e.Assertions) == 0 && (e.Code < 200 || e.Code > 299) {
				res.reason = fmt.Sprintf("no tests, and the response is HTTP %d", e.Code)
			}
		}
		results[e.ID] = res
	}
	return results
}

func tailOf(s string, n int) string {
	if len(s) > n {
		return "..." + s[len(s)-n:]
	}
	return s
}

func oneLine(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 300 {
		s = s[:300] + "..."
	}
	return s
}

func withoutProxies(env []string) []string {
	var out []string
	for _, e := range env {
		k, _, _ := strings.Cut(e, "=")
		switch strings.ToLower(k) {
		case "http_proxy", "https_proxy", "all_proxy", "no_proxy":
			continue
		}
		out = append(out, e)
	}
	return out
}
