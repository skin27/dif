package impl

import (
	"testing"

	"dif/message"
	stepdef "dif/steps/definition"
)

func TestEDIToXMLAndBack(t *testing.T) {
	opts := map[string]any{"segment": "LB", "field": "~", "component": "^", "subComponent": "!"} // as in testdata/examples/editoxml.json
	edi := "CUS~John^Doe~1901-01-07~john.doe@example.com\r\nADR~~Main St!12^Town~a<b&c\n"
	m := process(t, "editoxml", opts, message.New(edi))
	want := `<edi-message><delimiters segment="LB" field="~" component="^" sub-component="!"/>` +
		`<CUS><field.1><component.1>John</component.1><component.2>Doe</component.2></field.1><field.2>1901-01-07</field.2><field.3>john.doe@example.com</field.3></CUS>` +
		`<ADR><field.1></field.1><field.2><component.1><sub-component.1>Main St</sub-component.1><sub-component.2>12</sub-component.2></component.1><component.2>Town</component.2></field.2><field.3>a&lt;b&amp;c</field.3></ADR>` +
		`</edi-message>`
	if m[message.Body] != want || m[message.ContentType] != "application/xml" {
		t.Fatalf("xml = %s\nwant  %s", m[message.Body], want)
	}

	back := process(t, "xmltoedi", nil, m)
	if back[message.Body] != "CUS~John^Doe~1901-01-07~john.doe@example.com\nADR~~Main St!12^Town~a<b&c" {
		t.Errorf("edi = %q", back[message.Body])
	}

	// testdata/examples/xmltoedi.json, indented; and EDIFACT-like delimiters that end every segment.
	xml := "<edi-message>\n\t<delimiters segment=\"'\" field=\"+\" component=\":\" sub-component=\"!\"/>\n\t<CUS>\n\t\t<field.1>\n\t\t\t<component.1>John</component.1>\n\t\t\t<component.2>Doe</component.2>\n\t\t</field.1>\n\t\t<field.3>x</field.3>\n\t</CUS>\n\t<END/>\n</edi-message>"
	if got := process(t, "xmltoedi", nil, message.New(xml))[message.Body]; got != "CUS+John:Doe++x'END'" {
		t.Errorf("edi = %q", got)
	}
	tick := process(t, "editoxml", map[string]any{"segment": "'", "field": "+", "component": ":"}, message.New("UNH+1+ORDERS:D:96A'BGM+220'"))
	if tick[message.Body] != `<edi-message><delimiters segment="'" field="+" component=":" sub-component="!"/><UNH><field.1>1</field.1><field.2><component.1>ORDERS</component.1><component.2>D</component.2><component.3>96A</component.3></field.2></UNH><BGM><field.1>220</field.1></BGM></edi-message>` {
		t.Errorf("xml = %s", tick[message.Body])
	}

	wantInvalid(t, stepdef.Action, "editoxml", map[string]any{"field": "^"}, "options field and component: same delimiter")
	wantInvalid(t, stepdef.Action, "editoxml", map[string]any{"field": ""}, "option field: empty delimiter")
	for body, want := range map[string]string{
		"1BAD~x": `segment 1: name "1BAD" is not an XML name`,
	} {
		p := mustProcessor(t, stepdef.Action, "editoxml", nil).(stepdef.ActionProcessor)
		if _, err := p.Process(t.Context(), message.New(body)); err == nil || err.Error() != want {
			t.Errorf("%q: err = %v, want %q", body, err, want)
		}
	}
	p := mustProcessor(t, stepdef.Action, "xmltoedi", nil).(stepdef.ActionProcessor)
	if _, err := p.Process(t.Context(), message.New("<edi-message><CUS><name>x</name></CUS></edi-message>")); err == nil || err.Error() != "segment CUS: element <name>: want <field.N>" {
		t.Errorf("err = %v", err)
	}
}
