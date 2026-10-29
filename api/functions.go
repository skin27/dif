package api

import (
	"context"
	"fmt"

	flowdef "dif/flows/definition"
	flowimpl "dif/flows/impl"
	"dif/internal/mapping"
	stepdef "dif/steps/definition"
	stepimpl "dif/steps/impl"
)

func validateProcessorChecks(checks []flowimpl.ProcessorCheck) error {
	for _, c := range checks {
		if e := steps.ValidateActionTemplate(c.Node, c.Dynamic); e != nil {
			return fmt.Errorf("processor %q (%s): %w", c.Name, c.Origin, e)
		}
		if e := stepimpl.ValidateFunctionExpressions(c.Node); e != nil {
			return e
		}
	}
	return nil
}
func validatePreparedNode(n *flowdef.Node) error {
	if err := steps.ValidateRedacted(n); err != nil {
		return err
	}
	if err := stepimpl.ValidateFunctionExpressions(n); err != nil {
		return err
	}
	return nil
}

// parseFlow retains the single-flow API, while preparing new declarations before
// constructing any processor, including definitions that the flow does not use.
func parseFlow(data []byte, construct func(*flowdef.Node) (stepdef.Processor, error)) (*flowdef.Flow, error) {
	d, e := flowimpl.DecodeDocument(data)
	if e != nil {
		return nil, e
	}
	if len(d.Imports()) != 0 {
		return nil, fmt.Errorf("DIL imports require a program loader")
	}
	ctx := context.Background()
	if e = d.PrepareFunctions(ctx, mapping.Limits{}); e != nil {
		return nil, e
	}
	if e = d.PrepareMappings(ctx, mapping.Limits{}); e != nil {
		return nil, e
	}
	if e = d.PrepareServices(ctx, mapping.Limits{}); e != nil {
		return nil, e
	}
	checks, e := d.ProcessorChecks()
	if e != nil {
		return nil, e
	}
	if e = validateProcessorChecks(checks); e != nil {
		return nil, e
	}
	plans, e := d.Plans(nil)
	if e != nil {
		return nil, e
	}
	if len(plans) != 1 {
		return nil, fmt.Errorf("expected exactly one flow, found %d", len(plans))
	}
	if _, e = plans[0].Build(func(n *flowdef.Node) (stepdef.Processor, error) {
		if err := steps.Validate(n); err != nil {
			return nil, err
		}
		return nil, stepimpl.ValidateFunctionExpressions(n)
	}); e != nil {
		return nil, e
	}
	return plans[0].Build(construct)
}
