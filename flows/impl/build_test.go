package impl

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	flowdef "dif/flows/definition"
	"dif/message"
	stepdef "dif/steps/definition"
)

type noop struct{}

func (noop) Process(_ context.Context, m message.Message) (message.Message, error) { return m, nil }

func newNoop(*flowdef.Node) (stepdef.Processor, error) { return noop{}, nil }

// path returns "kind:id" for each node from source to sink.
func path(f *flowdef.Flow) []string {
	var p []string
	for n := f.Source; n != nil; {
		p = append(p, n.Kind+":"+n.ID)
		if n.Processor == nil {
			p = append(p, "<nil processor>")
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

func TestParseRouter(t *testing.T) {
	data, err := os.ReadFile("../../examples/contentrouter.json")
	if err != nil {
		t.Fatal(err)
	}
	f, err := Parse(data, newNoop)
	if err != nil {
		t.Fatal(err)
	}
	r := f.Source.Next[0]
	if r.Kind != flowdef.Router || r.URI != "content" || len(r.Next) != 3 || len(r.Links) != 3 {
		t.Fatalf("router = %+v, want content with 3 outbound links", r)
	}
	want := []stepdef.Link{
		{},
		{Rule: "234", Language: "jsonpath", Expression: "$.store.book[*].author"},
		{Rule: "123", Language: "simple", Expression: "${bodyAs(String)} == '123'"},
	}
	for i, l := range r.Links {
		if l != want[i] {
			t.Errorf("link %d = %+v, want %+v", i, l, want[i])
		}
		if r.Next[i].Kind != flowdef.Sink || r.Next[i].Processor == nil {
			t.Errorf("link %d leads to %+v, want a sink", i, r.Next[i])
		}
	}
}

func TestParseErrorHandler(t *testing.T) {
	tests := []struct {
		file         string
		redeliveries int
		delay        time.Duration
		route        string // kind:id of the error route's first step, "" for none
	}{
		{"errorHandler.json", 0, 0, "action:de8503cb-b66a-4503-88fe-fb9edeaec662"},
		{"deadletter.json", 3, 10 * time.Second, "sink:68513f81-1b54-4b2b-ac98-50b65c979014"},
		{"log.json", 0, 0, ""},
		{"hello.json", 0, time.Second, ""}, // no options: the defaults
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
			h := f.Error
			if h == nil || h.Redeliveries != tt.redeliveries || h.RedeliveryDelay != tt.delay {
				t.Fatalf("error handler = %+v, want %d redeliveries every %v", h, tt.redeliveries, tt.delay)
			}
			route := ""
			if h.Route != nil {
				route = h.Route.Kind + ":" + h.Route.ID
				if h.Route.Processor == nil {
					t.Errorf("error route %+v has no processor", h.Route)
				}
			}
			if route != tt.route {
				t.Errorf("error route = %q, want %q", route, tt.route)
			}
		})
	}
}

func TestParseErrorHandlerInvalid(t *testing.T) {
	const errStep = `{"id":"e","type":"error","uri":"failedexchange"`
	tests := []struct {
		name, json, want string
	}{
		{"two error steps", flow(src + `,` + sink + `,` + errStep + `},` + errStep + `}`), "step e: flow has more than one error step"},
		{"unknown uri", flow(src + `,` + sink + `,{"id":"e","type":"error","uri":"deadletter"}`), `step e: error step uri "deadletter" is not supported`},
		{"unknown option", flow(src + `,` + sink + `,` + errStep + `,"options":{"retries":1}}`), "step e: error step: unknown option retries"},
		{"bad option", flow(src + `,` + sink + `,` + errStep + `,"options":{"maximumRedeliveries":"x"}}`), `option maximumRedeliveries: want an integer, got "x"`},
		{"negative option", flow(src + `,` + sink + `,` + errStep + `,"options":{"redeliveryDelay":-1}}`), "option redeliveryDelay: want an integer of 0 or more"},
		{"inbound link", flow(src + `,` + sink + `,` + errStep + `,"links":{"link":{"id":"x","bound":"in"}}}`), "error step needs 0 inbound links"},
		{"dangling route", flow(src + `,` + sink + `,` + errStep + `,"links":{"link":{"id":"x","bound":"out"}}}`), "step e: outbound link x has no target"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(tt.json), newNoop)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want containing %q", err, tt.want)
			}
		})
	}

	// Aliases: redeliveryAttempts and redeliveryInterval count when the Camel names are absent.
	f, err := Parse([]byte(flow(src+`,`+sink+`,`+errStep+`,"options":{"redeliveryAttempts":"2","redeliveryInterval":5}}`)), newNoop)
	if err != nil || f.Error.Redeliveries != 2 || f.Error.RedeliveryDelay != 5*time.Millisecond {
		t.Errorf("error handler = %+v, %v; want 2 redeliveries every 5ms", f.Error, err)
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
	if got := f.Input["MyHeader"]; got != "SomeValue" {
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
		{"router without out link", flow(src + `,{"id":"b","type":"router","links":{"link":{"id":"b","bound":"in"}}}`), "router step needs 1 inbound and 1 outbound links, has 1 and 0"},
		{"action with two out links", flow(src + `,{"id":"b","type":"action","links":{"link":[{"id":"b","bound":"in"},{"id":"c","bound":"out"},{"id":"d","bound":"out"}]}}`), "action step needs 1 inbound and 1 outbound links, has 1 and 2"},
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

func TestParseMessageReference(t *testing.T) {
	data, err := os.ReadFile("../../examples/log.json")
	if err != nil {
		t.Fatal(err)
	}
	var headers any
	_, err = Parse(data, func(n *flowdef.Node) (stepdef.Processor, error) {
		if strings.HasPrefix(n.URI, "setheaders:") {
			headers = n.Options["headers"]
		}
		return noop{}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := `[{"language":"simple","name":"MyHeader","value":"SomeValue"}]`; headers != want {
		t.Errorf("headers option = %v, want %s", headers, want)
	}

	bad := flow(`{"id":"a","type":"source","uri":"timer","links":{"link":{"id":"b","bound":"out"}}},` +
		`{"id":"b","type":"sink","uri":"setheaders:message:nope","links":{"link":{"id":"b","bound":"in"}}}`)
	if _, err := Parse([]byte(bad), newNoop); err == nil || !strings.Contains(err.Error(), `step b: message "nope" not found`) {
		t.Errorf("unknown message: err = %v", err)
	}
}
