package impl

import (
	"context"
	"errors"
	"testing"
	"time"

	"dif/message"
	stepdef "dif/steps/definition"
)

func timer(t *testing.T, opts map[string]any) stepdef.SourceProcessor {
	t.Helper()
	return mustProcessor(t, stepdef.Source, "timer:tick", opts).(stepdef.SourceProcessor)
}

func TestTimerRepeatCount(t *testing.T) {
	var bodies []any
	err := timer(t, map[string]any{"period": 1.0, "repeatCount": "3"}).Run(context.Background(), func(m message.Message, _ func(message.Message, error)) error {
		bodies = append(bodies, m[message.Body])
		if m[message.TraceID] == nil {
			t.Error("message has no trace id")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 3 || bodies[0] != 1 || bodies[1] != 2 || bodies[2] != 3 {
		t.Errorf("bodies = %v, want [1 2 3]", bodies)
	}
}

func TestTimerPeriod(t *testing.T) {
	start := time.Now()
	timer(t, map[string]any{"period": 30.0, "repeatCount": 2.0}).Run(context.Background(), func(message.Message, func(message.Message, error)) error { return nil })
	if d := time.Since(start); d < 60*time.Millisecond {
		t.Errorf("2 ticks of 30ms took %v, want at least 60ms", d)
	}
}

func TestTimerDefaults(t *testing.T) {
	if got, want := timer(t, nil), (timerSource{period: time.Second}); got != want {
		t.Errorf("timer = %+v, want %+v", got, want)
	}
}

func TestTimerStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	go func() {
		timer(t, map[string]any{"period": 1.0}).Run(ctx, func(message.Message, func(message.Message, error)) error { return nil })
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("unlimited timer did not stop on cancel")
	}
}

func TestTimerStopsWhenFlowStops(t *testing.T) {
	n := 0
	timer(t, map[string]any{"period": 1.0}).Run(context.Background(), func(message.Message, func(message.Message, error)) error {
		n++
		return errors.New("flow is stopping")
	})
	if n != 1 {
		t.Errorf("emitted %d messages, want 1", n)
	}
}

func TestTimerInvalid(t *testing.T) {
	wantInvalid(t, stepdef.Source, "timer:tick", map[string]any{"period": "x"}, `option period: want integer, got "x"`)
	wantInvalid(t, stepdef.Source, "timer:tick", map[string]any{"period": 0.0}, "option period: 0 is less than 1")
	wantInvalid(t, stepdef.Source, "timer:tick", map[string]any{"repeatCount": 1.5}, "option repeatCount: want integer")
	wantInvalid(t, stepdef.Source, "timer:tick", map[string]any{"numbers": 2.0}, "unknown option numbers")
}

func TestRepeater(t *testing.T) {
	if got, want := mustProcessor(t, stepdef.Source, "repeater", nil), (timerSource{period: 10 * time.Second}); got != want {
		t.Errorf("repeater = %+v, want %+v", got, want)
	}
	var bodies []any
	mustProcessor(t, stepdef.Source, "repeater", map[string]any{"period": "1", "repeatCount": "2"}).(stepdef.SourceProcessor).Run(context.Background(), func(m message.Message, _ func(message.Message, error)) error {
		bodies = append(bodies, m[message.Body])
		return nil
	})
	if len(bodies) != 2 {
		t.Errorf("bodies = %v, want 2", bodies)
	}
}
