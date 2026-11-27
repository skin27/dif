package registry

import (
	flowdef "dif/flows/definition"
	stepdef "dif/steps/definition"
)

// Effect returns what the step of node n does to a message, as its definition's
// Effect says from the node's validated options; nil if the step has no Effect,
// is not registered or its options are not valid. It constructs no processor.
func (r *Registry) Effect(n *flowdef.Node) *stepdef.Effect {
	e, params, err := r.parametersWithValues(n, false)
	if err != nil || e.def.Effect == nil {
		return nil
	}
	return e.def.Effect(params)
}
