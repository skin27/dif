package engine

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestAddAllRejectsWithoutPartialRegistration(t *testing.T) {
	e := New()
	original := newFlow("existing", nil)
	must(t, e.Add(original))
	for _, batch := range [][]*Runner{
		{newFlow("new", nil), newFlow("existing", nil)},
		{newFlow("new", nil), newFlow("new", nil)},
		{newFlow("new", nil), nil},
	} {
		if err := e.AddAll(batch); err == nil {
			t.Fatal("invalid batch accepted")
		}
		if len(e.ListFlows("")) != 1 {
			t.Fatal("partial registration")
		}
	}
	must(t, e.AddAll([]*Runner{newFlow("a", nil), newFlow("b", nil)}))
	if len(e.ListFlows("")) != 3 {
		t.Fatal("group not registered")
	}
}

func TestStartGroupFailureRollsBackOnlyItsSources(t *testing.T) {
	other := newFlow("other", nil)
	must(t, other.Start())
	defer other.ForceStop()
	first := newFlow("first", nil)
	f := flow(readyTestSource{entered: make(chan struct{}), err: errors.New("bind failed")})
	f.ID = "bad"
	bad := NewRunner(f, nil)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := StartGroup(ctx, []*Runner{first, bad}); err == nil {
		t.Fatal("source failure accepted")
	}
	if first.State() != Stopped || bad.State() != Stopped {
		t.Fatalf("partial group left active: %s/%s", first.State(), bad.State())
	}
	if other.State() != Started {
		t.Fatal("unrelated flow stopped")
	}
}

func TestStartGroupCancellationAndAlreadyRunning(t *testing.T) {
	r := newFlow("a", nil)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := StartGroup(ctx, []*Runner{r}); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	if r.State() != Stopped {
		t.Fatal("cancelled startup active")
	}
	must(t, r.Start())
	defer r.ForceStop()
	if err := StartGroup(context.Background(), []*Runner{newFlow("b", nil), r}); err == nil {
		t.Fatal("already running accepted")
	}
	if r.State() != Started {
		t.Fatal("preflight stopped running flow")
	}
}

func TestStartGroupRejectsInvalidBatchBeforeStarting(t *testing.T) {
	r := newFlow("a", nil)
	for _, group := range [][]*Runner{{r, nil}, {r, r}, {r, newFlow("a", nil)}} {
		if err := StartGroup(context.Background(), group); err == nil {
			t.Fatal("invalid startup group accepted")
		}
		if r.State() != Stopped {
			t.Fatal("invalid batch partially started")
		}
	}
}
