package mapping

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"strings"
	"unicode/utf8"
)

func readJSON(data []byte, b *budget) (any, error) {
	if len(data) > b.limits.Bytes {
		return nil, fmt.Errorf("input byte limit exceeded")
	}
	if !utf8.Valid(data) {
		return nil, fmt.Errorf("input is not UTF-8")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.UseNumber()
	v, e := jsonValue(d, b, 0)
	if e != nil {
		return nil, e
	}
	if _, e = d.Token(); e != io.EOF {
		return nil, fmt.Errorf("unexpected trailing JSON")
	}
	return v, nil
}
func jsonValue(d *json.Decoder, b *budget, depth int) (any, error) {
	if e := b.node(depth); e != nil {
		return nil, e
	}
	t, e := d.Token()
	if e != nil {
		return nil, fmt.Errorf("invalid JSON at byte %d", d.InputOffset())
	}
	switch v := t.(type) {
	case json.Delim:
		switch v {
		case '{':
			o := object{}
			seen := map[string]bool{}
			for d.More() {
				k, e := d.Token()
				if e != nil {
					return nil, fmt.Errorf("invalid JSON object")
				}
				name, ok := k.(string)
				if !ok || seen[name] {
					return nil, fmt.Errorf("invalid or duplicate JSON key")
				}
				seen[name] = true
				x, e := jsonValue(d, b, depth+1)
				if e != nil {
					return nil, e
				}
				o = append(o, member{name, x})
			}
			end, e := d.Token()
			if e != nil || end != json.Delim('}') {
				return nil, fmt.Errorf("invalid JSON object")
			}
			return o, nil
		case '[':
			a := []any{}
			for d.More() {
				v, e := jsonValue(d, b, depth+1)
				if e != nil {
					return nil, e
				}
				a = append(a, v)
			}
			end, e := d.Token()
			if e != nil || end != json.Delim(']') {
				return nil, fmt.Errorf("invalid JSON array")
			}
			return a, nil
		}
		return nil, fmt.Errorf("invalid JSON delimiter")
	case json.Number:
		return number(string(v), b.limits)
	default:
		return v, nil
	}
}

type output struct {
	s strings.Builder
	b *budget
}

func (w *output) put(s string) error {
	if e := w.b.tick(); e != nil {
		return e
	}
	if w.s.Len()+len(s) > w.b.limits.Bytes {
		return fmt.Errorf("output byte limit exceeded")
	}
	w.s.WriteString(s)
	return nil
}
func (w *output) json(v any, depth int) error {
	if e := w.b.node(depth); e != nil {
		return e
	}
	switch x := v.(type) {
	case headerValues:
		o, e := x.materialize(w.b)
		if e != nil {
			return e
		}
		return w.json(o, depth)
	case nil:
		return w.put("null")
	case bool:
		if x {
			return w.put("true")
		}
		return w.put("false")
	case string:
		bs, _ := json.Marshal(x)
		return w.put(string(bs))
	case *big.Rat:
		s, e := decimal(x, w.b.limits)
		if e != nil {
			return e
		}
		return w.put(s)
	case object:
		if e := w.put("{"); e != nil {
			return e
		}
		for i, m := range x {
			if i > 0 {
				if e := w.put(","); e != nil {
					return e
				}
			}
			s, _ := json.Marshal(m.key)
			if e := w.put(string(s) + ":"); e != nil {
				return e
			}
			if e := w.json(m.value, depth+1); e != nil {
				return e
			}
		}
		return w.put("}")
	case []any:
		if e := w.put("["); e != nil {
			return e
		}
		for i, m := range x {
			if i > 0 {
				if e := w.put(","); e != nil {
					return e
				}
			}
			if e := w.json(m, depth+1); e != nil {
				return e
			}
		}
		return w.put("]")
	default:
		return fmt.Errorf("value cannot be represented as JSON")
	}
}
