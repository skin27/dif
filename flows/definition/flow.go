// Package definition is the internal flow model the engine executes.
// It is independent of DIL or any other DSL.
package definition

import (
	"dif/message"
	stepdef "dif/steps/definition"
)

// Step kinds.
const (
	Source = stepdef.Source // one outbound link
	Action = stepdef.Action // one inbound, one outbound link
	Router = stepdef.Router // one inbound, one or more outbound links
	Sink   = stepdef.Sink   // one inbound link
)

// Node is a step in a flow together with its outbound links.
type Node struct {
	ID        string
	Kind      string
	URI       string
	Options   map[string]any
	Next      []*Node           // targets of the outbound links
	Links     []stepdef.Link    // the outbound links' rules and conditions, parallel to Next; nil if they have none
	Processor stepdef.Processor // the step's processor
}

// Flow is a graph of nodes starting at Source.
type Flow struct {
	ID     string
	Name   string
	Source *Node
	Input  message.Message // headers and body of the configured message; nil if there is none
}
