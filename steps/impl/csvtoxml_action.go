package impl

import (
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"

	"dif/message"
	stepdef "dif/steps/definition"
)

// csvToXMLAction converts a CSV body to XML: a rows element holding a row
// element per record, which holds an element per field. With useHeader the
// first record names the fields (characters an XML name cannot hold become
// _); otherwise, and for fields beyond the header, they are field1, field2, …
//
// The encoding option only sets the encoding in the XML declaration: the
// body stays text, which the encoder step can convert.
type csvToXMLAction struct {
	delimiter rune
	useHeader bool
	encoding  string
}

func newCSVToXMLAction(_ string, p stepdef.Params) (stepdef.Processor, error) {
	d := p["delimiter"].(string)
	r, size := utf8.DecodeRuneInString(d)
	if size == 0 || size != len(d) || r == '"' || r == '\r' || r == '\n' || r == utf8.RuneError {
		return nil, fmt.Errorf("option delimiter: %q is not one character other than a quote or line break", d)
	}
	enc := p["encoding"].(string)
	if enc == "" || strings.Trim(enc, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789._-") != "" {
		return nil, fmt.Errorf("option encoding: %q is not an encoding name", enc)
	}
	return csvToXMLAction{r, p["useHeader"].(bool) || p["useHeaders"].(bool), enc}, nil
}

func (a csvToXMLAction) Process(_ context.Context, m message.Message) (message.Message, error) {
	r := csv.NewReader(strings.NewReader(text(m[message.Body])))
	r.Comma = a.delimiter
	r.FieldsPerRecord = -1 // records may differ in length

	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="` + a.encoding + `"?>` + "\n<rows>")
	var header []string
	for first := true; ; first = false {
		record, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("body is not CSV: %w", err)
		}
		if first && a.useHeader {
			for _, h := range record {
				header = append(header, sanitizeXMLName(strings.TrimSpace(h)))
			}
			continue
		}
		b.WriteString("<row>")
		for i, field := range record {
			name := "field" + strconv.Itoa(i+1)
			if i < len(header) {
				name = header[i]
			}
			if field == "" {
				b.WriteString("<" + name + "/>")
			} else {
				b.WriteString("<" + name + ">" + xmlTextEscaper.Replace(field) + "</" + name + ">")
			}
		}
		b.WriteString("</row>")
	}
	b.WriteString("</rows>")
	m[message.Body] = b.String()
	m[message.ContentType] = "application/xml"
	return m, nil
}
