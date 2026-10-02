// Package engine executes a flow model. It knows only the processor
// interfaces, never concrete steps or the DSL the flow was defined in.
package engine

import (
	"context"
	"fmt"
	"time"

	flowdef "dif/flows/definition"
	"dif/message"
	stepdef "dif/steps/definition"
)

// Result is the outcome of a flow run.
type Result struct {
	Message  message.Message
	Trail    []string // "kind:id" of every step the message passed, in order
	Duration time.Duration
}

// Run passes msg, produced by the flow's source, along its links to the sink.
// Processing stops at the first step that returns an error or when ctx is done.
func Run(ctx context.Context, f *flowdef.Flow, msg message.Message) (*Result, error) {
	start := time.Now()
	trail := []string{f.Source.Kind + ":" + f.Source.ID} // the source produced msg

	for n := nextNode(f.Source); n != nil; n = nextNode(n) {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("step %s: %w", n.ID, err)
		}
		if len(n.Next) > 1 {
			return nil, fmt.Errorf("step %s: routing to multiple links is not supported yet", n.ID)
		}

		switch p := n.Processor.(type) {
		case stepdef.ActionProcessor:
			out, err := p.Process(ctx, msg)
			if err != nil {
				return nil, fmt.Errorf("step %s: %w", n.ID, err)
			}
			if out == nil {
				return nil, fmt.Errorf("step %s: returned no message", n.ID)
			}
			msg = out
		case stepdef.SinkProcessor:
			if err := p.Consume(ctx, msg); err != nil {
				return nil, fmt.Errorf("step %s: %w", n.ID, err)
			}
		default:
			return nil, fmt.Errorf("step %s: %T is not an action or sink processor", n.ID, n.Processor)
		}
		trail = append(trail, n.Kind+":"+n.ID)
	}

	return &Result{Message: msg, Trail: trail, Duration: time.Since(start)}, nil
}

// nextNode returns the node after n, or nil if n is the last one.
func nextNode(n *flowdef.Node) *flowdef.Node {
	if len(n.Next) == 0 {
		return nil
	}
	return n.Next[0]
}
