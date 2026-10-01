package impl

import (
	"encoding/json"
	"fmt"

	flowdef "dif/flows/definition"
	stepdef "dif/steps/definition"
)

// Parse converts a DIL JSON document holding exactly one flow into the flow model.
// newStep creates the executable step for each node.
func Parse(data []byte, newStep func(*flowdef.Node) (stepdef.Step, error)) (*flowdef.Flow, error) {
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

	f, err := build(flows[0], newStep)
	if err != nil {
		return nil, fmt.Errorf("flow %s: %w", flows[0].ID, err)
	}

	if msgs := doc.DIL.Core.Messages.Message; len(msgs) > 0 {
		f.Input.Body = msgs[0].Body
		f.Input.Headers = map[string]string{}
		for _, h := range msgs[0].Headers.Header {
			f.Input.Headers[h.Name] = h.Value
		}
	}
	return f, nil
}

func build(df dilFlow, newStep func(*flowdef.Node) (stepdef.Step, error)) (*flowdef.Flow, error) {
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

		n := &flowdef.Node{ID: s.ID, Kind: s.Type, URI: s.URI, Options: s.Options}

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
		step, err := newStep(n)
		if err != nil {
			return nil, fmt.Errorf("step %s: %w", n.ID, err)
		}
		n.Step = step
	}

	return &flowdef.Flow{ID: df.ID, Name: df.Name, Source: source}, nil
}
