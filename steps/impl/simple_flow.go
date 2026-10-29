package impl

import (
	flowdef "dif/flows/definition"
	"dif/internal/mapping"
	stepdef "dif/steps/definition"
)

// FlowRuntimeKey is injected after schema validation by the API loader: the
// flow a step is in, for the expressions that refer to it.
const FlowRuntimeKey = internalPrefix + "flow"

// internalPrefix starts the name of every parameter the loader binds to a step.
// A step that reads all its parameters as options skips these.
const internalPrefix = "dif.internal."

// flowProperties is what a flow knows about itself: the properties the simple
// language reads as ${flowId}, ${flowName}, ${flowVersion}, ${tenant} and
// ${environment}.
type flowProperties struct {
	id, name, version, tenant, environment string
	functions                              *mapping.Functions
}

// flowOf returns the properties of the flow a step is in; nil if it is in none.
func flowOf(p stepdef.Params) *flowProperties {
	f, _ := p[FlowRuntimeKey].(*flowdef.Flow)
	functions, _ := p[mapping.FunctionsBindingKey].(*mapping.Functions)
	if f == nil {
		if functions == nil {
			return nil
		}
		return &flowProperties{functions: functions}
	}
	return &flowProperties{f.ID, f.Name, f.Version, f.Tenant, f.Environment, functions}
}
