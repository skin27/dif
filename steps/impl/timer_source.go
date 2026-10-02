package impl

import (
	"context"
	"time"

	"dif/message"
	stepdef "dif/steps/definition"
)

// timerSource emits a message every period with the counter (1, 2, 3, ...)
// as body. repeatCount limits the number of messages; 0 means unlimited.
type timerSource struct {
	period      time.Duration
	repeatCount int
}

func newTimerSource(_ string, p stepdef.Params) (stepdef.Processor, error) {
	return timerSource{
		period:      time.Duration(p["period"].(int)) * time.Millisecond,
		repeatCount: p["repeatCount"].(int),
	}, nil
}

func (t timerSource) Run(ctx context.Context, emit stepdef.Emit) error {
	ticker := time.NewTicker(t.period)
	defer ticker.Stop()

	for i := 1; t.repeatCount == 0 || i <= t.repeatCount; i++ {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
		if emit(message.New(i), nil) != nil {
			return nil // flow is stopping
		}
	}
	return nil
}
