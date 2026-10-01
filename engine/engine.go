// Package engine executes a flow model. It knows only the Step interface,
// never concrete steps or the DSL the flow was defined in.
package engine

import (
	"fmt"
	"time"

	flowdef "dif/flows/definition"
	"dif/message"
)

// Result is the outcome of a flow run.
type Result struct {
	Message  *message.Message
	Trail    []string // "kind:id" of every executed step, in order
	Duration time.Duration
}

// Run passes msg from the flow's source along its links to the sink.
// Processing stops at the first step that returns an error.
func Run(f *flowdef.Flow, msg *message.Message) (*Result, error) {
	start := time.Now()
	var trail []string

	for n := f.Source; n != nil; {
		out, err := n.Step.Execute(msg)
		if err != nil {
			return nil, fmt.Errorf("step %s: %w", n.ID, err)
		}
		if out == nil {
			return nil, fmt.Errorf("step %s: returned no message", n.ID)
		}
		msg = out
		trail = append(trail, n.Kind+":"+n.ID)

		switch len(n.Next) {
		case 0:
			n = nil
		case 1:
			n = n.Next[0]
		default:
			return nil, fmt.Errorf("step %s: routing to multiple links is not supported yet", n.ID)
		}
	}

	return &Result{Message: msg, Trail: trail, Duration: time.Since(start)}, nil
}
