package impl

import (
	"archive/zip"
	"bytes"
	"io"
	"strings"
	"testing"
)

// zipOf makes a zip archive of the files, in map order.
func zipOf(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, content := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		io.WriteString(w, content)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// zipPart returns the content of the file in the zip archive.
func zipPart(t *testing.T, data []byte, name string) string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range zr.File {
		if f.Name == name {
			rc, err := f.Open()
			if err != nil {
				t.Fatal(err)
			}
			defer rc.Close()
			b, _ := io.ReadAll(rc)
			return string(b)
		}
	}
	t.Fatalf("no %s in the archive", name)
	return ""
}

const (
	nsMain = `xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main"`
	nsRel  = `xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"`
)

// menuWorkbook is a workbook as Excel writes it, with shared strings, rich
// text, inline strings, booleans, rows and cells without references, and a
// worksheet reached by an absolute target:
//
//	Sheet1   A        B       C
//	1        Name     Price   Note
//	2        Food     1.5     a<b
//	3        Cake     2
//	4                 TRUE
//	5        x        7
//
//	Data     A
//	1        data
func menuWorkbook(t *testing.T) []byte {
	return zipOf(t, map[string]string{
		"[Content_Types].xml": `<Types/>`,
		"xl/workbook.xml": `<workbook ` + nsMain + ` ` + nsRel + `><sheets>` +
			`<sheet name="Sheet1" sheetId="1" r:id="rId1"/><sheet name="Data" sheetId="2" r:id="rId2"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">` +
			`<Relationship Id="rId1" Type="worksheet" Target="worksheets/sheet1.xml"/>` +
			`<Relationship Id="rId2" Type="worksheet" Target="/xl/worksheets/sheet2.xml"/></Relationships>`,
		"xl/sharedStrings.xml": `<sst ` + nsMain + `><si><t>Name</t></si><si><t>Price</t></si>` +
			`<si><r><t>Fo</t></r><r><rPr/><t>od</t></r></si><si><t>Cake</t></si></sst>`,
		"xl/worksheets/sheet1.xml": `<worksheet ` + nsMain + `><sheetData>` +
			`<row r="1"><c r="A1" t="s"><v>0</v></c><c r="B1" t="s"><v>1</v></c><c r="C1" t="inlineStr"><is><t>Note</t></is></c></row>` +
			`<row r="2"><c r="A2" t="s"><v>2</v></c><c r="B2"><v>1.5</v></c><c r="C2" t="inlineStr"><is><t>a&lt;b</t></is></c></row>` +
			`<row r="3"><c r="A3" t="s"><v>3</v></c><c r="B3" t="n"><v>2</v></c><c r="C3"/></row>` +
			`<row r="4"><c r="B4" t="b"><v>1</v></c></row>` +
			`<row><c t="inlineStr"><is><t>x</t></is></c><c><v>7</v></c></row>` +
			`</sheetData></worksheet>`,
		"xl/worksheets/sheet2.xml": `<worksheet ` + nsMain + `><sheetData><row r="1"><c r="A1" t="str"><v>data</v></c></row></sheetData></worksheet>`,
	})
}

