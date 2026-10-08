package impl

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"mime"
	"mime/multipart"
	"net/textproto"
	"strings"

	"dif/message"
	stepdef "dif/steps/definition"
)

// multipartAction wraps the body in a multipart/form-data body, to post it
// as a form: the part fname holds the body (as a file named after file.name,
// if set, with the body's Content-Type), followed by a text part per member
// of formFields. Content-Type becomes multipart/form-data with the boundary.
type multipartAction struct {
	field  string
	fields [][2]string // name, value, in the order given
}

func newMultipartAction(_ string, p stepdef.Params) (stepdef.Processor, error) {
	a := multipartAction{field: p["fname"].(string)}
	if a.field == "" {
		return nil, fmt.Errorf("option fname: empty field name")
	}
	if s := strings.TrimSpace(p["formFields"].(string)); s != "" {
		d := json.NewDecoder(strings.NewReader(s))
		if t, err := d.Token(); err != nil || t != json.Delim('{') {
			return nil, fmt.Errorf("option formFields: want a JSON object of text fields")
		}
		for d.More() {
			k, err := d.Token()
			if err != nil {
				return nil, fmt.Errorf("option formFields: %w", err)
			}
			var v any
			if err := d.Decode(&v); err != nil {
				return nil, fmt.Errorf("option formFields: %w", err)
			}
			a.fields = append(a.fields, [2]string{k.(string), text(v)})
		}
	}
	return a, nil
}

func (a multipartAction) Process(_ context.Context, m message.Message) (message.Message, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)

	h := textproto.MIMEHeader{}
	disposition := map[string]string{"name": a.field}
	if name, _ := m[FileName].(string); name != "" {
		disposition["filename"] = name
	}
	h.Set("Content-Disposition", mime.FormatMediaType("form-data", disposition))
	ct, _ := m[message.ContentType].(string)
	if ct == "" || strings.HasPrefix(ct, "multipart/") {
		ct = "application/octet-stream"
	}
	h.Set("Content-Type", ct)
	part, err := w.CreatePart(h)
	if err != nil {
		return nil, err
	}
	part.Write(bytesOf(m[message.Body]))

	for _, f := range a.fields {
		if err := w.WriteField(f[0], f[1]); err != nil {
			return nil, err
		}
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	m[message.Body] = buf.Bytes()
	m[message.ContentType] = w.FormDataContentType()
	return m, nil
}
