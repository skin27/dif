package impl

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"

	"dif/message"
	stepdef "dif/steps/definition"
)

// enrichRouter enriches the message (content enricher): a copy goes along
// the link with rule "enrich", what comes out is merged into the message, and
// the message continues along the other link. enrichType decides the merge:
//
//   - override: the enrich route's outcome (body and headers) replaces the message
//   - xml: the outcome's root element is appended to the message's root element
//   - json: the members of the outcome's object are set in the message's object
//
// For xml and json the message keeps its own headers. When the enrich route
// fails, the message fails with that error (useErrorRoute, so the flow's
// error route can take it); otherwise it continues unenriched and the error
// is logged.
type enrichRouter struct {
	id                string
	enrich, main      int
	merge             func(m, enrichment message.Message) (message.Message, error)
	failOnEnrichError bool
}

func newEnrichRouter(id string, p stepdef.Params) (stepdef.Processor, error) {
	r := enrichRouter{id: id, enrich: -1, main: -1, failOnEnrichError: p["useErrorRoute"].(bool)}
	for i, l := range p[stepdef.Links].([]stepdef.Link) {
		target := &r.main
		if l.Rule == "enrich" {
			target = &r.enrich
		}
		if *target >= 0 {
			return nil, fmt.Errorf("needs one outbound link with rule enrich and one without, has more")
		}
		*target = i
	}
	if r.enrich < 0 || r.main < 0 {
		return nil, fmt.Errorf("needs one outbound link with rule enrich and one without")
	}
	kind := p["enrichType"]
	for _, older := range []string{"enrichFileType", "enrichMethod"} { // the last one set wins
		if v, ok := p[older]; ok {
			kind = v
		}
	}
	switch kind {
	case "override":
		r.merge = func(_, e message.Message) (message.Message, error) { return e, nil }
	case "xml":
		r.merge = mergeBody(appendXMLRoot)
	case "json":
		r.merge = mergeBody(mergeJSONObjects)
	}
	return r, nil
}

func (r enrichRouter) Route(_ context.Context, m message.Message) ([]stepdef.Route, error) {
	return []stepdef.Route{{Next: r.enrich, Message: m.Copy()}}, nil
}

func (r enrichRouter) Gather(ctx context.Context, m message.Message, outcomes []stepdef.Outcome) ([]stepdef.Route, error) {
	o := outcomes[0]
	if o.Err != nil {
		if r.failOnEnrichError {
			return nil, o.Err
		}
		stepdef.Logger(ctx).Printf("step %s: enrichment failed, the message continues without it: %v", r.id, o.Err)
		return []stepdef.Route{{Next: r.main, Message: m}}, nil
	}
	merged, err := r.merge(m, o.Message)
	if err != nil {
		return nil, fmt.Errorf("enrichment: %w", err)
	}
	return []stepdef.Route{{Next: r.main, Message: merged}}, nil
}

// mergeBody returns a merge that sets the message's body to merge of its body
// and the enrichment's.
func mergeBody(merge func(body, enrichment []byte) (string, error)) func(m, e message.Message) (message.Message, error) {
	return func(m, e message.Message) (message.Message, error) {
		body, err := merge(bytesOf(m[message.Body]), bytesOf(e[message.Body]))
		if err != nil {
			return nil, err
		}
		m[message.Body] = body
		return m, nil
	}
}

// appendXMLRoot returns the XML document doc with the root element of the
// XML document child appended to its root element's content. Both documents
// stay as written; only child's declaration and surroundings are left out.
func appendXMLRoot(doc, child []byte) (string, error) {
	if _, err := parseXMLTree(doc); err != nil { // checks well-formedness; rootSpan does not
		return "", err
	}
	if _, err := parseXMLTree(child); err != nil {
		return "", fmt.Errorf("enrichment %w", err)
	}
	start, end, contentEnd, name := rootSpan(doc)
	cStart, cEnd, _, _ := rootSpan(child)
	element := child[cStart:cEnd]

	var b bytes.Buffer
	if contentEnd < 0 { // <root/>: open it up as <root>element</root>
		b.Write(doc[:start])
		b.Write(bytes.TrimRight(doc[start:end-2], " \t\r\n")) // the tag without its />
		b.WriteString(">")
		b.Write(element)
		b.WriteString("</" + name + ">")
		b.Write(doc[end:])
		return b.String(), nil
	}
	b.Write(doc[:contentEnd])
	b.Write(element)
	b.Write(doc[contentEnd:])
	return b.String(), nil
}

// rootSpan returns the byte offsets of the root element of the well-formed
// XML document data: where it starts and ends, where its content ends (the
// start of its end tag; -1 if it is empty, as in <root/>), and its name as
// written.
func rootSpan(data []byte) (start, end, contentEnd int, name string) {
	d := xml.NewDecoder(bytes.NewReader(data))
	depth := 0
	for {
		before := int(d.InputOffset())
		tok, err := d.RawToken()
		if err == io.EOF || err != nil {
			return start, end, contentEnd, name
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if depth == 0 {
				start, name = before, qualifiedName(t.Name)
			}
			depth++
		case xml.EndElement:
			depth--
			if depth == 0 {
				end, contentEnd = int(d.InputOffset()), before
				if end == before { // <root/>: start and end come from one tag
					contentEnd = -1
				}
				return start, end, contentEnd, name
			}
		}
	}
}

// mergeJSONObjects returns the JSON object doc with the members of the JSON
// object enrichment set in it: a member doc has already gets the
// enrichment's value in place, a new one is added at the end.
func mergeJSONObjects(doc, enrichment []byte) (string, error) {
	d, err := readJSON(doc)
	if err != nil {
		return "", err
	}
	e, err := readJSON(enrichment)
	if err != nil {
		return "", fmt.Errorf("enrichment %w", err)
	}
	obj, ok1 := d.(jsonObject)
	add, ok2 := e.(jsonObject)
	if !ok1 || !ok2 {
		return "", fmt.Errorf("json enrichment needs a JSON object in the body and in the enrichment")
	}
next:
	for _, m := range add {
		for i := range obj {
			if obj[i].key == m.key {
				obj[i].value = m.value
				continue next
			}
		}
		obj = append(obj, m)
	}
	var b bytes.Buffer
	writeJSON(&b, obj)
	return b.String(), nil
}
