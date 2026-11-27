// Package engine executes a flow model. It knows only the processor
// interfaces, never concrete steps or the DSL the flow was defined in.
package engine

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"sync"
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
	return executeWith(ctx, f, msg, reply, nil)
}

func executeWith(ctx context.Context, f *flowdef.Flow, msg message.Message, reply func(message.Message, error), stepObs *stepObserver) (*Result, error) {
	if msg == nil {
		msg = message.Message{}
	}
	msg.EnsureIdentity()
	var replies *replyState
	if reply != nil {
		replies = &replyState{ctx: ctx, contract: f.Contract, reply: reply}
		ctx = stepdef.WithReturn(ctx, replies.returnReply)
		replies.ctx = ctx
	}
	r := &run{
		ctx:      ctx,
		contract: f.Contract,
		trail:    []string{f.Source.Kind + ":" + f.Source.ID},
		errh:     f.Error,
		matched:  f.Handlers,
		replies:  replies,
		stepObs:  stepObs,
		trace:    TraceFrom(ctx),
	} // the source produced msg
	res, err := r.flow(ctx, f, msg)
	if replies != nil {
		err = replies.deliverFinal(res, err)
	} else if err == nil && res != nil && f.Contract != nil && f.Contract.ExchangePattern == message.InOut && f.Contract.ValidateOutput != nil {
		if verr := f.Contract.ValidateOutput(ctx, res.Message); verr != nil {
			err = &flowdef.ContractError{Service: f.Contract.Name, Kind: "output", Reason: verr.Error()}
		}
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
	return releaseWith(ctx, f, at, msg, nil)
}

func releaseWith(ctx context.Context, f *flowdef.Flow, at *flowdef.Node, msg message.Message, stepObs *stepObserver) (*Result, error) {
	msg.EnsureIdentity()
	delete(msg, message.ExchangePattern) // the sender of the messages it comes from is not waiting for it
	r := &run{trail: []string{at.Kind + ":" + at.ID}, errh: f.Error, matched: f.Handlers, stepObs: stepObs, trace: TraceFrom(ctx)}
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
		for _, after := range n.After {
			walk(after)
		}
		for _, onErr := range n.OnError {
			walk(onErr)
		}
	}
	walk(f.Source)
	for _, h := range f.ErrorHandlers() {
		walk(h.Route)
	}
	return found
}

// run is the state of one message execution (or one parallel branch).
type run struct {
	ctx      context.Context
	contract *flowdef.Contract
	trail    []string
	errh     *flowdef.ErrorHandler   // the default error handler; nil if none
	matched  []*flowdef.ErrorHandler // handlers for particular failures, tried before errh
	replies  *replyState             // shared thread-safe reply coordinator; nil when nobody waits for a reply
	stepObs  *stepObserver           // step-level event observer; nil when nobody listens
	trace    *Trace                  // opt-in step-diff tracer; nil when not tracing
	detached bool                    // true inside detached wiretap, after or onError paths
}

// returned is the reply a return step fixed.
type returned struct {
	msg message.Message
	err error
}

// replyState guards the sender's reply callback, fixed return reply, and
// output/error contract errors across concurrent router branches.
type replyState struct {
	mu          sync.Mutex
	ctx         context.Context
	contract    *flowdef.Contract
	contractErr error
	reply       func(message.Message, error)
	returned    *returned
}

