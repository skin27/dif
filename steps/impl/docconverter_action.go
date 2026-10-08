package impl

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"dif/message"
	stepdef "dif/steps/definition"
)

// docConverterAction converts the body between CSV, XML, JSON and YAML, as the
// platform's docconverter does (convert: xmltojson, csvtoyaml, …). The formats
// meet in one tree of ordered objects, arrays and values (see jsonObject),
// which makes the conversions agree with those of the other converters:
//
//   - CSV is the table the platform writes: rows, a row per record, an item per
//     cell, all text (<rows><row><item>…) or, as JSON, {"rows":{"row":[{"item":[…]}]}}.
//     To CSV, only such a table converts; anything else fails the message.
//   - XML is read as xmltojsonsimple reads it (attributes as "@name", repeated
//     elements as an array), and written as jsontoxmlsimple writes it. CSV to XML
//     starts with the declaration the platform writes, in single quotes.
//   - JSON and YAML keep their types. XML and CSV have none, so to JSON their
//     values stay text, and to YAML a number, true, false or null is written as
//     that value (as in xmltojsonsimple without keepStrings).
//   - YAML is written as Jackson writes it: a --- line, strings in double
//     quotes. From XML, its members are in the order a Java HashMap lists
//     them, as the platform's output is (see javaMapOrder).
type docConverterAction struct {
	from, to string
}

var docFormats = []string{"csv", "xml", "json", "yaml"}

func newDocConverterAction(_ string, p stepdef.Params) (stepdef.Processor, error) {
	convert := strings.ToLower(p["convert"].(string))
	for _, from := range docFormats {
		if to, ok := strings.CutPrefix(convert, from+"to"); ok {
			for _, f := range docFormats {
				if to == f && to != from {
					return docConverterAction{from, to}, nil
				}
			}
		}
	}
	return nil, fmt.Errorf("option convert: %q is not a conversion; want one of csv, xml, json, yaml to another, such as xmltojson", p["convert"])
}

func (a docConverterAction) Process(_ context.Context, m message.Message) (message.Message, error) {
	tree, err := a.read(m[message.Body])
	if err != nil {
		return nil, err
	}
	out, contentType, err := a.write(tree)
	if err != nil {
		return nil, err
	}
	m[message.Body] = out
	m[message.ContentType] = contentType
	return m, nil
}

// typedTo reports whether the values of an XML or CSV document are typed in the
// result, which only YAML does.
func (a docConverterAction) typedTo() bool { return a.to == "yaml" }

func (a docConverterAction) read(body any) (any, error) {
	switch a.from {
	case "csv":
		tree, err := readCSVTable(text(body))
		if err == nil && a.typedTo() {
			tree = typedValues(tree)
		}
		return tree, err
	case "xml":
		root, err := parseXMLTree(bytesOf(body))
		if err != nil {
			return nil, err
		}
		s := xmlToJSONSimpleAction{keepStrings: !a.typedTo()}
		v, err := s.value(root)
		if err != nil {
			return nil, err
		}
		return jsonObject{{root.name, v}}, nil
	case "yaml":
		return readYAML(text(body))
	}
	return readJSON(body)
}

func (a docConverterAction) write(tree any) (out, contentType string, err error) {
	switch a.to {
	case "json":
		var b bytes.Buffer
		writeJSON(&b, tree)
		return b.String(), "application/json", nil
	case "xml":
		var b strings.Builder
		if a.from == "csv" {
			b.WriteString(`<?xml version='1.0' encoding='UTF-8'?>`)
		}
		if err := (jsonToXMLSimpleAction{arrayElementName: "element"}).content(&b, tree); err != nil {
			return "", "", err
		}
		return b.String(), "application/xml", nil
	case "yaml":
		if a.from == "xml" {
			tree = javaOrdered(tree)
		}
		out, err := writeYAML(tree)
		return out, "application/yaml", err
	}
	out, err = writeCSVTable(tree)
	return out, "text/csv", err
}

