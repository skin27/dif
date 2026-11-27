package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dif/message"
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

func writeDILDoc(t *testing.T, dir, name string, doc map[string]any) string {
	t.Helper()
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestInterceptorTransformAndFlowcheck(t *testing.T) {
	link := func(id, bound string) map[string]any { return map[string]any{"id": id, "bound": bound} }
	links := func(l ...map[string]any) map[string]any { return map[string]any{"link": l} }
	makeDoc := func(ics []map[string]any) map[string]any {
		return map[string]any{"dil": map[string]any{
			"core": map[string]any{"interceptors": map[string]any{"interceptor": ics}},
			"integrations": map[string]any{"integration": map[string]any{"flows": map[string]any{"flow": map[string]any{
				"id": "tx", "steps": map[string]any{"step": []map[string]any{
					{"id": "in", "type": "source", "uri": "timer:t", "options": map[string]any{"period": 60000, "repeatCount": 1}, "links": links(link("use", "out"))},
					{"id": "use", "type": "action", "uri": "setbody", "options": map[string]any{"language": "simple", "expression": "${header.tenant}:${header.stage}"}, "links": links(link("use", "in"), link("done", "out"))},
					{"id": "done", "type": "sink", "uri": "passthrough", "links": links(link("done", "in"))},
					{"id": "err", "type": "error", "uri": "failedexchange", "links": links(link("recover", "out"))},
					{"id": "recover", "type": "sink", "uri": "setbody", "options": map[string]any{"language": "simple", "expression": "caught:${header.error.step}"}, "links": links(link("recover", "in"))},
				}},
			}}}},
		}}
	}
	dir := t.TempDir()

	// Without transform interceptors, flowcheck reports header-not-set for tenant and stage.
	plainPath := writeDILDoc(t, dir, "plain.json", makeDoc(nil))
	plainInfo, err := Inspect(plainPath)
	if err != nil {
		t.Fatal(err)
	}
	unset := 0
	for _, d := range plainInfo.Diagnostics {
		if d.Code == "header-not-set" {
			unset++
		}
	}
	if unset != 2 {
		t.Fatalf("without transform: got %d header-not-set diagnostics (%v), want 2", unset, plainInfo.Diagnostics)
	}

	// With entry + before + after transform interceptors, tenant and stage are set before "use",
	// so flowcheck reports zero header-not-set warnings, and runtime sees all mutations!
	txPath := writeDILDoc(t, dir, "tx.json", makeDoc([]map[string]any{
		{"name": "addTenant", "at": "entry", "mode": "transform",
			"action": map[string]any{"uri": "setheader", "options": map[string]any{"name": "tenant", "value": "acme"}}},
		{"name": "addStage", "at": "before", "mode": "transform", "pointcut": map[string]any{"steps": []string{"use"}},
			"action": map[string]any{"uri": "setheader", "options": map[string]any{"name": "stage", "value": "prod"}}},
		{"name": "wrapBody", "at": "after", "mode": "transform", "pointcut": map[string]any{"steps": []string{"done"}},
			"action": map[string]any{"uri": "setbody", "options": map[string]any{"language": "simple", "expression": "[${body}]@${header.metadata.step.target}"}}},
	}))
	txInfo, err := Inspect(txPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(txInfo.Diagnostics) != 0 {
		t.Errorf("with transform interceptors: unexpected diagnostics %v", txInfo.Diagnostics)
	}
	f, _ := start(t, txPath, log.New(&bytes.Buffer{}, "", 0))
	reply, err := f.Request(context.Background(), f.NewMessage())
	if err != nil || reply[Body] != "[acme:prod]@done" {
		t.Fatalf("reply = %v, %v; want [acme:prod]@done", reply[Body], err)
	}

	// A failing transform interceptor routes to on error.
	failPath := writeDILDoc(t, dir, "fail.json", makeDoc([]map[string]any{
		{"name": "badTx", "at": "before", "mode": "transform", "pointcut": map[string]any{"steps": []string{"use"}},
			"action": map[string]any{"uri": "simplevalidator", "options": map[string]any{"expression": "false"}}},
	}))
	ff, _ := start(t, failPath, log.New(&bytes.Buffer{}, "", 0))
	reply, err = ff.Request(context.Background(), ff.NewMessage())
	if err != nil || reply[Body] != "caught:intercept:badTx:use" {
		t.Fatalf("failing transform reply = %v, %v; want caught:intercept:badTx:use", reply[Body], err)
	}
}

func TestInterceptorGuard(t *testing.T) {
	link := func(id, bound string) map[string]any { return map[string]any{"id": id, "bound": bound} }
	links := func(l ...map[string]any) map[string]any { return map[string]any{"link": l} }
	dir := t.TempDir()

	// Guard with condition + onDeny, caught by on error(errorClass: "*GuardError").
	secretExpr := "${header.role} == 'secret-admin-token'"
	secretDeny := "top-secret-deny-reason"
	doc := map[string]any{"dil": map[string]any{
		"core": map[string]any{"interceptors": map[string]any{"interceptor": []map[string]any{
			{
				"name": "auth", "at": "entry", "mode": "guard",
				"condition": map[string]any{"language": "simple", "expression": secretExpr},
				"onDeny":    map[string]any{"message": secretDeny},
			},
			{
				"name": "bodyCheck", "mode": "guard", "pointcut": map[string]any{"steps": []string{"work"}},
				"condition": map[string]any{"language": "jsonpath", "expression": "$.allowed"},
				"action":    map[string]any{"uri": "setheader", "options": map[string]any{"name": "guarded", "value": "yes"}},
			},
		}}},
		"integrations": map[string]any{"integration": map[string]any{"flows": map[string]any{"flow": map[string]any{
			"id": "guarded", "steps": map[string]any{"step": []map[string]any{
				{"id": "in", "type": "source", "uri": "message:in", "links": links(link("work", "out"))},
				{"id": "work", "type": "sink", "uri": "setbody", "options": map[string]any{"language": "simple", "expression": "ran:${header.guarded}"}, "links": links(link("work", "in"))},
				{"id": "onGuard", "type": "error", "uri": "failedexchange", "options": map[string]any{"errorClass": "*GuardError"}, "links": links(link("denied", "out"))},
				{"id": "denied", "type": "sink", "uri": "setbody", "options": map[string]any{"language": "simple", "expression": "denied:${header.error.class}:${header.error.message}"}, "links": links(link("denied", "in"))},
			}},
		}}}},
	}}
	path := writeDILDoc(t, dir, "guard.json", doc)

	// Inspect/describe must NEVER expose expression text or onDeny message.
	info, err := Inspect(path)
	if err != nil {
		t.Fatal(err)
	}
	rawInfo, _ := json.Marshal(info)
	if strings.Contains(string(rawInfo), secretExpr) || strings.Contains(string(rawInfo), secretDeny) {
		t.Fatalf("Inspect leaked guard expression or onDeny message: %s", rawInfo)
	}

	f, _ := start(t, path, log.New(&bytes.Buffer{}, "", 0))

	// 1. Denied by entry guard: work must not run, caught by *GuardError handler.
	m1 := f.NewMessage()
	m1["role"] = "guest"
	m1[Body] = `{"allowed":true}`
	reply, err := f.Request(context.Background(), m1)
	if err != nil || reply[Body] != "denied:*definition.GuardError:"+secretDeny {
		t.Fatalf("denied reply = %v, %v", reply[Body], err)
	}

	// 2. Denied by jsonpath guard on work.
	m2 := f.NewMessage()
	m2["role"] = "secret-admin-token"
	m2[Body] = `{"allowed":false}`
	reply, err = f.Request(context.Background(), m2)
	if err != nil || !strings.HasPrefix(text(reply[Body]), "denied:*definition.GuardError:guard bodyCheck denied step work") {
		t.Fatalf("jsonpath deny reply = %v, %v", reply[Body], err)
	}

	// 3. Allowed by both guards: guarding action also ran.
	m3 := f.NewMessage()
	m3["role"] = "secret-admin-token"
	m3[Body] = `{"allowed":true}`
	reply, err = f.Request(context.Background(), m3)
	if err != nil || reply[Body] != "ran:yes" {
		t.Fatalf("allowed reply = %v, %v; want ran:yes", reply[Body], err)
	}

	// Flowcheck catches a typo in a guard condition header.
	typoDoc := map[string]any{"dil": map[string]any{
		"core": map[string]any{"interceptors": map[string]any{"interceptor": []map[string]any{
			{"name": "g", "mode": "guard", "pointcut": map[string]any{"steps": []string{"done"}},
				"condition": map[string]any{"language": "simple", "expression": "${header.tennat} == 'acme'"}},
		}}},
		"integrations": map[string]any{"integration": map[string]any{"flows": map[string]any{"flow": map[string]any{
			"id": "typo", "steps": map[string]any{"step": []map[string]any{
				{"id": "in", "type": "source", "uri": "timer:t", "options": map[string]any{"period": 60000}, "links": links(link("s1", "out"))},
				{"id": "s1", "type": "action", "uri": "setheader", "options": map[string]any{"name": "tenant", "value": "acme"}, "links": links(link("s1", "in"), link("done", "out"))},
				{"id": "done", "type": "sink", "uri": "passthrough", "links": links(link("done", "in"))},
			}},
		}}}},
	}}
	typoInfo, err := Inspect(writeDILDoc(t, dir, "typo.json", typoDoc))
	if err != nil {
		t.Fatal(err)
	}
	foundTypo := false
	for _, d := range typoInfo.Diagnostics {
		if d.Code == "header-not-set" && d.Step == "intercept:g:done" && strings.Contains(d.Message, "tenant") {
			foundTypo = true
		}
	}
	if !foundTypo {
		t.Errorf("expected header-not-set diagnostic for guard typo, got %v", typoInfo.Diagnostics)
	}
}

func TestInterceptorAfterObserveAndTransform(t *testing.T) {
	// Use runtimeStep to inspect the exact message and types on both after-observe and after-transform.
	var obsDur any
	var obsTarget any
	obsStep := runtimeStep(t, func(_ context.Context, m Message) (Message, error) {
		obsDur = m[message.StepDuration]
		obsTarget = m[message.StepTarget]
		// Mutating copy in observe mode must NOT reach main message.
		m[Body] = "mutated-by-observer"
		m["leaked"] = "bad"
		return m, nil
	})
	failObsStep := runtimeStep(t, func(_ context.Context, m Message) (Message, error) {
		return nil, fmt.Errorf("observer exploded")
	})

	path := interceptedDIL(t,
		map[string]any{"name": "obs", "at": "after", "pointcut": map[string]any{"steps": []string{"one"}},
			"action": map[string]any{"uri": obsStep}},
		map[string]any{"name": "badObs", "at": "after", "pointcut": map[string]any{"steps": []string{"one"}},
			"action": map[string]any{"uri": failObsStep}},
		map[string]any{"name": "txAfterSink", "at": "after", "mode": "transform", "pointcut": map[string]any{"steps": []string{"two"}},
			"action": map[string]any{"uri": "setheader", "options": map[string]any{"name": "afterTarget", "value": "${header.metadata.step.target}"}}},
	)
	var logged bytes.Buffer
	f, _ := start(t, path, log.New(&logged, "", 0))

	reply, err := f.Request(context.Background(), f.NewMessage())
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	if reply[Body] != "done" || reply["leaked"] != nil || reply["afterTarget"] != "two" {
		t.Errorf("reply = %v, want body=done, no leaked header, afterTarget=two", reply)
	}
	if dur, ok := obsDur.(int64); !ok || dur < 0 {
		t.Errorf("metadata.step.duration = %T (%v), want int64 >= 0", obsDur, obsDur)
	}
	if obsTarget != "one" {
		t.Errorf("metadata.step.target = %v, want one", obsTarget)
	}
	if _, ok := reply[message.StepDuration].(int64); !ok {
		t.Errorf("reply metadata.step.duration = %T (%v), want int64", reply[message.StepDuration], reply[message.StepDuration])
	}
	if !strings.Contains(logged.String(), "observer exploded") {
		t.Errorf("log = %q, want failing after observer logged", logged.String())
	}
}

func TestInterceptorOnErrorAndPolicy(t *testing.T) {
	link := func(id, bound string) map[string]any { return map[string]any{"id": id, "bound": bound} }
	links := func(l ...map[string]any) map[string]any { return map[string]any{"link": l} }
	dir := t.TempDir()

	var seenErrMsg, seenErrStep, seenErrClass, seenErrStack, seenTarget any
	var seenDur any
	var order []string
	onErrCapture := runtimeStep(t, func(_ context.Context, m Message) (Message, error) {
		order = append(order, "onError")
		seenErrMsg = m[message.ErrorMessage]
		seenErrStep = m[message.ErrorStep]
		seenErrClass = m[message.ErrorClass]
		seenErrStack = m[message.ErrorStackTrace]
		seenTarget = m[message.StepTarget]
		seenDur = m[message.StepDuration]
		return m, nil
	})
	onErrBoom := runtimeStep(t, func(_ context.Context, m Message) (Message, error) {
		return nil, fmt.Errorf("onError hook boom")
	})
	routeStep := runtimeStep(t, func(_ context.Context, m Message) (Message, error) {
		order = append(order, "errorRoute")
		m[Body] = "handled:" + text(m[message.ErrorStep])
		return m, nil
	})

	doc := map[string]any{"dil": map[string]any{
		"core": map[string]any{"interceptors": map[string]any{"interceptor": []map[string]any{
			{"name": "cap", "at": "onError", "pointcut": map[string]any{"steps": []string{"check"}},
				"action": map[string]any{"uri": onErrCapture}},
			{"name": "boom", "at": "onError", "pointcut": map[string]any{"steps": []string{"check"}},
				"action": map[string]any{"uri": onErrBoom}},
		}}},
		"integrations": map[string]any{"integration": map[string]any{"flows": map[string]any{"flow": map[string]any{
			"id": "errflow", "steps": map[string]any{"step": []map[string]any{
				{"id": "in", "type": "source", "uri": "message:in", "links": links(link("check", "out"))},
				{"id": "check", "type": "action", "uri": "simplevalidator", "options": map[string]any{"expression": "${body} == 'ok'"}, "links": links(link("check", "in"), link("done", "out"))},
				{"id": "done", "type": "sink", "uri": "passthrough", "links": links(link("done", "in"))},
				{"id": "e", "type": "error", "uri": "failedexchange", "links": links(link("h", "out"))},
				{"id": "h", "type": "sink", "uri": routeStep, "links": links(link("h", "in"))},
			}},
		}}}},
	}}
	path := writeDILDoc(t, dir, "onerror.json", doc)
	var logged bytes.Buffer
	f, _ := start(t, path, log.New(&logged, "", 0))

	m := f.NewMessage()
	m[Body] = "bad"
	reply, err := f.Request(context.Background(), m)
	if err != nil || reply[Body] != "handled:check" {
		t.Fatalf("reply = %v, %v; want handled:check", reply[Body], err)
	}
	if strings.Join(order, ",") != "onError,errorRoute" {
		t.Errorf("order = %v, want onError before errorRoute", order)
	}
	if !strings.Contains(text(seenErrMsg), "validation failed") || seenErrStep != "check" || seenErrClass == "" ||
		!strings.Contains(text(seenErrStack), "step check:") || seenTarget != "check" {
		t.Errorf("captured onError headers: msg=%v step=%v class=%v stack=%v target=%v", seenErrMsg, seenErrStep, seenErrClass, seenErrStack, seenTarget)
	}
	if dur, ok := seenDur.(int64); !ok || dur < 0 {
		t.Errorf("onError duration = %T (%v), want int64 >= 0", seenDur, seenDur)
	}
	if !strings.Contains(logged.String(), "onError hook boom") {
		t.Errorf("log = %q, want failing onError observer logged", logged.String())
	}

	// Policy enforcement covers onError interceptor capabilities, and flowcheck walks onError reads.
	policyDoc := map[string]any{"dil": map[string]any{
		"core": map[string]any{"interceptors": map[string]any{"interceptor": []map[string]any{
			{"name": "alert", "at": "onError", "pointcut": map[string]any{"steps": []string{"done"}},
				"action": map[string]any{"uri": "https://alerts.example.com/hook", "options": map[string]any{"trustStoreFile": ""}}},
			{"name": "badRead", "at": "onError", "pointcut": map[string]any{"steps": []string{"done"}},
				"action": map[string]any{"uri": "logger", "options": map[string]any{"expression": "${header.missingHdr}"}}},
		}}},
		"integrations": map[string]any{"integration": map[string]any{"flows": map[string]any{"flow": map[string]any{
			"id": "pol", "steps": map[string]any{"step": []map[string]any{
				{"id": "in", "type": "source", "uri": "timer:t", "options": map[string]any{"period": 60000}, "links": links(link("done", "out"))},
				{"id": "done", "type": "sink", "uri": "passthrough", "links": links(link("done", "in"))},
			}},
		}}}},
	}}
	polPath := writeDILDoc(t, dir, "pol.json", policyDoc)
	p := prepare(t, polPath)
	info := p.Info()
	foundReadDiag := false
	for _, d := range info.Diagnostics {
		if d.Code == "header-not-set" && d.Step == "intercept:badRead:done" {
			foundReadDiag = true
		}
	}
	if !foundReadDiag {
		t.Errorf("expected flowcheck header-not-set on onError interceptor, got %v", info.Diagnostics)
	}
	sandbox, _ := NewPolicy(nil)
	r := runtimeNew(t, ChannelConfig{})
	r.SetPolicy(sandbox)
	if _, err := p.Load(context.Background(), r, nil, nil); err == nil || !strings.Contains(err.Error(), "intercept:alert:done") || !strings.Contains(err.Error(), "alerts.example.com:443") {
		t.Fatalf("expected policy refusal on onError interceptor, got %v", err)
	}
}

func TestInterceptorScopeProgram(t *testing.T) {
	link := func(id, bound string) map[string]any { return map[string]any{"id": id, "bound": bound} }
	links := func(l ...map[string]any) map[string]any { return map[string]any{"link": l} }
	dir := t.TempDir()

	// Library document defines:
	// - a reusable processor "tag"
	// - a scope:"program" interceptor using processor:tag
	// - a scope:"document" interceptor (must stay local to lib.json!)
	// - a flow "libflow"
	libDoc := map[string]any{"dil": map[string]any{
		"core": map[string]any{
			"processors": map[string]any{"processor": map[string]any{
				"name":       "tag",
				"parameters": []map[string]any{{"name": "val", "type": "string", "default": "global"}},
				"action":     map[string]any{"uri": "setheader", "options": map[string]any{"name": "X-Program", "value": map[string]any{"$param": "val"}}},
			}},
			"interceptors": map[string]any{"interceptor": []map[string]any{
				{"name": "globalTag", "at": "entry", "mode": "transform", "scope": "program",
					"action": map[string]any{"uri": "processor:tag"}},
				{"name": "libLocal", "at": "entry", "mode": "transform", "scope": "document",
					"action": map[string]any{"uri": "setheader", "options": map[string]any{"name": "X-LibLocal", "value": "local"}}},
			}},
		},
		"integrations": map[string]any{"integration": map[string]any{"flows": map[string]any{"flow": map[string]any{
			"id": "libflow", "steps": map[string]any{"step": []map[string]any{
				{"id": "in", "type": "source", "uri": "message:in", "links": links(link("done", "out"))},
				{"id": "done", "type": "sink", "uri": "setbody", "options": map[string]any{"language": "simple", "expression": "${header.X-Program}:${header.X-LibLocal}"}, "links": links(link("done", "in"))},
			}},
		}}}},
	}}
	writeDILDoc(t, dir, "lib.json", libDoc)

	// Main document imports lib.json and defines flow "mainflow".
	mainDoc := map[string]any{"dil": map[string]any{
		"imports": map[string]any{"import": map[string]any{"path": "lib.json", "namespace": "lib"}},
		"integrations": map[string]any{"integration": map[string]any{"flows": map[string]any{"flow": map[string]any{
			"id": "mainflow", "steps": map[string]any{"step": []map[string]any{
				{"id": "in", "type": "source", "uri": "message:in", "links": links(link("done", "out"))},
				{"id": "done", "type": "sink", "uri": "setbody", "options": map[string]any{"language": "simple", "expression": "${header.X-Program}:${header.X-LibLocal}"}, "links": links(link("done", "in"))},
			}},
		}}}},
	}}
	mainPath := writeDILDoc(t, dir, "main.json", mainDoc)

	if err := ValidateProgram(mainPath); err != nil {
		t.Fatalf("ValidateProgram: %v", err)
	}
	info, err := InspectProgram(mainPath)
	if err != nil {
		t.Fatalf("InspectProgram: %v", err)
	}
	byFlow := map[string][]string{}
	for _, pf := range info.Flows {
		for _, s := range pf.Flow.Steps {
			if s.Interceptor {
				byFlow[pf.Flow.ID] = append(byFlow[pf.Flow.ID], s.ID)
			}
		}
	}
	// libflow gets globalTag + libLocal (no double-weaving of globalTag!); mainflow gets ONLY globalTag!
	if strings.Join(byFlow["libflow"], ",") != "intercept:globalTag:done,intercept:libLocal:done" {
		t.Errorf("libflow interceptors = %v", byFlow["libflow"])
	}
	if strings.Join(byFlow["mainflow"], ",") != "intercept:globalTag:done" {
		t.Errorf("mainflow interceptors = %v", byFlow["mainflow"])
	}

	// LoadProgram executes both flows and verifies runtime behavior (and PreparedProgram reuse causes no double-weaving).
	prog, err := LoadProgram(mainPath, nil)
	if err != nil {
		t.Fatalf("LoadProgram: %v", err)
	}
	flowsByID := map[string]*Flow{}
	for _, fl := range prog.Flows {
		if err := fl.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { fl.Stop() })
		flowsByID[fl.ID()] = fl
	}
	replyMain, err := flowsByID["mainflow"].Request(context.Background(), flowsByID["mainflow"].NewMessage())
	if err != nil || replyMain[Body] != "global:" {
		t.Errorf("mainflow reply = %v, %v; want global:", replyMain[Body], err)
	}
	replyLib, err := flowsByID["libflow"].Request(context.Background(), flowsByID["libflow"].NewMessage())
	if err != nil || replyLib[Body] != "global:local" {
		t.Errorf("libflow reply = %v, %v; want global:local", replyLib[Body], err)
	}

	// Duplicate scope:"program" interceptor names across two documents must fail.
	dupDoc := map[string]any{"dil": map[string]any{
		"imports": map[string]any{"import": map[string]any{"path": "lib.json", "namespace": "lib"}},
		"core": map[string]any{"interceptors": map[string]any{"interceptor": map[string]any{
			"name": "globalTag", "scope": "program", "action": map[string]any{"uri": "log"},
		}}},
		"integrations": map[string]any{"integration": map[string]any{"flows": map[string]any{"flow": map[string]any{
			"id": "dupflow", "steps": map[string]any{"step": []map[string]any{
				{"id": "in", "type": "source", "uri": "message:in", "links": links(link("done", "out"))},
				{"id": "done", "type": "sink", "uri": "passthrough", "links": links(link("done", "in"))},
			}},
		}}}},
	}}
	dupPath := writeDILDoc(t, dir, "dup.json", dupDoc)
	if _, err := InspectProgram(dupPath); err == nil || !strings.Contains(err.Error(), `duplicate program interceptor "globalTag"`) {
		t.Errorf("duplicate program interceptor err = %v", err)
	}
}
