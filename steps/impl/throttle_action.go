package impl

import (
	"context"
	"time"

	"dif/message"
	stepdef "dif/steps/definition"
)

// throttleAction lets at most maxRequests messages pass per timePeriod (a
// sliding window). A message beyond that waits until the oldest pass in the
// window has expired; messages wait their turn one at a time.
type throttleAction struct {
	period time.Duration
	turn   chan struct{} // held by the message whose turn it is
	passed []time.Time   // ring of the last maxRequests pass times; zero when unused
	next   int           // index of the oldest pass in passed
}

func newThrottleAction(_ string, p stepdef.Params) (stepdef.Processor, error) {
	return &throttleAction{
		period: time.Duration(p["timePeriod"].(int)) * time.Millisecond,
		turn:   make(chan struct{}, 1),
		passed: make([]time.Time, p["maxRequests"].(int)),
	}, nil
}

func (a *throttleAction) Process(ctx context.Context, m message.Message) (message.Message, error) {
	select {
	case a.turn <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-a.turn }()

	if wait := time.Until(a.passed[a.next].Add(a.period)); wait > 0 {
		t := time.NewTimer(wait)
		defer t.Stop()
		select {
		case <-t.C:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	a.passed[a.next] = time.Now()
	a.next = (a.next + 1) % len(a.passed)
	return m, nil
}
