package api

import (
	"context"
	"fmt"
	"strings"

	"dif/engine"
	flowdef "dif/flows/definition"
	flowimpl "dif/flows/impl"
	"dif/internal/mapping"
	"dif/internal/program"
	stepdef "dif/steps/definition"
	stepimpl "dif/steps/impl"
)

// ProgramSource supplies either a local path or immutable bytes. Path is an
// explicit trusted origin for imports; leave it empty for HTTPS/anonymous input.
type ProgramSource = program.Source
type ProgramLimits = program.Limits
type ProgramDocumentInfo = program.DocumentInfo
type IntegrationInfo = flowimpl.IntegrationInfo
type ReferenceInfo = flowimpl.ReferenceInfo

type ProgramFlowInfo struct {
	Origin      string          `json:"origin"`
	Integration IntegrationInfo `json:"integration"`
	References  []ReferenceInfo `json:"references"`
	Flow        *FlowInfo       `json:"flow"`
}
type ProgramInfo struct {
	Documents []ProgramDocumentInfo `json:"documents"`
	Flows     []ProgramFlowInfo     `json:"flows"`
	// Diagnostics are those of all flows, in the order of the flows.
	Diagnostics []Diagnostic `json:"diagnostics,omitempty"`
}

const serviceBindingPrefix = "service\x00"

type serviceTarget struct {
	id, name string
}

// PreparedProgram owns snapshots and statically validated, immutable flow plans.
// It is reusable: each Load constructs independent graphs and message defaults.
type PreparedProgram struct {
	resolved         *program.Result
	infos            []*FlowInfo
	bindings         map[string]bool
	targets          map[string][]string
	serviceProviders map[string]string
	serviceNames     map[string]string
	serviceCalls     map[string][]serviceTarget
}

// PrepareProgram resolves and validates a group without constructing processors.
// Flowlink dependencies are checked separately so an interactive host can include
// compatible flows it already registered. Load and InspectProgram check them.
func PrepareProgram(ctx context.Context, sources []ProgramSource, limits ProgramLimits) (*PreparedProgram, error) {
	r, err := program.Resolve(ctx, sources, limits)
	if err != nil {
		return nil, err
	}
	if err := validateProcessorChecks(r.ProcessorChecks); err != nil {
		return nil, err
	}
	p := &PreparedProgram{
		resolved:         r,
		bindings:         map[string]bool{},
		targets:          map[string][]string{},
		serviceProviders: map[string]string{},
		serviceNames:     map[string]string{},
		serviceCalls:     map[string][]serviceTarget{},
	}
	for _, entry := range r.Flows {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		f, err := entry.Plan.Build(func(n *flowdef.Node) (stepdef.Processor, error) { return nil, validatePreparedNode(n) })
		if err != nil {
			return nil, fmt.Errorf("%s: integration %q: %w", entry.Origin, entry.Plan.Integration.ID, err)
		}
		info, err := inspectFlow(f)
		if err != nil {
			return nil, fmt.Errorf("%s: flow %q: %w", entry.Origin, f.ID, err)
		}
		p.infos = append(p.infos, info)
		p.bindings[f.ID] = f.Source.URI == "flowlink" || f.Source.URI == "flowlink-async"
		if p.bindings[f.ID] {
			if id, ok := f.Source.Options["flowId"].(string); ok && id != "" && id != f.ID {
				return nil, fmt.Errorf("flow %q: flowlink source flowId must match its flow id", f.ID)
			}
		}
		if f.Source.URI == "service" {
			if sb, ok := f.Source.Bindings[mapping.ServiceBindingKey].(*mapping.ServiceBinding); ok && sb != nil {
				if prev, exists := p.serviceProviders[sb.ID]; exists {
					return nil, fmt.Errorf("duplicate service provider for %q in flows %q and %q", sb.Name, prev, f.ID)
				}
				p.serviceProviders[sb.ID] = f.ID
				p.serviceNames[sb.ID] = sb.Name
				p.bindings[serviceBindingPrefix+sb.ID] = true
			}
		}
		var visit func(*flowdef.Node)
		visit = func(n *flowdef.Node) {
			if n == nil {
				return
			}
			if n.Kind != stepdef.Source && (n.URI == "flowlink" || n.URI == "flowlink-async") {
				if id, ok := n.Options["targetFlowId"].(string); ok {
					p.targets[f.ID] = append(p.targets[f.ID], id)
				}
			}
			if n.Kind != stepdef.Source && n.URI == "service" {
				if sb, ok := n.Bindings[mapping.ServiceBindingKey].(*mapping.ServiceBinding); ok && sb != nil {
					p.serviceCalls[f.ID] = append(p.serviceCalls[f.ID], serviceTarget{id: sb.ID, name: sb.Name})
				}
			}
			for _, next := range n.Next {
				visit(next)
			}
			for _, after := range n.After {
				visit(after)
			}
			for _, onErr := range n.OnError {
				visit(onErr)
			}
		}
		visit(f.Source)
		for _, h := range f.ErrorHandlers() {
			visit(h.Route)
		}
	}
	return p, nil
}

