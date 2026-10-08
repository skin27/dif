package impl

import (
	"encoding/json"
	"fmt"
	"strings"
)

// The simple language is compiled in simple_compile.go and the files after
// it. This file holds what a message value looks like as text.

// isTypeName reports whether s looks like a Java type name, such as String,
// java.lang.Integer or byte[].
func isTypeName(s string) bool {
	return s != "" && strings.Trim(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_.$[]") == ""
}

// text renders a message value as text; JSON values are rendered as JSON.
func text(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case []byte:
		return string(x)
	case map[string]any, []any:
		if b, err := json.Marshal(x); err == nil {
			return string(b)
		}
	}
	return fmt.Sprint(v)
}

// bytesOf renders a message value as bytes: []byte as is, anything else as text.
func bytesOf(v any) []byte {
	if b, ok := v.([]byte); ok {
		return b
	}
	return []byte(text(v))
}
