package impl

import (
	"strings"
	"testing"

	"dif/message"
	stepdef "dif/steps/definition"
)

const persons = `<?xml version="1.0"?>
<persons xmlns:p="urn:p">
	<person id="1"><name>John Doe</name></person>
	<p:person><name>Jane <b>Doe</b></name></p:person>
	<group><person><name>Nested</name></person></group>
</persons>`

func selectTexts(t *testing.T, expr string, ns map[string]string, doc string) (raw, text []string) {
	t.Helper()
	x, err := compileXPathNS(expr, ns)
	if err != nil {
		t.Fatalf("%s: %v", expr, err)
	}
	nodes, err := x.selectXML([]byte(doc))
	if err != nil {
		t.Fatalf("%s: %v", expr, err)
	}
	for _, n := range nodes {
		raw, text = append(raw, n.raw), append(text, n.text)
	}
	return raw, text
}

// A plain path of element names is scanned for, without a tree. As in XPath a
// name without a prefix is that of an element in no namespace.
func TestXPathPlainPath(t *testing.T) {
	tests := []struct {
		expr string
		raw  []string
		text []string
	}{
		{"/persons/person", []string{`<person id="1"><name>John Doe</name></person>`}, []string{"John Doe"}},
		{"/persons/person/name", nil, []string{"John Doe"}},
		{"/persons/*/person/name", nil, []string{"Nested"}},
		{"/persons/*", nil, []string{"John Doe", "Jane Doe", "Nested"}}, // * is any element, also in a namespace
		{"/persons/none", nil, nil},
		{"/person", nil, nil},
	}
	for _, tt := range tests {
		x, err := compileXPath(tt.expr)
		if err != nil {
			t.Fatal(err)
		}
		if x.engine != nil {
			t.Errorf("%s needs no tree", tt.expr)
		}
		raw, text := selectTexts(t, tt.expr, nil, persons)
		_ = x
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

// The rest is XPath 2.0, evaluated on a tree.
func TestXPath2(t *testing.T) {
	const doc = `<root xmlns:h="urn:h"><order><line><n>1</n><v>10</v></line><line><n>2</n><v>20.5</v></line><line><n>3</n><v>30</v></line></order>` +
		`<h:table id="t1">first</h:table><h:table id="t2">second</h:table>` +
		`<when>2024-03-05T10:00:00</when><name>  a b  </name><x>1.0</x><item>b</item><item>a</item><item>b</item></root>`
	for expr, want := range map[string]string{
		"//line":                            "110|220.5|330",
		"//line/n/text()":                   "1|2|3",
		"/root/order/line[2]/v":             "20.5",
		"//line[v > 15]/n":                  "2|3",
		"//line[number(v) > 15]/n/text()":   "2|3",
		"//line[n = 2]/v/text()":            "20.5",
		"//line/v/number()":                 "10|20.5|30",
		"//line/n/number() = 1":             "true",
		"count(//line)":                     "3",
		"sum(//line/v)":                     "60.5",
		"max(//line/v)":                     "30",
		"min(//line/v)":                     "10",
		"avg(//line/v)":                     "20.166666666666668",
		"distinct-values(//item)":           "b|a",
		"string-join(//item, '-')":          "b-a-b",
		"year-from-dateTime(//when/text())": "2024",
		"normalize-space(//name)":           "a b",
		"upper-case(//name)":                "  A B  ",
		"//*:table":                         "first|second",
		"/root/*:table[@id = 't2']":         "second",
		"//h:table/@id":                     "t1|t2",
		"//x[text() = 1.0]":                 "1.0",
		"//x[text() = 1]":                   "1.0", // by value
		"//x[. = '1']":                      "",    // as text
		"if (count(//item) > 2) then 'many' else 'few'": "many",
		"for $i in //line return $i/n * 2":              "2|4|6",
		"(//line)[last()]/n":                            "3",
		"(/root/order/line/n[. > 1])[1]":                "2",
		"exists(//nothing)":                             "false",
		"string(//nothing)":                             "",
	} {
		_, text := selectTexts(t, expr, map[string]string{"h": "urn:h"}, doc)
		if got := strings.Join(text, "|"); got != want {
			t.Errorf("%s = %q, want %q", expr, got, want)
		}
	}
}

func TestXPath2Serialization(t *testing.T) {
	doc := `<root xmlns="urn:d" xmlns:p="urn:p"><p:a x="1 &amp; 2" y='"q"'><b>t &lt; u</b><!-- c --><?pi data?><e/></p:a><item/></root>`
	raw, _ := selectTexts(t, "//*:a", nil, doc)
	// The namespaces in scope are declared, so that the part can be read on its own.
	want := `<p:a xmlns="urn:d" xmlns:p="urn:p" x="1 &amp; 2" y="&quot;q&quot;"><b>t &lt; u</b><!-- c --><?pi data?><e/></p:a>`
	if len(raw) != 1 || raw[0] != want {
		t.Errorf("part = %q, want %q", raw, want)
	}
	if _, err := parseXMLTree([]byte(raw[0])); err != nil {
		t.Errorf("the part is no XML: %v", err)
	}
	raw, text := selectTexts(t, "//*:a/@x", nil, doc)
	if len(raw) != 1 || raw[0] != "1 & 2" || text[0] != "1 & 2" {
		t.Errorf("an attribute is its value: %q %q", raw, text)
	}
	raw, _ = selectTexts(t, "//*:b/text()", nil, doc)
	if len(raw) != 1 || raw[0] != "t < u" {
		t.Errorf("a text node is its value: %q", raw)
	}
}

func TestXPathNamespaces(t *testing.T) {
	doc := `<s:envelope xmlns:s="urn:soap"><s:body><m:get xmlns:m="urn:m"><m:id>7</m:id></m:get></s:body></s:envelope>`
	ns := map[string]string{"ns": "urn:m", "s": "urn:soap"}
	for expr, want := range map[string]string{
		"/s:envelope/s:body/ns:get/ns:id": "7",
		"//ns:id":                         "7",
		"//*:id":                          "7",
		"//id":                            "", // not in a namespace
	} {
		if _, text := selectTexts(t, expr, ns, doc); strings.Join(text, "|") != want {
			t.Errorf("%s = %q, want %q", expr, text, want)
		}
	}
	if _, err := compileXPath("/s:envelope"); err == nil || !strings.Contains(err.Error(), "xpath") {
		t.Errorf("an unbound prefix: err = %v", err)
	}
}

func TestXPathInvalid(t *testing.T) {
	for _, expr := range []string{"", "  ", "//", "/a[", "count(", "/a/@@b", "unknownfunction(1)", "fn:count()", "upper-case(1, 2, 3)"} {
		if _, err := compileXPath(expr); err == nil {
			t.Errorf("%q must not compile", expr)
		}
	}
	x, err := compileXPath("//a")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := x.selectXML([]byte("not xml")); err == nil || !strings.Contains(err.Error(), "body is not XML") {
		t.Errorf("err = %v", err)
	}
	if _, err := x.selectXML([]byte(`<!DOCTYPE a [<!ENTITY e SYSTEM "file:///etc/passwd">]><a>&e;</a>`)); err == nil {
		t.Error("a DOCTYPE must be refused")
	}
	x, _ = compileXPath("1 div 0")
	if _, err := x.selectXML([]byte("<a/>")); err == nil {
		t.Error("an error of the expression is an error of the step")
	}
}

func TestXPathCondition(t *testing.T) {
	for expr, want := range map[string]bool{
		"/persons/person/name= 'John Doe'":       true,
		`/persons/person/name = "John Doe"`:      true,
		"/persons/person/name = 'Nobody'":        false,
		"/persons/person/name != 'John Doe'":     false, // the only one in no namespace
		"/persons/group/person/name != 'Nested'": false,
		"//*:person/name = 'Jane Doe'":           true,
		"//*:person/name != 'John Doe'":          true, // Jane's differs
		"/persons/person":                        true,
		"/persons/nobody":                        false,
		"//person":                               true,
		"//person[@id = 1]":                      true,
		"//person[@id = 2]":                      false,
		"count(//person) = 2":                    true,
		"count(//*:person) = 3":                  true,
		"count(//person) > 3":                    false,
		"//*:person/name/text()":                 true,
		"not(//nobody)":                          true,
		"//nobody":                               false,
		"string((//person/name)[1])":             true,
		"string(//person/name)":                  false, // two items in a string() are an error
		"number(//person/@id) = 1.0":             true,
	} {
		p, err := compilePredicate("xpath", expr)
		if err != nil {
			t.Fatalf("%s: %v", expr, err)
		}
		if got, _ := p(message.New(persons)); got != want {
			t.Errorf("%s = %v, want %v", expr, got, want)
		}
	}
	p, _ := compilePredicate("xpath", "/a")
	if ok, err := p(message.New("not xml")); ok || err != nil {
		t.Errorf("a body that is not XML matches nothing: %v, %v", ok, err)
	}
	p, _ = compilePredicate("xpath", "//a")
	if ok, err := p(message.New("not xml")); ok || err != nil {
		t.Errorf("a body that is not XML matches nothing: %v, %v", ok, err)
	}
}

func TestXPathValue(t *testing.T) {
	x, err := compileXPath("//person/name/text()")
	if err != nil {
		t.Fatal(err)
	}
	if v, err := x.value([]byte(persons)); err != nil || v != "John Doe" {
		t.Errorf("first item: %q, %v", v, err)
	}
	x, _ = compileXPath("//nobody")
	if v, err := x.value([]byte(persons)); err != nil || v != "" {
		t.Errorf("nothing: %q, %v", v, err)
	}
}

// The steps that take an xpath: the header and variable values, the conditions of
// the content router with its namespace, and ${xpath()}.
func TestXPathInSteps(t *testing.T) {
	headers := `[{"name":"count","value":"count(//person)","language":"xpath"},` +
		`{"name":"first","value":"//person/name/text()","language":"xpath"},` +
		`{"name":"none","value":"//zzz","language":"xpath"},` +
		`{"name":"total","value":"sum(//n)","language":"xpath"}]`
	m := process(t, "setheaders", map[string]any{"headers": headers}, message.New(persons+"\n"))
	if m["count"] != "2" || m["first"] != "John Doe" || m["none"] != "" {
		t.Errorf("headers = %v", m)
	}
	m = process(t, "setheaders", map[string]any{"headers": headers}, message.New(`<r><n>1</n><n>2.5</n></r>`))
	if m["total"] != "3.5" || m["count"] != "0" {
		t.Errorf("headers = %v", m)
	}
	wantInvalid(t, stepdef.Action, "setheaders", map[string]any{"headers": `[{"name":"x","value":"//a[","language":"xpath"}]`}, `header x: xpath "//a["`)

	process(t, "settenantvariable:fromxml", map[string]any{"language": "xpath", "value": "//order/@id", "tenantDbName": "xp"}, message.New(`<order id="o-7"/>`))
	if v, _ := tenantVariables.get("xp", "fromxml"); v != "o-7" {
		t.Errorf("variable = %q", v)
	}

	// ns stands for the option namespace.
	doc := `<order xmlns="urn:o"><id>7</id></order>`
	links := []stepdef.Link{{Rule: "seven", Language: "xpath", Expression: "/ns:order/ns:id = 7"}, {}}
	if got := summary(route(t, "content", map[string]any{"namespace": "urn:o"}, links, message.New(doc))); !strings.HasPrefix(got, "0:") {
		t.Errorf("routes = %s, want the link with the condition", got)
	}
	if got := summary(route(t, "content", map[string]any{"namespace": "urn:other"}, links, message.New(doc))); !strings.HasPrefix(got, "1:") {
		t.Errorf("routes = %s, want the otherwise link", got)
	}
	if _, err := newRouter(stepdef.Router, "content", nil, links...); err == nil || !strings.Contains(err.Error(), `xpath "/ns:order/ns:id = 7"`) {
		t.Errorf("without the namespace the prefix is unbound: %v", err)
	}

	for expr, want := range map[string]string{
		"${xpath(count(//person))}":                  "2",
		"${xpath(//person/name/text())}":             "John Doe",
		"${xpath(//zzz)}":                            "",
		"${xpath(count(//person) + 40, Integer)} ok": "42 ok",
		"${xpath('//person/@id')}":                   "1",
	} {
		if got, err := evalTemplate(t, expr, message.New(persons)); err != nil || got != want {
			t.Errorf("%s = %q, %v; want %q", expr, got, err, want)
		}
	}
	if _, err := compileTemplate("${xpath(//a[)}"); err == nil {
		t.Error("an invalid xpath must not compile")
	}
}
