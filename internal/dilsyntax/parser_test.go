package dilsyntax

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// lex returns the tokens of src as kind:text pairs.
func lex(t *testing.T, src string) []string {
	t.Helper()
	lx := newLexer([]byte(src))
	var out []string
	for {
		tok, err := lx.next()
		if err != nil {
			t.Fatalf("%q: %v", src, err)
		}
		if tok.kind == tokEOF {
			return out
		}
		out = append(out, tok.describe()+":"+tok.text)
	}
}

func TestLexerEdgeCases(t *testing.T) {
	for _, tc := range []struct {
		src  string
		want []string
	}{
		// URIs with colons and slashes are one word; -> ends a word.
		{"https://host:9002/path?a=1&b=2", []string{"a word:https://host:9002/path?a=1&b=2"}},
		{"service:orders:PlaceOrder->log", []string{"a word:service:orders:PlaceOrder", "'->':", "a word:log"}},
		{"a-b -> -1", []string{"a word:a-b", "'->':", "a word:-1"}},
		// A word never ends with a colon, so "name: value" and "name:value" both split.
		{`name: "x"`, []string{"a word:name", "':':", "a string:x"}},
		{`name:"x"`, []string{"a word:name", "':':", "a string:x"}},
		{"queue:", []string{"a word:queue", "':':"}},
		// ${...} in strings, quotes of the other kind, escapes.
		{`"${header.type} == 'rush'"`, []string{"a string:${header.type} == 'rush'"}},
		{`'{"sku":"BOOK-1"}'`, []string{`a string:{"sku":"BOOK-1"}`}},
		{`"a\tb\n\u00e9\ud83d\ude00\"\\\/"`, []string{"a string:a\tb\né😀\"\\/"}},
		{`'it\'s'`, []string{"a string:it's"}},
		// Raw strings span lines, keep # and quotes, and drop carriage returns.
		{"`line 1\r\n# not a comment \"x\"\r\nline 3`", []string{"a string:line 1\n# not a comment \"x\"\nline 3"}},
		// Comments run to the end of the line, but not inside strings.
		{"a # comment -> b\n\"# not\" # c", []string{"a word:a", "a string:# not"}},
		{"\ufeff( ) { } [ ] ,", []string{"'(':", "')':", "'{':", "'}':", "'[':", "']':", "',':"}},
		{"$param", []string{"a word:$param"}},
	} {
		if got := lex(t, tc.src); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%q:\n got %q\nwant %q", tc.src, got, tc.want)
		}
	}
}

