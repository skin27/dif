package mapping

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestTextPlainFormatAndMapping(t *testing.T) {
	ctx := context.Background()
	plain := Format{Name: "Plain", MediaType: "text/plain"}
	if err := ValidateFormat(plain, Limits{}); err != nil {
		t.Fatalf("ValidateFormat(text/plain): %v", err)
	}
	if err := ValidateFormat(Format{Name: "BadSchema", MediaType: "text/plain", SchemaRef: "s", Schema: `{}`}, Limits{}); err == nil {
		t.Fatal("expected schemaRef on text/plain to fail")
	}
	if err := ValidateFormat(Format{Name: "BadReader", MediaType: "text/plain", Reader: map[string]json.RawMessage{"header": json.RawMessage(`true`)}}, Limits{}); err == nil {
		t.Fatal("expected CSV reader setting on text/plain to fail")
	}

	jsonFmt := Format{Name: "Doc", MediaType: "application/json"}
	toPlain, err := Compile(ctx, Definition{
		Name:       "ToPlain",
		Input:      "Doc",
		Output:     "Plain",
		Language:   "difmap/1",
		Expression: `upper(payload.greeting)`,
	}, jsonFmt, plain, "test", Limits{})
	if err != nil {
		t.Fatalf("Compile(ToPlain): %v", err)
	}
	res, err := toPlain.Apply(ctx, `{"greeting":"hello"}`, "application/json", nil)
	if err != nil || res != "HELLO" || toPlain.ContentType() != "text/plain" {
		t.Fatalf("got (%q, %q, %v)", res, toPlain.ContentType(), err)
	}

	badPlain, err := Compile(ctx, Definition{
		Name:       "BadPlain",
		Input:      "Plain",
		Output:     "Plain",
		Language:   "difmap/1",
		Expression: `{notText: payload}`,
	}, plain, plain, "test", Limits{})
	if err != nil {
		t.Fatalf("Compile(BadPlain): %v", err)
	}
	if _, err := badPlain.Apply(ctx, "hello", "text/plain", nil); err == nil {
		t.Fatal("expected non-string text/plain output to fail")
	}
}

func TestServiceContractCompileValidation(t *testing.T) {
	ctx := context.Background()
	jsonFmt := &Format{
		Name:      "OrderIn",
		MediaType: "application/json",
		SchemaRef: "OrderInSchema",
		Schema:    `{"type":"object","required":["sku"],"properties":{"sku":{"type":"string"}}}`,
	}

	t.Run("InOnly rejects output error and outputHeaders", func(t *testing.T) {
		for _, tc := range []ServiceDefinition{
			{Name: "Notify", ExchangePattern: "InOnly", Output: "OrderIn"},
			{Name: "Notify", ExchangePattern: "InOnly", Error: "OrderIn"},
			{Name: "Notify", ExchangePattern: "InOnly", OutputHeaders: []HeaderSpec{{Name: "X-Out", Type: "string"}}},
		} {
			fmts := ServiceFormats{}
			if tc.Output != "" {
				fmts.Output = jsonFmt
			}
			if tc.Error != "" {
				fmts.Error = jsonFmt
			}
			if _, err := CompileService(ctx, tc, fmts, "orders.json", Limits{}); err == nil {
				t.Fatalf("expected error for %+v", tc)
			}
		}
	})

	t.Run("header validation rules", func(t *testing.T) {
		reqTrue := true
		badCases := [][]HeaderSpec{
			{{Name: "", Type: "string"}},
			{{Name: "body", Type: "string"}},
			{{Name: "metadata.traceid", Type: "string"}},
			{{Name: "X-A", Type: "string"}, {Name: "X-A", Type: "number"}},
			{{Name: "X-Xml", Type: "xml"}},
			{{Name: "X-Bad", Type: "number", Default: json.RawMessage(`"nope"`)}},
			{{Name: "X-Contra", Type: "number", Required: &reqTrue, Default: json.RawMessage(`1`)}},
		}
		for _, hdrs := range badCases {
			_, err := CompileService(ctx, ServiceDefinition{Name: "S", InputHeaders: hdrs}, ServiceFormats{}, "a.json", Limits{})
			if err == nil {
				t.Fatalf("expected error for headers %+v", hdrs)
			}
		}
	})
}

