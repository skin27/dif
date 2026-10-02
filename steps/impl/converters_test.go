package impl

import (
	"context"
	"strings"
	"testing"

	"dif/message"
	stepdef "dif/steps/definition"
)

func convert(t *testing.T, uri string, opts map[string]any, body any) string {
	t.Helper()
	return process(t, uri, opts, message.New(body))[message.Body].(string)
}

func TestXMLToJSON(t *testing.T) {
	tests := []struct {
		name string
		opts map[string]any
		xml  string
		want string
	}{
		{"repeated children become an array", nil, persons,
			`{"@xmlns:p":"urn:p","person":{"@id":"1","name":"John Doe"},"p:person":{"name":{"b":"Doe","#text":"Jane "}},"group":{"person":{"name":"Nested"}}}`},
		{"prefixes removed", map[string]any{"removeNamespacePrefixes": true, "skipNamespaces": true}, persons,
			`{"person":[{"@id":"1","name":"John Doe"},{"name":{"b":"Doe","#text":"Jane "}}],"group":{"person":{"name":"Nested"}}}`},
		{"same names make an array", nil, `<menu><food>a</food><food>b</food></menu>`, `["a","b"]`},
		{"force top-level object", map[string]any{"forceTopLevelObject": true}, `<menu><food>a</food></menu>`, `{"menu":{"food":"a"}}`},
		{"leaf root", nil, `<a> x </a>`, `" x "`},
		{"empty element", nil, `<a><b/><c></c></a>`, `{"b":"","c":""}`},
		{"trim spaces", map[string]any{"trimSpaces": true}, `<a><b> x </b></a>`, `{"b":"x"}`},
		{"skip whitespace", map[string]any{"skipWhitespace": true}, `<a><b> x </b><c>  </c></a>`, `{"b":" x ","c":""}`},
		{"namespaces", map[string]any{"skipNamespaces": true, "removeNamespacePrefixes": true}, `<p:a xmlns:p="urn:p" p:id="1"><p:b>x</p:b></p:a>`, `{"@id":"1","b":"x"}`},
		{"no type hints", nil, `<a><n json_type="number">1</n></a>`, `{"n":{"@json_type":"number","#text":"1"}}`},
		{"type hints", map[string]any{"typeHints": true},
			`<a><n json_type="number">1.5</n><b json_type="boolean">true</b><z json_type="null"/><s json_type="string">1</s><l json_type="array"><e>x</e></l><o json_type="object"/></a>`,
			`{"n":1.5,"b":true,"z":null,"s":"1","l":["x"],"o":{}}`},
		{"escaping", nil, `<a><b>&lt;"x"&amp;</b></a>`, `{"b":"<\"x\"&"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := convert(t, "xmltojson", tt.opts, tt.xml); got != tt.want {
				t.Errorf("got  %s\nwant %s", got, tt.want)
			}
		})
	}
}

func TestJSONToXML(t *testing.T) {
	tests := []struct {
		name string
		opts map[string]any
		json string
		want string
	}{
		{"object", nil, `{"b":"x","a":{"@id":"1","#text":"t","c":[1,true,null]}}`,
			`<o><b>x</b><a id="1">t<c><e>1</e><e>true</e><e/></c></a></o>`},
		{"names", map[string]any{"rootName": "r", "arrayName": "list", "elementName": "item"}, `[{"a":"<&>"}]`,
			`<list><item><a>&lt;&amp;&gt;</a></item></list>`},
		{"value", nil, `"x"`, `<o>x</o>`},
		{"type hints", map[string]any{"typeHints": true}, `{"n":1,"l":[],"z":null}`,
			`<o json_type="object"><n json_type="number">1</n><l json_type="array"/><z json_type="null"/></o>`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := convert(t, "jsontoxml", tt.opts, tt.json); got != xmlDecl+tt.want {
				t.Errorf("got  %s\nwant %s", got, xmlDecl+tt.want)
			}
		})
	}
}

func TestJSONXMLRoundTrip(t *testing.T) {
	const doc = `{"store":{"@id":"s1","book":[{"author":"Nigel Rees","price":8.95,"tags":["a"],"isbn":null},{"author":" ","price":12,"tags":[],"new":true}],"empty":{}}}` // attributes come first in XML
	hints := map[string]any{"typeHints": true}
	if got := convert(t, "xmltojson", hints, convert(t, "jsontoxml", hints, doc)); got != doc {
		t.Errorf("round trip\ngot  %s\nwant %s", got, doc)
	}

	const simple = `{"a":{"b":[1,2],"c":"x","d":{"e":true}}}`
	if got := convert(t, "xmltojsonsimple", nil, convert(t, "jsontoxmlsimple", nil, simple)); got != simple {
		t.Errorf("simple round trip\ngot  %s\nwant %s", got, simple)
	}
}

func TestXMLToJSONSimple(t *testing.T) {
	tests := []struct {
		name string
		opts map[string]any
		xml  string
		want string
	}{
		{"root and types", nil, `<menu><food id="1">a</food><food>2</food><x>true</x><y>null</y><z>007</z></menu>`,
			`{"menu":{"food":[{"id":1,"content":"a"},2],"x":true,"y":null,"z":"007"}}`},
		{"keep strings, remove root", map[string]any{"keepStrings": true, "removeRoot": true}, `<menu><food> 2 </food></menu>`, `{"food":"2"}`},
		{"remove namespaces", map[string]any{"removeNamespaces": true}, `<p:a xmlns:p="urn:p"><p:b/></p:a>`, `{"a":{"b":""}}`},
		{"types", map[string]any{"hasTypes": true, "keepStrings": true},
			`<a><i type="integer">12</i><d type="double">1.5</d><b type="boolean">false</b><s type="string">3</s><n type="null">x</n><bad type="integer">1.5</bad></a>`,
			`{"a":{"i":12,"d":1.5,"b":false,"s":"3","n":null,"bad":"1.5"}}`},
		{"type mismatch null", map[string]any{"hasTypes": true, "typeValueMismatch": "NULL"}, `<a><bad type="number">x</bad><odd type="date">x</odd></a>`,
			`{"a":{"bad":null,"odd":null}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := convert(t, "xmltojsonsimple", tt.opts, tt.xml); got != tt.want {
				t.Errorf("got  %s\nwant %s", got, tt.want)
			}
		})
	}
}

