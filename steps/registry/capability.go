package registry

import (
	flowdef "dif/flows/definition"
	stepdef "dif/steps/definition"
)

// Capabilities returns the outside resources the step of node n can touch, as
// its definition's Capabilities says from the node's validated options (with
// the typed connection it refers to merged in). declared is false if the step
// declares none, is not registered or its options are not valid. It constructs
// no processor.
func (r *Registry) Capabilities(n *flowdef.Node) (caps []stepdef.Capability, declared bool) {
	e, params, err := r.parametersWithValues(n, false)
	if err != nil || e.def.Capabilities == nil {
		return nil, false
	}
	return e.def.Capabilities(params), true
}
