package api

import (
	"fmt"
	"strings"
	"testing"
)

// TestAggregateCompletionTimeout runs timer -> aggregate -> setbody -> log
// through the real engine: each tick carries on as it came, and the group, when
// no tick has come for the timeout, goes on as a message of its own.
func TestAggregateCompletionTimeout(t *testing.T) {
	path := dil(t, "timed",
		step{"tick", "source", "timer:tick", map[string]any{"period": 10, "repeatCount": "3"}},
		step{"agg", "action", "aggregate", map[string]any{"aggregateType": "json", "completionTimeout": "300"}},
		step{"body", "action", "setbody", map[string]any{"language": "simple", "expression": "group ${body}"}},
		step{"log", "sink", "log", map[string]any{}},
	)
	f, results := start(t, path, nil)

	for i, want := range []string{"1", "2", "3"} {
		res := await(t, results)
		if fmt.Sprint(res.Message[Body]) != want {
			t.Errorf("tick %d: body = %v, want %q: the tick carries on as it came", i+1, res.Message[Body], want)
		}
		if got := strings.Join(res.Trail, " "); got != "source:tick action:agg" {
			t.Errorf("tick %d: trail = %s", i+1, got)
		}
	}
	res := await(t, results)
	if res.Message[Body] != "group [1,2,3]" {
		t.Errorf("group: body = %v", res.Message[Body])
	}
	if got := strings.Join(res.Trail, " "); got != "action:agg action:body sink:log" {
		t.Errorf("group: trail = %s, want it to enter after the aggregate", got)
	}
	if err := f.Stop(); err != nil {
		t.Fatal(err)
	}
}

// TestAggregateCompletionInterval completes the group every interval, and a
// stopped flow releases nothing.
func TestAggregateCompletionInterval(t *testing.T) {
	path := dil(t, "interval",
		step{"tick", "source", "timer:tick", map[string]any{"period": 10, "repeatCount": "2"}},
		step{"agg", "action", "aggregate", map[string]any{"aggregateType": "json", "completionInterval": "300"}},
		step{"log", "sink", "log", map[string]any{}},
	)
	f, results := start(t, path, nil)
	await(t, results)
	await(t, results)
	if res := await(t, results); res.Message[Body] != "[1,2]" {
		t.Errorf("group: body = %v", res.Message[Body])
	}
	if err := f.Stop(); err != nil {
		t.Fatal(err)
	}
	select {
	case o := <-results:
		t.Errorf("a stopped flow released %+v", o.res)
	default:
	}
}
