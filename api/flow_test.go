package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	stepdef "dif/steps/definition"
)

// step is one DIL step; dil links the steps in the given order.
type step struct {
	id, kind, uri string
	opts          map[string]any
}

// dil writes a DIL file with one flow made of steps and returns its path.
func dil(t *testing.T, id string, steps ...step) string {
	t.Helper()
	var list []map[string]any
	for i, s := range steps {
		var links []map[string]any
		if i > 0 {
			links = append(links, map[string]any{"id": s.id, "bound": "in"})
		}
		if i < len(steps)-1 {
			links = append(links, map[string]any{"id": steps[i+1].id, "bound": "out"})
		}
		list = append(list, map[string]any{"id": s.id, "type": s.kind, "uri": s.uri, "options": s.opts, "links": map[string]any{"link": links}})
	}
	return writeDIL(t, id, list)
}

// writeDIL writes a DIL file with one flow made of the DIL steps in list and
// returns its path.
func writeDIL(t *testing.T, id string, list []map[string]any) string {
	t.Helper()
	doc := map[string]any{"dil": map[string]any{"integrations": map[string]any{"integration": map[string]any{
		"flows": map[string]any{"flow": map[string]any{"id": id, "steps": map[string]any{"step": list}}},
	}}}}
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), id+".json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

type outcome struct {
	res *Result
	err error
}

// start loads and starts the flow in path, logging to logger (nil for the
// standard logger); its results arrive on the returned channel.
func start(t *testing.T, path string, logger *log.Logger) (*Flow, chan outcome) {
	t.Helper()
	results := make(chan outcome, 100)
	f, err := Load(path, func(res *Result, err error) { results <- outcome{res, err} })
	if err != nil {
		t.Fatal(err)
	}
	if logger != nil {
		f.SetLogger(logger)
	}
	if err := f.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if f.State() != Stopped {
			f.Stop()
		}
	})
	return f, results
}

func await(t *testing.T, results chan outcome) *Result {
	t.Helper()
	select {
	case o := <-results:
		if o.err != nil {
			t.Fatal(o.err)
		}
		return o.res
	case <-time.After(3 * time.Second):
		t.Fatal("no result within 3s")
		return nil
	}
}

// TestTimerSetBodySetHeaderLog runs timer -> setbody -> setheader -> log through the real engine.
func TestTimerSetBodySetHeaderLog(t *testing.T) {
	path := dil(t, "ticks",
		step{"tick", "source", "timer:tick", map[string]any{"period": 10, "repeatCount": "3"}},
		step{"body", "action", "setbody", map[string]any{"language": "simple", "expression": "tick ${body}"}},
		step{"header", "action", "setheader", map[string]any{"name": "greeting", "language": "constant", "value": "hello"}},
		step{"log", "sink", "log", map[string]any{"showHeaders": true, "showBody": true}},
	)
	var logged bytes.Buffer
	f, results := start(t, path, log.New(&logged, "", 0))

	for i := 1; i <= 3; i++ {
		res := await(t, results)
		if want := fmt.Sprintf("tick %d", i); res.Message[Body] != want {
			t.Errorf("message %d: body = %v, want %q", i, res.Message[Body], want)
		}
		if res.Message["greeting"] != "hello" {
			t.Errorf("message %d: greeting = %v, want hello", i, res.Message["greeting"])
		}
		if got := strings.Join(res.Trail, " "); got != "source:tick action:body action:header sink:log" {
			t.Errorf("trail = %s", got)
		}
	}
	if err := f.Stop(); err != nil {
		t.Fatal(err)
	}

	lines := strings.Split(strings.TrimSpace(logged.String()), "\n")
	if len(lines) != 3 {
		t.Fatalf("logged %d lines, want 3:\n%s", len(lines), logged.String())
	}
	for i, line := range lines {
		for _, want := range []string{"step log: traceid=", "greeting=hello", fmt.Sprintf("body=tick %d", i+1)} {
			if !strings.Contains(line, want) {
				t.Errorf("log line %q, want containing %q", line, want)
			}
		}
	}
}