// compile compiles src and decodes the JSON.
func compile(t *testing.T, src string) map[string]any {
	t.Helper()
	data, err := Compile("test.dil", []byte(src))
	if err != nil {
		t.Fatalf("%v\n%s", err, src)
	}
	var doc map[string]any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

// path follows keys and list indexes through a decoded document.
func path(v any, keys ...any) any {
	for _, k := range keys {
		switch k := k.(type) {
		case string:
			v = asMap(v)[k]
		case int:
			l, _ := v.([]any)
			if k >= len(l) {
				return nil
			}
			v = l[k]
		}
	}
	return v
}

func steps(doc map[string]any, integration, flow int) []any {
	l, _ := path(doc, "dil", "integrations", "integration", integration, "flows", "flow", flow, "steps", "step").([]any)
	return l
}

func TestChainsTypesAndIDs(t *testing.T) {
	doc := compile(t, `
flow hello(name: "Hello", inputMessage: "greeting", version: 2) {
  timer:tick(period: 1000, repeatCount: 3) as tick   # the source
    -> setbody(expression: "${body}!", language: "simple")
    -> setbody(expression: "x")
    -> log as setbody-3
    -> passthrough
}`)
	f := path(doc, "dil", "integrations", "integration", 0, "flows", "flow", 0)
	if path(f, "id") != "hello" || path(f, "name") != "Hello" {
		t.Fatalf("flow fields: %v", f)
	}
	if !reflect.DeepEqual(path(f, "options"), map[string]any{"inputMessage": "greeting", "version": 2.0}) {
		t.Fatalf("options: %v", path(f, "options"))
	}
	var got []string
	for _, s := range steps(doc, 0, 0) {
		got = append(got, path(s, "id").(string)+" "+path(s, "type").(string)+" "+path(s, "uri").(string))
	}
	want := []string{"tick source timer:tick", "setbody-1 action setbody", "setbody-2 action setbody", "setbody-3 action log", "passthrough-1 sink passthrough"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("steps:\n got %q\nwant %q", got, want)
	}
	if !reflect.DeepEqual(path(steps(doc, 0, 0)[0], "options"), map[string]any{"period": 1000.0, "repeatCount": 3.0}) {
		t.Fatalf("source options: %v", path(steps(doc, 0, 0)[0], "options"))
	}
	// Each link goes out of one step and into the next.
	for i := 0; i < 4; i++ {
		out := path(steps(doc, 0, 0)[i], "links", "link", len(path(steps(doc, 0, 0)[i], "links", "link").([]any))-1)
		in := path(steps(doc, 0, 0)[i+1], "links", "link", 0)
		if path(out, "bound") != "out" || path(in, "bound") != "in" || path(out, "id") != path(in, "id") {
			t.Fatalf("link %d: %v -> %v", i, out, in)
		}
	}
}

func TestOptionValues(t *testing.T) {
	doc := compile(t, `flow f { a(s: "x", n: -1.5e3, t: true, f: false, z: null, l: [1, "two" [3]], o: {k: "v", "odd key": {}}, raw: `+"`a\nb`"+`) as a }`)
	want := map[string]any{"s": "x", "n": -1500.0, "t": true, "f": false, "z": nil, "l": []any{1.0, "two", []any{3.0}},
		"o": map[string]any{"k": "v", "odd key": map[string]any{}}, "raw": "a\nb"}
	if got := path(steps(doc, 0, 0)[0], "options"); !reflect.DeepEqual(got, want) {
		t.Fatalf("options:\n got %v\nwant %v", got, want)
	}
}

func TestRoutersKeepTheirRoutes(t *testing.T) {
	doc := compile(t, `
flow route {
  rest(method: "post", path: "/orders") as in
    -> content as by-type {
         when simple "${header.type} == 'rush'" -> queue:rush as rush
         otherwise -> queue:standard as standard
         _ -> log as plain
         split "//item" -> log as rule-and-expression
         _ jsonpath "$.a" -> log as no-rule
         split xpath "" -> log as no-expression
         "as" -> log as quoted-rule
       }
}`)
	s := steps(doc, 0, 0)
	if path(s, 1, "type") != "router" || path(s, 2, "type") != "sink" {
		t.Fatalf("types: %v %v", path(s, 1, "type"), path(s, 2, "type"))
	}
	var got [][3]any
	for _, l := range path(s, 1, "links", "link").([]any)[1:] {
		got = append(got, [3]any{path(l, "rule"), path(l, "language"), path(l, "expression")})
	}
	want := [][3]any{
		{"when", "simple", "${header.type} == 'rush'"},
		{"otherwise", nil, nil},
		{nil, nil, nil},
		{"split", nil, "//item"},
		{nil, "jsonpath", "$.a"},
		{"split", "xpath", nil},
		{"as", nil, nil},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("routes:\n got %v\nwant %v", got, want)
	}
	// The routes lead to the steps in their order.
	var targets []string
	for _, x := range s[2:] {
		targets = append(targets, path(x, "id").(string))
	}
	if strings.Join(targets, " ") != "rush standard plain rule-and-expression no-rule no-expression quoted-rule" {
		t.Fatalf("targets: %v", targets)
	}
}

func TestExplicitTypes(t *testing.T) {
	// A router processor in an action position, a sink in the middle: the
	// keyword sets the type that inference would not give.
	doc := compile(t, `
flow f {
  a as a
    -> action split as s {
         split xpath "//x" -> sink b as b
       }
}
flow g {
  router a as a {
    _ -> "sink" as c
  }
}`)
	if path(steps(doc, 0, 0), 1, "type") != "action" || path(steps(doc, 0, 0), 2, "type") != "sink" {
		t.Fatalf("f: %v", steps(doc, 0, 0))
	}
	if path(steps(doc, 0, 1), 0, "type") != "router" || path(steps(doc, 0, 1), 1, "uri") != "sink" {
		t.Fatalf("g: %v", steps(doc, 0, 1))
	}
}

func TestErrorHandlers(t *testing.T) {
	doc := compile(t, `
flow f {
  on error(errorClass: "*FaultError", maximumRedeliveries: 2) as fault {
    setbody(expression: "${header.error.body}") -> passthrough
  }
  message as in -> passthrough as out
  on error
}`)
	s := steps(doc, 0, 0)
	var got []string
	for _, x := range s {
		got = append(got, path(x, "id").(string)+" "+path(x, "type").(string))
	}
	want := []string{"in source", "out sink", "fault error", "setbody-1 action", "passthrough-1 sink", "error-1 error"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("steps:\n got %q\nwant %q", got, want)
	}
	if path(s, 2, "uri") != "failedexchange" || path(s, 2, "options", "maximumRedeliveries") != 2.0 {
		t.Fatalf("handler: %v", s[2])
	}
	if path(s, 2, "links", "link", 0, "id") != path(s, 3, "links", "link", 0, "id") || path(s, 5, "links") != nil {
		t.Fatalf("handler links: %v %v", s[2], s[5])
	}
}

func TestIntegrationsAndDeclarations(t *testing.T) {
	doc := compile(t, `
import "./contracts.dil" as orders

message ValidOrder {
  body: '{"sku":"BOOK-1","quantity":2}'
  header Idempotency-Key: "idem-100"
  header Sent: simple "${date:now}"
}

connection db {
  type: "sql"
  authentication: {username: "dif", password: "dummy-password"}
}

flow first { message -> passthrough }

integration orders(name: "Orders", version: 1) {
  flow second { message -> passthrough }
}

resource Schema {
  content: `+"`{\n  \"type\": \"object\"\n}`"+`
}

flow third { message -> passthrough }
`)
	dil := path(doc, "dil")
	if !reflect.DeepEqual(path(dil, "imports", "import"), []any{map[string]any{"path": "./contracts.dil", "namespace": "orders"}}) {
		t.Fatalf("imports: %v", path(dil, "imports"))
	}
	wantMessage := map[string]any{"name": "ValidOrder", "body": `{"sku":"BOOK-1","quantity":2}`, "headers": map[string]any{"header": []any{
		map[string]any{"name": "Idempotency-Key", "value": "idem-100"},
		map[string]any{"name": "Sent", "value": "${date:now}", "language": "simple"},
	}}}
	if got := path(dil, "core", "messages", "message", 0); !reflect.DeepEqual(got, wantMessage) {
		t.Fatalf("message:\n got %v\nwant %v", got, wantMessage)
	}
	if path(dil, "core", "connections", "connection", 0, "id") != "db" || path(dil, "core", "connections", "connection", 0, "authentication", "username") != "dif" {
		t.Fatalf("connection: %v", path(dil, "core", "connections"))
	}
	if path(dil, "core", "resources", "resource", 0, "content") != "{\n  \"type\": \"object\"\n}" {
		t.Fatalf("resource: %v", path(dil, "core", "resources"))
	}
	// The flows outside an integration form one, where the first of them is.
	ins := path(dil, "integrations", "integration").([]any)
	if len(ins) != 2 || path(ins[0], "id") != nil || path(ins[1], "id") != "orders" || path(ins[1], "name") != "Orders" || path(ins[1], "version") != 1.0 {
		t.Fatalf("integrations: %v", ins)
	}
	if path(ins[0], "flows", "flow", 0, "id") != "first" || path(ins[0], "flows", "flow", 1, "id") != "third" || path(ins[1], "flows", "flow", 0, "id") != "second" {
		t.Fatalf("flows: %v", ins)
	}
}

func TestSyntaxErrors(t *testing.T) {
	const secret = "hunter2"
	deep := "flow f { a(o: " + strings.Repeat("[", 2000) + " ) }"
	for _, tc := range []struct{ src, want string }{
		{"flow x {\n  timer as t\n    log\n}", "test.dil:3:5: expected '->' before the next step, 'on error' or '}' to end the flow, found a word"},
		{"flow x {\n  a ->\n}", "test.dil:3:1: expected a step, found '}'"},
		{`flow x { a(b "hunter2") }`, "test.dil:1:14: expected ':' after the option name, found a string"},
		{`flow x { a(b: hunter2) }`, "test.dil:1:15: expected a value, found a word (write text in quotes)"},
		{"flow x { a { when -> b } -> c }", "test.dil:1:26: a router's routes are inside its braces"},
		{`flow x { a { when "hunter2" b } }`, "test.dil:1:29: expected '->' after the branch's rule and condition, found a word"},
		{"flow x { a(b: \"hunter2) }", "test.dil:1:15: string not closed on its line"},
		{"resource r { content: `hunter2 }", "test.dil:1:23: raw string not closed"},
		{`flow x { a(b: "hunter2\q") }`, "test.dil:1:23: invalid escape in string"},
		{"flow x { a; b }", "test.dil:1:11: unexpected character ';'"},
		{"hunter2 x {}", "test.dil:1:1: expected import, integration, flow or a declaration"},
		{`flow x { a(b: 1, b: "hunter2") }`, "test.dil:1:18: duplicate option"},
		{`map m { e: 1 e: "hunter2" }`, "test.dil:1:14: duplicate field"},
		{`map m { name: "x" }`, "test.dil:1:1: the map has its name both after 'map' and as a field"},
		{"message m {\n header a: \"hunter2\"\n headers: {}\n}", "test.dil:2:2: the message has both header lines and a headers field"},
		{`import "hunter2"`, "test.dil:1:17: expected 'as' and a namespace after the import path, found end of input"},
		{"integration i { message }", "test.dil:1:17: expected a flow or '}' to end the integration, found a word"},
		{deep, "nested too deep"},
	} {
		_, err := Compile("test.dil", []byte(tc.src))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%q:\n got %v\nwant %s", tc.src, err, tc.want)
			continue
		}
		if strings.Contains(err.Error(), secret) {
			t.Errorf("%q: the error shows a value: %v", tc.src, err)
		}
	}
}

