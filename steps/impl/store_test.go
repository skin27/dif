package impl

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	flowdef "dif/flows/definition"
	"dif/internal/channels"
	"dif/message"
	stepdef "dif/steps/definition"
)

// testStore declares a memory store named after the test in the default
// channels, which the steps of these tests use.
func testStore(t *testing.T) *channels.Store {
	t.Helper()
	s, err := defaultChannels.DeclareStore(t.Name(), channels.StoreConfig{})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestStoreStep(t *testing.T) {
	s := testStore(t)
	uri := "store:" + t.Name()
	put := func(m message.Message, opts map[string]any) {
		t.Helper()
		o := map[string]any{"operation": "put", "key": "${header.id}"}
		for k, v := range opts {
			o[k] = v
		}
		process(t, uri, o, m)
	}
	get := func(id string, opts map[string]any) message.Message {
		t.Helper()
		o := map[string]any{"operation": "get", "key": "${header.id}"}
		for k, v := range opts {
			o[k] = v
		}
		m := message.New("unchanged")
		m["id"] = id
		return process(t, uri, o, m)
	}

	// put keeps the body (or value) with its type.
	m := message.New(map[string]any{"rate": 1.1})
	m["id"], m["n"] = "EUR", int64(7)
	put(m, nil)
	put(m, map[string]any{"key": "count-${header.id}", "value": "${header.n}"})
	put(m, map[string]any{"key": "text", "value": "rate of ${header.id}"})
	if v, ok, _ := s.Get("EUR"); !ok || !reflect.DeepEqual(v, map[string]any{"rate": 1.1}) {
		t.Errorf("put body: %#v", v)
	}
	if v, _, _ := s.Get("count-EUR"); v != int64(7) {
		t.Errorf("put header: %#v", v)
	}
	if v, _, _ := s.Get("text"); v != "rate of EUR" {
		t.Errorf("put template: %#v", v)
	}

	// get sets the header, or the body.
	if out := get("EUR", map[string]any{"headerName": "rate"}); !reflect.DeepEqual(out["rate"], map[string]any{"rate": 1.1}) || out[message.Body] != "unchanged" {
		t.Errorf("get to header: %v", out)
	}
	if out := get("EUR", nil); !reflect.DeepEqual(out[message.Body], map[string]any{"rate": 1.1}) {
		t.Errorf("get to body: %v", out)
	}
	// A missing key changes nothing, unless default gives a value.
	if out := get("USD", map[string]any{"headerName": "rate"}); out["rate"] != nil {
		t.Errorf("missing key set the header: %v", out)
	}
	if out := get("USD", nil); out[message.Body] != "unchanged" {
		t.Errorf("missing key changed the body: %v", out)
	}
	if out := get("USD", map[string]any{"headerName": "rate", "default": "none for ${header.id}"}); out["rate"] != "none for USD" {
		t.Errorf("default: %v", out)
	}

	// remove.
	process(t, uri, map[string]any{"operation": "remove", "key": "EUR"}, message.New(nil))
	if _, ok, _ := s.Get("EUR"); ok {
		t.Error("remove left the key")
	}

	// An empty key fails.
	p := mustProcessor(t, stepdef.Action, uri, map[string]any{"operation": "get", "key": "${header.none}"}).(stepdef.ActionProcessor)
	if _, err := p.Process(context.Background(), message.New(nil)); err == nil || !strings.Contains(err.Error(), "key is empty") {
		t.Errorf("empty key: %v", err)
	}

	wantInvalid(t, stepdef.Action, uri, map[string]any{"key": "k"}, "missing required option operation")
	wantInvalid(t, stepdef.Action, uri, map[string]any{"operation": "get"}, "missing required option key")
	wantInvalid(t, stepdef.Action, uri, map[string]any{"operation": "clear", "key": "k"}, "option operation")
	wantInvalid(t, stepdef.Action, uri, map[string]any{"operation": "get", "key": "k", "ttl": 1}, "ttl")
	wantInvalid(t, stepdef.Action, uri, map[string]any{"operation": "get", "key": "k", "headerName": "body"}, "reserved for the body")
	wantInvalid(t, stepdef.Action, "store", map[string]any{"operation": "get", "key": "k"}, "no store name")
	wantInvalid(t, stepdef.Action, "store:undeclared", map[string]any{"operation": "get", "key": "k"}, `store "undeclared" is not declared`)
}

// TestStoreStepDeclared makes the step as a loader does, with the stores the
// flow may refer to: the step declares the store in its runtime, and a name
// no declaration has fails.
func TestStoreStepDeclared(t *testing.T) {
	rt, err := NewChannels(channels.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	stores := map[string]stepdef.Store{"rates": {Name: "rates", TTL: time.Hour}, "lib:codes": {Name: "codes"}}
	node := func(uri string) *flowdef.Node {
		return &flowdef.Node{ID: "s", Kind: stepdef.Action, URI: uri, Options: map[string]any{"operation": "put", "key": "k"},
			Bindings: stepdef.Params{stepdef.StoresBinding: stores}}
	}
	if _, err := steps.ProcessorWithParams(node("store:rates"), stepdef.Params{ChannelRuntimeKey: rt}); err != nil {
		t.Fatal(err)
	}
	if s := rt.Store("rates"); s == nil || s.Config().TTLMS != 3600000 {
		t.Fatalf("declared store: %+v", s)
	}
	if _, err := steps.ProcessorWithParams(node("store:lib:codes"), stepdef.Params{ChannelRuntimeKey: rt}); err != nil || rt.Store("codes") == nil {
		t.Fatalf("imported store: %v", err)
	}
	if _, err := steps.ProcessorWithParams(node("store:other"), stepdef.Params{ChannelRuntimeKey: rt}); err == nil || !strings.Contains(err.Error(), `store "other" not found in dil.core.stores`) {
		t.Errorf("undeclared: %v", err)
	}
	stores["durable"] = stepdef.Store{Name: "durable", Durable: true}
	if _, err := steps.ProcessorWithParams(node("store:durable"), stepdef.Params{ChannelRuntimeKey: rt}); err == nil || !strings.Contains(err.Error(), "channels.directory") {
		t.Errorf("durable without a directory: %v", err)
	}
}

func TestStoreReference(t *testing.T) {
	s := testStore(t)
	s.Put("EUR", 1.1)
	s.Put("a:b", "colon")
	name := t.Name()
	m := message.New(nil)
	m["cur"] = "EUR"

	for expr, want := range map[string]any{
		"${store:" + name + ":EUR}":                  1.1,
		"${store:" + name + ":${header.cur}}":        1.1,
		"rate ${store:" + name + ":EUR}":             "rate 1.1",
		"${store:" + name + ":USD}":                  nil,
		"${store:" + name + ":USD} ?: none":          "none",
		"${store:" + name + ":a:b}":                  "colon",
		"${string:store:" + name + ":${header.cur}}": "1.1",
	} {
		x, err := compileExpression("simple", expr)
		if err != nil {
			t.Fatalf("%s: %v", expr, err)
		}
		if got, err := x.value(m); err != nil || got != want {
			t.Errorf("%s = %#v, %v; want %#v", expr, got, err, want)
		}
	}

	// Without a loader, a store that is not declared fails when the expression runs.
	x, err := compileExpression("simple", "${store:nowhere:k}")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := x.eval(m); err == nil || !strings.Contains(err.Error(), `store "nowhere" is not declared`) {
		t.Errorf("undeclared at run: %v", err)
	}
	for _, bad := range []string{"${store:" + name + "}", "${store::k}", "${store:" + name + ":}", "${store:${header.cur}:k}"} {
		if _, err := compileExpression("simple", bad); err == nil {
			t.Errorf("%s compiled", bad)
		}
	}

	// A loader's flow knows its stores: a name it does not declare fails when
	// compiled, and an imported store is <namespace>:<name>.
	rt, err := NewChannels(channels.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	flow := &flowProperties{stores: map[string]stepdef.Store{"lib:rates": {Name: "rates"}}, channels: rt}
	if _, err := compileExpressionIn(flow, "simple", "${store:nowhere:k}"); err == nil || !strings.Contains(err.Error(), `store "nowhere" not found`) {
		t.Errorf("undeclared at compile: %v", err)
	}
	x, err = compileExpressionIn(flow, "simple", "${store:lib:rates:EUR}")
	if err != nil {
		t.Fatal(err)
	}
	rt.Store("rates").Put("EUR", "imported")
	if got, err := x.eval(m); err != nil || got != "imported" {
		t.Errorf("imported store: %q, %v", got, err)
	}

	// The static checks see the headers the key reads.
	if reads := headerReads("${store:"+name+":${header.cur}}", false); !reflect.DeepEqual(reads, [][]string{{"cur"}}) {
		t.Errorf("reads = %v", reads)
	}
}

func TestStoreEffect(t *testing.T) {
	for _, tc := range []struct {
		opts map[string]any
		want stepdef.Effect
	}{
		{map[string]any{"operation": "put", "key": "${header.id}", "value": "${header.v}"}, stepdef.Effect{KeepBody: true, Headers: stepdef.HeadersKeep, Reads: [][]string{{"id"}, {"v"}}}},
		{map[string]any{"operation": "remove", "key": "k"}, keepAll},
		{map[string]any{"operation": "get", "key": "k", "headerName": "h"}, stepdef.Effect{KeepBody: true, Headers: stepdef.HeadersKeep, MaySet: []string{"h"}}},
		{map[string]any{"operation": "get", "key": "k", "headerName": "h", "default": "x"}, stepdef.Effect{KeepBody: true, Headers: stepdef.HeadersKeep, Sets: []string{"h"}}},
		{map[string]any{"operation": "get", "key": "k"}, stepdef.Effect{Headers: stepdef.HeadersKeep}},
	} {
		if got := storeEffect(tc.opts); got == nil || !reflect.DeepEqual(*got, tc.want) {
			t.Errorf("%v: effect = %+v, want %+v", tc.opts, got, tc.want)
		}
	}
}

// TestTenantVariablesInDurableStore keeps the variables of a tenant in a
// durable store that a program declares: they survive a new runtime.
func TestTenantVariablesInDurableStore(t *testing.T) {
	config := channels.Config{Directory: t.TempDir()}
	stores := map[string]stepdef.Store{"tenant.acme": {Name: "tenant.acme", Durable: true}}
	run := func(uri string, opts map[string]any) message.Message {
		t.Helper()
		rt, err := NewChannels(config)
		if err != nil {
			t.Fatal(err)
		}
		defer rt.Close()
		opts["tenantDbName"] = "acme"
		p, err := steps.ProcessorWithParams(&flowdef.Node{ID: "v", Kind: stepdef.Action, URI: uri, Options: opts, Bindings: stepdef.Params{stepdef.StoresBinding: stores}},
			stepdef.Params{ChannelRuntimeKey: rt})
		if err != nil {
			t.Fatal(err)
		}
		if !rt.Store("tenant.acme").Config().Durable {
			t.Fatal("the tenant's store is not the declared one")
		}
		out, err := p.(stepdef.ActionProcessor).Process(context.Background(), message.New(nil))
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	run("settenantvariable:token", map[string]any{"value": "secret-token"})
	if out := run("gettenantvariable:token", map[string]any{"headerName": "t"}); out["t"] != "secret-token" {
		t.Errorf("after a restart: %v", out["t"])
	}
	run("removetenantvariable:token", map[string]any{})
	if out := run("gettenantvariable:token", map[string]any{"headerName": "t"}); out["t"] != "" {
		t.Errorf("removed: %v", out["t"])
	}
}
