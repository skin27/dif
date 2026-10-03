package registry

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	flowdef "dif/flows/definition"
	"dif/message"
	stepdef "dif/steps/definition"
)

// recorder is an action processor that remembers the params it was created with.
type recorder struct {
	id     string
	params stepdef.Params
}

func (recorder) Process(_ context.Context, m message.Message) (message.Message, error) { return m, nil }

type sink struct{}

func (sink) Consume(context.Context, message.Message) error { return nil }

const pathSchema = `{"type": "object", "properties": {"path": {"type": "string"}, "n": {"type": "integer", "default": 1}}, "additionalProperties": false}`

func newRecorder(id string, p stepdef.Params) (stepdef.Processor, error) { return recorder{id, p}, nil }

func testRegistry(t *testing.T) *Registry {
	t.Helper()
	r := New()
	for _, d := range []stepdef.Definition{
		{Name: "rec", Kind: stepdef.Action, Schema: []byte(pathSchema), New: newRecorder},
		{Name: "out", Kind: stepdef.Sink, Schema: []byte(`{"type": "object"}`),
			New: func(string, stepdef.Params) (stepdef.Processor, error) { return sink{}, nil }},
		{Name: "liar", Kind: stepdef.Source, Schema: []byte(`{"type": "object"}`), New: newRecorder},
		{Name: "failing", Kind: stepdef.Action, Schema: []byte(`{"type": "object"}`),
			New: func(string, stepdef.Params) (stepdef.Processor, error) { return nil, errors.New("boom") }},
	} {
		if err := r.Register(d); err != nil {
			t.Fatal(err)
		}
	}
	return r
}

func TestProcessor(t *testing.T) {
	r := testRegistry(t)

	p, err := r.Processor(&flowdef.Node{ID: "s1", Kind: flowdef.Action, URI: "rec:some/where", Options: map[string]any{"n": "5"}})
	if err != nil {
		t.Fatal(err)
	}
	want := recorder{"s1", stepdef.Params{"path": "some/where", "n": 5}}
	if !reflect.DeepEqual(p, want) {
		t.Errorf("processor = %+v, want %+v", p, want)
	}

	// An explicit path option wins over the URI.
	p, err = r.Processor(&flowdef.Node{ID: "s2", Kind: flowdef.Action, URI: "rec:uri", Options: map[string]any{"path": "opt"}})
	if err != nil || p.(recorder).params["path"] != "opt" {
		t.Errorf("processor = %+v, %v; want path opt", p, err)
	}
}

func TestProcessorKindFallback(t *testing.T) {
	r := testRegistry(t)
	if p, err := r.Processor(&flowdef.Node{Kind: flowdef.Sink, URI: "rec"}); err != nil {
		t.Errorf("action in a sink position: %v", err)
	} else if _, ok := p.(stepdef.ActionProcessor); !ok {
		t.Errorf("processor = %T, want an action processor", p)
	}
	if p, err := r.Processor(&flowdef.Node{Kind: flowdef.Action, URI: "out:x"}); err != nil {
		t.Errorf("sink in an action position: %v", err)
	} else if _, ok := p.(stepdef.SinkProcessor); !ok {
		t.Errorf("processor = %T, want a sink processor", p)
	}
}

// routerRecorder is a router processor that remembers its params.
type routerRecorder struct{ params stepdef.Params }

func (routerRecorder) Route(context.Context, message.Message) ([]stepdef.Route, error) {
	return nil, nil
}

func TestProcessorRouter(t *testing.T) {
	r := New()
	err := r.Register(stepdef.Definition{Name: "pick", Kind: stepdef.Router, Schema: []byte(`{"type": "object"}`),
		New: func(_ string, p stepdef.Params) (stepdef.Processor, error) { return routerRecorder{p}, nil }})
	if err != nil {
		t.Fatal(err)
	}

	links := []stepdef.Link{{Rule: "a", Language: "simple", Expression: "${body} == 'x'"}, {}}
	p, err := r.Processor(&flowdef.Node{Kind: flowdef.Router, URI: "pick", Next: make([]*flowdef.Node, 2), Links: links})
	if err != nil {
		t.Fatal(err)
	}
	if got := p.(routerRecorder).params[stepdef.Links]; !reflect.DeepEqual(got, links) {
		t.Errorf("links = %v, want %v", got, links)
	}

	// A flow built without link attributes gives the router empty ones; a
	// router in an action position (such as a filter) gets its one link.
	p, err = r.Processor(&flowdef.Node{Kind: flowdef.Action, URI: "pick", Next: make([]*flowdef.Node, 1)})
	if err != nil {
		t.Fatalf("router in an action position: %v", err)
	}
	if got := p.(routerRecorder).params[stepdef.Links]; !reflect.DeepEqual(got, []stepdef.Link{{}}) {
		t.Errorf("links = %v, want one empty link", got)
	}

	if _, err := r.Processor(&flowdef.Node{Kind: flowdef.Sink, URI: "pick"}); err == nil {
		t.Error("router in a sink position: want an error")
	}
	if _, err := r.Processor(&flowdef.Node{Kind: flowdef.Router, URI: "pick", Options: map[string]any{"links": "x"}}); err != nil {
		t.Errorf("an option named links: %v", err) // the router still gets the node's links
	}
}

