package api

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"dif/message"
)

func TestEvalExpression(t *testing.T) {
	ctx := context.Background()

	// Verify simple expression and immutability.
	msg := message.New("hello world")
	msg["tenant"] = "acme"
	before := cloneMessage(msg)

	val, typ, err := EvalExpression(ctx, "simple", "${uppercase(${body})} (${headers.tenant})", msg)
	if err != nil {
		t.Fatalf("simple eval failed: %v", err)
	}
	if val != "HELLO WORLD (acme)" || typ != "string" {
		t.Fatalf("got (%v, %q), want (HELLO WORLD (acme), string)", val, typ)
	}
	if !reflect.DeepEqual(msg, before) {
		t.Fatalf("EvalExpression mutated input message")
	}

	// Verify map / difmap/1 on JSON body + headers.
	jsonMsg := message.New(`{"name":"alice","items":[{"price":10},{"price":25}]}`)
	jsonMsg["currency"] = "EUR"
	for _, lang := range []string{"map", "difmap/1"} {
		v, tn, err := EvalExpression(ctx, lang, `{ "who": upper(payload.name), "total": sum(payload.items map (i) -> i.price), "ccy": headers.currency }`, jsonMsg)
		if err != nil {
			t.Fatalf("%s eval failed: %v", lang, err)
		}
		if tn != "object" {
			t.Fatalf("%s type: got %q, want object", lang, tn)
		}
		m, ok := v.(map[string]any)
		if !ok || m["who"] != "ALICE" || m["total"] != int64(35) || m["ccy"] != "EUR" {
			t.Fatalf("%s unexpected value: %#v", lang, v)
		}
	}

	// Verify jsonpath.
	v, tn, err := EvalExpression(ctx, "jsonpath", "$.items[?(@.price > 15)].price", jsonMsg)
	if err != nil {
		t.Fatalf("jsonpath eval failed: %v", err)
	}
	if tn != "number" || v != float64(25) {
		t.Fatalf("jsonpath got (%#v, %q), want (25, number)", v, tn)
	}

	// Verify xpath.
	xmlMsg := message.New(`<order><id>ORD-9</id><amount>120</amount></order>`)
	v, tn, err = EvalExpression(ctx, "xpath", "/order/id", xmlMsg)
	if err != nil {
		t.Fatalf("xpath eval failed: %v", err)
	}
	if tn != "string" || v != "ORD-9" {
		t.Fatalf("xpath got (%#v, %q), want (ORD-9, string)", v, tn)
	}

	// Unsupported language error.
	if _, _, err := EvalExpression(ctx, "groovy", "1+1", msg); err == nil {
		t.Fatal("expected error for unsupported language")
	}
}

func TestExecInline(t *testing.T) {
	ctx := context.Background()
	msg := message.New("hello")
	msg["keep"] = "yes"
	before := cloneMessage(msg)

	res, tr, err := ExecInline(ctx, `setheader(name: "tag", language: "constant", value: "v1") as h -> setbody(language: "simple", expression: "${uppercase(${body})}") as b -> passthrough as p`, msg)
	if err != nil {
		t.Fatalf("ExecInline failed: %v", err)
	}
	if !reflect.DeepEqual(msg, before) {
		t.Fatal("ExecInline mutated caller message")
	}
	if res.Message[Body] != "HELLO" || res.Message["tag"] != "v1" || res.Message["keep"] != "yes" {
		t.Fatalf("unexpected ExecInline message: %+v", res.Message)
	}
	steps := tr.Snapshot()
	if len(steps) != 3 {
		t.Fatalf("expected 3 traced steps, got %d", len(steps))
	}
	if steps[0].StepID != "h" || steps[0].HeadersAdded["tag"] != "v1" {
		t.Fatalf("unexpected step 0 trace: %+v", steps[0])
	}
	if steps[1].StepID != "b" || steps[1].BodyBefore != "hello" || steps[1].BodyAfter != "HELLO" {
		t.Fatalf("unexpected step 1 trace: %+v", steps[1])
	}

	// Single-step chain (no explicit passthrough sink).
	res2, tr2, err := ExecInline(ctx, `setbody(language: "simple", expression: "ok:${body}")`, message.New("x"))
	if err != nil {
		t.Fatalf("single-step ExecInline failed: %v", err)
	}
	if res2.Message[Body] != "ok:x" || len(tr2.Snapshot()) != 1 {
		t.Fatalf("unexpected single-step result: res=%+v trace=%+v", res2, tr2.Snapshot())
	}

	// Invalid chain fails cleanly.
	if _, _, err := ExecInline(ctx, "", msg); err == nil {
		t.Fatal("expected error for empty chain")
	}
}

func TestTraceFlowAndMethod(t *testing.T) {
	flowSrc := `flow greet {
  message:greet as src
  -> setheader(name: "who", language: "constant", value: "world") as setWho
  -> setbody(language: "simple", expression: "${body} ${headers.who}") as fmt
  -> passthrough as done
}`
	flow, err := LoadBytes([]byte(flowSrc), nil)
	if err != nil {
		t.Fatalf("LoadBytes failed: %v", err)
	}
	res, tr, err := TraceFlow(context.Background(), flow, message.New("hi"))
	if err != nil {
		t.Fatalf("TraceFlow failed: %v", err)
	}
	if res.Message[Body] != "hi world" {
		t.Fatalf("unexpected body: %v", res.Message[Body])
	}
	steps := tr.Snapshot()
	if len(steps) != 3 || steps[1].BodyBefore != "hi" || steps[1].BodyAfter != "hi world" {
		t.Fatalf("unexpected trace steps: %+v", steps)
	}

	// Also verify nil flow guard.
	if _, _, err := TraceFlow(context.Background(), nil, message.New("x")); err == nil || !strings.Contains(err.Error(), "nil") {
		t.Fatalf("expected nil flow error, got %v", err)
	}
}