func TestServicePlanRuntimeValidation(t *testing.T) {
	ctx := context.Background()
	inFmt := &Format{
		Name:      "OrderIn",
		MediaType: "application/json",
		SchemaRef: "OrderInSchema",
		Schema:    `{"type":"object","required":["sku","qty"],"properties":{"sku":{"type":"string"},"qty":{"type":"integer","minimum":1}}}`,
		Origin:    "contracts.json",
	}
	outFmt := &Format{
		Name:      "OrderOut",
		MediaType: "application/json",
		SchemaRef: "OrderOutSchema",
		Schema:    `{"type":"object","required":["orderId"],"properties":{"orderId":{"type":"string"}}}`,
		Origin:    "contracts.json",
	}
	errFmt := &Format{
		Name:      "Problem",
		MediaType: "application/json",
		SchemaRef: "ProblemSchema",
		Schema:    `{"type":"object","required":["code"],"properties":{"code":{"type":"string"}}}`,
		Origin:    "contracts.json",
	}

	reqFalse := false
	plan, err := CompileService(ctx, ServiceDefinition{
		Name:            "PlaceOrder",
		ExchangePattern: "inout",
		Input:           "OrderIn",
		Output:          "OrderOut",
		Error:           "Problem",
		InputHeaders: []HeaderSpec{
			{Name: "X-API-Key", Type: "string"},
			{Name: "priority", Type: "number", Default: json.RawMessage(`1`)},
			{Name: "dryRun", Type: "boolean", Required: &reqFalse},
			{Name: "tags", Type: "array", Default: json.RawMessage(`["std"]`)},
		},
		OutputHeaders: []HeaderSpec{
			{Name: "Order-Status", Type: "string"},
		},
	}, ServiceFormats{Input: inFmt, Output: outFmt, Error: errFmt}, "contracts.json", Limits{})
	if err != nil {
		t.Fatalf("CompileService: %v", err)
	}
	if plan.ExchangePattern() != "InOut" {
		t.Fatalf("ExchangePattern = %q, want InOut", plan.ExchangePattern())
	}

	// Valid internal message: stamps Content-Type, applies fresh defaults, preserves extra headers.
	m1 := map[string]any{
		"body":      `{"sku":"BOOK-1","qty":2}`,
		"X-API-Key": "secret",
		"Extra":     "kept",
	}
	if err := plan.ValidateInput(ctx, m1, false); err != nil {
		t.Fatalf("ValidateInput valid: %v", err)
	}
	if m1["Content-Type"] != "application/json" || m1["priority"] != json.Number("1") || m1["Extra"] != "kept" {
		t.Fatalf("unexpected mutated m1: %#v", m1)
	}
	if _, hasDry := m1["dryRun"]; hasDry {
		t.Fatal("optional header without default should stay absent")
	}
	// Mutating defaulted slice on m1 must not affect subsequent messages.
	m1["tags"].([]any)[0] = "changed"
	m2 := map[string]any{"body": `{"sku":"BOOK-1","qty":1}`, "X-API-Key": "secret"}
	if err := plan.ValidateInput(ctx, m2, false); err != nil {
		t.Fatalf("ValidateInput m2: %v", err)
	}
	if m2["tags"].([]any)[0] != "std" {
		t.Fatalf("default slice was aliased across messages: %#v", m2["tags"])
	}

	// Internal mode rejects string numbers without coercion and does not mutate message on failure.
	badInternal := map[string]any{
		"body":      `{"sku":"BOOK-1","qty":1}`,
		"X-API-Key": "secret",
		"priority":  "5",
	}
	if err := plan.ValidateInput(ctx, badInternal, false); err == nil {
		t.Fatal("expected internal string number header to fail")
	}
	if _, hasCT := badInternal["Content-Type"]; hasCT {
		t.Fatal("failed validation must not partially mutate message")
	}

	// Wire mode parses number/boolean strings and canonical HTTP header casing.
	wireMsg := map[string]any{
		"body":      `{"sku":"BOOK-1","qty":3}`,
		"X-Api-Key": "wire-secret",
		"Priority":  "5",
		"dryRun":    "true",
	}
	if err := plan.ValidateInput(ctx, wireMsg, true); err != nil {
		t.Fatalf("ValidateInput wire: %v", err)
	}
	if wireMsg["X-API-Key"] != "wire-secret" || wireMsg["priority"] != json.Number("5") || wireMsg["dryRun"] != true {
		t.Fatalf("unexpected wireMsg: %#v", wireMsg)
	}
	if _, stillCanon := wireMsg["X-Api-Key"]; stillCanon {
		t.Fatal("canonicalized duplicate X-Api-Key should have been normalized to X-API-Key")
	}

	// Missing body is rejected when format is declared.
	if err := plan.ValidateInput(ctx, map[string]any{"X-API-Key": "s"}, false); err == nil {
		t.Fatal("expected missing body to fail")
	}
	// Conflicting Content-Type is rejected.
	if err := plan.ValidateInput(ctx, map[string]any{"body": `{"sku":"A","qty":1}`, "Content-Type": "text/plain", "X-API-Key": "s"}, false); err == nil {
		t.Fatal("expected conflicting Content-Type to fail")
	}

	// Output & Error validation.
	outMsg := map[string]any{"body": `{"orderId":"ORD-9"}`, "Order-Status": "CREATED"}
	if err := plan.ValidateOutput(ctx, outMsg); err != nil || outMsg["Content-Type"] != "application/json" {
		t.Fatalf("ValidateOutput: %v (%#v)", err, outMsg)
	}
	errMsg := map[string]any{"body": `{"code":"OUT_OF_STOCK"}`}
	if err := plan.ValidateError(ctx, errMsg); err != nil || errMsg["Content-Type"] != "application/json" {
		t.Fatalf("ValidateError: %v (%#v)", err, errMsg)
	}
	if err := plan.ValidateError(ctx, map[string]any{"body": `{"wrong":1}`}); err == nil {
		t.Fatal("expected invalid error payload to fail")
	}

	// Bodyless contract rejects non-empty body and accepts nil / "" / []byte{}.
	bodyless, err := CompileService(ctx, ServiceDefinition{Name: "Ping"}, ServiceFormats{}, "ping.json", Limits{})
	if err != nil {
		t.Fatalf("CompileService(Ping): %v", err)
	}
	for _, okBody := range []any{nil, "", []byte{}} {
		m := map[string]any{}
		if okBody != nil {
			m["body"] = okBody
		}
		if err := bodyless.ValidateInput(ctx, m, false); err != nil {
			t.Fatalf("bodyless ValidateInput(%#v): %v", okBody, err)
		}
	}
	if err := bodyless.ValidateInput(ctx, map[string]any{"body": "unexpected"}, false); err == nil || !strings.Contains(err.Error(), "no payload") {
		t.Fatalf("expected bodyless rejection, got %v", err)
	}
}
