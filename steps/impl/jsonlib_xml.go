package impl

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// This file is the conversion of XML to JSON that the Java platform does with
// json-lib's XMLSerializer (net.sf.json.xml, version 2.4) and Camel's xmljson
// data format. It follows that code step by step, including its oddities, as
// the output of the platform is what flows rely on: an element with one child
// element and whitespace around it is an array, repeated elements accumulate
// into an array that later ones are appended to, "null" text is JSON null, and
// so on. Names in the comments are those of the Java methods.

// jlOptions are the options of the serializer that matter when reading.
type jlOptions struct {
	forceTopLevelObject   bool
	skipNamespaces        bool
	skipWhitespace        bool
	trimSpaces            bool
	removeNamespacePrefix bool // removeNamespacePrefixFromElements
	typeHints             bool // typeHintsEnabled; the hints are json_class, json_type, json_null and json_params
}

// json-lib's JSONTypes.
const (
	jlArrayType    = "array"
	jlBooleanType  = "boolean"
	jlFloatType    = "float"
	jlFunctionType = "function"
	jlIntegerType  = "integer"
	jlNumberType   = "number"
	jlObjectType   = "object"
	jlStringType   = "string"
)

// jlFunction is a JSONFunction, which is written as function(params){ text }.
type jlFunction struct {
	params []string
	text   string
}

func (f jlFunction) String() string {
	s := "function(" + strings.Join(f.params, ",") + "){"
	if f.text != "" {
		s += " " + f.text + " "
	}
	return s + "}"
}

// jlRead is XMLSerializer.read. The result is a JSON value in the types of
// jsonorder.go (nil for JSONNull), written by jlWrite.
func (o jlOptions) read(root *xomElem) (any, error) {
	// The platform does not take a root element that has just text as null with
	// skipWhitespace, as json-lib's isNullObject does: it is an array of the text.
	if null, err := o.isNullObject(root); err != nil || null && !(o.skipWhitespace && len(root.children) == 1) {
		return nil, err
	}
	defaultType, hasType := o.getType(root)
	if !hasType {
		defaultType = jlStringType
	}
	isArray, err := o.isArray(root, true)
	if err != nil {
		return nil, err
	}
	var v any
	if isArray {
		v, err = o.processArrayElement(root, defaultType)
	} else {
		v, err = o.processObjectElement(root, defaultType)
	}
	if err != nil {
		return nil, err
	}
	if o.forceTopLevelObject {
		v = jsonObject{{o.removePrefix(root.name), jlProcess(v)}}
	}
	return v, nil
}

func (o jlOptions) removePrefix(name string) string {
	if o.removeNamespacePrefix {
		if _, local, ok := strings.Cut(name, ":"); ok {
			return local
		}
	}
	return name
}

func (o jlOptions) trimValue(s string) string {
	if o.trimSpaces {
		return javaTrim(s)
	}
	return s
}

// hint returns the value of the type hint attribute name of the element (class,
// type, null or params), if it has it. With typeHints the hints are written as
// they are (json-lib's type hints compatibility), without them they are json_class
// and so on, so that the attributes class and type are plain attributes.
func (o jlOptions) hint(e *xomElem, name string) (string, bool) {
	if !o.typeHints {
		name = "json_" + name
	}
	return e.attr(name)
}

// getClass is XMLSerializer.getClass: "object", "array" or "" for none.
func (o jlOptions) class(e *xomElem) string {
	v, ok := o.hint(e, "class")
	if !ok {
		return ""
	}
	switch v = javaTrim(v); {
	case strings.EqualFold(v, jlObjectType):
		return jlObjectType
	case strings.EqualFold(v, jlArrayType):
		return jlArrayType
	}
	return ""
}

// getType is XMLSerializer.getType: the type the element names, or "" if it names none.
// The second result tells whether the element has a type attribute at all.
func (o jlOptions) getType(e *xomElem) (string, bool) {
	v, ok := o.hint(e, "type")
	if !ok {
		return "", false
	}
	v = javaTrim(v)
	for _, t := range []string{jlBooleanType, jlNumberType, jlIntegerType, jlFloatType, jlObjectType, jlArrayType, jlStringType, jlFunctionType} {
		if strings.EqualFold(v, t) {
			return t, true
		}
	}
	return "", true
}

