package impl

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"path"
	"regexp"
	"strconv"
	"strings"
)

// The excel converters read and write the xlsx format (Office Open XML): a
// zip of XML parts. Only values are handled: strings and numbers, no styles,
// formulas (their last value is read), dates (read as the serial numbers
// Excel stores) or the older binary xls format.

const (
	maxXLSXRows = 1048576
	maxXLSXCols = 16384
	// maxXLSXCells bounds the cells a conversion turns into XML, whatever
	// range a rule asks for.
	maxXLSXCells = 1000000
)

// cellRef is the position of a cell, from 1.
type cellRef struct{ row, col int }

func (c cellRef) String() string { return colName(c.col) + strconv.Itoa(c.row) }

// colName returns the column letters of col: 1 is A, 27 is AA.
func colName(col int) string {
	var s []byte
	for ; col > 0; col = (col - 1) / 26 {
		s = append([]byte{byte('A' + (col-1)%26)}, s...)
	}
	return string(s)
}

// parseCellRef parses a reference such as B3 or $B$3.
func parseCellRef(s string) (cellRef, error) {
	orig := s
	s = strings.ReplaceAll(strings.ToUpper(strings.TrimSpace(s)), "$", "")
	i := 0
	var c cellRef
	for ; i < len(s) && s[i] >= 'A' && s[i] <= 'Z'; i++ {
		if c.col = c.col*26 + int(s[i]-'A') + 1; c.col > maxXLSXCols {
			break
		}
	}
	row, err := strconv.Atoi(s[i:])
	if i == 0 || err != nil || c.col > maxXLSXCols || row < 1 || row > maxXLSXRows {
		return cellRef{}, fmt.Errorf("%q is not a cell such as B3", orig)
	}
	c.row = row
	return c, nil
}

// parseCellRange parses a range such as A2:C4, or one cell. The result runs
// from its top left to its bottom right cell.
func parseCellRange(s string) (from, to cellRef, err error) {
	a, b, isRange := strings.Cut(s, ":")
	if !isRange {
		b = a
	}
	var errA, errB error
	from, errA = parseCellRef(a)
	to, errB = parseCellRef(b)
	if errA != nil || errB != nil {
		return cellRef{}, cellRef{}, fmt.Errorf("%q is not a cell range such as A2:C4", s)
	}
	from, to = cellRef{min(from.row, to.row), min(from.col, to.col)}, cellRef{max(from.row, to.row), max(from.col, to.col)}
	return from, to, nil
}

// sheet is a worksheet read from a workbook: its non-empty cells as text,
// and the bounds of the cells.
type sheet struct {
	name     string
	cells    map[cellRef]string
	from, to cellRef // zero for a sheet with no cells
}

func (s *sheet) set(c cellRef, v string) {
	if v == "" {
		return
	}
	s.cells[c] = v
	if s.to.row == 0 {
		s.from, s.to = c, c
		return
	}
	s.from = cellRef{min(s.from.row, c.row), min(s.from.col, c.col)}
	s.to = cellRef{max(s.to.row, c.row), max(s.to.col, c.col)}
}

type xlsxText struct {
	T []string `xml:"t"`
	R []struct {
		T string `xml:"t"`
	} `xml:"r"`
}

func (t xlsxText) String() string {
	s := strings.Join(t.T, "")
	for _, r := range t.R {
		s += r.T
	}
	return s
}

