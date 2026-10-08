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
//
// The message itself records where it went: Run sets its OriginalBody and
// continues its Trail with the flow and source, and every step it enters
// becomes its Step and is added to its Trail. Unlike Result.Trail, which
// lists the steps of all branches, a message's Trail is its own path.
func Run(ctx context.Context, f *flowdef.Flow, msg message.Message) (*Result, error) {
	return execute(ctx, f, msg, nil)
}

// execute runs msg as Run does and replies to its sender: reply, if not nil,
// gets the outcome once. That is when the message is done, or earlier, when a
// step makes its exchange one-way (message.InOnly): then reply gets the
// message as it is at that point and the flow goes on.
func execute(ctx context.Context, f *flowdef.Flow, msg message.Message, reply func(message.Message, error)) (*Result, error) {
	if msg == nil {
		msg = message.Message{}
	}
	msg.EnsureIdentity()
	r := &run{trail: []string{f.Source.Kind + ":" + f.Source.ID}, errh: f.Error, reply: reply} // the source produced msg
	res, err := r.flow(ctx, f, msg)
	if r.reply != nil {
		var out message.Message
		if res != nil {
			out = res.Message
		}
		r.reply(out, err)
	}
	return res, err
}

// flow passes msg from the source of f to the end of its flow.
func (r *run) flow(ctx context.Context, f *flowdef.Flow, msg message.Message) (*Result, error) {
	start := time.Now()
	delete(msg, message.ExchangePattern) // the pattern of an exchange in another flow
	msg[message.OriginalBody] = msg[message.Body]
	if f.ID != "" {
		addTrail(msg, "flow:"+f.ID)
	}
	enter(msg, f.Source.Kind, f.Source.ID)

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
	reply func(message.Message, error) // the sender's, until it has its reply; nil if none
}

// replyIfOneWay replies to the sender with m once the exchange of m is one-way.
func (r *run) replyIfOneWay(m message.Message) {
	if r.reply != nil && m[message.ExchangePattern] == message.InOnly {
		r.reply(m.Copy(), nil)
		r.reply = nil
	}
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
	addTrail(m, "error:"+r.errh.ID)
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
		enter(msg, n.Kind, n.ID)

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
			r.replyIfOneWay(msg)
		case stepdef.SinkProcessor:
			if err := r.retry(ctx, n, func() error { return p.Consume(ctx, msg) }); err != nil {
				return nil, &StepError{n.ID, msg, err}
			}
		case stepdef.Looper:
			r.trail = append(r.trail, n.Kind+":"+n.ID)
			return r.loop(ctx, n, msg, p)
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
// message that comes out of the last route that is not detached (msg if
// none). A router that is a stepdef.Gatherer gets the routes' outcomes and
// returns the routes to run next.
func (r *run) route(ctx context.Context, n *flowdef.Node, msg message.Message, routes []stepdef.Route) (message.Message, error) {
	g, gathers := n.Processor.(stepdef.Gatherer)
	out, outcomes, err := r.runRoutes(ctx, n, msg, routes, gathers)
	if err != nil || !gathers {
		if err != nil && gathers {
			if cleanup, ok := n.Processor.(stepdef.GatherAborter); ok {
				err = errors.Join(err, cleanup.AbortGather(msg))
			}
		}
		return out, err
	}
	next, err := g.Gather(ctx, msg, outcomes)
	if err != nil {
		if errors.As(err, new(*StepError)) { // a route's failure: keep its step and message
			return nil, err
		}
		return nil, &StepError{n.ID, msg, err}
	}
	out, _, err = r.runRoutes(ctx, n, msg, next, false)
	return out, err
}

// loop runs the rounds of Looper n, which msg entered, and returns the message
// that comes out of the last one.
func (r *run) loop(ctx context.Context, n *flowdef.Node, msg message.Message, l stepdef.Looper) (message.Message, error) {
	prev := msg
	for round := 0; ; round++ {
		var routes []stepdef.Route
		var last bool
		err := r.retry(ctx, n, func() (err error) {
			routes, last, err = l.Round(ctx, msg, prev, round)
			return err
		})
		if err != nil {
			return nil, &StepError{n.ID, prev, err}
		}
		if prev, _, err = r.runRoutes(ctx, n, prev, routes, false); err != nil {
			return nil, err
		}
		if last {
			return prev, nil
		}
	}
}

// runRoutes runs routes one after another. With gather it collects their
// outcomes instead of stopping at the first error; otherwise it returns the
// message of the last route that is not detached (msg if none).
func (r *run) runRoutes(ctx context.Context, n *flowdef.Node, msg message.Message, routes []stepdef.Route, gather bool) (message.Message, []stepdef.Outcome, error) {
	out := msg
	var outcomes []stepdef.Outcome
	for _, rt := range routes {
		if rt.Next < 0 || rt.Next >= len(n.Next) {
			return nil, nil, fmt.Errorf("step %s: route to link %d, but the step has %d", n.ID, rt.Next, len(n.Next))
		}
		if rt.Message == nil {
			return nil, nil, fmt.Errorf("step %s: route to link %d has no message", n.ID, rt.Next)
		}
		res, err := r.path(ctx, n.Next[rt.Next], rt.Message)
		switch {
		case rt.Detached:
			if err != nil {
				stepdef.Logger(ctx).Printf("step %s: detached route to link %d failed: %v", n.ID, rt.Next, err)
			}
		case gather:
			if err != nil && ctx.Err() != nil {
				return nil, nil, err // a forced stop is not an outcome
			}
			outcomes = append(outcomes, stepdef.Outcome{Message: res, Err: err})
		case err != nil:
			return nil, nil, err
		default:
			out = res
		}
	}
	return out, outcomes, nil
}

// enter records in the metadata of m that it entered step id of kind: the
// step becomes its Step and "kind:id" is added to its Trail.
func enter(m message.Message, kind, id string) {
	addTrail(m, kind+":"+id)
	m[message.Step] = id
}

// addTrail adds entry to the Trail of m.
func addTrail(m message.Message, entry string) {
	if t, _ := m[message.Trail].(string); t != "" {
		entry = t + " " + entry
	}
	m[message.Trail] = entry
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
