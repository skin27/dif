package impl

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"dif/message"
	stepdef "dif/steps/definition"
)

// xmlToJSONAction converts an XML body to JSON the way json-lib (Camel's
// xmljson) does:
//
//   - an element with only text becomes a string, an empty one ""
//   - an element with attributes or children becomes an object: attributes
//     as "@name", children by name (repeated names become an array) and its
//     text, if any, as "#text"
//   - an element with two or more children of the same name, and no
//     attributes, becomes an array of their values
//   - the root element is left out, unless forceTopLevelObject
//
// Values stay strings unless typeHints: then a json_type attribute (number,
// boolean, string, null, array or object) sets the type.
type xmlToJSONAction struct {
	forceTopLevelObject, skipWhitespace, trimSpaces, skipNamespaces, removeNamespacePrefixes, typeHints bool
}

func newXMLToJSONAction(_ string, p stepdef.Params) (stepdef.Processor, error) {
	return xmlToJSONAction{
		forceTopLevelObject:     p["forceTopLevelObject"].(bool),
		skipWhitespace:          p["skipWhitespace"].(bool),
		trimSpaces:              p["trimSpaces"].(bool),
		skipNamespaces:          p["skipNamespaces"].(bool),
		removeNamespacePrefixes: p["removeNamespacePrefixes"].(bool),
		typeHints:               p["typeHints"].(bool),
	}, nil
}

func (a xmlToJSONAction) Process(_ context.Context, m message.Message) (message.Message, error) {
	root, err := parseXMLTree(bytesOf(m[message.Body]))
	if err != nil {
		return nil, err
	}
	v, err := a.value(root)
	if err != nil {
		return nil, err
	}
	if a.forceTopLevelObject {
		v = jsonObject{{a.name(root.name), v}}
	}
	var b bytes.Buffer
	writeJSON(&b, v)
	m[message.Body] = b.String()
	return m, nil
}

func (a xmlToJSONAction) name(n string) string {
	if a.removeNamespacePrefixes {
		return localName(n)
	}
	return n
}

// value returns the JSON value of e.
func (a xmlToJSONAction) value(e *xmlElem) (any, error) {
	var attrs []xmlAttr
	hint := ""
	for _, at := range e.attrs {
		switch {
		case a.typeHints && at.name == "json_type":
			hint = at.value
		case a.skipNamespaces && isNamespaceDecl(at.name):
		default:
			attrs = append(attrs, at)
		}
	}

	if len(attrs) == 0 && len(e.children) == 0 {
		return a.typed(a.text(e.text), hint)
	}

	if len(attrs) == 0 && (hint == "array" || hint == "" && sameNames(e.children)) {
		arr := make([]any, 0, len(e.children))
		for _, c := range e.children {
			v, err := a.value(c)
			if err != nil {
				return nil, err
			}
			arr = append(arr, v)
		}
		return arr, nil
	}

	o := jsonObject{}
	for _, at := range attrs {
		o.add("@"+a.name(at.name), at.value)
	}
	for _, c := range e.children {
		v, err := a.value(c)
		if err != nil {
			return nil, err
		}
		o.add(a.name(c.name), v)
	}
	if t := a.text(e.text); strings.TrimSpace(t) != "" {
		o.add("#text", t)
	}
	return o, nil
}

// sameNames reports whether there are two or more elements, all with the same name.
func sameNames(es []*xmlElem) bool {
	if len(es) < 2 {
		return false
	}
	for _, e := range es[1:] {
		if e.name != es[0].name {
			return false
		}
	}
	return true
}

func (a xmlToJSONAction) text(s string) string {
	if a.trimSpaces || a.skipWhitespace && strings.TrimSpace(s) == "" {
		return strings.TrimSpace(s)
	}
	return s
}

// typed converts the text of a leaf element by its type hint.
func (a xmlToJSONAction) typed(s, hint string) (any, error) {
	t := strings.TrimSpace(s)
	switch hint {
	case "", "string":
		return s, nil
	case "number":
		if isJSONNumber(t) {
			return json.Number(t), nil
		}
	case "boolean":
		if t == "true" || t == "false" {
			return t == "true", nil
		}
	case "null":
		return nil, nil
	case "array":
		return []any{}, nil
	case "object":
		return jsonObject{}, nil
	default:
		return nil, fmt.Errorf("unknown json_type %q", hint)
	}
	return nil, fmt.Errorf("json_type %s: %q is not a %s", hint, s, hint)
}