// CheckFlowLinks validates literal targets against this group and the host's
// existing flows (value true means a compatible flowlink source).
func (p *PreparedProgram) CheckFlowLinks(existing map[string]bool) error {
	for _, entry := range p.resolved.Flows {
		for _, target := range p.targets[entry.Plan.ID] {
			compatible, ok := p.bindings[target]
			if !ok {
				compatible, ok = existing[target]
			}
			if !ok {
				return fmt.Errorf("flow %q: missing flowlink target %q", entry.Plan.ID, target)
			}
			if !compatible {
				return fmt.Errorf("flow %q: target %q has no compatible flowlink source", entry.Plan.ID, target)
			}
		}
		for id, flowID := range p.serviceProviders {
			if flowID == entry.Plan.ID && existing[serviceBindingPrefix+id] {
				return fmt.Errorf("flow %q: duplicate service provider for %q", flowID, p.serviceNames[id])
			}
		}
		for _, target := range p.serviceCalls[entry.Plan.ID] {
			if p.bindings[serviceBindingPrefix+target.id] || existing[serviceBindingPrefix+target.id] {
				continue
			}
			return fmt.Errorf("flow %q: missing service provider for %q", entry.Plan.ID, target.name)
		}
	}
	return nil
}

// Info returns independent metadata without option, expression, or resource values.
func (p *PreparedProgram) Info() *ProgramInfo {
	info := &ProgramInfo{Documents: []ProgramDocumentInfo{}, Flows: []ProgramFlowInfo{}}
	for _, doc := range p.resolved.Documents {
		doc.Imports = append([]program.Dependency{}, doc.Imports...)
		doc.Integrations = append([]IntegrationInfo{}, doc.Integrations...)
		doc.Formats = append([]FormatInfo{}, doc.Formats...)
		doc.Maps = append([]MapInfo{}, doc.Maps...)
		doc.Services = append([]ServiceInfo{}, doc.Services...)
		for i := range doc.Services {
			doc.Services[i].InputHeaders = append([]HeaderInfo{}, doc.Services[i].InputHeaders...)
			doc.Services[i].OutputHeaders = append([]HeaderInfo{}, doc.Services[i].OutputHeaders...)
		}
		doc.Functions = append([]mapping.FunctionInfo{}, doc.Functions...)
		for i := range doc.Functions {
			doc.Functions[i].Parameters = append([]mapping.ParameterInfo{}, doc.Functions[i].Parameters...)
		}
		doc.Processors = append([]flowimpl.ProcessorInfo{}, doc.Processors...)
		for i := range doc.Processors {
			doc.Processors[i].Parameters = append([]mapping.ParameterInfo{}, doc.Processors[i].Parameters...)
		}
		doc.Connections = append([]flowimpl.ConnectionInfo{}, doc.Connections...)
		doc.Stores = append([]StoreInfo(nil), doc.Stores...)
		info.Documents = append(info.Documents, doc)
	}
	for i, e := range p.resolved.Flows {
		// Inspection contains only safe scalar metadata and slices.
		f := *p.infos[i]
		if f.Service != nil {
			f.Service = cloneServiceInfo(*f.Service)
		}
		f.Steps = append([]NodeInfo{}, f.Steps...)
		for j := range f.Steps {
			if f.Steps[j].Processor != nil {
				v := *f.Steps[j].Processor
				v.Parameters = append([]mapping.ParameterInfo{}, v.Parameters...)
				f.Steps[j].Processor = &v
			}
			if f.Steps[j].Mapping != nil {
				m := *f.Steps[j].Mapping
				f.Steps[j].Mapping = &m
			}
			if f.Steps[j].Service != nil {
				f.Steps[j].Service = cloneServiceInfo(*f.Steps[j].Service)
			}
			f.Steps[j].Links = append([]LinkInfo{}, f.Steps[j].Links...)
			f.Steps[j].Capabilities = append([]Capability(nil), f.Steps[j].Capabilities...)
		}
		f.Configuration = append([]ConfigurationInfo{}, f.Configuration...)
		if f.Error != nil {
			v := *f.Error
			v.Matches = append([]string{}, v.Matches...)
			f.Error = &v
		}
		f.ErrorHandlers = append([]ErrorInfo{}, f.ErrorHandlers...)
		for j := range f.ErrorHandlers {
			f.ErrorHandlers[j].Matches = append([]string{}, f.ErrorHandlers[j].Matches...)
		}
		f.Diagnostics = append([]Diagnostic(nil), f.Diagnostics...)
		info.Diagnostics = append(info.Diagnostics, f.Diagnostics...)
		info.Flows = append(info.Flows, ProgramFlowInfo{e.Origin, e.Plan.Integration, append([]ReferenceInfo{}, e.Plan.References...), &f})
	}
	return info
}

