package impl

import (
	"encoding/base64"
	"strings"
	"testing"

	"dif/message"
	stepdef "dif/steps/definition"
)

// excelToXML runs the workbook through exceltoxml with the rules.
func excelToXML(t *testing.T, rules string, workbook []byte) string {
	t.Helper()
	m := process(t, "exceltoxml", map[string]any{"rules": rules}, message.New(workbook))
	if m[message.ContentType] != "application/xml" {
		t.Errorf("Content-Type = %v, want application/xml", m[message.ContentType])
	}
	return m[message.Body].(string)
}

func TestExcelToXML(t *testing.T) {
	book := menuWorkbook(t)
	for name, tc := range map[string]struct{ rules, want string }{
		// examples/exceltoxml.json
		"example rule": {
			`[{"name":"node1","worksheet":"Sheet1","cellRange":"A2:C4","transpose":false,"headerRow":false,"discardEmpty":false,"_id":"c94df562"}]`,
			`<node1><row><field1>Food</field1><field2>1.5</field2><field3>a&lt;b</field3></row>` +
				`<row><field1>Cake</field1><field2>2</field2><field3/></row>` +
				`<row><field1/><field2>TRUE</field2><field3/></row></node1>`,
		},
		"header row": {
			`[{"name":"menu","worksheet":"Sheet1","cellRange":"A1:C3","headerRow":true}]`,
			`<menu><row><Name>Food</Name><Price>1.5</Price><Note>a&lt;b</Note></row><row><Name>Cake</Name><Price>2</Price><Note/></row></menu>`,
		},
		"discard empty": {
			`[{"name":"d","cellRange":"A3:C4","discardEmpty":true},{"name":"e","cellRange":"A4","discardEmpty":true},{"name":"f","cellRange":"A4"}]`,
			`<d><row><field1>Cake</field1><field2>2</field2></row><row><field2>TRUE</field2></row></d><e></e><f><row><field1/></row></f>`,
		},
		"transpose": {
			`[{"name":"t","worksheet":"Sheet1","cellRange":"A1:C2","transpose":true}]`,
			`<t><row><field1>Name</field1><field2>Food</field2></row><row><field1>Price</field1><field2>1.5</field2></row><row><field1>Note</field1><field2>a&lt;b</field2></row></t>`,
		},
		"transpose with header": {
			`[{"name":"t","cellRange":"A1:C2","transpose":"true","headerRow":"true"}]`,
			`<t><row><Name>Price</Name><Food>1.5</Food></row><row><Name>Note</Name><Food>a&lt;b</Food></row></t>`,
		},
		"whole sheet": {
			`[{"name":"all"}]`,
			`<all><row><field1>Name</field1><field2>Price</field2><field3>Note</field3></row>` +
				`<row><field1>Food</field1><field2>1.5</field2><field3>a&lt;b</field3></row>` +
				`<row><field1>Cake</field1><field2>2</field2><field3/></row>` +
				`<row><field1/><field2>TRUE</field2><field3/></row>` +
				`<row><field1>x</field1><field2>7</field2><field3/></row></all>`,
		},
		"two rules, names": {
			`[{"worksheet":"Data"},{"cellRange":"B2:B3"},{"name":"1 st"}]`,
			`<Data><row><field1>data</field1></row></Data>` +
				`<sheet2><row><field1>1.5</field1></row><row><field1>2</field1></row></sheet2>` +
				`<_1_st><row><field1>Name</field1><field2>Price</field2><field3>Note</field3></row>` +
				`<row><field1>Food</field1><field2>1.5</field2><field3>a&lt;b</field3></row>` +
				`<row><field1>Cake</field1><field2>2</field2><field3/></row>` +
				`<row><field1/><field2>TRUE</field2><field3/></row>` +
				`<row><field1>x</field1><field2>7</field2><field3/></row></_1_st>`,
		},
		"range beyond the data": {
			`[{"name":"r","cellRange":"C4:D5"}]`,
			`<r><row><field1/><field2/></row><row><field1/><field2/></row></r>`,
		},
		"header names": {
			`[{"name":"h","cellRange":"A4:C5","headerRow":true}]`,
			`<h><row><field1>x</field1><TRUE>7</TRUE><field3/></row></h>`, // no header in A4; B4 is TRUE
		},
	} {
		if got := excelToXML(t, tc.rules, book); got != "<workbook>"+tc.want+"</workbook>" {
			t.Errorf("%s:\n got %s\nwant <workbook>%s</workbook>", name, got, tc.want)
		}
	}
}

