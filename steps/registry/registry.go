// Package registry maps DIL step URIs to step processors. It validates a
// step's options against the processor's JSON Schema before creating it, so
// a flow with an invalid step can never be installed.
package registry

import (
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"

	flowdef "dif/flows/definition"
	stepdef "dif/steps/definition"
)

// Registry holds step definitions by name and kind. It is safe for concurrent use.
type Registry struct {
	mu   sync.RWMutex
	defs map[string]entry // name + "/" + kind
}

type entry struct {
	def    stepdef.Definition
	schema *schema
}

// New returns an empty registry.
func New() *Registry {
	return &Registry{defs: map[string]entry{}}
}

// Register adds a step definition. Its schema must compile, and its name and
// kind must not be registered yet.
func (r *Registry) Register(d stepdef.Definition) error {
	switch {
	case d.Name == "" || strings.Contains(d.Name, ":"):
		return fmt.Errorf("register step %q: invalid name", d.Name)
	case d.Kind != stepdef.Source && d.Kind != stepdef.Action && d.Kind != stepdef.Router && d.Kind != stepdef.Sink:
		return fmt.Errorf("register step %s: invalid kind %q", d.Name, d.Kind)
	case d.New == nil:
		return fmt.Errorf("register step %s (%s): New is nil", d.Name, d.Kind)
	}
	s, err := compile(d.Schema)
	if err != nil {
		return fmt.Errorf("register step %s (%s): %w", d.Name, d.Kind, err)
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	key := d.Name + "/" + d.Kind
	if _, dup := r.defs[key]; dup {
		return fmt.Errorf("register step %s (%s): already registered", d.Name, d.Kind)
	}
	r.defs[key] = entry{d, s}
	return nil
}

// Processor creates the processor for a flow node. The scheme of the node's
// URI names the step ("file" for "file:/data/in"); the rest of the URI is
// passed as the "path" option unless the options set it. The options are
// validated against the step's schema.
//
// An action node may use a sink processor (the message passes on unchanged
// after it is consumed) or a router processor (one that passes the message on
// or stops it, such as a filter), in that order of preference; a sink node may
// use an action processor.
// A router processor gets the node's outbound links as Params[stepdef.Links].
func (r *Registry) Processor(n *flowdef.Node) (stepdef.Processor, error) {
	return r.ProcessorWithParams(n, nil)
}

// ProcessorWithParams injects trusted runtime bindings after option validation.
func (r *Registry) ProcessorWithParams(n *flowdef.Node, bindings stepdef.Params) (stepdef.Processor, error) {
	e, params, err := r.parameters(n)
	if err != nil {
		return nil, err
	}
	name, _, _ := strings.Cut(n.URI, ":")
	for _, key := range e.def.RuntimeBindings {
		if value, ok := bindings[key]; ok {
			params[key] = value
		}
	}
	p, err := e.def.New(n.ID, params)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}
	if !implements(p, e.def.Kind) {
		return nil, fmt.Errorf("%s: processor %T is not a %s processor", name, p, e.def.Kind)
	}
	return p, nil
}

// Validate checks the registered step and its option schema without constructing
// a processor. It does not check expressions or external resources.
func (r *Registry) Validate(n *flowdef.Node) error {
	_, _, err := r.parameters(n)
	return err
}

func (r *Registry) parameters(n *flowdef.Node) (entry, stepdef.Params, error) {
	name, path, _ := strings.Cut(n.URI, ":")
	e, ok := r.lookup(name, n.Kind)
	if !ok {
		return entry{}, nil, fmt.Errorf("no processor for %q (%s)", name, n.Kind)
	}

	opts := make(map[string]any, len(n.Options)+1)
	maps.Copy(opts, n.Options)
	if _, set := opts["path"]; path != "" && !set {
		opts["path"] = path
	}
	params, err := e.schema.validate(opts)
	if err != nil {
		return entry{}, nil, fmt.Errorf("%s: %w", name, err)
	}

	if e.def.Kind == stepdef.Router {
		links := n.Links
		if len(links) != len(n.Next) { // a flow built without link attributes
			links = make([]stepdef.Link, len(n.Next))
		}
		params[stepdef.Links] = links
	}

	return e, params, nil
}

// StepInfo describes a registered step for a catalog, from its definition and schema.
type StepInfo struct {
	Name        string
	Kind        string // Source, Action, Router or Sink
	Pattern     string // Enterprise Integration Pattern; "" for none
	Description string
	Options     []OptionInfo // sorted by name
}

// OptionInfo describes one option of a step, from its schema.
type OptionInfo struct {
	Name        string
	Type        string // string, integer, number or boolean
	Description string
	Default     any // nil for none
	Required    bool
}

// Steps returns the registered steps, sorted by name and kind.
func (r *Registry) Steps() []StepInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	steps := make([]StepInfo, 0, len(r.defs))
	for _, e := range r.defs {
		info := StepInfo{Name: e.def.Name, Kind: e.def.Kind, Pattern: e.def.Pattern, Description: e.schema.description}
		for _, name := range e.schema.names {
			p := e.schema.props[name]
			info.Options = append(info.Options, OptionInfo{
				Name: name, Type: p.typ, Description: p.description, Default: p.def,
				Required: slices.Contains(e.schema.required, name),
			})
		}
		steps = append(steps, info)
	}
	slices.SortFunc(steps, func(a, b StepInfo) int {
		return strings.Compare(a.Name+"/"+a.Kind, b.Name+"/"+b.Kind)
	})
	return steps
}

func (r *Registry) lookup(name, kind string) (entry, bool) {
	kinds := []string{kind}
	switch kind {
	case stepdef.Action:
		kinds = append(kinds, stepdef.Router, stepdef.Sink) // a wastebin router stops the message, its sink would not
	case stepdef.Sink:
		kinds = append(kinds, stepdef.Action)
	}

	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, k := range kinds {
		if e, ok := r.defs[name+"/"+k]; ok {
			return e, true
		}
	}
	return entry{}, false
}

func implements(p stepdef.Processor, kind string) bool {
	switch kind {
	case stepdef.Source:
		_, ok := p.(stepdef.SourceProcessor)
		return ok
	case stepdef.Action:
		_, ok := p.(stepdef.ActionProcessor)
		return ok
	case stepdef.Router:
		_, ok := p.(stepdef.RouterProcessor)
		_, loops := p.(stepdef.Looper)
		return ok || loops
	case stepdef.Sink:
		_, ok := p.(stepdef.SinkProcessor)
		return ok
	}
	return false
}
