// Package definition defines the contracts every step processor implements.
//
// A processor operates on a Message: one map of headers, metadata headers and
// the body. Message executions are independent and may run concurrently.
//
//   - Source processors create messages and inject them into the flow.
//   - Action processors modify or inspect a message and pass it to the next step.
//   - Router processors determine the next route and/or create multiple message paths.
//   - Sink processors consume a message and normally end the current processing path.
//
// Processors support cancellation through ctx and return errors to the engine.
// They never retry or handle errors themselves; that is the engine's job.
// A processor instance may be used by several message executions at once and
// must therefore be safe for concurrent use.
package definition

import (
	"context"

	"dif/message"
)

// Processor is a step processor: a SourceProcessor, ActionProcessor,
// RouterProcessor (or Looper) or SinkProcessor.
type Processor any

// SourceProcessor produces messages until it is done or ctx is cancelled,
// handing each to the flow with emit.
type SourceProcessor interface {
	Run(ctx context.Context, emit Emit) error
}

// ReadySourceProcessor optionally provides a source startup barrier. RunReady
// calls ready once it can receive messages, before calling emit. The engine
// calls RunReady instead of Run and waits for ready (or return) during Start.
// Sources must release their resources before returning after cancellation.
type ReadySourceProcessor interface {
	SourceProcessor
	RunReady(ctx context.Context, emit Emit, ready func()) error
}

// Emit hands m to the flow and returns once the flow has taken it. It returns
// an error only when the flow is stopping, never because a step failed.
//
// A source that needs the outcome, such as a request-reply endpoint, passes
// reply: the flow calls it once m has been processed, with the final message
// or the error. reply may be nil.
type Emit func(m message.Message, reply func(message.Message, error)) error

// CompletionSourceProcessor receives terminal processing completion separately
// from exchange replies, which may be sent early. complete is called once after
// processing (including error routing), only if emit accepted the delivery.
// A completion error is reported as a processing and source failure.
type CompletionSourceProcessor interface {
	SourceProcessor
	RunDelivery(ctx context.Context, emit EmitDelivery, ready func()) error
}

type EmitDelivery func(m message.Message, reply func(message.Message, error), complete func(error) error) error

// ActionProcessor modifies or inspects m and returns the message for the next step.
type ActionProcessor interface {
	Process(ctx context.Context, m message.Message) (message.Message, error)
}

// RouterProcessor decides which outbound links receive which messages. The
// engine runs the routes in order, each to the end of its path. The message
// that comes out of the last route that is not Detached is the router's
// outcome; with no such route it is m as it entered the router (no routes at
// all ends the message there, as a filter does).
//
// A router in an action position has one outbound link, so it either passes
// the message on or stops it.
type RouterProcessor interface {
	Route(ctx context.Context, m message.Message) ([]Route, error)
}

// Route sends Message to the step's outbound link with index Next. Routes run
// one after another and must not share a Message: a router that sends m to
// several links sends copies (message.Message.Copy).
type Route struct {
	Next    int
	Message message.Message

	// Detached routes, such as a wire tap, do not affect the outcome: their
	// result is ignored and their error is logged instead of failing the message.
	Detached bool
}

// A Gatherer is a router that combines what comes out of its routes
// (scatter-gather), such as an enricher. The engine runs the routes Route
// returns and, instead of stopping at a failed one, collects the outcome of
// every route that is not detached, in order. It then calls Gather with the
// message that entered the router and those outcomes, and runs the routes
// Gather returns as it runs any router's routes. An error from Gather fails
// the message; to fail it with a route's error, return Outcome.Err.
type Gatherer interface {
	Gather(ctx context.Context, m message.Message, outcomes []Outcome) ([]Route, error)
}

// GatherAborter releases resources acquired by Route if cancellation or an
// invalid route prevents Gather from being called. Cleanup runs after branches
// have returned; cancellation alone must not release a still-running operation.
type GatherAborter interface {
	AbortGather(m message.Message) error
}

// A Looper is a router that runs its routes in rounds, such as a loop: it has
// Round instead of RouterProcessor's Route. The engine calls Round first with
// round 0 and prev the message that entered the router, then, after it has
// run the routes of a round, with the next round and prev the message that
// came out of them (prev as it was if none did). It stops after the round
// Round reports as the last one; the message that comes out of that round is
// the router's outcome.
//
// in is the message that entered the router. It stays as it entered as long
// as the Looper sends only copies of it, so it can be evaluated in every round.
type Looper interface {
	Round(ctx context.Context, in, prev message.Message, round int) (routes []Route, last bool, err error)
}

// Outcome is what came out of a route: the message at the end of its path,
// or the error that stopped it.
type Outcome struct {
	Message message.Message
	Err     error
}

// Link describes an outbound link of a router, as the flow defines it: the
// rule that names its role (such as "wiretap" or "split") and, for a
// condition, its language and expression. A router gets its links, in the
// order of its outbound links, as Params[Links]; Route.Next indexes them.
type Link struct {
	Rule, Language, Expression string
}

// Links is the Params key under which a router processor gets its []Link.
const Links = "links"

// SinkProcessor consumes m, typically by sending it outside the flow.
type SinkProcessor interface {
	Consume(ctx context.Context, m message.Message) error
}

// Step kinds, as in DIL.
const (
	Source = "source"
	Action = "action"
	Router = "router"
	Sink   = "sink"
)

// Params are a step's options after validation against its schema: defaults
// are applied and values have the type the schema declares (string, bool,
// int or float64).
type Params map[string]any

// Definition describes a step processor so it can be registered.
type Definition struct {
	Name    string // the step URI's scheme, e.g. "timer" for "timer:tick"
	Kind    string // Source, Action, Router or Sink
	Schema  []byte // JSON Schema of the step's options (a DIL step's "options" object)
	Pattern string // the Enterprise Integration Pattern it implements, e.g. "Splitter"; "" for none

	// RuntimeBindings opts a constructor into trusted, non-option parameters
	// supplied by a loader. Ordinary custom processors receive only schema options.
	RuntimeBindings []string

	// New creates the processor for the step with id stepID from validated params.
	New func(stepID string, p Params) (Processor, error)
}
