package impl

import (
	"context"
	"errors"
	"fmt"
	"testing"

	flowdef "dif/flows/definition"
	"dif/message"
	stepdef "dif/steps/definition"
	"dif/steps/registry"
)

func TestGuardAction(t *testing.T) {
	r := registry.New()
	if err := Register(r); err != nil {
		t.Fatal(err)
	}

	makeGuard := func(opts map[string]any) stepdef.ActionProcessor {
		t.Helper()
		p, err := r.Processor(&flowdef.Node{ID: "intercept:auth:call", Kind: stepdef.Action, URI: "guard", Options: opts})
		if err != nil {
			t.Fatalf("new guard: %v", err)
		}
		return p.(stepdef.ActionProcessor)
	}

	// Simple language: passes and denies with *flowdef.GuardError without unwrapping.
	g := makeGuard(map[string]any{
		"expression":  "${header.role} == 'admin'",
		"message":     "admin role required",
		"interceptor": "auth",
		"targetStep":  "call",
	})
	m := message.New("hi")
	m["role"] = "admin"
	out, err := g.Process(context.Background(), m)
	if err != nil || out[message.Body] != "hi" {
		t.Fatalf("allowed = %v, %v", out, err)
	}

	m["role"] = "guest"
	_, err = g.Process(context.Background(), m)
	var ge *flowdef.GuardError
	if !errors.As(err, &ge) {
		t.Fatalf("err = %T (%v), want *flowdef.GuardError", err, err)
	}
	if errors.Unwrap(ge) != nil || fmt.Sprintf("%T", ge) != "*definition.GuardError" {
		t.Errorf("GuardError unwrap=%v type=%T", errors.Unwrap(ge), ge)
	}
	if ge.Interceptor != "auth" || ge.Step != "call" || ge.Reason != "admin role required" || ge.Error() != "admin role required" {
		t.Errorf("GuardError = %+v", ge)
	}

	// XPath language.
	xg := makeGuard(map[string]any{"language": "xpath", "expression": "/order/total > 10", "interceptor": "minTotal", "targetStep": "ship"})
	if _, err := xg.Process(context.Background(), message.New(`<order><total>25</total></order>`)); err != nil {
		t.Errorf("xpath pass: %v", err)
	}
	if _, err := xg.Process(context.Background(), message.New(`<order><total>5</total></order>`)); !errors.As(err, &ge) || ge.Error() != "guard minTotal denied step ship" {
		t.Errorf("xpath deny: %v", err)
	}

	// JSONPath language.
	jg := makeGuard(map[string]any{"language": "jsonpath", "expression": "$.approved"})
	if _, err := jg.Process(context.Background(), message.New(`{"approved":true}`)); err != nil {
		t.Errorf("jsonpath pass: %v", err)
	}
	if _, err := jg.Process(context.Background(), message.New(`{"approved":false}`)); !errors.As(err, &ge) {
		t.Errorf("jsonpath deny: %v", err)
	}

	// Effect and Capabilities.
	node := &flowdef.Node{ID: "g", Kind: stepdef.Action, URI: "guard", Options: map[string]any{"expression": "${header.token} != null"}}
	eff := r.Effect(node)
	if eff == nil || !eff.KeepBody || eff.Headers != stepdef.HeadersKeep || len(eff.Reads) != 1 || eff.Reads[0][0] != "token" {
		t.Errorf("effect = %+v", eff)
	}
	caps, declared := r.Capabilities(node)
	if !declared || len(caps) != 0 {
		t.Errorf("capabilities = %v (declared=%v), want none", caps, declared)
	}
}
