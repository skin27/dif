package engine

import (
	"context"
	"errors"
	"fmt"
	"time"

	stepdef "dif/steps/definition"
)

// StartGroup initializes consumers first and activates only after all sources
// are ready. On failure it cancels only runners started by this attempt.
// Callers serialize lifecycle operations on the supplied stopped runners.
func StartGroup(ctx context.Context, runners []*Runner) (err error) {
	seen := map[string]bool{}
	for _, r := range runners {
		if r == nil {
			return fmt.Errorf("group contains a nil runner")
		}
		if seen[r.ID()] {
			return fmt.Errorf("duplicate flow id %q in startup group", r.ID())
		}
		seen[r.ID()] = true
		if r.State() != Stopped {
			return fmt.Errorf("flow %q must be stopped before group startup", r.ID())
		}
	}
	activate := make(chan struct{})
	ctx = stepdef.WithActivation(ctx, activate)
	started := []*Runner{}
	defer func() {
		if err == nil {
			return
		}
		for _, r := range started {
			r.Cancel()
		}
		cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		for _, r := range started {
			err = errors.Join(err, r.StopContext(cleanup))
		}
	}()
	for _, internal := range []bool{true, false} {
		for _, r := range runners {
			input, _ := r.LocalChannels()
			if (input != "") != internal {
				continue
			}
			started = append(started, r)
			if err := r.StartContext(ctx); err != nil {
				return fmt.Errorf("flow %q startup failed: %w", r.ID(), err)
			}
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, r := range runners {
		if r.SourceError() != nil {
			return fmt.Errorf("flow %q source failed during startup", r.ID())
		}
	}
	close(activate)
	return nil
}
