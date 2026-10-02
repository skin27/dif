package impl

import (
	"strings"
	"testing"
)

const persons = `<?xml version="1.0"?>
<persons xmlns:p="urn:p">
	<person id="1"><name>John Doe</name></person>
	<p:person><name>Jane <b>Doe</b></name></p:person>
	<group><person><name>Nested</name></person></group>
</persons>`

func TestXPathSelect(t *testing.T) {
	tests := []struct {
		expr string
		raw  []string
		text []string
	}{
		{"/persons/person", []string{`<person id="1"><name>John Doe</name></person>`, `<p:person><name>Jane <b>Doe</b></name></p:person>`}, []string{"John Doe", "Jane Doe"}},
		{"/persons/person/name", nil, []string{"John Doe", "Jane Doe"}},
		{"/p:persons/p:person/name", nil, []string{"John Doe", "Jane Doe"}},
		{"/persons/*/person/name", nil, []string{"Nested"}},
		{"/persons/*", nil, []string{"John Doe", "Jane Doe", "Nested"}},
		{"/persons/none", nil, nil},
		{"/person", nil, nil},
	}
	for _, tt := range tests {
		x, err := compileXPath(tt.expr)
		if err != nil {
			t.Fatalf("%s: %v", tt.expr, err)
		}
		nodes, err := x.selectXML([]byte(persons))
		if err != nil {
			t.Fatalf("%s: %v", tt.expr, err)
		}
		var raw, text []string
		for _, n := range nodes {
			raw, text = append(raw, n.raw), append(text, n.text)
		}
		if tt.raw != nil && strings.Join(raw, "|") != strings.Join(tt.raw, "|") {
			t.Errorf("%s: raw = %q, want %q", tt.expr, raw, tt.raw)
		}
		if strings.Join(text, "|") != strings.Join(tt.text, "|") {
			t.Errorf("%s: text = %q, want %q", tt.expr, text, tt.text)
		}
	}

	x, _ := compileXPath("/a")
	if _, err := x.selectXML([]byte("<a><b></a>")); err == nil || !strings.Contains(err.Error(), "body is not XML") {
		t.Errorf("invalid XML: err = %v", err)
	}
}

func TestXPathUnsupported(t *testing.T) {
	for _, expr := range []string{"", "persons/person", "//person", "/persons/person[1]", "/persons/@id", "/persons/text()", "/a//b", "/a/"} {
		if _, err := compileXPath(expr); err == nil || !strings.Contains(err.Error(), "unsupported xpath") {
			t.Errorf("%q: err = %v, want unsupported", expr, err)
		}
	}
}

func TestXPathPredicate(t *testing.T) {
	for expr, want := range map[string]bool{
		"/persons/person/name= 'John Doe'":       true,
		`/persons/person/name = "Jane Doe"`:      true,
		"/persons/person/name = 'Nobody'":        false,
		"/persons/person/name != 'John Doe'":     true, // Jane's differs
		"/persons/group/person/name != 'Nested'": false,
		"/persons/person":                        true,
		"/persons/nobody":                        false,
	} {
		p, err := compileXPathPredicate(expr)
		if err != nil {
			t.Fatalf("%s: %v", expr, err)
		}
		if got := p.match([]byte(persons)); got != want {
			t.Errorf("%s = %v, want %v", expr, got, want)
		}
	}
	p, _ := compileXPathPredicate("/a")
	if p.match([]byte("not xml")) {
		t.Error("a body that is not XML matched")
	}
}
