package api

import (
	"os"

	"dif/internal/dilsyntax"
)

// readDocument reads a DIL document from path. A .dil file is DIL text, which
// is compiled to DIL JSON; errors in it name the file, line and column.
func readDocument(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return dilsyntax.Document(path, path, data)
}
