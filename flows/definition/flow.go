// Package definition is the internal flow model the engine executes.
// It is independent of DIL or any other DSL.
package definition

import stepdef "dif/steps/definition"

// Step kinds.
const (
	Source = "source" // one outbound link
	Action = "action" // one inbound, one outbound link
	Router = "router" // one inbound, multiple outbound links (not supported yet)
	Sink   = "sink"   // one inbound link
)

// Node is a step in a flow together with its outbound links.
type Node struct {
	ID      string
	Kind    string
	URI     string
	Options map[string]any
	Next    []*Node      // targets of the outbound links
	Step    stepdef.Step // the executable step
}

// Flow is a graph of nodes starting at Source.
type Flow struct {
	ID     string
	Name   string
	Source *Node
	Input  InputMessage
}

// InputMessage is the initial message fed into the source.
type InputMessage struct {
	Body    any
	Headers map[string]string
}