// hasNamespaces is XMLSerializer.hasNamespaces.
func hasNamespaces(e *xomElem) bool {
	for _, d := range e.decls {
		if !isBlankJava(d.uri) {
			return true
		}
	}
	return false
}

// checkChildElements is XMLSerializer.checkChildElements.
func (o jlOptions) checkChildElements(e *xomElem, isTopLevel bool) (bool, error) {
	childCount := len(e.children)
	elements := e.childElements()
	elementCount := len(elements)

	if childCount == 1 && e.children[0].kind == xomText {
		return isTopLevel, nil
	}
	if childCount == elementCount {
		if elementCount == 0 {
			return true, nil
		}
		if elementCount == 1 {
			return o.skipWhitespace, nil // the only child is an element
		}
	}
	if childCount > elementCount {
		for _, c := range e.children {
			if c.kind == xomText && hasNonWhitespace(c.text) && !o.skipWhitespace {
				return false, nil
			}
		}
	}
	if elementCount == 0 {
		// json-lib asks for the first of no child elements and fails
		return false, fmt.Errorf("element <%s> has no child elements to convert", e.name)
	}
	for _, c := range elements[1:] {
		if c.name != elements[0].name {
			return false, nil
		}
	}
	return true, nil
}

// isArray is XMLSerializer.isArray.
func (o jlOptions) isArray(e *xomElem, isTopLevel bool) (bool, error) {
	isArray := false
	_, hasClass := o.hint(e, "class")
	_, hasType := o.hint(e, "type")
	var err error
	switch n := len(e.attrs); {
	case o.class(e) == jlArrayType:
		isArray = true
	case n == 0:
		isArray, err = o.checkChildElements(e, isTopLevel)
	case n == 1 && (hasClass || hasType):
		isArray, err = o.checkChildElements(e, isTopLevel)
	case n == 2 && hasClass && hasType:
		isArray, err = o.checkChildElements(e, isTopLevel)
	}
	if err != nil {
		return false, err
	}
	if isArray && hasNamespaces(e) {
		return false, nil
	}
	return isArray, nil
}

// isFunction is XMLSerializer.isFunction.
func (o jlOptions) isFunctionElement(e *xomElem) bool {
	n := len(e.attrs)
	if n == 0 {
		return false
	}
	typ, hasType := o.hint(e, "type")
	_, hasParams := o.hint(e, "params")
	if n == 1 && hasParams {
		return true
	}
	return n == 2 && hasParams && hasType && (strings.EqualFold(typ, jlStringType) || strings.EqualFold(typ, jlFunctionType))
}

// isNullObject is XMLSerializer.isNullObject.
func (o jlOptions) isNullObject(e *xomElem) (bool, error) {
	if len(e.children) == 0 {
		_, hasNull := o.hint(e, "null")
		_, hasClass := o.hint(e, "class")
		_, hasType := o.hint(e, "type")
		n := len(e.attrs)
		switch {
		case n == 0, hasNull:
			return true, nil
		case n == 1 && (hasClass || hasType):
			return true, nil
		case n == 2 && hasClass && hasType:
			return true, nil
		}
	}
	return o.skipWhitespace && len(e.children) == 1 && e.children[0].kind == xomText, nil
}

// isObject is XMLSerializer.isObject.
func (o jlOptions) isObject(e *xomElem, isTopLevel bool) (bool, error) {
	isArray, err := o.isArray(e, isTopLevel)
	if err != nil || isArray || o.isFunctionElement(e) {
		return false, err
	}
	if hasNamespaces(e) {
		return true, nil
	}
	if n := len(e.attrs); n > 0 {
		attrs := 0
		for _, name := range []string{"null", "class", "type"} {
			if _, ok := o.hint(e, name); ok {
				attrs++
			}
		}
		switch n {
		case 1:
			if attrs == 0 {
				return true, nil
			}
		case 2:
			if attrs < 2 {
				return true, nil
			}
		case 3:
			if attrs < 3 {
				return true, nil
			}
		default:
			return true, nil
		}
	}
	if len(e.children) == 1 && e.children[0].kind == xomText {
		return isTopLevel, nil
	}
	return true, nil
}

