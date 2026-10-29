package impl

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"dif/engine"
	flowdef "dif/flows/definition"
	"dif/internal/mapping"
	"dif/message"
	stepdef "dif/steps/definition"
)

func TestHTTPSSourceServiceContractResponsesAndWireHeaders(t *testing.T) {
	ctx := context.Background()
	inFmt := &mapping.Format{
		Name:      "OrderIn",
		MediaType: "application/json",
		SchemaRef: "OrderInSchema",
		Schema:    `{"type":"object","required":["sku"],"properties":{"sku":{"type":"string"}}}`,
	}
	outFmt := &mapping.Format{
		Name:      "OrderOut",
		MediaType: "application/json",
		SchemaRef: "OrderOutSchema",
		Schema:    `{"type":"object","required":["ok"],"properties":{"ok":{"type":"boolean"}}}`,
	}
	errFmt := &mapping.Format{
		Name:      "Problem",
		MediaType: "application/json",
		SchemaRef: "ProblemSchema",
		Schema:    `{"type":"object","required":["code"],"properties":{"code":{"type":"string"}}}`,
	}
	plan, err := mapping.CompileService(ctx, mapping.ServiceDefinition{
		Name:   "PlaceOrder",
		Input:  "OrderIn",
		Output: "OrderOut",
		Error:  "Problem",
		InputHeaders: []mapping.HeaderSpec{
			{Name: "X-API-Key", Type: "string"},
			{Name: "priority", Type: "number", Default: json.RawMessage(`1`)},
			{Name: "dryRun", Type: "boolean", Default: json.RawMessage(`false`)},
		},
		OutputHeaders: []mapping.HeaderSpec{
			{Name: "X-Processed-Priority", Type: "number"},
		},
	}, mapping.ServiceFormats{Input: inFmt, Output: outFmt, Error: errFmt}, "test.json", mapping.Limits{})
	if err != nil {
		t.Fatalf("CompileService: %v", err)
	}

	var capturedEmit stepdef.Emit
	ready := make(chan struct{})
	src := mockHTTPContractSource{onRun: func(emit stepdef.Emit, rdy func()) {
		capturedEmit = emit
		close(ready)
		rdy()
	}}

	act := stepFunc(func(ctx context.Context, m message.Message) (message.Message, error) {
		body, _ := m[message.Body].(string)
		if strings.Contains(body, `"FAULT"`) {
			fault := m.Copy()
			fault[message.Body] = `{"code":"DECLINED"}`
			fault["X-Fault-Code"] = "DECLINED"
			stepdef.Return(ctx, fault, errConst("declined"), false)
			return m, nil
		}
		m["X-Processed-Priority"] = m["priority"]
		if m["dryRun"] == true {
			m[message.Body] = `{"ok":false}`
		} else {
			m[message.Body] = `{"ok":true}`
		}
		return m, nil
	})

	flow := &flowdef.Flow{
		ID: "http-contract-flow",
		Contract: &flowdef.Contract{
			Name:            "PlaceOrder",
			ExchangePattern: message.InOut,
			HasErrorFormat:  true,
			ValidateInput: func(ctx context.Context, m message.Message, wire bool) error {
				return plan.ValidateInput(ctx, map[string]any(m), wire)
			},
			ValidateOutput: func(ctx context.Context, m message.Message) error {
				return plan.ValidateOutput(ctx, map[string]any(m))
			},
			ValidateError: func(ctx context.Context, m message.Message) error {
				return plan.ValidateError(ctx, map[string]any(m))
			},
		},
		Source: &flowdef.Node{
			ID:        "http-in",
			Kind:      flowdef.Source,
			URI:       "rest",
			Processor: src,
			Next: []*flowdef.Node{{
				ID:        "handle",
				Kind:      flowdef.Action,
				URI:       "custom",
				Processor: act,
			}},
		},
	}

	runner := engine.NewRunner(flow, nil)
	if err := runner.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer runner.Stop()
	<-ready

	h := (httpsSource{method: "POST"}).handler(capturedEmit)

	// 1. Valid wire request with HTTP-canonicalized header names and wire scalar text.
	req := httptest.NewRequest("POST", "/orders?dryRun=true", strings.NewReader(`{"sku":"ABC"}`))
	req.Header.Set("X-API-Key", "secret")
	req.Header.Set("priority", "7")
	rec := httptest.NewRecorder()
	h(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("valid status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if gotCT := rec.Header().Get("Content-Type"); gotCT != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", gotCT)
	}
	if gotPrio := rec.Header().Get("X-Processed-Priority"); gotPrio != "7" {
		t.Fatalf("X-Processed-Priority = %q, want 7", gotPrio)
	}
	if rec.Body.String() != `{"ok":false}` {
		t.Fatalf("body = %q, want {\"ok\":false}", rec.Body.String())
	}

	// 2. Unhandled input contract violation -> HTTP 400 Bad Request.
	badReq := httptest.NewRequest("POST", "/orders", strings.NewReader(`{"wrong":1}`))
	badReq.Header.Set("X-API-Key", "secret")
	badRec := httptest.NewRecorder()
	h(badRec, badReq)
	if badRec.Code != http.StatusBadRequest {
		t.Fatalf("invalid input status = %d, want 400 (%s)", badRec.Code, badRec.Body.String())
	}

	// 3. Malformed wire number header -> HTTP 400 Bad Request.
	badNumReq := httptest.NewRequest("POST", "/orders", strings.NewReader(`{"sku":"ABC"}`))
	badNumReq.Header.Set("X-API-Key", "secret")
	badNumReq.Header.Set("priority", "not-a-number")
	badNumRec := httptest.NewRecorder()
	h(badNumRec, badNumReq)
	if badNumRec.Code != http.StatusBadRequest {
		t.Fatalf("invalid wire number status = %d, want 400 (%s)", badNumRec.Code, badNumRec.Body.String())
	}

	// 4. Contracted return(status=error) -> HTTP 500 with structured application/json body and fault headers.
	faultReq := httptest.NewRequest("POST", "/orders", strings.NewReader(`{"sku":"FAULT"}`))
	faultReq.Header.Set("X-API-Key", "secret")
	faultRec := httptest.NewRecorder()
	h(faultRec, faultReq)
	if faultRec.Code != http.StatusInternalServerError {
		t.Fatalf("fault status = %d, want 500", faultRec.Code)
	}
	if gotCT := faultRec.Header().Get("Content-Type"); gotCT != "application/json" {
		t.Fatalf("fault Content-Type = %q, want application/json", gotCT)
	}
	if faultRec.Header().Get("X-Fault-Code") != "DECLINED" || faultRec.Body.String() != `{"code":"DECLINED"}` {
		t.Fatalf("unexpected fault response: headers=%v body=%q", faultRec.Header(), faultRec.Body.String())
	}
}

type mockHTTPContractSource struct {
	onRun func(stepdef.Emit, func())
}

func (m mockHTTPContractSource) Run(ctx context.Context, emit stepdef.Emit) error {
	m.onRun(emit, func() {})
	<-ctx.Done()
	return nil
}

func (m mockHTTPContractSource) RunReady(ctx context.Context, emit stepdef.Emit, ready func()) error {
	m.onRun(emit, ready)
	<-ctx.Done()
	return nil
}

type stepFunc func(context.Context, message.Message) (message.Message, error)

func (f stepFunc) Process(ctx context.Context, m message.Message) (message.Message, error) {
	return f(ctx, m)
}

type errConst string

func (e errConst) Error() string { return string(e) }
