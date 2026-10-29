package api

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// interceptedDIL writes the flow message:in -> setbody (one) -> setbody sink
// (two) with the interceptors in its core, and returns its path.
func interceptedDIL(t *testing.T, interceptors ...map[string]any) string {
	t.Helper()
	link := func(id, bound string) map[string]any { return map[string]any{"id": id, "bound": bound} }
	links := func(l ...map[string]any) map[string]any { return map[string]any{"link": l} }
	doc := map[string]any{"dil": map[string]any{
		"core": map[string]any{"interceptors": map[string]any{"interceptor": interceptors}},
		"integrations": map[string]any{"integration": map[string]any{"flows": map[string]any{"flow": map[string]any{
			"id": "intercepted", "steps": map[string]any{"step": []map[string]any{
				{"id": "in", "type": "source", "uri": "message:in", "links": links(link("one", "out"))},
				{"id": "one", "type": "action", "uri": "setbody", "options": map[string]any{"expression": "hello"}, "links": links(link("one", "in"), link("two", "out"))},
				{"id": "two", "type": "sink", "uri": "setbody", "options": map[string]any{"expression": "done"}, "links": links(link("two", "in"))},
			}},
		}}}},
	}}
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "intercepted.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestInterceptors: the action of an interceptor runs in front of each step
// it selects, with a copy of the message; what it does or what fails in it
// does not reach the message.
func TestInterceptors(t *testing.T) {
	path := interceptedDIL(t,
		map[string]any{"name": "audit", "pointcut": map[string]any{"uris": []string{"setbody"}},
			"action": map[string]any{"uri": "logger", "options": map[string]any{"expression": "saw ${body}"}}},
		map[string]any{"name": "boom", "pointcut": map[string]any{"steps": []string{"one"}},
			"action": map[string]any{"uri": "simplevalidator", "options": map[string]any{"expression": "${body} == 'never'"}}},
	)
	var logged bytes.Buffer
	f, results := start(t, path, log.New(&logged, "", 0))

	m := f.NewMessage()
	m[Body] = "start"
	reply, err := f.Request(context.Background(), m)
	if err != nil || reply[Body] != "done" {
		t.Fatalf("reply = %v, %v; want done: the interceptors must not change or fail the message", reply, err)
	}
	if res := await(t, results); res.Err != nil || res.Message[Body] != "done" {
		t.Errorf("result = %+v", res)
	}

	text := logged.String()
	first := strings.Index(text, "step intercept:audit:one:action: INFO saw start")
	second := strings.Index(text, "step intercept:audit:two:action: INFO saw hello")
	if first < 0 || second < first {
		t.Errorf("log = %q\nwant the audit before step one (saw start), then before step two (saw hello)", text)
	}
	if !strings.Contains(text, "step intercept:boom:one: detached route to link 1 failed:") || !strings.Contains(text, "validation failed") {
		t.Errorf("log = %q, want the failure of the failing interceptor logged", text)
	}
}

// Inspect and describe show the steps an interceptor added, and mark them.
func TestInspectMarksInterceptorSteps(t *testing.T) {
	path := interceptedDIL(t, map[string]any{"name": "audit", "pointcut": map[string]any{"steps": []string{"two"}},
		"action": map[string]any{"uri": "log"}})
	info, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	var added, own []string
	for _, s := range info.Steps {
		if s.Interceptor {
			added = append(added, s.ID)
		} else {
			own = append(own, s.ID)
		}
	}
	if strings.Join(added, " ") != "intercept:audit:two intercept:audit:two:action" || strings.Join(own, " ") != "in one two" {
		t.Errorf("added = %v, own = %v", added, own)
	}
}

// In a document with several flows an interceptor applies to the flows its
// pointcut names, in the group loader as well.
func TestInterceptorsInAProgram(t *testing.T) {
	link := func(id, bound string) map[string]any { return map[string]any{"id": id, "bound": bound} }
	links := func(l ...map[string]any) map[string]any { return map[string]any{"link": l} }
	flow := func(id string) map[string]any {
		return map[string]any{"id": id, "name": id, "steps": map[string]any{"step": []map[string]any{
			{"id": "in", "type": "source", "uri": "message:in", "links": links(link("done", "out"))},
			{"id": "done", "type": "sink", "uri": "setbody", "options": map[string]any{"expression": "done"}, "links": links(link("done", "in"))},
		}}}
	}
	doc := map[string]any{"dil": map[string]any{
		"core": map[string]any{"interceptors": map[string]any{"interceptor": map[string]any{
			"name": "audit", "at": "entry", "pointcut": map[string]any{"flows": []string{"alpha"}}, "action": map[string]any{"uri": "log"}}}},
		"integrations": map[string]any{"integration": map[string]any{"flows": map[string]any{"flow": []map[string]any{flow("alpha"), flow("beta")}}}},
	}}
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "program.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := InspectProgram(path)
	if err != nil {
		t.Fatal(err)
	}
	added := map[string]int{}
	for _, f := range info.Flows {
		for _, s := range f.Flow.Steps {
			if s.Interceptor {
				added[f.Flow.ID]++
			}
		}
	}
	if added["alpha"] != 2 || added["beta"] != 0 {
		t.Errorf("steps added = %v, want two in alpha and none in beta", added)
	}
}
