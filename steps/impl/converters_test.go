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
	typeHints := map[string]any{"typeHints": true}
	tests := []struct {
		name string
		opts map[string]any
		xml  string
		want string
	}{
		{"repeated children become an array", nil, persons,
			`{"@xmlns:p":"urn:p","person":{"@id":"1","name":"John Doe"},"p:person":{"name":{"#text":"Jane ","b":"Doe"}},"group":{"person":{"name":"Nested"}}}`},
		{"prefixes removed", map[string]any{"removeNamespacePrefixes": true, "skipNamespaces": true}, persons,
			`{"person":[{"@id":"1","name":"John Doe"},{"name":{"#text":"Jane ","b":"Doe"}}],"group":{"person":{"name":"Nested"}}}`},
		{"same names make an array", nil, `<menu><food>a</food><food>b</food></menu>`, `["a","b"]`},
		{"force top-level object", map[string]any{"forceTopLevelObject": true}, `<menu><food>a</food></menu>`, `{"menu":{"food":"a"}}`},
		{"a root with only text is an array", nil, `<a> x </a>`, `[" x "]`},
		{"an empty element is an empty array", nil, `<a><b/><c></c></a>`, `{"b":[],"c":[]}`},
		{"an empty root is null", nil, `<a/>`, `null`},
		{"trim spaces", map[string]any{"trimSpaces": true}, `<a><b> x </b></a>`, `{"b":"x"}`},
		{"trim spaces in attributes", map[string]any{"trimSpaces": true}, `<a k=" v "><b>1</b></a>`, `{"@k":"v","b":"1"}`},
		{"skip whitespace keeps the text", map[string]any{"skipWhitespace": true}, `<a><b> x </b><c>  </c></a>`, `{"b":" x ","c":"  "}`},
		{"skip whitespace makes an element with attributes and only text null", map[string]any{"skipWhitespace": true}, `<a><b k="1">x</b><c>y</c></a>`, `{"b":null,"c":"y"}`},
		{"skip whitespace makes a lone child an array", map[string]any{"skipWhitespace": true}, `<a><b k="1">x</b></a>`, `[null]`},
		{"skip whitespace still gives a text root an array", map[string]any{"skipWhitespace": true}, `<a>text</a>`, `["text"]`},
		{"namespaces", map[string]any{"skipNamespaces": true, "removeNamespacePrefixes": true}, `<p:a xmlns:p="urn:p" p:id="1"><p:b>x</p:b></p:a>`, `{"@id":"1","b":"x"}`},
		{"namespaces are kept", nil, `<lh:List xmlns:lh="http://liquidhub.com/SimpleList" name="Fruit List"><lh:Item>Apple</lh:Item><lh:Item>Banana</lh:Item></lh:List>`,
			`{"@xmlns:lh":"http://liquidhub.com/SimpleList","@name":"Fruit List","lh:Item":["Apple","Banana"]}`},
		// The platform's answer. The namespace of an element is written on it, unless its parent has it already.
		{"namespaces on children", nil,
			`<Orders xmlns="http://DataAccess.com/webservices/" xmlns:m="http://DataAccess.com/webservicesOrders/" xmlns:x="http://DataAccess.com/webservicesCustomer/"><x:Order><OrderNo>1</OrderNo><m:Customer><m:CustomerNo>99</m:CustomerNo><x:State>CA</x:State><m:State>Angry</m:State><State>Yes</State></m:Customer></x:Order></Orders>`,
			ordersJSON},
		// The quirks of json-lib: with whitespace around it, an element with one child element is an array,
		// and the second of two elements with one name is appended as an array to the first.
		{"an array in an array", nil,
			"<root>\n <products class=\"array\">\n  <product>\n   <id type=\"string\">1</id>\n  </product>\n  <product>\n   <id type=\"string\">2</id>\n  </product>\n </products>\n</root>",
			`[{"@class":"array","product":[{"@type":"string","#text":"1"},[{"@type":"string","#text":"2"}]]}]`},
		{"without whitespace it is an object", nil, `<root><a><b>1</b></a></root>`, `{"a":{"b":"1"}}`},
		{"three of a name", nil, `<r><x k="1">a</x><x k="2">b</x><x k="3">c</x><y/></r>`,
			`{"x":[{"@k":"1","#text":"a"},{"@k":"2","#text":"b"},{"@k":"3","#text":"c"}],"y":[]}`},
		{"escaping", nil, `<a><b>&lt;"x"&amp;</b></a>`, `{"b":"<\"x\"&"}`},
		{"the string null is null, text that looks like JSON is JSON", nil, `<a><n>null</n><l>[1,2]</l><o>{"k":"v"}</o><x>{not json}</x><t>true</t></a>`,
			`{"n":null,"l":[1,2],"o":{"k":"v"},"x":"{not json}","t":"true"}`},
		// They have no content, but they are children: the element has one child element among three, which makes it an array.
		{"comments and processing instructions are children", nil, `<a><!-- c --><b>1</b><?pi x?></a>`, `["1"]`},
		{"comments do not show in an object", nil, `<a><!-- c --><b>1</b><c>2</c></a>`, `{"b":"1","c":"2"}`},
		{"a declared encoding is ignored, the text is characters", nil, "<?xml version=\"1.0\" encoding=\"Windows-1252\"?><a><b>caf\u00e9</b></a>", `{"b":"café"}`},
		{"CDATA is text", nil, `<a><b><![CDATA[1 < 2]]></b></a>`, `{"b":"1 < 2"}`},

		// Without typeHints, class and type are attributes like others.
		{"no type hints", nil, `<a><n type="number">1</n></a>`, `{"n":{"@type":"number","#text":"1"}}`},
		{"the json_ hints always count", nil, `<a><n json_type="number">1</n></a>`, `{"n":1}`},
		{"type hints", typeHints,
			`<a><n type="number">1.5</n><b type="boolean">true</b><z null="true"/><s type="string">1</s><l class="array"><e>x</e></l><o class="object"/><i type="integer">7</i></a>`,
			`{"n":1.5,"b":true,"z":null,"s":"1","l":["x"],"o":null,"i":7}`},
		{"type hints, numbers as Java writes them", typeHints,
			`<a><n type="number">1.0</n><m type="number">007</m><k type="number"> 5 </k><big type="number">12345678901</big><e type="number">1e3</e><f type="float">2</f><g type="number">0.5</g><h type="number">100000000</h></a>`,
			`{"n":1,"m":7,"k":5,"big":1.2345678901E10,"e":1000,"f":2,"g":0.5,"h":100000000}`},
		// json-lib fails on these (a NumberFormatException); the platform's current instances answer null.
		{"type hints, text that is no number is null", typeHints, `<a><n type="number">x</n><i type="integer">1.5</i><f type="float">y</f><e type="number"></e></a>`, `{"n":null,"i":null,"f":null,"e":null}`},
		{"type hints, a boolean is true in any case and else false", typeHints, `<a><x type="boolean">TRUE</x><y type="boolean">yes</y><z type="boolean"> true</z></a>`, `{"x":true,"y":false,"z":false}`},
		{"type hints, the hint attributes are left out and other attributes kept", typeHints, `<a><n id="1" type="number">1</n></a>`, `{"n":1}`},
		{"type hints, an element of class object", typeHints, `<a><o class="object" id="1"><p type="number">2</p></o></a>`, `{"o":{"@id":"1","p":2}}`},
		// json-lib loses the type of an element that has attributes inside an array.
		{"type hints, an array of class array", typeHints, `<a><l class="array"><e type="number">1</e><e type="number">2</e></l></a>`, `{"l":["1","2"]}`},
		{"type hints, an array of plain elements", typeHints, `<a><l class="array"><e>1</e><e>2</e></l></a>`, `{"l":["1","2"]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := convert(t, "xmltojson", tt.opts, tt.xml); got != tt.want {
				t.Errorf("got  %s\nwant %s", got, tt.want)
			}
		})
	}
}

// ordersJSON is what the platform answers for the Orders document of "namespaces on children".
const ordersJSON = `{"@xmlns":"http://DataAccess.com/webservices/","@xmlns:m":"http://DataAccess.com/webservicesOrders/","@xmlns:x":"http://DataAccess.com/webservicesCustomer/","x:Order":{"OrderNo":{"@xmlns":"http://DataAccess.com/webservices/","#text":"1"},"m:Customer":{"@xmlns:m":"http://DataAccess.com/webservicesOrders/","m:CustomerNo":"99","x:State":{"@xmlns:x":"http://DataAccess.com/webservicesCustomer/","#text":"CA"},"m:State":"Angry","State":{"@xmlns":"http://DataAccess.com/webservices/","#text":"Yes"}}}}`

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
			`<o class="object"><n type="number">1</n><l class="array"/><z class="object" null="true"/></o>`},
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
	const doc = `{"store":{"@id":"s1","book":[{"author":"Nigel Rees","price":8.95,"tags":["a"],"isbn":null},{"author":" ","price":12,"tags":[],"new":true}]}}` // attributes come first in XML
	hints := map[string]any{"typeHints": true}
	if got := convert(t, "xmltojson", hints, convert(t, "jsontoxml", hints, doc)); got != doc {
		t.Errorf("round trip\ngot  %s\nwant %s", got, doc)
	}
	// json-lib reads an empty object, which it writes as an empty element, back as null.
	if got := convert(t, "xmltojson", hints, convert(t, "jsontoxml", hints, `{"a":{}}`)); got != `{"a":null}` {
		t.Errorf("empty object: got %s", got)
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
			`{"menu":{"food":[{"@id":1,"jsonContent":"a"},2],"x":true,"y":null,"z":"007"}}`},
		{"keep strings, remove root", map[string]any{"keepStrings": true, "removeRoot": true}, `<menu><food> 2 </food></menu>`, `{"food":"2"}`},
		{"remove namespaces", map[string]any{"removeNamespaces": true}, `<p:a xmlns:p="urn:p"><p:b/></p:a>`, `{"a":{"b":""}}`},
		{"attributes are marked with @", nil, `<p:a xmlns:p="urn:p" p:id="7" x="y">t<b k="1"/></p:a>`, `{"p:a":{"@xmlns:p":"urn:p","@p:id":7,"@x":"y","b":{"@k":1},"jsonContent":"t"}}`},
		{"attributes without namespaces", map[string]any{"removeNamespaces": true}, `<p:a xmlns:p="urn:p" p:id="7" x="y">t</p:a>`, `{"a":{"@id":7,"@x":"y","jsonContent":"t"}}`},
		{"types", map[string]any{"hasTypes": true, "keepStrings": true},
			`<a><i type="integer">12</i><d type="double">1.5</d><b type="boolean">false</b><s type="string">3</s><n type="null">x</n><bad type="integer">1.5</bad></a>`,
			`{"a":{"i":12,"d":1.5,"b":false,"s":"3","n":null,"bad":"1.5"}}`},
		{"type mismatch null", map[string]any{"hasTypes": true, "typeValueMismatch": "NULL"}, `<a><bad type="number">x</bad><odd type="date">x</odd></a>`,
			`{"a":{"bad":null,"odd":null}}`},
		{"type mismatch in lower case", map[string]any{"hasTypes": true, "typeValueMismatch": "null"}, `<a><bad type="number">x</bad></a>`,
			`{"a":{"bad":null}}`},
		{"type mismatch original", map[string]any{"hasTypes": true, "typeValueMismatch": "original"}, `<a><bad type="number">x</bad></a>`,
			`{"a":{"bad":"x"}}`},
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
		{"all but integers quoted", map[string]any{"quoteFields": "non_integer_fields"}, "\"a\",1,\"\",\"\"\n\"\",2,\"x, \"\"y\"\"\",\"\"\n\"\",\"\",\"\",\"solo\"\n"},
		{"descending", map[string]any{"orderHeaders": "descending", "includeHeader": true}, "price,note,name,item\n1,,a,\n2,\"x, \"\"y\"\"\",,\n,,,solo\n"},
		{"ascending is ordered", map[string]any{"orderHeaders": "ascending", "includeHeader": true}, "item,name,note,price\n,a,,1\n,,\"x, \"\"y\"\"\",2\n" + "solo,,,\n"},
		{"the system's end of line", map[string]any{"lineSeparator": "endofline", "orderHeaders": "ascending"}, ",a,,1" + systemEOL + ",,\"x, \"\"y\"\"\",2" + systemEOL + "solo,,," + systemEOL},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := convert(t, "xmltocsv", tt.opts, xml); got != tt.want {
				t.Errorf("got  %q\nwant %q", got, tt.want)
			}
		})
	}
}

func TestXMLToCSVXPath(t *testing.T) {
	const xml = `<items><list><item><ID>1</ID><Name>Pete</Name></item><item><ID>2</ID><Name>John</Name></item></list><item><ID>9</ID><Name>Not</Name></item></items>`
	// The elements the expression selects are the records; by default the root's children are.
	if got := convert(t, "xmltocsv", map[string]any{"xPathExpression": "/items/list/item", "quoteFields": "all_fields"}, xml); got != "\"1\",\"Pete\"\n\"2\",\"John\"\n" {
		t.Errorf("xpath: got %q", got)
	}
	if got := convert(t, "xmltocsv", map[string]any{"xPathExpression": " "}, xml); got != "2John,,\n,9,Not\n" {
		t.Errorf("blank xpath: got %q, want the root's children as records (list, then item)", got)
	}
	wantInvalid(t, stepdef.Action, "xmltocsv", map[string]any{"xPathExpression": "item["}, `option xPathExpression: xpath "item["`)
}

func TestCSVToXMLUseHeaders(t *testing.T) {
	const csv = "a,b\n1,2\n"
	want := convert(t, "csvtoxml", map[string]any{"useHeader": true}, csv)
	if !strings.Contains(want, "<a>1</a>") {
		t.Fatalf("useHeader: %s", want)
	}
	if got := convert(t, "csvtoxml", map[string]any{"useHeaders": "true"}, csv); got != want {
		t.Errorf("useHeaders: got %s, want %s", got, want)
	}
	if got := convert(t, "csvtoxml", map[string]any{"useHeaders": false}, csv); !strings.Contains(got, "<field1>a</field1>") {
		t.Errorf("useHeaders false: got %s", got)
	}
}

func TestConvertersSetContentType(t *testing.T) {
	for _, tt := range []struct{ uri, body, want string }{
		{"xmltojson", "<a><b>1</b></a>", "application/json"},
		{"xmltojsonsimple", "<a><b>1</b></a>", "application/json"},
		{"jsontoxml", `{"b":"1"}`, "application/xml"},
		{"jsontoxmlsimple", `{"a":{"b":"1"}}`, "application/xml"},
		{"csvtoxml", "a,b\n", "application/xml"},
		{"xmltocsv", "<a><b>1</b></a>", "text/csv"},
	} {
		m := message.New(tt.body)
		m[message.ContentType] = "text/plain"
		if got := process(t, tt.uri, nil, m)[message.ContentType]; got != tt.want {
			t.Errorf("%s: Content-Type = %v, want %s", tt.uri, got, tt.want)
		}
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
	for body, want := range map[string]string{
		`<a><n type="float">NaN</n></a>`: "non-finite",
		`<a><b/>`:                        "body is not XML",
		`<p:a/>`:                         "prefix",
	} {
		if _, err := p.Process(context.Background(), message.New(body)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want containing %q", body, err, want)
		}
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
	wantInvalid(t, stepdef.Action, "xmltojsonsimple", map[string]any{"typeValueMismatch": "WARN"}, `option typeValueMismatch: "WARN" is not one of`)
}

func TestXMLToJSONSimpleTypeMismatchError(t *testing.T) {
	for _, mismatch := range []string{"ERROR", "error"} {
		p := mustProcessor(t, stepdef.Action, "xmltojsonsimple", map[string]any{"hasTypes": true, "typeValueMismatch": mismatch}).(stepdef.ActionProcessor)
		for _, xml := range []string{
			`<a><bad type="number">x</bad></a>`,
			`<a><b><bad type="boolean">yes</bad></b></a>`, // two levels down
			`<a><odd type="date">x</odd></a>`,             // a type that is not known
		} {
			if _, err := p.Process(context.Background(), message.New(xml)); err == nil || !strings.Contains(err.Error(), "does not fit its type") {
				t.Errorf("%s %s: err = %v, want a type mismatch", mismatch, xml, err)
			}
		}
		out, err := p.Process(context.Background(), message.New(`<a><n type="number">1</n></a>`))
		if err != nil || out[message.Body] != `{"a":{"n":1}}` {
			t.Errorf("%s: a fitting value: %v, %v", mismatch, out[message.Body], err)
		}
	}
}
