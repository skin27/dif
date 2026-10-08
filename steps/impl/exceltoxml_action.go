package impl

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"dif/message"
	stepdef "dif/steps/definition"
)

// excelToXMLAction converts an xlsx workbook into XML: a workbook element
// holding, for every rule, an element named after the rule's name (else its
// worksheet, else sheetN) with a row element per row of the rule's cells,
// each holding an element per cell:
//
//	<workbook><node1><row><field1>a</field1><field2>1</field2></row></node1></workbook>
//
// A rule takes the cells of cellRange (A2:C4; the whole used range if empty)
// of the worksheet (the first if empty). With transpose rows and columns
// swap. With headerRow the first row names the fields (invalid characters
// become _) and is not a row itself. Fields without a name are field1,
// field2, … Empty cells are empty elements; discardEmpty leaves them out,
// and rows with no value at all.
type excelToXMLAction struct{ rules []excelRule }

type excelRule struct {
	Name         string    `json:"name"`
	Worksheet    string    `json:"worksheet"`
	CellRange    string    `json:"cellRange"`
	Transpose    looseBool `json:"transpose"`
	HeaderRow    looseBool `json:"headerRow"`
	DiscardEmpty looseBool `json:"discardEmpty"`

	from, to cellRef // the cellRange, if it has one
}

func newExcelToXMLAction(_ string, p stepdef.Params) (stepdef.Processor, error) {
	var rules []excelRule
	if err := json.Unmarshal([]byte(p["rules"].(string)), &rules); err != nil {
		return nil, fmt.Errorf("option rules: want a JSON list of rules: %w", err)
	}
	if len(rules) == 0 {
		return nil, fmt.Errorf("option rules: no rules")
	}
	for i := range rules {
		r := &rules[i]
		if strings.TrimSpace(r.CellRange) != "" {
			var err error
			if r.from, r.to, err = parseCellRange(r.CellRange); err != nil {
				return nil, fmt.Errorf("option rules: rule %d: cellRange: %w", i+1, err)
			}
		}
		switch {
		case r.Name == "" && r.Worksheet != "":
			r.Name = sanitizeXMLName(r.Worksheet)
		case r.Name == "":
			r.Name = "sheet" + strconv.Itoa(i+1)
		default:
			r.Name = sanitizeXMLName(r.Name)
		}
	}
	return excelToXMLAction{rules}, nil
}

func (a excelToXMLAction) Process(_ context.Context, m message.Message) (message.Message, error) {
	sheets, err := readXLSX(bytesOf(m[message.Body]))
	if err != nil {
		return nil, err
	}
	if len(sheets) == 0 {
		return nil, fmt.Errorf("workbook has no worksheets")
	}

	var b strings.Builder
	b.WriteString("<workbook>")
	for i, r := range a.rules {
		s := sheets[0]
		if r.Worksheet != "" {
			s = nil
			for _, c := range sheets {
				if c.name == r.Worksheet {
					s = c
				}
			}
			if s == nil {
				return nil, fmt.Errorf("rule %d: no worksheet %q", i+1, r.Worksheet)
			}
		}
		if err := r.write(&b, s); err != nil {
			return nil, fmt.Errorf("rule %d: %w", i+1, err)
		}
	}
	b.WriteString("</workbook>")
	m[message.Body] = b.String()
	m[message.ContentType] = "application/xml"
	return m, nil
}

// write writes the rule's element for the cells of s.
func (r excelRule) write(b *strings.Builder, s *sheet) error {
	from, to := r.from, r.to
	if to.row == 0 {
		from, to = s.from, s.to
	}
	b.WriteString("<" + r.Name + ">")
	defer b.WriteString("</" + r.Name + ">")
	if to.row == 0 { // an empty worksheet
		return nil
	}
	rows, cols := to.row-from.row+1, to.col-from.col+1
	if rows*cols > maxXLSXCells {
		return fmt.Errorf("%d cells is more than the %d a rule can convert", rows*cols, maxXLSXCells)
	}

	// at returns the cell in row i, column j of the table the rule makes.
	at := func(i, j int) string {
		if r.Transpose {
			return s.cells[cellRef{from.row + j, from.col + i}]
		}
		return s.cells[cellRef{from.row + i, from.col + j}]
	}
	nrows, ncols := rows, cols
	if r.Transpose {
		nrows, ncols = cols, rows
	}

	names := make([]string, ncols)
	first := 0
	if r.HeaderRow && nrows > 0 {
		first = 1
	}
	for j := range names {
		names[j] = "field" + strconv.Itoa(j+1)
		if h := strings.TrimSpace(at(0, j)); first == 1 && h != "" {
			names[j] = sanitizeXMLName(h)
		}
	}

	for i := first; i < nrows; i++ {
		var row strings.Builder
		for j := 0; j < ncols; j++ {
			switch v := at(i, j); {
			case v != "":
				row.WriteString("<" + names[j] + ">" + xmlTextEscaper.Replace(v) + "</" + names[j] + ">")
			case !bool(r.DiscardEmpty):
				row.WriteString("<" + names[j] + "/>")
			}
		}
		if row.Len() > 0 {
			b.WriteString("<row>" + row.String() + "</row>")
		}
	}
	return nil
}
