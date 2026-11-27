package flowcheck

import (
	"strings"
	"testing"

	flowdef "dif/flows/definition"
	"dif/message"
	stepdef "dif/steps/definition"
)

func TestConcurrencyHazard(t *testing.T) {
	makeFlow := func(conc int, partitionExpr string, corrKey string) *flowdef.Flow {
		src := &flowdef.Node{ID: "in", Kind: flowdef.Source, URI: "timer:t"}
		agg := &flowdef.Node{ID: "agg", Kind: flowdef.Router, URI: "aggregate", Options: map[string]any{}}
		if corrKey != "" {
			agg.Options["correlationKey"] = corrKey
		}
		out := &flowdef.Node{ID: "out", Kind: flowdef.Sink, URI: "log"}
		src.Next = []*flowdef.Node{agg}
		agg.Next = []*flowdef.Node{out}
		return &flowdef.Flow{
			ID:            "orders",
			Source:        src,
			Concurrency:   conc,
			PartitionExpr: partitionExpr,
		}
	}
	eff := func(n *flowdef.Node) *Effect {
		if n.Kind == flowdef.Source {
			return closedSource
		}
		return keep
	}

	// Concurrent unpartitioned + unkeyed aggregate -> concurrency-hazard warning.
	ds := Check(makeFlow(4, "", ""), eff)
	if got := show(ds); got != "warning concurrency-hazard agg" {
		t.Errorf("unkeyed concurrent aggregate: got %q, want %q", got, "warning concurrency-hazard agg")
	}

	// Safe cases: sequential, partitioned, or aggregate with correlationKey.
	for _, tc := range []struct {
		name string
		f    *flowdef.Flow
	}{
		{"sequential", makeFlow(1, "", "")},
		{"partitioned", makeFlow(4, "${header.customerId}", "")},
		{"correlationKey", makeFlow(4, "", "${header.orderId}")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, d := range Check(tc.f, eff) {
				if d.Code == CodeConcurrencyHazard {
					t.Errorf("unexpected concurrency-hazard diagnostic: %v", d)
				}
			}
		})
	}
}

func TestPartitionByHeaderCheck(t *testing.T) {
	src := &flowdef.Node{ID: "in", Kind: flowdef.Source, URI: "timer:t"}
	out := &flowdef.Node{ID: "out", Kind: flowdef.Sink, URI: "log"}
	src.Next = []*flowdef.Node{out}

	input := message.New("hi")
	input["customerId"] = "c1"

	f := &flowdef.Flow{
		ID:            "orders",
		Source:        src,
		Input:         input,
		Concurrency:   4,
		PartitionExpr: "${header.custmerId}", // typo for customerId
	}
	ds := Check(f, func(n *flowdef.Node) *Effect {
		if n.Kind == flowdef.Source {
			return &Effect{Headers: stepdef.HeadersKeep}
		}
		return keep
	})
	if len(ds) != 1 {
		t.Fatalf("diagnostics = %v, want 1", ds)
	}
	if ds[0].Severity != Warning || ds[0].Code != CodeHeaderNotSet || ds[0].Step != "" {
		t.Errorf("diag = %+v, want warning header-not-set on flow", ds[0])
	}
	if !strings.Contains(ds[0].Message, `did you mean "customerId"`) {
		t.Errorf("message = %q, want suggestion for customerId", ds[0].Message)
	}

	// Exact header name or engine-provided header -> no diagnostic.
	for _, expr := range []string{"${header.customerId}", "${header.Correlation-Id}"} {
		f.PartitionExpr = expr
		if ds := Check(f, func(n *flowdef.Node) *Effect {
			if n.Kind == flowdef.Source {
				return &Effect{Headers: stepdef.HeadersKeep}
			}
			return keep
		}); len(ds) != 0 {
			t.Errorf("expr %q: unexpected diagnostics %v", expr, ds)
		}
	}
}
