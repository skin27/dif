package impl

import (
	"strings"
	"testing"
)

func TestJavaHash(t *testing.T) {
	// Values from Java: "".hashCode() is 0, "a" is 97, "Hello" is 69609650,
	// and a long string wraps around the int.
	for s, want := range map[string]int32{"": 0, "a": 97, "Hello": 69609650, "polygenelubricants": -2147483648, "é": 233, "😀": 1772899} {
		if got := javaHash(s); got != want {
			t.Errorf("javaHash(%q) = %d, want %d", s, got, want)
		}
	}
}

func TestJavaMapOrder(t *testing.T) {
	keys := func(o jsonObject) string {
		var k []string
		for _, m := range o {
			k = append(k, m.key)
		}
		return strings.Join(k, " ")
	}
	object := func(keys ...string) jsonObject {
		var o jsonObject
		for _, k := range keys {
			o = append(o, jsonMember{k, nil})
		}
		return o
	}
	// The order of the keys of the product the platform converted to YAML, at
	// the top (15 keys, a table of 32) and inside it.
	for _, tt := range []struct{ in, want string }{
		{"id name description category price stock dimensions weight manufacturer tags specifications reviews availability createdAt updatedAt",
			"description weight availability specifications manufacturer tags createdAt reviews price name id category stock dimensions updatedAt"},
		{"value unit", "unit value"},
		{"currency amount vatIncluded", "amount vatIncluded currency"},
		{"width height depth unit", "unit depth width height"},
		{"power voltage waterTank programs", "power programs waterTank voltage"},
		{"name country support", "country name support"},
		{"email phone", "phone email"},
		{"user rating comment", "rating comment user"},
		{"location quantity", "quantity location"},
		{"capacity unit", "unit capacity"},
		{"online stores", "stores online"},
		{"rows", "rows"},
	} {
		if got := keys(javaMapOrder(object(strings.Fields(tt.in)...))); got != tt.want {
			t.Errorf("order of %q = %q, want %q", tt.in, got, tt.want)
		}
	}
	if got := javaBuckets(12); got != 16 {
		t.Errorf("buckets(12) = %d, want 16", got)
	}
	if got := javaBuckets(13); got != 32 {
		t.Errorf("buckets(13) = %d, want 32: the 13th entry doubles the table", got)
	}
}
