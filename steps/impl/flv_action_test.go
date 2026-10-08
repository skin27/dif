package impl

import (
	"strings"
	"testing"

	"dif/message"
	stepdef "dif/steps/definition"
)

// exampleFlvRules are the rules of examples/flv.json, as the parser passes them.
const exampleFlvRules = `[{"group":true,"matchOn":"HDR","subcollection":[{"field":"header","length":3},{"_id":"c0f03f5f","field":"body","length":"5"}]}]`

func flv(t *testing.T, opts map[string]any, body string) string {
	t.Helper()
	m := process(t, "flv", opts, message.New(body))
	if m[message.ContentType] != "application/xml" {
		t.Errorf("Content-Type = %v", m[message.ContentType])
	}
	return m[message.Body].(string)
}

// TestFlvFromTheRegressionTests uses the flows and requests of the regression
// tests (FLV_default, FLV_groups, FLV_one_group, FLV_two_group, FLV_middle_group),
// where every rule is an option, and the answers of the Java platform.
func TestFlvFromTheRegressionTests(t *testing.T) {
	const type4 = "Type[2]Regelnr[4]Content[11]Controlenr[10]"
	tests := []struct {
		name string
		opts map[string]any
		body string
		want string
	}{
		{"default", map[string]any{"YAY": "header[3]body[5]"}, "YAYthing",
			`<flv-message><rule matchOn="YAY" fields="header[3]body[5]" /><segment><header>YAY</header><body>thing</body></segment></flv-message>`},
		{"groups: every line of a group rule has a group", map[string]any{"_group_YAY": "YAYheader[3]YAYbody[5]", "_group_HDR": "HDRheader[3]HDRbody[5]"},
			"YAYthing\nHDRbody1234567890\nHDRbody2345678901",
			`<flv-message><rule matchOn="YAY" fields="YAYheader[3]YAYbody[5]" /><rule matchOn="HDR" fields="HDRheader[3]HDRbody[5]" />` +
				`<group><segment><YAYheader>YAY</YAYheader><YAYbody>thing</YAYbody></segment></group>` +
				`<group><segment><HDRheader>HDR</HDRheader><HDRbody>body1</HDRbody></segment></group>` +
				`<group><segment><HDRheader>HDR</HDRheader><HDRbody>body2</HDRbody></segment></group></flv-message>`},
		// The platform lists HDR before YAY here. Its order is not that of the options, nor of their names, and a flow
		// cannot tell the order of its options: rules are listed longest matchOn first, then in reverse alphabetical order.
		{"one group: the lines of a plain rule join the open group", map[string]any{"_group_YAY": "YAYheader[3]YAYbody[5]", "HDR": "HDRheader[3]HDRbody[5]"},
			"YAYthin1\nYAYthin2\nHDRbody1234567890\nHDRbody2345678901",
			`<flv-message><rule matchOn="YAY" fields="YAYheader[3]YAYbody[5]" /><rule matchOn="HDR" fields="HDRheader[3]HDRbody[5]" />` +
				`<group><segment><YAYheader>YAY</YAYheader><YAYbody>thin1</YAYbody></segment></group>` +
				`<group><segment><YAYheader>YAY</YAYheader><YAYbody>thin2</YAYbody></segment>` +
				`<segment><HDRheader>HDR</HDRheader><HDRbody>body1</HDRbody></segment>` +
				`<segment><HDRheader>HDR</HDRheader><HDRbody>body2</HDRbody></segment></group></flv-message>`},
		{"two groups", map[string]any{"_group_YAY": "YAYheader[3]YAYbody[5]", "HDR": "HDRheader[3]HDRbody[5]", "_group_ABC": "line1[3]line2[3]"},
			"YAYthin1\nHDRbody1234567890\nABCline\nABCline",
			`<flv-message><rule matchOn="YAY" fields="YAYheader[3]YAYbody[5]" /><rule matchOn="HDR" fields="HDRheader[3]HDRbody[5]" /><rule matchOn="ABC" fields="line1[3]line2[3]" />` +
				`<group><segment><YAYheader>YAY</YAYheader><YAYbody>thin1</YAYbody></segment>` +
				`<segment><HDRheader>HDR</HDRheader><HDRbody>body1</HDRbody></segment></group>` +
				`<group><segment><line1>ABC</line1><line2>lin</line2></segment></group>` +
				`<group><segment><line1>ABC</line1><line2>lin</line2></segment></group></flv-message>`},
		// A plain rule with no group open is a segment of its own; a line no rule matches (R3) is left out.
		{"middle group", map[string]any{"R1": type4, "_group_R2": type4, "HDR": type4},
			"R1A001 Amsterdam 1234567\nR2B001 020 1234567\nR2B002 021 1234567\nR3C001 ABC 1234567",
			`<flv-message><rule matchOn="HDR" fields="` + type4 + `" /><rule matchOn="R2" fields="` + type4 + `" /><rule matchOn="R1" fields="` + type4 + `" />` +
				`<segment><Type>R1</Type><Regelnr>A001</Regelnr><Content>Amsterdam</Content><Controlenr>1234567</Controlenr></segment>` +
				`<group><segment><Type>R2</Type><Regelnr>B001</Regelnr><Content>020 123456</Content><Controlenr>7</Controlenr></segment></group>` +
				`<group><segment><Type>R2</Type><Regelnr>B002</Regelnr><Content>021 123456</Content><Controlenr>7</Controlenr></segment></group></flv-message>`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := flv(t, tt.opts, tt.body); got != tt.want {
				t.Errorf("got  %s\nwant %s", got, tt.want)
			}
		})
	}
}

