// Package definition is the internal flow model the engine executes.
// It is independent of DIL or any other DSL.
package definition

import (
	"time"

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
	Flow      *Flow             // the flow the step is in
}

// Flow is a graph of nodes starting at Source.
type Flow struct {
	ID          string
	Name        string
	Version     string // the version of the flow; "" if it has none
	Tenant      string // the tenant the flow belongs to; "" if it has none
	Environment string // the environment it runs in, such as test; "" if it has none
	Source      *Node
	Input       message.Message // headers and body of the configured message; nil if there is none
	Error       *ErrorHandler   // what to do when a step fails; nil: the message fails
}

// ErrorHandler is what a flow does when a step fails: try the step again,
// and if it keeps failing, send the message along the error route.
type ErrorHandler struct {
	ID              string        // the error step, for the trail
	Redeliveries    int           // times a failing step is tried again
	RedeliveryDelay time.Duration // wait before each new try
	Route           *Node         // first step of the error route; nil if there is none
}
