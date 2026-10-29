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
	Error       *ErrorHandler   // what to do when a step fails and no handler in Handlers matches; nil: the message fails
	Handlers    []*ErrorHandler // handlers for particular failures, tried in order before Error; each has a Match
}

// ErrorHandlers returns every error handler of the flow in the order they are
// tried: those with a Match, then the default one.
func (f *Flow) ErrorHandlers() []*ErrorHandler {
	hs := append([]*ErrorHandler(nil), f.Handlers...)
	if f.Error != nil {
		hs = append(hs, f.Error)
	}
	return hs
}

// ErrorHandler is what a flow does when a step fails: try the step again,
// and if it keeps failing, send the message along the error route.
type ErrorHandler struct {
	ID              string        // the error step, for the trail
	Redeliveries    int           // times a failing step is tried again
	RedeliveryDelay time.Duration // wait before each new try
	Route           *Node         // first step of the error route; nil if there is none

	// Match tells whether the handler takes a failure; nil: every failure.
	Match   func(Failure) bool
	Matches []string // what Match looks at, such as errorClass, for description only

	// Unhandled: after the error route the message still fails, so the sender
	// gets the failure. By default the error route's outcome is the message's.
	Unhandled bool

	// Backoff multiplies the delay of each new try (1 or less: the delay stays),
	// and MaxDelay, if positive, is the longest delay.
	Backoff  float64
	MaxDelay time.Duration
}

// Failure describes a failed step, for an ErrorHandler to match.
type Failure struct {
	Step    string // id of the step that failed
	URI     string // the step's URI
	Class   string // the type of the error at the bottom of the chain, such as *fs.PathError
	Message string // the error text
}