func TestJSONToXMLSimple(t *testing.T) {
	tests := []struct {
		name string
		opts map[string]any
		json string
		want string
	}{
		{"members", nil, `{"a":{"b":[1,{"c":null}],"content":"t"},"x":""}`, `<a><b>1</b><b><c>null</c></b>t</a><x/>`},
		{"root and array elements", map[string]any{"addRoot": true, "rootTag": "r", "changeArrayElements": true, "arrayElementName": "i"}, `{"b":[1,2]}`,
			`<r><b><i>1</i><i>2</i></b></r>`},
		{"top-level array", nil, `[1,"<"]`, `<element>1</element><element>&lt;</element>`},
		{"invalid keys", nil, `{"1 a":{"b c":1}}`, `<_1_a><b_c>1</b_c></_1_a>`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := convert(t, "jsontoxmlsimple", tt.opts, tt.json); got != tt.want {
				t.Errorf("got  %s\nwant %s", got, tt.want)
			}
		})
	}
}

func TestCSVToXML(t *testing.T) {
	const csv = "firstname|last name|age\nJoe|Foo|21\nJohn|\"D|oe\"||x\n"
	got := convert(t, "csvtoxml", map[string]any{"delimiter": "|", "useHeader": "true", "encoding": "UTF-16"}, csv)
	want := `<?xml version="1.0" encoding="UTF-16"?>` + "\n" +
		`<rows><row><firstname>Joe</firstname><last_name>Foo</last_name><age>21</age></row>` +
		`<row><firstname>John</firstname><last_name>D|oe</last_name><age/><field4>x</field4></row></rows>`
	if got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	got = convert(t, "csvtoxml", nil, "a,<b>\n")
	if want := xmlDecl + `<rows><row><field1>a</field1><field2>&lt;b&gt;</field2></row></rows>`; got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}
	if got := convert(t, "csvtoxml", nil, ""); got != xmlDecl+"<rows></rows>" {
		t.Errorf("empty: got %s", got)
	}
}

