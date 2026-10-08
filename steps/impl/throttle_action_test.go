package impl

import (
	"context"
	"errors"
	"testing"
	"time"

	"dif/message"
	stepdef "dif/steps/definition"
)

func TestThrottle(t *testing.T) {
	p := mustProcessor(t, stepdef.Action, "throttle", map[string]any{"maxRequests": "2", "timePeriod": "50"}).(stepdef.ActionProcessor)
	start := time.Now()
	var passed []time.Duration
	for range 5 {
		if _, err := p.Process(context.Background(), message.New("x")); err != nil {
			t.Fatal(err)
		}
		passed = append(passed, time.Since(start))
	}
	// 2 pass at once, the next 2 after 50ms, the last after 100ms.
	if passed[1] > 40*time.Millisecond || passed[2] < 50*time.Millisecond || passed[4] < 100*time.Millisecond {
		t.Errorf("messages passed at %v", passed)
	}
}

func TestThrottleCancel(t *testing.T) {
	p := mustProcessor(t, stepdef.Action, "throttle", map[string]any{"maxRequests": 1, "timePeriod": 60000}).(stepdef.ActionProcessor)
	if _, err := p.Process(context.Background(), message.New("x")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err := p.Process(ctx, message.New("y")); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want the context's error", err)
	}
}

func TestThrottleInvalid(t *testing.T) {
	wantInvalid(t, stepdef.Action, "throttle", nil, "missing required option maxRequests")
	wantInvalid(t, stepdef.Action, "throttle", map[string]any{"maxRequests": 0}, "option maxRequests: 0 is less than 1")
	wantInvalid(t, stepdef.Action, "throttle", map[string]any{"maxRequests": 1, "timePeriod": 0}, "option timePeriod: 0 is less than 1")
}
