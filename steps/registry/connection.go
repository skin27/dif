package registry

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	flowdef "dif/flows/definition"
	stepdef "dif/steps/definition"
)

// applyConnection merges the connection a loader bound to the node into opts,
// the node's options before validation, as the step's definition maps it. An
// option the step sets itself wins. With a mapped address, the connection's
// address is put in front of the step's own value of that option.
//
// A connection the step cannot take, or with a field the step has no option
// for, is an error: nothing a connection sets is silently ignored. The errors
// name the connection and the field, never a value.
func applyConnection(def stepdef.Definition, n *flowdef.Node, opts map[string]any) error {
	c, _ := n.Bindings[stepdef.ConnectionBinding].(*stepdef.Connection)
	if c == nil {
		return nil
	}
	m := def.Connection
	if m == nil {
		return fmt.Errorf("connection %q: the step takes no connection", c.Name)
	}
	if !slices.Contains(m.Kinds, c.Kind) {
		return fmt.Errorf("connection %q is %s; the step takes %s", c.Name, c.Kind, strings.Join(m.Kinds, " or "))
	}
	for _, field := range slices.Sorted(maps.Keys(c.Fields)) {
		if m.Address != "" && slices.Contains(stepdef.AddressFields, field) {
			continue
		}
		option, ok := m.Options[field]
		if !ok {
			return fmt.Errorf("connection %q: the step does not take %s", c.Name, field)
		}
		if _, set := opts[option]; option != "" && !set {
			opts[option] = c.Fields[field]
		}
		if v := c.Fields[field]; v == false || v == "false" {
			continue
		}
		for k, v := range m.Implied[field] {
			if _, set := opts[k]; !set {
				opts[k] = v
			}
		}
	}
	if address := c.Address(); m.Address != "" && address != "" {
		own, _ := opts[m.Address].(string)
		if own = strings.TrimLeft(own, "/"); own != "" {
			address += "/" + own
		}
		opts[m.Address] = address
	}
	return nil
}