// sendReply validates an InOut reply or explicit fault against the flow contract
// before handing it to the sender.
func (rs *replyState) sendReply(ctx context.Context, c *flowdef.Contract, send func(message.Message, error), out message.Message, replyErr error, explicitFault bool) {
	if c != nil && c.ExchangePattern == message.InOut {
		switch {
		case replyErr == nil:
			if c.ValidateOutput != nil {
				if out == nil {
					out = message.Message{}
				}
				if err := c.ValidateOutput(ctx, out); err != nil {
					cerr := &flowdef.ContractError{Service: c.Name, Kind: "output", Reason: err.Error()}
					rs.mu.Lock()
					rs.contractErr = cerr
					rs.mu.Unlock()
					send(nil, cerr)
					return
				}
			}
		case explicitFault:
			if c.ValidateError != nil {
				if out == nil {
					out = message.Message{}
				}
				if err := c.ValidateError(ctx, out); err != nil {
					cerr := &flowdef.ContractError{Service: c.Name, Kind: "error", Reason: err.Error()}
					rs.mu.Lock()
					rs.contractErr = cerr
					rs.mu.Unlock()
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
	rs := r.replies
	if rs == nil || m[message.ExchangePattern] != message.InOnly {
		return
	}
	rs.mu.Lock()
	if rs.reply == nil {
		rs.mu.Unlock()
		return
	}
	send := rs.reply
	rs.reply = nil
	ret := rs.returned
	ctx := rs.ctx
	c := rs.contract
	rs.mu.Unlock()
	if ret != nil {
		rs.sendReply(ctx, c, send, ret.msg, ret.err, ret.err != nil)
	} else {
		rs.sendReply(ctx, c, send, m.Copy(), nil, false)
	}
}

// returnReply takes the reply of a return step (see stepdef.Return). The first
// one wins: it reports false when the sender has a reply, or will have one.
func (rs *replyState) returnReply(m message.Message, err error, detach bool) bool {
	if rs == nil {
		return false
	}
	rs.mu.Lock()
	if rs.reply == nil || rs.returned != nil {
		rs.mu.Unlock()
		return false
	}
	if !detach {
		rs.returned = &returned{m, err}
		rs.mu.Unlock()
		return true
	}
	send := rs.reply
	rs.reply = nil
	ctx := rs.ctx
	c := rs.contract
	rs.mu.Unlock()
	rs.sendReply(ctx, c, send, m, err, err != nil)
	return true
}

func (rs *replyState) clearReturned() {
	if rs == nil {
		return
	}
	rs.mu.Lock()
	rs.returned = nil
	rs.mu.Unlock()
}

func (rs *replyState) deliverFinal(res *Result, err error) error {
	rs.mu.Lock()
	if rs.reply != nil {
		var out message.Message
		if res != nil {
			out = res.Message
		}
		replyErr := err
		explicitFault := false
		if rs.returned != nil && err == nil && res != nil {
			out, replyErr, explicitFault = rs.returned.msg, rs.returned.err, rs.returned.err != nil
		}
		send := rs.reply
		rs.reply = nil
		ctx := rs.ctx
		c := rs.contract
		rs.mu.Unlock()
		rs.sendReply(ctx, c, send, out, replyErr, explicitFault)
		rs.mu.Lock()
	}
	cerr := rs.contractErr
	rs.mu.Unlock()
	if err == nil && cerr != nil {
		return cerr
	}
	return err
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
	r.replies.clearReturned() // the failure replaces a reply fixed before it; the error route may return its own
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

func elapsedSince(timed bool, start time.Time) time.Duration {
	if !timed {
		return 0
	}
	return time.Since(start)
}

// runDetached runs a detached observer chain (after or onError) on a copy of
// the message. Failures are logged and never affect the main message or reply.
func (r *run) runDetached(ctx context.Context, owner *flowdef.Node, hook string, target *flowdef.Node, msg message.Message) {
	sub := &run{ctx: ctx, trace: r.trace, detached: true}
	_, err := sub.path(ctx, target, msg)
	r.trail = append(r.trail, sub.trail...)
	if err != nil {
		stepdef.Logger(ctx).Printf("step %s: %s observer %s failed: %v", owner.ID, hook, target.ID, err)
	}
}

func (r *run) recordTrace(n *flowdef.Node, elapsed time.Duration, beforeBody any, beforeHeaders map[string]any, afterMsg message.Message, stepErr error) {
	if r.trace == nil {
		return
	}
	var afterBody any
	var afterHeaders map[string]any
	if afterMsg != nil {
		afterBody = snapshotValue(afterMsg[message.Body])
		afterHeaders = snapshotUserHeaders(afterMsg)
	}
	added, changed, removed := diffHeaders(beforeHeaders, afterHeaders)
	errText := ""
	if stepErr != nil {
		errText = stepErr.Error()
	}
	r.trace.record(StepTrace{
		StepID:         n.ID,
		Kind:           n.Kind,
		URI:            n.URI,
		DurationMs:     elapsed.Milliseconds(),
		BodyBefore:     beforeBody,
		BodyAfter:      afterBody,
		HeadersAdded:   added,
		HeadersChanged: changed,
		HeadersRemoved: removed,
		Error:          errText,
		Detached:       r.detached,
	})
}

// failStep records a step failure: emits step.failed if observed, invokes any
// detached OnError observer chains with error and timing metadata, and returns
// the StepError for flow-level error handling.
func (r *run) failStep(ctx context.Context, n *flowdef.Node, msg message.Message, err error, elapsed time.Duration) (message.Message, error) {
	se := &StepError{Step: n.ID, Message: msg, Err: err, URI: n.URI}
	if r.stepObs != nil {
		r.stepObs.failed(n.ID, n.URI, elapsed, se)
	}
	if len(n.OnError) > 0 && ctx.Err() == nil {
		for _, obs := range n.OnError {
			c := msg.Copy()
			c[ErrorMessage] = err.Error()
			c[ErrorStep] = n.ID
			c[ErrorClass] = errorClass(err)
			c[ErrorStackTrace] = se.Error()
			c[message.StepTarget] = n.ID
			c[message.StepDuration] = elapsed.Milliseconds()
			r.runDetached(ctx, n, "onError", obs, c)
		}
	}
	return nil, se
}

// path passes msg through step n and the steps after it, to the end of the
// path, and returns the message that comes out.
func (r *run) path(ctx context.Context, n *flowdef.Node, msg message.Message) (message.Message, error) {
	for n != nil {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("step %s: %w", n.ID, err)
		}
		enter(msg, n.Kind, n.ID)

		timed := n.Measure || len(n.OnError) > 0 || r.stepObs != nil || r.trace != nil
		var start time.Time
		if timed {
			start = time.Now()
		}
		var beforeBody any
		var beforeHeaders map[string]any
		if r.trace != nil {
			beforeBody = snapshotValue(msg[message.Body])
			beforeHeaders = snapshotUserHeaders(msg)
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
				elapsed := elapsedSince(timed, start)
				r.recordTrace(n, elapsed, beforeBody, beforeHeaders, msg, err)
				return r.failStep(ctx, n, msg, err, elapsed)
			}
			msg = out
			r.replyIfOneWay(msg)
		case stepdef.SinkProcessor:
			if err := r.retry(ctx, n, func() error { return p.Consume(ctx, msg) }); err != nil {
				elapsed := elapsedSince(timed, start)
				r.recordTrace(n, elapsed, beforeBody, beforeHeaders, msg, err)
				return r.failStep(ctx, n, msg, err, elapsed)
			}
		case stepdef.Looper:
			r.trail = append(r.trail, n.Kind+":"+n.ID)
			return r.loop(ctx, n, msg, p, timed, start, beforeBody, beforeHeaders)
		case stepdef.RouterProcessor:
			var routes []stepdef.Route
			err := r.retry(ctx, n, func() (err error) {
				routes, err = p.Route(ctx, msg)
				return err
			})
			if err != nil {
				elapsed := elapsedSince(timed, start)
				r.recordTrace(n, elapsed, beforeBody, beforeHeaders, msg, err)
				return r.failStep(ctx, n, msg, err, elapsed)
			}
			elapsed := elapsedSince(timed, start)
			r.recordTrace(n, elapsed, beforeBody, beforeHeaders, msg, nil)
			if r.stepObs != nil {
				r.stepObs.completed(n.ID, n.URI, elapsed)
			}
			r.trail = append(r.trail, n.Kind+":"+n.ID)
			return r.route(ctx, n, msg, routes, timed, start)
		default:
			return nil, fmt.Errorf("step %s: %T is not an action, router or sink processor", n.ID, n.Processor)
		}

		elapsed := elapsedSince(timed, start)
		if n.Measure {
			msg[message.StepDuration] = elapsed.Milliseconds()
			msg[message.StepTarget] = n.ID
		}
		r.recordTrace(n, elapsed, beforeBody, beforeHeaders, msg, nil)
		if r.stepObs != nil {
			r.stepObs.completed(n.ID, n.URI, elapsed)
		}
		r.trail = append(r.trail, n.Kind+":"+n.ID)
		if len(n.After) > 0 {
			for _, obs := range n.After {
				r.runDetached(ctx, n, "after", obs, msg.Copy())
			}
		}

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
func (r *run) route(ctx context.Context, n *flowdef.Node, msg message.Message, routes []stepdef.Route, timed bool, start time.Time) (message.Message, error) {
	g, gathers := n.Processor.(stepdef.Gatherer)
	var out message.Message
	var outcomes []stepdef.Outcome
	var err error
	if pr, ok := n.Processor.(stepdef.ParallelRouter); ok {
		if policy := pr.ParallelPolicy(); policy.Parallel {
			out, outcomes, err = r.runRoutesParallel(ctx, n, msg, routes, gathers, policy)
		} else {
			out, outcomes, err = r.runRoutes(ctx, n, msg, routes, gathers)
		}
	} else {
		out, outcomes, err = r.runRoutes(ctx, n, msg, routes, gathers)
	}
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
		elapsed := elapsedSince(timed, start)
		if r.trace != nil {
			r.recordTrace(n, elapsed, snapshotValue(msg[message.Body]), snapshotUserHeaders(msg), msg, err)
		}
		return r.failStep(ctx, n, msg, err, elapsed)
	}
	out, _, err = r.runRoutes(ctx, n, msg, next, false)
	return out, err
}

// loop runs the rounds of Looper n, which msg entered, and returns the message
// that comes out of the last one.
func (r *run) loop(ctx context.Context, n *flowdef.Node, msg message.Message, l stepdef.Looper, timed bool, start time.Time, beforeBody any, beforeHeaders map[string]any) (message.Message, error) {
	prev := msg
	for round := 0; ; round++ {
		var routes []stepdef.Route
		var last bool
		err := r.retry(ctx, n, func() (err error) {
			routes, last, err = l.Round(ctx, msg, prev, round)
			return err
		})
		if err != nil {
			elapsed := elapsedSince(timed, start)
			r.recordTrace(n, elapsed, beforeBody, beforeHeaders, prev, err)
			return r.failStep(ctx, n, prev, err, elapsed)
		}
		if prev, _, err = r.runRoutes(ctx, n, prev, routes, false); err != nil {
			return nil, err
		}
		if last {
			elapsed := elapsedSince(timed, start)
			r.recordTrace(n, elapsed, beforeBody, beforeHeaders, prev, nil)
			if r.stepObs != nil {
				r.stepObs.completed(n.ID, n.URI, elapsed)
			}
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
		prevDetached := r.detached
		if rt.Detached {
			r.detached = true
		}
		res, err := r.path(ctx, n.Next[rt.Next], rt.Message)
		r.detached = prevDetached
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

// runRoutesParallel executes non-AfterParallel routes concurrently under
// policy, joins all launched goroutines before returning, merges trails and
// outcomes in deterministic route index order, and then runs any trailing
// AfterParallel routes sequentially.
func (r *run) runRoutesParallel(ctx context.Context, n *flowdef.Node, msg message.Message, routes []stepdef.Route, gather bool, policy stepdef.ParallelPolicy) (message.Message, []stepdef.Outcome, error) {
	for _, rt := range routes {
		if rt.Next < 0 || rt.Next >= len(n.Next) {
			return nil, nil, fmt.Errorf("step %s: route to link %d, but the step has %d", n.ID, rt.Next, len(n.Next))
		}
		if rt.Message == nil {
			return nil, nil, fmt.Errorf("step %s: route to link %d has no message", n.ID, rt.Next)
		}
	}

	var parallel, after []stepdef.Route
	for _, rt := range routes {
		if rt.AfterParallel {
			after = append(after, rt)
		} else {
			parallel = append(parallel, rt)
		}
	}
	if len(parallel) == 0 {
		return r.runRoutes(ctx, n, msg, after, gather)
	}
	if len(parallel) == 1 && policy.BranchTimeout <= 0 {
		out, outcomes, err := r.runRoutes(ctx, n, msg, parallel, gather)
		if err != nil || len(after) == 0 {
			return out, outcomes, err
		}
		afterOut, afterOutcomes, err := r.runRoutes(ctx, n, out, after, gather)
		if err != nil {
			return nil, nil, err
		}
		return afterOut, append(outcomes, afterOutcomes...), nil
	}

	limit := policy.MaxConcurrency
	if limit <= 0 {
		limit = max(2, runtime.GOMAXPROCS(0)*2)
	}
	if limit > len(parallel) {
		limit = len(parallel)
	}
	sem := make(chan struct{}, limit)

	groupCtx, groupCancel := context.WithCancel(ctx)
	defer groupCancel()

	type branchResult struct {
		out                message.Message
		err                error
		trail              []string
		cancelledBySibling bool
	}
	results := make([]branchResult, len(parallel))

	var (
		failFastMu    sync.Mutex
		failFastFired bool
		failFastErr   error
		wg            sync.WaitGroup
	)

	for i, rt := range parallel {
		if policy.FailFast && groupCtx.Err() != nil {
			results[i] = branchResult{err: groupCtx.Err(), cancelledBySibling: ctx.Err() == nil}
			continue
		}
		select {
		case sem <- struct{}{}:
		case <-groupCtx.Done():
			results[i] = branchResult{err: groupCtx.Err(), cancelledBySibling: ctx.Err() == nil}
			continue
		}
		wg.Add(1)
		go func(idx int, route stepdef.Route) {
			defer wg.Done()
			defer func() { <-sem }()

			branchCtx := groupCtx
			var branchCancel context.CancelFunc
			if policy.BranchTimeout > 0 {
				branchCtx, branchCancel = context.WithTimeout(groupCtx, policy.BranchTimeout)
				defer branchCancel()
			}
			sub := &run{
				ctx:      branchCtx,
				contract: r.contract,
				errh:     r.errh,
				matched:  r.matched,
				replies:  r.replies,
				stepObs:  r.stepObs,
				trace:    r.trace,
				detached: r.detached || route.Detached,
			}
			out, err := sub.path(branchCtx, n.Next[route.Next], route.Message)
			res := branchResult{out: out, err: err, trail: sub.trail}
			if err != nil && !route.Detached && policy.FailFast {
				siblingCanceled := ctx.Err() == nil && groupCtx.Err() != nil &&
					!errors.Is(branchCtx.Err(), context.DeadlineExceeded) &&
					errors.Is(err, context.Canceled)
				failFastMu.Lock()
				if siblingCanceled && failFastFired {
					res.cancelledBySibling = true
					failFastMu.Unlock()
					results[idx] = res
					return
				}
				if !failFastFired {
					failFastFired = true
					failFastErr = err
				}
				failFastMu.Unlock()
				groupCancel()
			}
			results[idx] = res
		}(i, rt)
	}
	wg.Wait()

	for _, res := range results {
		r.trail = append(r.trail, res.trail...)
	}

	out := msg
	var outcomes []stepdef.Outcome
	var firstErr error
	for i, rt := range parallel {
		res := results[i]
		switch {
		case rt.Detached:
			if res.err != nil {
				stepdef.Logger(ctx).Printf("step %s: detached route to link %d failed: %v", n.ID, rt.Next, res.err)
			}
		case gather:
			if res.err != nil && ctx.Err() != nil {
				return nil, nil, res.err // a forced stop is not an outcome
			}
			if res.err != nil && !res.cancelledBySibling && firstErr == nil {
				firstErr = res.err
			}
			outcomes = append(outcomes, stepdef.Outcome{Message: res.out, Err: res.err})
		case res.err != nil:
			if !res.cancelledBySibling && firstErr == nil {
				firstErr = res.err
			}
		default:
			out = res.out
		}
	}

	if !gather {
		if firstErr != nil {
			return nil, nil, firstErr
		}
		if failFastErr != nil {
			return nil, nil, failFastErr
		}
	} else if policy.FailFast && len(parallel) > 1 {
		if firstErr != nil {
			return nil, nil, firstErr
		}
		if failFastErr != nil {
			return nil, nil, failFastErr
		}
	}

	if len(after) > 0 {
		afterOut, afterOutcomes, err := r.runRoutes(ctx, n, out, after, gather)
		if err != nil {
			return nil, nil, err
		}
		out = afterOut
		outcomes = append(outcomes, afterOutcomes...)
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
