package impl

import (
	"context"
	"strings"
	"testing"

	"dif/message"
	stepdef "dif/steps/definition"
)

func docConvert(t *testing.T, how, body string) string {
	t.Helper()
	m := process(t, "docconverter", map[string]any{"convert": how}, message.New(body))
	return m[message.Body].(string)
}

func docConvertErr(t *testing.T, how, body string) error {
	t.Helper()
	p := mustProcessor(t, stepdef.Action, "docconverter", map[string]any{"convert": how}).(stepdef.ActionProcessor)
	_, err := p.Process(context.Background(), message.New(body))
	return err
}

const (
	docCSV  = "id,name,price,ok\nP-1,\"Coffee, large\",899.95,true\nP-2,,0,\n"
	docJSON = `{"rows":{"row":[{"item":["id","name","price","ok"]},{"item":["P-1","Coffee, large","899.95","true"]},{"item":["P-2","","0",""]}]}}`
)

func TestDocConverterFromCSV(t *testing.T) {
	if got := docConvert(t, "csvtojson", docCSV); got != docJSON {
		t.Errorf("csvtojson = %s\nwant %s", got, docJSON)
	}
	wantXML := `<?xml version='1.0' encoding='UTF-8'?><rows><row><item>id</item><item>name</item><item>price</item><item>ok</item></row>` +
		`<row><item>P-1</item><item>Coffee, large</item><item>899.95</item><item>true</item></row>` +
		`<row><item>P-2</item><item/><item>0</item><item/></row></rows>`
	if got := docConvert(t, "csvtoxml", docCSV); got != wantXML {
		t.Errorf("csvtoxml = %s\nwant %s", got, wantXML)
	}
	// YAML types what CSV has no type for; a string that is no number stays one.
	wantYAML := `---
rows:
  row:
    - item:
        - "id"
        - "name"
        - "price"
        - "ok"
    - item:
        - "P-1"
        - "Coffee, large"
        - 899.95
        - true
    - item:
        - "P-2"
        - ""
        - 0
        - ""
`
	if got := docConvert(t, "csvtoyaml", docCSV); got != wantYAML {
		t.Errorf("csvtoyaml = %s\nwant %s", got, wantYAML)
	}

	// A byte order mark and CRLF line ends do not belong to the data.
	if got := docConvert(t, "csvtojson", "\xef\xbb\xbfa,b\r\nc,d\r\n"); got != `{"rows":{"row":[{"item":["a","b"]},{"item":["c","d"]}]}}` {
		t.Errorf("csvtojson with BOM and CRLF = %s", got)
	}
	// Records of different lengths are kept as they are; nothing is a table too.
	if got := docConvert(t, "csvtojson", "a\nb,c,d\n"); got != `{"rows":{"row":[{"item":["a"]},{"item":["b","c","d"]}]}}` {
		t.Errorf("csvtojson uneven = %s", got)
	}
	if got := docConvert(t, "csvtojson", ""); got != `{"rows":{"row":[]}}` {
		t.Errorf("csvtojson of nothing = %s", got)
	}
	if err := docConvertErr(t, "csvtojson", "a,\"b\nc"); err == nil || !strings.Contains(err.Error(), "body is not CSV") {
		t.Errorf("err = %v, want not CSV", err)
	}
}

const docXML = `<?xml version="1.0"?>
<product>
  <id>PRD-1</id>
  <price><amount>899.95</amount><vatIncluded>true</vatIncluded><note>007</note></price>
  <tags><tag>coffee</tag><tag>espresso</tag></tags>
  <stock><warehouse><location>Amsterdam</location><quantity>24</quantity></warehouse><warehouse><location>Rotterdam</location><quantity>0</quantity></warehouse></stock>
  <lead x="1"/>
</product>`

func TestDocConverterFromXML(t *testing.T) {
	// To JSON the values are text, and repeated elements an array, in document order.
	wantJSON := `{"product":{"id":"PRD-1","price":{"amount":"899.95","vatIncluded":"true","note":"007"},"tags":{"tag":["coffee","espresso"]},` +
		`"stock":{"warehouse":[{"location":"Amsterdam","quantity":"24"},{"location":"Rotterdam","quantity":"0"}]},"lead":{"@x":"1"}}}`
	if got := docConvert(t, "xmltojson", docXML); got != wantJSON {
		t.Errorf("xmltojson = %s\nwant %s", got, wantJSON)
	}

	// To YAML the values are typed ("007" is no number), and the members of an
	// object come in the order of a Java HashMap, as the platform writes them.
	wantYAML := `---
product:
  price:
    note: "007"
    amount: 899.95
    vatIncluded: true
  id: "PRD-1"
  stock:
    warehouse:
      - quantity: 24
        location: "Amsterdam"
      - quantity: 0
        location: "Rotterdam"
  lead:
    "@x": 1
  tags:
    tag:
      - "coffee"
      - "espresso"
`
	if got := docConvert(t, "xmltoyaml", docXML); got != wantYAML {
		t.Errorf("xmltoyaml = %s\nwant %s", got, wantYAML)
	}
	if err := docConvertErr(t, "xmltojson", "not xml"); err == nil {
		t.Error("xmltojson accepted text")
	}
}

