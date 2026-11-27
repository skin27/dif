// Package dilsyntax is the text syntax of DIL: .dil files. It is a second
// representation of the DIL model, next to DIL JSON. Text is parsed to an
// AST, and the AST is lowered to DIL JSON; from there DIF reads a document
// as it reads any DIL JSON, so the engine and the flow model know nothing of
// the text. Print does the reverse: it writes DIL JSON as text.
//
// docs/dil-language.md describes the language: its grammar and how each
// construct is lowered.
package dilsyntax

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
)

// Ext is the extension of DIL text files.
const Ext = ".dil"

// IsTextPath reports whether path names a DIL text file: it ends in .dil.
func IsTextPath(path string) bool { return strings.EqualFold(filepath.Ext(path), Ext) }

// IsText reports whether data is DIL text rather than DIL JSON: it is no JSON
// and starts with a comment or a word. A DIL JSON document starts with '{';
// DIL text never does.
func IsText(data []byte) bool {
	s := bytes.TrimLeft(bytes.TrimPrefix(data, []byte("\xef\xbb\xbf")), " \t\r\n")
	if len(s) == 0 {
		return false
	}
	c := s[0]
	if c != '#' && !('a' <= c && c <= 'z' || 'A' <= c && c <= 'Z') {
		return false
	}
	return !json.Valid(data)
}

// Compile parses DIL text and lowers it to DIL JSON. name labels errors, as
// in "orders.dil:12:5: expected '->' after ..."; errors never show the text
// of a string or a word, which can hold a credential.
func Compile(name string, src []byte) ([]byte, error) {
	f, err := Parse(name, src)
	if err != nil {
		return nil, err
	}
	return Lower(name, f)
}

// Document returns the DIL JSON of a document read from path (empty for bytes
// that come without a file): DIL text, compiled, when path ends in .dil or
// the data is text (see IsText); otherwise the data itself. name labels
// errors.
func Document(name, path string, data []byte) ([]byte, error) {
	if IsTextPath(path) || IsText(data) {
		return Compile(name, data)
	}
	return data, nil
}
