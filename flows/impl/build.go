package impl

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"maps"
	"strconv"
	"strings"
	"unicode/utf8"

	flowdef "dif/flows/definition"
	"dif/internal/mapping"
	stepdef "dif/steps/definition"
)

// Parse converts a DIL JSON document holding exactly one flow into the flow model.
// newProcessor creates the processor for each node.
func Parse(data []byte, newProcessor func(*flowdef.Node) (stepdef.Processor, error)) (*flowdef.Flow, error) {
	return parse(data, newProcessor, nil, nil, nil, nil, nil)
}

func parse(data []byte, newProcessor func(*flowdef.Node) (stepdef.Processor, error), plans map[string]*mapping.Plan, services map[string]*mapping.ServiceBinding, scopes map[string]*mapping.Functions, templates map[string]ProcessorInfo, stores map[string]stepdef.Store) (*flowdef.Flow, error) {
	var doc dilDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse dil: %w", err)
	}
	if len(doc.DIL.Imports.Import) != 0 {
		return nil, fmt.Errorf("DIL imports require a program loader")
	}

	var flows []dilFlow
	for _, in := range doc.DIL.Integrations.Integration {
		flows = append(flows, in.Flows.Flow...)
	}
	if len(flows) != 1 {
		return nil, fmt.Errorf("expected exactly one flow, found %d", len(flows))
	}
	if plans == nil {
		d, err := DecodeDocument(data)
		if err != nil {
			return nil, err
		}
		if err = d.PrepareFunctions(context.Background(), mapping.Limits{}); err != nil {
			return nil, err
		}
		if err := d.PrepareMappings(context.Background(), mapping.Limits{}); err != nil {
			return nil, err
		}
		if err := d.PrepareServices(context.Background(), mapping.Limits{}); err != nil {
			return nil, err
		}
		var errExpand error
		flows[0], scopes, templates, plans, services, errExpand = d.expandProcessors(flows[0])
		if errExpand != nil {
			return nil, errExpand
		}
		stores = d.storeBindings()
	}
	processor := func(n *flowdef.Node) (stepdef.Processor, error) {
		conn := n.Bindings[stepdef.ConnectionBinding]
		n.Bindings = stepdef.Params{mapping.FunctionsBindingKey: scopes[n.ID], "dif.internal.flow": n.Flow, stepdef.StoresBinding: stores}
		if conn != nil {
			n.Bindings[stepdef.ConnectionBinding] = conn
		}
		if info, ok := templates[n.ID]; ok {
			n.Bindings["dif.internal.actionTemplate"] = true
			n.Bindings["dif.internal.processorInfo"] = info
			if info.stores != nil {
				n.Bindings[stepdef.StoresBinding] = info.stores // a reusable processor refers to the stores of its document
			}
		}
		if name, ok := strings.CutPrefix(n.URI, "store:"); ok && n.Kind != stepdef.Source {
			if _, declared := n.Bindings[stepdef.StoresBinding].(map[string]stepdef.Store)[name]; !declared {
				return nil, fmt.Errorf("store %q not found in dil.core.stores", name)
			}
		}
		if name, ok := strings.CutPrefix(n.URI, "mapper:"); ok {
			plan := plans[name]
			if plan == nil {
				return nil, fmt.Errorf("map %q not found", name)
			}
			if n.Kind != stepdef.Action {
				return nil, fmt.Errorf("mapper must be an action")
			}
			n.URI = "mapper"
			n.Bindings[mapping.BindingKey] = plan
		} else if n.URI == "mapper" {
			return nil, fmt.Errorf("mapper requires a named core map")
		} else if name, ok := strings.CutPrefix(n.URI, "service:"); ok {
			sb := services[name]
			if sb == nil {
				return nil, fmt.Errorf("service %q not found", name)
			}
			if n.Kind == stepdef.Source {
				n.Flow.Contract = newFlowContract(sb)
				n.Bindings[FlowServiceBindingKey] = sb
			} else if err := checkServiceCallerStep(n.Kind, n.Options, nil, sb); err != nil {
				return nil, err
			}
			n.URI = "service"
			n.Bindings[mapping.ServiceBindingKey] = sb
		} else if n.URI == "service" {
			return nil, fmt.Errorf("service requires a named core service")
		}
		if n.Kind == stepdef.Source && flows[0].Options.Service != "" {
			optSB := services[flows[0].Options.Service]
			if optSB == nil {
				return nil, fmt.Errorf("options.service: service %q not found", flows[0].Options.Service)
			}
			if n.URI == "service" {
				existingSB := n.Bindings[mapping.ServiceBindingKey].(*mapping.ServiceBinding)
				if existingSB.ID != optSB.ID {
					return nil, fmt.Errorf("options.service %q conflicts with service source %q", flows[0].Options.Service, existingSB.Name)
				}
			} else {
				if err := checkSourceServiceMEP(n, optSB.Plan.ExchangePattern()); err != nil {
					return nil, fmt.Errorf("options.service %q: %w", flows[0].Options.Service, err)
				}
				n.Flow.Contract = newFlowContract(optSB)
				n.Bindings[FlowServiceBindingKey] = optSB
			}
		}
		p, err := newProcessor(n)
		if info, ok := templates[n.ID]; ok && err != nil {
			return nil, fmt.Errorf("processor %q (%s): %w", info.Name, info.Origin, err)
		}
		return p, err
	}

	core := coreRefs{messages: map[string]dilMessage{}, resources: map[string]string{}, connections: map[string]dilConnection{}}
	for _, m := range doc.DIL.Core.Messages.Message {
		core.messages[m.Name] = m
	}
	for _, r := range doc.DIL.Core.Resources.Resource {
		core.resources[r.Name] = r.Content
	}
	for _, c := range doc.DIL.Core.Connections.Connection {
		core.connections[c.ID] = c
	}

	f, err := buildResolved(flows[0], core, processor, templates)
	if err != nil {
		return nil, fmt.Errorf("flow %s: %w", flows[0].ID, err)
	}

	input, err := selectInput(flows[0], doc, core)
	if err != nil {
		return nil, err
	}
	f.Input = input
	return f, nil
}

