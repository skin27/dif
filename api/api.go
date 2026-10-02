// Package api is the public entry point for running DIF flows.
package api

import (
	"os"

	"dif/engine"
	flowimpl "dif/flows/impl"
	"dif/message"
	stepdef "dif/steps/definition"
	stepimpl "dif/steps/impl"
	"dif/steps/registry"
)

type (
	Message        = message.Message
	Result         = engine.Result
	State          = engine.State
	Engine         = engine.Engine
	FlowStatus     = engine.FlowStatus
	StepDefinition = stepdef.Definition
)

// Body is the message key of the body.
const Body = message.Body

const (
	Stopped = engine.Stopped
	Started = engine.Started
	Paused  = engine.Paused
)

// steps is the processor registry used by Load, holding the built-in steps.
var steps = func() *registry.Registry {
	r := registry.New()
	if err := stepimpl.Register(r); err != nil {
		panic(err) // the built-in schemas are embedded, so this is a programming error
	}
	return r
}()

// RegisterStep adds a step processor that flows loaded afterwards can use.
func RegisterStep(d StepDefinition) error { return steps.Register(d) }

// NewEngine returns an engine without flows; register loaded flows with Add.
func NewEngine() *Engine { return engine.New() }

// Flow is a loaded flow with lifecycle methods (Start, Pause, Resume, Stop,
// State, Wait), Send to hand it a message while it runs and NewMessage to
// build its configured message.
type Flow struct {
	*engine.Runner
}

// Load reads a DIL flow from path and builds it. Every step's options are
// validated against its schema; a flow with an unknown or invalid step is
// not loaded. The flow is Stopped until Start is called; onResult is called
// for every message it processes.
func Load(path string, onResult func(*Result, error)) (*Flow, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	f, err := flowimpl.Parse(data, steps.Processor)
	if err != nil {
		return nil, err
	}
	return &Flow{Runner: engine.NewRunner(f, onResult)}, nil
}
