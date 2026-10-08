package sanitize

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// node is a JSON value with its byte range in the source, so a string can be
// replaced in place and the rest of the file stays byte for byte as it was.
type node struct {
	kind       byte // 'o' object, 'a' array, 's' string, 'v' any other scalar
	start, end int  // the value is src[start:end]
	str        string
	fields     []field
	items      []*node
}

type field struct {
	key string
	val *node
}

type scanner struct {
	b []byte
	i int
}

// parseJSON parses src, which must be valid JSON, optionally after a UTF-8 BOM.
func parseJSON(src []byte) (*node, error) {
	s := &scanner{b: src}
	if bytes.HasPrefix(src, []byte{0xEF, 0xBB, 0xBF}) {
		s.i = 3
	}
	n, err := s.value()
	if err != nil {
		return nil, err
	}
	s.skip()
	if s.i != len(s.b) {
		return nil, fmt.Errorf("offset %d: unexpected data after the JSON value", s.i)
	}
	return n, nil
}

func (s *scanner) skip() {
	for s.i < len(s.b) {
		switch s.b[s.i] {
		case ' ', '\t', '\r', '\n':
			s.i++
		default:
			return
		}
	}
}

func (s *scanner) value() (*node, error) {
	s.skip()
	if s.i >= len(s.b) {
		return nil, fmt.Errorf("offset %d: unexpected end of JSON", s.i)
	}
	switch s.b[s.i] {
	case '{':
		return s.object()
	case '[':
		return s.array()
	case '"':
		return s.str()
	}
	start := s.i
	for s.i < len(s.b) && !bytes.ContainsRune([]byte(" \t\r\n,]}"), rune(s.b[s.i])) {
		s.i++
	}
	if s.i == start {
		return nil, fmt.Errorf("offset %d: unexpected character %q", s.i, s.b[s.i])
	}
	return &node{kind: 'v', start: start, end: s.i}, nil
}

func (s *scanner) str() (*node, error) {
	start := s.i
	s.i++ // opening quote
	for s.i < len(s.b) {
		switch s.b[s.i] {
		case '\\':
			s.i += 2
			continue
		case '"':
			s.i++
			n := &node{kind: 's', start: start, end: s.i}
			if err := json.Unmarshal(s.b[start:s.i], &n.str); err != nil {
				return nil, fmt.Errorf("offset %d: %w", start, err)
			}
			return n, nil
		}
		s.i++
	}
	return nil, fmt.Errorf("offset %d: unterminated string", start)
}

func (s *scanner) object() (*node, error) {
	n := &node{kind: 'o', start: s.i}
	s.i++ // {
	for {
		s.skip()
		if s.i >= len(s.b) {
			return nil, fmt.Errorf("offset %d: unterminated object", n.start)
		}
		if s.b[s.i] == '}' {
			s.i++
			n.end = s.i
			return n, nil
		}
		if s.b[s.i] == ',' {
			s.i++
			continue
		}
		if s.b[s.i] != '"' {
			return nil, fmt.Errorf("offset %d: want an object key", s.i)
		}
		k, err := s.str()
		if err != nil {
			return nil, err
		}
		s.skip()
		if s.i >= len(s.b) || s.b[s.i] != ':' {
			return nil, fmt.Errorf("offset %d: want ':'", s.i)
		}
		s.i++
		v, err := s.value()
		if err != nil {
			return nil, err
		}
		n.fields = append(n.fields, field{k.str, v})
	}
}

func (s *scanner) array() (*node, error) {
	n := &node{kind: 'a', start: s.i}
	s.i++ // [
	for {
		s.skip()
		if s.i >= len(s.b) {
			return nil, fmt.Errorf("offset %d: unterminated array", n.start)
		}
		if s.b[s.i] == ']' {
			s.i++
			n.end = s.i
			return n, nil
		}
		if s.b[s.i] == ',' {
			s.i++
			continue
		}
		v, err := s.value()
		if err != nil {
			return nil, err
		}
		n.items = append(n.items, v)
	}
}

// get returns the string value of the field, if the object has one.
func (n *node) get(key string) (*node, bool) {
	for _, f := range n.fields {
		if f.key == key && f.val.kind == 's' {
			return f.val, true
		}
	}
	return nil, false
}