func TestIsText(t *testing.T) {
	for src, want := range map[string]bool{
		`{"dil": {}}`:          false,
		"\ufeff  {\"dil\":{}}": false,
		"":                     false,
		"true":                 false,
		"[1]":                  false,
		"# a flow\nflow f {}":  true,
		"flow f {}":            true,
		"\ufeff\nmessage m {}": true,
		"{ not json":           false,
	} {
		if got := IsText([]byte(src)); got != want {
			t.Errorf("IsText(%q) = %v", src, got)
		}
	}
	if !IsTextPath("a/b/Orders.DIL") || IsTextPath("orders.json") || IsTextPath("dil") {
		t.Fatal("IsTextPath")
	}
}

func TestPrintQuotesWhatAWordCannotSay(t *testing.T) {
	// Ids, URIs and rules that are keywords or hold other characters are quoted.
	src := []byte(`{"dil":{"integrations":{"integration":{"flows":{"flow":{"id":"my flow","steps":{"step":[
	  {"id":"a b","type":"source","uri":"sink","links":{"link":{"id":"1","bound":"out"}}},
	  {"id":"r","type":"router","uri":"https://h:1/p#frag","links":{"link":[{"id":"1","bound":"in"},
	    {"id":"2","bound":"out","rule":"_"},{"id":"3","bound":"out","rule":"as","language":"simple"},{"id":"4","bound":"out","language":"xpath","expression":"//a"}]}},
	  {"id":"x","type":"sink","uri":"on","links":{"link":{"id":"2","bound":"in"}}},
	  {"id":"y","type":"sink","uri":"_","links":{"link":{"id":"3","bound":"in"}}},
	  {"id":"","type":"sink","uri":"as","links":{"link":{"id":"4","bound":"in"}}}
	]}}}}}}}`)
	text, err := Print(src)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`flow "my flow" {`, `"sink" as "a b"`, `-> "https://h:1/p#frag" as r {`, `"_"`, `"as" simple ""`, `_ xpath "//a"`, `-> "on" as x`, `-> "_" as y`, `-> "as" as ""`} {
		if !strings.Contains(string(text), want) {
			t.Errorf("missing %s in\n%s", want, text)
		}
	}
	back, err := Compile("", text)
	if err != nil {
		t.Fatalf("%v\n%s", err, text)
	}
	if !reflect.DeepEqual(canonical(t, src), canonical(t, back)) {
		t.Fatalf("round trip changes the flow:\n%s", text)
	}
}

