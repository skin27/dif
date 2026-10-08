package channels

import (
	"context"
	"fmt"
	"time"
)

type keyState struct {
	pending bool
	expires int64
}

func (r *Runtime) expireKeys() {
	now := time.Now().UnixMilli()
	for _, keys := range r.keys {
		for key, state := range keys {
			if !state.pending && state.expires <= now {
				delete(keys, key)
			}
		}
	}
}

func (r *Runtime) HasNamespace(namespace string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.keys[namespace]
	return ok
}

// Claim serializes duplicates. A waiting duplicate becomes the owner after a
// failure, or is suppressed after success. Reservations are intentionally not
// persisted: a crash releases them; only completed keys survive recovery.
func (r *Runtime) Claim(ctx context.Context, namespace, key string) (bool, error) {
	if key == "" || len(key) > 4096 {
		return false, fmt.Errorf("idempotency key must contain 1..4096 bytes")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		if err := r.status(); err != nil {
			return false, err
		}
		keys, ok := r.keys[namespace]
		if !ok {
			return false, fmt.Errorf("unknown idempotency namespace %q", namespace)
		}
		r.expireKeys()
		state := keys[key]
		if state == nil {
			if len(keys) >= r.config.Idempotency[namespace].MaxKeys {
				return false, fmt.Errorf("idempotency namespace %q is full", namespace)
			}
			keys[key] = &keyState{pending: true}
			return true, nil
		}
		if !state.pending {
			return false, nil
		}
		changed := r.changed
		r.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
		}
		r.mu.Lock()
	}
}

func (r *Runtime) Finish(namespace, key string, success bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.status(); err != nil {
		return err
	}
	state := r.keys[namespace][key]
	if state == nil || !state.pending {
		return fmt.Errorf("idempotency key not reserved")
	}
	if !success {
		delete(r.keys[namespace], key)
		r.notify()
		return nil
	}
	cfg := r.config.Idempotency[namespace]
	expires := time.Now().Add(time.Duration(cfg.RetentionMS) * time.Millisecond).UnixMilli()
	if cfg.Durable {
		if err := r.append(record{Op: "key", Queue: namespace, Key: key, Expires: expires}); err != nil {
			delete(r.keys[namespace], key)
			r.notify()
			return err
		}
	}
	state.pending, state.expires = false, expires
	r.notify()
	return nil
}
