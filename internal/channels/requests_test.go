package channels

import (
	"sync"
	"testing"
	"time"

	"dif/message"
)

func requestRuntime(t *testing.T, c Config) *Runtime {
	t.Helper()
	r, err := Open(c)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := r.Close(); err != nil {
			t.Error(err)
		}
	})
	return r
}

func registerRequest(t *testing.T, r *Runtime, now time.Time, queue string) message.Message {
	t.Helper()
	m := message.New("document")
	m[message.RequestID] = m[message.MessageID]
	m[message.CorrelationID] = "shared-conversation"
	if err := r.RegisterRequest(m, queue, now.Add(time.Second), now, nil); err != nil {
		t.Fatal(err)
	}
	r.AdmitRequest(m[message.RequestID].(string))
	return m
}

func response(m message.Message) message.Message {
	out := m.Child("result")
	out[message.ReplyStatus] = "success"
	return out
}

func TestRequestMatchingAndTerminalRetention(t *testing.T) {
	r := requestRuntime(t, Config{Requests: RequestConfig{RetentionMS: 1000}})
	now := time.Unix(100, 0)
	a := registerRequest(t, r, now, "results")
	b := registerRequest(t, r, now, "results")
	for _, m := range []message.Message{b, a} {
		if why, err := r.MatchReply("wrong", response(m), now); why != "wrong-destination" || err != nil {
			t.Fatalf("wrong destination: %q %v", why, err)
		}
		if why, err := r.MatchReply("results", response(m), now); why != "" || err != nil {
			t.Fatalf("match: %q %v", why, err)
		}
		got, err := r.RequestOutcome("results", now)
		if err != nil || got[message.RequestID] != m[message.RequestID] {
			t.Fatalf("outcome: %v %v", got, err)
		}
		if next, _ := r.RequestOutcome("results", now); next != nil {
			t.Fatal("in-flight outcome delivered twice")
		}
		id := m[message.RequestID].(string)
		if err := r.FinishRequest(id, false, now); err != nil {
			t.Fatal(err)
		}
		retry, _ := r.RequestOutcome("results", now)
		if retry[message.MessageID] != got[message.MessageID] {
			t.Fatal("retry changed outcome identity")
		}
		if err := r.FinishRequest(id, true, now); err != nil {
			t.Fatal(err)
		}
		if why, _ := r.MatchReply("results", response(m), now); why != "duplicate" {
			t.Fatalf("duplicate: %q", why)
		}
	}
	if why, _ := r.MatchReply("results", response(a), now.Add(time.Second)); why != "unknown" {
		t.Fatalf("retention: %q", why)
	}
	if why, _ := r.MatchReply("results", message.New("bad"), now); why != "malformed" {
		t.Fatalf("malformed: %q", why)
	}
}

func TestRequestDeadlineRace(t *testing.T) {
	for _, atDeadline := range []bool{false, true} {
		r := requestRuntime(t, Config{})
		now := time.Unix(100, 0)
		m := registerRequest(t, r, now, "results")
		when := now.Add(time.Second - time.Nanosecond)
		want := "success"
		if atDeadline {
			when = now.Add(time.Second)
			want = "timeout"
		}
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); _, _ = r.MatchReply("results", response(m), when) }()
		var got message.Message
		go func() { defer wg.Done(); got, _ = r.RequestOutcome("results", when) }()
		wg.Wait()
		if got == nil {
			got, _ = r.RequestOutcome("results", when)
		}
		if got[message.ReplyStatus] != want {
			t.Fatalf("outcome = %v; want %s", got, want)
		}
		if atDeadline {
			if got[message.CausationID] != m[message.MessageID] || got[message.CorrelationID] != m[message.CorrelationID] {
				t.Fatal("timeout lost identity")
			}
			if why, _ := r.MatchReply("results", response(m), when); why != "late" {
				t.Fatalf("late: %q", why)
			}
		}
	}
}

func TestRequestAdmissionCapacityAndIsolation(t *testing.T) {
	r := requestRuntime(t, Config{Requests: RequestConfig{Capacity: 1}})
	now := time.Unix(100, 0)
	m := message.New(nil)
	m[message.RequestID] = m[message.MessageID]
	released := 0
	if err := r.RegisterRequest(m, "results", now.Add(time.Second), now, func() { released++ }); err != nil {
		t.Fatal(err)
	}
	if got, _ := r.RequestOutcome("results", now.Add(time.Hour)); got != nil {
		t.Fatal("unadmitted request expired")
	}
	other := message.New(nil)
	other[message.RequestID] = other[message.MessageID]
	if err := r.RegisterRequest(other, "results", now.Add(time.Second), now, nil); err == nil {
		t.Fatal("capacity ignored")
	}
	r.AbortRequest(m[message.RequestID].(string))
	if released != 1 {
		t.Fatal("abort did not release accounting")
	}
	if err := r.RegisterRequest(other, "results", now.Add(time.Second), now, nil); err != nil {
		t.Fatal(err)
	}
	isolated := requestRuntime(t, Config{})
	if why, _ := isolated.MatchReply("results", response(other), now); why != "unknown" {
		t.Fatal("runtime state leaked")
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := r.RequestOutcome("results", now); err == nil {
		t.Fatal("closed runtime accepted work")
	}
}

func TestReplyConsumerOwnership(t *testing.T) {
	r := requestRuntime(t, Config{})
	a, err := r.AcquireConsumer("results", false)
	if err != nil {
		t.Fatal(err)
	}
	b, err := r.AcquireConsumer("results", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.AcquireConsumer("results", true); err == nil {
		t.Fatal("reply source stole ordinary queue")
	}
	a()
	b()
	release, err := r.AcquireConsumer("results", true)
	if err != nil {
		t.Fatal(err)
	}
	for _, reply := range []bool{true, false} {
		if _, err := r.AcquireConsumer("results", reply); err == nil {
			t.Fatal("consumer stole reply queue")
		}
	}
	release()
	stop, err := r.AcquireConsumer("results", true)
	if err != nil {
		t.Fatal(err)
	}
	stop()
}
