package impl

import (
	"os"
	"strings"
	"testing"

	flowdef "dif/flows/definition"
	"dif/message"
	stepdef "dif/steps/definition"
)

type noop struct{}

func (noop) Execute(m *message.Message) (*message.Message, error) { return m, nil }

func newNoop(*flowdef.Node) (stepdef.Step, error) { return noop{}, nil }

// path returns "kind:id" for each node from source to sink.
func path(f *flowdef.Flow) []string {
	var p []string
	for n := f.Source; n != nil; {
		p = append(p, n.Kind+":"+n.ID)
		if n.Step == nil {
			p = append(p, "<nil step>")
		}
		if len(n.Next) == 0 {
			break
		}
		n = n.Next[0]
	}
	return p
}

func TestParseExamples(t *testing.T) {
	tests := []struct {
		file string
		want []string
	}{
		{"test.json", []string{
			"source:1e6a09fd-a42a-4c33-9794-32536f9711e5",
			"sink:daead6ba-244e-4dc1-8dc3-c288ade5387c",
		}},
		{"log.json", []string{
			"source:492db687-512a-4c88-a259-bcc0c50e7cf0",
			"action:60978f6d-106e-46e8-9ec4-51891b9843c0",
			"action:a26dee44-17cb-4d33-aebb-dddc55dbc361",
			"sink:9f42c79a-3e70-4c9a-805f-60f5a538b15f",
		}},
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			data, err := os.ReadFile("../../examples/" + tt.file)
			if err != nil {
				t.Fatal(err)
			}
			f, err := Parse(data, newNoop)
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Join(path(f), " "); got != strings.Join(tt.want, " ") {
				t.Errorf("path = %s\nwant   %s", got, strings.Join(tt.want, " "))
			}
		})
	}
}

func TestParseInputMessage(t *testing.T) {
	data, err := os.ReadFile("../../examples/log.json")
	if err != nil {
		t.Fatal(err)
	}
	f, err := Parse(data, newNoop)
	if err != nil {
		t.Fatal(err)
	}
	if got := f.Input.Headers["MyHeader"]; got != "SomeValue" {
		t.Errorf("header MyHeader = %q, want SomeValue", got)
	}
}

// flow wraps steps JSON in a minimal DIL document.
func flow(steps string) string {
	return `{"dil":{"integrations":{"integration":{"flows":{"flow":{"id":"f","steps":{"step":[` + steps + `]}}}}}}}`
}

const (
	src  = `{"id":"a","type":"source","links":{"link":{"id":"b","bound":"out"}}}`
	sink = `{"id":"b","type":"sink","links":{"link":{"id":"b","bound":"in"}}}`
)

func TestParseErrors(t *testing.T) {
	tests := []struct {
		name, json, want string
	}{
		{"invalid json", `{`, "parse dil"},
		{"no flow", `{"dil":{}}`, "found 0"},
		{"two flows", `{"dil":{"integrations":{"integration":{"flows":{"flow":[{"id":"x"},{"id":"y"}]}}}}}`, "found 2"},
		{"no source", flow(sink), "no source"},
		{"dangling link", flow(src), "has no target"},
		{"router", flow(src + `,{"id":"b","type":"router"}`), "not supported"},
		{"unknown type", flow(src + `,{"id":"b","type":"bogus"}`), "unknown step type"},
		{"sink with out link", flow(src + `,{"id":"b","type":"sink","links":{"link":[{"id":"b","bound":"in"},{"id":"c","bound":"out"}]}}`), "needs 1 inbound and 0 outbound"},
		{"unreachable", flow(src + `,` + sink + `,{"id":"c","type":"sink","links":{"link":{"id":"c","bound":"in"}}}`), "not reachable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(tt.json), newNoop)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want containing %q", err, tt.want)
			}
		})
	}
}