// coreRefs are what steps can refer to in dil.core: messages and resources, by
// name, and connections, by id.
type coreRefs struct {
	messages    map[string]dilMessage
	resources   map[string]string
	connections map[string]dilConnection
}

func build(df dilFlow, core coreRefs, newProcessor func(*flowdef.Node) (stepdef.Processor, error)) (*flowdef.Flow, error) {
	return buildResolved(df, core, newProcessor, nil)
}
func buildResolved(df dilFlow, core coreRefs, newProcessor func(*flowdef.Node) (stepdef.Processor, error), resolved map[string]ProcessorInfo) (*flowdef.Flow, error) {
	var (
		source   *flowdef.Node
		nodes    []*flowdef.Node
		byInLink = map[string]*flowdef.Node{}         // inbound link id -> node
		outLinks = map[*flowdef.Node][]dilLink{}      // node -> outbound links
		errh     *flowdef.ErrorHandler                // the error step that takes every error
		matched  []*flowdef.ErrorHandler              // the error steps that take particular errors
		errLinks = map[*flowdef.ErrorHandler]string{} // outbound link of an error step: the start of its error route
		flow     = &flowdef.Flow{ID: df.ID, Name: df.Name, Version: versionText(df.Options.Version),
			Tenant: df.Options.Tenant, Environment: df.Options.Environment}
	)

	for _, s := range df.Steps.Step {
		switch s.Type {
		case flowdef.Source, flowdef.Action, flowdef.Router, flowdef.Sink:
		case "error":
			h, link, err := errorHandler(s)
			if err != nil {
				return nil, fmt.Errorf("step %s: %w", s.ID, err)
			}
			switch {
			case h.Match != nil:
				matched = append(matched, h)
			case errh != nil:
				return nil, fmt.Errorf("step %s: flow has more than one error step that takes every error", s.ID)
			default:
				errh = h
			}
			errLinks[h] = link
			continue
		default:
			return nil, fmt.Errorf("step %s: unknown step type %q", s.ID, s.Type)
		}

		opts, uri := s.Options, s.URI
		var conn *stepdef.Connection
		var err error
		if info, prepared := resolved[s.ID]; prepared {
			conn = info.connection
		} else {
			opts, uri, err = resolveRefs(s, core)
			if err == nil {
				opts, conn, err = resolveConnection(uri, opts, core)
			}
		}
		if err != nil {
			return nil, fmt.Errorf("step %s: %w", s.ID, err)
		}
		if opts == nil {
			opts = map[string]any{}
		}
		n := &flowdef.Node{ID: s.ID, Kind: s.Type, URI: uri, Options: opts, Flow: flow}
		if conn != nil {
			n.Bindings = stepdef.Params{stepdef.ConnectionBinding: conn}
		}
		if n.Kind == flowdef.Source && (n.URI == "flowlink" || n.URI == "flowlink-async") && opts["flowId"] == nil {
			// A flow link source listens for its own flow, which DIL leaves out.
			n.Options = maps.Clone(opts)
			n.Options["flowId"] = df.ID
		}
		if n.Kind == flowdef.Source && s.URI == "queue" && opts["path"] == nil {
			// A queue source with no queue name listens on the queue named
			// after its flow, as queue actions address it.
			n.Options = maps.Clone(opts)
			n.Options["path"] = df.ID
		}

		var ins []string
		var outs []dilLink
		for _, l := range s.Links.Link {
			switch l.Bound {
			case "in":
				ins = append(ins, l.ID)
			case "out":
				outs = append(outs, l)
			default:
				return nil, fmt.Errorf("step %s: link %s has invalid bound %q", s.ID, l.ID, l.Bound)
			}
		}

		wantIn, wantOut := 1, 1
		switch s.Type {
		case flowdef.Source:
			wantIn = 0
		case flowdef.Sink:
			wantOut = 0
		case flowdef.Router:
			if len(outs) > 0 {
				wantOut = len(outs) // a router has one or more outbound links
			}
		}
		if len(ins) != wantIn || len(outs) != wantOut {
			return nil, fmt.Errorf("step %s: %s step needs %d inbound and %d outbound links, has %d and %d",
				s.ID, s.Type, wantIn, wantOut, len(ins), len(outs))
		}

		if s.Type == flowdef.Source {
			if source != nil {
				return nil, fmt.Errorf("step %s: flow has more than one source", s.ID)
			}
			source = n
		}
		for _, id := range ins {
			if _, dup := byInLink[id]; dup {
				return nil, fmt.Errorf("step %s: inbound link %s is used by more than one step", s.ID, id)
			}
			byInLink[id] = n
		}
		nodes = append(nodes, n)
		outLinks[n] = outs
	}

	if source == nil {
		return nil, fmt.Errorf("flow has no source step")
	}

	for _, n := range nodes {
		for _, l := range outLinks[n] {
			target, ok := byInLink[l.ID]
			if !ok {
				return nil, fmt.Errorf("step %s: outbound link %s has no target", n.ID, l.ID)
			}
			n.Next = append(n.Next, target)
			n.Links = append(n.Links, stepdef.Link{Rule: l.Rule, Language: l.Language, Expression: l.Expression})
		}
	}

	starts := []*flowdef.Node{source}
	for _, h := range append(append([]*flowdef.ErrorHandler(nil), matched...), errh) {
		if h == nil || errLinks[h] == "" {
			continue
		}
		route, ok := byInLink[errLinks[h]]
		if !ok {
			return nil, fmt.Errorf("step %s: outbound link %s has no target", h.ID, errLinks[h])
		}
		h.Route = route
		starts = append(starts, route)
	}

	// Walk every path from the source and the error step so the engine never
	// sees a cycle or a dangling step. Every step has one inbound link, so the
	// paths form trees.
	seen := map[*flowdef.Node]bool{}
	for todo := starts; len(todo) > 0; {
		n := todo[len(todo)-1]
		todo = todo[:len(todo)-1]
		if seen[n] {
			return nil, fmt.Errorf("step %s: flow contains a cycle", n.ID)
		}
		seen[n] = true
		todo = append(todo, n.Next...)
	}
	if len(seen) != len(nodes) {
		return nil, fmt.Errorf("flow has %d steps not reachable from the source", len(nodes)-len(seen))
	}

	for _, n := range nodes {
		p, err := newProcessor(n)
		if err != nil {
			return nil, fmt.Errorf("step %s: %w", n.ID, err)
		}
		n.Processor = p
	}

	flow.Source, flow.Error, flow.Handlers = source, errh, matched
	return flow, nil
}