func TestXMLToCSV(t *testing.T) {
	const xml = `<menu><food><name>a</name><price>1</price></food><food><price> 2 </price><note>x, "y"</note><name/></food><item>solo</item></menu>`
	tests := []struct {
		name string
		opts map[string]any
		want string
	}{
		{"defaults", nil, "a,1,,\n,2,\"x, \"\"y\"\"\",\n,,,solo\n"},
		{"header, index, ordered, all quoted, crlf",
			map[string]any{"includeHeader": true, "includeIndexColumn": true, "orderHeaders": "ordered", "quoteFields": "all_fields", "lineSeparator": "carriage_return_linefeed", "delimiter": ";"},
			"\"line\";\"item\";\"name\";\"note\";\"price\"\r\n\"1\";\"\";\"a\";\"\";\"1\"\r\n\"2\";\"\";\"\";\"x, \"\"y\"\"\";\"2\"\r\n\"3\";\"solo\";\"\";\"\";\"\"\r\n"},
		{"non-empty quoted", map[string]any{"quoteFields": "non_empty_fields", "includeHeader": true}, "\"name\",\"price\",\"note\",\"item\"\n\"a\",\"1\",,\n,\"2\",\"x, \"\"y\"\"\",\n,,,\"solo\"\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := convert(t, "xmltocsv", tt.opts, xml); got != tt.want {
				t.Errorf("got  %q\nwant %q", got, tt.want)
			}
		})
	}
}

func TestConvertersInvalid(t *testing.T) {
	for _, tt := range []struct{ uri, body, want string }{
		{"xmltojson", "not xml", "body is not XML"},
		{"xmltojson", "<a><b></a>", "body is not XML"},
		{"xmltojson", "<a/><b/>", "more than one root element"},
		{"xmltojsonsimple", "{}", "body is not XML"},
		{"xmltocsv", "", "body is not XML: no root element"},
		{"jsontoxml", "<a/>", "body is not JSON"},
		{"jsontoxml", `{"a":1} x`, "body is not JSON"},
		{"jsontoxml", `{"a b":1}`, `key "a b" is not an XML name`},
		{"jsontoxml", `{"@a":{"b":1}}`, "attribute @a: an object is not a value"},
		{"jsontoxmlsimple", "x", "body is not JSON"},
		{"csvtoxml", "a,\"b\n", "body is not CSV"},
	} {
		p := mustProcessor(t, stepdef.Action, tt.uri, nil).(stepdef.ActionProcessor)
		if _, err := p.Process(context.Background(), message.New(tt.body)); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s %q: err = %v, want containing %q", tt.uri, tt.body, err, tt.want)
		}
	}
	p := mustProcessor(t, stepdef.Action, "xmltojson", map[string]any{"typeHints": true}).(stepdef.ActionProcessor)
	if _, err := p.Process(context.Background(), message.New(`<a><n json_type="number">x</n></a>`)); err == nil || !strings.Contains(err.Error(), `json_type number: "x" is not a number`) {
		t.Errorf("err = %v", err)
	}
	if _, err := newProcessor(stepdef.Action, "jsontoxmlsimple", map[string]any{"checkJsonKeys": true}); err != nil {
		t.Fatal(err)
	}
	p = mustProcessor(t, stepdef.Action, "jsontoxmlsimple", map[string]any{"checkJsonKeys": "true"}).(stepdef.ActionProcessor)
	if _, err := p.Process(context.Background(), message.New(`{"a b":1}`)); err == nil || !strings.Contains(err.Error(), `key "a b" is not an XML name`) {
		t.Errorf("err = %v", err)
	}

	wantInvalid(t, stepdef.Action, "jsontoxml", map[string]any{"rootName": "1x"}, `option rootName: "1x" is not an XML name`)
	wantInvalid(t, stepdef.Action, "jsontoxmlsimple", map[string]any{"arrayElementName": ""}, `option arrayElementName: "" is not an XML name`)
	wantInvalid(t, stepdef.Action, "csvtoxml", map[string]any{"delimiter": "||"}, "option delimiter")
	wantInvalid(t, stepdef.Action, "csvtoxml", map[string]any{"encoding": `UTF-8"`}, "option encoding")
	wantInvalid(t, stepdef.Action, "xmltocsv", map[string]any{"delimiter": ""}, "option delimiter")
	wantInvalid(t, stepdef.Action, "xmltocsv", map[string]any{"quoteFields": "some"}, `option quoteFields: "some" is not one of`)
	wantInvalid(t, stepdef.Action, "xmltojsonsimple", map[string]any{"typeValueMismatch": "ERROR"}, `option typeValueMismatch: "ERROR" is not one of`)
}