// TestFileRoundTrip runs file -> setbody -> file through the real engine.
func TestFileRoundTrip(t *testing.T) {
	in, out := filepath.Join(t.TempDir(), "in"), filepath.Join(t.TempDir(), "out")
	if err := os.MkdirAll(in, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(in, "a.txt"), []byte("hello file"), 0o644); err != nil {
		t.Fatal(err)
	}

	path := dil(t, "files",
		step{"in", "source", "file:" + in, map[string]any{"initialDelay": 0, "delay": 10}},
		step{"body", "action", "setbody", map[string]any{"language": "simple", "expression": "${body} from ${header.file.name}"}},
		step{"out", "sink", "file:" + out, nil},
	)
	f, results := start(t, path, nil)
	await(t, results)
	if err := f.Stop(); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(filepath.Join(out, "a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "hello file from a.txt" {
		t.Errorf("out/a.txt = %q, want %q", got, "hello file from a.txt")
	}
	if _, err := os.Stat(filepath.Join(in, ".done", "a.txt")); err != nil {
		t.Errorf("in/a.txt was not moved to .done: %v", err)
	}
}

func TestInvalidFlowsAreRejected(t *testing.T) {
	timer := func(opts map[string]any) step { return step{"src", "source", "timer:t", opts} }
	logSink := step{"log", "sink", "log", nil}
	tests := []struct {
		name  string
		steps []step
		want  string
	}{
		{"bad period", []step{timer(map[string]any{"period": "x"}), logSink}, `step src: timer: option period: want integer, got "x"`},
		{"unknown option", []step{timer(map[string]any{"numbers": 3}), logSink}, "step src: timer: unknown option numbers"},
		{"unsupported language", []step{timer(nil), {"b", "action", "setbody", map[string]any{"language": "groovy"}}, logSink},
			`step b: setbody: option language: "groovy" is not one of "constant", "simple"`},
		{"setheader without name", []step{timer(nil), {"h", "action", "setheader", map[string]any{"value": "x"}}, logSink},
			"step h: setheader: missing required option name"},
		{"unknown source", []step{{"src", "source", "carrierpigeon://example.com/in", nil}, logSink}, `step src: no processor for "carrierpigeon" (source)`},
		{"keystore missing", []step{{"src", "source", "https://0.0.0.0:9001/in", map[string]any{"serverIdentityFile": "nope.p12"}}, logSink}, "step src: https: server identity: open nope.p12"},
		{"unknown action", []step{timer(nil), {"x", "action", "jolt", nil}, logSink}, `step x: no processor for "jolt" (action)`},
		{"unknown core message", []step{timer(nil), {"x", "action", "setheaders:message:x", nil}, logSink}, `step x: message "x" not found`},
		{"timer as sink", []step{timer(nil), {"t", "sink", "timer:t", nil}}, `step t: no processor for "timer" (sink)`},
		{"unknown placeholder", []step{timer(nil), {"x", "action", "unknown", map[string]any{"stylesheet": "<xsl/>"}}, logSink}, `step x: no processor for "unknown" (action)`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(dil(t, "bad", tt.steps...), nil)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want containing %q", err, tt.want)
			}
		})
	}

}

// upper is a custom step: it upper-cases the body.
type upper struct{}

func (upper) Process(_ context.Context, m Message) (Message, error) {
	m[Body] = strings.ToUpper(fmt.Sprint(m[Body]))
	return m, nil
}

func TestRegisterStep(t *testing.T) {
	name := fmt.Sprintf("upper%d", time.Now().UnixNano()) // unique, so -count=N works
	err := RegisterStep(StepDefinition{
		Name:   name,
		Kind:   stepdef.Action,
		Schema: []byte(`{"type": "object", "additionalProperties": false}`),
		New:    func(string, stepdef.Params) (stepdef.Processor, error) { return upper{}, nil },
	})
	if err != nil {
		t.Fatal(err)
	}

	path := dil(t, "custom",
		step{"src", "source", "message:in", nil},
		step{"up", "sink", name, nil},
	)
	f, results := start(t, path, nil)
	m := f.NewMessage()
	m[Body] = "shout"
	if err := f.Send(m); err != nil {
		t.Fatal(err)
	}
	if res := await(t, results); res.Message[Body] != "SHOUT" {
		t.Errorf("body = %v, want SHOUT", res.Message[Body])
	}
}

// TestHTTPSFlows runs two flows over HTTPS through the real engine:
// server: https source -> setbody "pong ${body}"
// client: timer -> setbody "ping" -> https action (POST to server) -> log
func TestHTTPSFlows(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()

	server := dil(t, "server",
		step{"in", "source", "https://" + addr + "/pingpong", map[string]any{
			"serverIdentityFile": "../keystore/testdata/server-identity.p12", "serverIdentityPassword": "changeit"}},
		step{"reply", "sink", "setbody", map[string]any{"language": "simple", "expression": "pong ${body}"}},
	)
	client := dil(t, "client",
		step{"tick", "source", "timer:tick", map[string]any{"period": 50, "repeatCount": 1}},
		step{"ping", "action", "setbody", map[string]any{"expression": "ping"}},
		step{"call", "action", "https://" + addr + "/pingpong", map[string]any{
			"httpMethod": "POST", "trustStoreFile": "../keystore/testdata/truststore.p12", "trustStorePassword": "changeit"}},
		step{"log", "sink", "log", map[string]any{"showBody": true}},
	)

	_, serverResults := start(t, server, nil)
	var logged bytes.Buffer
	_, clientResults := start(t, client, log.New(&logged, "", 0))

	res := await(t, clientResults)
	if res.Message[Body] != "pong ping" || res.Message["http.status"] != 200 {
		t.Errorf("client message = %v, want body \"pong ping\" and http.status 200", res.Message)
	}
	if got := strings.Join(res.Trail, " "); got != "source:tick action:ping action:call sink:log" {
		t.Errorf("client trail = %s", got)
	}
	if res := await(t, serverResults); res.Message[Body] != "pong ping" {
		t.Errorf("server message = %v", res.Message)
	}
	if !strings.Contains(logged.String(), "body=pong ping") {
		t.Errorf("client log = %q", logged.String())
	}
}

// TestRequestExchangePattern calls a flow like a function: with a one-way
// step the reply is the message at that step, otherwise at the end.
func TestRequestExchangePattern(t *testing.T) {
	for uri, want := range map[string]string{"setfireandforget": "early", "settwoways": "late"} {
		path := dil(t, uri,
			step{"in", "source", "message:in", nil},
			step{"early", "action", "setbody", map[string]any{"expression": "early"}},
			step{"pattern", "action", uri, nil},
			step{"late", "sink", "setbody", map[string]any{"expression": "late"}},
		)
		f, results := start(t, path, nil)
		reply, err := f.Request(context.Background(), f.NewMessage())
		if err != nil || reply[Body] != want {
			t.Errorf("%s: reply = %v, %v; want body %s", uri, reply, err, want)
		}
		if res := await(t, results); res.Message[Body] != "late" {
			t.Errorf("%s: the flow ended with %v, want late", uri, res.Message[Body])
		}
	}
}
