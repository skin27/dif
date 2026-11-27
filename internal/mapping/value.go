// Package mapping compiles and evaluates bounded, side-effect-free mappings.
package mapping

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

const BindingKey = "dif.mapping.plan"

// Limits are host-owned. Zero fields select conservative defaults.
type Limits struct {
	SourceBytes      int `json:"sourceBytes,omitempty"`
	Depth            int `json:"depth,omitempty"`
	Operations       int `json:"operations,omitempty"`
	Nodes            int `json:"nodes,omitempty"`
	Bytes            int `json:"bytes,omitempty"`
	Digits           int `json:"digits,omitempty"`
	CallDepth        int `json:"callDepth,omitempty"`
	Functions        int `json:"functions,omitempty"`
	TotalSourceBytes int `json:"totalSourceBytes,omitempty"`
}

func (l Limits) Normalize() (Limits, error) {
	p := []*int{&l.SourceBytes, &l.Depth, &l.Operations, &l.Nodes, &l.Bytes, &l.Digits, &l.CallDepth, &l.Functions, &l.TotalSourceBytes}
	d := []int{256 << 10, 128, 1000000, 100000, 16 << 20, 128, 64, 1000, 16 << 20}
	for i, v := range p {
		if *v < 0 {
			return l, fmt.Errorf("mapping limits must be positive")
		}
		if *v == 0 {
			*v = d[i]
		}
	}
	if l.Depth > 512 || l.Digits > 4096 || l.CallDepth > 512 {
		return l, fmt.Errorf("mapping depth or precision exceeds implementation limit")
	}
	return l, nil
}

type member struct {
	key   string
	value any
}
type object []member
type missing struct{}

var absent = missing{}

type headerValues map[string]any

func (h headerValues) materialize(b *budget) (any, error) {
	v := map[string]any{}
	for k, value := range h {
		if e := b.tick(); e != nil {
			return nil, e
		}
		if k != "body" && !strings.HasPrefix(k, "metadata.") {
			v[k] = value
		}
	}
	return normalize(v, b, 0)
}

func (o object) get(k string) any {
	for _, m := range o {
		if m.key == k {
			return m.value
		}
	}
	return absent
}
func isMissing(v any) bool { _, ok := v.(missing); return ok }

type budget struct {
	ctx         context.Context
	limits      Limits
	ops, nodes  int
	calls       int
	nativeBytes int
	indexes     map[*member]map[string]any
}

func (b *budget) lookup(o object, k string) (any, error) {
	if len(o) == 0 {
		return absent, nil
	}
	if b.indexes == nil {
		b.indexes = map[*member]map[string]any{}
	}
	index := b.indexes[&o[0]]
	if index == nil {
		index = make(map[string]any, len(o))
		for _, m := range o {
			if e := b.tick(); e != nil {
				return nil, e
			}
			index[m.key] = m.value
		}
		b.indexes[&o[0]] = index
	}
	if v, ok := index[k]; ok {
		return v, nil
	}
	return absent, nil
}
func (b *budget) native(n int) error {
	if n > b.limits.Bytes-b.nativeBytes {
		return fmt.Errorf("native input byte limit exceeded")
	}
	b.nativeBytes += n
	return nil
}

