package impl

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"dif/message"
	stepdef "dif/steps/definition"
)

// State is keyed by the entering map's identity, not Message-Id: two deliveries
// of the same logical message may be running concurrently. It never enters the
// message, so nested durable enqueues do not persist processor-private values.
type idempotentRouter struct {
	runtime   *Channels
	namespace string
	key       expression
	timeout   time.Duration
	claims    sync.Map
}
type claim struct {
	key   string
	owner bool
}

func newIdempotentRouter(_ string, p stepdef.Params) (stepdef.Processor, error) {
	if len(p[stepdef.Links].([]stepdef.Link)) != 1 {
		return nil, fmt.Errorf("idempotent needs one outbound link containing the guarded processing")
	}
	r := channelRuntime(p)
	namespace := p["namespace"].(string)
	if !r.HasNamespace(namespace) {
		return nil, fmt.Errorf("configure idempotency namespace %q in the channel runtime", namespace)
	}
	key, err := compileExpression("simple", p["key"].(string))
	if err != nil {
		return nil, err
	}
	return &idempotentRouter{runtime: r, namespace: namespace, key: key, timeout: time.Duration(p["claimTimeout"].(int)) * time.Millisecond}, nil
}

func (r *idempotentRouter) Route(ctx context.Context, m message.Message) ([]stepdef.Route, error) {
	key, err := r.key.eval(m)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()
	owner, err := r.runtime.Claim(ctx, r.namespace, key)
	if err != nil {
		return nil, err
	}
	r.claims.Store(fmt.Sprintf("%p", m), claim{key, owner})
	if !owner {
		return nil, nil
	}
	return []stepdef.Route{{Next: 0, Message: m}}, nil
}

func (r *idempotentRouter) Gather(ctx context.Context, m message.Message, outcomes []stepdef.Outcome) ([]stepdef.Route, error) {
	v, ok := r.claims.LoadAndDelete(fmt.Sprintf("%p", m))
	if !ok {
		return nil, fmt.Errorf("missing idempotency claim")
	}
	c := v.(claim)
	if !c.owner {
		return nil, nil
	}
	var err error
	if len(outcomes) != 1 {
		err = fmt.Errorf("idempotent expected one branch result")
	} else {
		err = outcomes[0].Err
	}
	if err == nil {
		err = ctx.Err()
	}
	finishErr := r.runtime.Finish(r.namespace, c.key, err == nil)
	if err != nil || finishErr != nil {
		return nil, errors.Join(err, finishErr)
	}
	// Preserve a processor's replacement map as the router outcome.
	if out := outcomes[0].Message; out != nil {
		copy := out.Copy()
		clear(m)
		for k, v := range copy {
			m[k] = v
		}
	}
	return nil, nil
}

func (r *idempotentRouter) AbortGather(m message.Message) error {
	v, ok := r.claims.LoadAndDelete(fmt.Sprintf("%p", m))
	if !ok {
		return nil
	}
	c := v.(claim)
	if c.owner {
		return r.runtime.Finish(r.namespace, c.key, false)
	}
	return nil
}
