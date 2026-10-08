package impl

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/knroy/go-xml/xdm"

	"dif/message"
	stepdef "dif/steps/definition"
)

// The schema of the regression tests CB_Schematron_Valid and CB_Schematron_Invalid.
const sectionSchema = `<?xml version="1.0" encoding="UTF-8"?>
<schema xmlns="http://purl.oclc.org/dsdl/schematron">
   <title>Check Sections 12/07</title>
   <pattern id="section-check">
      <rule context="section">
         <assert test="title">This section has no title</assert>
         <assert test="para">This section has no paragraphs</assert>
      </rule>
   </pattern>
</schema>`

func schematronRun(t *testing.T, schema, body string) message.Message {
	t.Helper()
	return process(t, "schematron", map[string]any{"resource": schema}, message.New(body))
}

func TestSchematronValid(t *testing.T) {
	body := `<document><section><title>De titel.</title><para>Tekst.</para></section></document>`
	out := schematronRun(t, sectionSchema, body)
	if out[schematronStatus] != "SUCCESS" {
		t.Errorf("status = %v", out[schematronStatus])
	}
	if out[message.Body] != body {
		t.Errorf("the body changed: %v", out[message.Body])
	}
	report := out[schematronReport].(string)
	for _, want := range []string{`<svrl:schematron-output`, `title="Check Sections 12/07"`, `<svrl:active-pattern`, `id="section-check"`, `<svrl:fired-rule context="section"/>`} {
		if !strings.Contains(report, want) {
			t.Errorf("report lacks %q:\n%s", want, report)
		}
	}
	if strings.Contains(report, "failed-assert") {
		t.Errorf("report has a failed assert:\n%s", report)
	}
}

func TestSchematronInvalid(t *testing.T) {
	body := `<document><section><title>Hoofdstuk 1</title></section><section><para>x</para></section></document>`
	out := schematronRun(t, sectionSchema, body)
	if out[schematronStatus] != "FAILED" {
		t.Fatalf("status = %v", out[schematronStatus])
	}
	if out[message.Body] != body {
		t.Errorf("the body changed: %v", out[message.Body])
	}
	report := out[schematronReport].(string)
	for _, want := range []string{
		`<svrl:failed-assert test="para" location="/document[1]/section[1]">`,
		`<svrl:text>This section has no paragraphs</svrl:text>`,
		`<svrl:failed-assert test="title" location="/document[1]/section[2]">`,
		`<svrl:text>This section has no title</svrl:text>`,
	} {
		if !strings.Contains(report, want) {
			t.Errorf("report lacks %q:\n%s", want, report)
		}
	}
	if n := strings.Count(report, "<svrl:failed-assert"); n != 2 {
		t.Errorf("%d failed asserts, want 2:\n%s", n, report)
	}
	if n := strings.Count(report, "<svrl:fired-rule"); n != 2 {
		t.Errorf("%d fired rules, want 2:\n%s", n, report)
	}
}

