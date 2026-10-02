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

// Run passes msg, produced by the flow's source, along its links to the sinks.
// At a router the message follows the routes the router returns, each path to
// its end; see stepdef.RouterProcessor for which message comes out. Processing
// stops at the first step that returns an error or when ctx is done.
func Run(ctx context.Context, f *flowdef.Flow, msg message.Message) (*Result, error) {
	start := time.Now()
	r := run{trail: []string{f.Source.Kind + ":" + f.Source.ID}} // the source produced msg

	n, err := nextStep(f.Source)
	if err != nil {
		return nil, err
	}
	if n != nil {
		if msg, err = r.path(ctx, n, msg); err != nil {
			return nil, err
		}
	}
	return &Result{Message: msg, Trail: r.trail, Duration: time.Since(start)}, nil
}

// run is the state of one message execution.
type run struct {
	trail []string
}

// path passes msg through step n and the steps after it, to the end of the
// path, and returns the message that comes out.
func (r *run) path(ctx context.Context, n *flowdef.Node, msg message.Message) (message.Message, error) {
	for n != nil {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("step %s: %w", n.ID, err)
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
		case stepdef.RouterProcessor:
			routes, err := p.Route(ctx, msg)
			if err != nil {
				return nil, fmt.Errorf("step %s: %w", n.ID, err)
			}
			r.trail = append(r.trail, n.Kind+":"+n.ID)
			return r.route(ctx, n, msg, routes)
		default:
			return nil, fmt.Errorf("step %s: %T is not an action, router or sink processor", n.ID, n.Processor)
		}
		r.trail = append(r.trail, n.Kind+":"+n.ID)

		var err error
		if n, err = nextStep(n); err != nil {
			return nil, err
		}
	}
	return msg, nil
}

// route runs the routes of router n, which msg entered, and returns the
// message that comes out of the last route that is not detached (msg if none).
func (r *run) route(ctx context.Context, n *flowdef.Node, msg message.Message, routes []stepdef.Route) (message.Message, error) {
	out := msg
	for _, rt := range routes {
		if rt.Next < 0 || rt.Next >= len(n.Next) {
			return nil, fmt.Errorf("step %s: route to link %d, but the step has %d", n.ID, rt.Next, len(n.Next))
		}
		if rt.Message == nil {
			return nil, fmt.Errorf("step %s: route to link %d has no message", n.ID, rt.Next)
		}
		res, err := r.path(ctx, n.Next[rt.Next], rt.Message)
		switch {
		case rt.Detached:
			if err != nil {
				stepdef.Logger(ctx).Printf("step %s: detached route to link %d failed: %v", n.ID, rt.Next, err)
			}
		case err != nil:
			return nil, err
		default:
			out = res
		}
	}
	return out, nil
}

// nextStep returns the step after n, or nil if n ends its path. Only a router may
// have more than one outbound link.
func nextStep(n *flowdef.Node) (*flowdef.Node, error) {
	switch len(n.Next) {
	case 0:
		return nil, nil
	case 1:
		return n.Next[0], nil
	}
	return nil, fmt.Errorf("step %s: only a router can have more than one outbound link", n.ID)
}
