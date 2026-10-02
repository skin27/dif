package impl

import (
	"context"
	"fmt"
	"strings"

	"dif/message"
	stepdef "dif/steps/definition"
)

// jsonToXMLSimpleAction converts a JSON body to XML the way org.json does,
// the reverse of xmlToJSONSimpleAction:
//
//   - an object member becomes an element named after its key; the member
//     "content" becomes text
//   - an array member becomes one element per item, all named after the key;
//     with changeArrayElements, one element named after the key holding an
//     element arrayElementName per item
//   - null becomes the text null
//   - with addRoot, the result is wrapped in the element rootTag; without it,
//     a top-level object with several members gives several root elements
//
// A key that is not an XML name fails the message when checkJsonKeys;
// otherwise its invalid characters become _.
type jsonToXMLSimpleAction struct {
	addRoot, checkKeys, changeArrayElements bool
	rootTag, arrayElementName               string
}

func newJSONToXMLSimpleAction(_ string, p stepdef.Params) (stepdef.Processor, error) {
	a := jsonToXMLSimpleAction{
		addRoot:             p["addRoot"].(bool),
		checkKeys:           p["checkJsonKeys"].(bool),
		changeArrayElements: p["changeArrayElements"].(bool),
		rootTag:             p["rootTag"].(string),
		arrayElementName:    p["arrayElementName"].(string),
	}
	for opt, name := range map[string]string{"rootTag": a.rootTag, "arrayElementName": a.arrayElementName} {
		if !isXMLQName(name) {
			return nil, fmt.Errorf("option %s: %q is not an XML name", opt, name)
		}
	}
	return a, nil
}

func (a jsonToXMLSimpleAction) Process(_ context.Context, m message.Message) (message.Message, error) {
	v, err := readJSON(m[message.Body])
	if err != nil {
		return nil, err
	}
	var b strings.Builder
	if a.addRoot {
		err = a.element(&b, a.rootTag, v)
	} else {
		err = a.content(&b, v)
	}
	if err != nil {
		return nil, err
	}
	m[message.Body] = b.String()
	return m, nil
}

// content writes v as the content of an element.
func (a jsonToXMLSimpleAction) content(b *strings.Builder, v any) error {
	switch x := v.(type) {
	case jsonObject:
		for _, mb := range x {
			if mb.key == "content" {
				b.WriteString(xmlTextEscaper.Replace(a.text(mb.value)))
				continue
			}
			name, err := a.name(mb.key)
			if err != nil {
				return err
			}
			if err := a.element(b, name, mb.value); err != nil {
				return err
			}
		}
		return nil
	case []any:
		for _, e := range x {
			if err := a.element(b, a.arrayElementName, e); err != nil {
				return err
			}
		}
		return nil
	}
	b.WriteString(xmlTextEscaper.Replace(a.text(v)))
	return nil
}

// element writes v as the element name; an array as one element per item,
// or with changeArrayElements as one element holding them.
func (a jsonToXMLSimpleAction) element(b *strings.Builder, name string, v any) error {
	if arr, ok := v.([]any); ok && !a.changeArrayElements {
		for _, e := range arr {
			if err := a.element(b, name, e); err != nil {
				return err
			}
		}
		return nil
	}
	var c strings.Builder
	if err := a.content(&c, v); err != nil {
		return err
	}
	if c.Len() == 0 {
		b.WriteString("<" + name + "/>")
	} else {
		b.WriteString("<" + name + ">" + c.String() + "</" + name + ">")
	}
	return nil
}

func (a jsonToXMLSimpleAction) text(v any) string {
	if v == nil {
		return "null"
	}
	return scalarText(v)
}

// name returns key as an element name.
func (a jsonToXMLSimpleAction) name(key string) (string, error) {
	if isXMLQName(key) {
		return key, nil
	}
	if a.checkKeys {
		return "", fmt.Errorf("key %q is not an XML name", key)
	}
	return sanitizeXMLName(key), nil
}

// sanitizeXMLName returns s with every character an XML name cannot hold
// replaced by _, and _ in front if it cannot start one.
func sanitizeXMLName(s string) string {
	b := []rune(s)
	for i, r := range b {
		if !isXMLName("a" + string(r)) {
			b[i] = '_'
		}
	}
	s = string(b)
	if !isXMLName(s) {
		s = "_" + s
	}
	return s
}