func TestSchematronLanguage(t *testing.T) {
	cases := []struct {
		name, schema, body string
		failed             int    // failed asserts
		reports            int    // successful reports
		want               string // part of the report
	}{
		{"namespaces", `<schema xmlns="http://purl.oclc.org/dsdl/schematron"><ns prefix="o" uri="urn:order"/>
			<pattern><rule context="o:order"><assert test="o:id">no id</assert></rule></pattern></schema>`,
			`<order xmlns="urn:order"><line/></order>`, 1, 0, "no id"},
		{"report does not fail", `<schema xmlns="http://purl.oclc.org/dsdl/schematron">
			<pattern><rule context="a"><report test="@x">x is set on <name/></report></rule></pattern></schema>`,
			`<a x="1"/>`, 0, 1, "x is set on a"},
		{"report that does not hold", `<schema xmlns="http://purl.oclc.org/dsdl/schematron">
			<pattern><rule context="a"><report test="@x">x</report></rule></pattern></schema>`,
			`<a/>`, 0, 0, ""},
		{"value-of", `<schema xmlns="http://purl.oclc.org/dsdl/schematron">
			<pattern><rule context="item"><assert test="@qty &lt; 10">quantity <value-of select="@qty"/> is too high for <value-of select="@id"/></assert></rule></pattern></schema>`,
			`<l><item id="p1" qty="3"/><item id="p2" qty="42"/></l>`, 1, 0, "quantity 42 is too high for p2"},
		{"let", `<schema xmlns="http://purl.oclc.org/dsdl/schematron"><let name="limit" value="5"/>
			<pattern><rule context="item"><let name="q" value="number(@qty)"/><assert test="$q &lt;= $limit">over <value-of select="$limit"/></assert></rule></pattern></schema>`,
			`<l><item qty="3"/><item qty="9"/></l>`, 1, 0, "over 5"},
		{"first matching rule of a pattern wins", `<schema xmlns="http://purl.oclc.org/dsdl/schematron">
			<pattern><rule context="a[@special]"><assert test="false()">special</assert></rule>
			<rule context="a"><assert test="false()">general</assert></rule></pattern></schema>`,
			`<r><a special="1"/><a/></r>`, 2, 0, "general"},
		{"every pattern fires", `<schema xmlns="http://purl.oclc.org/dsdl/schematron">
			<pattern><rule context="a"><assert test="false()">one</assert></rule></pattern>
			<pattern><rule context="a"><assert test="false()">two</assert></rule></pattern></schema>`,
			`<a/>`, 2, 0, "two"},
		{"context with a path and a union", `<schema xmlns="http://purl.oclc.org/dsdl/schematron">
			<pattern><rule context="/r/a | b"><assert test="false()">hit</assert></rule></pattern></schema>`,
			`<r><a/><x><b/></x><x><a/></x></r>`, 2, 0, "hit"},
		{"context is an attribute", `<schema xmlns="http://purl.oclc.org/dsdl/schematron">
			<pattern><rule context="@id"><assert test="string-length(.) = 3">id <value-of select="."/> is not 3 long</assert></rule></pattern></schema>`,
			`<r><a id="abc"/><a id="abcd"/></r>`, 1, 0, "id abcd is not 3 long"},
		{"xpath 2", `<schema xmlns="http://purl.oclc.org/dsdl/schematron" queryBinding="xslt2">
			<pattern><rule context="r"><assert test="every $n in a/@n satisfies xs:integer($n) gt 0">non-positive</assert></rule></pattern></schema>`,
			`<r><a n="1"/><a n="0"/></r>`, 1, 0, "non-positive"},
		{"assert id and role", `<schema xmlns="http://purl.oclc.org/dsdl/schematron">
			<pattern><rule context="a"><assert id="A1" role="fatal" test="false()">no</assert></rule></pattern></schema>`,
			`<a/>`, 1, 0, `id="A1" role="fatal"`},
		{"text is tidied and markup in it kept as text", `<schema xmlns="http://purl.oclc.org/dsdl/schematron">
			<pattern><rule context="a"><assert test="false()">  a   <emph>very</emph>
			   bad &amp; wrong </assert></rule></pattern></schema>`,
			`<a/>`, 1, 0, "a very bad &amp; wrong"},
		{"no node matches", `<schema xmlns="http://purl.oclc.org/dsdl/schematron">
			<pattern><rule context="nothing"><assert test="false()">x</assert></rule></pattern></schema>`,
			`<a/>`, 0, 0, "active-pattern"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := schematronRun(t, c.schema, c.body)
			report := out[schematronReport].(string)
			if n := strings.Count(report, "<svrl:failed-assert"); n != c.failed {
				t.Errorf("%d failed asserts, want %d:\n%s", n, c.failed, report)
			}
			if n := strings.Count(report, "<svrl:successful-report"); n != c.reports {
				t.Errorf("%d successful reports, want %d:\n%s", n, c.reports, report)
			}
			wantStatus := "SUCCESS"
			if c.failed > 0 {
				wantStatus = "FAILED"
			}
			if out[schematronStatus] != wantStatus {
				t.Errorf("status = %v, want %s", out[schematronStatus], wantStatus)
			}
			if !strings.Contains(report, c.want) {
				t.Errorf("report lacks %q:\n%s", c.want, report)
			}
		})
	}
}

func TestSchematronMarkupIsEscapedInTheReport(t *testing.T) {
	schema := `<schema xmlns="http://purl.oclc.org/dsdl/schematron"><pattern id='p"1'>
		<rule context="a"><assert test="@v = 'x' and 1 &lt; 2">bad <value-of select="@v"/></assert></rule></pattern></schema>`
	out := schematronRun(t, schema, `<a v="&lt;&amp;&quot;"/>`)
	report := out[schematronReport].(string)
	for _, want := range []string{`test="@v = 'x' and 1 &lt; 2"`, `id="p&quot;1"`, `bad &lt;&amp;"`} {
		if !strings.Contains(report, want) {
			t.Errorf("report lacks %q:\n%s", want, report)
		}
	}
	if _, err := xdm.ParseString(report, xdm.ParseOptions{}); err != nil {
		t.Errorf("the report is not XML: %v\n%s", err, report)
	}
}

func TestSchematronSchemaFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sections.sch")
	if err := os.WriteFile(path, []byte(sectionSchema), 0o600); err != nil {
		t.Fatal(err)
	}
	out := process(t, "schematron", map[string]any{"path": path}, message.New(`<section/>`))
	if out[schematronStatus] != "FAILED" {
		t.Errorf("status = %v", out[schematronStatus])
	}
}

func TestSchematronInvalidOptions(t *testing.T) {
	sch := func(inner string) map[string]any {
		return map[string]any{"resource": `<schema xmlns="http://purl.oclc.org/dsdl/schematron">` + inner + `</schema>`}
	}
	wantInvalid(t, stepdef.Action, "schematron", nil, "set one of the options resource")
	wantInvalid(t, stepdef.Action, "schematron", map[string]any{"resource": sectionSchema, "path": "x.sch"}, "set one of the options resource")
	wantInvalid(t, stepdef.Action, "schematron", map[string]any{"path": filepath.Join(t.TempDir(), "missing.sch")}, "option path")
	wantInvalid(t, stepdef.Action, "schematron", map[string]any{"resource": "not xml <"}, "schema is not XML")
	wantInvalid(t, stepdef.Action, "schematron", map[string]any{"resource": "<other/>"}, "the root element is not schema")
	wantInvalid(t, stepdef.Action, "schematron", sch(""), "the schema has no pattern")
	wantInvalid(t, stepdef.Action, "schematron", sch(`<include href="x.sch"/>`), "include is not supported")
	wantInvalid(t, stepdef.Action, "schematron", sch(`<pattern abstract="true" id="p"/>`), "abstract patterns")
	wantInvalid(t, stepdef.Action, "schematron", sch(`<pattern is-a="p" id="q"/>`), "abstract patterns")
	wantInvalid(t, stepdef.Action, "schematron", sch(`<pattern><rule abstract="true" id="r"/></pattern>`), "abstract rules")
	wantInvalid(t, stepdef.Action, "schematron", sch(`<pattern><rule context="a"><extends rule="r"/></rule></pattern>`), "extends is not supported")
	wantInvalid(t, stepdef.Action, "schematron", sch(`<pattern><rule><assert test="1"/></rule></pattern>`), "a rule has no context")
	wantInvalid(t, stepdef.Action, "schematron", sch(`<pattern><rule context="a"><assert/></rule></pattern>`), "an assert has no test")
	wantInvalid(t, stepdef.Action, "schematron", sch(`<pattern><rule context="a"><assert test="1 +">x</assert></rule></pattern>`), "assert test")
	wantInvalid(t, stepdef.Action, "schematron", sch(`<pattern><rule context="a["><assert test="1">x</assert></rule></pattern>`), "rule context")
}

func TestSchematronFailsTheMessage(t *testing.T) {
	p := mustProcessor(t, stepdef.Action, "schematron", map[string]any{"resource": sectionSchema}).(stepdef.ActionProcessor)
	if _, err := p.Process(context.Background(), message.New("not xml <")); err == nil || !strings.Contains(err.Error(), "body is not XML") {
		t.Errorf("err = %v, want body is not XML", err)
	}
	// A test that cannot be evaluated is an error in the schema, not a failed assert.
	broken := `<schema xmlns="http://purl.oclc.org/dsdl/schematron"><pattern><rule context="a"><assert test="nofunction(1)">x</assert></rule></pattern></schema>`
	p = mustProcessor(t, stepdef.Action, "schematron", map[string]any{"resource": broken}).(stepdef.ActionProcessor)
	if _, err := p.Process(context.Background(), message.New("<a/>")); err == nil || !strings.Contains(err.Error(), "nofunction") {
		t.Errorf("err = %v, want it to name the function", err)
	}
}

func TestSchematronConcurrent(t *testing.T) {
	p := mustProcessor(t, stepdef.Action, "schematron", map[string]any{"resource": sectionSchema}).(stepdef.ActionProcessor)
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			body, want := `<d><section><title>t</title><para>p</para></section></d>`, "SUCCESS"
			if i%2 == 1 {
				body, want = `<d><section/></d>`, "FAILED"
			}
			out, err := p.Process(context.Background(), message.New(body))
			if err != nil || out[schematronStatus] != want {
				t.Errorf("status = %v, %v, want %s", out[schematronStatus], err, want)
			}
		}()
	}
	wg.Wait()
}