const docProductJSON = `{"product":{"id":"PRD-1","price":{"amount":899.95,"vat":true},"tags":["coffee","espresso"],"dims":[],"none":null,"n":12,"s":"12","e":{}}}`

func TestDocConverterFromJSON(t *testing.T) {
	// An array is one element per item, named after its key; no declaration.
	wantXML := `<product><id>PRD-1</id><price><amount>899.95</amount><vat>true</vat></price><tags>coffee</tags><tags>espresso</tags>` +
		`<none>null</none><n>12</n><s>12</s><e/></product>`
	if got := docConvert(t, "jsontoxml", docProductJSON); got != wantXML {
		t.Errorf("jsontoxml = %s\nwant %s", got, wantXML)
	}
	// YAML keeps the types and the order of the document; strings are quoted, even "12", and so is a key YAML 1.1 reads as a boolean ("n").
	wantYAML := `---
product:
  id: "PRD-1"
  price:
    amount: 899.95
    vat: true
  tags:
    - "coffee"
    - "espresso"
  dims: []
  none: null
  "n": 12
  s: "12"
  e: {}
`
	if got := docConvert(t, "jsontoyaml", docProductJSON); got != wantYAML {
		t.Errorf("jsontoyaml = %s\nwant %s", got, wantYAML)
	}
	if got := docConvert(t, "jsontoyaml", `[1,"two",{"three":[3.5e2]}]`); got != "---\n- 1\n- \"two\"\n- three:\n    - 3.5e2\n" {
		t.Errorf("jsontoyaml of an array = %q", got)
	}
	if err := docConvertErr(t, "jsontoxml", "{"); err == nil || !strings.Contains(err.Error(), "body is not JSON") {
		t.Errorf("err = %v", err)
	}
}

func TestDocConverterFromYAML(t *testing.T) {
	in := `---
rows:
  row:
  - item: ["id", "name", price, flag, none, hex, big, "2026-07-17T10:15:30Z", 2026-07-17]
  - item: [a, 'b: c', 1.5e3, true, ~, 0x1F, 12345678901234567890, "x", y]
`
	want := `{"rows":{"row":[{"item":["id","name","price","flag","none","hex","big","2026-07-17T10:15:30Z","2026-07-17"]},` +
		`{"item":["a","b: c",1.5e3,true,null,31,12345678901234567890,"x","y"]}]}}`
	if got := docConvert(t, "yamltojson", in); got != want {
		t.Errorf("yamltojson = %s\nwant %s", got, want)
	}
	// Anchors and aliases are resolved, a later key wins, and only the first document is read.
	if got := docConvert(t, "yamltojson", "a: &x {b: 1}\nc: *x\nd: 1\nd: 2\n---\nignored: 1\n"); got != `{"a":{"b":1},"c":{"b":1},"d":2}` {
		t.Errorf("yamltojson with aliases = %s", got)
	}
	if got := docConvert(t, "yamltoxml", "p:\n  q: [1, 2]\n  r: text & <more>\n"); got != `<p><q>1</q><q>2</q><r>text &amp; &lt;more&gt;</r></p>` {
		t.Errorf("yamltoxml = %s", got)
	}
	for _, bad := range []string{"", "a: [", "a: b: c", "? [a]\n: b\n"} {
		if err := docConvertErr(t, "yamltojson", bad); err == nil || !strings.Contains(err.Error(), "body is not YAML") {
			t.Errorf("yamltojson(%q): err = %v, want not YAML", bad, err)
		}
	}
}

func TestDocConverterAliasesCannotExplode(t *testing.T) {
	var b strings.Builder
	b.WriteString("a0: &a0 [x, x, x, x, x, x, x, x, x, x]\n")
	for i := 1; i < 12; i++ {
		prev := "a" + string(rune('0'+i-1))
		cur := "a" + string(rune('0'+i))
		b.WriteString(cur + ": &" + cur + " [*" + prev + ", *" + prev + ", *" + prev + ", *" + prev + ", *" + prev + ", *" + prev + ", *" + prev + ", *" + prev + ", *" + prev + ", *" + prev + "]\n")
	}
	if err := docConvertErr(t, "yamltojson", b.String()); err == nil {
		t.Error("an alias bomb was expanded")
	}
}

