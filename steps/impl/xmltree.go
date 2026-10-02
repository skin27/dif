package impl

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"strings"
)

// xmlElem is an element of an XML document, with names as written
// (prefix:local) and its children in document order. The converters build
// on it.
type xmlElem struct {
	name     string
	attrs    []xmlAttr
	children []*xmlElem
	text     string // the text directly in the element (not in its children)
	value    string // all text in the element and its descendants, in document order
}

type xmlAttr struct {
	name, value string
}

// parseXMLTree parses data into its root element.
func parseXMLTree(data []byte) (*xmlElem, error) {
	var (
		root  *xmlElem
		stack []*xmlElem
		text  [][]byte // text directly in each open element
		value [][]byte // all text in each open element
	)
	d := xml.NewDecoder(bytes.NewReader(data))
	for {
		tok, err := d.RawToken() // keeps prefixes as written
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("body is not XML: %w", err)
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if len(stack) == 0 && root != nil {
				return nil, fmt.Errorf("body is not XML: more than one root element")
			}
			e := &xmlElem{name: qualifiedName(t.Name)}
			for _, a := range t.Attr {
				e.attrs = append(e.attrs, xmlAttr{qualifiedName(a.Name), a.Value})
			}
			if len(stack) == 0 {
				root = e
			} else {
				parent := stack[len(stack)-1]
				parent.children = append(parent.children, e)
			}
			stack = append(stack, e)
			text, value = append(text, nil), append(value, nil)
		case xml.EndElement:
			if len(stack) == 0 || stack[len(stack)-1].name != qualifiedName(t.Name) {
				return nil, fmt.Errorf("body is not XML: unexpected end element </%s>", qualifiedName(t.Name))
			}
			e, n := stack[len(stack)-1], len(stack)-1
			e.text, e.value = string(text[n]), string(value[n])
			stack, text, value = stack[:n], text[:n], value[:n]
		case xml.CharData:
			if len(stack) > 0 {
				text[len(text)-1] = append(text[len(text)-1], t...)
				for i := range value {
					value[i] = append(value[i], t...)
				}
			} else if len(bytes.TrimSpace(t)) > 0 {
				return nil, fmt.Errorf("body is not XML: text outside the root element")
			}
		}
	}
	if root == nil {
		return nil, fmt.Errorf("body is not XML: no root element")
	}
	if len(stack) > 0 {
		return nil, fmt.Errorf("body is not XML: element <%s> is not closed", stack[len(stack)-1].name)
	}
	return root, nil
}

func qualifiedName(n xml.Name) string {
	if n.Space == "" {
		return n.Local
	}
	return n.Space + ":" + n.Local
}

// localName returns name without its namespace prefix.
func localName(name string) string {
	if _, local, ok := strings.Cut(name, ":"); ok {
		return local
	}
	return name
}

// isNamespaceDecl reports whether an attribute declares a namespace (xmlns, xmlns:p).
func isNamespaceDecl(name string) bool {
	return name == "xmlns" || strings.HasPrefix(name, "xmlns:")
}

var (
	xmlTextEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	xmlAttrEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
)

// xmlDecl is the declaration the converters that write a document start with.
const xmlDecl = `<?xml version="1.0" encoding="UTF-8"?>` + "\n"