// processArrayElement is XMLSerializer.processArrayElement.
func (o jlOptions) processArrayElement(e *xomElem, defaultType string) ([]any, error) {
	arr := []any{}
	for _, c := range e.children {
		switch c.kind {
		case xomText:
			if hasNonWhitespace(c.text) {
				arr = append(arr, jlProcess(c.text))
			}
		case xomElement:
			var err error
			if arr, _, err = o.setValue(arr, nil, c.elem, defaultType, true); err != nil {
				return nil, err
			}
		}
	}
	return arr, nil
}

// processObjectElement is XMLSerializer.processObjectElement; the result is a
// jsonObject, or nil for JSONNull.
func (o jlOptions) processObjectElement(e *xomElem, defaultType string) (any, error) {
	null, err := o.isNullObject(e)
	if err != nil || null {
		return nil, err
	}
	obj := jsonObject{}

	if !o.skipNamespaces {
		for _, d := range e.decls {
			if isBlankJava(d.uri) {
				continue
			}
			key := "@xmlns"
			if !isBlankJava(d.prefix) {
				key += ":" + d.prefix
			}
			jlSetOrAccumulate(&obj, key, o.trimValue(d.uri))
		}
	}
	for _, a := range e.attrs {
		if o.typeHints && (strings.EqualFold(a.name, "class") || strings.EqualFold(a.name, "type")) {
			continue
		}
		jlSetOrAccumulate(&obj, "@"+o.removePrefix(a.name), o.trimValue(a.value))
	}
	for _, c := range e.children {
		switch c.kind {
		case xomText:
			if hasNonWhitespace(c.text) {
				jlSetOrAccumulate(&obj, "#text", o.trimValue(c.text))
			}
		case xomElement:
			var err error
			if _, obj, err = o.setValue(nil, obj, c.elem, defaultType, false); err != nil {
				return nil, err
			}
		}
	}
	return obj, nil
}

// processElement is XMLSerializer.processElement.
func (o jlOptions) processElement(e *xomElem, typ string) (any, error) {
	if null, err := o.isNullObject(e); err != nil || null {
		return nil, err
	}
	if isArray, err := o.isArray(e, false); err != nil {
		return nil, err
	} else if isArray {
		return o.processArrayElement(e, typ)
	}
	if isObject, err := o.isObject(e, false); err != nil {
		return nil, err
	} else if isObject {
		return o.processObjectElement(e, typ)
	}
	return o.trimValue(e.value()), nil
}

