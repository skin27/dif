package api

import (
	"fmt"
	"os"
	"sort"
	"strings"

	flowdef "dif/flows/definition"
	flowimpl "dif/flows/impl"
	stepdef "dif/steps/definition"
)

// FlowInfo describes a statically validated flow without exposing option values
// or URI paths, which may contain credentials. Steps include the error route.
type FlowInfo struct {
	ID            string              `json:"id"`
	Name          string              `json:"name"`
	Source        string              `json:"source"`
	Steps         []NodeInfo          `json:"steps"`
	Error         *ErrorInfo          `json:"error,omitempty"`
	Configuration []ConfigurationInfo `json:"configuration"`
}

type NodeInfo struct {
	ID    string     `json:"id"`
	Kind  string     `json:"kind"`
	Step  string     `json:"step"`
	Links []LinkInfo `json:"links"`
}

type LinkInfo struct {
	Target   string `json:"target"`
	Rule     string `json:"rule,omitempty"`
	Language string `json:"language,omitempty"`
}

type ErrorInfo struct {
	ID           string `json:"id"`
	Redeliveries int    `json:"redeliveries"`
	DelayMillis  int64  `json:"delayMillis"`
	Target       string `json:"target,omitempty"`
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
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return InspectBytes(data)
}

// InspectBytes statically validates a previously resolved DIL document.
func InspectBytes(data []byte) (*FlowInfo, error) {
	f, err := flowimpl.Parse(data, func(n *flowdef.Node) (stepdef.Processor, error) {
		return nil, steps.Validate(n)
	})
	if err != nil {
		return nil, err
	}
	if f.ID == "" {
		return nil, fmt.Errorf("flow has no id")
	}
	info := &FlowInfo{ID: f.ID, Name: f.Name, Source: f.Source.ID, Steps: []NodeInfo{}, Configuration: []ConfigurationInfo{}}
	ids := map[string]bool{}
	if f.Error != nil {
		e := f.Error
		info.Error = &ErrorInfo{ID: e.ID, Redeliveries: e.Redeliveries, DelayMillis: e.RedeliveryDelay.Milliseconds()}
		if e.ID == "" {
			return nil, fmt.Errorf("error step has no id")
		}
		ids[e.ID] = true
		if e.Route != nil {
			info.Error.Target = e.Route.ID
		}
	}
	var visit func(*flowdef.Node) error
	visit = func(n *flowdef.Node) error {
		if n.ID == "" || ids[n.ID] {
			return fmt.Errorf("step id %q is empty or duplicated", n.ID)
		}
		ids[n.ID] = true
		name, _, _ := strings.Cut(n.URI, ":")
		node := NodeInfo{ID: n.ID, Kind: n.Kind, Step: name, Links: []LinkInfo{}}
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
	if f.Error != nil && f.Error.Route != nil {
		if err := visit(f.Error.Route); err != nil {
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
