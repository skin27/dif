package impl

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"dif/message"
	stepdef "dif/steps/definition"
)

// formToXMLAction converts an application/x-www-form-urlencoded body into
// XML: a form element holding an element per field, in the order of the body.
// A field that is repeated gives repeated elements; a name an XML name cannot
// hold has its invalid characters replaced by _.
//
//	first-name=Joe&age=21  ->  <form><first-name>Joe</first-name><age>21</age></form>
type formToXMLAction struct{}

func newFormToXMLAction(string, stepdef.Params) (stepdef.Processor, error) {
	return formToXMLAction{}, nil
}

func (formToXMLAction) Process(_ context.Context, m message.Message) (message.Message, error) {
	var b strings.Builder
	b.WriteString("<form>")
	for _, pair := range strings.Split(strings.TrimSpace(text(m[message.Body])), "&") {
		if pair == "" {
			continue
		}
		rawName, rawValue, _ := strings.Cut(pair, "=")
		name, err := url.QueryUnescape(rawName)
		if err == nil && name == "" {
			err = fmt.Errorf("field with no name")
		}
		var value string
		if err == nil {
			value, err = url.QueryUnescape(rawValue)
		}
		if err != nil {
			return nil, fmt.Errorf("body is not form data: %q: %w", pair, err)
		}
		tag := sanitizeXMLName(name)
		if value == "" {
			b.WriteString("<" + tag + "/>")
		} else {
			b.WriteString("<" + tag + ">" + xmlTextEscaper.Replace(value) + "</" + tag + ">")
		}
	}
	b.WriteString("</form>")
	m[message.Body] = b.String()
	m[message.ContentType] = "application/xml"
	return m, nil
}