// typedValues returns v with its text values read as xmltojsonsimple reads
// them: a number, true, false or null becomes that value.
func typedValues(v any) any {
	switch x := v.(type) {
	case jsonObject:
		out := make(jsonObject, len(x))
		for i, m := range x {
			out[i] = jsonMember{m.key, typedValues(m.value)}
		}
		return out
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = typedValues(e)
		}
		return out
	case jsonArray:
		return typedValues([]any(x))
	case string:
		return xmlToJSONSimpleAction{}.scalar(x)
	}
	return v
}

// bom starts a text file that was saved as UTF-8 with a byte order mark.
const bom = "\xef\xbb\xbf"

// readCSVTable reads CSV records into a table: {"rows":{"row":[{"item":[cells]}]}}.
func readCSVTable(body string) (any, error) {
	r := csv.NewReader(strings.NewReader(strings.TrimPrefix(body, bom)))
	r.FieldsPerRecord = -1 // records may differ in length
	rows := []any{}
	for {
		record, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("body is not CSV: %w", err)
		}
		cells := make([]any, len(record))
		for i, c := range record {
			cells[i] = c
		}
		rows = append(rows, jsonObject{{"item", cells}})
	}
	return jsonObject{{"rows", jsonObject{{"row", rows}}}}, nil
}

// writeCSVTable writes a table (see readCSVTable) as CSV, a line per row. The
// single row or item that XML and org.json do not make an array of is a row or
// item as well.
func writeCSVTable(tree any) (string, error) {
	notTable := fmt.Errorf("the document is not a table: want rows, with a row for each record, holding an item for each cell, as csvtojson writes it")
	only := func(v any, key string) (any, bool) {
		o, ok := v.(jsonObject)
		if !ok || len(o) != 1 || (key != "" && o[0].key != key) {
			return nil, false
		}
		return o[0].value, true
	}
	rows, ok := only(tree, "")
	if ok {
		rows, ok = only(rows, "")
	}
	if !ok {
		return "", notTable
	}
	list, ok := asList(rows)
	if o, isObject := rows.(jsonObject); isObject { // one row
		list, ok = []any{o}, true
	}
	if !ok {
		return "", notTable
	}

	var b bytes.Buffer
	w := csv.NewWriter(&b)
	for _, row := range list {
		items, ok := only(row, "item")
		if !ok {
			return "", notTable
		}
		cells, isList := asList(items)
		if !isList {
			cells = []any{items}
		}
		record := make([]string, len(cells))
		for i, c := range cells {
			switch x := c.(type) {
			case nil:
			case string:
				record[i] = x
			case json.Number:
				record[i] = string(x)
			case bool:
				record[i] = strconv.FormatBool(x)
			default:
				return "", fmt.Errorf("the document is not a table: an item holds an object or a list")
			}
		}
		if err := w.Write(record); err != nil {
			return "", err
		}
	}
	w.Flush()
	return b.String(), w.Error()
}

// asList returns v as a list, if it is one (also the array of repeated XML elements).
func asList(v any) ([]any, bool) {
	switch x := v.(type) {
	case []any:
		return x, true
	case jsonArray:
		return []any(x), true
	}
	return nil, false
}

// maxYAMLNodes bounds what a YAML document may expand to, aliases included.
const maxYAMLNodes = 1 << 20

// readYAML reads the first document of body as an ordered tree.
func readYAML(body string) (any, error) {
	var doc yaml.Node
	if err := yaml.NewDecoder(strings.NewReader(body)).Decode(&doc); err != nil {
		if err == io.EOF {
			return nil, fmt.Errorf("body is not YAML: it is empty")
		}
		return nil, fmt.Errorf("body is not YAML: %w", err)
	}
	if len(doc.Content) == 0 {
		return nil, nil
	}
	budget := maxYAMLNodes
	v, err := yamlValue(doc.Content[0], &budget)
	if err != nil {
		return nil, fmt.Errorf("body is not YAML: %w", err)
	}
	return v, nil
}

