package impl

import (
	"testing"

	"dif/message"
	stepdef "dif/steps/definition"
)

func TestSetHeaders(t *testing.T) {
	headers := `[
		{"name": "firstName", "value": "John", "language": "simple"},
		{"name": "lastName", "value": "${body}", "language": "constant"},
		{"name": "full", "value": "${header.firstName} ${body}"},
		{"name": "empty", "value": ""}
	]`
	m := process(t, "setheaders:message:x", map[string]any{"headers": headers}, message.New("Doe"))
	for name, want := range map[string]string{"firstName": "John", "lastName": "${body}", "full": "John Doe", "empty": ""} {
		if m[name] != want {
			t.Errorf("header %s = %#v, want %q", name, m[name], want)
		}
	}
	if m[message.Body] != "Doe" {
		t.Errorf("body = %v, want it unchanged", m[message.Body])
	}

	// No headers is valid: the message passes unchanged.
	if m := process(t, "setheaders:message:x", map[string]any{"headers": "[]"}, message.New("x")); m[message.Body] != "x" {
		t.Errorf("body = %v", m[message.Body])
	}
}

func TestSetHeadersInvalid(t *testing.T) {
	invalid := func(headers, want string) {
		t.Helper()
		wantInvalid(t, stepdef.Action, "setheaders:message:x", map[string]any{"headers": headers}, want)
	}
	invalid(`[{"name": "random", "value": "new Random().nextInt(10)", "language": "groovy"}]`, `header random: language "groovy" is not supported; use constant, simple, xpath or jsonpath`)
	invalid(`[{"name": "x", "value": "${bodyAs}"}]`, "header x: unsupported simple expression")
	invalid(`[{"name": "body", "value": "x"}]`, "reserved for the body")
	invalid(`[{"name": "metadata.traceid", "value": "x"}]`, "reserved for metadata")
	invalid(`[{"name": "", "value": "x"}]`, "header name is empty")
	invalid(`{"name": "x"}`, "option headers: want a JSON array")
	wantInvalid(t, stepdef.Action, "setheaders:message:x", nil, "missing required option headers")
}

// TestSetHeadersAcceptsDesignerOptions checks the options the designer leaves
// on a setheaders step: the headers are those of the core message, so neither
// changes what the step does.
func TestSetHeadersAcceptsDesignerOptions(t *testing.T) {
	m := process(t, "setheaders:message:x", map[string]any{
		"headers":       `[{"name": "a", "value": "1"}]`,
		"expression":    "694aca3eda8dd5001600023e",
		"writeAsString": "false",
	}, message.New("x"))
	if m["a"] != "1" {
		t.Errorf("header a = %#v, want \"1\"", m["a"])
	}
}
