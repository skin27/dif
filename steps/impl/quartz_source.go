package impl

import (
	"context"
	"fmt"
	"time"

	"dif/message"
	stepdef "dif/steps/definition"
)

// QuartzFireTime is the header with the time a quartz source fired for (RFC 3339).
const QuartzFireTime = "quartz.firetime"

// quartzSource emits a message, without a body, at every time its cron
// expression matches: a batch flow's schedule. A time missed while the flow
// was busy or paused is skipped, not caught up.
type quartzSource struct {
	schedule *cronSchedule
}

func newQuartzSource(_ string, p stepdef.Params) (stepdef.Processor, error) {
	loc := time.Local
	if tz := p["timeZone"].(string); tz != "" {
		var err error
		if loc, err = time.LoadLocation(tz); err != nil {
			return nil, fmt.Errorf("option timeZone: %w", err)
		}
	}
	s, err := parseCron(p["cron"].(string), loc)
	if err != nil {
		return nil, err
	}
	if s.next(time.Now()).IsZero() {
		return nil, fmt.Errorf("cron %q never fires", p["cron"])
	}
	return quartzSource{s}, nil
}

func (q quartzSource) Run(ctx context.Context, emit stepdef.Emit) error {
	for {
		at := q.schedule.next(time.Now())
		if at.IsZero() {
			return nil
		}
		t := time.NewTimer(time.Until(at))
		select {
		case <-ctx.Done():
			t.Stop()
			return nil
		case <-t.C:
		}
		m := message.New(nil)
		m[QuartzFireTime] = at.Format(time.RFC3339)
		if emit(m, nil) != nil {
			return nil // flow is stopping
		}
	}
}