// setValue is the two overloads of XMLSerializer.setValue: it adds the value
// of the child element e to the array arr (inArray) or to the object obj,
// and returns them.
func (o jlOptions) setValue(arr []any, obj jsonObject, e *xomElem, defaultType string, inArray bool) ([]any, jsonObject, error) {
	class := o.class(e)
	typ, _ := o.getType(e)
	if typ == "" {
		typ = defaultType
	}
	key := o.removePrefix(e.name)

	// put adds a value: to the array, or to the object under the element's name.
	put := func(v any) {
		if inArray {
			arr = append(arr, jlProcess(v))
		} else {
			jlSetOrAccumulate(&obj, key, v)
		}
	}
	// simplify is simplifyValue, whose parent is the object, if there is one.
	simplify := func(v any) any {
		if inArray {
			return jlSimplify(nil, v)
		}
		return jlSimplify(obj, v)
	}

	if hasNamespaces(e) && !o.skipNamespaces {
		v, err := o.processElement(e, typ)
		if err != nil {
			return nil, nil, err
		}
		put(simplify(v))
		return arr, obj, nil
	}
	if len(e.attrs) > 0 {
		if o.isFunctionElement(e) {
			params, _ := o.hint(e, "params")
			put(jlFunction{javaSplit(params, ","), e.value()})
			return arr, obj, nil
		}
		if inArray {
			v, err := o.processElement(e, typ)
			if err != nil {
				return nil, nil, err
			}
			put(simplify(v))
			return arr, obj, nil
		}
	}

	classProcessed := false
	switch {
	case strings.EqualFold(class, jlArrayType):
		v, err := o.processArrayElement(e, typ)
		if err != nil {
			return nil, nil, err
		}
		put(v)
		classProcessed = true
	case strings.EqualFold(class, jlObjectType):
		v, err := o.processObjectElement(e, typ)
		if err != nil {
			return nil, nil, err
		}
		put(simplify(v))
		classProcessed = true
	}
	if classProcessed {
		return arr, obj, nil
	}

	if typ == "" {
		// json-lib has no type to compare then and fails
		return nil, nil, fmt.Errorf("element <%s>: the json_type of the root element is not a JSON type", e.name)
	}
	switch strings.ToLower(typ) {
	case jlBooleanType:
		put(strings.EqualFold(e.value(), "true"))
	case jlNumberType:
		put(jlNumber(e.value(), true))
	case jlIntegerType:
		put(jlNumber(e.value(), false))
	case jlFloatType:
		if f, err := jlParseDouble(e.value()); err == nil {
			put(jlDouble(f))
		} else {
			put(nil)
		}
	case jlFunctionType:
		var params []string
		if p, ok := o.hint(e, "params"); ok {
			params = javaSplit(p, ",")
		}
		put(jlFunction{params, e.value()})
	case jlStringType:
		if p, ok := o.hint(e, "params"); ok {
			put(jlFunction{javaSplit(p, ","), e.value()})
			break
		}
		if isArray, err := o.isArray(e, false); err != nil {
			return nil, nil, err
		} else if isArray {
			v, err := o.processArrayElement(e, defaultType)
			if err != nil {
				return nil, nil, err
			}
			put(v)
			break
		}
		if isObject, err := o.isObject(e, false); err != nil {
			return nil, nil, err
		} else if isObject {
			v, err := o.processObjectElement(e, defaultType)
			if err != nil {
				return nil, nil, err
			}
			put(simplify(v))
			break
		}
		put(o.trimValue(e.value()))
	}
	// A type that is none of these (object, array) adds nothing, as in json-lib.
	return arr, obj, nil
}

// jlSimplify is XMLSerializer.simplifyValue: it removes the namespace
// declarations that the parent object has too from an object, and turns an
// object that has just text into that text.
func jlSimplify(parent jsonObject, v any) any {
	obj, ok := v.(jsonObject)
	if !ok {
		return v
	}
	for _, m := range parent {
		if strings.HasPrefix(m.key, "@xmlns") {
			if i := obj.index(m.key); i >= 0 && jlEqual(m.value, obj[i].value) {
				obj = append(obj[:i:i], obj[i+1:]...)
			}
		}
	}
	if len(obj) == 1 && obj[0].key == "#text" {
		return obj[0].value
	}
	return obj
}

func jlEqual(a, b any) bool {
	sa, aok := a.(string)
	sb, bok := b.(string)
	return aok && bok && sa == sb
}

func (o jsonObject) index(key string) int {
	for i, m := range o {
		if m.key == key {
			return i
		}
	}
	return -1
}

// jlSetOrAccumulate is XMLSerializer.setOrAccumulate: the value is set, or
// accumulated when the object has the key already: an array is appended to
// whatever it holds (also another array, which becomes an element), any other
// value becomes the first element of a new array.
func jlSetOrAccumulate(obj *jsonObject, key string, v any) {
	v = jlProcess(v)
	i := obj.index(key)
	switch {
	case i < 0:
		*obj = append(*obj, jsonMember{key, v})
	default:
		if arr, ok := (*obj)[i].value.([]any); ok {
			(*obj)[i].value = append(arr, jlProcess(v))
		} else {
			(*obj)[i].value = []any{(*obj)[i].value, jlProcess(v)}
		}
	}
}