func TestDocConverterToCSV(t *testing.T) {
	// What csvtoyaml and csvtoxml write converts back, quoting where CSV needs it.
	yaml := docConvert(t, "csvtoyaml", docCSV)
	if got := docConvert(t, "yamltocsv", yaml); got != "id,name,price,ok\nP-1,\"Coffee, large\",899.95,true\nP-2,,0,\n" {
		t.Errorf("yamltocsv = %q", got)
	}
	if got := docConvert(t, "jsontocsv", docJSON); got != "id,name,price,ok\nP-1,\"Coffee, large\",899.95,true\nP-2,,0,\n" {
		t.Errorf("jsontocsv = %q", got)
	}
	// The XML of one row or one item is not an array.
	if got := docConvert(t, "xmltocsv", `<rows><row><item>a</item><item>b</item></row></rows>`); got != "a,b\n" {
		t.Errorf("xmltocsv of one row = %q", got)
	}
	if got := docConvert(t, "xmltocsv", `<rows><row><item>a</item></row><row><item>b</item></row></rows>`); got != "a\nb\n" {
		t.Errorf("xmltocsv of one item per row = %q", got)
	}
	if got := docConvert(t, "jsontocsv", `{"t":{"row":[{"item":["say \"hi\"","line\nbreak",null,false,1]}]}}`); got != "\"say \"\"hi\"\"\",\"line\nbreak\",,false,1\n" {
		t.Errorf("jsontocsv quoting = %q", got)
	}
	for _, in := range []string{
		`{"product":{"id":1}}`,                         // not a table
		`[["a","b"]]`,                                  // a list of lists is not the table
		`{"rows":{"row":[{"item":[{"a":1}]}]}}`,        // a cell that is an object
		`{"rows":{"row":[{"item":["a"],"x":1}]}}`,      // a row with more than item
		`{"rows":{"row":["a"]}}`,                       // a row that is no object
		`{"rows":{"row":[{"item":"a"}],"other":true}}`, // more than row
	} {
		if err := docConvertErr(t, "jsontocsv", in); err == nil || !strings.Contains(err.Error(), "not a table") {
			t.Errorf("jsontocsv(%s): err = %v, want not a table", in, err)
		}
	}
}

func TestDocConverterContentType(t *testing.T) {
	for how, want := range map[string]string{
		"csvtojson": "application/json", "csvtoxml": "application/xml", "csvtoyaml": "application/yaml",
		"jsontocsv": "text/csv",
	} {
		in := docCSV
		if how == "jsontocsv" {
			in = docJSON
		}
		m := process(t, "docconverter", map[string]any{"convert": how}, message.New(in))
		if m[message.ContentType] != want {
			t.Errorf("%s: content type = %v, want %s", how, m[message.ContentType], want)
		}
	}
}

func TestDocConverterOptions(t *testing.T) {
	// The default is the Kamelet's.
	if got := docConvert(t, "xmltojson", "<a>1</a>"); got != `{"a":"1"}` {
		t.Errorf("xmltojson = %s", got)
	}
	m := process(t, "docconverter", nil, message.New("<a>1</a>"))
	if m[message.Body] != `{"a":"1"}` {
		t.Errorf("default conversion = %v, want xmltojson", m[message.Body])
	}
	if got := docConvert(t, "XmlToJson", "<a>1</a>"); got != `{"a":"1"}` {
		t.Errorf("XmlToJson = %s", got)
	}
	for _, bad := range []string{"jsontojson", "xmltoxls", "tojson", "xml", "yamltoyaml", ""} {
		wantInvalid(t, stepdef.Action, "docconverter", map[string]any{"convert": bad}, "convert")
	}
}

func TestDocConverterYAMLNumbersKeepTheirText(t *testing.T) {
	in := `[12345678901234567890, 1E3, -0, 0.10, 1e-7, 100000000000000000000.5, true, false, null, "text"]`
	want := "---\n- 12345678901234567890\n- 1E3\n- -0\n- 0.10\n- 1e-7\n- 100000000000000000000.5\n- true\n- false\n- null\n- \"text\"\n"
	if got := docConvert(t, "jsontoyaml", in); got != want {
		t.Errorf("jsontoyaml = %q\nwant %q", got, want)
	}
}
