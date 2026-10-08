package cli

import (
	"fmt"
	"path/filepath"
	"strings"
	"unicode"
)

// flowFilePath defaults extensionless flow filenames to DIL JSON files.
// Explicit extensions and empty arguments are preserved.
func flowFilePath(path string) string {
	if path != "" && filepath.Ext(path) == "" {
		return path + ".json"
	}
	return path
}

// utilityArguments supports quoted paths for development commands in the
// shell. Backslashes stay literal, including Windows directory separators.
func utilityArguments(line string) ([]string, error) {
	var args []string
	var word strings.Builder
	var quote rune
	started := false
	for _, ch := range line {
		switch {
		case quote != 0:
			if ch == quote {
				quote = 0
			} else {
				word.WriteRune(ch)
			}
		case ch == '\'' || ch == '"':
			quote, started = ch, true
		case unicode.IsSpace(ch):
			if started {
				args = append(args, word.String())
				word.Reset()
				started = false
			}
		default:
			word.WriteRune(ch)
			started = true
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("unclosed quote in arguments")
	}
	if started {
		args = append(args, word.String())
	}
	return args, nil
}