type Program struct {
	Flows []*Flow
	Info  *ProgramInfo
}

// Load constructs the complete stopped group after checking all references.
// Existing bindings allow a host to link to already registered flows.
func (p *PreparedProgram) Load(ctx context.Context, runtime *Runtime, onResult func(string, *Result, error), existing map[string]bool) (*Program, error) {
	if err := p.CheckFlowLinks(existing); err != nil {
		return nil, err
	}
	if len(p.resolved.Flows) == 0 {
		return nil, fmt.Errorf("program contains no flows")
	}
	if runtime != nil {
		if err := runtime.channels.Check(); err != nil {
			return nil, err
		}
		if runtime.policy != nil {
			if ds := p.Info().CheckPolicy(runtime.policy); len(ds) > 0 {
				return nil, policyError(ds)
			}
		}
	}
	if err := p.declareStores(runtime); err != nil {
		return nil, err
	}
	loaded := &Program{Flows: []*Flow{}, Info: p.Info()}
	for _, entry := range p.resolved.Flows {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		f, err := entry.Plan.Build(func(n *flowdef.Node) (stepdef.Processor, error) {
			if runtime == nil {
				return steps.Processor(n)
			}
			return steps.ProcessorWithParams(n, stepdef.Params{stepimpl.ChannelRuntimeKey: runtime.channels, stepimpl.FlowRuntimeKey: n.Flow})
		})
		if err != nil {
			return nil, fmt.Errorf("%s: flow %q initialization failed: %w", entry.Origin, entry.Plan.ID, err)
		}
		var callback func(*Result, error)
		if onResult != nil {
			callback = func(res *Result, err error) { onResult(f.ID, res, err) }
		}
		events := defaultEvents
		if runtime != nil {
			events = runtime.events
		}
		loaded.Flows = append(loaded.Flows, newFlow(f, callback, events))
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return loaded, nil
}

func preparePath(path string) (*PreparedProgram, error) {
	return PrepareProgram(context.Background(), []ProgramSource{{Name: path, Path: path}}, ProgramLimits{})
}
func LoadProgram(path string, onResult func(string, *Result, error)) (*Program, error) {
	p, err := preparePath(path)
	if err != nil {
		return nil, err
	}
	return p.Load(context.Background(), nil, onResult, nil)
}
func (r *Runtime) LoadProgram(path string, onResult func(string, *Result, error)) (*Program, error) {
	p, err := preparePath(path)
	if err != nil {
		return nil, err
	}
	return p.Load(context.Background(), r, onResult, nil)
}
func InspectProgram(path string) (*ProgramInfo, error) {
	p, err := preparePath(path)
	if err != nil {
		return nil, err
	}
	if err := p.CheckFlowLinks(nil); err != nil {
		return nil, err
	}
	return p.Info(), nil
}
func ValidateProgram(path string) error { _, err := InspectProgram(path); return err }

// FlowBindings reports compatible already-registered flowlink and service sources.
func FlowBindings(runners []*engine.Runner) map[string]bool {
	bindings := map[string]bool{}
	for _, r := range runners {
		in, _ := r.LocalChannels()
		bindings[r.ID()] = in == "queue:flowlink:"+r.ID()
		if id, ok := strings.CutPrefix(in, "queue:service:"); ok && id != "" {
			bindings[serviceBindingPrefix+id] = true
		}
	}
	return bindings
}
