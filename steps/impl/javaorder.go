package impl

import (
	"sort"
	"unicode/utf16"
)

// Some platform output lists the members of an object in the order a Java
// HashMap iterates them, not the order of the document (org.json keeps its
// objects in a HashMap). A text compare of such output needs that order, so
// javaMapOrder reproduces it: the entries of a HashMap that was filled with the
// keys in this order come out by bucket, and within a bucket in the order they
// were put. A table of 16 buckets doubles whenever it holds more than three
// quarters of its size; a key goes to the bucket of its hash code, whose high
// bits are mixed into the low ones, taken modulo the size.

// javaHash is Java's String.hashCode.
func javaHash(s string) int32 {
	var h int32
	for _, u := range utf16.Encode([]rune(s)) {
		h = 31*h + int32(u)
	}
	return h
}

// javaBuckets is the size of the table of a HashMap that holds n entries.
func javaBuckets(n int) int {
	size := 16
	for n > size/4*3 {
		size *= 2
	}
	return size
}

// javaMapOrder returns o with its members in the order a HashMap iterates them.
func javaMapOrder(o jsonObject) jsonObject {
	size := uint32(javaBuckets(len(o)))
	bucket := func(key string) uint32 {
		h := uint32(javaHash(key))
		return (h ^ h>>16) & (size - 1)
	}
	out := append(jsonObject(nil), o...)
	sort.SliceStable(out, func(i, j int) bool { return bucket(out[i].key) < bucket(out[j].key) })
	return out
}

// javaOrdered returns v with the members of all its objects in HashMap order.
func javaOrdered(v any) any {
	switch x := v.(type) {
	case jsonObject:
		out := make(jsonObject, len(x))
		for i, m := range x {
			out[i] = jsonMember{m.key, javaOrdered(m.value)}
		}
		return javaMapOrder(out)
	case jsonArray:
		return jsonArray(javaOrdered([]any(x)).([]any))
	case []any:
		out := make([]any, len(x))
		for i, e := range x {
			out[i] = javaOrdered(e)
		}
		return out
	}
	return v
}