// versionText is the version of a flow as text: DIL writes it as a number.
func versionText(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case string:
		return x
	}
	return fmt.Sprint(v)
}

// unknownSteps names the steps that DIL exports with the URI "unknown", by
// what only that step has in its type and options.
var unknownSteps = []struct {
	uri   string
	match func(dilStep) bool
}{
	{"deadletter", hasOption("", "deadLetterQueue")},
	{"flowlink", hasOption("", "targetFlowId")},
	{"flowlink", hasOption(flowdef.Source, "transport")},
	{"flv", hasRule("subcollection")},
	{"exceltoxml", hasRule("worksheet")},
	// formtoxml has no options at all, so any other option-less action is taken for it.
	{"formtoxml", func(s dilStep) bool { return s.Type == flowdef.Action && len(s.Options) == 0 }},
}

// hasOption matches a step of the type (any if empty) with the option.
func hasOption(kind, option string) func(dilStep) bool {
	return func(s dilStep) bool {
		_, ok := s.Options[option]
		return ok && (kind == "" || kind == s.Type)
	}
}

// hasRule matches a step whose option rules is a list with a rule that has the key.
func hasRule(key string) func(dilStep) bool {
	return func(s dilStep) bool {
		rules, _ := s.Options["rules"].([]any)
		for _, r := range rules {
			if rule, _ := r.(map[string]any); rule[key] != nil {
				return true
			}
		}
		return false
	}
}

