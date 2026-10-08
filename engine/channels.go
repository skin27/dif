package engine

import (
	flowdef "dif/flows/definition"
	stepdef "dif/steps/definition"
)

// LocalChannels describes generic in-process dependencies without interpreting
// step names or DIL. A nonempty input identifies an internal consumer.
func (r *Runner) LocalChannels() (input string, outputs []string) {
	if source, ok := r.source.(stepdef.InternalSourceProcessor); ok {
		input = source.LocalChannel()
	}
	seen := map[*flowdef.Node]bool{}
	var visit func(*flowdef.Node)
	visit = func(n *flowdef.Node) {
		if n == nil || seen[n] {
			return
		}
		seen[n] = true
		if p, ok := n.Processor.(stepdef.LocalProducer); ok {
			outputs = append(outputs, p.LocalTargets()...)
		}
		for _, next := range n.Next {
			visit(next)
		}
	}
	visit(r.flow.Source)
	if r.flow.Error != nil {
		visit(r.flow.Error.Route)
	}
	return
}
