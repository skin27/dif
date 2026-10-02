// Package engine executes a flow model. It knows only the processor
// interfaces, never concrete steps or the DSL the flow was defined in.
package engine

import (
	"context"
	"errors"
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
	Err      error // the failure the error route handled; nil if no step failed
}

// Headers the engine sets on a message it sends along the error route.
const (
	ErrorMessage = "error.message" // what went wrong
	ErrorStep    = "error.step"    // id of the step that failed
)

// StepError is the failure of a step, with the message the step got.
type StepError struct {
	Step    string
	Message message.Message
	Err     error
}

func (e *StepError) Error() string { return "step " + e.Step + ": " + e.Err.Error() }
func (e *StepError) Unwrap() error { return e.Err }

// Run passes msg, produced by the flow's source, along its links to the sinks.
// At a router the message follows the routes the router returns, each path to
// its end; see stepdef.RouterProcessor for which message comes out.
//
// Processing stops at the first step that fails or when ctx is done. With an
// error handler, a failing step is first tried again; if it keeps failing,
// the message as the step got it, with the ErrorMessage and ErrorStep
// headers, goes along the error route. The message has then not failed: its
// outcome is the error route's, and Result.Err says what was handled.
func Run(ctx context.Context, f *flowdef.Flow, msg message.Message) (*Result, error) {
	start := time.Now()
	r := run{trail: []string{f.Source.Kind + ":" + f.Source.ID}, errh: f.Error} // the source produced msg

	n, err := nextStep(f.Source)
	if err != nil {
		return nil, err
	}
	if n != nil {
		out, err := r.path(ctx, n, msg)
		if err != nil {
			handled, herr := r.handle(ctx, err)
			if herr != nil {
				return nil, herr
			}
			return &Result{Message: handled, Trail: r.trail, Duration: time.Since(start), Err: err}, nil
		}
		msg = out
	}
	return &Result{Message: msg, Trail: r.trail, Duration: time.Since(start)}, nil
}

// run is the state of one message execution.
type run struct {
	trail []string
	errh  *flowdef.ErrorHandler
}

// handle sends the message of the failed step along the error route and
// returns its outcome, or err when there is no error route to take.
func (r *run) handle(ctx context.Context, err error) (message.Message, error) {
	var se *StepError
	if r.errh == nil || r.errh.Route == nil || ctx.Err() != nil || !errors.As(err, &se) {
		return nil, err
	}
	m := se.Message
	m[ErrorMessage], m[ErrorStep] = se.Err.Error(), se.Step
	r.trail = append(r.trail, "error:"+r.errh.ID)
	out, routeErr := r.path(ctx, r.errh.Route, m)
	if routeErr != nil {
		return nil, fmt.Errorf("%w; error route: %w", err, routeErr)
	}
	return out, nil
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
			var out message.Message
			err := r.retry(ctx, n, func() (err error) {
				out, err = p.Process(ctx, msg)
				if err == nil && out == nil {
					err = errors.New("returned no message")
				}
				return err
			})
			if err != nil {
				return nil, &StepError{n.ID, msg, err}
			}
			msg = out
		case stepdef.SinkProcessor:
			if err := r.retry(ctx, n, func() error { return p.Consume(ctx, msg) }); err != nil {
				return nil, &StepError{n.ID, msg, err}
			}
		case stepdef.RouterProcessor:
			var routes []stepdef.Route
			err := r.retry(ctx, n, func() (err error) {
				routes, err = p.Route(ctx, msg)
				return err
			})
			if err != nil {
				return nil, &StepError{n.ID, msg, err}
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

// retry calls do, and while it fails calls it again, as often as the error
// handler allows redeliveries, after its delay. It stops when ctx is done.
func (r *run) retry(ctx context.Context, n *flowdef.Node, do func() error) error {
	err := do()
	if err == nil || r.errh == nil {
		return err
	}
	for i := 1; err != nil && i <= r.errh.Redeliveries && ctx.Err() == nil; i++ {
		stepdef.Logger(ctx).Printf("step %s: redelivery %d of %d in %v after: %v", n.ID, i, r.errh.Redeliveries, r.errh.RedeliveryDelay, err)
		t := time.NewTimer(r.errh.RedeliveryDelay)
		select {
		case <-t.C:
		case <-ctx.Done():
			t.Stop()
			return err
		}
		err = do()
	}
	return err
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
