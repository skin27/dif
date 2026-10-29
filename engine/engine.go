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
	ErrorMessage     = message.ErrorMessage
	ErrorStep        = message.ErrorStep
	ErrorClass       = message.ErrorClass
	ErrorStackTrace  = message.ErrorStackTrace
	ErrorBody        = message.ErrorBody
	ErrorContentType = message.ErrorContentType
)

// StepError is the failure of a step, with the message the step got.
type StepError struct {
	Step    string
	Message message.Message
	Err     error
	URI     string // the URI of the step, for an error handler to match
}

func (e *StepError) Error() string { return "step " + e.Step + ": " + e.Err.Error() }
func (e *StepError) Unwrap() error { return e.Err }

// Run passes msg, produced by the flow's source, along its links to the sinks.
// At a router the message follows the routes the router returns, each path to
// its end; see stepdef.RouterProcessor for which message comes out.
//
// Processing stops at the first step that fails or when ctx is done. With an
// error handler (the first of the flow's handlers that matches the failure,
// else its default one), a failing step is first tried again; if it keeps
// failing, the message as the step got it, with the ErrorMessage and ErrorStep
// headers, goes along the error route. The message has then not failed: its
// outcome is the error route's, and Result.Err says what was handled. A handler
// that is Unhandled still fails the message after its route: Run returns the
// route's Result and the original error.
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
// message as it is at that point and the flow goes on. A return step (see
// stepdef.Return) chooses the reply: either at once, or fixed at that step
// and given when the message is done. A failure replaces a reply fixed before
// it, and so does the error route's outcome, unless the route returns its own.
func execute(ctx context.Context, f *flowdef.Flow, msg message.Message, reply func(message.Message, error)) (*Result, error) {
	if msg == nil {
		msg = message.Message{}
	}
	msg.EnsureIdentity()
	r := &run{
		ctx:      ctx,
		contract: f.Contract,
		trail:    []string{f.Source.Kind + ":" + f.Source.ID},
		errh:     f.Error,
		matched:  f.Handlers,
		reply:    reply,
	} // the source produced msg
	if reply != nil {
		ctx = stepdef.WithReturn(ctx, r.returnReply)
		r.ctx = ctx
	}
	res, err := r.flow(ctx, f, msg)
	if r.reply != nil {
		var out message.Message
		if res != nil {
			out = res.Message
		}
		replyErr := err
		explicitFault := false
		if r.returned != nil && err == nil && res != nil {
			out, replyErr, explicitFault = r.returned.msg, r.returned.err, r.returned.err != nil
		}
		r.deliverReply(out, replyErr, explicitFault)
	} else if reply == nil && err == nil && res != nil && f.Contract != nil && f.Contract.ExchangePattern == message.InOut && f.Contract.ValidateOutput != nil {
		if verr := f.Contract.ValidateOutput(ctx, res.Message); verr != nil {
			r.contractErr = &flowdef.ContractError{Service: f.Contract.Name, Kind: "output", Reason: verr.Error()}
		}
	}
	if err == nil && r.contractErr != nil {
		err = r.contractErr
	}
	return res, err
}

// flow passes msg from the source of f to the end of its flow.
func (r *run) flow(ctx context.Context, f *flowdef.Flow, msg message.Message) (*Result, error) {
	start := time.Now()
	wire := msg[message.WireHeaders] == true
	delete(msg, message.WireHeaders)
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
	if f.Contract != nil && f.Contract.ValidateInput != nil {
		if verr := f.Contract.ValidateInput(ctx, msg, wire); verr != nil {
			entryErr := &StepError{
				Step:    f.Source.ID,
				Message: msg,
				Err:     &flowdef.ContractError{Service: f.Contract.Name, Kind: "input", Reason: verr.Error()},
				URI:     f.Source.URI,
			}
			handled, unhandled, herr := r.handle(ctx, entryErr)
			if herr != nil {
				return nil, herr
			}
			res := &Result{Message: handled, Trail: r.trail, Duration: time.Since(start), Err: entryErr}
			if unhandled {
				return res, entryErr
			}
			return res, nil
		}
	}
	return r.finish(ctx, start, n, msg)
}

