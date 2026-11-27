package api

import (
	"fmt"
	"sort"
	"strings"

	flowdef "dif/flows/definition"
	flowimpl "dif/flows/impl"
	"dif/internal/mapping"
	stepdef "dif/steps/definition"
)

// FlowInfo describes a statically validated flow without exposing option values
// or URI paths, which may contain credentials. Steps include the error route.
type FlowInfo struct {
	ID            string              `json:"id"`
	Name          string              `json:"name"`
	Source        string              `json:"source"`
	Service       *ServiceInfo        `json:"service,omitempty"`
	Steps         []NodeInfo          `json:"steps"`
	Error         *ErrorInfo          `json:"error,omitempty"`
	ErrorHandlers []ErrorInfo         `json:"errorHandlers,omitempty"`
	Configuration []ConfigurationInfo `json:"configuration"`
	// Diagnostics are the findings of the static checks of the flow; see Diagnostic.
	Diagnostics []Diagnostic `json:"diagnostics,omitempty"`
}

type NodeInfo struct {
	Processor *flowimpl.ProcessorInfo `json:"processor,omitempty"`
	Mapping   *MapInfo                `json:"mapping,omitempty"`
	Service   *ServiceInfo            `json:"service,omitempty"`
	ID        string                  `json:"id"`
	Kind      string                  `json:"kind"`
	Step      string                  `json:"step"`
	Links     []LinkInfo              `json:"links"`
	// Interceptor is the step an interceptor added; empty for the flow's own steps.
	Interceptor bool `json:"interceptor,omitempty"`
}

type MapInfo = mapping.Info
type ServiceInfo = mapping.ServiceInfo
type HeaderInfo = mapping.HeaderInfo
type FormatInfo = flowimpl.FormatInfo
type MappingLimits = mapping.Limits

func cloneServiceInfo(s ServiceInfo) *ServiceInfo {
	s.InputHeaders = append([]HeaderInfo(nil), s.InputHeaders...)
	s.OutputHeaders = append([]HeaderInfo(nil), s.OutputHeaders...)
	return &s
}

type LinkInfo struct {
	Target   string `json:"target"`
	Rule     string `json:"rule,omitempty"`
	Language string `json:"language,omitempty"`
}

type ErrorInfo struct {
	ID           string   `json:"id"`
	Redeliveries int      `json:"redeliveries"`
	DelayMillis  int64    `json:"delayMillis"`
	Target       string   `json:"target,omitempty"`
	Matches      []string `json:"matches,omitempty"`   // the options that restrict which errors it takes, never their values
	Unhandled    bool     `json:"unhandled,omitempty"` // the message still fails after the error route
}

// ConfigurationInfo identifies required options and optional environment
// fallbacks. It never includes the configured value.
type ConfigurationInfo struct {
	StepID      string `json:"stepId"`
	Option      string `json:"option"`
	Required    bool   `json:"required"`
	Environment string `json:"environment,omitempty"`
}

// Validate checks DIL structure, graph, references and registered option schemas.
// Processor-specific semantics and external resources are checked only by Load.
func Validate(path string) error {
	_, err := Inspect(path)
	return err
}

// Inspect validates a flow without constructing processors or starting sources.
func Inspect(path string) (*FlowInfo, error) {
	data, err := readDocument(path)
	if err != nil {
		return nil, err
	}
	return InspectBytes(data)
}

// InspectBytes statically validates a previously resolved DIL document.
func InspectBytes(data []byte) (*FlowInfo, error) {
	f, err := parseFlow(data, func(n *flowdef.Node) (stepdef.Processor, error) {
		return nil, steps.Validate(n)
	})
	if err != nil {
		return nil, err
	}
	return inspectFlow(f)
}

