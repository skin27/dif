package dilsyntax

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestFlowConcurrencyOptionsRoundTrip(t *testing.T) {
	src := `flow orders(concurrency: 8, partitionBy: "${header.customerId}") {
  message:in -> passthrough
}
`
	f, err := Parse("orders.dil", []byte(src))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	j, err := Lower("orders.dil", f)
	if err != nil {
		t.Fatalf("Lower: %v", err)
	}

	var doc struct {
		DIL struct {
			Integrations struct {
				Integration []struct {
					Flows struct {
						Flow []struct {
							ID      string `json:"id"`
							Options struct {
								Concurrency int    `json:"concurrency"`
								PartitionBy string `json:"partitionBy"`
							} `json:"options"`
						} `json:"flow"`
					} `json:"flows"`
				} `json:"integration"`
			} `json:"integrations"`
		} `json:"dil"`
	}
	if err := json.Unmarshal(j, &doc); err != nil {
		t.Fatalf("Unmarshal lowered JSON: %v", err)
	}
	fl := doc.DIL.Integrations.Integration[0].Flows.Flow[0]
	if fl.ID != "orders" || fl.Options.Concurrency != 8 || fl.Options.PartitionBy != "${header.customerId}" {
		t.Fatalf("lowered flow = %+v", fl)
	}

	printed, err := Print(j)
	if err != nil {
		t.Fatalf("Print: %v", err)
	}
	if !strings.Contains(string(printed), `concurrency: 8`) || !strings.Contains(string(printed), `partitionBy: "${header.customerId}"`) {
		t.Fatalf("printed DIL missing concurrency/partitionBy:\n%s", printed)
	}
	f2, err := Parse("printed.dil", printed)
	if err != nil {
		t.Fatalf("Parse printed: %v", err)
	}
	j2, err := Lower("printed.dil", f2)
	if err != nil {
		t.Fatalf("Lower printed: %v", err)
	}
	if string(j) != string(j2) {
		t.Fatalf("round-trip JSON mismatch:\nwant: %s\ngot:  %s", j, j2)
	}
}
