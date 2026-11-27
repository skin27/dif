package impl

import (
	"context"

	flowdef "dif/flows/definition"
	"dif/internal/mapping"
	"dif/message"
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

	// stores are the stores ${store:...} may read, by the names the flow uses;
	// nil when the flow was made without a loader. channels is the runtime they are in.
	stores   map[string]stepdef.Store
	channels *Channels
}

// flowOf returns the properties of the flow a step is in; nil if it is in none.
func flowOf(p stepdef.Params) *flowProperties {
	f, _ := p[FlowRuntimeKey].(*flowdef.Flow)
	functions, _ := p[mapping.FunctionsBindingKey].(*mapping.Functions)
	stores, _ := declaredStores(p)
	if f == nil {
		if functions == nil && stores == nil {
			return nil
		}
		return &flowProperties{functions: functions, stores: stores, channels: channelRuntime(p)}
	}
	return &flowProperties{id: f.ID, name: f.Name, version: f.Version, tenant: f.Tenant, environment: f.Environment, functions: functions, stores: stores, channels: channelRuntime(p)}
}

// CompilePartitionKey compiles a flow's partitionBy Simple expression into a
// key extractor function safe for concurrent use.
func CompilePartitionKey(f *flowdef.Flow, expr string) (func(context.Context, message.Message) (string, error), error) {
	var fp *flowProperties
	if f != nil {
		fp = &flowProperties{id: f.ID, name: f.Name, version: f.Version, tenant: f.Tenant, environment: f.Environment}
	}
	x, err := compileExpressionIn(fp, "simple", expr)
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context, m message.Message) (string, error) {
		return x.eval(m, ctx)
	}, nil
}
