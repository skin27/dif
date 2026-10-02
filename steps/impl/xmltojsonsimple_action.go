package impl

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"

	"dif/message"
	stepdef "dif/steps/definition"
)

// xmlToJSONSimpleAction converts an XML body to JSON the way org.json does:
//
//   - the result is an object with the root element, unless removeRoot
//   - an element with only text becomes its trimmed text, an empty one ""
//   - an element with attributes or children becomes an object: attributes
//     and children by name (repeated names become an array) and its text, if
//     any, as "content"
//   - text that is a number, true, false or null becomes that JSON value,
//     unless keepStrings
//
// With hasTypes, a type attribute (string, number, integer, double, boolean or
// null) sets the type of an element's text; text that does not fit becomes
// null (typeValueMismatch NULL) or stays a string (ORIGINAL).
type xmlToJSONSimpleAction struct {
	keepStrings, removeNamespaces, removeRoot, hasTypes bool
	mismatchNull                                        bool
}

func newXMLToJSONSimpleAction(_ string, p stepdef.Params) (stepdef.Processor, error) {
	return xmlToJSONSimpleAction{
		keepStrings:      p["keepStrings"].(bool),
		removeNamespaces: p["removeNamespaces"].(bool),
		removeRoot:       p["removeRoot"].(bool),
		hasTypes:         p["hasTypes"].(bool),
		mismatchNull:     p["typeValueMismatch"] == "NULL",
	}, nil
}

func (a xmlToJSONSimpleAction) Process(_ context.Context, m message.Message) (message.Message, error) {
	root, err := parseXMLTree(bytesOf(m[message.Body]))
	if err != nil {
		return nil, err
	}
	v := a.value(root)
	if !a.removeRoot {
		v = jsonObject{{a.name(root.name), v}}
	}
	var b bytes.Buffer
	writeJSON(&b, v)
	m[message.Body] = b.String()
	return m, nil
}

func (a xmlToJSONSimpleAction) name(n string) string {
	if a.removeNamespaces {
		return localName(n)
	}
	return n
}

func (a xmlToJSONSimpleAction) value(e *xmlElem) any {
	var attrs []xmlAttr
	typ := ""
	for _, at := range e.attrs {
		switch {
		case a.hasTypes && at.name == "type":
			typ = at.value
		case a.removeNamespaces && isNamespaceDecl(at.name):
		default:
			attrs = append(attrs, at)
		}
	}
	text := strings.TrimSpace(e.text)
	if len(attrs) == 0 && len(e.children) == 0 {
		if typ != "" {
			return a.typed(text, typ)
		}
		return a.scalar(text)
	}

	o := jsonObject{}
	for _, at := range attrs {
		o.add(a.name(at.name), a.scalar(at.value))
	}
	for _, c := range e.children {
		o.add(a.name(c.name), a.value(c))
	}
	if text != "" {
		o.add("content", a.scalar(text))
	}
	return o
}

// scalar returns s as a number, boolean or null when it is one, unless keepStrings.
func (a xmlToJSONSimpleAction) scalar(s string) any {
	if a.keepStrings {
		return s
	}
	switch {
	case s == "true" || s == "false":
		return s == "true"
	case s == "null":
		return nil
	case isJSONNumber(s):
		return json.Number(s)
	}
	return s
}

// typed returns s as the type typ names.
func (a xmlToJSONSimpleAction) typed(s, typ string) any {
	ok := true
	var v any = s
	switch strings.ToLower(typ) {
	case "string":
	case "number", "double", "float", "decimal":
		v, ok = json.Number(s), isJSONNumber(s)
	case "integer", "int", "long":
		v, ok = json.Number(s), isJSONNumber(s) && !strings.ContainsAny(s, ".eE")
	case "boolean":
		v, ok = s == "true", s == "true" || s == "false"
	case "null":
		v = nil
	default:
		ok = false
	}
	switch {
	case ok:
		return v
	case a.mismatchNull:
		return nil
	}
	return s
}
