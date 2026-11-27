package impl

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"

	flowdef "dif/flows/definition"
	"dif/internal/mapping"
	"dif/message"
	stepdef "dif/steps/definition"
)

// Import names a document-local namespace. Paths are resolved by the loader.
type Import struct {
	Path      string `json:"path"`
	Namespace string `json:"namespace"`
}

func (i *Import) UnmarshalJSON(data []byte) error {
	type plain Import
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode((*plain)(i)); err != nil {
		return fmt.Errorf("invalid import entry (expected path and namespace)")
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return fmt.Errorf("invalid import entry")
	}
	if strings.TrimSpace(i.Path) == "" || !namespacePattern.MatchString(i.Namespace) {
		return fmt.Errorf("import requires a path and a namespace matching [A-Za-z_][A-Za-z0-9_-]*")
	}
	return nil
}

var namespacePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)

// Document is a decoded DIL document. Its definitions are private and immutable.
type Document struct {
	doc              dilDoc
	origin           string
	scopeKey         string
	imports          map[string]*Document
	compiledMaps     map[string]*mapping.Plan
	compiledServices map[string]*mapping.ServiceBinding
	functions        map[string]*mapping.Function
	compiling        map[string]bool
	functionLimits   mapping.Limits
}

type IntegrationInfo struct {
	ID      string `json:"id,omitempty"`
	Name    string `json:"name,omitempty"`
	Type    string `json:"type,omitempty"`
	Version string `json:"version,omitempty"`
}

type ReferenceInfo struct {
	Step string `json:"step,omitempty"`
	Kind string `json:"kind"`
	Name string `json:"name"`
}

// FlowPlan stores immutable resolved definitions, never processor instances.
type FlowPlan struct {
	ID              string
	Integration     IntegrationInfo
	References      []ReferenceInfo
	data            []byte
	input           []byte
	mappings        map[string]*mapping.Plan
	services        map[string]*mapping.ServiceBinding
	scopes          map[string]*mapping.Functions
	actionTemplates map[string]ProcessorInfo
	stores          map[string]stepdef.Store
	stepConns       map[string]*stepdef.Connection
	stepStores      map[string]map[string]stepdef.Store
	preResolved     map[string]bool
}

// DecodeDocument checks declaration uniqueness without constructing processors.
func DecodeDocument(data []byte) (*Document, error) {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, fmt.Errorf("invalid DIL JSON")
	}
	if v, ok := envelope["dil"]; !ok || len(v) == 0 || v[0] != '{' {
		return nil, fmt.Errorf("document requires a dil object")
	}
	d := &Document{}
	if err := json.Unmarshal(data, &d.doc); err != nil {
		return nil, fmt.Errorf("invalid DIL structure: %w", safeDecodeError(err))
	}
	seen := map[string]bool{}
	check := func(kind, name string) error {
		if name == "" {
			return fmt.Errorf("empty %s name", kind)
		}
		key := kind + "\x00" + name
		if seen[key] {
			return fmt.Errorf("duplicate %s %q", kind, name)
		}
		seen[key] = true
		return nil
	}
	for _, i := range d.doc.DIL.Imports.Import {
		if err := check("import namespace", i.Namespace); err != nil {
			return nil, err
		}
	}
	for _, f := range d.doc.DIL.Core.Functions.Function {
		if err := check("function", f.Name); err != nil {
			return nil, err
		}
	}
	for _, p := range d.doc.DIL.Core.Processors.Processor {
		if err := check("processor", p.Name); err != nil {
			return nil, err
		}
	}
	for _, s := range d.doc.DIL.Core.Services.Service {
		if err := check("service", s.Name); err != nil {
			return nil, err
		}
	}
	for _, m := range d.doc.DIL.Core.Messages.Message {
		if err := check("message", m.Name); err != nil {
			return nil, err
		}
	}
	for _, r := range d.doc.DIL.Core.Resources.Resource {
		if err := check("resource", r.Name); err != nil {
			return nil, err
		}
	}
	for _, c := range d.doc.DIL.Core.Connections.Connection {
		if err := check("connection", c.ID); err != nil {
			return nil, err
		}
		if err := c.check(); err != nil {
			return nil, err
		}
	}
	for _, s := range d.doc.DIL.Core.Stores.Store {
		if err := check("store", s.Name); err != nil {
			return nil, err
		}
		if _, err := s.store(); err != nil {
			return nil, err
		}
	}
	for i := range d.doc.DIL.Core.Tests.Test {
		t := &d.doc.DIL.Core.Tests.Test[i]
		if err := check("test", t.Name); err != nil {
			return nil, err
		}
		if err := t.validate(); err != nil {
			return nil, err
		}
	}
	for _, in := range d.doc.DIL.Integrations.Integration {
		if in.ID != "" {
			if err := check("integration", in.ID); err != nil {
				return nil, err
			}
		}
	}
	return d, nil
}

