package impl

import (
	"os"
	"path/filepath"
	"testing"

	"dif/message"
	stepdef "dif/steps/definition"
)

func TestFileEnrich(t *testing.T) {
	dir := t.TempDir()
	for name, content := range map[string]string{"a.txt": "first", "b.json": `{"b":1}`, "c.xml": "<c/>", ".hidden": "no", "latin1.txt": "caf\xe9"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	enrich := func(opts map[string]any) message.Message {
		t.Helper()
		m := message.New("old")
		m["keep"] = "yes"
		return process(t, "fileenrich:"+dir, opts, m)
	}

	if m := enrich(nil); m[message.Body] != "first" || m[FileName] != "a.txt" || m[message.ContentType] != "text/plain" || m["keep"] != "yes" {
		t.Errorf("first file: %v", m)
	}
	if m := enrich(map[string]any{"fileName": "b.json"}); m[message.Body] != `{"b":1}` || m[message.ContentType] != "application/json" {
		t.Errorf("fileName: %v", m)
	}
	if m := enrich(map[string]any{"include": `.*\.(xml|json)`, "exclude": `b\..*`}); m[message.Body] != "<c/>" {
		t.Errorf("include and exclude: %v", m)
	}
	if m := enrich(map[string]any{"fileName": "latin1.txt", "charset": "ISO-8859-1"}); m[message.Body] != "café" {
		t.Errorf("charset: %q", m[message.Body])
	}
	if m := enrich(map[string]any{"fileName": "c.xml", "binary": true}); string(m[message.Body].([]byte)) != "<c/>" {
		t.Errorf("binary: %#v", m[message.Body])
	}
	if m := enrich(map[string]any{"fileName": "none.txt"}); m[message.Body] != "old" || m[FileName] != nil {
		t.Errorf("no file: %v, want the message unchanged", m)
	}
	if m := process(t, "fileenrich:"+filepath.Join(dir, "missing"), nil, message.New("old")); m[message.Body] != "old" {
		t.Errorf("missing directory: %v", m)
	}

	// The file stays, unless delete is set.
	enrich(map[string]any{"fileName": "a.txt"})
	if _, err := os.Stat(filepath.Join(dir, "a.txt")); err != nil {
		t.Errorf("a.txt is gone without delete: %v", err)
	}
	enrich(map[string]any{"fileName": "a.txt", "delete": true})
	if _, err := os.Stat(filepath.Join(dir, "a.txt")); !os.IsNotExist(err) {
		t.Errorf("a.txt stays with delete: %v", err)
	}

	wantInvalid(t, stepdef.Action, "fileenrich:"+dir, map[string]any{"charset": "EBCDIC"}, `option charset: charset "EBCDIC" is not supported`)
	wantInvalid(t, stepdef.Action, "fileenrich:"+dir, map[string]any{"include": "("}, "option include")
}