func (b *budget) tick() error {
	b.ops++
	if b.ops > b.limits.Operations {
		return fmt.Errorf("mapping operation limit exceeded")
	}
	return b.ctx.Err()
}
func (b *budget) node(depth int) error {
	if err := b.tick(); err != nil {
		return err
	}
	b.nodes++
	if depth > b.limits.Depth || b.nodes > b.limits.Nodes {
		return fmt.Errorf("mapping depth or node limit exceeded")
	}
	return nil
}
func number(s string, l Limits) (*big.Rat, error) {
	if len(s) > l.Digits*2+16 {
		return nil, fmt.Errorf("numeric precision limit exceeded")
	}
	// Bound exponent before big.Rat allocates powers of ten.
	if at := strings.IndexAny(s, "eE"); at >= 0 {
		e, err := strconv.Atoi(s[at+1:])
		if err != nil || e > l.Digits || e < -l.Digits {
			return nil, fmt.Errorf("numeric exponent exceeds limit")
		}
	}
	if !json.Valid([]byte(s)) || strings.ContainsAny(s, "/\"[]{}") {
		return nil, fmt.Errorf("expected decimal number")
	}
	r, ok := new(big.Rat).SetString(s)
	if !ok {
		return nil, fmt.Errorf("expected decimal number")
	}
	return boundedNumber(r, l)
}
func boundedNumber(r *big.Rat, l Limits) (*big.Rat, error) {
	if len(strings.TrimPrefix(r.Num().String(), "-")) > l.Digits || len(r.Denom().String()) > l.Digits+1 {
		return nil, fmt.Errorf("numeric precision limit exceeded")
	}
	return r, nil
}
func decimal(r *big.Rat, l Limits) (string, error) {
	d := new(big.Int).Set(r.Denom())
	two, five := big.NewInt(2), big.NewInt(5)
	rem := new(big.Int)
	a, c := 0, 0
	for new(big.Int).Mod(d, two).Sign() == 0 {
		d.Quo(d, two)
		a++
	}
	for rem.Mod(d, five).Sign() == 0 {
		d.Quo(d, five)
		c++
	}
	if d.Cmp(big.NewInt(1)) != 0 {
		return "", fmt.Errorf("non-terminating decimal requires round")
	}
	scale := max(a, c)
	if scale > l.Digits {
		return "", fmt.Errorf("numeric precision limit exceeded")
	}
	s := r.FloatString(scale)
	if strings.Contains(s, ".") {
		s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	}
	if s == "-0" {
		s = "0"
	}
	return s, nil
}

// normalize copies supported native values; no input container is mutated.
func normalize(v any, b *budget, depth int) (any, error) {
	if err := b.node(depth); err != nil {
		return nil, err
	}
	switch x := v.(type) {
	case nil, bool:
		return v, nil
	case string:
		if err := b.work(len(x)); err != nil {
			return nil, err
		}
		if !utf8.ValidString(x) {
			return nil, fmt.Errorf("native string is not UTF-8")
		}
		if e := b.native(len(x)); e != nil {
			return nil, e
		}
		return x, nil
	case json.Number:
		return number(string(x), b.limits)
	case int:
		return number(strconv.Itoa(x), b.limits)
	case int64:
		return number(strconv.FormatInt(x, 10), b.limits)
	case int8, int16, int32, uint, uint8, uint16, uint32:
		return number(fmt.Sprint(x), b.limits)
	case uint64:
		return number(strconv.FormatUint(x, 10), b.limits)
	case float64:
		return number(strconv.FormatFloat(x, 'g', -1, 64), b.limits)
	case float32:
		return number(strconv.FormatFloat(float64(x), 'g', -1, 32), b.limits)
	case []any:
		if len(x) > b.limits.Nodes {
			return nil, fmt.Errorf("native array exceeds node limit")
		}
		a := make([]any, 0, len(x))
		for _, v := range x {
			n, e := normalize(v, b, depth+1)
			if e != nil {
				return nil, e
			}
			a = append(a, n)
		}
		return a, nil
	case map[string]any:
		if len(x) > b.limits.Nodes {
			return nil, fmt.Errorf("native object exceeds node limit")
		}
		keys := make([]string, 0, len(x))
		for k := range x {
			if !utf8.ValidString(k) {
				return nil, fmt.Errorf("native key is not UTF-8")
			}
			if e := b.native(len(k)); e != nil {
				return nil, e
			}
			keys = append(keys, k)
		}
		sort.Strings(keys)
		o := object{}
		for _, k := range keys {
			n, e := normalize(x[k], b, depth+1)
			if e != nil {
				return nil, e
			}
			o = append(o, member{k, n})
		}
		return o, nil
	default:
		return nil, fmt.Errorf("unsupported native value type")
	}
}
