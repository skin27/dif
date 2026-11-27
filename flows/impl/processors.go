package impl

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"strings"

	flowdef "dif/flows/definition"
	"dif/internal/mapping"
	stepdef "dif/steps/definition"
)

type dilProcessor struct {
	Name       string              `json:"name"`
	Parameters []mapping.Parameter `json:"parameters"`
	Action     processorAction     `json:"action"`
}
type processorAction struct {
	URI     string         `json:"uri"`
	Options map[string]any `json:"options"`
}

func (p *dilProcessor) UnmarshalJSON(data []byte) error {
	type plain dilProcessor
	return strictDeclaration(data, (*plain)(p), "processor")
}
func (p *processorAction) UnmarshalJSON(data []byte) error {
	type plain processorAction
	return strictDeclaration(data, (*plain)(p), "processor action")
}

type ProcessorInfo struct {
	Name       string                  `json:"name"`
	Origin     string                  `json:"origin,omitempty"`
	Parameters []mapping.ParameterInfo `json:"parameters"`

	connection *stepdef.Connection      // of the expanded action, resolved in the defining document
	stores     map[string]stepdef.Store // the stores the defining document may refer to
}
type ProcessorCheck struct {
	Node         *flowdef.Node
	Dynamic      map[string]string
	Origin, Name string
}

func (d *Document) ProcessorInfo() []ProcessorInfo {
	out := []ProcessorInfo{}
	for _, p := range d.doc.DIL.Core.Processors.Processor {
		i := ProcessorInfo{Name: p.Name, Origin: d.origin, Parameters: []mapping.ParameterInfo{}}
		for _, v := range p.Parameters {
			i.Parameters = append(i.Parameters, mapping.ParameterInfo{Name: v.Name, Type: v.Type, Optional: len(v.Default) > 0})
		}
		out = append(out, i)
	}
	return out
}
func (d *Document) processorOwner(name string) (*Document, *dilProcessor) {
	for i := range d.doc.DIL.Core.Processors.Processor {
		p := &d.doc.DIL.Core.Processors.Processor[i]
		if p.Name == name {
			return d, p
		}
	}
	alias, key, ok := strings.Cut(name, ":")
	if ok && d.imports[alias] != nil {
		other := d.imports[alias]
		for i := range other.doc.DIL.Core.Processors.Processor {
			p := &other.doc.DIL.Core.Processors.Processor[i]
			if p.Name == key {
				return other, p
			}
		}
	}
	return nil, nil
}
func walkOptions(v any, params map[string]any, depth int) (any, error) {
	if depth > 128 {
		return nil, fmt.Errorf("processor option depth exceeded")
	}
	switch x := v.(type) {
	case map[string]any:
		if literal, ok := x["$literal"]; ok && len(x) == 1 {
			return literal, nil
		}
		if ref, ok := x["$param"]; ok && len(x) == 1 {
			name, ok := ref.(string)
			if !ok {
				return nil, fmt.Errorf("invalid parameter reference")
			}
			value, found := params[name]
			if !found {
				return nil, fmt.Errorf("unknown parameter %q", name)
			}
			return value, nil
		}
		out := map[string]any{}
		for k, v := range x {
			a, e := walkOptions(v, params, depth+1)
			if e != nil {
				return nil, e
			}
			out[k] = a
		}
		return out, nil
	case []any:
		out := []any{}
		for _, v := range x {
			a, e := walkOptions(v, params, depth+1)
			if e != nil {
				return nil, e
			}
			out = append(out, a)
		}
		return out, nil
	default:
		return v, nil
	}
}
func (d *Document) prepareProcessors(l mapping.Limits) error {
	for _, p := range d.doc.DIL.Core.Processors.Processor {
		if p.Name == "" || p.Action.URI == "" || strings.HasPrefix(p.Action.URI, "processor:") || p.Action.URI == "processor" {
			return fmt.Errorf("processor requires a name and a non-nested action URI")
		}
		if _, e := mapping.ValidateParameters(p.Parameters, l); e != nil {
			return fmt.Errorf("processor %q: %w", p.Name, e)
		}
		params := map[string]any{}
		for _, v := range p.Parameters {
			params[v.Name] = nil
		}
		if _, e := walkOptions(p.Action.Options, params, 0); e != nil {
			return fmt.Errorf("processor %q: %w", p.Name, e)
		}
	}
	return nil
}
func (d *Document) visibleCore() coreRefs {
	core := refsOf(d.doc)
	for _, i := range d.Imports() {
		other := d.imports[i.Namespace]
		if other == nil {
			continue
		}
		for k, v := range refsOf(other.doc).resources {
			name := i.Namespace + ":" + k
			if _, ok := core.resources[name]; !ok {
				core.resources[name] = v
			}
		}
		for k, v := range refsOf(other.doc).messages {
			name := i.Namespace + ":" + k
			if _, ok := core.messages[name]; !ok {
				core.messages[name] = v
			}
		}
		for k, v := range refsOf(other.doc).connections {
			name := i.Namespace + ":" + k
			if _, ok := core.connections[name]; !ok {
				core.connections[name] = v
			}
		}
	}
	return core
}
func (d *Document) ProcessorChecks() ([]ProcessorCheck, error) {
	out := []ProcessorCheck{}
	for _, p := range d.doc.DIL.Core.Processors.Processor {
		dynamic := map[string]string{}
		opts := maps.Clone(p.Action.Options)
		for k, v := range opts {
			if marker, ok := v.(map[string]any); ok && len(marker) == 1 {
				if name, ok := marker["$param"].(string); ok {
					for _, param := range p.Parameters {
						if param.Name == name {
							dynamic[k] = param.Type
						}
					}
					delete(opts, k)
				} else if literal, ok := marker["$literal"]; ok {
					opts[k] = literal
				}
			}
		}
		if dynamic["connection"] != "" {
			return nil, fmt.Errorf("processor %q: option connection cannot be a parameter", p.Name)
		}
		s := dilStep{ID: p.Name, Type: flowdef.Action, URI: p.Action.URI, Options: opts}
		opts, uri, e := resolveRefs(s, d.visibleCore())
		if e != nil {
			return nil, e
		}
		bindings := map[string]any{mapping.FunctionsBindingKey: d.functionBindings()}
		// dataSource may itself be a configuration parameter; resolve it at use.
		if dynamic["dataSource"] == "" {
			var conn *stepdef.Connection
			opts, conn, e = resolveConnection(uri, opts, d.visibleCore())
			if e != nil {
				return nil, fmt.Errorf("processor %q: %w", p.Name, e)
			}
			if conn != nil {
				bindings[stepdef.ConnectionBinding] = conn
			}
		}
		if name, ok := strings.CutPrefix(uri, "mapper:"); ok {
			if d.mappingBindings()[name] == nil {
				return nil, fmt.Errorf("processor %q: map %q not found", p.Name, name)
			}
			uri = "mapper"
		} else if name, ok := strings.CutPrefix(uri, "service:"); ok {
			sb := d.serviceBindings()[name]
			if sb == nil {
				return nil, fmt.Errorf("processor %q: service %q not found", p.Name, name)
			}
			if err := checkServiceCallerStep(flowdef.Action, opts, dynamic, sb); err != nil {
				return nil, fmt.Errorf("processor %q: %w", p.Name, err)
			}
			uri = "service"
			bindings[mapping.ServiceBindingKey] = sb
		}
		out = append(out, ProcessorCheck{Node: &flowdef.Node{ID: p.Name, Kind: flowdef.Action, URI: uri, Options: opts, Bindings: bindings}, Dynamic: dynamic, Name: p.Name, Origin: d.origin})
	}
	return out, nil
}

