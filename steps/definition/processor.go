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
// RouterProcessor or SinkProcessor.
type Processor any

// SourceProcessor produces messages until it is done or ctx is cancelled,
// handing each to the flow with emit.
type SourceProcessor interface {
	Run(ctx context.Context, emit Emit) error
}

// Emit hands m to the flow and returns once the flow has taken it. It returns
// an error only when the flow is stopping, never because a step failed.
//
// A source that needs the outcome, such as a request-reply endpoint, passes
// reply: the flow calls it once m has been processed, with the final message
// or the error. reply may be nil.
type Emit func(m message.Message, reply func(message.Message, error)) error

// ActionProcessor modifies or inspects m and returns the message for the next step.
type ActionProcessor interface {
	Process(ctx context.Context, m message.Message) (message.Message, error)
}

// RouterProcessor decides which outbound links receive which messages.
// Routers are defined for completeness; the engine does not run them yet.
type RouterProcessor interface {
	Route(ctx context.Context, m message.Message) ([]Route, error)
}

// Route sends Message to the step's outbound link with index Next.
type Route struct {
	Next    int
	Message message.Message
}

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
	Name   string // the step URI's scheme, e.g. "timer" for "timer:tick"
	Kind   string // Source, Action, Router or Sink
	Schema []byte // JSON Schema of the step's options (a DIL step's "options" object)

	// New creates the processor for the step with id stepID from validated params.
	New func(stepID string, p Params) (Processor, error)
}
