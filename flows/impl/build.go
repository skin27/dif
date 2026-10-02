package impl

import (
	"encoding/json"
	"fmt"
	"maps"
	"strings"

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
		byInLink = map[string]*flowdef.Node{}   // inbound link id -> node
		outLinks = map[*flowdef.Node][]string{} // node -> outbound link ids
	)

	for _, s := range df.Steps.Step {
		switch s.Type {
		case flowdef.Source, flowdef.Action, flowdef.Sink:
		case "error":
			continue // error handlers are not supported yet
		case flowdef.Router:
			return nil, fmt.Errorf("step %s: router steps are not supported yet", s.ID)
		default:
			return nil, fmt.Errorf("step %s: unknown step type %q", s.ID, s.Type)
		}

		opts, err := resolveMessage(s, messages)
		if err != nil {
			return nil, fmt.Errorf("step %s: %w", s.ID, err)
		}
		n := &flowdef.Node{ID: s.ID, Kind: s.Type, URI: s.URI, Options: opts}

		var ins, outs []string
		for _, l := range s.Links.Link {
			switch l.Bound {
			case "in":
				ins = append(ins, l.ID)
			case "out":
				outs = append(outs, l.ID)
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
		for _, id := range outLinks[n] {
			target, ok := byInLink[id]
			if !ok {
				return nil, fmt.Errorf("step %s: outbound link %s has no target", n.ID, id)
			}
			n.Next = append(n.Next, target)
		}
	}

	// Walk from source to sink so the engine never sees a cycle or a dangling step.
	seen := map[*flowdef.Node]bool{}
	for n := source; ; n = n.Next[0] {
		if seen[n] {
			return nil, fmt.Errorf("step %s: flow contains a cycle", n.ID)
		}
		seen[n] = true
		if len(n.Next) == 0 {
			break
		}
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

	return &flowdef.Flow{ID: df.ID, Name: df.Name, Source: source}, nil
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
