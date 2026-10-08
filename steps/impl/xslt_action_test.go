package impl

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"dif/message"
	stepdef "dif/steps/definition"
)

const xsltProducts = `<?xml version="1.0" encoding="UTF-8"?>
<xsl:stylesheet version="1.0" xmlns:xsl="http://www.w3.org/1999/XSL/Transform">
	<xsl:output method="xml" indent="yes"/>
	<xsl:template match="/products">
		<products><xsl:apply-templates select="product"/></products>
	</xsl:template>
	<xsl:template match="product"><product><xsl:value-of select="name"/></product></xsl:template>
</xsl:stylesheet>`

const productsXML = `<products><product><id>1</id><name>Bike</name></product><product><id>2</id><name>Chair</name></product></products>`

func xsltOut(t *testing.T, opts map[string]any, m message.Message) string {
	t.Helper()
	out := process(t, "xslt", opts, m)
	s, _ := out[message.Body].(string)
	return strings.Join(strings.Fields(s), "")
}

func TestXSLTTransformsTheBody(t *testing.T) {
	got := xsltOut(t, map[string]any{"resource": xsltProducts}, message.New(productsXML))
	want := `<?xmlversion="1.0"encoding="UTF-8"?><products><product>Bike</product><product>Chair</product></products>`
	if got != want {
		t.Errorf("body = %s, want %s", got, want)
	}
}

func TestXSLTVersion2(t *testing.T) {
	sheet := `<xsl:stylesheet version="2.0" xmlns:xsl="http://www.w3.org/1999/XSL/Transform" xmlns:xs="http://www.w3.org/2001/XMLSchema">
	<xsl:output method="text"/>
	<xsl:template match="/">
		<xsl:variable name="total" select="sum(//price/xs:decimal(.))"/>
		<xsl:value-of select="concat('total=', $total, ' max=', max(//price/xs:decimal(.)))"/>
	</xsl:template>
</xsl:stylesheet>`
	got := xsltOut(t, map[string]any{"resource": sheet}, message.New(`<l><price>1.5</price><price>2.5</price></l>`))
	if got != "total=4.0max=2.5" && got != "total=4max=2.5" {
		t.Errorf("body = %q", got)
	}
}

// A literal result element with xsl:version is a stylesheet by itself, and
// html as the root element makes html the default output method.
func TestXSLTSimplifiedStylesheet(t *testing.T) {
	sheet := `<html xsl:version="1.0" xmlns:xsl="http://www.w3.org/1999/XSL/Transform"><body><xsl:for-each select="menu/food"><p><xsl:value-of select="name"/></p></xsl:for-each></body></html>`
	got := xsltOut(t, map[string]any{"resource": sheet}, message.New(`<menu><food><name>Waffles</name></food><food><name>Toast</name></food></menu>`))
	if !strings.Contains(got, "<p>Waffles</p><p>Toast</p>") {
		t.Errorf("body = %q", got)
	}
}

// Saxon, which the platform runs, indents by three spaces and writes HTML 5.
func TestXSLTWritesWhatSaxonWrites(t *testing.T) {
	xml := `<xsl:stylesheet version="1.0" xmlns:xsl="http://www.w3.org/1999/XSL/Transform"><xsl:output method="xml" indent="yes"/><xsl:template match="/"><root><div><span>x</span></div><p/></root></xsl:template></xsl:stylesheet>`
	out := process(t, "xslt", map[string]any{"resource": xml}, message.New("<a/>"))
	want := "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<root>\n   <div>\n      <span>x</span>\n   </div>\n   <p/>\n</root>"
	if out[message.Body] != want {
		t.Errorf("body = %q, want %q", out[message.Body], want)
	}

	html := `<html xsl:version="1.0" xmlns:xsl="http://www.w3.org/1999/XSL/Transform"><body><p>x</p></body></html>`
	out = process(t, "xslt", map[string]any{"resource": html}, message.New("<a/>"))
	if got := out[message.Body].(string); !strings.HasPrefix(got, "<!DOCTYPE HTML>\n<html>") {
		t.Errorf("body = %q, want it to start with the HTML 5 document type", got)
	}

	// A stylesheet that asks for another version of HTML, or for nothing
	// indented, is left as it is.
	for _, c := range []struct{ out, want string }{
		{`<xsl:output method="html" version="4.01"/>`, "<html>"},
		{`<xsl:output method="html" html-version="5.0" indent="no"/>`, "<!DOCTYPE html>"},
		{`<xsl:output method="xml" indent="no"/>`, "<?xml"},
	} {
		sheet := `<xsl:stylesheet version="2.0" xmlns:xsl="http://www.w3.org/1999/XSL/Transform">` + c.out + `<xsl:template match="/"><html><body/></html></xsl:template></xsl:stylesheet>`
		got := process(t, "xslt", map[string]any{"resource": sheet}, message.New("<a/>"))[message.Body].(string)
		if !strings.HasPrefix(strings.ToLower(got), strings.ToLower(c.want)) || strings.Contains(got, "<!DOCTYPE HTML>\n<!DOCTYPE") || strings.Contains(got, "\n   ") {
			t.Errorf("%s: body = %q, want it to start with %q", c.out, got, c.want)
		}
	}
}