func TestExcelToXMLErrors(t *testing.T) {
	book := menuWorkbook(t)
	p := mustProcessor(t, stepdef.Action, "exceltoxml", map[string]any{"rules": `[{"name":"a"},{"name":"b","worksheet":"Nope"}]`}).(stepdef.ActionProcessor)
	if _, err := p.Process(t.Context(), message.New(book)); err == nil || err.Error() != `rule 2: no worksheet "Nope"` {
		t.Errorf("err = %v", err)
	}
	if _, err := p.Process(t.Context(), message.New("a,b\n1,2")); err == nil || !strings.Contains(err.Error(), "body is not an xlsx workbook") {
		t.Errorf("not xlsx: err = %v", err)
	}
	big := mustProcessor(t, stepdef.Action, "exceltoxml", map[string]any{"rules": `[{"cellRange":"A1:XFD1048576"}]`}).(stepdef.ActionProcessor)
	if _, err := big.Process(t.Context(), message.New(book)); err == nil || !strings.Contains(err.Error(), "is more than the 1000000 a rule can convert") {
		t.Errorf("too big: err = %v", err)
	}

	for rules, want := range map[string]string{
		`nope`:                             "option rules: want a JSON list of rules",
		`[]`:                               "option rules: no rules",
		`[{"cellRange":"A:C"}]`:            `rule 1: cellRange: "A:C" is not a cell range such as A2:C4`,
		`[{"name":"a"},{"cellRange":"?"}]`: "rule 2: cellRange",
		`[{"transpose":"perhaps"}]`:        "option rules",
	} {
		wantInvalid(t, stepdef.Action, "exceltoxml", map[string]any{"rules": rules}, want)
	}
	wantInvalid(t, stepdef.Action, "exceltoxml", nil, "missing required option rules")
}

func TestExcelToXMLEmptyWorksheet(t *testing.T) {
	data, err := writeXLSX([]outSheet{{"Blank", nil}})
	if err != nil {
		t.Fatal(err)
	}
	if got := excelToXML(t, `[{"name":"b"}]`, data); got != "<workbook><b></b></workbook>" {
		t.Errorf("body = %s", got)
	}
}

// examples/xmltoexcel.json: the worksheets option is RAW(base64) of a JSON list of JSON texts.
var exampleWorksheets = "RAW(" + base64.StdEncoding.EncodeToString([]byte(`["{\"name\":\"\",\"xPathExpression\":\"\"}"]`)) + ")"

const menuXML = `<menu>
	<item><name>Food</name><price>1.5</price><code>007</code></item>
	<item><price>2</price><name>Cake &amp; tea</name></item>
</menu>`

// cellsOf returns the cells of the worksheets in the workbook, as "Sheet!A1=value".
func cellsOf(t *testing.T, workbook any) []string {
	t.Helper()
	sheets, err := readXLSX(bytesOf(workbook))
	if err != nil {
		t.Fatal(err)
	}
	var cells []string
	for _, s := range sheets {
		for r := s.from.row; r <= s.to.row; r++ {
			for c := s.from.col; c <= s.to.col; c++ {
				if v, ok := s.cells[cellRef{r, c}]; ok {
					cells = append(cells, s.name+"!"+cellRef{r, c}.String()+"="+v)
				}
			}
		}
	}
	return cells
}

func TestXMLToExcel(t *testing.T) {
	for name, tc := range map[string]struct {
		opts map[string]any
		want string
	}{
		"plain": {nil, "Sheet1!A1=Food Sheet1!B1=1.5 Sheet1!C1=007 Sheet1!A2=Cake & tea Sheet1!B2=2"},
		"header and index": {
			map[string]any{"includeHeader": true, "includeIndexColumn": true, "indexColumnName": "no"},
			"Sheet1!A1=no Sheet1!B1=name Sheet1!C1=price Sheet1!D1=code Sheet1!A2=1 Sheet1!B2=Food Sheet1!C2=1.5 Sheet1!D2=007 Sheet1!A3=2 Sheet1!B3=Cake & tea Sheet1!C3=2",
		},
		"ordered": {
			map[string]any{"includeHeader": true, "orderHeaders": "ordered"},
			"Sheet1!A1=code Sheet1!B1=name Sheet1!C1=price Sheet1!A2=007 Sheet1!B2=Food Sheet1!C2=1.5 Sheet1!B3=Cake & tea Sheet1!C3=2",
		},
		// examples/xmltoexcel.json
		"example options": {
			map[string]any{"includeHeader": true, "includeIndexColumn": false, "indexColumnName": "line", "orderHeaders": "unordered", "excelFormat": "xlsx", "useCustomWorksheets": false, "worksheets": exampleWorksheets},
			"Sheet1!A1=name Sheet1!B1=price Sheet1!C1=code Sheet1!A2=Food Sheet1!B2=1.5 Sheet1!C2=007 Sheet1!A3=Cake & tea Sheet1!B3=2",
		},
		"empty worksheet entry": {
			map[string]any{"useCustomWorksheets": true, "worksheets": exampleWorksheets},
			"Sheet1!A1=Food Sheet1!B1=1.5 Sheet1!C1=007 Sheet1!A2=Cake & tea Sheet1!B2=2",
		},
		"custom worksheets": {
			map[string]any{"includeHeader": true, "useCustomWorksheets": true, "worksheets": `[{"name":"Foods","xPathExpression":"/menu/item"},"{\"name\":\"Names\",\"xPathExpression\":\"/menu/item/name\"}"]`},
			"Foods!A1=name Foods!B1=price Foods!C1=code Foods!A2=Food Foods!B2=1.5 Foods!C2=007 Foods!A3=Cake & tea Foods!B3=2 Names!A1=name Names!A2=Food Names!A3=Cake & tea",
		},
	} {
		m := process(t, "xmltoexcel", tc.opts, message.New(menuXML))
		if m[message.ContentType] != "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet" {
			t.Errorf("%s: Content-Type = %v", name, m[message.ContentType])
		}
		if got := strings.Join(cellsOf(t, m[message.Body]), " "); got != tc.want {
			t.Errorf("%s:\n got %s\nwant %s", name, got, tc.want)
		}
	}
}

