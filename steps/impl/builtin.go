// Package impl contains the built-in step processors. Each step's options are
// described by a JSON Schema in schemas/<name>-<kind>.json.
package impl

import (
	"embed"

	stepdef "dif/steps/definition"
	"dif/steps/registry"
)

//go:embed schemas/*.json
var schemas embed.FS

// Register adds the built-in steps to r.
func Register(r *registry.Registry) error {
	builtins := []struct {
		name, kind string
		new        func(string, stepdef.Params) (stepdef.Processor, error)
	}{
		{"timer", stepdef.Source, newTimerSource},
		{"file", stepdef.Source, newFileSource},
		{"message", stepdef.Source, newMessageSource},
		{"log", stepdef.Action, newLogAction},
		{"setbody", stepdef.Action, newSetBodyAction},
		{"setheader", stepdef.Action, newSetHeaderAction},
		{"passthrough", stepdef.Action, newPassthrough},
		{"file", stepdef.Sink, newFileSink},
	}
	for _, b := range builtins {
		schema, err := schemas.ReadFile("schemas/" + b.name + "-" + b.kind + ".json")
		if err != nil {
			return err
		}
		if err := r.Register(stepdef.Definition{Name: b.name, Kind: b.kind, Schema: schema, New: b.new}); err != nil {
			return err
		}
	}
	return nil
}
