package api

import (
	"context"
	"strings"
	"testing"
)

// TestRequestReturnInErrorRoute: an error route that ends in a failing return
// tells the caller its message was refused, though the flow handled the error.
func TestRequestReturnInErrorRoute(t *testing.T) {
	link := func(id, bound string) map[string]any { return map[string]any{"id": id, "bound": bound} }
	links := func(l ...map[string]any) map[string]any { return map[string]any{"link": l} }
	path := writeDIL(t, "refuses", []map[string]any{
		{"id": "in", "type": "source", "uri": "message:in", "links": links(link("check", "out"))},
		{"id": "check", "type": "action", "uri": "simplevalidator", "options": map[string]any{"expression": "${body} == 'ok'"},
			"links": links(link("check", "in"), link("done", "out"))},
		{"id": "done", "type": "sink", "uri": "setbody", "options": map[string]any{"expression": "accepted"}, "links": links(link("done", "in"))},
		{"id": "failed", "type": "error", "uri": "failedexchange", "links": links(link("refuse", "out"))},
		{"id": "refuse", "type": "sink", "uri": "return", "options": map[string]any{"status": "error", "language": "constant", "expression": "order refused"},
			"links": links(link("refuse", "in"))},
	})
	f, results := start(t, path, nil)

	good := f.NewMessage()
	good[Body] = "ok"
	if reply, err := f.Request(context.Background(), good); err != nil || reply[Body] != "accepted" {
		t.Errorf("good order: reply = %v, %v; want accepted", reply, err)
	}
	if res := await(t, results); res.Err != nil {
		t.Errorf("good order failed: %v", res.Err)
	}

	bad := f.NewMessage()
	bad[Body] = "bad"
	if reply, err := f.Request(context.Background(), bad); err == nil || err.Error() != "order refused" {
		t.Errorf("bad order: reply = %v, %v; want the failure order refused", reply, err)
	}
	if res := await(t, results); res.Err == nil {
		t.Errorf("bad order: the flow has not recorded the failure it handled")
	}
}

// TestRequestReturn calls a flow like a function: the return step chooses the
// reply, at once or fixed for the end, and what happens after it does not
// change it. The flow itself ends with its own message.
func TestRequestReturn(t *testing.T) {
	tests := []struct {
		name string
		opts map[string]any
		body string // the reply's body
		err  string // the reply's failure, "" if none
	}{
		{"fixed for the end", map[string]any{"expression": "receipt ${body}"}, "receipt early", ""},
		{"detached", map[string]any{"expression": "receipt ${body}", "detach": true}, "receipt early", ""},
		{"a failure", map[string]any{"expression": "denied", "status": "error"}, "", "denied"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := dil(t, "returns",
				step{"in", "source", "message:in", nil},
				step{"early", "action", "setbody", map[string]any{"expression": "early"}},
				step{"return", "action", "return", tt.opts},
				step{"late", "sink", "setbody", map[string]any{"expression": "late"}},
			)
			f, results := start(t, path, nil)
			reply, err := f.Request(context.Background(), f.NewMessage())
			switch {
			case tt.err != "":
				if err == nil || !strings.Contains(err.Error(), tt.err) {
					t.Errorf("err = %v, want %q", err, tt.err)
				}
			case err != nil || reply[Body] != tt.body:
				t.Errorf("reply = %v, %v; want body %q", reply, err, tt.body)
			}
			if res := await(t, results); res.Message[Body] != "late" || res.Err != nil {
				t.Errorf("the flow ended with %v, %v; want late, and no failure", res.Message[Body], res.Err)
			}
		})
	}
}
