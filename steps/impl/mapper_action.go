package impl

import (
	"context"
	"dif/internal/mapping"
	"dif/message"
	stepdef "dif/steps/definition"
	"fmt"
)

type mapperAction struct{ plan *mapping.Plan }

func newMapperAction(_ string, p stepdef.Params) (stepdef.Processor, error) {
	plan, ok := p[mapping.BindingKey].(*mapping.Plan)
	if !ok || plan == nil {
		return nil, fmt.Errorf("mapper requires a prepared core map")
	}
	return mapperAction{plan}, nil
}
func (a mapperAction) Process(ctx context.Context, m message.Message) (message.Message, error) {
	ct := ""
	if v, ok := m[message.ContentType]; ok {
		var valid bool
		ct, valid = v.(string)
		if !valid {
			return nil, fmt.Errorf("Content-Type must be text")
		}
	}
	out, err := a.plan.Apply(ctx, m[message.Body], ct, map[string]any(m))
	if err != nil {
		return nil, err
	}
	m[message.Body] = out
	m[message.ContentType] = a.plan.ContentType()
	return m, nil
}
