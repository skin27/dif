package mapping

import (
	"fmt"
	"math/big"
	"strings"
	"unicode/utf8"
)

var builtins = map[string]int{"string": 1, "number": 1, "boolean": 1, "trim": 1, "upper": 1, "lower": 1, "size": 1, "sum": 1, "round": 2, "exists": 1, "isNull": 1, "child": 2, "children": 2, "attr": 2, "text": 1, "element": 3, "textNode": 1}

type environment struct {
	parent *environment
	values map[string]any
}

func (e *environment) get(k string) any {
	for e != nil {
		if v, ok := e.values[k]; ok {
			return v
		}
		e = e.parent
	}
	return absent
}
func (x *expr) eval(b *budget, e *environment) (v any, err error) {
	if err = b.tick(); err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			err = fmt.Errorf("line %d column %d: %w", x.tok.line, x.tok.col, err)
		}
	}()
	switch x.op {
	case "literal":
		return x.value, nil
	case "variable":
		return e.get(x.names[0]), nil
	case "let":
		a, er := x.args[0].eval(b, e)
		if er != nil {
			return nil, er
		}
		return x.args[1].eval(b, &environment{e, map[string]any{x.names[0]: a}})
	case "if":
		c, er := x.args[0].eval(b, e)
		if er != nil {
			return nil, er
		}
		ok, is := c.(bool)
		if !is {
			return nil, fmt.Errorf("condition must be boolean")
		}
		if ok {
			return x.args[1].eval(b, e)
		}
		return x.args[2].eval(b, e)
	case "object", "array":
		if er := b.node(0); er != nil {
			return nil, er
		}
		a := []any{}
		o := object{}
		for i, ex := range x.args {
			v, er := ex.eval(b, e)
			if er != nil {
				return nil, er
			}
			if isMissing(v) {
				return nil, fmt.Errorf("missing output value requires default")
			}
			if x.op == "array" {
				a = append(a, v)
			} else {
				o = append(o, member{x.names[i], v})
			}
		}
		if x.op == "array" {
			return a, nil
		}
		return o, nil
	case "select":
		a, er := x.args[0].eval(b, e)
		if er != nil {
			return nil, er
		}
		k, er := x.args[1].eval(b, e)
		if er != nil {
			return nil, er
		}
		if isMissing(a) {
			return absent, nil
		}
		if key, ok := k.(string); ok {
			switch o := a.(type) {
			case object:
				return b.lookup(o, key)
			case headerValues:
				if key == "body" || strings.HasPrefix(key, "metadata.") {
					return absent, nil
				}
				v, found := o[key]
				if !found {
					return absent, nil
				}
				return normalize(v, b, 0)
			}
		}
		if ar, ok := a.([]any); ok {
			if n, ok := k.(*big.Rat); ok && n.IsInt() && n.Num().IsInt64() {
				i := n.Num().Int64()
				if i < 0 || i >= int64(len(ar)) {
					return absent, nil
				}
				return ar[i], nil
			}
		}
		return nil, fmt.Errorf("invalid selector or value type")
	case "map", "filter":
		v, er := x.args[0].eval(b, e)
		if er != nil {
			return nil, er
		}
		a, ok := v.([]any)
		if !ok {
			return nil, fmt.Errorf("collection must be an array")
		}
		out := []any{}
		for i, item := range a {
			if er := b.node(0); er != nil {
				return nil, er
			}
			vars := map[string]any{x.names[0]: item}
			if len(x.names) > 1 {
				vars[x.names[1]] = new(big.Rat).SetInt64(int64(i))
			}
			v, er := x.args[1].eval(b, &environment{e, vars})
			if er != nil {
				return nil, er
			}
			if x.op == "filter" {
				yes, ok := v.(bool)
				if !ok {
					return nil, fmt.Errorf("filter predicate must be boolean")
				}
				if yes {
					out = append(out, item)
				}
			} else {
				if isMissing(v) {
					return nil, fmt.Errorf("missing array element requires default")
				}
				out = append(out, v)
			}
		}
		return out, nil
	case "coalesce":
		for _, a := range x.args {
			v, err := a.eval(b, e)
			if err != nil {
				return nil, err
			}
			if v != nil && !isMissing(v) {
				return v, nil
			}
		}
		return nil, nil
	case "function":
		args := make([]any, 0, len(x.args))
		for _, a := range x.args {
			v, err := a.eval(b, e)
			if err != nil {
				return nil, err
			}
			if h, ok := v.(headerValues); ok {
				v, err = h.materialize(b)
				if err != nil {
					return nil, err
				}
			}
			args = append(args, v)
		}
		return x.function.invoke(args, b)
	case "call":
		args := []any{}
		for _, a := range x.args {
			v, er := a.eval(b, e)
			if er != nil {
				return nil, er
			}
			args = append(args, v)
		}
		return call(x.names[0], args, b)
	}
	a, er := x.args[0].eval(b, e)
	if er != nil {
		return nil, er
	}
	switch x.op {
	case "unary-":
		if n, ok := a.(*big.Rat); ok {
			return boundedNumber(new(big.Rat).Neg(n), b.limits)
		}
		return nil, fmt.Errorf("expected number")
	case "unarynot", "unary!":
		if v, ok := a.(bool); ok {
			return !v, nil
		}
		return nil, fmt.Errorf("expected boolean")
	case "default":
		if a != nil && !isMissing(a) {
			return a, nil
		}
		return x.args[1].eval(b, e)
	case "and", "or":
		v, ok := a.(bool)
		if !ok {
			return nil, fmt.Errorf("expected boolean")
		}
		if x.op == "and" && !v || x.op == "or" && v {
			return v, nil
		}
	}
	c, er := x.args[1].eval(b, e)
	if er != nil {
		return nil, er
	}
	switch x.op {
	case "and", "or":
		if v, ok := c.(bool); ok {
			return v, nil
		}
		return nil, fmt.Errorf("expected boolean")
	case "==", "!=":
		eq, er := equal(a, c, b)
		if x.op == "!=" {
			eq = !eq
		}
		return eq, er
	case "++":
		s, ok := a.(string)
		t, ok2 := c.(string)
		if !ok || !ok2 {
			return nil, fmt.Errorf("concatenation requires strings")
		}
		if len(s)+len(t) > b.limits.Bytes {
			return nil, fmt.Errorf("string size limit exceeded")
		}
		return s + t, nil
	}
	an, ok := a.(*big.Rat)
	cn, ok2 := c.(*big.Rat)
	if x.op == "<" || x.op == ">" || x.op == "<=" || x.op == ">=" {
		cmp := 0
		if ok && ok2 {
			cmp = an.Cmp(cn)
		} else if s, is := a.(string); is {
			t, is := c.(string)
			if !is {
				return nil, fmt.Errorf("comparison requires matching scalar types")
			}
			cmp = strings.Compare(s, t)
		} else {
			return nil, fmt.Errorf("comparison requires matching scalar types")
		}
		switch x.op {
		case "<":
			return cmp < 0, nil
		case ">":
			return cmp > 0, nil
		case "<=":
			return cmp <= 0, nil
		default:
			return cmp >= 0, nil
		}
	}
	if !ok || !ok2 {
		return nil, fmt.Errorf("arithmetic requires numbers")
	}
	r := new(big.Rat)
	switch x.op {
	case "+":
		r.Add(an, cn)
	case "-":
		r.Sub(an, cn)
	case "*":
		r.Mul(an, cn)
	case "/":
		if cn.Sign() == 0 {
			return nil, fmt.Errorf("division by zero")
		}
		r.Quo(an, cn)
	default:
		return nil, fmt.Errorf("unknown operation")
	}
	return boundedNumber(r, b.limits)
}
func equal(a, c any, b *budget) (bool, error) {
	if h, ok := a.(headerValues); ok {
		v, e := h.materialize(b)
		if e != nil {
			return false, e
		}
		a = v
	}
	if h, ok := c.(headerValues); ok {
		v, e := h.materialize(b)
		if e != nil {
			return false, e
		}
		c = v
	}
	if er := b.tick(); er != nil {
		return false, er
	}
	switch x := a.(type) {
	case nil:
		return c == nil, nil
	case missing:
		return isMissing(c), nil
	case string:
		if err := b.work(len(x)); err != nil {
			return false, err
		}
		v, ok := c.(string)
		return ok && x == v, nil
	case bool:
		v, ok := c.(bool)
		return ok && x == v, nil
	case *big.Rat:
		v, ok := c.(*big.Rat)
		return ok && x.Cmp(v) == 0, nil
	case []any:
		y, ok := c.([]any)
		if !ok || len(x) != len(y) {
			return false, nil
		}
		for i, v := range x {
			ok, e := equal(v, y[i], b)
			if !ok || e != nil {
				return ok, e
			}
		}
		return true, nil
	case object:
		y, ok := c.(object)
		if !ok || len(x) != len(y) {
			return false, nil
		}
		for _, v := range x {
			other, e := b.lookup(y, v.key)
			if e != nil {
				return false, e
			}
			ok, e := equal(v.value, other, b)
			if !ok || e != nil {
				return ok, e
			}
		}
		return true, nil
	default:
		return false, fmt.Errorf("equality unsupported for this value")
	}
}
func call(name string, a []any, b *budget) (any, error) {
	if _, ok := extraBuiltins[name]; ok {
		return extraCall(name, a, b)
	}
	for _, v := range a {
		if s, ok := v.(string); ok {
			if e := b.work(len(s)); e != nil {
				return nil, e
			}
		}
	}
	bad := func() (any, error) { return nil, fmt.Errorf("%s: invalid argument type", name) }
	switch name {
	case "exists":
		return !isMissing(a[0]), nil
	case "isNull":
		return a[0] == nil, nil
	case "string":
		switch v := a[0].(type) {
		case string:
			return v, nil
		case bool:
			if v {
				return "true", nil
			}
			return "false", nil
		case *big.Rat:
			return decimal(v, b.limits)
		}
	case "number":
		switch v := a[0].(type) {
		case *big.Rat:
			return v, nil
		case string:
			return number(v, b.limits)
		}
	case "boolean":
		switch v := a[0].(type) {
		case bool:
			return v, nil
		case string:
			if v == "true" {
				return true, nil
			}
			if v == "false" {
				return false, nil
			}
		}
	case "trim", "upper", "lower":
		if s, ok := a[0].(string); ok {
			var out string
			switch name {
			case "trim":
				out = strings.TrimSpace(s)
			case "upper":
				out = strings.ToUpper(s)
			case "lower":
				out = strings.ToLower(s)
			}
			if len(out) > b.limits.Bytes {
				return nil, fmt.Errorf("string size limit exceeded")
			}
			return out, nil
		}
	case "size":
		n := 0
		switch v := a[0].(type) {
		case string:
			n = utf8.RuneCountInString(v)
		case []any:
			n = len(v)
		case object:
			n = len(v)
		case headerValues:
			for k := range v {
				if e := b.tick(); e != nil {
					return nil, e
				}
				if k != "body" && !strings.HasPrefix(k, "metadata.") {
					n++
				}
			}
		default:
			return bad()
		}
		return new(big.Rat).SetInt64(int64(n)), nil
	case "sum":
		if ar, ok := a[0].([]any); ok {
			r := new(big.Rat)
			for _, v := range ar {
				if err := b.tick(); err != nil {
					return nil, err
				}
				n, ok := v.(*big.Rat)
				if !ok {
					return bad()
				}
				r.Add(r, n)
				if _, err := boundedNumber(r, b.limits); err != nil {
					return nil, err
				}
			}
			return r, nil
		}
	case "round":
		n, ok := a[0].(*big.Rat)
		s, ok2 := a[1].(*big.Rat)
		if !ok || !ok2 || !s.IsInt() || !s.Num().IsInt64() {
			return bad()
		}
		scale := s.Num().Int64()
		if scale < 0 || scale > int64(b.limits.Digits) {
			return nil, fmt.Errorf("round scale exceeds precision")
		}
		power := new(big.Int).Exp(big.NewInt(10), big.NewInt(scale), nil)
		v := new(big.Rat).Mul(n, new(big.Rat).SetInt(power))
		abs := new(big.Int).Abs(v.Num())
		q, rem := new(big.Int), new(big.Int)
		q.QuoRem(abs, v.Denom(), rem)
		if rem.Lsh(rem, 1).Cmp(v.Denom()) >= 0 {
			q.Add(q, big.NewInt(1))
		}
		if n.Sign() < 0 {
			q.Neg(q)
		}
		return boundedNumber(new(big.Rat).SetFrac(q, power), b.limits)
	case "child", "children", "attr", "text", "element", "textNode":
		return xmlCall(name, a, b)
	}
	return bad()
}