func inspectFlow(f *flowdef.Flow) (*FlowInfo, error) {
	if f.ID == "" {
		return nil, fmt.Errorf("flow has no id")
	}
	info := &FlowInfo{ID: f.ID, Name: f.Name, Source: f.Source.ID, Steps: []NodeInfo{}, Configuration: []ConfigurationInfo{}}
	if sb, ok := f.Source.Bindings[flowimpl.FlowServiceBindingKey].(*mapping.ServiceBinding); ok && sb != nil {
		info.Service = cloneServiceInfo(sb.Plan.Info())
	}
	ids := map[string]bool{}
	for _, e := range f.ErrorHandlers() {
		if e.ID == "" || ids[e.ID] {
			return nil, fmt.Errorf("error step id %q is empty or duplicated", e.ID)
		}
		ids[e.ID] = true
		ei := ErrorInfo{ID: e.ID, Redeliveries: e.Redeliveries, DelayMillis: e.RedeliveryDelay.Milliseconds(),
			Matches: append([]string(nil), e.Matches...), Unhandled: e.Unhandled}
		if e.Route != nil {
			ei.Target = e.Route.ID
		}
		if e == f.Error {
			info.Error = &ei
		} else {
			info.ErrorHandlers = append(info.ErrorHandlers, ei)
		}
	}
	var visit func(*flowdef.Node) error
	visit = func(n *flowdef.Node) error {
		if n.ID == "" || ids[n.ID] {
			return fmt.Errorf("step id %q is empty or duplicated", n.ID)
		}
		ids[n.ID] = true
		name, _, _ := strings.Cut(n.URI, ":")
		node := NodeInfo{ID: n.ID, Kind: n.Kind, Step: name, Links: []LinkInfo{}, Interceptor: strings.HasPrefix(n.ID, flowimpl.InterceptorPrefix)}
		if info, ok := n.Bindings["dif.internal.processorInfo"].(flowimpl.ProcessorInfo); ok {
			node.Processor = &info
		}
		if p, ok := n.Bindings[mapping.BindingKey].(*mapping.Plan); ok {
			info := p.Info()
			node.Mapping = &info
		}
		if sb, ok := n.Bindings[mapping.ServiceBindingKey].(*mapping.ServiceBinding); ok && sb != nil {
			node.Service = cloneServiceInfo(sb.Plan.Info())
		}
		for i, target := range n.Next {
			link := LinkInfo{Target: target.ID}
			if i < len(n.Links) {
				link.Rule, link.Language = n.Links[i].Rule, n.Links[i].Language
			}
			node.Links = append(node.Links, link)
		}
		info.Steps = append(info.Steps, node)
		entry := stepsForInspection(n)
		for _, option := range entry.Options {
			env := ""
			for _, word := range strings.Fields(option.Description) {
				if strings.HasPrefix(word, "DIF_") {
					env = strings.TrimRight(word, ".,;)")
				}
			}
			if option.Required || env != "" {
				info.Configuration = append(info.Configuration, ConfigurationInfo{StepID: n.ID, Option: option.Name, Required: option.Required, Environment: env})
			}
		}
		for _, next := range n.Next {
			if err := visit(next); err != nil {
				return err
			}
		}
		return nil
	}
	if err := visit(f.Source); err != nil {
		return nil, err
	}
	for _, e := range f.ErrorHandlers() {
		if e.Route == nil {
			continue
		}
		if err := visit(e.Route); err != nil {
			return nil, err
		}
	}
	sort.Slice(info.Configuration, func(i, j int) bool {
		a, b := info.Configuration[i], info.Configuration[j]
		if a.StepID == b.StepID {
			return a.Option < b.Option
		}
		return a.StepID < b.StepID
	})
	info.Diagnostics = checkFlow(f)
	return info, nil
}

// Use the registry's same kind fallback order when describing options.
func stepsForInspection(n *flowdef.Node) StepInfo {
	name, _, _ := strings.Cut(n.URI, ":")
	kinds := []string{n.Kind}
	if n.Kind == stepdef.Action {
		kinds = append(kinds, stepdef.Router, stepdef.Sink)
	}
	if n.Kind == stepdef.Sink {
		kinds = append(kinds, stepdef.Action)
	}
	for _, kind := range kinds {
		for _, s := range StepCatalog() {
			if s.Name == name && s.Kind == kind {
				return s
			}
		}
	}
	return StepInfo{}
}
