package api

import (
	"context"
	"io"
	"log"
	"testing"
)

// node is a DIL step whose inbound link (none for a source) has its id, and
// whose outbound links are outs.
func node(id, kind, uri string, opts map[string]any, outs ...map[string]any) map[string]any {
	links := outs
	if kind != "source" {
		links = append([]map[string]any{{"id": id, "bound": "in"}}, outs...)
	}
	return map[string]any{"id": id, "type": kind, "uri": uri, "options": opts, "links": map[string]any{"link": links}}
}

// out is an outbound link to step to, with a rule and a simple condition
// (both optional).
func out(to, rule, expression string) map[string]any {
	l := map[string]any{"id": to, "bound": "out"}
	if rule != "" {
		l["rule"] = rule
	}
	if expression != "" {
		l["language"], l["expression"] = "simple", expression
	}
	return l
}

func body(expr string) map[string]any {
	return map[string]any{"language": "simple", "expression": expr}
}

// TestControlFlow runs flows with if, loop, dowhile and wastebin steps, as the
// examples in testdata/examples/experimental use them, and checks the reply.
func TestControlFlow(t *testing.T) {
	src := func(next string) map[string]any { return node("in", "source", "message:in", nil, out(next, "", "")) }
	tests := []struct {
		name  string
		steps []map[string]any
		body  string
		want  string
	}{
		{"if, then", []map[string]any{
			src("if"),
			node("if", "router", "if", nil, out("yes", "if", "${body} contains 'Test'"), out("no", "", "")),
			node("yes", "sink", "setbody", body("yes")),
			node("no", "sink", "setbody", body("no")),
		}, "a Test", "yes"},
		{"if, else", []map[string]any{
			src("if"),
			node("if", "router", "if", nil, out("yes", "if", "${body} contains 'Test'"), out("no", "", "")),
			node("yes", "sink", "setbody", body("yes")),
			node("no", "sink", "setbody", body("no")),
		}, "other", "no"},
		{"loop action", []map[string]any{
			src("loop"),
			node("loop", "action", "loop", map[string]any{"language": "simple", "copy": false}, out("each", "loop", "3")),
			node("each", "action", "setbody", body("${body} ${header.loop.index}"), out("log", "", "")),
			node("log", "sink", "logger", body("${body}")),
		}, "Loops:", "Loops: 0 1 2"},
		{"loop router", []map[string]any{
			src("loop"),
			node("loop", "router", "loop", nil, out("each", "loop", "2"), out("done", "", "")),
			node("each", "sink", "setbody", body("${body} ${header.loop.index}/${header.loop.size}")),
			node("done", "sink", "setbody", body("${body} done")),
		}, "Loops:", "Loops: 0/2 1/2 done"},
		{"dowhile action", []map[string]any{
			src("while"),
			node("while", "action", "dowhile", nil, out("each", "dowhile", "${body} != 'xxx'")),
			node("each", "sink", "setbody", body("${body}x")),
		}, "x", "xxx"},
		{"wastebin", []map[string]any{
			src("bin"),
			node("bin", "action", "wastebin", nil, out("after", "", "")),
			node("after", "sink", "setbody", body("after the wastebin")),
		}, "dropped", "dropped"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f, _ := start(t, writeDIL(t, "control", tt.steps), log.New(io.Discard, "", 0))
			m := f.NewMessage()
			m[Body] = tt.body
			reply, err := f.Request(context.Background(), m)
			if err != nil || reply[Body] != tt.want {
				t.Errorf("reply = %v, %v; want body %q", reply[Body], err, tt.want)
			}
		})
	}
}