func TestFlvRulesList(t *testing.T) {
	want := `<flv-message><rule matchOn="HDR" fields="header[3]body[5]" /><group><segment><header>HDR</header><body>thing</body></segment></group></flv-message>`
	if got := flv(t, map[string]any{"rules": exampleFlvRules}, "HDRthing"); got != want {
		t.Errorf("got  %s\nwant %s", got, want)
	}

	// The rules are tried in the order of the list, also those with the shorter matchOn first, and one without matchOn matches every line.
	rules := `[
		{"matchOn": "HDR", "group": true, "subcollection": [{"field": "type", "length": 3}, {"field": "date", "length": 8}]},
		{"matchOn": "DTL", "name": "ignored", "group": "true", "subcollection": [{"field": "type", "length": 3}, {"field": "item", "length": 6}, {"field": "qty", "length": 3}]},
		{"matchOn": "TRL", "subcollection": [{"field": "type", "length": 3}, {"field": "count", "length": 5, "ignored": 1}]},
		{"matchOn": "", "subcollection": [{"field": "x y", "length": 2}]}
	]`
	body := "HDR20240102\r\n" +
		"\n" +
		"DTLapple  12 \n" +
		"DTLpear    5\n" +
		"TRL  2   \n" +
		"HDR20240103\n" +
		"zzNoRule\n"
	want = `<flv-message>` +
		`<rule matchOn="HDR" fields="type[3]date[8]" /><rule matchOn="DTL" fields="type[3]item[6]qty[3]" /><rule matchOn="TRL" fields="type[3]count[5]" /><rule matchOn="" fields="x y[2]" />` +
		`<group><segment><type>HDR</type><date>20240102</date></segment></group>` +
		`<group><segment><type>DTL</type><item>apple</item><qty>12</qty></segment></group>` +
		`<group><segment><type>DTL</type><item>pear</item><qty>5</qty></segment>` +
		`<segment><type>TRL</type><count>2</count></segment></group>` + // five characters of the line, trimmed; it joins the open group
		`<group><segment><type>HDR</type><date>20240103</date></segment>` +
		`<segment><x_y>zz</x_y></segment></group>` + // the rule without matchOn is the fallback
		`</flv-message>`
	if got := flv(t, map[string]any{"rules": rules}, body); got != want {
		t.Errorf("body:\n got %v\nwant %s", got, want)
	}

	// The rules of the list come first, then the options in their order.
	got := flv(t, map[string]any{"rules": exampleFlvRules, "ABC": "x[3]", "ABCD": "y[4]"}, "ABCDz")
	if !strings.HasPrefix(got, `<flv-message><rule matchOn="HDR" fields="header[3]body[5]" /><rule matchOn="ABCD" fields="y[4]" /><rule matchOn="ABC" fields="x[3]" />`) ||
		!strings.Contains(got, `<segment><y>ABCD</y></segment>`) {
		t.Errorf("rules and options: %s", got)
	}
}

