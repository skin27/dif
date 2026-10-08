package impl

import (
	"testing"

	"dif/message"
	stepdef "dif/steps/definition"
)

// exampleFlvRules are the rules of examples/flv.json, as the parser passes them.
const exampleFlvRules = `[{"group":true,"matchOn":"HDR","subcollection":[{"field":"header","length":3},{"_id":"c0f03f5f","field":"body","length":"5"}]}]`

func TestFlv(t *testing.T) {
	m := process(t, "flv", map[string]any{"rules": exampleFlvRules}, message.New("HDRthing"))
	want := "<flv><group><HDR><header>HDR</header><body>thing</body></HDR></group></flv>"
	if m[message.Body] != want || m[message.ContentType] != "application/xml" {
		t.Errorf("body = %v (%v), want %s", m[message.Body], m[message.ContentType], want)
	}

	rules := `[
		{"matchOn": "HDR", "group": true, "subcollection": [{"field": "type", "length": 3}, {"field": "date", "length": 8}]},
		{"matchOn": "DTL", "name": "line", "group": "true", "subcollection": [{"field": "type", "length": 3}, {"field": "item", "length": 6}, {"field": "qty", "length": 3}]},
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
	want = "<flv>" +
		"<group><HDR><type>HDR</type><date>20240102</date></HDR></group>" +
		"<group><line><type>DTL</type><item>apple</item><qty>12</qty></line><line><type>DTL</type><item>pear</item><qty>5</qty></line></group>" +
		"<TRL><type>TRL</type><count>2</count></TRL>" + // five characters of the line, trimmed
		"<group><HDR><type>HDR</type><date>20240103</date></HDR></group>" +
		"<record><x_y>zz</x_y></record>" + // the rule without matchOn is the fallback
		"</flv>"
	if got := process(t, "flv", map[string]any{"rules": rules}, message.New(body))[message.Body]; got != want {
		t.Errorf("body:\n got %v\nwant %s", got, want)
	}
}

func TestFlvFieldsAndEscaping(t *testing.T) {
	opts := map[string]any{"rules": `[{"matchOn": "A", "subcollection": [{"field": "a", "length": 2}, {"field": "b", "length": 3}, {"field": "c", "length": 2}]}]`}
	for body, want := range map[string]string{
		"A<&>xyz":      "<A><a>A&lt;</a><b>&amp;&gt;x</b><c>yz</c></A>", // cut by characters, then escaped
		"Aé€ü":         "<A><a>Aé</a><b>€ü</b><c/></A>",                 // by characters, not bytes
		"A":            "<A><a>A</a><b/><c/></A>",                       // a short line leaves the rest empty
		"A 1  2 3 456": "<A><a>A</a><b>1</b><c>2</c></A>",               // values are trimmed; the rest is ignored
	} {
		if got := process(t, "flv", opts, message.New(body))[message.Body]; got != "<flv>"+want+"</flv>" {
			t.Errorf("%q:\n got %v\nwant %s", body, got, want)
		}
	}
	if got := process(t, "flv", opts, message.New(""))[message.Body]; got != "<flv></flv>" {
		t.Errorf("empty body = %v", got)
	}

	p := mustProcessor(t, stepdef.Action, "flv", opts).(stepdef.ActionProcessor)
	if _, err := p.Process(t.Context(), message.New("A12\nB34")); err == nil || err.Error() != "line 2: no rule matches" {
		t.Errorf("err = %v, want line 2: no rule matches", err)
	}
}

func TestFlvInvalidRules(t *testing.T) {
	for rules, want := range map[string]string{
		`not json`:           "option rules: want a JSON list of rules",
		`{}`:                 "option rules: want a JSON list of rules",
		`[]`:                 "option rules: no rules",
		`[{"matchOn": "A"}]`: "rule 1 has no subcollection",
		`[{"subcollection": [{"field": "a", "length": 1}]}, {"subcollection": []}]`:       "rule 2 has no subcollection",
		`[{"subcollection": [{"length": 3}]}]`:                                            "rule 1, field 1: want a field name and a length",
		`[{"subcollection": [{"field": "a", "length": 1}, {"field": "b", "length": 0}]}]`: "rule 1, field 2: want a field name and a length",
		`[{"subcollection": [{"field": "a", "length": "x"}]}]`:                            "option rules",
		`[{"group": "maybe", "subcollection": [{"field": "a", "length": 1}]}]`:            "option rules",
	} {
		wantInvalid(t, stepdef.Action, "flv", map[string]any{"rules": rules}, want)
	}
	wantInvalid(t, stepdef.Action, "flv", nil, "missing required option rules")
}