func TestReadXLSX(t *testing.T) {
	sheets, err := readXLSX(menuWorkbook(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(sheets) != 2 || sheets[0].name != "Sheet1" || sheets[1].name != "Data" {
		t.Fatalf("sheets = %v, want Sheet1 and Data", sheets)
	}
	s := sheets[0]
	for ref, want := range map[string]string{
		"A1": "Name", "B1": "Price", "C1": "Note", "A2": "Food", "B2": "1.5", "C2": "a<b",
		"A3": "Cake", "B3": "2", "B4": "TRUE", "A5": "x", "B5": "7",
	} {
		c, _ := parseCellRef(ref)
		if got := s.cells[c]; got != want {
			t.Errorf("Sheet1!%s = %q, want %q", ref, got, want)
		}
	}
	if len(s.cells) != 11 || s.from != (cellRef{1, 1}) || s.to != (cellRef{5, 3}) {
		t.Errorf("%d cells in %v:%v, want 11 in A1:C5 (empty cells are none)", len(s.cells), s.from, s.to)
	}
	if got := sheets[1].cells[cellRef{1, 1}]; got != "data" {
		t.Errorf("Data!A1 = %q, want data", got)
	}
}

func TestReadXLSXInvalid(t *testing.T) {
	good := map[string]string{
		"xl/workbook.xml":            `<workbook ` + nsMain + ` ` + nsRel + `><sheets><sheet name="S" sheetId="1" r:id="rId1"/></sheets></workbook>`,
		"xl/_rels/workbook.xml.rels": `<Relationships><Relationship Id="rId1" Target="worksheets/sheet1.xml"/></Relationships>`,
		"xl/worksheets/sheet1.xml":   `<worksheet ` + nsMain + `><sheetData><row r="1"><c r="A1" t="inlineStr"><is><t>x</t></is></c></row></sheetData></worksheet>`,
	}
	with := func(name, content string) []byte {
		files := map[string]string{}
		for k, v := range good {
			files[k] = v
		}
		if content == "" {
			delete(files, name)
		} else {
			files[name] = content
		}
		return zipOf(t, files)
	}
	if _, err := readXLSX(with("xl/sharedStrings.xml", "")); err != nil {
		t.Fatalf("a workbook without shared strings: %v", err)
	}

	for name, tc := range map[string]struct {
		data []byte
		want string
	}{
		"not a zip":         {[]byte("a,b,c"), "body is not an xlsx workbook"},
		"empty":             {nil, "body is not an xlsx workbook"},
		"a zip, not xlsx":   {zipOf(t, map[string]string{"a.txt": "x"}), "body is not an xlsx workbook: no xl/workbook.xml"},
		"no relationships":  {with("xl/_rels/workbook.xml.rels", ""), "no xl/_rels/workbook.xml.rels"},
		"no worksheet part": {with("xl/worksheets/sheet1.xml", ""), "no xl/worksheets/sheet1.xml"},
		"broken workbook":   {with("xl/workbook.xml", "<workbook>"), "xl/workbook.xml"},
		"unknown relation":  {with("xl/_rels/workbook.xml.rels", `<Relationships/>`), `worksheet "S" has no relationship "rId1"`},
		"bad shared index":  {with("xl/worksheets/sheet1.xml", `<worksheet><sheetData><row><c t="s"><v>9</v></c></row></sheetData></worksheet>`), `no shared string "9"`},
		"bad cell ref":      {with("xl/worksheets/sheet1.xml", `<worksheet><sheetData><row><c r="1A" t="str"><v>x</v></c></row></sheetData></worksheet>`), `"1A" is not a cell`},
		"too many rows":     {with("xl/worksheets/sheet1.xml", `<worksheet><sheetData><row r="1048577"><c t="str"><v>x</v></c></row></sheetData></worksheet>`), "more than 1048576 rows"},
	} {
		if _, err := readXLSX(tc.data); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want containing %q", name, err, tc.want)
		}
	}
}

func TestCellReferences(t *testing.T) {
	for ref, want := range map[string]cellRef{"A1": {1, 1}, "b3": {3, 2}, "$C$10": {10, 3}, "Z1": {1, 26}, "AA1": {1, 27}, "AZ2": {2, 52}, "XFD1048576": {1048576, 16384}} {
		got, err := parseCellRef(ref)
		if err != nil || got != want {
			t.Errorf("parseCellRef(%q) = %v, %v, want %v", ref, got, err, want)
		}
		if err == nil && !strings.EqualFold(strings.ReplaceAll(ref, "$", ""), got.String()) {
			t.Errorf("%v.String() = %s, want %s", got, got, ref)
		}
	}
	for _, ref := range []string{"", "A", "1", "A0", "1A", "A-1", "XFE1", "A1048577", "AAAAAAAAAAAAAAAAAAAAAAAAA1", "A1B"} {
		if _, err := parseCellRef(ref); err == nil {
			t.Errorf("parseCellRef(%q): want an error", ref)
		}
	}

	for in, want := range map[string][2]cellRef{"A2:C4": {{2, 1}, {4, 3}}, "C4:A2": {{2, 1}, {4, 3}}, "B3": {{3, 2}, {3, 2}}, "a1:b2": {{1, 1}, {2, 2}}} {
		if from, to, err := parseCellRange(in); err != nil || from != want[0] || to != want[1] {
			t.Errorf("parseCellRange(%q) = %v, %v, %v, want %v", in, from, to, err, want)
		}
	}
	for _, in := range []string{"A:C", "A1:", ":B2", "A1:B2:C3", "x"} {
		if _, _, err := parseCellRange(in); err == nil {
			t.Errorf("parseCellRange(%q): want an error", in)
		}
	}
}

func TestWriteXLSX(t *testing.T) {
	data, err := writeXLSX([]outSheet{
		{"Menu", [][]string{{"name", "price", "code"}, {"a<b & \x01c", "1.5", "007"}, {"", "-3", "12345678901234567890"}}},
		{"Empty", nil},
	})
	if err != nil {
		t.Fatal(err)
	}

	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil || zr.File[0].Name != "[Content_Types].xml" {
		t.Fatalf("archive: %v, first part %v, want [Content_Types].xml first", err, zr.File[0].Name)
	}
	var sheet1 string
	for _, f := range zr.File {
		if f.Name == "xl/worksheets/sheet1.xml" {
			rc, _ := f.Open()
			b, _ := io.ReadAll(rc)
			rc.Close()
			sheet1 = string(b)
		}
	}
	for _, want := range []string{
		`<c r="A1" t="inlineStr"><is><t xml:space="preserve">name</t></is></c>`,
		`<c r="A2" t="inlineStr"><is><t xml:space="preserve">a&lt;b &amp; c</t></is></c>`, // control character dropped
		`<c r="B2"><v>1.5</v></c>`,
		`<c r="C2" t="inlineStr"><is><t xml:space="preserve">007</t></is></c>`, // a leading zero is text
		`<row r="3"><c r="B3"><v>-3</v></c><c r="C3" t="inlineStr">`,           // no cell for the empty value; 20 digits are text
	} {
		if !strings.Contains(sheet1, want) {
			t.Errorf("sheet1 = %s\nwant it to contain %s", sheet1, want)
		}
	}

	// What is written reads back, as the text it holds.
	sheets, err := readXLSX(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(sheets) != 2 || sheets[0].name != "Menu" || sheets[1].name != "Empty" || len(sheets[1].cells) != 0 {
		t.Fatalf("sheets = %v", sheets)
	}
	for ref, want := range map[string]string{"A2": "a<b & c", "B2": "1.5", "C2": "007", "B3": "-3", "C3": "12345678901234567890"} {
		c, _ := parseCellRef(ref)
		if got := sheets[0].cells[c]; got != want {
			t.Errorf("Menu!%s = %q, want %q", ref, got, want)
		}
	}

	if _, err := writeXLSX([]outSheet{{"S", [][]string{make([]string, maxXLSXCols+1)}}}); err == nil || !strings.Contains(err.Error(), "more than 16384 columns") {
		t.Errorf("too many columns: err = %v", err)
	}
}

func TestXLSXSheetName(t *testing.T) {
	for in, want := range map[string]string{
		"Sheet1": "Sheet1", `a/b\c:d*e?f[g]`: "a_b_c_d_e_f_g_", "": "",
		"abcdefghijklmnopqrstuvwxyz0123456789": "abcdefghijklmnopqrstuvwxyz01234",
		"ééééééééééééééééééééééééééééééééé":    strings.Repeat("é", 31),
	} {
		if got := xlsxSheetName(in); got != want {
			t.Errorf("xlsxSheetName(%q) = %q, want %q", in, got, want)
		}
	}
}
