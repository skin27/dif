package mapping

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"strings"
	"unicode/utf8"
)

// Format is a neutral resolved contract. Schema contains snapshotted source.
type Format struct {
	Name      string                     `json:"name"`
	MediaType string                     `json:"mediaType"`
	SchemaRef string                     `json:"schemaRef,omitempty"`
	Reader    map[string]json.RawMessage `json:"reader,omitempty"`
	Writer    map[string]json.RawMessage `json:"writer,omitempty"`
	Schema    string                     `json:"-"`
	Origin    string                     `json:"-"`
}
type settings struct {
	delimiter rune
	header    bool
	columns   []string
	crlf      bool
}
type contract struct {
	name, media    string
	reader, writer settings
	schema         *schema
}

func media(s string) (string, error) {
	m, p, e := mime.ParseMediaType(s)
	if e != nil {
		return "", fmt.Errorf("invalid media type")
	}
	for k, v := range p {
		if k != "charset" || !strings.EqualFold(v, "utf-8") {
			return "", fmt.Errorf("unsupported media type parameter or encoding")
		}
	}
	switch m {
	case "application/json", "application/xml", "text/csv":
		return m, nil
	}
	return "", fmt.Errorf("unsupported mapping media type")
}
func compileFormat(f Format, l Limits) (contract, error) {
	c := contract{name: f.Name}
	var e error
	c.media, e = media(f.MediaType)
	if e != nil {
		return c, e
	}
	if f.SchemaRef != "" && f.Schema == "" {
		return c, fmt.Errorf("missing schema resource")
	}
	if f.Schema != "" && c.media != "application/json" {
		return c, fmt.Errorf("schemas currently require JSON format")
	}
	c.schema, e = compileSchema(f.Schema, l)
	if e != nil {
		return c, e
	}
	c.reader, e = compileSettings(f.Reader, c.media, false)
	if e != nil {
		return c, e
	}
	c.writer, e = compileSettings(f.Writer, c.media, true)
	return c, e
}
func compileSettings(raw map[string]json.RawMessage, media string, writer bool) (settings, error) {
	s := settings{delimiter: ',', header: true}
	for k, v := range raw {
		if media != "text/csv" {
			return s, fmt.Errorf("reader/writer settings are only supported for CSV")
		}
		switch k {
		case "delimiter":
			var text string
			if json.Unmarshal(v, &text) != nil || utf8.RuneCountInString(text) != 1 {
				return s, fmt.Errorf("delimiter must be one character")
			}
			r, _ := utf8.DecodeRuneInString(text)
			if r == 0 || r == '"' || r == '\n' || r == '\r' || r == utf8.RuneError {
				return s, fmt.Errorf("invalid CSV delimiter")
			}
			s.delimiter = r
		case "header":
			if string(v) == "null" || json.Unmarshal(v, &s.header) != nil {
				return s, fmt.Errorf("header must be boolean")
			}
		case "columns":
			if json.Unmarshal(v, &s.columns) != nil {
				return s, fmt.Errorf("columns must be strings")
			}
			seen := map[string]bool{}
			for _, c := range s.columns {
				if c == "" || seen[c] {
					return s, fmt.Errorf("columns must be unique and nonempty")
				}
				seen[c] = true
			}
		case "lineEnding":
			var text string
			if !writer || json.Unmarshal(v, &text) != nil || text != "lf" && text != "crlf" {
				return s, fmt.Errorf("writer lineEnding must be lf or crlf")
			}
			s.crlf = text == "crlf"
		default:
			return s, fmt.Errorf("unknown format setting %q", k)
		}
	}
	return s, nil
}
func (c contract) decode(body any, b *budget) (any, error) {
	var data []byte
	switch x := body.(type) {
	case string:
		data = []byte(x)
	case []byte:
		data = x
	default:
		if c.media == "application/json" {
			return normalize(body, b, 0)
		}
		return nil, fmt.Errorf("format requires text or bytes")
	}
	if len(data) > b.limits.Bytes {
		return nil, fmt.Errorf("input byte limit exceeded")
	}
	if !utf8.Valid(data) {
		return nil, fmt.Errorf("input is not UTF-8")
	}
	switch c.media {
	case "application/json":
		return readJSON(data, b)
	case "application/xml":
		return readXML(data, b)
	default:
		return readCSV(string(data), c.reader, b)
	}
}
func (c contract) encode(v any, b *budget) (string, error) {
	w := &output{b: b}
	var e error
	switch c.media {
	case "application/json":
		e = w.json(v, 0)
	case "application/xml":
		e = w.xml(v)
	case "text/csv":
		e = w.csv(v, c.writer)
	}
	return w.s.String(), e
}
func readCSV(src string, s settings, b *budget) (any, error) {
	r := csv.NewReader(strings.NewReader(src))
	r.Comma = s.delimiter
	columns := s.columns
	if s.header {
		var e error
		columns, e = r.Read()
		if e != nil {
			return nil, fmt.Errorf("CSV requires a header row")
		}
		if len(s.columns) > 0 && strings.Join(columns, "\x00") != strings.Join(s.columns, "\x00") {
			return nil, fmt.Errorf("CSV header does not match columns")
		}
	}
	if len(columns) == 0 {
		return nil, fmt.Errorf("CSV requires column names")
	}
	seen := map[string]bool{}
	for _, c := range columns {
		if c == "" || seen[c] {
			return nil, fmt.Errorf("CSV header names must be unique and nonempty")
		}
		seen[c] = true
	}
	r.FieldsPerRecord = len(columns)
	rows := []any{}
	for {
		if e := b.tick(); e != nil {
			return nil, e
		}
		record, e := r.Read()
		if e == io.EOF {
			break
		}
		if e != nil {
			return nil, fmt.Errorf("invalid CSV record")
		}
		if e = b.node(0); e != nil {
			return nil, e
		}
		o := object{}
		for i, v := range record {
			if e = b.node(1); e != nil {
				return nil, e
			}
			o = append(o, member{columns[i], v})
		}
		rows = append(rows, o)
	}
	return rows, nil
}

// Write implements io.Writer with cancellation and output limits.
func (w *output) Write(p []byte) (int, error) {
	if e := w.put(string(p)); e != nil {
		return 0, e
	}
	return len(p), nil
}
func (w *output) csv(v any, s settings) error {
	rows, ok := v.([]any)
	if !ok {
		return fmt.Errorf("CSV output must be an array of objects")
	}
	if len(s.columns) == 0 {
		return fmt.Errorf("CSV output requires writer.columns")
	}
	c := csv.NewWriter(w)
	c.Comma = s.delimiter
	c.UseCRLF = s.crlf
	if s.header {
		if e := c.Write(s.columns); e != nil {
			return e
		}
	}
	for _, v := range rows {
		if e := w.b.node(0); e != nil {
			return e
		}
		o, ok := v.(object)
		if !ok {
			return fmt.Errorf("CSV row must be an object")
		}
		cells := []string{}
		for _, key := range s.columns {
			v, e := w.b.lookup(o, key)
			if e != nil {
				return e
			}
			text := ""
			if v != nil && !isMissing(v) {
				s, e := call("string", []any{v}, w.b)
				if e != nil {
					return fmt.Errorf("CSV cells must be scalar")
				}
				text = s.(string)
			}
			cells = append(cells, text)
		}
		if e := c.Write(cells); e != nil {
			return e
		}
	}
	c.Flush()
	return c.Error()
}
