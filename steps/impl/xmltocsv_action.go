package impl

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"dif/message"
	stepdef "dif/steps/definition"
)

// xmlToCSVAction converts an XML body to CSV: every child of the root
// element is a record, and every child of a record a field, whose name is
// its column and whose trimmed text is its value. A record without children
// is one field named after itself. The columns are the field names in the
// order they first appear (orderHeaders unordered) or alphabetically
// (ordered); a record without a column's field has it empty.
//
// quoteFields quotes all fields, only non-empty ones, or none; a field that
// holds the delimiter, a quote or a line break is always quoted.
type xmlToCSVAction struct {
	includeHeader, includeIndex bool
	indexName, delimiter, eol   string
	ordered                     bool
	quote                       string
}

func newXMLToCSVAction(_ string, p stepdef.Params) (stepdef.Processor, error) {
	eol := map[string]string{"linefeed": "\n", "carriage_return": "\r", "carriage_return_linefeed": "\r\n"}[p["lineSeparator"].(string)]
	if d := p["delimiter"].(string); d == "" || strings.ContainsAny(d, "\"\r\n") {
		return nil, fmt.Errorf("option delimiter: %q is empty or holds a quote or line break", d)
	}
	return xmlToCSVAction{
		includeHeader: p["includeHeader"].(bool),
		includeIndex:  p["includeIndexColumn"].(bool),
		indexName:     p["indexColumnName"].(string),
		delimiter:     p["delimiter"].(string),
		eol:           eol,
		ordered:       p["orderHeaders"] == "ordered",
		quote:         p["quoteFields"].(string),
	}, nil
}

func (a xmlToCSVAction) Process(_ context.Context, m message.Message) (message.Message, error) {
	root, err := parseXMLTree(bytesOf(m[message.Body]))
	if err != nil {
		return nil, err
	}

	var columns []string
	records := make([]map[string]string, 0, len(root.children))
	for _, row := range root.children {
		fields := row.children
		if len(fields) == 0 {
			fields = []*xmlElem{row}
		}
		record := make(map[string]string, len(fields))
		for _, f := range fields {
			if !slices.Contains(columns, f.name) {
				columns = append(columns, f.name)
			}
			record[f.name] = strings.TrimSpace(f.value) // a repeated field keeps its last value
		}
		records = append(records, record)
	}
	if a.ordered {
		slices.Sort(columns)
	}

	var b strings.Builder
	if a.includeHeader {
		line := columns
		if a.includeIndex {
			line = append([]string{a.indexName}, columns...)
		}
		a.writeLine(&b, line)
	}
	line := make([]string, 0, len(columns)+1)
	for i, record := range records {
		line = line[:0]
		if a.includeIndex {
			line = append(line, strconv.Itoa(i+1))
		}
		for _, c := range columns {
			line = append(line, record[c])
		}
		a.writeLine(&b, line)
	}
	m[message.Body] = b.String()
	m[message.ContentType] = "text/csv"
	return m, nil
}

func (a xmlToCSVAction) writeLine(b *strings.Builder, fields []string) {
	for i, f := range fields {
		if i > 0 {
			b.WriteString(a.delimiter)
		}
		needed := strings.Contains(f, a.delimiter) || strings.ContainsAny(f, "\"\r\n")
		if needed || a.quote == "all_fields" || a.quote == "non_empty_fields" && f != "" {
			b.WriteString(`"` + strings.ReplaceAll(f, `"`, `""`) + `"`)
		} else {
			b.WriteString(f)
		}
	}
	b.WriteString(a.eol)
}
