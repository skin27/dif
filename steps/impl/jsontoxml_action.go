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

// jsonToXMLAction converts a JSON body to XML the way json-lib (Camel's
// xmljson) does, the reverse of xmlToJSONAction:
//
//   - a top-level object or value becomes the element rootName, a top-level
//     array the element arrayName
//   - an object member becomes an element named after its key, except "@name"
//     members, which become attributes, and "#text", which becomes text
//   - an array becomes an element holding an element elementName per item
//
// With typeHints every element gets a json_type attribute (object, array,
// string, number, boolean or null), so xmltojson can restore the types.
type jsonToXMLAction struct {
	elementName, arrayName, rootName string
	typeHints                        bool
}

func newJSONToXMLAction(_ string, p stepdef.Params) (stepdef.Processor, error) {
	a := jsonToXMLAction{
		elementName: p["elementName"].(string),
		arrayName:   p["arrayName"].(string),
		rootName:    p["rootName"].(string),
		typeHints:   p["typeHints"].(bool),
	}
	for opt, name := range map[string]string{"elementName": a.elementName, "arrayName": a.arrayName, "rootName": a.rootName} {
		if !isXMLQName(name) {
			return nil, fmt.Errorf("option %s: %q is not an XML name", opt, name)
		}
	}
	return a, nil
}

func (a jsonToXMLAction) Process(_ context.Context, m message.Message) (message.Message, error) {
	v, err := readJSON(m[message.Body])
	if err != nil {
		return nil, err
	}
	var b strings.Builder
	b.WriteString(xmlDecl)
	name := a.rootName
	if _, ok := v.([]any); ok {
		name = a.arrayName
	}
	if err := a.write(&b, name, v); err != nil {
		return nil, err
	}
	m[message.Body] = b.String()
	m[message.ContentType] = "application/xml"
	return m, nil
}

// write writes v as the element name.
func (a jsonToXMLAction) write(b *strings.Builder, name string, v any) error {
	if !isXMLQName(name) {
		return fmt.Errorf("key %q is not an XML name", name)
	}
	b.WriteString("<" + name)
	if a.typeHints {
		b.WriteString(` json_type="` + jsonType(v) + `"`)
	}

	var content strings.Builder
	switch x := v.(type) {
	case jsonObject:
		for _, mb := range x {
			switch attr, isAttr := strings.CutPrefix(mb.key, "@"); {
			case isAttr:
				if !isXMLQName(attr) {
					return fmt.Errorf("key %q is not an XML attribute name", mb.key)
				}
				if _, ok := mb.value.(jsonObject); ok {
					return fmt.Errorf("attribute %s: an object is not a value", mb.key)
				}
				if _, ok := mb.value.([]any); ok {
					return fmt.Errorf("attribute %s: an array is not a value", mb.key)
				}
				b.WriteString(" " + attr + `="` + xmlAttrEscaper.Replace(scalarText(mb.value)) + `"`)
			case mb.key == "#text":
				content.WriteString(xmlTextEscaper.Replace(scalarText(mb.value)))
			default:
				if err := a.write(&content, mb.key, mb.value); err != nil {
					return err
				}
			}
		}
	case []any:
		for _, e := range x {
			if err := a.write(&content, a.elementName, e); err != nil {
				return err
			}
		}
	default:
		content.WriteString(xmlTextEscaper.Replace(scalarText(v)))
	}

	if content.Len() == 0 {
		b.WriteString("/>")
		return nil
	}
	b.WriteString(">" + content.String() + "</" + name + ">")
	return nil
}

// jsonType names the JSON type of v for type hints.
func jsonType(v any) string {
	switch v.(type) {
	case jsonObject:
		return "object"
	case []any:
		return "array"
	case json.Number:
		return "number"
	case bool:
		return "boolean"
	case nil:
		return "null"
	}
	return "string"
}

// scalarText returns a JSON value as text: null is empty, an object or
// array (as "#text") is JSON.
func scalarText(v any) string {
	switch v.(type) {
	case nil:
		return ""
	case jsonObject, []any:
		var b bytes.Buffer
		writeJSON(&b, v)
		return b.String()
	}
	return text(v)
}

// isXMLQName reports whether s is an XML name with at most one prefix (p:name).
func isXMLQName(s string) bool {
	prefix, local, ok := strings.Cut(s, ":")
	if !ok {
		return isXMLName(s)
	}
	return isXMLName(prefix) && isXMLName(local)
}
