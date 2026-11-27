package impl

import (
	"context"
	"errors"
	"strings"
	"testing"

	flowdef "dif/flows/definition"
	"dif/message"
)

func flowWithOptions(options string) []byte {
	return []byte(`{"dil":{"integrations":{"integration":{"id":"i","flows":{"flow":{"id":"f","options":{` + options + `},"steps":{"step":[` +
		`{"id":"s","type":"source","uri":"message:in","links":{"link":{"id":"l1","bound":"out"}}},` +
		`{"id":"k","type":"sink","uri":"passthrough","links":{"link":{"id":"l1","bound":"in"}}}` +
		`]}}}}}}}`)
}

func TestParseFlowConcurrencyOptions(t *testing.T) {
	valid := []struct {
		name string
		opts string
		want int
	}{
		{"default omitted", "", 0},
		{"int 1", `"concurrency":1`, 1},
		{"int 8", `"concurrency":8`, 8},
		{"string 8", `"concurrency":"8"`, 8},
		{"max 1024", `"concurrency":1024`, 1024},
	}
	for _, tc := range valid {
		t.Run(tc.name, func(t *testing.T) {
			f, err := Parse(flowWithOptions(tc.opts), newNoop)
			if err != nil {
				t.Fatalf("Parse(%s): %v", tc.opts, err)
			}
			if f.Concurrency != tc.want {
				t.Errorf("Concurrency = %d, want %d", f.Concurrency, tc.want)
			}
		})
	}

	invalid := []string{
		`"concurrency":0`,
		`"concurrency":-2`,
		`"concurrency":1025`,
		`"concurrency":2.5`,
		`"concurrency":"0"`,
		`"concurrency":"1025"`,
		`"concurrency":"fast"`,
		`"concurrency":true`,
	}
	for _, opts := range invalid {
		t.Run("invalid_"+opts, func(t *testing.T) {
			if _, err := Parse(flowWithOptions(opts), newNoop); err == nil || !strings.Contains(err.Error(), "options.concurrency") {
				t.Errorf("Parse(%s): err = %v, want options.concurrency error", opts, err)
			}
		})
	}
}

func TestParseFlowPartitionBy(t *testing.T) {
	prev := getPartitionKeyCompiler()
	t.Cleanup(func() { SetPartitionKeyCompiler(prev) })

	SetPartitionKeyCompiler(func(_ *flowdef.Flow, expr string) (func(context.Context, message.Message) (string, error), error) {
		if expr == "${broken" {
			return nil, errors.New("unclosed ${")
		}
		return func(_ context.Context, m message.Message) (string, error) {
			s, _ := m["customerId"].(string)
			return s, nil
		}, nil
	})

	f, err := Parse(flowWithOptions(`"concurrency":4,"partitionBy":"${header.customerId}"`), newNoop)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if f.Concurrency != 4 || f.PartitionExpr != "${header.customerId}" || f.PartitionKey == nil {
		t.Fatalf("got concurrency=%d expr=%q key=%v", f.Concurrency, f.PartitionExpr, f.PartitionKey != nil)
	}
	m := message.New("x")
	m["customerId"] = "c-42"
	key, err := f.PartitionKey(context.Background(), m)
	if err != nil || key != "c-42" {
		t.Fatalf("PartitionKey = %q, %v; want c-42", key, err)
	}

	if _, err := Parse(flowWithOptions(`"partitionBy":"   "`), newNoop); err == nil || !strings.Contains(err.Error(), "options.partitionBy") {
		t.Errorf("whitespace partitionBy: err = %v", err)
	}
	if _, err := Parse(flowWithOptions(`"partitionBy":"${broken"`), newNoop); err == nil || !strings.Contains(err.Error(), "options.partitionBy") {
		t.Errorf("broken partitionBy: err = %v", err)
	}
}