func TestFlvFieldsAndEscaping(t *testing.T) {
	opts := map[string]any{"rules": `[{"matchOn": "A", "subcollection": [{"field": "a", "length": 2}, {"field": "b", "length": 3}, {"field": "c", "length": 2}]}]`}
	const head = `<flv-message><rule matchOn="A" fields="a[2]b[3]c[2]" />`
	for body, want := range map[string]string{
		"A<&>xyz":      "<a>A&lt;</a><b>&amp;&gt;x</b><c>yz</c>", // cut by characters, then escaped
		"Aé€ü":         "<a>Aé</a><b>€ü</b><c/>",                 // by characters, not bytes
		"A":            "<a>A</a><b/><c/>",                       // a short line leaves the rest empty
		"A 1  2 3 456": "<a>A</a><b>1</b><c>2</c>",               // values are trimmed; the rest is ignored
	} {
		if got := flv(t, opts, body); got != head+"<segment>"+want+"</segment></flv-message>" {
			t.Errorf("%q:\n got %v\nwant %s", body, got, want)
		}
	}
	if got := flv(t, opts, ""); got != head+"</flv-message>" {
		t.Errorf("empty body = %v", got)
	}
	// A line no rule matches is left out.
	if got := flv(t, opts, "B12\nA12\nC34"); got != head+"<segment><a>A1</a><b>2</b><c/></segment></flv-message>" {
		t.Errorf("lines without a rule: %v", got)
	}
}

func TestFlvInvalidRules(t *testing.T) {
	for rules, want := range map[string]string{
		`not json`:           "option rules: want a JSON list of rules",
		`{}`:                 "option rules: want a JSON list of rules",
		`[{"matchOn": "A"}]`: "rule 1 has no subcollection",
		`[{"subcollection": [{"field": "a", "length": 1}]}, {"subcollection": []}]`:       "rule 2 has no subcollection",
		`[{"subcollection": [{"length": 3}]}]`:                                            "rule 1, field 1: want a field name and a length",
		`[{"subcollection": [{"field": "a", "length": 1}, {"field": "b", "length": 0}]}]`: "rule 1, field 2: want a field name and a length",
		`[{"subcollection": [{"field": "a", "length": "x"}]}]`:                            "option rules",
		`[{"group": "maybe", "subcollection": [{"field": "a", "length": 1}]}]`:            "option rules",
	} {
		wantInvalid(t, stepdef.Action, "flv", map[string]any{"rules": rules}, want)
	}
	wantInvalid(t, stepdef.Action, "flv", map[string]any{"rules": `[]`}, "no rules")
	wantInvalid(t, stepdef.Action, "flv", nil, "no rules")
	for _, spec := range []any{"header", "header[x]", "[3]", "header[3]x", 3.0, true} {
		wantInvalid(t, stepdef.Action, "flv", map[string]any{"HDR": spec}, "option HDR: want the fields of a rule")
	}
	wantInvalid(t, stepdef.Action, "flv", map[string]any{"HDR": "header[0]"}, "option HDR: field header has a length of 0")
}
