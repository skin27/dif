package api

import "testing"

// TestHello is the end-to-end test: JSON -> parse -> steps -> engine -> Message.
func TestHello(t *testing.T) {
	var results []*Result
	f, err := Load("../examples/hello.json", func(res *Result, err error) {
		if err != nil {
			t.Error(err)
			return
		}
		results = append(results, res)
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Start(); err != nil {
		t.Fatal(err)
	}
	if err := f.Send(f.NewMessage()); err != nil {
		t.Fatal(err)
	}
	if f.State() != Started {
		t.Errorf("state = %s, want %s: a flow runs until it is stopped", f.State(), Started)
	}
	if err := f.Stop(); err != nil {
		t.Fatal(err)
	}

	if len(results) != 1 {
		t.Fatalf("got %d results, want 1", len(results))
	}
	res := results[0]
	if res.Message.Body != "HELLO WORLD" {
		t.Errorf("body = %v, want HELLO WORLD", res.Message.Body)
	}
	if got := res.Message.Headers["greeting"]; got != "hello" {
		t.Errorf("header greeting = %q, want hello", got)
	}
	if len(res.Trail) != 3 {
		t.Errorf("trail = %v, want 3 steps", res.Trail)
	}
}
