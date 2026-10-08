package impl

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"

	"dif/message"
	stepdef "dif/steps/definition"
)

// unzipAction extracts the file of a zip archive in the body: its content
// becomes the body and its name the FileName header. An archive with more
// than one file is rejected, as one message cannot hold several files (that
// needs a splitter). Content-Type is set by the file's extension, or removed
// when the extension is unknown.
type unzipAction struct{}

func newUnzipAction(string, stepdef.Params) (stepdef.Processor, error) {
	return unzipAction{}, nil
}

func (unzipAction) Process(_ context.Context, m message.Message) (message.Message, error) {
	data := bytesOf(m[message.Body])
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, fmt.Errorf("body is not a zip archive: %w", err)
	}

	var file *zip.File
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		if file != nil {
			return nil, fmt.Errorf("zip archive holds more than one file (%s, %s); unzip supports one", file.Name, f.Name)
		}
		file = f
	}
	if file == nil {
		return nil, fmt.Errorf("zip archive holds no file")
	}

	r, err := file.Open()
	if err != nil {
		return nil, fmt.Errorf("zip file %s: %w", file.Name, err)
	}
	defer r.Close()
	content, err := io.ReadAll(io.LimitReader(r, maxBodySize+1))
	if err != nil {
		return nil, fmt.Errorf("zip file %s: %w", file.Name, err)
	}
	if len(content) > maxBodySize {
		return nil, fmt.Errorf("zip file %s is larger than %d bytes", file.Name, maxBodySize)
	}

	m[message.Body] = content
	m[FileName] = file.Name
	setContentType(m, file.Name)
	return m, nil
}