// jlProcess is the processing json-lib does of a value that is put in an
// object or array (AbstractJSON._processValue): a string may be a function, or
// JSON text that becomes the JSON value. Other values stay as they are.
func jlProcess(v any) any {
	s, ok := v.(string)
	if !ok {
		return v
	}
	switch {
	case isFunctionText(s):
		return s
	case len(s) >= 2 && (s[0] == '\'' && s[len(s)-1] == '\'' || s[0] == '"' && s[len(s)-1] == '"'):
		stripped := s[1 : len(s)-1]
		switch {
		case isFunctionText(stripped):
			return `"` + stripped + `"`
		case strings.HasPrefix(stripped, "[") && strings.HasSuffix(stripped, "]"),
			strings.HasPrefix(stripped, "{") && strings.HasSuffix(stripped, "}"):
			return stripped
		}
		return s
	case s == "null", s == "true", s == "false":
		return s
	case strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]"), strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}"):
		if j, err := readJSON(s); err == nil {
			return j
		}
	}
	return s
}

var functionPattern = regexp.MustCompile(`(?s)^function ?\(.*?\)[ \n\t]*\{.*?\}$`)

// isFunctionText is JSONUtils.isFunction for a string.
func isFunctionText(s string) bool {
	return strings.HasPrefix(s, "function") && functionPattern.MatchString(s)
}

// jlNumber is Integer.valueOf of the text, else, if orDouble, Double.valueOf. Text
// that is no number gives null: json-lib throws a NumberFormatException for it,
// but the platform answers null (only its older instances fail the message).
func jlNumber(s string, orDouble bool) any {
	if n, err := strconv.ParseInt(s, 10, 32); err == nil {
		return json.Number(strconv.FormatInt(n, 10))
	}
	if !orDouble {
		return nil
	}
	f, err := jlParseDouble(s)
	if err != nil {
		return nil
	}
	return jlDouble(f)
}

var javaDoublePattern = regexp.MustCompile(`^[+-]?(NaN|Infinity|((\d+\.?\d*|\.\d+)([eE][+-]?\d+)?)[fFdD]?)$`)

// jlParseDouble is Double.valueOf, which ignores surrounding whitespace.
func jlParseDouble(s string) (float64, error) {
	t := javaTrim(s)
	if !javaDoublePattern.MatchString(t) {
		return 0, fmt.Errorf("For input string: %q", s)
	}
	t = strings.TrimRight(t, "fFdD")
	switch strings.TrimLeft(t, "+-") {
	case "NaN":
		return math.NaN(), nil
	case "Infinity":
		if strings.HasPrefix(t, "-") {
			return math.Inf(-1), nil
		}
		return math.Inf(1), nil
	}
	f, err := strconv.ParseFloat(t, 64)
	if err != nil && !math.IsInf(f, 0) {
		return 0, fmt.Errorf("For input string: %q", s)
	}
	return f, nil
}

// jlDouble is a Double as json-lib writes it (JSONUtils.numberToString): Java's
// text of the number without trailing zeros.
func jlDouble(f float64) any {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return jlNonFinite{}
	}
	s := javaDoubleString(f)
	if strings.Contains(s, ".") && !strings.ContainsAny(s, "eE") {
		s = strings.TrimRight(s, "0")
		s = strings.TrimSuffix(s, ".")
	}
	return json.Number(s)
}

// jlNonFinite is a number JSON cannot hold; json-lib refuses to put it in a value.
type jlNonFinite struct{}

// javaDoubleString is Double.toString: plain from 0.001 up to 10 million,
// otherwise as d.dddE±n.
func javaDoubleString(f float64) string {
	if f == 0 {
		if math.Signbit(f) {
			return "-0.0"
		}
		return "0.0"
	}
	if abs := math.Abs(f); abs >= 1e-3 && abs < 1e7 {
		s := strconv.FormatFloat(f, 'f', -1, 64)
		if !strings.Contains(s, ".") {
			s += ".0"
		}
		return s
	}
	mantissa, exp, _ := strings.Cut(strconv.FormatFloat(f, 'E', -1, 64), "E")
	if !strings.Contains(mantissa, ".") {
		mantissa += ".0"
	}
	n, _ := strconv.Atoi(exp)
	return mantissa + "E" + strconv.Itoa(n)
}