// finish passes msg through the path that starts at step n, and the error route
// handles a failure.
func (r *run) finish(ctx context.Context, start time.Time, n *flowdef.Node, msg message.Message) (*Result, error) {
	if n != nil {
		out, err := r.path(ctx, n, msg)
		if err != nil {
			handled, unhandled, herr := r.handle(ctx, err)
			if herr != nil {
				return nil, herr
			}
			res := &Result{Message: handled, Trail: r.trail, Duration: time.Since(start), Err: err}
			if unhandled {
				return res, err // the error route ran, but the message still fails
			}
			return res, nil
		}
		msg = out
	}
	return &Result{Message: msg, Trail: r.trail, Duration: time.Since(start)}, nil
}

// release runs msg, which the processor of step at passed on by itself (see
// stepdef.Releaser), from the step after it. Nobody waits for its reply.
func release(ctx context.Context, f *flowdef.Flow, at *flowdef.Node, msg message.Message) (*Result, error) {
	msg.EnsureIdentity()
	delete(msg, message.ExchangePattern) // the sender of the messages it comes from is not waiting for it
	r := &run{trail: []string{at.Kind + ":" + at.ID}, errh: f.Error, matched: f.Handlers}
	n, err := nextStep(at)
	if err != nil {
		return nil, err
	}
	return r.finish(ctx, time.Now(), n, msg)
}

// releasers returns the steps of f whose processors are stepdef.Releasers.
func releasers(f *flowdef.Flow) []*flowdef.Node {
	var found []*flowdef.Node
	seen := map[*flowdef.Node]bool{}
	var walk func(n *flowdef.Node)
	walk = func(n *flowdef.Node) {
		if n == nil || seen[n] {
			return
		}
		seen[n] = true
		if _, ok := n.Processor.(stepdef.Releaser); ok {
			found = append(found, n)
		}
		for _, next := range n.Next {
			walk(next)
		}
	}
	walk(f.Source)
	for _, h := range f.ErrorHandlers() {
		walk(h.Route)
	}
	return found
}

// run is the state of one message execution.
type run struct {
	ctx         context.Context
	contract    *flowdef.Contract
	contractErr error
	trail       []string
	errh        *flowdef.ErrorHandler        // the default error handler; nil if none
	matched     []*flowdef.ErrorHandler      // handlers for particular failures, tried before errh
	reply       func(message.Message, error) // the sender's, until it has its reply; nil if none
	returned    *returned                    // the reply a return step fixed for the end of the flow; nil if none
}

// returned is the reply a return step fixed.
type returned struct {
	msg message.Message
	err error
}

// deliverReply validates an InOut reply or explicit fault against the flow contract
// before handing it to the sender.
func (r *run) deliverReply(out message.Message, replyErr error, explicitFault bool) {
	if r.reply == nil {
		return
	}
	send := r.reply
	r.reply = nil
	if c := r.contract; c != nil && c.ExchangePattern == message.InOut {
		switch {
		case replyErr == nil:
			if c.ValidateOutput != nil {
				if out == nil {
					out = message.Message{}
				}
				if err := c.ValidateOutput(r.ctx, out); err != nil {
					cerr := &flowdef.ContractError{Service: c.Name, Kind: "output", Reason: err.Error()}
					r.contractErr = cerr
					send(nil, cerr)
					return
				}
			}
		case explicitFault:
			if c.ValidateError != nil {
				if out == nil {
					out = message.Message{}
				}
				if err := c.ValidateError(r.ctx, out); err != nil {
					cerr := &flowdef.ContractError{Service: c.Name, Kind: "error", Reason: err.Error()}
					r.contractErr = cerr
					send(nil, cerr)
					return
				}
			}
			replyErr = &flowdef.FaultError{
				Service:    c.Name,
				Reason:     replyErr.Error(),
				Message:    out.Copy(),
				Contracted: true,
			}
		}
	}
	send(out, replyErr)
}

// replyIfOneWay replies to the sender once the exchange of m is one-way: with
// the reply a return step fixed, else with m.
func (r *run) replyIfOneWay(m message.Message) {
	if r.reply != nil && m[message.ExchangePattern] == message.InOnly {
		if r.returned != nil {
			r.deliverReply(r.returned.msg, r.returned.err, r.returned.err != nil)
		} else {
			r.deliverReply(m.Copy(), nil, false)
		}
	}
}

// returnReply takes the reply of a return step (see stepdef.Return). The first
// one wins: it reports false when the sender has a reply, or will have one.
func (r *run) returnReply(m message.Message, err error, detach bool) bool {
	if r.reply == nil || r.returned != nil {
		return false
	}
	if !detach {
		r.returned = &returned{m, err}
		return true
	}
	r.deliverReply(m, err, err != nil)
	return true
}