func TestProcessorErrors(t *testing.T) {
	r := testRegistry(t)
	tests := []struct {
		name string
		node flowdef.Node
		want string
	}{
		{"unknown step", flowdef.Node{Kind: flowdef.Source, URI: "https://0.0.0.0:9001/x"}, `no processor for "https" (source)`},
		{"wrong kind", flowdef.Node{Kind: flowdef.Source, URI: "rec"}, `no processor for "rec" (source)`},
		{"invalid option", flowdef.Node{Kind: flowdef.Action, URI: "rec", Options: map[string]any{"n": "x"}}, `rec: option n: want integer, got "x"`},
		{"unknown option", flowdef.Node{Kind: flowdef.Action, URI: "rec", Options: map[string]any{"bogus": true}}, "rec: unknown option bogus"},
		{"factory error", flowdef.Node{Kind: flowdef.Action, URI: "failing"}, "failing: boom"},
		{"wrong interface", flowdef.Node{Kind: flowdef.Source, URI: "liar"}, "is not a source processor"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := r.Processor(&tt.node)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestRegisterErrors(t *testing.T) {
	r := testRegistry(t)
	obj := []byte(`{"type": "object"}`)
	tests := []struct {
		name string
		def  stepdef.Definition
		want string
	}{
		{"duplicate", stepdef.Definition{Name: "rec", Kind: stepdef.Action, Schema: obj, New: newRecorder}, "already registered"},
		{"empty name", stepdef.Definition{Kind: stepdef.Action, Schema: obj, New: newRecorder}, "invalid name"},
		{"name with colon", stepdef.Definition{Name: "a:b", Kind: stepdef.Action, Schema: obj, New: newRecorder}, "invalid name"},
		{"bad kind", stepdef.Definition{Name: "x", Kind: "error", Schema: obj, New: newRecorder}, "invalid kind"},
		{"no factory", stepdef.Definition{Name: "x", Kind: stepdef.Action, Schema: obj}, "New is nil"},
		{"bad schema", stepdef.Definition{Name: "x", Kind: stepdef.Action, Schema: []byte(`{"type": "object", "allOf": []}`), New: newRecorder}, "unsupported keyword"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := r.Register(tt.def); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want containing %q", err, tt.want)
			}
		})
	}

	// The same name with another kind is a different step.
	if err := r.Register(stepdef.Definition{Name: "rec", Kind: stepdef.Sink, Schema: obj, New: newRecorder}); err != nil {
		t.Errorf("same name, other kind: %v", err)
	}
}

func TestSteps(t *testing.T) {
	r := New()
	for _, d := range []stepdef.Definition{
		{Name: "rec", Kind: stepdef.Action, Pattern: "Wire Tap", New: newRecorder, Schema: []byte(`{"type": "object", "description": "Records.",
			"properties": {"path": {"type": "string", "description": "Where"}, "n": {"type": "integer", "default": 1}}, "required": ["path"]}`)},
		{Name: "out", Kind: stepdef.Sink, Schema: []byte(`{"type": "object"}`), New: newRecorder},
		{Name: "out", Kind: stepdef.Action, Schema: []byte(`{"type": "object"}`), New: newRecorder},
	} {
		if err := r.Register(d); err != nil {
			t.Fatal(err)
		}
	}
	want := []StepInfo{
		{Name: "out", Kind: stepdef.Action},
		{Name: "out", Kind: stepdef.Sink},
		{Name: "rec", Kind: stepdef.Action, Pattern: "Wire Tap", Description: "Records.", Options: []OptionInfo{
			{Name: "n", Type: "integer", Default: 1},
			{Name: "path", Type: "string", Description: "Where", Required: true},
		}},
	}
	if got := r.Steps(); !reflect.DeepEqual(got, want) {
		t.Errorf("Steps() = %+v\nwant %+v", got, want)
	}
}
