package api

import "testing"

// TestRunHello is the end-to-end test: JSON -> parse -> steps -> engine -> Message.
func TestRunHello(t *testing.T) {
	res, err := Run("../examples/hello.json")
	if err != nil {
		t.Fatal(err)
	}
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