type expandedFlow struct {
	flow        dilFlow
	scopes      map[string]*mapping.Functions
	templates   map[string]ProcessorInfo
	plans       map[string]*mapping.Plan
	services    map[string]*mapping.ServiceBinding
	stepConns   map[string]*stepdef.Connection
	stepStores  map[string]map[string]stepdef.Store
	preResolved map[string]bool
}

func (d *Document) expandProcessors(df dilFlow, extra []ProgramInterceptor) (expandedFlow, error) {
	df, owners, err := d.weaveInterceptors(df, extra)
	if err != nil {
		return expandedFlow{flow: df}, err
	}
	scopes := map[string]*mapping.Functions{}
	templates := map[string]ProcessorInfo{}
	plans := d.mappingBindings()
	services := d.serviceBindings()
	stepConns := map[string]*stepdef.Connection{}
	stepStores := map[string]map[string]stepdef.Store{}
	preResolved := map[string]bool{}
	defaultScope := d.functionBindings()
	df.Steps.Step = append(oneOrMany[dilStep](nil), df.Steps.Step...)
	for i, s := range df.Steps.Step {
		stepDoc := d
		if own := owners[s.ID]; own != nil {
			stepDoc = own
		}
		if stepDoc == d {
			scopes[s.ID] = defaultScope
		} else {
			scopes[s.ID] = stepDoc.functionBindings()
		}
		name, ok := strings.CutPrefix(s.URI, "processor:")
		if !ok {
			if stepDoc == d {
				continue
			}
			// A program-scoped interceptor from another document resolves its
			// own refs, connections, maps, services, and stores against stepDoc.
			var e error
			s.Options, s.URI, e = resolveRefs(s, stepDoc.visibleCore())
			if e != nil {
				return expandedFlow{flow: df}, fmt.Errorf("step %q (%s): %w", s.ID, stepDoc.origin, e)
			}
			var conn *stepdef.Connection
			s.Options, conn, e = resolveConnection(s.URI, s.Options, stepDoc.visibleCore())
			if e != nil {
				return expandedFlow{flow: df}, fmt.Errorf("step %q (%s): %w", s.ID, stepDoc.origin, e)
			}
			if strings.HasPrefix(s.URI, "sql2") && s.Options["connection"] != nil {
				s.Options = maps.Clone(s.Options)
				delete(s.Options, "dataSource")
			}
			if mapName, ok := strings.CutPrefix(s.URI, "mapper:"); ok {
				plan := stepDoc.mappingBindings()[mapName]
				if plan == nil {
					return expandedFlow{flow: df}, fmt.Errorf("step %q (%s): map %q not found", s.ID, stepDoc.origin, mapName)
				}
				key := "@interceptor/" + s.ID
				for plans[key] != nil {
					key += "_"
				}
				plans[key] = plan
				s.URI = "mapper:" + key
			} else if svcName, ok := strings.CutPrefix(s.URI, "service:"); ok {
				sb := stepDoc.serviceBindings()[svcName]
				if sb == nil {
					return expandedFlow{flow: df}, fmt.Errorf("step %q (%s): service %q not found", s.ID, stepDoc.origin, svcName)
				}
				key := "@interceptor/" + s.ID
				for services[key] != nil {
					key += "_"
				}
				services[key] = sb
				s.URI = "service:" + key
			}
			if conn != nil {
				stepConns[s.ID] = conn
			}
			stepStores[s.ID] = stepDoc.storeBindings()
			preResolved[s.ID] = true
			df.Steps.Step[i] = s
			continue
		}
		if s.Type != flowdef.Action && !(s.Type == flowdef.Sink && strings.HasPrefix(s.ID, InterceptorPrefix)) {
			return expandedFlow{flow: df}, fmt.Errorf("processor %q must be an action", name)
		}
		owner, p := stepDoc.processorOwner(name)
		if p == nil {
			return expandedFlow{flow: df}, fmt.Errorf("processor %q not found", name)
		}
		fail := func(e error) (expandedFlow, error) {
			return expandedFlow{flow: df}, fmt.Errorf("step %q processor %q (%s): %w", s.ID, name, owner.origin, e)
		}
		for k := range s.Options {
			if k != "arguments" {
				return fail(fmt.Errorf("unknown processor option %q", k))
			}
		}
		args := map[string]any{}
		if raw, ok := s.Options["arguments"]; ok {
			var valid bool
			args, valid = raw.(map[string]any)
			if !valid {
				return fail(fmt.Errorf("arguments must be an object"))
			}
		}
		values := map[string]any{}
		for _, param := range p.Parameters {
			v, found := args[param.Name]
			if !found {
				if len(param.Default) == 0 {
					return fail(fmt.Errorf("missing parameter %q", param.Name))
				}
				decoder := json.NewDecoder(bytes.NewReader(param.Default))
				decoder.UseNumber()
				if e := decoder.Decode(&v); e != nil {
					return fail(e)
				}
			}
			if e := mapping.CheckNativeType(param.Type, v, owner.functionLimits); e != nil {
				return fail(fmt.Errorf("parameter %q: %w", param.Name, e))
			}
			values[param.Name] = v
		}
		for k := range args {
			if _, ok := values[k]; !ok {
				return fail(fmt.Errorf("unknown parameter %q", k))
			}
		}
		v, e := walkOptions(p.Action.Options, values, 0)
		if e != nil {
			return fail(e)
		}
		s.Options, _ = v.(map[string]any)
		s.URI = p.Action.URI
		s.Options, s.URI, e = resolveRefs(s, owner.visibleCore())
		if e != nil {
			return fail(e)
		}
		var conn *stepdef.Connection
		s.Options, conn, e = resolveConnection(s.URI, s.Options, owner.visibleCore())
		if e != nil {
			return fail(e)
		}
		// Resolved SQL connection is now self-contained; do not look it up in the caller.
		if strings.HasPrefix(s.URI, "sql2") && s.Options["connection"] != nil {
			s.Options = maps.Clone(s.Options)
			delete(s.Options, "dataSource")
		}
		if mapName, ok := strings.CutPrefix(s.URI, "mapper:"); ok {
			plan := owner.mappingBindings()[mapName]
			if plan == nil {
				return fail(fmt.Errorf("map %q not found", mapName))
			}
			key := "@processor/" + s.ID
			for plans[key] != nil {
				key += "_"
			}
			plans[key] = plan
			s.URI = "mapper:" + key
		} else if svcName, ok := strings.CutPrefix(s.URI, "service:"); ok {
			sb := owner.serviceBindings()[svcName]
			if sb == nil {
				return fail(fmt.Errorf("service %q not found", svcName))
			}
			key := "@processor/" + s.ID
			for services[key] != nil {
				key += "_"
			}
			services[key] = sb
			s.URI = "service:" + key
		}
		scopes[s.ID] = owner.functionBindings()
		templates[s.ID] = ProcessorInfo{Name: p.Name, Origin: owner.origin, connection: conn, stores: owner.storeBindings()}
		df.Steps.Step[i] = s
	}
	return expandedFlow{
		flow:        df,
		scopes:      scopes,
		templates:   templates,
		plans:       plans,
		services:    services,
		stepConns:   stepConns,
		stepStores:  stepStores,
		preResolved: preResolved,
	}, nil
}