// knownURI returns the step's URI; for "unknown", the URI of the step its
// type and options identify, if any.
func knownURI(s dilStep) string {
	if s.URI == "unknown" {
		for _, u := range unknownSteps {
			if u.match(s) {
				return u.uri
			}
		}
	}
	return s.URI
}

// jsonOptionSteps are the steps whose option rules is a list of objects. The
// steps' schemas only have scalar options, so they get the list as JSON text.
var jsonOptionSteps = map[string]bool{"flv": true, "exceltoxml": true}

// stepOptions returns the options and URI of a step that refers to nothing in
// dil.core.
func stepOptions(s dilStep) (map[string]any, string, error) {
	uri := knownURI(s)
	scheme, _, _ := strings.Cut(uri, ":")
	if scheme == "settenantvariable" {
		return decodedValue(s.Options), uri, nil
	}
	if !jsonOptionSteps[scheme] {
		return s.Options, uri, nil
	}
	rules, ok := s.Options["rules"]
	if _, isText := rules.(string); !ok || isText {
		return s.Options, uri, nil
	}
	data, err := json.Marshal(rules)
	if err != nil {
		return nil, "", err
	}
	opts := maps.Clone(s.Options)
	opts["rules"] = string(data)
	return opts, uri, nil
}

// decodedValue returns the options of a settenantvariable step with its value
// as text. DIL carries that value in base64, such as dGVzdA== for test. A value
// that is no base64, or does not decode to text, is already the text itself.
func decodedValue(opts map[string]any) map[string]any {
	v, ok := opts["value"].(string)
	if !ok || v == "" {
		return opts
	}
	raw, err := base64.StdEncoding.DecodeString(v)
	if err != nil || !utf8.Valid(raw) {
		return opts
	}
	for _, r := range string(raw) {
		if r < ' ' && r != '\n' && r != '\r' && r != '\t' {
			return opts
		}
	}
	out := maps.Clone(opts)
	out["value"] = string(raw)
	return out
}

// nonNegativeInt returns v, a JSON number or (as DIL converted from XML has
// it) a string, as an int of 0 or more.
func nonNegativeInt(v any) (int, error) {
	var n float64
	switch x := v.(type) {
	case float64:
		n = x
	case string:
		var err error
		if n, err = strconv.ParseFloat(x, 64); err != nil {
			return 0, fmt.Errorf("want an integer, got %q", x)
		}
	default:
		return 0, fmt.Errorf("want an integer, got %v", v)
	}
	if n < 0 || n != float64(int(n)) {
		return 0, fmt.Errorf("want an integer of 0 or more, got %v", v)
	}
	return int(n), nil
}

// resolveRefs returns the step's options and URI, resolving what the URI refers
// to in dil.core, so the processor needs no knowledge of DIL:
//
//   - <scheme>:message:<name> (such as setheaders) adds the option "headers":
//     that message's headers as a JSON array of {name, value, language}
//   - <scheme>:ref:<name> (such as jsonvalidator) adds the option "resource":
//     that resource's content; the URI becomes <scheme>
func resolveRefs(s dilStep, core coreRefs) (map[string]any, string, error) {
	scheme, rest, _ := strings.Cut(s.URI, ":")
	// Mapper and service steps own their full name suffix, including imported
	// namespaces named "ref" or "message". Those are not legacy resource/message URIs.
	if scheme == "mapper" || scheme == "service" || scheme == "store" {
		return s.Options, s.URI, nil
	}
	if name, ok := strings.CutPrefix(rest, "ref:"); ok {
		content, ok := core.resources[name]
		if !ok {
			return nil, "", fmt.Errorf("resource %q not found in dil.core.resources", name)
		}
		opts := make(map[string]any, len(s.Options)+1)
		maps.Copy(opts, s.Options)
		opts["resource"] = content
		return opts, scheme, nil
	}
	name, ok := strings.CutPrefix(rest, "message:")
	if !ok {
		return stepOptions(s)
	}
	m, ok := core.messages[name]
	if !ok {
		return nil, "", fmt.Errorf("message %q not found in dil.core.messages", name)
	}

	headers := make([]map[string]string, 0, len(m.Headers.Header))
	for _, h := range m.Headers.Header {
		headers = append(headers, map[string]string{"name": h.Name, "value": h.Value, "language": h.Language})
	}
	data, err := json.Marshal(headers)
	if err != nil {
		return nil, "", err
	}
	opts := make(map[string]any, len(s.Options)+1)
	maps.Copy(opts, s.Options)
	opts["headers"] = string(data)
	return opts, s.URI, nil
}
