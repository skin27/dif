package impl

import (
	"context"
	"strings"
	"testing"

	"dif/message"
	stepdef "dif/steps/definition"
)

func TestSimplePredicate(t *testing.T) {
	m := message.New("123")
	m["kind"], m["other"], m["flag"] = "order", "order", "TRUE"
	for expr, want := range map[string]bool{
		"${bodyAs(String)} == '123'":        true,
		"${body} == 123":                    true,
		`${body} == "124"`:                  false,
		"${body} != '124'":                  true,
		"${header.kind} == ${header.other}": true,
		"${header.kind} contains 'rd'":      true,
		"${header.kind} contains 'x'":       false,
		"${header.missing} == ''":           true,
		"${header.kind} == 'a == b'":        false,
		"${header.flag}":                    true,
		"${header.kind}":                    false,
	} {
		p, err := compilePredicate("simple", expr)
		if err != nil {
			t.Fatalf("%s: %v", expr, err)
		}
		if got, err := p(m); err != nil || got != want {
			t.Errorf("%s = %v, want %v", expr, got, want)
		}
	}
}

func TestPredicateLanguages(t *testing.T) {
	xml := message.New(persons)
	json := message.New(store)
	for _, tt := range []struct {
		lang, expr string
		m          message.Message
		want       bool
	}{
		{"xpath", "/persons/person/name = 'John Doe'", xml, true},
		{"xpath", "/persons/person/name = 'John Doe'", json, false},
		{"jsonpath", "$.store.book[*].author", json, true},
		{"jsonpath", "$.store.book[*].author", xml, false},
	} {
		p, err := compilePredicate(tt.lang, tt.expr)
		if err != nil {
			t.Fatal(err)
		}
		if got, err := p(tt.m); err != nil || got != tt.want {
			t.Errorf("%s %s = %v, want %v", tt.lang, tt.expr, got, tt.want)
		}
	}
}

func TestPredicateFailsAtRuntime(t *testing.T) {
	p, err := compilePredicate("simple", "${bodyAs(Integer)} == '1'")
	if err != nil {
		t.Fatalf("${bodyAs(Integer)} must compile: %v", err)
	}
	if _, err := p(message.New("1")); err == nil || !strings.Contains(err.Error(), "${bodyAs(Integer)}: the body cannot be converted to Integer") {
		t.Errorf("err = %v", err)
	}
	r, err := newRouter(stepdef.Router, "content", nil, stepdef.Link{}, stepdef.Link{Rule: "r", Expression: "${bodyAs(Integer)} == '1'"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Route(context.Background(), message.New("1")); err == nil || !strings.Contains(err.Error(), "outbound link 1: ${bodyAs(Integer)}") {
		t.Errorf("content router: err = %v", err)
	}
}

func TestPredicateInvalid(t *testing.T) {
	for _, tt := range []struct{ lang, expr, want string }{
		{"groovy", "true", `language "groovy" is not supported`},
		{"simple", "${body} == 'a' and ${body} == 'b'", "combine conditions with && and ||"},
		{"simple", "${body} == 'a' or ${body} == 'b'", "combine conditions with && and ||"},
		{"simple", "${date:now} == 'x'", "${date:now}: want ${date:now:<format>}"},
		{"simple", "${body} == ${random(x)}", "${random(x)}: want random(<max>)"},
		{"simple", "${body} == ${exchangeId}", "unsupported simple expression ${exchangeId}"},
		{"simple", "${bodyAs(String} == 'x'", "unsupported simple expression ${bodyAs(String}"},
		{"xpath", "//person[", `xpath "//person["`},
		{"jsonpath", "$.a[", `jsonpath "$.a["`},
	} {
		if _, err := compilePredicate(tt.lang, tt.expr); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s %q: err = %v, want containing %q", tt.lang, tt.expr, err, tt.want)
		}
	}
}
