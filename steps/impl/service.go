package impl

import (
	"context"
	"fmt"
	"time"

	flowdef "dif/flows/definition"
	"dif/internal/channels"
	"dif/internal/mapping"
	"dif/message"
	stepdef "dif/steps/definition"
)

type serviceSource struct {
	queueSource
}

func serviceBinding(p stepdef.Params) (*mapping.ServiceBinding, error) {
	sb, ok := p[mapping.ServiceBindingKey].(*mapping.ServiceBinding)
	if !ok || sb == nil || sb.ID == "" || sb.Plan == nil {
		return nil, fmt.Errorf("service requires a prepared core service")
	}
	return sb, nil
}

func newServiceSource(_ string, p stepdef.Params) (stepdef.Processor, error) {
	sb, err := serviceBinding(p)
	if err != nil {
		return nil, err
	}
	q := channelRuntime(p).queue("service:" + sb.ID)
	if q.core.Durable() {
		return nil, fmt.Errorf("service endpoints must use memory storage")
	}
	return serviceSource{queueSource{q}}, nil
}

type serviceAction struct {
	target  *memQueue
	name    string
	plan    *mapping.ServicePlan
	wait    bool
	inOut   bool
	timeout time.Duration
	policy  channels.Policy
}

func newServiceCaller(p stepdef.Params, isSink bool) (serviceAction, error) {
	sb, err := serviceBinding(p)
	if err != nil {
		return serviceAction{}, err
	}
	inOut := sb.Plan.ExchangePattern() == message.InOut
	if inOut && isSink {
		return serviceAction{}, fmt.Errorf("InOut service %q requires an action step", sb.Name)
	}
	delivery, _ := p["delivery"].(string)
	if inOut && delivery == "enqueue" {
		return serviceAction{}, fmt.Errorf("InOut service %q does not support delivery=enqueue", sb.Name)
	}
	policy, err := admissionPolicy(p)
	if err != nil {
		return serviceAction{}, err
	}
	q := channelRuntime(p).queue("service:" + sb.ID)
	if q.core.Durable() {
		return serviceAction{}, fmt.Errorf("service endpoints must use memory storage")
	}
	return serviceAction{
		target:  q,
		name:    sb.Name,
		plan:    sb.Plan,
		wait:    inOut || delivery != "enqueue",
		inOut:   inOut,
		timeout: requestTimeout(p),
		policy:  policy,
	}, nil
}

func newServiceAction(_ string, p stepdef.Params) (stepdef.Processor, error) {
	return newServiceCaller(p, false)
}

type serviceSink struct {
	serviceAction
}

func newServiceSink(_ string, p stepdef.Params) (stepdef.Processor, error) {
	a, err := newServiceCaller(p, true)
	if err != nil {
		return nil, err
	}
	return serviceSink{a}, nil
}

func (a serviceAction) Process(ctx context.Context, m message.Message) (message.Message, error) {
	req := m.Copy()
	if err := a.plan.ValidateInput(ctx, map[string]any(req), false); err != nil {
		return nil, &flowdef.ContractError{
			Service: a.name,
			Kind:    "input",
			Reason:  err.Error(),
		}
	}
	if !a.wait {
		return m, a.target.putPolicy(ctx, queued{m: req}, a.policy)
	}

	wait, cancel := context.WithTimeout(ctx, a.timeout)
	defer cancel()
	replies := make(chan linkReply, 1)
	err := a.target.putPolicy(wait, queued{
		m:     req,
		reply: func(out message.Message, err error) { replies <- linkReply{out, err} },
		wait:  wait,
	}, a.policy)
	if err != nil {
		return nil, err
	}

	select {
	case r := <-replies:
		switch {
		case r.err != nil:
			return nil, fmt.Errorf("service %s: %w", a.name, r.err)
		case a.inOut:
			return r.m, nil
		}
		return m, nil
	case <-wait.Done():
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("service %s did not reply within %v", a.name, a.timeout)
	}
}

func (a serviceAction) LocalTargets() []string { return []string{"queue:" + a.target.name} }

func (s serviceSink) Consume(ctx context.Context, m message.Message) error {
	_, err := s.Process(ctx, m)
	return err
}