func yamlValue(n *yaml.Node, budget *int) (any, error) {
	if *budget--; *budget < 0 {
		return nil, fmt.Errorf("the document is too large (aliases included)")
	}
	switch n.Kind {
	case yaml.AliasNode:
		return yamlValue(n.Alias, budget)
	case yaml.MappingNode:
		o := jsonObject{}
		for i := 0; i+1 < len(n.Content); i += 2 {
			k := n.Content[i]
			for k.Kind == yaml.AliasNode {
				k = k.Alias
			}
			if k.Kind != yaml.ScalarNode {
				return nil, fmt.Errorf("line %d: a key that is not a scalar", k.Line)
			}
			v, err := yamlValue(n.Content[i+1], budget)
			if err != nil {
				return nil, err
			}
			replaced := false
			for j := range o { // a later member with the same key wins
				if o[j].key == k.Value {
					o[j].value, replaced = v, true
				}
			}
			if !replaced {
				o = append(o, jsonMember{k.Value, v})
			}
		}
		return o, nil
	case yaml.SequenceNode:
		list := make([]any, 0, len(n.Content))
		for _, c := range n.Content {
			v, err := yamlValue(c, budget)
			if err != nil {
				return nil, err
			}
			list = append(list, v)
		}
		return list, nil
	}
	switch n.ShortTag() {
	case "!!null":
		return nil, nil
	case "!!bool":
		var b bool
		if err := n.Decode(&b); err != nil {
			return n.Value, nil
		}
		return b, nil
	case "!!int":
		if isJSONNumber(n.Value) {
			return json.Number(n.Value), nil
		}
		var i int64
		if err := n.Decode(&i); err != nil {
			return n.Value, nil // too big, or not decimal
		}
		return json.Number(strconv.FormatInt(i, 10)), nil
	case "!!float":
		if isJSONNumber(n.Value) {
			return json.Number(n.Value), nil
		}
		var f float64
		if err := n.Decode(&f); err != nil || math.IsNaN(f) || math.IsInf(f, 0) {
			return n.Value, nil
		}
		return json.Number(strconv.FormatFloat(f, 'g', -1, 64)), nil
	}
	return n.Value, nil // text, and what JSON has no type for: a date, binary
}

// writeYAML writes a tree as a YAML document in the style of Jackson: it starts
// with ---, strings are in double quotes, numbers, booleans and null are not.
func writeYAML(tree any) (string, error) {
	var b bytes.Buffer
	b.WriteString("---\n")
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(yamlNode(tree)); err != nil {
		return "", err
	}
	if err := enc.Close(); err != nil {
		return "", err
	}
	return b.String(), nil
}

func yamlNode(v any) *yaml.Node {
	switch x := v.(type) {
	case jsonObject:
		n := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		for _, m := range x {
			key := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: m.key}
			if !plainYAMLKey(m.key) {
				key.Style = yaml.DoubleQuotedStyle
			}
			n.Content = append(n.Content, key, yamlNode(m.value))
		}
		return n
	case jsonArray:
		return yamlNode([]any(x))
	case []any:
		n := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		for _, e := range x {
			n.Content = append(n.Content, yamlNode(e))
		}
		return n
	case string:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: x, Style: yaml.DoubleQuotedStyle}
	case json.Number:
		tag := "!!float"
		if !strings.ContainsAny(string(x), ".eE") {
			tag = "!!int"
		}
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: string(x)}
	case bool:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: strconv.FormatBool(x)}
	case nil:
		return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!null", Value: "null"}
	}
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: fmt.Sprint(v), Style: yaml.DoubleQuotedStyle}
}

// plainYAMLKey reports whether a key reads as itself without quotes: words of
// letters, digits and _ - . that YAML would not take for a number, a boolean
// or null.
func plainYAMLKey(k string) bool {
	if k == "" || k[0] >= '0' && k[0] <= '9' || k[0] == '-' || k[0] == '.' {
		return false
	}
	for _, c := range k {
		if !(c == '_' || c == '-' || c == '.' || c >= '0' && c <= '9' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c > 127) {
			return false
		}
	}
	switch strings.ToLower(k) {
	case "true", "false", "yes", "no", "on", "off", "y", "n", "null", "~":
		return false
	}
	return true
}
