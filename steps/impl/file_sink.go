package impl

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"

	"dif/message"
	stepdef "dif/steps/definition"
)

// fileSink writes the message body to a file. Headers, metadata included, are
// never written. The file name is the fileName option, else the FileName
// header, else the trace id.
type fileSink struct {
	dir        string
	fileName   string
	autoCreate bool
	fileExist  string // Override, Append, Fail or Ignore

	mu sync.Mutex // serializes writes, so concurrent appends don't interleave
}

func newFileSink(_ string, p stepdef.Params) (stepdef.Processor, error) {
	s := &fileSink{
		dir:        p["path"].(string),
		autoCreate: p["autoCreate"].(bool),
		fileExist:  p["fileExist"].(string),
	}
	s.fileName, _ = p["fileName"].(string)
	if s.dir == "" {
		return nil, fmt.Errorf("directory is empty")
	}
	return s, nil
}

func (s *fileSink) Consume(_ context.Context, m message.Message) error {
	name := s.fileName
	if name == "" {
		name, _ = m[FileName].(string)
	}
	if name == "" {
		name, _ = m[message.TraceID].(string)
	}
	if name == "" {
		return fmt.Errorf("no file name: set the fileName option or the %s header", FileName)
	}
	path := filepath.Join(s.dir, filepath.FromSlash(name))

	var data []byte
	switch b := m[message.Body].(type) {
	case []byte:
		data = b
	default:
		data = []byte(text(b))
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if s.autoCreate {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
	}

	flag := os.O_WRONLY | os.O_CREATE
	switch s.fileExist {
	case "Override":
		flag |= os.O_TRUNC
	case "Append":
		flag |= os.O_APPEND
	case "Fail", "Ignore":
		flag |= os.O_EXCL
	}
	f, err := os.OpenFile(path, flag, 0o644)
	if errors.Is(err, fs.ErrExist) {
		if s.fileExist == "Ignore" {
			return nil
		}
		return fmt.Errorf("file %s already exists", path)
	}
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}