func safeDecodeError(err error) error {
	if e, ok := err.(*json.UnmarshalTypeError); ok {
		return fmt.Errorf("invalid type at %s (byte %d)", e.Field, e.Offset)
	}
	// Custom import and test errors contain schema information only.
	if strings.HasPrefix(err.Error(), "import") || strings.HasPrefix(err.Error(), "invalid import") || strings.HasPrefix(err.Error(), "invalid test") {
		return err
	}
	return fmt.Errorf("invalid JSON field type")
}

func (d *Document) Imports() []Import { return append([]Import(nil), d.doc.DIL.Imports.Import...) }
func (d *Document) Integrations() []IntegrationInfo {
	result := []IntegrationInfo{}
	for _, i := range d.doc.DIL.Integrations.Integration {
		result = append(result, integrationInfo(i))
	}
	return result
}
func integrationInfo(i dilIntegration) IntegrationInfo {
	return IntegrationInfo{i.ID, i.Name, i.Type, versionText(i.Version)}
}

// Plans makes one flow plan per flow, resolving only direct imports. Local exact
// names win over qualified spellings, including historical names with colons.
func (d *Document) Plans(imports map[string]*Document) ([]FlowPlan, error) {
	return d.PlansWithInterceptors(imports, nil)
}

// PlansWithInterceptors makes one flow plan per flow, also weaving program-scoped
// interceptors from other documents in the resolved program without mutating d.
func (d *Document) PlansWithInterceptors(imports map[string]*Document, extra []ProgramInterceptor) ([]FlowPlan, error) {
	merged := d.doc
	for _, imp := range d.Imports() {
		other := imports[imp.Namespace]
		if other == nil {
			return nil, fmt.Errorf("missing import namespace %q", imp.Namespace)
		}
		for _, m := range other.doc.DIL.Core.Messages.Message {
			m.Name = imp.Namespace + ":" + m.Name
			if !hasMessage(merged, m.Name) {
				merged.DIL.Core.Messages.Message = append(append(oneOrMany[dilMessage](nil), merged.DIL.Core.Messages.Message...), m)
			}
		}
		for _, r := range other.doc.DIL.Core.Resources.Resource {
			r.Name = imp.Namespace + ":" + r.Name
			if !hasResource(merged, r.Name) {
				merged.DIL.Core.Resources.Resource = append(append(oneOrMany[dilResource](nil), merged.DIL.Core.Resources.Resource...), r)
			}
		}
		for _, c := range other.doc.DIL.Core.Connections.Connection {
			c.ID = imp.Namespace + ":" + c.ID
			if !hasConnection(merged, c.ID) {
				merged.DIL.Core.Connections.Connection = append(append(oneOrMany[dilConnection](nil), merged.DIL.Core.Connections.Connection...), c)
			}
		}
	}
	merged.DIL.Imports.Import = nil
	merged.DIL.Core.Tests.Test = nil
	core := refsOf(merged)
	stores := d.storeBindings()
	plans := []FlowPlan{}
	for _, in := range d.doc.DIL.Integrations.Integration {
		for _, df := range in.Flows.Flow {
			processorRefs := []ReferenceInfo{}
			for _, s := range df.Steps.Step {
				if name, ok := strings.CutPrefix(s.URI, "processor:"); ok {
					processorRefs = append(processorRefs, ReferenceInfo{s.ID, "processor", name})
				}
			}
			exp, err := d.expandProcessors(df, extra)
			if err != nil {
				return nil, err
			}
			df = exp.flow
			input, err := selectInput(df, d.doc, core)
			if err != nil {
				return nil, fmt.Errorf("integration %q flow %q: %w", in.ID, df.ID, err)
			}
			copyIn := in
			copyIn.Flows.Flow = oneOrMany[dilFlow]{df}
			one := merged
			one.DIL.Integrations.Integration = oneOrMany[dilIntegration]{copyIn}
			data, err := json.Marshal(one)
			if err != nil {
				return nil, fmt.Errorf("flow %q cannot be encoded", df.ID)
			}
			inputData, err := json.Marshal(input)
			if err != nil {
				return nil, fmt.Errorf("flow %q input cannot be encoded", df.ID)
			}
			refs := processorRefs
			if df.Options.InputMessage != "" {
				refs = append(refs, ReferenceInfo{Kind: "message", Name: df.Options.InputMessage})
			}
			if df.Options.Service != "" {
				refs = append(refs, ReferenceInfo{Kind: "service", Name: df.Options.Service})
			}
			for _, s := range df.Steps.Step {
				// Error-handler options are validated by the graph builder, outside
				// the step registry. Check them here without quoting their values.
				if s.Type == "error" {
					if s.URI != "failedexchange" {
						return nil, fmt.Errorf("integration %q flow %q step %q: error step must use failedexchange", in.ID, df.ID, s.ID)
					}
					if _, bad := parseErrorOptions(s.Options); bad != nil {
						return nil, fmt.Errorf("integration %q flow %q step %q: error option %s %s", in.ID, df.ID, s.ID, bad.name, bad.want)
					}
				}
				scheme, rest, _ := strings.Cut(s.URI, ":")
				if name, ok := s.Options["store"].(string); ok && name != "" {
					refs = append(refs, ReferenceInfo{s.ID, "store", name})
				}
				if scheme == "store" {
					refs = append(refs, ReferenceInfo{s.ID, "store", rest})
					continue
				}
				if scheme == "mapper" {
					if !strings.HasPrefix(rest, "@processor/") && !strings.HasPrefix(rest, "@interceptor/") {
						refs = append(refs, ReferenceInfo{s.ID, "map", rest})
					}
					continue
				}
				if scheme == "service" {
					if !strings.HasPrefix(rest, "@processor/") && !strings.HasPrefix(rest, "@interceptor/") {
						refs = append(refs, ReferenceInfo{s.ID, "service", rest})
					}
					continue
				}
				for _, kind := range []string{"ref", "message"} {
					if name, ok := strings.CutPrefix(rest, kind+":"); ok {
						k := kind
						if k == "ref" {
							k = "resource"
						}
						refs = append(refs, ReferenceInfo{s.ID, k, name})
					}
				}
				if name, ok := s.Options["dataSource"].(string); ok && name != "" && scheme == "sql2" {
					refs = append(refs, ReferenceInfo{s.ID, "connection", name})
				} else if name, ok := s.Options["connection"].(string); ok && name != "" && !strings.HasPrefix(strings.TrimSpace(name), "{") {
					refs = append(refs, ReferenceInfo{s.ID, "connection", name})
				}
			}
			plans = append(plans, FlowPlan{
				ID:              df.ID,
				Integration:     integrationInfo(in),
				References:      refs,
				data:            data,
				input:           inputData,
				mappings:        exp.plans,
				services:        exp.services,
				scopes:          exp.scopes,
				actionTemplates: exp.templates,
				stores:          stores,
				stepConns:       exp.stepConns,
				stepStores:      exp.stepStores,
				preResolved:     exp.preResolved,
			})
		}
	}
	return plans, nil
}

