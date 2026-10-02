package impl

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"dif/message"
	stepdef "dif/steps/definition"
)

// FileName is the header holding a file's path relative to its directory.
// The file source sets it; the file sink uses it when no fileName is configured.
const FileName = "file.name"

// doneDir is the subdirectory consumed files are moved to, unless they are deleted.
const doneDir = ".done"

// fileSource polls a directory and emits a message per file, with the file's
// content as body. Files and directories whose name starts with a dot are
// skipped. A consumed file is deleted or moved to doneDir.
type fileSource struct {
	dir          string
	fileName     string // consume only files with this name; "" for all
	autoCreate   bool
	recursive    bool
	delete       bool
	initialDelay time.Duration
	delay        time.Duration
}

func newFileSource(_ string, p stepdef.Params) (stepdef.Processor, error) {
	s := fileSource{
		dir:          p["path"].(string),
		autoCreate:   p["autoCreate"].(bool),
		recursive:    p["recursive"].(bool),
		delete:       p["delete"].(bool),
		initialDelay: time.Duration(p["initialDelay"].(int)) * time.Millisecond,
		delay:        time.Duration(p["delay"].(int)) * time.Millisecond,
	}
	s.fileName, _ = p["fileName"].(string)
	if s.dir == "" {
		return nil, fmt.Errorf("directory is empty")
	}
	return s, nil
}

func (s fileSource) Run(ctx context.Context, emit stepdef.Emit) error {
	if s.autoCreate {
		if err := os.MkdirAll(s.dir, 0o755); err != nil {
			return err
		}
	}

	for wait := s.initialDelay; ; wait = s.delay {
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(wait):
		}

		files, err := s.list()
		if err != nil {
			return err
		}
		for _, rel := range files {
			if ctx.Err() != nil {
				return nil
			}
			path := filepath.Join(s.dir, rel)
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			m := message.New(string(data))
			m[FileName] = filepath.ToSlash(rel)
			if emit(m, nil) != nil {
				return nil // flow is stopping; the file stays for the next run
			}
			if err := s.consumed(path, rel); err != nil {
				return err
			}
		}
	}
}

// list returns the paths, relative to dir, of the files to consume, sorted.
func (s fileSource) list() ([]string, error) {
	var files []string
	match := func(name string) bool {
		return !strings.HasPrefix(name, ".") && (s.fileName == "" || name == s.fileName)
	}

	if !s.recursive {
		entries, err := os.ReadDir(s.dir)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if e.Type().IsRegular() && match(e.Name()) {
				files = append(files, e.Name())
			}
		}
		return files, nil
	}

	err := filepath.WalkDir(s.dir, func(path string, d fs.DirEntry, err error) error {
		switch {
		case err != nil:
			return err
		case d.IsDir():
			if path != s.dir && strings.HasPrefix(d.Name(), ".") {
				return filepath.SkipDir
			}
		case d.Type().IsRegular() && match(d.Name()):
			rel, err := filepath.Rel(s.dir, path)
			if err != nil {
				return err
			}
			files = append(files, rel)
		}
		return nil
	})
	return files, err
}

// consumed deletes the file or moves it to doneDir.
func (s fileSource) consumed(path, rel string) error {
	if s.delete {
		return os.Remove(path)
	}
	done := filepath.Join(s.dir, doneDir, rel)
	if err := os.MkdirAll(filepath.Dir(done), 0o755); err != nil {
		return err
	}
	return os.Rename(path, done)
}
