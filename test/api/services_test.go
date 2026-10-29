package api_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"dif/api"
	"dif/engine"
	flowdef "dif/flows/definition"
)

func TestServiceContractsExampleAndRedaction(t *testing.T) {
	// 1. Standalone contract-only module with zero flows validates cleanly.
	if err := api.ValidateProgram("../../api/testdata/services/contracts.json"); err != nil {
		t.Fatalf("ValidateProgram(contracts.json): %v", err)
	}

	// 2. Multi-module service program loads, runs InOut + InOnly + structured FaultError, and drains cleanly.
	rt, err := api.NewRuntime(api.ChannelConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()

	var audited atomic.Int32
	prepared, err := api.PrepareProgram(context.Background(), []api.ProgramSource{{
		Name: "orders.json",
		Path: "../../api/testdata/services/orders.json",
	}}, api.ProgramLimits{})
	if err != nil {
		t.Fatalf("PrepareProgram(orders.json): %v", err)
	}

	prog, err := prepared.Load(context.Background(), rt, func(flowID string, res *api.Result, err error) {
		if flowID == "record-audit-provider" && err == nil && res != nil && res.Message[api.Body] == "accepted ORD-BOOK-1" {
			audited.Add(1)
		}
	}, nil)
	if err != nil {
		t.Fatalf("Load(orders.json): %v", err)
	}

	runners := make([]*engine.Runner, 0, len(prog.Flows))
	byID := map[string]*api.Flow{}
	for _, f := range prog.Flows {
		runners = append(runners, f.Runner)
		byID[f.ID()] = f
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := engine.StartGroup(ctx, runners); err != nil {
		t.Fatalf("StartGroup: %v", err)
	}
	defer func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer stopCancel()
		for _, f := range prog.Flows {
			_ = f.StopContext(stopCtx)
		}
	}()

	// Valid order: default priority=1 applied, OrderOut validated, Order-Status header returned, audit triggered.
	submit := byID["submit-order"]
	out, err := submit.Request(ctx, submit.NewMessage())
	if err != nil {
		t.Fatalf("submit-order Request: %v", err)
	}
	if out[api.Body] != `{"orderId":"ORD-BOOK-1","sku":"BOOK-1","quantity":2,"priority":1}` {
		t.Fatalf("unexpected submit-order body: %v", out[api.Body])
	}
	if out["Order-Status"] != "ACCEPTED" || out["Content-Type"] != "application/json" {
		t.Fatalf("unexpected submit-order headers: %#v", out)
	}
	if audited.Load() != 1 {
		t.Fatalf("expected 1 audit message, got %d", audited.Load())
	}

	// Out-of-stock order: provider returns validated OrderProblem via return(status=error),
	// caller catches *FaultError and exposes structured error.body.
	oos := byID["submit-out-of-stock"]
	faultOut, err := oos.Request(ctx, oos.NewMessage())
	if err != nil {
		t.Fatalf("submit-out-of-stock Request: %v", err)
	}
	if faultOut[api.Body] != `{"code":"OUT_OF_STOCK","sku":"OUT-OF-STOCK"}` {
		t.Fatalf("unexpected fault recovery body: %v", faultOut[api.Body])
	}
	if faultOut["error.contentType"] != "application/json" {
		t.Fatalf("expected error.contentType=application/json, got %#v", faultOut["error.contentType"])
	}

	// Caller-side pre-validation rejects malformed request before enqueueing to provider.
	providerBefore := byID["place-order-provider"].Status()
	_, err = submit.Request(ctx, api.Message{api.Body: `{"sku":"","quantity":0}`})
	var ce *flowdef.ContractError
	if !errors.As(err, &ce) || ce.Kind != "input" || ce.Service != "PlaceOrder" {
		t.Fatalf("expected caller input ContractError, got %T (%v)", err, err)
	}
	providerAfter := byID["place-order-provider"].Status()
	if providerAfter.Completed != providerBefore.Completed || providerAfter.Failed != providerBefore.Failed {
		t.Fatalf("provider should not have received caller-invalid message: before=%+v after=%+v", providerBefore, providerAfter)
	}

	// Redaction check: InspectProgram exposes ServiceInfo metadata without schema contents or header default literals.
	rawInfo, err := json.Marshal(prog.Info)
	if err != nil {
		t.Fatal(err)
	}
	text := string(rawInfo)
	for _, secret := range []string{"minLength", "idem-100", "OUT_OF_STOCK"} {
		if strings.Contains(text, secret) {
			t.Fatalf("ProgramInfo leaked %q: %s", secret, text)
		}
	}
	if !strings.Contains(text, `"name":"PlaceOrder"`) || !strings.Contains(text, `"hasDefault":true`) {
		t.Fatalf("ProgramInfo missing service metadata: %s", text)
	}
}

func TestServiceProviderCardinalityAndCrossLoadBindings(t *testing.T) {
	dir := t.TempDir()
	contractsPath := saveProgram(t, dir, "contracts.json", programJSON(t, map[string]any{
		"formats": map[string]any{"format": map[string]any{"name": "Txt", "mediaType": "text/plain"}},
		"services": map[string]any{"service": []map[string]any{
			{"name": "Echo", "exchangePattern": "InOut", "input": "Txt", "output": "Txt"},
			{"name": "Notify", "exchangePattern": "InOnly", "input": "Txt"},
		}},
	}, nil))

	providerDoc := programJSON(t, nil, map[string]any{"path": "contracts.json", "namespace": "svc"},
		map[string]any{
			"id": "echo-provider",
			"steps": map[string]any{"step": []map[string]any{
				{"id": "in", "type": "source", "uri": "service:svc:Echo", "links": map[string]any{"link": map[string]any{"id": "1", "bound": "out"}}},
				{"id": "out", "type": "sink", "uri": "passthrough", "links": map[string]any{"link": map[string]any{"id": "1", "bound": "in"}}},
			}},
		},
		map[string]any{
			"id": "notify-provider",
			"steps": map[string]any{"step": []map[string]any{
				{"id": "in", "type": "source", "uri": "service:svc:Notify", "links": map[string]any{"link": map[string]any{"id": "1", "bound": "out"}}},
				{"id": "out", "type": "sink", "uri": "passthrough", "links": map[string]any{"link": map[string]any{"id": "1", "bound": "in"}}},
			}},
		},
	)
	providerPath := saveProgram(t, dir, "provider.json", providerDoc)

	// Caller imports ONLY contracts.json (under a different alias "c"), never provider.json!
	callerDoc := programJSON(t, nil, map[string]any{"path": "contracts.json", "namespace": "c"},
		programFlow("echo-caller", "",
			map[string]any{"id": "call", "type": "action", "uri": "service:c:Echo"},
		),
		map[string]any{
			"id": "notify-caller",
			"steps": map[string]any{"step": []map[string]any{
				{"id": "in", "type": "source", "uri": "message", "links": map[string]any{"link": map[string]any{"id": "1", "bound": "out"}}},
				{"id": "sink", "type": "sink", "uri": "service:c:Notify", "options": map[string]any{"delivery": "enqueue"}, "links": map[string]any{"link": map[string]any{"id": "1", "bound": "in"}}},
			}},
		},
	)
	callerPath := saveProgram(t, dir, "caller.json", callerDoc)
	_ = contractsPath

	// 1. Loading caller.json alone fails CheckFlowLinks (missing service provider).
	callerPrep, err := api.PrepareProgram(context.Background(), []api.ProgramSource{{Name: "caller.json", Path: callerPath}}, api.ProgramLimits{})
	if err != nil {
		t.Fatalf("PrepareProgram(caller): %v", err)
	}
	if err := callerPrep.CheckFlowLinks(nil); err == nil || !strings.Contains(err.Error(), "missing service provider") {
		t.Fatalf("expected missing service provider error, got %v", err)
	}

	// 2. Loading provider.json first, then checking/loading caller.json against FlowBindings succeeds!
	rt, err := api.NewRuntime(api.ChannelConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()

	provPrep, err := api.PrepareProgram(context.Background(), []api.ProgramSource{{Name: "provider.json", Path: providerPath}}, api.ProgramLimits{})
	if err != nil {
		t.Fatalf("PrepareProgram(provider): %v", err)
	}
	provProg, err := provPrep.Load(context.Background(), rt, nil, nil)
	if err != nil {
		t.Fatalf("Load(provider): %v", err)
	}
	provRunners := []*engine.Runner{provProg.Flows[0].Runner, provProg.Flows[1].Runner}
	existing := api.FlowBindings(provRunners)

	// Duplicate provider across interactive/host load is rejected.
	if err := provPrep.CheckFlowLinks(existing); err == nil || !strings.Contains(err.Error(), "duplicate service provider") {
		t.Fatalf("expected duplicate service provider against existing bindings, got %v", err)
	}

	// Caller binds to existing provider via canonical service ID across separate loads.
	callerProg, err := callerPrep.Load(context.Background(), rt, nil, existing)
	if err != nil {
		t.Fatalf("Load(caller with existing): %v", err)
	}

	allRunners := append(provRunners, callerProg.Flows[0].Runner, callerProg.Flows[1].Runner)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := engine.StartGroup(ctx, allRunners); err != nil {
		t.Fatalf("StartGroup: %v", err)
	}
	defer func() {
		for _, r := range allRunners {
			r.Cancel()
		}
	}()

	reply, err := callerProg.Flows[0].Request(ctx, api.Message{api.Body: "hello capability"})
	if err != nil || reply[api.Body] != "hello capability" || reply["Content-Type"] != "text/plain" {
		t.Fatalf("unexpected Echo reply: (%#v, %v)", reply, err)
	}
	if _, err := callerProg.Flows[1].Request(ctx, api.Message{api.Body: "queued audit"}); err != nil {
		t.Fatalf("Notify enqueue failed: %v", err)
	}

	// 3. Duplicate provider within the same program is rejected during PrepareProgram.
	dupProviderPath := saveProgram(t, dir, "dup.json", programJSON(t, nil, map[string]any{"path": "contracts.json", "namespace": "other"},
		map[string]any{
			"id": "echo-provider-2",
			"steps": map[string]any{"step": []map[string]any{
				{"id": "in", "type": "source", "uri": "service:other:Echo", "links": map[string]any{"link": map[string]any{"id": "1", "bound": "out"}}},
				{"id": "out", "type": "sink", "uri": "passthrough", "links": map[string]any{"link": map[string]any{"id": "1", "bound": "in"}}},
			}},
		},
	))
	_, err = api.PrepareProgram(context.Background(), []api.ProgramSource{
		{Name: "provider.json", Path: providerPath},
		{Name: "dup.json", Path: dupProviderPath},
	}, api.ProgramLimits{})
	if err == nil || !strings.Contains(err.Error(), "duplicate service provider") {
		t.Fatalf("expected duplicate service provider in group, got %v", err)
	}
}

func TestServiceStaticMEPAndOptionChecks(t *testing.T) {
	core := map[string]any{
		"formats": map[string]any{"format": map[string]any{"name": "Txt", "mediaType": "text/plain"}},
		"services": map[string]any{"service": []map[string]any{
			{"name": "Ask", "exchangePattern": "InOut", "input": "Txt", "output": "Txt"},
			{"name": "Fire", "exchangePattern": "InOnly", "input": "Txt"},
		}},
	}

	// 1. One-way timer source cannot bind InOut options.service.
	timerInOut := programJSON(t, core, nil, map[string]any{
		"id":      "bad-timer",
		"options": map[string]any{"service": "Ask"},
		"steps": map[string]any{"step": []map[string]any{
			{"id": "in", "type": "source", "uri": "timer:tick", "options": map[string]any{"period": 1000}, "links": map[string]any{"link": map[string]any{"id": "1", "bound": "out"}}},
			{"id": "out", "type": "sink", "uri": "wastebin", "links": map[string]any{"link": map[string]any{"id": "1", "bound": "in"}}},
		}},
	})
	if _, err := api.InspectBytes(timerInOut); err == nil || !strings.Contains(err.Error(), "one-way source") {
		t.Fatalf("expected timer InOut rejection, got %v", err)
	}

	// 2. REST default InOut source cannot bind InOnly options.service unless exchangePattern=InOnly.
	restInOnlyBad := programJSON(t, core, nil, map[string]any{
		"id":      "bad-rest",
		"options": map[string]any{"service": "Fire"},
		"steps": map[string]any{"step": []map[string]any{
			{"id": "in", "type": "source", "uri": "rest://127.0.0.1:0/fire", "links": map[string]any{"link": map[string]any{"id": "1", "bound": "out"}}},
			{"id": "out", "type": "sink", "uri": "wastebin", "links": map[string]any{"link": map[string]any{"id": "1", "bound": "in"}}},
		}},
	})
	if _, err := api.InspectBytes(restInOnlyBad); err == nil || !strings.Contains(err.Error(), "incompatible with InOnly service") {
		t.Fatalf("expected rest InOut vs InOnly rejection, got %v", err)
	}

	restInOnlyOK := programJSON(t, core, nil, map[string]any{
		"id":      "ok-rest",
		"options": map[string]any{"service": "Fire"},
		"steps": map[string]any{"step": []map[string]any{
			{"id": "in", "type": "source", "uri": "rest://127.0.0.1:0/fire", "options": map[string]any{"exchangePattern": "InOnly"}, "links": map[string]any{"link": map[string]any{"id": "1", "bound": "out"}}},
			{"id": "out", "type": "sink", "uri": "wastebin", "links": map[string]any{"link": map[string]any{"id": "1", "bound": "in"}}},
		}},
	})
	info, err := api.InspectBytes(restInOnlyOK)
	if err != nil || info.Service == nil || info.Service.Name != "Fire" {
		t.Fatalf("InspectBytes(restInOnlyOK): info=%+v err=%v", info, err)
	}

	// 3. InOut service cannot be invoked as a sink or with delivery=enqueue.
	sinkInOut := programJSON(t, core, nil, map[string]any{
		"id": "bad-sink",
		"steps": map[string]any{"step": []map[string]any{
			{"id": "in", "type": "source", "uri": "message", "links": map[string]any{"link": map[string]any{"id": "1", "bound": "out"}}},
			{"id": "s", "type": "sink", "uri": "service:Ask", "links": map[string]any{"link": map[string]any{"id": "1", "bound": "in"}}},
		}},
	})
	if _, err := api.InspectBytes(sinkInOut); err == nil || !strings.Contains(err.Error(), "requires an action step") {
		t.Fatalf("expected InOut sink rejection, got %v", err)
	}

	enqueueInOut := programJSON(t, core, nil, programFlow("bad-enqueue", "",
		map[string]any{"id": "call", "type": "action", "uri": "service:Ask", "options": map[string]any{"delivery": "enqueue"}},
	))
	if _, err := api.InspectBytes(enqueueInOut); err == nil || !strings.Contains(err.Error(), "delivery=enqueue") {
		t.Fatalf("expected InOut delivery=enqueue rejection, got %v", err)
	}
}
