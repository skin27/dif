package impl

import (
	"strings"
	"testing"

	"dif/message"
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
		if got := p(m); got != want {
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
		if got := p(tt.m); got != tt.want {
			t.Errorf("%s %s = %v, want %v", tt.lang, tt.expr, got, tt.want)
		}
	}
}

func TestPredicateInvalid(t *testing.T) {
	for _, tt := range []struct{ lang, expr, want string }{
		{"groovy", "true", `language "groovy" is not supported`},
		{"simple", "${body} == 'a' && ${body} == 'b'", "combining conditions is not supported"},
		{"simple", "${body} == 'a' or ${body} == 'b'", "combining conditions is not supported"},
		{"simple", "${date:now} == 'x'", "unsupported simple expression ${date:now}"},
		{"simple", "${body} == ${random(3)}", "unsupported simple expression ${random(3)}"},
		{"xpath", "//person", "unsupported xpath"},
		{"jsonpath", "$..author", "unsupported jsonpath"},
	} {
		if _, err := compilePredicate(tt.lang, tt.expr); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s %q: err = %v, want containing %q", tt.lang, tt.expr, err, tt.want)
		}
	}
}
