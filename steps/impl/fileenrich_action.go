package impl

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"dif/message"
	stepdef "dif/steps/definition"
)

// fileEnrichAction replaces the body with the content of a file in a
// directory (a content enricher that polls, as Camel's pollEnrich): the first
// file, in name order, that fileName, include and exclude select. With no such
// file the message passes on unchanged. The file is left in place, unless
// delete is set. A directory that does not exist holds no files.
type fileEnrichAction struct {
	files            fileSource // dir, fileName, recursive and delete
	include, exclude *regexp.Regexp
	binary           bool
	charset          charset
}

func newFileEnrichAction(_ string, p stepdef.Params) (stepdef.Processor, error) {
	a := fileEnrichAction{
		files: fileSource{
			dir:       p["path"].(string),
			fileName:  p["fileName"].(string),
			recursive: p["recursive"].(bool),
			delete:    p["delete"].(bool),
		},
		binary: p["binary"].(bool),
	}
	if a.files.dir == "" {
		return nil, fmt.Errorf("directory is empty")
	}
	var err error
	if a.charset, err = parseCharset(p["charset"].(string)); err != nil {
		return nil, fmt.Errorf("option charset: %w", err)
	}
	for opt, re := range map[string]**regexp.Regexp{"include": &a.include, "exclude": &a.exclude} {
		if s := p[opt].(string); s != "" {
			if *re, err = regexp.Compile("^(?:" + s + ")$"); err != nil {
				return nil, fmt.Errorf("option %s: %w", opt, err)
			}
		}
	}
	return a, nil
}

func (a fileEnrichAction) Process(_ context.Context, m message.Message) (message.Message, error) {
	files, err := a.files.list()
	if os.IsNotExist(err) {
		return m, nil // nothing to enrich with
	}
	if err != nil {
		return nil, err
	}
	for _, rel := range files {
		name := filepath.Base(rel)
		if (a.include != nil && !a.include.MatchString(name)) || (a.exclude != nil && a.exclude.MatchString(name)) {
			continue
		}
		path := filepath.Join(a.files.dir, rel)
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		if a.binary {
			m[message.Body] = data
		} else {
			m[message.Body] = a.charset.text(data)
		}
		m[FileName] = filepath.ToSlash(rel)
		setContentType(m, rel)
		if a.files.delete {
			if err := os.Remove(path); err != nil {
				return nil, err
			}
		}
		return m, nil
	}
	return m, nil
}
