package impl

import (
	"bytes"
	"context"
	"io/fs"
	"log"
	"strings"
	"testing"

	flowdef "dif/flows/definition"
	"dif/message"
	stepdef "dif/steps/definition"
	"dif/steps/registry"
)

// steps holds the built-in steps. Tests create processors through it, so
// every test also exercises the step's schema.
var steps = func() *registry.Registry {
	r := registry.New()
	if err := Register(r); err != nil {
		panic(err)
	}
	return r
}()

func newProcessor(kind, uri string, opts map[string]any) (stepdef.Processor, error) {
	return steps.Processor(&flowdef.Node{ID: "test-step", Kind: kind, URI: uri, Options: opts})
}

func mustProcessor(t *testing.T, kind, uri string, opts map[string]any) stepdef.Processor {
	t.Helper()
	p, err := newProcessor(kind, uri, opts)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// wantInvalid checks that the options are rejected with an error containing want.
func wantInvalid(t *testing.T, kind, uri string, opts map[string]any, want string) {
	t.Helper()
	_, err := newProcessor(kind, uri, opts)
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("%s %v: err = %v, want containing %q", uri, opts, err, want)
	}
}

// process runs m through the action step uri with opts.
func process(t *testing.T, uri string, opts map[string]any, m message.Message) message.Message {
	t.Helper()
	p := mustProcessor(t, stepdef.Action, uri, opts).(stepdef.ActionProcessor)
	out, err := p.Process(context.Background(), m)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// newRouter creates the router step uri, in a position of kind, with opts
// and the given outbound links.
func newRouter(kind, uri string, opts map[string]any, links ...stepdef.Link) (stepdef.RouterProcessor, error) {
	p, err := steps.Processor(&flowdef.Node{ID: "test-step", Kind: kind, URI: uri, Options: opts, Links: links, Next: make([]*flowdef.Node, len(links))})
	if err != nil {
		return nil, err
	}
	return p.(stepdef.RouterProcessor), nil
}

// route runs m through the router step uri and returns its routes.
func route(t *testing.T, uri string, opts map[string]any, links []stepdef.Link, m message.Message) []stepdef.Route {
	t.Helper()
	r, err := newRouter(stepdef.Router, uri, opts, links...)
	if err != nil {
		t.Fatal(err)
	}
	routes, err := r.Route(context.Background(), m)
	if err != nil {
		t.Fatal(err)
	}
	return routes
}

// captureLog sends the standard logger's output to the returned buffer for
// the rest of the test.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	out, flags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(out)
		log.SetFlags(flags)
	})
	return &buf
}

// TestEverySchemaIsRegistered guards against a schema file without a step.
func TestEverySchemaIsRegistered(t *testing.T) {
	files, err := fs.Glob(schemas, "schemas/*.json")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		name, kind, _ := strings.Cut(strings.TrimSuffix(strings.TrimPrefix(f, "schemas/"), ".json"), "-")
		if _, err := newProcessor(kind, name+":x", map[string]any{"path": "x", "name": "x"}); err != nil &&
			strings.Contains(err.Error(), "no processor") {
			t.Errorf("%s: %v", f, err)
		}
	}
	if len(files) != 26 {
		t.Errorf("found %d schemas, want 26", len(files))
	}
}
