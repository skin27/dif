package mapping

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestXMLMappings(t *testing.T) {
	xmlf := Format{Name: "xml", MediaType: "application/xml"}
	jf := Format{Name: "json", MediaType: "application/json"}
	p, e := Compile(context.Background(), Definition{Language: "difmap/1", Expression: `{id:attr(payload,"id"), lines:children(payload,"{urn:test}Item") map (item) -> {sku:attr(item,"sku"), quantity:number(text(item))}}`}, xmlf, jf, "", Limits{})
	if e != nil {
		t.Fatal(e)
	}
	out, e := p.Apply(context.Background(), `<Order xmlns:p="urn:test" id="A"><p:Item sku="X">2</p:Item><p:Item sku="Y">3</p:Item></Order>`, "", nil)
	if e != nil || out != `{"id":"A","lines":[{"sku":"X","quantity":2},{"sku":"Y","quantity":3}]}` {
		t.Fatalf("XML map: %s, %v", out, e)
	}
	p, e = Compile(context.Background(), Definition{Language: "difmap/1", Expression: `element("{urn:test}Order", {id:payload.id}, [textNode("before & "), element("Item", {}, [textNode(payload.name)]), textNode(" after")])`}, jf, xmlf, "", Limits{})
	if e != nil {
		t.Fatal(e)
	}
	out, e = p.Apply(context.Background(), `{"id":"A","name":"<x>"}`, "", nil)
	if e != nil || !strings.Contains(out, `<n0:Order xmlns:n0="urn:test" id="A">before &amp; <Item>&lt;x&gt;</Item> after</n0:Order>`) {
		t.Fatalf("XML writer: %s, %v", out, e)
	}
	p, e = Compile(context.Background(), Definition{Language: "identity"}, xmlf, xmlf, "", Limits{})
	if e != nil {
		t.Fatal(e)
	}
	for _, body := range []string{`<r><!--private--></r>`, `<!DOCTYPE r><r/>`, `<r><?test value?></r>`, `<r/><r/>`, `<a><b></a>`, `<p:r/>`, `<r a="1" a="2"/>`, `<r xmlns:p="urn:x" xmlns:q="urn:x" p:a="1" q:a="2"/>`} {
		if _, e := p.Apply(context.Background(), body, "", nil); e == nil {
			t.Fatalf("accepted invalid XML: %s", body)
		}
	}
	out, e = p.Apply(context.Background(), `<?xml version="1.0" encoding="UTF-8"?><r>before<a/>after</r>`, "", nil)
	if e != nil || out != `<r>before<a></a>after</r>` {
		t.Fatalf("mixed content: %s, %v", out, e)
	}
	p, e = Compile(context.Background(), Definition{Language: "difmap/1", Expression: `text(payload)`}, xmlf, jf, "", Limits{})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = p.Apply(context.Background(), `<r>a<b/>c</r>`, "", nil); e == nil {
		t.Fatal("mixed content flattened")
	}
}
func TestCSVMappings(t *testing.T) {
	jf := Format{Name: "json", MediaType: "application/json"}
	cf := Format{Name: "csv", MediaType: "text/csv", Writer: map[string]json.RawMessage{"columns": json.RawMessage(`["id","name","missing"]`), "lineEnding": json.RawMessage(`"crlf"`)}}
	p, e := Compile(context.Background(), Definition{Language: "identity"}, jf, cf, "", Limits{})
	if e != nil {
		t.Fatal(e)
	}
	out, e := p.Apply(context.Background(), `[{"name":"a,b","id":9007199254740993},{"id":2,"name":"line\nvalue","missing":null}]`, "", nil)
	want := "id,name,missing\r\n9007199254740993,\"a,b\",\r\n2,\"line\r\nvalue\",\r\n"
	if e != nil || out != want {
		t.Fatalf("CSV: %q (%v)", out, e)
	}
	p, e = Compile(context.Background(), Definition{Language: "difmap/1", Expression: `payload map (row) -> {id:number(row.id), name:row.name}`}, cf, jf, "", Limits{})
	if e != nil {
		t.Fatal(e)
	}
	out, e = p.Apply(context.Background(), "id,name\n1,\"a,b\"\n2,c\n", "", nil)
	if e != nil || out != `[{"id":1,"name":"a,b"},{"id":2,"name":"c"}]` {
		t.Fatalf("CSV read: %s (%v)", out, e)
	}
	for _, body := range []string{"id,id\n1,2", ",name\n1,2", "id,name\n1,2,3", ""} {
		if _, e = p.Apply(context.Background(), body, "", nil); e == nil {
			t.Fatalf("invalid CSV: %q", body)
		}
	}
	bad := cf
	bad.Reader = map[string]json.RawMessage{"guess": json.RawMessage(`true`)}
	if e = ValidateFormat(bad, Limits{}); e == nil {
		t.Fatal("unknown setting accepted")
	}
	cf.Writer = nil
	if _, e = Compile(context.Background(), Definition{Language: "identity"}, jf, cf, "", Limits{}); e == nil {
		t.Fatal("CSV writer without columns accepted")
	}
}
func TestXSLTAdapter(t *testing.T) {
	f := Format{Name: "xml", MediaType: "application/xml"}
	src := `<xsl:stylesheet xmlns:xsl="http://www.w3.org/1999/XSL/Transform" version="2.0"><xsl:output method="xml" omit-xml-declaration="yes"/><xsl:param name="who"/><xsl:template match="/"><result><xsl:value-of select="$who"/></result></xsl:template></xsl:stylesheet>`
	d := Definition{Language: "xslt", SourceRef: "sheet", Expression: src}
	p, e := Compile(context.Background(), d, f, f, "", Limits{})
	if e != nil {
		t.Fatal(e)
	}
	out, e := p.Apply(context.Background(), `<root/>`, "", map[string]any{"who": "Ada"})
	if e != nil || out != `<result>Ada</result>` {
		t.Fatalf("XSLT: %s, %v", out, e)
	}
	d.Expression = strings.Replace(src, `method="xml"`, `method="text"`, 1)
	if _, e = Compile(context.Background(), d, f, f, "", Limits{}); e == nil {
		t.Fatal("conflicting XSLT output accepted")
	}
	d.Expression = `<xsl:stylesheet xmlns:xsl="http://www.w3.org/1999/XSL/Transform" version="2.0"><xsl:include href="file:///private"/></xsl:stylesheet>`
	if _, e = Compile(context.Background(), d, f, f, "", Limits{}); e == nil {
		t.Fatal("external stylesheet accepted")
	}
}