func TestXMLToExcelNumbersAreNumeric(t *testing.T) {
	m := process(t, "xmltoexcel", nil, message.New(`<r><x><a>1.5</a><b>007</b><c>1e3</c></x></r>`))
	sheet := zipPart(t, m[message.Body].([]byte), "xl/worksheets/sheet1.xml")
	for _, want := range []string{`<c r="A1"><v>1.5</v></c>`, `<c r="B1" t="inlineStr">`, `<c r="C1" t="inlineStr">`} {
		if !strings.Contains(sheet, want) {
			t.Errorf("sheet = %s, want it to contain %s", sheet, want)
		}
	}
}

func TestXMLToExcelInvalid(t *testing.T) {
	p := mustProcessor(t, stepdef.Action, "xmltoexcel", nil).(stepdef.ActionProcessor)
	if _, err := p.Process(t.Context(), message.New("a,b")); err == nil || !strings.Contains(err.Error(), "body is not XML") {
		t.Errorf("not XML: err = %v", err)
	}
	custom := func(worksheets string) map[string]any {
		return map[string]any{"useCustomWorksheets": true, "worksheets": worksheets}
	}
	for _, want := range map[string]struct {
		o    map[string]any
		want string
	}{
		"xls":        {map[string]any{"excelFormat": "xls"}, "excelFormat"},
		"no list":    {custom(""), "option worksheets: want a JSON list of worksheets"},
		"empty list": {custom("[]"), "needs at least one worksheet"},
		"bad base64": {custom("RAW(!!)"), "RAW(...) is not base64"},
		"bad entry":  {custom(`["nope"]`), "option worksheets: worksheet 1"},
		"duplicate":  {custom(`[{"name":"a"},{"name":"A"}]`), `two worksheets are named "A"`},
		"bad path":   {custom(`[{"name":"a","xPathExpression":"//a[1]"}]`), `worksheet "a": unsupported xpath`},
		"order":      {map[string]any{"orderHeaders": "random"}, "orderHeaders"},
		"not a bool": {map[string]any{"includeHeader": "maybe"}, "includeHeader"},
	} {
		wantInvalid(t, stepdef.Action, "xmltoexcel", want.o, want.want)
	}
	// Unusable worksheets are ignored without useCustomWorksheets, as the example's are.
	mustProcessor(t, stepdef.Action, "xmltoexcel", map[string]any{"worksheets": "nonsense"})
}

func TestXMLToExcelCustomWorksheetNames(t *testing.T) {
	m := process(t, "xmltoexcel", map[string]any{"useCustomWorksheets": true, "worksheets": `[{"name":"a/b"},{"name":""},{"name":"` + strings.Repeat("n", 40) + `"}]`}, message.New(menuXML))
	sheets, err := readXLSX(bytesOf(m[message.Body]))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, s := range sheets {
		names = append(names, s.name)
	}
	if got := strings.Join(names, ","); got != "a_b,Sheet2,"+strings.Repeat("n", 31) {
		t.Errorf("worksheet names = %s", got)
	}
}

// What xmltoexcel writes, exceltoxml reads.
func TestExcelRoundTrip(t *testing.T) {
	m := process(t, "xmltoexcel", map[string]any{"includeHeader": true}, message.New(menuXML))
	got := excelToXML(t, `[{"name":"items","headerRow":true}]`, m[message.Body].([]byte))
	want := `<workbook><items><row><name>Food</name><price>1.5</price><code>007</code></row><row><name>Cake &amp; tea</name><price>2</price><code/></row></items></workbook>`
	if got != want {
		t.Errorf("round trip:\n got %s\nwant %s", got, want)
	}
}