func TestPrintRejectsWhatIsNoTree(t *testing.T) {
	for _, tc := range []struct{ steps, want string }{
		{`{"id":"a","type":"source","uri":"x","links":{"link":{"id":"1","bound":"out"}}},{"id":"c","type":"sink","uri":"x","links":{"link":{"id":"1","bound":"in"}}},{"id":"b","type":"sink","uri":"x","links":{"link":{"id":"2","bound":"in"}}}`, `step "b" is not reachable`},
		{`{"id":"a","type":"source","uri":"x","links":{"link":{"id":"1","bound":"out"}}}`, `outbound link "1" has no target`},
		{`{"id":"a","type":"action","uri":"x"}`, "no source step"},
		{`{"id":"e","type":"error","uri":"log"}`, "must use failedexchange"},
	} {
		_, err := Print([]byte(`{"dil":{"integrations":{"integration":{"flows":{"flow":{"id":"f","steps":{"step":[` + tc.steps + `]}}}}}}}`))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want %s", tc.steps, err, tc.want)
		}
	}
}

// TestCanonicalSeesChanges checks that the round-trip comparison would see a
// changed option, a changed rule or routes in another order.
func TestCanonicalSeesChanges(t *testing.T) {
	base := `flow f { a as a -> r as r { when simple "x" -> b(o: 1) as b  otherwise -> c as c } }`
	want := canonical(t, mustCompile(t, base))
	for _, changed := range []string{
		`flow f { a as a -> r as r { when simple "x" -> b(o: 2) as b  otherwise -> c as c } }`,
		`flow f { a as a -> r as r { when simple "y" -> b(o: 1) as b  otherwise -> c as c } }`,
		`flow f { a as a -> r as r { otherwise -> c as c  when simple "x" -> b(o: 1) as b } }`,
		`flow f { a as a -> action r as r { when simple "x" -> b(o: 1) as b  otherwise -> c as c } }`,
	} {
		if reflect.DeepEqual(want, canonical(t, mustCompile(t, changed))) {
			t.Errorf("no difference seen: %s", changed)
		}
	}
}

