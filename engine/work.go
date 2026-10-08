package engine

import (
	"context"
	"sync"
)

// Work tracks work across a group of runners and their internal queues.
// Wait is used only after external producers have stopped. Until then a zero
// count is transient and does not mean the group has finished.
type Work struct {
	mu             sync.Mutex
	active, queued int64
	changed        chan struct{}
}

func (w *Work) BeginWork(queued bool) func() {
	w.mu.Lock()
	if queued {
		w.queued++
	} else {
		w.active++
	}
	w.mu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			w.mu.Lock()
			defer w.mu.Unlock()
			if queued {
				w.queued--
			} else {
				w.active--
			}
			if w.changed != nil {
				close(w.changed)
				w.changed = nil
			}
		})
	}
}

func (w *Work) Counts() (active, queued int64) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.active, w.queued
}

func (w *Work) Wait(ctx context.Context) error {
	for {
		w.mu.Lock()
		if w.active+w.queued == 0 {
			w.mu.Unlock()
			return nil
		}
		if w.changed == nil {
			w.changed = make(chan struct{})
		}
		changed := w.changed
		w.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
