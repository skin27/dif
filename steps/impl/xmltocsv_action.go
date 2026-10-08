package impl

import (
	"context"
	"fmt"
	"runtime"
	"slices"
	"strconv"
	"strings"

	"dif/message"
	stepdef "dif/steps/definition"
)

// xmlToCSVAction converts an XML body to CSV: every child of the root
// element, or every element that xPathExpression selects, is a record (see
// xmlRecords), and every child of a record a field. The columns are ordered as
// orderHeaders says; a record without a column's field has it empty.
//
// quoteFields quotes all fields, only non-empty ones, all but integers, or
// none; a field that holds the delimiter, a quote or a line break is always
// quoted.
type xmlToCSVAction struct {
	includeHeader, includeIndex bool
	indexName, delimiter, eol   string
	order                       string // orderHeaders
	quote                       string
	path                        xpath // nil: the root's children
}

func newXMLToCSVAction(_ string, p stepdef.Params) (stepdef.Processor, error) {
	eol := map[string]string{"linefeed": "\n", "carriage_return": "\r", "carriage_return_linefeed": "\r\n", "endofline": systemEOL}[p["lineSeparator"].(string)]
	if d := p["delimiter"].(string); d == "" || strings.ContainsAny(d, "\"\r\n") {
		return nil, fmt.Errorf("option delimiter: %q is empty or holds a quote or line break", d)
	}
	var path xpath
	if expr, _ := p["xPathExpression"].(string); strings.TrimSpace(expr) != "" {
		var err error
		if path, err = compileXPath(expr); err != nil {
			return nil, fmt.Errorf("option xPathExpression: %w", err)
		}
	}
	return xmlToCSVAction{
		includeHeader: p["includeHeader"].(bool),
		includeIndex:  p["includeIndexColumn"].(bool),
		indexName:     p["indexColumnName"].(string),
		delimiter:     p["delimiter"].(string),
		eol:           eol,
		order:         p["orderHeaders"].(string),
		quote:         p["quoteFields"].(string),
		path:          path,
	}, nil
}

func (a xmlToCSVAction) Process(_ context.Context, m message.Message) (message.Message, error) {
	root, err := parseXMLTree(bytesOf(m[message.Body]))
	if err != nil {
		return nil, err
	}

	rows, err := xmlRows(root, a.path, bytesOf(m[message.Body]))
	if err != nil {
		return nil, err
	}
	columns, records := xmlRecords(rows, a.order)

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

// systemEOL is the end of a line on the system DIF runs on.
var systemEOL = func() string {
	if runtime.GOOS == "windows" {
		return "\r\n"
	}
	return "\n"
}()

// xmlRows returns the rows of the document data, whose root is root: the
// elements path selects, or the children of the root if path is nil.
func xmlRows(root *xmlElem, path xpath, data []byte) ([]*xmlElem, error) {
	if path == nil {
		return root.children, nil
	}
	nodes, err := path.selectXML(data)
	if err != nil {
		return nil, err
	}
	rows := make([]*xmlElem, 0, len(nodes))
	for _, n := range nodes {
		e, err := parseXMLTree([]byte(n.raw))
		if err != nil {
			return nil, err
		}
		rows = append(rows, e)
	}
	return rows, nil
}

// xmlRecords turns rows into records: every child of a row is a field, whose
// name is its column and whose trimmed text is its value; a row without
// children is one field named after itself. The columns are the field names
// in the order they first appear (order unordered), or alphabetically, from A
// (ordered or ascending) or from Z (descending). A repeated field keeps its
// last value.
func xmlRecords(rows []*xmlElem, order string) (columns []string, records []map[string]string) {
	records = make([]map[string]string, 0, len(rows))
	for _, row := range rows {
		fields := row.children
		if len(fields) == 0 {
			fields = []*xmlElem{row}
		}
		record := make(map[string]string, len(fields))
		for _, f := range fields {
			if !slices.Contains(columns, f.name) {
				columns = append(columns, f.name)
			}
			record[f.name] = strings.TrimSpace(f.value)
		}
		records = append(records, record)
	}
	switch order {
	case "ordered", "ascending":
		slices.Sort(columns)
	case "descending":
		slices.Sort(columns)
		slices.Reverse(columns)
	}
	return columns, records
}

func (a xmlToCSVAction) writeLine(b *strings.Builder, fields []string) {
	for i, f := range fields {
		if i > 0 {
			b.WriteString(a.delimiter)
		}
		needed := strings.Contains(f, a.delimiter) || strings.ContainsAny(f, "\"\r\n")
		if needed || a.quote == "all_fields" || a.quote == "non_empty_fields" && f != "" || a.quote == "non_integer_fields" && !isInteger(f) {
			b.WriteString(`"` + strings.ReplaceAll(f, `"`, `""`) + `"`)
		} else {
			b.WriteString(f)
		}
	}
	b.WriteString(a.eol)
}

// isInteger reports whether s is a whole number: digits with an optional sign.
func isInteger(s string) bool {
	_, err := strconv.ParseInt(s, 10, 64)
	return err == nil
}
