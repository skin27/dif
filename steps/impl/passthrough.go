// Package impl contains the concrete steps.
package impl

import (
	flowdef "dif/flows/definition"
	"dif/message"
	stepdef "dif/steps/definition"
)

// Passthrough returns the message unchanged.
type Passthrough struct{}

func (Passthrough) Execute(m *message.Message) (*message.Message, error) { return m, nil }

// New creates the step for a node. Every node is a Passthrough for now;
// mapping node URIs to concrete steps belongs here, not in the engine.
func New(*flowdef.Node) (stepdef.Step, error) {
	return Passthrough{}, nil
}