func TestXSLTHeadersAreParameters(t *testing.T) {
	sheet := `<xsl:stylesheet version="1.0" xmlns:xsl="http://www.w3.org/1999/XSL/Transform">
	<xsl:output method="text"/>
	<xsl:param name="who"/>
	<xsl:param name="count"/>
	<xsl:param name="missing" select="'none'"/>
	<xsl:template match="/"><xsl:value-of select="concat($who, ':', $count, ':', $missing)"/></xsl:template>
</xsl:stylesheet>`
	m := message.New(`<a/>`)
	m["who"] = "me"
	m["count"] = 3
	m["not a name"] = "ignored"
	m["raw"] = []byte("ignored")
	if got := xsltOut(t, map[string]any{"resource": sheet}, m); got != "me:3:none" {
		t.Errorf("body = %q", got)
	}
}

func TestXSLTStylesheetFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "products.xsl")
	if err := os.WriteFile(path, []byte(xsltProducts), 0o600); err != nil {
		t.Fatal(err)
	}
	got := xsltOut(t, map[string]any{"path": path}, message.New(productsXML))
	if !strings.Contains(got, "<product>Bike</product>") {
		t.Errorf("body = %q", got)
	}
}

func TestXSLTInvalidOptions(t *testing.T) {
	wantInvalid(t, stepdef.Action, "xslt", nil, "set one of the options resource")
	wantInvalid(t, stepdef.Action, "xslt", map[string]any{"resource": xsltProducts, "path": "x.xsl"}, "set one of the options resource")
	wantInvalid(t, stepdef.Action, "xslt", map[string]any{"path": filepath.Join(t.TempDir(), "missing.xsl")}, "option path")
	wantInvalid(t, stepdef.Action, "xslt", map[string]any{"resource": "not xml <"}, "stylesheet is not XML")
	wantInvalid(t, stepdef.Action, "xslt", map[string]any{"resource": `<xsl:stylesheet version="1.0" xmlns:xsl="http://www.w3.org/1999/XSL/Transform"><xsl:template match="/"><xsl:nothing/></xsl:template></xsl:stylesheet>`}, "stylesheet:")
}

func TestXSLTFailsTheMessage(t *testing.T) {
	p := mustProcessor(t, stepdef.Action, "xslt", map[string]any{"resource": xsltProducts}).(stepdef.ActionProcessor)
	if _, err := p.Process(context.Background(), message.New("not xml <")); err == nil || !strings.Contains(err.Error(), "body is not XML") {
		t.Errorf("err = %v, want body is not XML", err)
	}

	unknown := `<xsl:stylesheet version="1.0" xmlns:xsl="http://www.w3.org/1999/XSL/Transform"><xsl:template match="/"><xsl:value-of select="nofunction(1)"/></xsl:template></xsl:stylesheet>`
	p = mustProcessor(t, stepdef.Action, "xslt", map[string]any{"resource": unknown}).(stepdef.ActionProcessor)
	if _, err := p.Process(context.Background(), message.New("<a/>")); err == nil || !strings.Contains(err.Error(), "nofunction") {
		t.Errorf("err = %v, want it to name the function", err)
	}

	stop := `<xsl:stylesheet version="2.0" xmlns:xsl="http://www.w3.org/1999/XSL/Transform"><xsl:template match="/"><xsl:message terminate="yes">no way</xsl:message></xsl:template></xsl:stylesheet>`
	p = mustProcessor(t, stepdef.Action, "xslt", map[string]any{"resource": stop}).(stepdef.ActionProcessor)
	if _, err := p.Process(context.Background(), message.New("<a/>")); err == nil || !strings.Contains(err.Error(), "no way") {
		t.Errorf("err = %v, want the terminating message", err)
	}
}

// The stylesheet may not read files or the network: a flow's stylesheet can
// come from somebody else.
func TestXSLTReadsNothingOutside(t *testing.T) {
	secret := filepath.Join(t.TempDir(), "secret.xml")
	if err := os.WriteFile(secret, []byte("<s>top secret</s>"), 0o600); err != nil {
		t.Fatal(err)
	}
	sheet := `<xsl:stylesheet version="2.0" xmlns:xsl="http://www.w3.org/1999/XSL/Transform"><xsl:template match="/"><xsl:value-of select="doc('` + secret + `')"/></xsl:template></xsl:stylesheet>`
	p, err := newProcessor(stepdef.Action, "xslt", map[string]any{"resource": sheet})
	if err != nil {
		return // refused at build, which is as good
	}
	out, err := p.(stepdef.ActionProcessor).Process(context.Background(), message.New("<a/>"))
	if err == nil && strings.Contains(out[message.Body].(string), "top secret") {
		t.Errorf("the stylesheet read a file: %v", out[message.Body])
	}
}

func TestXSLTConcurrent(t *testing.T) {
	p := mustProcessor(t, stepdef.Action, "xslt", map[string]any{"resource": xsltProducts}).(stepdef.ActionProcessor)
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 20 {
				out, err := p.Process(context.Background(), message.New(productsXML))
				if err != nil || !strings.Contains(out[message.Body].(string), "<product>Chair</product>") {
					t.Errorf("out = %v, err = %v", out[message.Body], err)
					return
				}
			}
		}()
	}
	wg.Wait()
}