// handlerFor returns the error handler for a failure: the first handler with a
// match that takes it, else the default one; nil if the flow has none.
func (r *run) handlerFor(step, uri string, err error) *flowdef.ErrorHandler {
	if len(r.matched) > 0 {
		f := flowdef.Failure{Step: step, URI: uri, Class: errorClass(err), Message: err.Error()}
		for _, h := range r.matched {
			if h.Match == nil || h.Match(f) {
				return h
			}
		}
	}
	return r.errh
}

// handle sends the message of the failed step along the error route of the
// handler for the failure and returns its outcome, or err when there is no
// error route to take. unhandled tells that the handler wants the message to
// fail anyway.
func (r *run) handle(ctx context.Context, err error) (out message.Message, unhandled bool, herr error) {
	var se *StepError
	if ctx.Err() != nil || !errors.As(err, &se) {
		return nil, false, err
	}
	h := r.handlerFor(se.Step, se.URI, se.Err)
	if h == nil || h.Route == nil {
		return nil, false, err
	}
	r.returned = nil // the failure replaces a reply fixed before it; the error route may return its own
	m := se.Message
	m[ErrorMessage], m[ErrorStep] = se.Err.Error(), se.Step
	m[ErrorClass], m[ErrorStackTrace] = errorClass(se.Err), err.Error()
	var fe *flowdef.FaultError
	if errors.As(se.Err, &fe) && fe.Message != nil {
		m[ErrorBody] = fe.Message[message.Body]
		if ct, ok := fe.Message[message.ContentType].(string); ok && ct != "" {
			m[ErrorContentType] = ct
		} else {
			delete(m, ErrorContentType)
		}
	} else {
		delete(m, ErrorBody)
		delete(m, ErrorContentType)
	}
	r.trail = append(r.trail, "error:"+h.ID)
	addTrail(m, "error:"+h.ID)
	out, routeErr := r.path(ctx, h.Route, m)
	if routeErr != nil {
		return nil, false, fmt.Errorf("%w; error route: %w", err, routeErr)
	}
	return out, h.Unhandled, nil
}

// redeliveryDelay is the wait before redelivery number i (from 1) of h: its
// delay, multiplied by Backoff for each earlier one, and at most MaxDelay.
func redeliveryDelay(h *flowdef.ErrorHandler, i int) time.Duration {
	d := h.RedeliveryDelay
	for k := 1; k < i && h.Backoff > 1; k++ {
		next := float64(d) * h.Backoff
		if next >= float64(1<<62) { // beyond what a Duration holds
			d = 1 << 62
			break
		}
		d = time.Duration(next)
		if h.MaxDelay > 0 && d >= h.MaxDelay {
			break
		}
	}
	if h.MaxDelay > 0 && d > h.MaxDelay {
		d = h.MaxDelay
	}
	return d
}

// errorClass is the type of the error at the bottom of the chain of errors
// that err wraps, such as *fs.PathError.
func errorClass(err error) string {
	for {
		next := errors.Unwrap(err)
		if next == nil {
			return fmt.Sprintf("%T", err)
		}
		err = next
	}
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
				return nil, &StepError{n.ID, msg, err, n.URI}
			}
			msg = out
			r.replyIfOneWay(msg)
		case stepdef.SinkProcessor:
			if err := r.retry(ctx, n, func() error { return p.Consume(ctx, msg) }); err != nil {
				return nil, &StepError{n.ID, msg, err, n.URI}
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
				return nil, &StepError{n.ID, msg, err, n.URI}
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
// handler for the failure allows redeliveries, after its delay. The handler is
// chosen again after every failure. It stops when ctx is done.
func (r *run) retry(ctx context.Context, n *flowdef.Node, do func() error) error {
	err := do()
	if err == nil || (r.errh == nil && len(r.matched) == 0) {
		return err
	}
	for i := 1; err != nil && ctx.Err() == nil; i++ {
		h := r.handlerFor(n.ID, n.URI, err)
		if h == nil || i > h.Redeliveries {
			break
		}
		delay := redeliveryDelay(h, i)
		stepdef.Logger(ctx).Printf("step %s: redelivery %d of %d in %v after: %v", n.ID, i, h.Redeliveries, delay, err)
		t := time.NewTimer(delay)
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
		return nil, &StepError{n.ID, msg, err, n.URI}
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
			return nil, &StepError{n.ID, prev, err, n.URI}
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
