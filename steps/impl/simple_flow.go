package impl

// flowProperties is what a flow knows about itself: the properties the simple
// language reads as ${flowId}, ${flowName}, ${flowVersion}, ${tenant} and
// ${environment}.
type flowProperties struct {
	id, name, version, tenant, environment string
}
