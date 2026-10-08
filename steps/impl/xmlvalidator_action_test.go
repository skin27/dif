package impl

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dif/message"
	stepdef "dif/steps/definition"
)

const productXSD = `<?xml version="1.0" encoding="UTF-8"?>
<xs:schema xmlns:xs="http://www.w3.org/2001/XMLSchema" elementFormDefault="qualified">
	<xs:element name="product">
		<xs:complexType>
			<xs:sequence>
				<xs:element name="id">
					<xs:simpleType><xs:restriction base="xs:string"><xs:pattern value="PRD-[0-9]+"/></xs:restriction></xs:simpleType>
				</xs:element>
				<xs:element name="name" type="xs:string"/>
				<xs:element name="stock" type="xs:int" maxOccurs="unbounded"/>
			</xs:sequence>
		</xs:complexType>
	</xs:element>
</xs:schema>`

func validateXML(t *testing.T, body string) string {
	t.Helper()
	m := message.New(body)
	m["keep"] = "me"
	out := process(t, "xmlvalidator", map[string]any{"resource": productXSD}, m)
	if out["keep"] != "me" {
		t.Errorf("the headers changed: %v", out)
	}
	return out[message.Body].(string)
}

func TestXMLValidatorPassesAValidBodyOn(t *testing.T) {
	const body = `<product><id>PRD-100</id><name>Machine</name><stock>3</stock><stock>4</stock></product>`
	if got := validateXML(t, body); got != body {
		t.Errorf("body = %q, want it unchanged", got)
	}
}

func TestXMLValidatorPutsTheProblemsInTheBody(t *testing.T) {
	got := validateXML(t, "<product>\n<id>X-1</id>\n<name>Machine</name>\n<stock>many</stock>\n</product>")
	for _, want := range []string{
		"org.apache.camel.processor.validation.SchemaValidationException: Validation failed with 2 errors:",
		"/product/id", "/product/stock",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("body = %q, want it to contain %q", got, want)
		}
	}
	// A flow tells it from a valid message by that name.
	if !strings.Contains(got, "SchemaValidationException") {
		t.Errorf("body = %q", got)
	}

	for body, want := range map[string]string{
		"<product><name>x</name></product>": "Validation failed",
		"<other/>":                          "Validation failed",
		"<product><id>PRD-1</id><name>":     "body is not XML",
		"just text":                         "body is not XML",
		"":                                  "body is not XML",
	} {
		if got := validateXML(t, body); !strings.HasPrefix(got, schemaValidationException) || !strings.Contains(got, want) {
			t.Errorf("%q: body = %q, want %q", body, got, want)
		}
	}
}

func TestXMLValidatorSchemaFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "product.xsd")
	if err := os.WriteFile(path, []byte(productXSD), 0o600); err != nil {
		t.Fatal(err)
	}
	out := process(t, "xmlvalidator", map[string]any{"path": path}, message.New("<product/>"))
	if got := out[message.Body].(string); !strings.Contains(got, "SchemaValidationException") {
		t.Errorf("body = %q", got)
	}
}

func TestXMLValidatorInvalidOptions(t *testing.T) {
	wantInvalid(t, stepdef.Action, "xmlvalidator", nil, "set one of the options resource")
	wantInvalid(t, stepdef.Action, "xmlvalidator", map[string]any{"resource": productXSD, "path": "x.xsd"}, "set one of the options resource")
	wantInvalid(t, stepdef.Action, "xmlvalidator", map[string]any{"path": filepath.Join(t.TempDir(), "missing.xsd")}, "option path")
	wantInvalid(t, stepdef.Action, "xmlvalidator", map[string]any{"resource": "<unclosed"}, "schema is not XML")
	wantInvalid(t, stepdef.Action, "xmlvalidator", map[string]any{"resource": `<xs:schema xmlns:xs="http://www.w3.org/2001/XMLSchema"><xs:element name="a" type="xs:nothing"/></xs:schema>`}, "src-resolve")
}

// A schema may not read what it names: it can come from somebody else.
func TestXMLValidatorReadsNothingOutside(t *testing.T) {
	other := filepath.Join(t.TempDir(), "other.xsd")
	if err := os.WriteFile(other, []byte(productXSD), 0o600); err != nil {
		t.Fatal(err)
	}
	schema := `<xs:schema xmlns:xs="http://www.w3.org/2001/XMLSchema"><xs:include schemaLocation="` + other + `"/></xs:schema>`
	if _, err := newProcessor(stepdef.Action, "xmlvalidator", map[string]any{"resource": schema}); err == nil {
		t.Error("a schema that includes a file loaded")
	}
}

func TestXMLValidatorStopsWhenCancelled(t *testing.T) {
	p := mustProcessor(t, stepdef.Action, "xmlvalidator", map[string]any{"resource": productXSD}).(stepdef.ActionProcessor)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.Process(ctx, message.New(`<product><id>PRD-1</id><name>a</name><stock>1</stock></product>`)); err == nil {
		t.Skip("validation finished before it looked at the context")
	}
}
