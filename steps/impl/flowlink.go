package impl

import (
	"context"
	"dif/internal/channels"
	"fmt"
	"time"

	"dif/message"
	stepdef "dif/steps/definition"
)

// Flow links send messages from one flow to another within a dif process.
// Every flow with a flowlink source has an endpoint: an in-memory queue
// named after its flow id. A message sent to a flow that is not started yet
// waits on the queue until it starts.

// linkEndpoint returns the endpoint of the flow flowID.
func linkEndpoint(flowID string) *memQueue { return queueNamed("flowlink:" + flowID) }

// flowLinkSource emits the messages other flows send to this flow, and gives
// a sender that waits the outcome.
type flowLinkSource struct {
	queueSource
}

func newFlowLinkSource(_ string, p stepdef.Params) (stepdef.Processor, error) {
	id := p["flowId"].(string)
	if id == "" {
		return nil, fmt.Errorf("option flowId: empty flow id")
	}
	q := channelRuntime(p).queue("flowlink:" + id)
	if q.core.Durable() {
		return nil, fmt.Errorf("flowlink endpoints must use memory storage")
	}
	return flowLinkSource{queueSource{q}}, nil
}

// flowLinkAction sends a copy of the message to the flow targetFlowId:
//
//   - async, InOnly: it does not wait; the message passes on unchanged
//   - sync, InOnly: it waits until the target flow has processed the message;
//     the message passes on unchanged, or fails if the target failed it
//   - InOut: it waits for the target flow's outcome, which replaces the message
//
// Waiting ends after requestTimeout; a message the target has not taken by
// then is dropped.
type flowLinkAction struct {
	target   *memQueue
	targetID string
	wait     bool // for the target's outcome
	inOut    bool // the outcome replaces the message
	timeout  time.Duration
	policy   channels.Policy
}

func newFlowLinkAction(_ string, p stepdef.Params) (stepdef.Processor, error) {
	id := p["targetFlowId"].(string)
	if id == "" {
		return nil, fmt.Errorf("option targetFlowId: empty flow id")
	}
	sync := map[string]bool{"sync": true, "direct": true, "vm": true}[p["transport"].(string)]
	inOut := p["exchangePattern"] == "InOut"
	policy, err := admissionPolicy(p)
	if err != nil {
		return nil, err
	}
	q := channelRuntime(p).queue("flowlink:" + id)
	if q.core.Durable() {
		return nil, fmt.Errorf("flowlink endpoints must use memory storage")
	}
	return flowLinkAction{
		target:   q,
		policy:   policy,
		targetID: id,
		wait:     sync || inOut,
		inOut:    inOut,
		timeout:  requestTimeout(p),
	}, nil
}

// requestTimeout returns the time to wait for the target: the option
// requestTimout, the designer's spelling, else requestTimeout.
func requestTimeout(p stepdef.Params) time.Duration {
	ms, ok := p["requestTimout"].(int)
	if !ok {
		ms = p["requestTimeout"].(int)
	}
	return time.Duration(ms) * time.Millisecond
}

type linkReply struct {
	m   message.Message
	err error
}

func (a flowLinkAction) Process(ctx context.Context, m message.Message) (message.Message, error) {
	if !a.wait {
		return m, a.target.putPolicy(ctx, queued{m: m.Copy()}, a.policy)
	}

	wait, cancel := context.WithTimeout(ctx, a.timeout)
	defer cancel() // also drops the message if the target has not taken it
	replies := make(chan linkReply, 1)
	err := a.target.putPolicy(wait, queued{
		m:     m.Copy(),
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
			return nil, fmt.Errorf("flow %s: %w", a.targetID, r.err)
		case a.inOut:
			return r.m, nil
		}
		return m, nil
	case <-wait.Done():
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("flow %s did not reply within %v", a.targetID, a.timeout)
	}
}

func (a flowLinkAction) LocalTargets() []string { return []string{"queue:" + a.target.name} }
