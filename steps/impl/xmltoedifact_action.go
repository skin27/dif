package impl

import (
	"context"
	"fmt"
	"strings"

	"dif/message"
	stepdef "dif/steps/definition"
)

// xmlToEDIFACTAction converts the XML form of an EDIFACT interchange, as
// Smooks writes it, into EDIFACT with the default delimiters (+ : ' ? .):
//
//	<env:unEdifact>
//	  <env:UNB>…</env:UNB>
//	  <env:interchangeMessage>
//	    <env:UNH>…</env:UNH>
//	    <iftmin:IFTMIN>
//	      <iftmin:BGM><c:C002><c:e1001>340</c:e1001></c:C002><c:e1004>347605</c:e1004></iftmin:BGM>
//	    …
//
// An element whose name is three upper-case characters (BGM, UNB, …) is a
// segment; any other element above one (the interchange, a message, a segment
// group) is only walked through, in document order. A segment's children are
// its data elements, in order; a child that has children is a composite,
// whose children are its components. The conversion is structural: it does
// not know the message definitions of edifactType, so an optional element that
// the XML omits is not restored as an empty position. To keep a position, leave
// the element in the XML, empty. Trailing empty elements and components are
// dropped, and the release character ? escapes a delimiter in a value.
type xmlToEDIFACTAction struct{}

func newXMLToEDIFACTAction(string, stepdef.Params) (stepdef.Processor, error) {
	return xmlToEDIFACTAction{}, nil
}

var edifactEscaper = strings.NewReplacer("?", "??", "+", "?+", ":", "?:", "'", "?'")

func (xmlToEDIFACTAction) Process(_ context.Context, m message.Message) (message.Message, error) {
	root, err := parseXMLTree(bytesOf(m[message.Body]))
	if err != nil {
		return nil, err
	}
	var b strings.Builder
	if err := writeEDIFACT(&b, root); err != nil {
		return nil, err
	}
	if b.Len() == 0 {
		return nil, fmt.Errorf("no EDIFACT segment in the XML: want elements named like BGM")
	}
	m[message.Body] = b.String()
	m[message.ContentType] = "application/edifact"
	return m, nil
}

// isSegmentTag reports whether name is the tag of a segment: three upper-case
// letters or digits, starting with a letter.
func isSegmentTag(name string) bool {
	if len(name) != 3 || name[0] < 'A' || name[0] > 'Z' {
		return false
	}
	for _, c := range []byte(name) {
		if !(c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

func writeEDIFACT(b *strings.Builder, e *xmlElem) error {
	tag := localName(e.name)
	if !isSegmentTag(tag) {
		for _, c := range e.children {
			if err := writeEDIFACT(b, c); err != nil {
				return err
			}
		}
		return nil
	}

	elements := make([]string, len(e.children))
	for i, el := range e.children {
		if len(el.children) == 0 {
			elements[i] = edifactEscaper.Replace(el.value)
			continue
		}
		components := make([]string, len(el.children))
		for j, c := range el.children {
			if len(c.children) > 0 {
				return fmt.Errorf("segment %s: element <%s>: component <%s> has children; EDIFACT has only elements and components", tag, el.name, c.name)
			}
			components[j] = edifactEscaper.Replace(c.value)
		}
		elements[i] = strings.Join(trimEmpty(components), ":")
	}
	b.WriteString(tag)
	for _, el := range trimEmpty(elements) {
		b.WriteString("+" + el)
	}
	b.WriteString("'")
	return nil
}

// trimEmpty returns list without its trailing empty values.
func trimEmpty(list []string) []string {
	for len(list) > 0 && list[len(list)-1] == "" {
		list = list[:len(list)-1]
	}
	return list
}