// javaTrim is String.trim: it removes the characters up to and including a space.
func javaTrim(s string) string {
	return strings.TrimFunc(s, func(r rune) bool { return r <= ' ' })
}

// isJavaWhitespace is Character.isWhitespace.
func isJavaWhitespace(r rune) bool {
	switch r {
	case '\t', '\n', '\u000B', '\f', '\r', '\u001C', '\u001D', '\u001E', '\u001F':
		return true
	case ' ', ' ', ' ':
		return false
	}
	return unicode.In(r, unicode.Zs, unicode.Zl, unicode.Zp)
}

// hasNonWhitespace is StringUtils.isNotBlank(StringUtils.strip(s)).
func hasNonWhitespace(s string) bool {
	return strings.IndexFunc(s, func(r rune) bool { return !isJavaWhitespace(r) }) >= 0
}

// isBlankJava is StringUtils.isBlank.
func isBlankJava(s string) bool { return !hasNonWhitespace(s) }

// javaSplit is StringUtils.split(s, sep): the parts, without empty ones.
func javaSplit(s, sep string) []string {
	var parts []string
	for _, p := range strings.Split(s, sep) {
		if p != "" {
			parts = append(parts, p)
		}
	}
	return parts
}

// jlWrite writes v as json-lib does (JSONObject.toString): compact, a string
// that is a function as it is, the string "null" as null, and numbers as
// jlDouble gives them.
func jlWrite(b *strings.Builder, v any) error {
	switch x := v.(type) {
	case nil:
		b.WriteString("null")
	case jsonObject:
		b.WriteByte('{')
		for i, m := range x {
			if i > 0 {
				b.WriteByte(',')
			}
			jlQuote(b, m.key)
			b.WriteByte(':')
			if err := jlWrite(b, m.value); err != nil {
				return err
			}
		}
		b.WriteByte('}')
	case []any:
		b.WriteByte('[')
		for i, e := range x {
			if i > 0 {
				b.WriteByte(',')
			}
			if err := jlWrite(b, e); err != nil {
				return err
			}
		}
		b.WriteByte(']')
	case string:
		if x == "null" {
			b.WriteString("null")
		} else {
			jlQuote(b, x)
		}
	case json.Number:
		b.WriteString(string(x))
	case bool:
		b.WriteString(strconv.FormatBool(x))
	case jlFunction:
		b.WriteString(x.String())
	case jlNonFinite:
		return fmt.Errorf("JSON does not allow non-finite numbers.")
	default:
		jlQuote(b, fmt.Sprint(x))
	}
	return nil
}

// jlQuote is JSONUtils.quote: a string in double quotes, with a backslash
// before a quote, a backslash and a slash after <, and control characters escaped.
func jlQuote(b *strings.Builder, s string) {
	if isFunctionText(s) {
		b.WriteString(s)
		return
	}
	b.WriteByte('"')
	var prev rune
	for _, c := range s {
		switch {
		case c == '\\' || c == '"':
			b.WriteByte('\\')
			b.WriteRune(c)
		case c == '/':
			if prev == '<' {
				b.WriteByte('\\')
			}
			b.WriteByte('/')
		case c == '\b':
			b.WriteString(`\b`)
		case c == '\t':
			b.WriteString(`\t`)
		case c == '\n':
			b.WriteString(`\n`)
		case c == '\f':
			b.WriteString(`\f`)
		case c == '\r':
			b.WriteString(`\r`)
		case c < ' ':
			fmt.Fprintf(b, `\u%04x`, c)
		default:
			b.WriteRune(c)
		}
		prev = c
	}
	b.WriteByte('"')
}
