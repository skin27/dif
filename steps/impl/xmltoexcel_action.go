package impl

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"dif/message"
	stepdef "dif/steps/definition"
)

// xmlToExcelAction converts an XML body into an xlsx workbook with the
// mapping xmltocsv uses for CSV: every child of the root element is a row,
// every child of a row a cell, whose column is the cell's element name.
// Numbers are numeric cells, everything else text.
//
// With useCustomWorksheets the workbook has a worksheet per entry of
// worksheets, a JSON list of {"name": "Sheet", "xPathExpression": "/a/b"},
// whose rows are the elements the expression selects; an empty expression
// selects the root's children. The list may be given as RAW(<base64>), and its
// entries as JSON text, as DIL exports it.
type xmlToExcelAction struct {
	includeHeader, includeIndex bool
	indexName                   string
	ordered                     bool
	custom                      []excelWorksheet
}

type excelWorksheet struct {
	Name  string `json:"name"`
	XPath string `json:"xPathExpression"`

	path xpath // nil: the root's children
}

func newXMLToExcelAction(_ string, p stepdef.Params) (stepdef.Processor, error) {
	a := xmlToExcelAction{
		includeHeader: p["includeHeader"].(bool),
		includeIndex:  p["includeIndexColumn"].(bool),
		indexName:     p["indexColumnName"].(string),
		ordered:       p["orderHeaders"] == "ordered",
	}
	if !p["useCustomWorksheets"].(bool) {
		return a, nil
	}

	list, err := worksheetList(p["worksheets"].(string))
	if err != nil {
		return nil, fmt.Errorf("option worksheets: %w", err)
	}
	if len(list) == 0 {
		return nil, fmt.Errorf("option worksheets: useCustomWorksheets needs at least one worksheet")
	}
	seen := map[string]bool{}
	for i := range list {
		w := &list[i]
		if w.Name = xlsxSheetName(strings.TrimSpace(w.Name)); w.Name == "" {
			w.Name = "Sheet" + strconv.Itoa(i+1)
		}
		if seen[strings.ToLower(w.Name)] {
			return nil, fmt.Errorf("option worksheets: two worksheets are named %q", w.Name)
		}
		seen[strings.ToLower(w.Name)] = true
		if strings.TrimSpace(w.XPath) != "" {
			if w.path, err = compileXPath(w.XPath); err != nil {
				return nil, fmt.Errorf("option worksheets: worksheet %q: %w", w.Name, err)
			}
		}
	}
	a.custom = list
	return a, nil
}

// worksheetList parses the worksheets option: JSON, optionally in RAW(<base64>),
// of objects or of strings holding an object.
func worksheetList(s string) ([]excelWorksheet, error) {
	s = strings.TrimSpace(s)
	if enc, ok := strings.CutSuffix(strings.TrimPrefix(s, "RAW("), ")"); ok && strings.HasPrefix(s, "RAW(") {
		raw, err := base64.StdEncoding.DecodeString(enc)
		if err != nil {
			return nil, fmt.Errorf("RAW(...) is not base64: %w", err)
		}
		s = string(raw)
	}
	var items []json.RawMessage
	if err := json.Unmarshal([]byte(s), &items); err != nil {
		return nil, fmt.Errorf("want a JSON list of worksheets: %w", err)
	}
	list := make([]excelWorksheet, len(items))
	for i, item := range items {
		var text string
		if json.Unmarshal(item, &text) == nil {
			item = json.RawMessage(text)
		}
		if err := json.Unmarshal(item, &list[i]); err != nil {
			return nil, fmt.Errorf("worksheet %d: %w", i+1, err)
		}
	}
	return list, nil
}

func (a xmlToExcelAction) Process(_ context.Context, m message.Message) (message.Message, error) {
	body := bytesOf(m[message.Body])
	root, err := parseXMLTree(body)
	if err != nil {
		return nil, err
	}

	sheets := []outSheet{{name: "Sheet1", rows: a.table(root.children)}}
	if a.custom != nil {
		sheets = sheets[:0]
		for _, w := range a.custom {
			rows := root.children
			if w.path != nil {
				nodes, err := w.path.selectXML(body)
				if err != nil {
					return nil, err
				}
				rows = nil
				for _, n := range nodes {
					e, err := parseXMLTree([]byte(n.raw))
					if err != nil {
						return nil, err
					}
					rows = append(rows, e)
				}
			}
			sheets = append(sheets, outSheet{w.Name, a.table(rows)})
		}
	}

	out, err := writeXLSX(sheets)
	if err != nil {
		return nil, err
	}
	m[message.Body] = out
	m[message.ContentType] = "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	return m, nil
}

// table returns the cells of a worksheet whose rows are the elements rows.
func (a xmlToExcelAction) table(rows []*xmlElem) [][]string {
	columns, records := xmlRecords(rows, a.ordered)
	var table [][]string
	if a.includeHeader {
		line := columns
		if a.includeIndex {
			line = append([]string{a.indexName}, columns...)
		}
		table = append(table, line)
	}
	for i, record := range records {
		line := make([]string, 0, len(columns)+1)
		if a.includeIndex {
			line = append(line, strconv.Itoa(i+1))
		}
		for _, c := range columns {
			line = append(line, record[c])
		}
		table = append(table, line)
	}
	return table
}
