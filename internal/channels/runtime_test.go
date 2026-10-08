package channels

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"dif/message"
)

func openTest(t *testing.T, c Config) *Runtime {
	t.Helper()
	r, err := Open(c)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	return r
}
func diskConfig(dir string) Config {
	return Config{Directory: dir, MaxMessageBytes: 4096, MaxDiskBytes: 64 << 10, Queues: map[string]QueueConfig{"jobs": {Durable: true, Capacity: 2, MaxDeliveries: 2, RetryDelayMS: 1}}, Idempotency: map[string]IdempotencyConfig{"orders": {Durable: true, MaxKeys: 2, RetentionMS: 10000}}}
}
func enqueue(t *testing.T, q *Queue, body any) {
	t.Helper()
	if err := q.Enqueue(context.Background(), Entry{Message: message.New(body)}, Policy{}); err != nil {
		t.Fatal(err)
	}
}
func take(t *testing.T, q *Queue) Entry {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	e, err := q.Take(ctx)
	if err != nil {
		t.Fatal(err)
	}
	return e
}
func waitFor(t *testing.T, f func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !f() {
		if time.Now().After(deadline) {
			t.Fatal("condition not reached")
		}
		time.Sleep(time.Millisecond)
	}
}

func TestAdmissionBlocksAndCancels(t *testing.T) {
	r := openTest(t, Config{Queues: map[string]QueueConfig{"q": {Capacity: 1}}})
	q := r.Queue("q")
	enqueue(t, q, 1)
	if err := q.Enqueue(context.Background(), Entry{Message: message.New(2)}, Policy{}); err == nil {
		t.Fatal("full queue accepted")
	}
	done := make(chan error, 1)
	go func() {
		done <- q.Enqueue(context.Background(), Entry{Message: message.New(2)}, Policy{Overflow: "block", Timeout: time.Second})
	}()
	waitFor(t, func() bool { return r.Snapshot().Queues[0].Blocked == 1 })
	if e := take(t, q); e.Message[message.Body] != 1 {
		t.Fatal(e)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := q.Enqueue(ctx, Entry{}, Policy{}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := q.Enqueue(context.Background(), Entry{}, Policy{Overflow: "block", Timeout: 5 * time.Millisecond}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if e := take(t, q); e.Message[message.Body] != 2 {
		t.Fatal(e)
	}
	if err := q.Enqueue(context.Background(), Entry{}, Policy{Overflow: "block"}); err == nil {
		t.Fatal("unbounded blocking accepted")
	}
}

func TestTopicBlockingMembershipAndAtomicAdmission(t *testing.T) {
	r := openTest(t, Config{Topics: map[string]TopicConfig{"events": {Capacity: 1}}})
	topic := r.Topic("events")
	a := topic.Subscribe(context.Background())
	defer topic.Unsubscribe(a)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b := topic.Subscribe(ctx)
	defer topic.Unsubscribe(b)
	enqueue(t, b.Queue, "full")
	done := make(chan error, 1)
	go func() {
		done <- topic.Publish(context.Background(), message.New("broadcast"), Policy{Overflow: "block", Timeout: time.Second}, nil)
	}()
	waitFor(t, func() bool { r.mu.Lock(); defer r.mu.Unlock(); return topic.blocked == 1 })
	if a.Queue.Depth() != 0 {
		t.Fatal("partial publication")
	}
	c := topic.Subscribe(context.Background())
	defer topic.Unsubscribe(c)
	cancel() // cancellation alone must wake the publisher, before Unsubscribe.
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	for _, s := range []*Subscription{a, c} {
		if got := take(t, s.Queue).Message[message.Body]; got != "broadcast" {
			t.Fatal(got)
		}
	}
	if got := take(t, b.Queue).Message[message.Body]; got != "full" {
		t.Fatal(got)
	}
}

func TestDurableRecoveryOriginalAndSettlement(t *testing.T) {
	c := diskConfig(t.TempDir())
	r := openTest(t, c)
	q := r.Queue("jobs")
	m := message.New([]byte{0, 1, 255})
	m["nested"] = map[string]any{"int": int64(9223372036854775807), "empty": []any{}, "nil": []byte(nil), "bool": true}
	if err := q.Enqueue(context.Background(), Entry{Message: m}, Policy{}); err != nil {
		t.Fatal(err)
	}
	e := take(t, q)
	e.Message[message.Body] = "mutated during processing"
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	r = openTest(t, c)
	q = r.Queue("jobs")
	e = take(t, q)
	if !reflect.DeepEqual(e.Message, m) {
		t.Fatalf("recovery changed message: %#v", e.Message)
	}
	if err := q.Complete(e.ID, nil); err != nil {
		t.Fatal(err)
	}
	r.Close()
	r = openTest(t, c)
	if r.Queue("jobs").Depth() != 0 {
		t.Fatal("acknowledged delivery replayed")
	}
}

func TestDurableRetryAndAtomicDeadLetter(t *testing.T) {
	c := diskConfig(t.TempDir())
	r := openTest(t, c)
	q := r.Queue("jobs")
	enqueue(t, q, "original")
	e := take(t, q)
	e.Message[message.Body] = "failed mutation"
	if err := q.Complete(e.ID, errors.New("first")); err != nil {
		t.Fatal(err)
	}
	e = take(t, q)
	if e.Message[message.Body] != "original" {
		t.Fatal("retry did not restore original")
	}
	if err := q.Complete(e.ID, errors.New("exhausted")); err != nil {
		t.Fatal(err)
	}
	r.Close()
	r = openTest(t, c)
	if r.Queue("jobs").Depth() != 0 {
		t.Fatal("DLQ move retained source")
	}
	e = take(t, r.Queue("jobs.DLQ"))
	if e.Message["error.message"] != "exhausted" || e.Message[message.Body] != "original" {
		t.Fatal(e.Message)
	}
}

func TestFullDeadLetterRetainsOriginal(t *testing.T) {
	c := diskConfig(t.TempDir())
	c.Queues["jobs"] = QueueConfig{Durable: true, Capacity: 2, MaxDeliveries: 1, DeadLetter: "dead"}
	c.Queues["dead"] = QueueConfig{Durable: true, Capacity: 1, DeadLetter: "dead"}
	r := openTest(t, c)
	enqueue(t, r.Queue("dead"), "existing")
	enqueue(t, r.Queue("jobs"), "kept")
	e := take(t, r.Queue("jobs"))
	if err := r.Queue("jobs").Complete(e.ID, errors.New("bad")); err == nil {
		t.Fatal("full DLQ accepted")
	}
	r.Close()
	r = openTest(t, c)
	if e := take(t, r.Queue("jobs")); e.Message[message.Body] != "kept" {
		t.Fatal(e)
	}
}

func TestJournalCompactionAndTailRecovery(t *testing.T) {
	c := diskConfig(t.TempDir())
	r := openTest(t, c)
	for i := 0; i < 50; i++ {
		enqueue(t, r.Queue("jobs"), strings.Repeat("x", 1000))
		e := take(t, r.Queue("jobs"))
		if err := r.Queue("jobs").Complete(e.ID, nil); err != nil {
			t.Fatal(err)
		}
	}
	enqueue(t, r.Queue("jobs"), "survives")
	r.Close()
	f, err := os.OpenFile(filepath.Join(c.Directory, "channels.journal"), os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	f.Write([]byte("DIF"))
	f.Close()
	r = openTest(t, c)
	if e := take(t, r.Queue("jobs")); e.Message[message.Body] != "survives" {
		t.Fatal(e)
	}
	if r.Snapshot().JournalBytes > c.MaxDiskBytes/2 {
		t.Fatal("journal exceeded bound")
	}
}

func TestJournalCorruptionFailsClosed(t *testing.T) {
	c := diskConfig(t.TempDir())
	r := openTest(t, c)
	enqueue(t, r.Queue("jobs"), "important")
	r.Close()
	p := filepath.Join(c.Directory, "channels.journal")
	data, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	data[len(data)-1] ^= 1
	if err := os.WriteFile(p, data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(c); err == nil || !strings.Contains(err.Error(), "corrupt") {
		t.Fatalf("corruption silently accepted: %v", err)
	}
}

func TestStorageOwnershipAndFailure(t *testing.T) {
	c := diskConfig(t.TempDir())
	r := openTest(t, c)
	if other, err := Open(c); err == nil {
		other.Close()
		t.Fatal("second owner accepted")
	}
	r.file.Close() // Simulate a failed write without relying on disk permissions.
	if err := r.Queue("jobs").Enqueue(context.Background(), Entry{Message: message.New(1)}, Policy{}); err == nil {
		t.Fatal("failed disk write reported success")
	}
	select {
	case <-r.Failed():
	default:
		t.Fatal("storage failure not signalled")
	}
	if r.Snapshot().StorageFailures != 1 {
		t.Fatal(r.Snapshot())
	}
}

func TestDurableSerializationAndLimits(t *testing.T) {
	r := openTest(t, diskConfig(t.TempDir()))
	q := r.Queue("jobs")
	for _, body := range []any{make(chan int), strings.Repeat("x", 5000)} {
		if err := q.Enqueue(context.Background(), Entry{Message: message.New(body)}, Policy{}); err == nil {
			t.Fatalf("accepted unsupported body %T", body)
		}
	}
	if q.Depth() != 0 {
		t.Fatal("rejected messages entered queue")
	}
	if err := q.Enqueue(context.Background(), Entry{Message: message.New(1), Reply: func(message.Message, error) {}}, Policy{}); err == nil {
		t.Fatal("durable reply callback accepted")
	}
}

func TestIdempotencyConcurrentFailureRecoveryAndRetention(t *testing.T) {
	c := diskConfig(t.TempDir())
	r := openTest(t, c)
	ctx := context.Background()
	owner, err := r.Claim(ctx, "orders", "one")
	if err != nil || !owner {
		t.Fatal(owner, err)
	}
	waitCtx, cancel := context.WithTimeout(ctx, 5*time.Millisecond)
	defer cancel()
	if _, err := r.Claim(waitCtx, "orders", "one"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if err := r.Finish("orders", "one", false); err != nil {
		t.Fatal(err)
	}
	owner, err = r.Claim(ctx, "orders", "one")
	if err != nil || !owner {
		t.Fatal(owner, err)
	}
	if err := r.Finish("orders", "one", true); err != nil {
		t.Fatal(err)
	}
	r.Claim(ctx, "orders", "unfinished")
	r.Close()
	r = openTest(t, c)
	if owner, err := r.Claim(ctx, "orders", "one"); err != nil || owner {
		t.Fatal("completed key not recovered", owner, err)
	}
	if owner, err := r.Claim(ctx, "orders", "unfinished"); err != nil || !owner {
		t.Fatal("pending key not released", owner, err)
	}
	r.Finish("orders", "unfinished", false)
	var wg sync.WaitGroup
	owners := make(chan bool, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			owner, err := r.Claim(ctx, "orders", "concurrent")
			if err != nil {
				t.Error(err)
				return
			}
			owners <- owner
			if owner {
				if err := r.Finish("orders", "concurrent", true); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	wg.Wait()
	close(owners)
	n := 0
	for owner := range owners {
		if owner {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d owners", n)
	}
	if _, err := r.Claim(ctx, "orders", "full"); err == nil {
		t.Fatal("key capacity ignored")
	}
	r.mu.Lock()
	r.keys["orders"]["one"].expires = time.Now().Add(-time.Second).UnixMilli()
	r.mu.Unlock()
	if owner, err := r.Claim(ctx, "orders", "one"); err != nil || !owner {
		t.Fatal("expired key suppressed", owner, err)
	}
}

// This helper deliberately exits without closing storage, simulating process
// failure after acceptance/reservation/acknowledgement/transfer.
func TestCrashHelper(t *testing.T) {
	dir := os.Getenv("DIF_CHANNEL_CRASH_DIR")
	if dir == "" {
		return
	}
	r, err := Open(diskConfig(dir))
	if err != nil {
		t.Fatal(err)
	}
	q := r.Queue("jobs")
	if err := q.Enqueue(context.Background(), Entry{Message: message.New("crash")}, Policy{}); err != nil {
		t.Fatal(err)
	}
	stage := os.Getenv("DIF_CHANNEL_CRASH_STAGE")
	if stage != "enqueue" {
		e, err := q.Take(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if stage == "ack" {
			if err := q.Complete(e.ID, nil); err != nil {
				t.Fatal(err)
			}
		}
		if stage == "move" {
			q.Complete(e.ID, errors.New("retry"))
			e, err = q.Take(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if err := q.Complete(e.ID, errors.New("move")); err != nil {
				t.Fatal(err)
			}
		}
	}
	os.Exit(23)
}

func TestProcessCrashRecovery(t *testing.T) {
	for _, stage := range []string{"enqueue", "reserve", "ack", "move"} {
		t.Run(stage, func(t *testing.T) {
			dir := t.TempDir()
			cmd := exec.Command(os.Args[0], "-test.run=^TestCrashHelper$")
			cmd.Env = append(os.Environ(), "DIF_CHANNEL_CRASH_DIR="+dir, "DIF_CHANNEL_CRASH_STAGE="+stage)
			out, err := cmd.CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 23 {
				t.Fatalf("helper: %v %s", err, out)
			}
			r := openTest(t, diskConfig(dir))
			want := 1
			if stage == "ack" || stage == "move" {
				want = 0
			}
			if r.Queue("jobs").Depth() != want {
				t.Fatal(r.Snapshot())
			}
			if stage == "move" && r.Queue("jobs.DLQ").Depth() != 1 {
				t.Fatal("lost transfer")
			}
		})
	}
}
