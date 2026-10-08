package impl

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"dif/message"
	stepdef "dif/steps/definition"
)

// EDI here is the generic, delimiter-based format: segments of fields, a
// field of components, a component of sub-components, each level split by
// its own delimiter. Its XML form names every segment after its first field
// and numbers the rest:
//
//	<edi-message>
//	  <delimiters segment="LB" field="~" component="^" sub-component="!"/>
//	  <CUS>
//	    <field.1><component.1>John</component.1><component.2>Doe</component.2></field.1>
//	    <field.2>1901-01-07</field.2>
//	  </CUS>
//	</edi-message>
//
// The segment delimiter LB is a line break (\n or \r\n).

// ediDelimiters are the delimiters of an EDI message, from the segment
// level down.
type ediDelimiters struct{ segment, field, component, sub string }

func (d ediDelimiters) check() error {
	seen := map[string]string{}
	for _, x := range []struct{ name, value string }{{"segment", d.segment}, {"field", d.field}, {"component", d.component}, {"subComponent", d.sub}} {
		if x.value == "" {
			return fmt.Errorf("option %s: empty delimiter", x.name)
		}
		if other, dup := seen[x.value]; dup {
			return fmt.Errorf("options %s and %s: same delimiter %q", other, x.name, x.value)
		}
		seen[x.value] = x.name
	}
	return nil
}

// ediToXMLAction converts an EDI body into its XML form.
type ediToXMLAction struct{ d ediDelimiters }

func newEDIToXMLAction(_ string, p stepdef.Params) (stepdef.Processor, error) {
	d := ediDelimiters{p["segment"].(string), p["field"].(string), p["component"].(string), p["subComponent"].(string)}
	if err := d.check(); err != nil {
		return nil, err
	}
	return ediToXMLAction{d}, nil
}

func (a ediToXMLAction) Process(_ context.Context, m message.Message) (message.Message, error) {
	var b strings.Builder
	fmt.Fprintf(&b, `<edi-message><delimiters segment="%s" field="%s" component="%s" sub-component="%s"/>`,
		xmlAttrEscaper.Replace(a.d.segment), xmlAttrEscaper.Replace(a.d.field), xmlAttrEscaper.Replace(a.d.component), xmlAttrEscaper.Replace(a.d.sub))

	for i, seg := range a.segments(text(m[message.Body])) {
		fields := strings.Split(seg, a.d.field)
		name := strings.TrimSpace(fields[0])
		if !isXMLName(name) {
			return nil, fmt.Errorf("segment %d: name %q is not an XML name", i+1, name)
		}
		b.WriteString("<" + name + ">")
		for j, f := range fields[1:] {
			a.writeLevel(&b, "field."+strconv.Itoa(j+1), f, []string{a.d.component, a.d.sub}, []string{"component", "sub-component"})
		}
		b.WriteString("</" + name + ">")
	}
	b.WriteString("</edi-message>")
	m[message.Body] = b.String()
	m[message.ContentType] = "application/xml"
	return m, nil
}

// segments splits body into its non-empty segments.
func (a ediToXMLAction) segments(body string) []string {
	var parts []string
	if a.d.segment == "LB" {
		parts = strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n")
	} else {
		parts = strings.Split(body, a.d.segment)
	}
	segs := parts[:0]
	for _, s := range parts {
		if strings.TrimSpace(s) != "" {
			segs = append(segs, strings.TrimLeft(s, "\r\n"))
		}
	}
	return segs
}

// writeLevel writes value as element name: as text, or, when it holds the
// first of delims, as numbered children named after the first of names.
func (a ediToXMLAction) writeLevel(b *strings.Builder, name, value string, delims, names []string) {
	b.WriteString("<" + name + ">")
	if len(delims) > 0 && strings.Contains(value, delims[0]) {
		for i, part := range strings.Split(value, delims[0]) {
			a.writeLevel(b, names[0]+"."+strconv.Itoa(i+1), part, delims[1:], names[1:])
		}
	} else {
		b.WriteString(xmlTextEscaper.Replace(value))
	}
	b.WriteString("</" + name + ">")
}

// xmlToEDIAction converts the XML form back into EDI, with the delimiters
// of its delimiters element (else the editoxml defaults: LB ~ ^ !).
type xmlToEDIAction struct{}

func newXMLToEDIAction(string, stepdef.Params) (stepdef.Processor, error) {
	return xmlToEDIAction{}, nil
}

func (xmlToEDIAction) Process(_ context.Context, m message.Message) (message.Message, error) {
	root, err := parseXMLTree(bytesOf(m[message.Body]))
	if err != nil {
		return nil, err
	}
	d := ediDelimiters{"LB", "~", "^", "!"}
	var segs []string
	for _, e := range root.children {
		if localName(e.name) == "delimiters" {
			for _, at := range e.attrs {
				switch at.name {
				case "segment":
					d.segment = at.value
				case "field":
					d.field = at.value
				case "component":
					d.component = at.value
				case "sub-component":
					d.sub = at.value
				}
			}
			continue
		}
		fields := []string{localName(e.name)}
		for _, f := range e.children {
			n, err := ediIndex(f.name, "field.")
			if err != nil {
				return nil, fmt.Errorf("segment %s: %w", fields[0], err)
			}
			for len(fields) <= n {
				fields = append(fields, "")
			}
			if fields[n], err = ediJoin(f, []string{d.component, d.sub}, []string{"component.", "sub-component."}); err != nil {
				return nil, fmt.Errorf("segment %s: %w", fields[0], err)
			}
		}
		segs = append(segs, strings.Join(fields, d.field))
	}

	var out string
	if d.segment == "LB" {
		out = strings.Join(segs, "\n")
	} else if len(segs) > 0 {
		out = strings.Join(segs, d.segment) + d.segment
	}
	m[message.Body] = out
	m[message.ContentType] = "text/plain"
	return m, nil
}

// ediJoin returns the EDI value of element e: its text, or its numbered
// children (named after the first of prefixes) joined by the first of delims.
func ediJoin(e *xmlElem, delims, prefixes []string) (string, error) {
	if len(e.children) == 0 || len(delims) == 0 {
		return e.value, nil
	}
	var parts []string
	for _, c := range e.children {
		n, err := ediIndex(c.name, prefixes[0])
		if err != nil {
			return "", err
		}
		for len(parts) < n {
			parts = append(parts, "")
		}
		if parts[n-1], err = ediJoin(c, delims[1:], prefixes[1:]); err != nil {
			return "", err
		}
	}
	return strings.Join(parts, delims[0]), nil
}

// ediIndex returns n of an element named <prefix>n, n >= 1.
func ediIndex(name, prefix string) (int, error) {
	s, ok := strings.CutPrefix(localName(name), prefix)
	n, err := strconv.Atoi(s)
	if !ok || err != nil || n < 1 || n > 10000 {
		return 0, fmt.Errorf("element <%s>: want <%sN>", name, prefix)
	}
	return n, nil
}
