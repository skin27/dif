package impl

import (
	"context"
	"testing"
	"time"

	flowdef "dif/flows/definition"
	"dif/message"
	stepdef "dif/steps/definition"
)

// collect runs src and returns the bodies it emitted.
func collect(t *testing.T, ctx context.Context, src stepdef.Source) []any {
	t.Helper()
	var bodies []any
	err := src.Run(ctx, func(m *message.Message) error {
		bodies = append(bodies, m.Body)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return bodies
}

func TestTimerCount(t *testing.T) {
	got := collect(t, context.Background(), Timer{Period: time.Millisecond, Count: 3})
	if len(got) != 3 || got[0] != 1 || got[1] != 2 || got[2] != 3 {
		t.Errorf("bodies = %v, want [1 2 3]", got)
	}
}

func TestTimerStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	done := make(chan struct{})
	go func() {
		Timer{Period: time.Millisecond}.Run(ctx, func(*message.Message) error { return nil })
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("unlimited timer did not stop on cancel")
	}
}

func TestNewSource(t *testing.T) {
	src, err := NewSource(&flowdef.Node{URI: "timer:tick", Options: map[string]any{"period": "5", "numbers": 2.0}})
	if err != nil {
		t.Fatal(err)
	}
	if want := (Timer{Period: 5 * time.Millisecond, Count: 2}); src != want {
		t.Errorf("source = %+v, want %+v", src, want)
	}

	src, err = NewSource(&flowdef.Node{URI: "https://0.0.0.0:9001/x"})
	if err != nil || src != nil {
		t.Errorf("non-timer source = %v, %v; want nil, nil", src, err)
	}

	if _, err := NewSource(&flowdef.Node{URI: "timer", Options: map[string]any{"period": "x"}}); err == nil {
		t.Error("invalid period: want error")
	}
	if _, err := NewSource(&flowdef.Node{URI: "timer", Options: map[string]any{"period": 0.0}}); err == nil {
		t.Error("zero period: want error")
	}
}

func TestIntOption(t *testing.T) {
	opts := map[string]any{"num": 5.0, "str": "5", "bad": "x", "bool": true}
	for key, want := range map[string]int{"num": 5, "str": 5, "absent": 7} {
		if got, err := intOption(opts, key, 7); err != nil || got != want {
			t.Errorf("%s = %d, %v; want %d", key, got, err, want)
		}
	}
	for _, key := range []string{"bad", "bool"} {
		if _, err := intOption(opts, key, 7); err == nil {
			t.Errorf("%s: want error", key)
		}
	}
}
