package impl

import (
	"context"
	"time"

	"dif/message"
	stepdef "dif/steps/definition"
)

// timerSource emits a message every period with the counter (1, 2, 3, ...)
// as body. repeatCount limits the number of messages; 0 or less means
// unlimited. The counter source starts counting at start instead of 1 and
// sets Content-Type text/plain.
type timerSource struct {
	period      time.Duration
	repeatCount int
	offset      int    // added to the counter: start - 1
	contentType string // "" for none
}

func newTimerSource(_ string, p stepdef.Params) (stepdef.Processor, error) {
	return timerSource{
		period:      time.Duration(p["period"].(int)) * time.Millisecond,
		repeatCount: p["repeatCount"].(int),
	}, nil
}

func newCounterSource(_ string, p stepdef.Params) (stepdef.Processor, error) {
	return timerSource{
		period:      time.Duration(p["period"].(int)) * time.Millisecond,
		repeatCount: p["numbers"].(int),
		offset:      p["start"].(int) - 1,
		contentType: "text/plain",
	}, nil
}

func (t timerSource) Run(ctx context.Context, emit stepdef.Emit) error {
	return t.RunReady(ctx, emit, func() {})
}

func (t timerSource) RunReady(ctx context.Context, emit stepdef.Emit, ready func()) error {
	ticker := time.NewTicker(t.period)
	defer ticker.Stop()
	ready()
	for i := 1; t.repeatCount <= 0 || i <= t.repeatCount; i++ {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
		m := message.New(i + t.offset)
		if t.contentType != "" {
			m[message.ContentType] = t.contentType
		}
		if emit(m, nil) != nil {
			return nil // flow is stopping
		}
	}
	return nil
}
