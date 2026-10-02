package impl

import (
	"encoding/json"
	"fmt"
	"maps"
	"strconv"
	"strings"
	"time"

	flowdef "dif/flows/definition"
	"dif/message"
	stepdef "dif/steps/definition"
)

// Parse converts a DIL JSON document holding exactly one flow into the flow model.
// newProcessor creates the processor for each node.
func Parse(data []byte, newProcessor func(*flowdef.Node) (stepdef.Processor, error)) (*flowdef.Flow, error) {
	var doc dilDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse dil: %w", err)
	}

	var flows []dilFlow
	for _, in := range doc.DIL.Integrations.Integration {
		flows = append(flows, in.Flows.Flow...)
	}
	if len(flows) != 1 {
		return nil, fmt.Errorf("expected exactly one flow, found %d", len(flows))
	}

	messages := map[string]dilMessage{}
	for _, m := range doc.DIL.Core.Messages.Message {
		messages[m.Name] = m
	}

	f, err := build(flows[0], messages, newProcessor)
	if err != nil {
		return nil, fmt.Errorf("flow %s: %w", flows[0].ID, err)
	}

	if msgs := doc.DIL.Core.Messages.Message; len(msgs) > 0 {
		f.Input = message.Message{}
		for _, h := range msgs[0].Headers.Header {
			f.Input[h.Name] = h.Value
		}
		if msgs[0].Body != nil {
			f.Input[message.Body] = msgs[0].Body
		}
	}
	return f, nil
}

func build(df dilFlow, messages map[string]dilMessage, newProcessor func(*flowdef.Node) (stepdef.Processor, error)) (*flowdef.Flow, error) {
	var (
		source   *flowdef.Node
		nodes    []*flowdef.Node
		byInLink = map[string]*flowdef.Node{}    // inbound link id -> node
		outLinks = map[*flowdef.Node][]dilLink{} // node -> outbound links
		errh     *flowdef.ErrorHandler
		errLink  string // outbound link of the error step: the start of the error route
	)

	for _, s := range df.Steps.Step {
		switch s.Type {
		case flowdef.Source, flowdef.Action, flowdef.Router, flowdef.Sink:
		case "error":
			if errh != nil {
				return nil, fmt.Errorf("step %s: flow has more than one error step", s.ID)
			}
			var err error
			if errh, errLink, err = errorHandler(s); err != nil {
				return nil, fmt.Errorf("step %s: %w", s.ID, err)
			}
			continue
		default:
			return nil, fmt.Errorf("step %s: unknown step type %q", s.ID, s.Type)
		}

		opts, err := resolveMessage(s, messages)
		if err != nil {
			return nil, fmt.Errorf("step %s: %w", s.ID, err)
		}
		n := &flowdef.Node{ID: s.ID, Kind: s.Type, URI: s.URI, Options: opts}

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
	if errLink != "" {
		route, ok := byInLink[errLink]
		if !ok {
			return nil, fmt.Errorf("step %s: outbound link %s has no target", errh.ID, errLink)
		}
		errh.Route = route
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

	return &flowdef.Flow{ID: df.ID, Name: df.Name, Source: source, Error: errh}, nil
}

// errorHandler returns the error handler an error step defines, and the id of
// its outbound link (empty if it has no error route). Its options are Camel's
// redelivery settings: maximumRedeliveries (or redeliveryAttempts) and
// redeliveryDelay (or redeliveryInterval) in milliseconds, default 1000.
func errorHandler(s dilStep) (*flowdef.ErrorHandler, string, error) {
	if s.URI != "failedexchange" {
		return nil, "", fmt.Errorf("error step uri %q is not supported; use failedexchange", s.URI)
	}
	var out []string
	for _, l := range s.Links.Link {
		if l.Bound != "out" {
			return nil, "", fmt.Errorf("error step needs 0 inbound links and at most 1 outbound link")
		}
		out = append(out, l.ID)
	}
	if len(out) > 1 {
		return nil, "", fmt.Errorf("error step needs 0 inbound links and at most 1 outbound link")
	}

	opts := map[string]int{"maximumRedeliveries": -1, "redeliveryAttempts": -1, "redeliveryDelay": -1, "redeliveryInterval": -1}
	for k, v := range s.Options {
		if _, ok := opts[k]; !ok {
			return nil, "", fmt.Errorf("error step: unknown option %s", k)
		}
		n, err := nonNegativeInt(v)
		if err != nil {
			return nil, "", fmt.Errorf("error step: option %s: %w", k, err)
		}
		opts[k] = n
	}
	pick := func(name, alias string, def int) int {
		if opts[name] >= 0 {
			return opts[name]
		}
		if opts[alias] >= 0 {
			return opts[alias]
		}
		return def
	}

	h := &flowdef.ErrorHandler{
		ID:              s.ID,
		Redeliveries:    pick("maximumRedeliveries", "redeliveryAttempts", 0),
		RedeliveryDelay: time.Duration(pick("redeliveryDelay", "redeliveryInterval", 1000)) * time.Millisecond,
	}
	if len(out) == 0 {
		return h, "", nil
	}
	return h, out[0], nil
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

// resolveMessage returns the step's options. A step whose URI refers to a core
// message, <scheme>:message:<name> (such as setheaders), also gets the
// option "headers": that message's headers as a JSON array of
// {name, value, language}. The processor then needs no knowledge of DIL.
func resolveMessage(s dilStep, messages map[string]dilMessage) (map[string]any, error) {
	_, rest, _ := strings.Cut(s.URI, ":")
	name, ok := strings.CutPrefix(rest, "message:")
	if !ok {
		return s.Options, nil
	}
	m, ok := messages[name]
	if !ok {
		return nil, fmt.Errorf("message %q not found in dil.core.messages", name)
	}

	headers := make([]map[string]string, 0, len(m.Headers.Header))
	for _, h := range m.Headers.Header {
		headers = append(headers, map[string]string{"name": h.Name, "value": h.Value, "language": h.Language})
	}
	data, err := json.Marshal(headers)
	if err != nil {
		return nil, err
	}
	opts := make(map[string]any, len(s.Options)+1)
	maps.Copy(opts, s.Options)
	opts["headers"] = string(data)
	return opts, nil
}
