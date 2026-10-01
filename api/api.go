// Package api is the public entry point for running DIF flows.
package api

import (
	"os"

	"dif/engine"
	flowimpl "dif/flows/impl"
	"dif/message"
	stepimpl "dif/steps/impl"
)

type (
	Message = message.Message
	Result  = engine.Result
)

// Run reads a DIL flow from path, builds it and executes it once.
func Run(path string) (*Result, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	f, err := flowimpl.Parse(data, stepimpl.New)
	if err != nil {
		return nil, err
	}

	msg := message.New(f.Input.Body)
	for k, v := range f.Input.Headers {
		msg.Headers[k] = v
	}
	return engine.Run(f, msg)
}