// readXLSX reads the worksheets of an xlsx workbook, in workbook order.
func readXLSX(data []byte) ([]*sheet, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("body is not an xlsx workbook: %w", err)
	}
	files := map[string]*zip.File{}
	for _, f := range zr.File {
		files[f.Name] = f
	}
	// part unmarshals an XML part of the workbook into v; a missing part is
	// an error only if required.
	part := func(name string, v any, required bool) error {
		f := files[name]
		if f == nil {
			if required {
				return fmt.Errorf("body is not an xlsx workbook: no %s", name)
			}
			return nil
		}
		rc, err := f.Open()
		if err != nil {
			return fmt.Errorf("xlsx %s: %w", name, err)
		}
		defer rc.Close()
		content, err := io.ReadAll(io.LimitReader(rc, maxBodySize+1))
		if err != nil || len(content) > maxBodySize {
			return fmt.Errorf("xlsx %s: cannot be read within %d bytes", name, maxBodySize)
		}
		if err := xml.Unmarshal(content, v); err != nil {
			return fmt.Errorf("xlsx %s: %w", name, err)
		}
		return nil
	}

	var wb struct {
		Sheets []struct {
			Name string `xml:"name,attr"`
			RID  string `xml:"id,attr"` // r:id
		} `xml:"sheets>sheet"`
	}
	var rels struct {
		Rels []struct {
			ID     string `xml:"Id,attr"`
			Target string `xml:"Target,attr"`
		} `xml:"Relationship"`
	}
	var sst struct {
		Strings []xlsxText `xml:"si"`
	}
	if err := part("xl/workbook.xml", &wb, true); err != nil {
		return nil, err
	}
	if err := part("xl/_rels/workbook.xml.rels", &rels, true); err != nil {
		return nil, err
	}
	if err := part("xl/sharedStrings.xml", &sst, false); err != nil {
		return nil, err
	}
	targets := map[string]string{}
	for _, r := range rels.Rels {
		if strings.HasPrefix(r.Target, "/") {
			targets[r.ID] = path.Clean(r.Target[1:])
		} else {
			targets[r.ID] = path.Join("xl", r.Target)
		}
	}

	var sheets []*sheet
	for _, ws := range wb.Sheets {
		var data struct {
			Rows []struct {
				R     int `xml:"r,attr"`
				Cells []struct {
					R  string   `xml:"r,attr"`
					T  string   `xml:"t,attr"`
					V  string   `xml:"v"`
					IS xlsxText `xml:"is"`
				} `xml:"c"`
			} `xml:"sheetData>row"`
		}
		target, ok := targets[ws.RID]
		if !ok {
			return nil, fmt.Errorf("xlsx: worksheet %q has no relationship %q", ws.Name, ws.RID)
		}
		if err := part(target, &data, true); err != nil {
			return nil, err
		}

		s := &sheet{name: ws.Name, cells: map[cellRef]string{}}
		row := 0
		for _, r := range data.Rows {
			if row = max(row+1, r.R); row > maxXLSXRows { // r is optional: rows follow each other
				return nil, fmt.Errorf("xlsx worksheet %q: more than %d rows", ws.Name, maxXLSXRows)
			}
			col := 0
			for _, c := range r.Cells {
				col++
				if c.R != "" {
					ref, err := parseCellRef(c.R)
					if err != nil {
						return nil, fmt.Errorf("xlsx worksheet %q: %w", ws.Name, err)
					}
					col = ref.col
				}
				if col > maxXLSXCols {
					return nil, fmt.Errorf("xlsx worksheet %q: more than %d columns", ws.Name, maxXLSXCols)
				}
				var v string
				switch c.T {
				case "s":
					i, err := strconv.Atoi(strings.TrimSpace(c.V))
					if err != nil || i < 0 || i >= len(sst.Strings) {
						return nil, fmt.Errorf("xlsx worksheet %q: cell %s: no shared string %q", ws.Name, cellRef{row, col}, c.V)
					}
					v = sst.Strings[i].String()
				case "inlineStr":
					v = c.IS.String()
				case "b":
					v = map[string]string{"1": "TRUE", "0": "FALSE"}[strings.TrimSpace(c.V)]
				default: // numbers, formula strings, errors, dates
					v = c.V
				}
				s.set(cellRef{row, col}, v)
			}
		}
		sheets = append(sheets, s)
	}
	return sheets, nil
}

// outSheet is a worksheet to write: rows of cell values.
type outSheet struct {
	name string
	rows [][]string
}

var xlsxNumber = regexp.MustCompile(`^-?(0|[1-9][0-9]{0,14})(\.[0-9]{1,14})?$`)

