// Package api is the public entry point for running DIF flows.
package api

import (
	"os"

	"dif/engine"
	flowdef "dif/flows/definition"
	flowimpl "dif/flows/impl"
	"dif/message"
	stepimpl "dif/steps/impl"
)

type (
	Message = message.Message
	Result  = engine.Result
	State   = engine.State
)

const (
	Stopped = engine.Stopped
	Started = engine.Started
	Paused  = engine.Paused
)

// Flow is a loaded flow with lifecycle methods (Start, Pause, Resume, Stop,
// State, Wait) and Send to hand it a message while it runs.
type Flow struct {
	*engine.Runner
	input flowdef.InputMessage
}

// Load reads a DIL flow from path and builds it. The flow is Stopped until
// Start is called; onResult is called for every message it processes.
func Load(path string, onResult func(*Result, error)) (*Flow, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	f, err := flowimpl.Parse(data, stepimpl.New)
	if err != nil {
		return nil, err
	}

	src, err := stepimpl.NewSource(f.Source)
	if err != nil {
		return nil, err
	}
	return &Flow{Runner: engine.NewRunner(f, src, onResult), input: f.Input}, nil
}

// NewMessage returns a new message built from the flow's configured message
// (the first dil.core.messages.message), or an empty message if there is none.
func (f *Flow) NewMessage() *Message {
	m := message.New(f.input.Body)
	for k, v := range f.input.Headers {
		m.Headers[k] = v
	}
	return m
}
