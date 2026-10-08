package impl

import (
	"context"
	"strings"
	"testing"

	"dif/message"
	stepdef "dif/steps/definition"
)

func TestSimpleReplace(t *testing.T) {
	m := message.New("Hello ${header.name}, welcome to ${headers.company}.")
	m["name"], m["company"] = "Pedro", "Fluxygen"
	if got := process(t, "simplereplace", nil, m)[message.Body]; got != "Hello Pedro, welcome to Fluxygen." {
		t.Errorf("body = %q", got)
	}
	if got := process(t, "simplereplace", nil, message.New("no references"))[message.Body]; got != "no references" {
		t.Errorf("body = %q", got)
	}
}

func TestSimpleReplaceInvalid(t *testing.T) {
	p := mustProcessor(t, stepdef.Action, "simplereplace", nil).(stepdef.ActionProcessor)
	if _, err := p.Process(context.Background(), message.New("${exchangeId}")); err == nil || !strings.Contains(err.Error(), "body: unsupported simple expression ${exchangeId}") {
		t.Errorf("err = %v, want unsupported expression", err)
	}
	wantInvalid(t, stepdef.Action, "simplereplace", map[string]any{"language": "simple"}, "unknown option language")
}
