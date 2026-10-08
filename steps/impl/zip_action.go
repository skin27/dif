package impl

import (
	"archive/zip"
	"bytes"
	"context"

	"dif/message"
	stepdef "dif/steps/definition"
)

// zipAction compresses the body into a zip archive holding one file, named
// after the FileName header, else the trace id. The archive's name, with
// ".zip" added, becomes the FileName header; Content-Type is application/zip.
type zipAction struct{}

func newZipAction(string, stepdef.Params) (stepdef.Processor, error) {
	return zipAction{}, nil
}

func (zipAction) Process(_ context.Context, m message.Message) (message.Message, error) {
	name, _ := m[FileName].(string)
	if name == "" {
		name = text(m[message.TraceID])
	}

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	w, err := zw.Create(name)
	if err != nil {
		return nil, err
	}
	if _, err := w.Write(bytesOf(m[message.Body])); err != nil {
		return nil, err
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}

	m[message.Body] = buf.Bytes()
	m[FileName] = name + ".zip"
	m[message.ContentType] = "application/zip"
	return m, nil
}