func mustCompile(t *testing.T, src string) []byte {
	t.Helper()
	data, err := Compile("", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// TestPrintIntegrations checks integrations without fields: the first with
// flows is written as flows outside an integration, the others as blocks.
func TestPrintIntegrations(t *testing.T) {
	flow := func(id string) string {
		return `{"id":"` + id + `","steps":{"step":{"id":"s","type":"source","uri":"timer"}}}`
	}
	src := []byte(`{"dil":{"integrations":{"integration":[
	  {"flows":{"flow":[]}},
	  {"flows":{"flow":[` + flow("a") + `,` + flow("b") + `]}},
	  {"id":"named","version":1,"flows":{"flow":` + flow("c") + `}},
	  {"flows":{"flow":` + flow("d") + `}},
	  {"flows":{"flow":{"name":"no id","options":{"tenant":"t"},"steps":{"step":[]}}}}
	]}}}`)
	text, err := Print(src)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"integration {}\n", "\nflow a {\n  timer as s\n}\n", "integration named(version: 1) {", "integration {\n  flow d {", `  flow(name: "no id", tenant: "t") {}`} {
		if !strings.Contains(string(text), want) {
			t.Errorf("missing %q in\n%s", want, text)
		}
	}
	back, err := Compile("", text)
	if err != nil {
		t.Fatalf("%v\n%s", err, text)
	}
	if !reflect.DeepEqual(canonical(t, src), canonical(t, back)) {
		t.Fatalf("round trip changes the integrations:\n%s", text)
	}
}