func (p FlowPlan) Build(newProcessor func(*flowdef.Node) (stepdef.Processor, error)) (*flowdef.Flow, error) {
	f, err := parse(p.data, newProcessor, p.mappings, p.services, p.scopes, p.actionTemplates, p.stores, p.stepConns, p.stepStores, p.preResolved)
	if err != nil {
		return nil, err
	}
	// Parse's legacy first-message default must not select an imported message.
	if err := json.Unmarshal(p.input, &f.Input); err != nil {
		return nil, err
	}
	return f, nil
}

func refsOf(d dilDoc) coreRefs {
	c := coreRefs{messages: map[string]dilMessage{}, resources: map[string]string{}, connections: map[string]dilConnection{}}
	for _, m := range d.DIL.Core.Messages.Message {
		c.messages[m.Name] = m
	}
	for _, r := range d.DIL.Core.Resources.Resource {
		c.resources[r.Name] = r.Content
	}
	for _, x := range d.DIL.Core.Connections.Connection {
		c.connections[x.ID] = x
	}
	return c
}
func hasMessage(d dilDoc, name string) bool {
	for _, m := range d.DIL.Core.Messages.Message {
		if m.Name == name {
			return true
		}
	}
	return false
}
func hasResource(d dilDoc, name string) bool {
	for _, r := range d.DIL.Core.Resources.Resource {
		if r.Name == name {
			return true
		}
	}
	return false
}
func hasConnection(d dilDoc, id string) bool {
	for _, c := range d.DIL.Core.Connections.Connection {
		if c.ID == id {
			return true
		}
	}
	return false
}

func selectInput(df dilFlow, doc dilDoc, core coreRefs) (message.Message, error) {
	var selected dilMessage
	if name := df.Options.InputMessage; name != "" {
		var ok bool
		selected, ok = core.messages[name]
		if !ok {
			return nil, fmt.Errorf("options.inputMessage: message %q not found", name)
		}
	} else if len(doc.DIL.Core.Messages.Message) != 0 {
		selected = doc.DIL.Core.Messages.Message[0]
	} else {
		return nil, nil
	}
	m := message.Message{}
	for _, h := range selected.Headers.Header {
		m[h.Name] = h.Value
	}
	if selected.Body != nil {
		m[message.Body] = selected.Body
	}
	return m, nil
}