// xlsxSheetName makes s a valid worksheet name: at most 31 characters, none
// of []:*?/\.
func xlsxSheetName(s string) string {
	s = strings.Map(func(r rune) rune {
		if strings.ContainsRune(`[]:*?/\`, r) {
			return '_'
		}
		return r
	}, s)
	if r := []rune(s); len(r) > 31 {
		s = string(r[:31])
	}
	return s
}

// xmlChars returns s without the characters XML 1.0 cannot hold, escaped as text.
func xmlChars(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 && r != '\t' && r != '\n' && r != '\r' || r == 0xFFFE || r == 0xFFFF || r >= 0xD800 && r <= 0xDFFF {
			return -1
		}
		return r
	}, s)
	return xmlTextEscaper.Replace(s)
}

// writeXLSX writes a workbook of the sheets. Numbers are numeric cells, all
// else text; an empty value is no cell.
func writeXLSX(sheets []outSheet) ([]byte, error) {
	const (
		header  = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` + "\n"
		mainNS  = "http://schemas.openxmlformats.org/spreadsheetml/2006/main"
		relsNS  = "http://schemas.openxmlformats.org/package/2006/relationships"
		docRel  = "http://schemas.openxmlformats.org/officeDocument/2006/relationships"
		sheetCT = "application/vnd.openxmlformats-officedocument.spreadsheetml"
	)
	var contentTypes, workbook, workbookRels strings.Builder
	contentTypes.WriteString(header + `<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">` +
		`<Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>` +
		`<Default Extension="xml" ContentType="application/xml"/>` +
		`<Override PartName="/xl/workbook.xml" ContentType="` + sheetCT + `.sheet.main+xml"/>`)
	workbook.WriteString(header + `<workbook xmlns="` + mainNS + `" xmlns:r="` + docRel + `"><sheets>`)
	workbookRels.WriteString(header + `<Relationships xmlns="` + relsNS + `">`)

	type part struct{ name, content string }
	var sheetParts []part

	for i, s := range sheets {
		n := strconv.Itoa(i + 1)
		contentTypes.WriteString(`<Override PartName="/xl/worksheets/sheet` + n + `.xml" ContentType="` + sheetCT + `.worksheet+xml"/>`)
		workbook.WriteString(`<sheet name="` + xmlAttrEscaper.Replace(s.name) + `" sheetId="` + n + `" r:id="rId` + n + `"/>`)
		workbookRels.WriteString(`<Relationship Id="rId` + n + `" Type="` + docRel + `/worksheet" Target="worksheets/sheet` + n + `.xml"/>`)

		if len(s.rows) > maxXLSXRows {
			return nil, fmt.Errorf("worksheet %q: more than %d rows", s.name, maxXLSXRows)
		}
		var b strings.Builder
		b.WriteString(header + `<worksheet xmlns="` + mainNS + `"><sheetData>`)
		for r, row := range s.rows {
			if len(row) > maxXLSXCols {
				return nil, fmt.Errorf("worksheet %q: more than %d columns", s.name, maxXLSXCols)
			}
			b.WriteString(`<row r="` + strconv.Itoa(r+1) + `">`)
			for c, v := range row {
				switch ref := (cellRef{r + 1, c + 1}).String(); {
				case v == "":
				case xlsxNumber.MatchString(v):
					b.WriteString(`<c r="` + ref + `"><v>` + v + `</v></c>`)
				default:
					b.WriteString(`<c r="` + ref + `" t="inlineStr"><is><t xml:space="preserve">` + xmlChars(v) + `</t></is></c>`)
				}
			}
			b.WriteString(`</row>`)
		}
		b.WriteString(`</sheetData></worksheet>`)
		sheetParts = append(sheetParts, part{"xl/worksheets/sheet" + n + ".xml", b.String()})
	}
	contentTypes.WriteString(`</Types>`)
	workbook.WriteString(`</sheets></workbook>`)
	workbookRels.WriteString(`</Relationships>`)

	parts := append([]part{
		{"[Content_Types].xml", contentTypes.String()}, // first, as readers expect
		{"_rels/.rels", header + `<Relationships xmlns="` + relsNS + `"><Relationship Id="rId1" Type="` + docRel + `/officeDocument" Target="xl/workbook.xml"/></Relationships>`},
		{"xl/workbook.xml", workbook.String()},
		{"xl/_rels/workbook.xml.rels", workbookRels.String()},
	}, sheetParts...)

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, p := range parts {
		w, err := zw.Create(p.name)
		if err == nil {
			_, err = io.WriteString(w, p.content)
		}
		if err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
