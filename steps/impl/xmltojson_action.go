package impl

import (
	"context"
	"strings"

	"dif/message"
	stepdef "dif/steps/definition"
)

// xmlToJSONAction converts an XML body to JSON the way the Java platform does,
// with Camel's xmljson data format, which uses json-lib's XMLSerializer (see
// jsonlib_xml.go for the rules, which are json-lib's):
//
//   - attributes are "@name", namespace declarations "@xmlns:prefix" and text
//     beside other content "#text"
//   - elements with the same name make an array, as does an element that holds
//     only elements with one name (and whitespace)
//   - the root element is left out, unless forceTopLevelObject
//   - values stay strings, unless typeHints: then json_type (number, integer,
//     float, boolean, string, function) sets the type of an element's text,
//     json_class (object, array) tells if it is an object or an array, and
//     json_null makes it null
//
// The options skipWhitespace, trimSpaces, skipNamespaces and
// removeNamespacePrefixes are json-lib's.
type xmlToJSONAction struct {
	jlOptions
}

func newXMLToJSONAction(_ string, p stepdef.Params) (stepdef.Processor, error) {
	return xmlToJSONAction{jlOptions{
		forceTopLevelObject:   p["forceTopLevelObject"].(bool),
		skipWhitespace:        p["skipWhitespace"].(bool),
		trimSpaces:            p["trimSpaces"].(bool),
		skipNamespaces:        p["skipNamespaces"].(bool),
		removeNamespacePrefix: p["removeNamespacePrefixes"].(bool),
		typeHints:             p["typeHints"].(bool),
	}}, nil
}

func (a xmlToJSONAction) Process(_ context.Context, m message.Message) (message.Message, error) {
	root, err := parseXOM(bytesOf(m[message.Body]))
	if err != nil {
		return nil, err
	}
	v, err := a.read(root)
	if err != nil {
		return nil, err
	}
	var b strings.Builder
	if err := jlWrite(&b, v); err != nil {
		return nil, err
	}
	m[message.Body] = b.String()
	m[message.ContentType] = "application/json"
	return m, nil
}
