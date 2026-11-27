package api

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"dif/internal/flowcheck"
	"dif/message"
	stepdef "dif/steps/definition"
)

func TestLoadAndInspectFlowConcurrencyAndPartitionBy(t *testing.T) {
	// Register a tiny custom action step that records execution order per customerId.
	var mu sync.Mutex
	seen := map[string][]string{}
	stepName := fmt.Sprintf("partrec%d", time.Now().UnixNano())
	if err := RegisterStep(StepDefinition{
		Name:   stepName,
		Kind:   stepdef.Action,
		Schema: []byte(`{"type":"object","properties":{},"additionalProperties":false}`),
		New: func(string, stepdef.Params) (stepdef.Processor, error) {
			return actionFn(func(_ context.Context, m message.Message) (message.Message, error) {
				cust, _ := m["customerId"].(string)
				body, _ := m[message.Body].(string)
				mu.Lock()
				seen[cust] = append(seen[cust], body)
				mu.Unlock()
				return m, nil
			}), nil
		},
	}); err != nil {
		t.Fatal(err)
	}

	dilText := fmt.Sprintf(`flow orders(concurrency: 4, partitionBy: "${header.customerId}") {
  message:in -> %s -> passthrough
}
`, stepName)

	// InspectBytes: Concurrency and Partitioned populated, expression NOT leaked.
	info, err := InspectBytes([]byte(dilText))
	if err != nil {
		t.Fatalf("InspectBytes: %v", err)
	}
	if info.Concurrency != 4 || !info.Partitioned {
		t.Fatalf("FlowInfo = {Concurrency:%d, Partitioned:%v}, want {4, true}", info.Concurrency, info.Partitioned)
	}
	rawJSON, _ := json.Marshal(info)
	if strings.Contains(string(rawJSON), "customerId") || strings.Contains(string(rawJSON), "${header") {
		t.Errorf("FlowInfo JSON leaked partitionBy expression: %s", rawJSON)
	}

	// LoadBytes and run partitioned messages: strict FIFO per customerId.
	f, err := LoadBytes([]byte(dilText), nil)
	if err != nil {
		t.Fatalf("LoadBytes: %v", err)
	}
	if err := f.Start(); err != nil {
		t.Fatal(err)
	}
	defer f.Stop()

	for i := range 10 {
		for _, cust := range []string{"alice", "bob", "carol"} {
			m := message.New(fmt.Sprintf("%d", i))
			m["customerId"] = cust
			if _, err := f.Request(context.Background(), m); err != nil {
				t.Fatalf("Request(%s, %d): %v", cust, i, err)
			}
		}
	}
	want := []string{"0", "1", "2", "3", "4", "5", "6", "7", "8", "9"}
	mu.Lock()
	defer mu.Unlock()
	for _, cust := range []string{"alice", "bob", "carol"} {
		if !slices.Equal(seen[cust], want) {
			t.Errorf("customer %s order = %v, want %v", cust, seen[cust], want)
		}
	}
}

type actionFn func(context.Context, message.Message) (message.Message, error)

func (f actionFn) Process(ctx context.Context, m message.Message) (message.Message, error) {
	return f(ctx, m)
}

func TestInspectRejectsInvalidConcurrencyAndPartitionBy(t *testing.T) {
	badConcurrency := `flow bad(concurrency: 2000) {
  message:in -> passthrough
}`
	if _, err := InspectBytes([]byte(badConcurrency)); err == nil || !strings.Contains(err.Error(), "options.concurrency") {
		t.Errorf("bad concurrency: err = %v", err)
	}

	badPartition := `flow bad(concurrency: 4, partitionBy: "${header.unclosed") {
  message:in -> passthrough
}`
	if _, err := InspectBytes([]byte(badPartition)); err == nil || !strings.Contains(err.Error(), "options.partitionBy") {
		t.Errorf("bad partitionBy: err = %v", err)
	}
}

func TestInspectDiagnosticsForConcurrencyHazardAndPartitionTypo(t *testing.T) {
	hazard := `flow aggflow(concurrency: 4) {
  timer:t -> aggregate(aggregateType: "json", completionSize: 2) -> log
}`
	info, err := InspectBytes([]byte(hazard))
	if err != nil {
		t.Fatalf("InspectBytes(hazard): %v", err)
	}
	foundHazard := false
	for _, d := range info.Diagnostics {
		if d.Code == flowcheck.CodeConcurrencyHazard && d.Severity == SeverityWarning {
			foundHazard = true
		}
	}
	if !foundHazard {
		t.Errorf("expected concurrency-hazard warning, got diagnostics: %+v", info.Diagnostics)
	}

	typo := `message sample {
  header customerId: "c1"
  body: "hi"
}
flow orders(concurrency: 4, partitionBy: "${header.custmerId}", inputMessage: "sample") {
  timer:t -> log
}`
	info2, err := InspectBytes([]byte(typo))
	if err != nil {
		t.Fatalf("InspectBytes(typo): %v", err)
	}
	foundTypo := false
	for _, d := range info2.Diagnostics {
		if d.Code == flowcheck.CodeHeaderNotSet && d.Severity == SeverityWarning && strings.Contains(d.Message, `did you mean "customerId"`) {
			foundTypo = true
		}
	}
	if !foundTypo {
		t.Errorf("expected header-not-set suggestion for partitionBy typo, got diagnostics: %+v", info2.Diagnostics)
	}
}
