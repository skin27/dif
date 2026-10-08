package impl

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/knroy/go-xml/xdm"
	"github.com/knroy/go-xml/xsd"

	"dif/message"
	stepdef "dif/steps/definition"
)

// schemaValidationException is the class Camel's validator throws, which the
// platform writes in the body of a message that is not valid. Flows tell the
// two outcomes apart by it ("${bodyAs(String)} contains 'SchemaValidationException'").
const schemaValidationException = "org.apache.camel.processor.validation.SchemaValidationException"

// xmlValidatorAction validates an XML body against an XML Schema, by
// github.com/knroy/go-xml (xsd.Load, Validate). It does not fail a message that
// is not valid: as on the platform the problems become its body, so that a
// router after it can send it somewhere else. A schema that does not load
// fails the build.
//
// xs:import and xs:include stay closed (xsd.Options has no resolver): a schema
// that is somebody's input must not read the files of the host.
type xmlValidatorAction struct {
	schema *xsd.Schema
}

func newXMLValidatorAction(_ string, p stepdef.Params) (stepdef.Processor, error) {
	inline, file := p["resource"].(string), p["path"].(string)
	var src string
	switch {
	case (inline == "") == (file == ""):
		return nil, fmt.Errorf("set one of the options resource (xmlvalidator:ref:<name>) and path")
	case inline != "":
		src = inline
	default:
		data, err := os.ReadFile(file)
		if err != nil {
			return nil, fmt.Errorf("option path: %w", err)
		}
		src = string(data)
	}
	tree, err := xdm.ParseString(src, xdm.ParseOptions{})
	if err != nil {
		return nil, fmt.Errorf("schema is not XML: %s", firstLine(err))
	}
	schema, err := xsd.Load(tree.Root, "", xsd.Options{})
	if err != nil {
		return nil, fmt.Errorf("schema: %s", firstLine(err))
	}
	return xmlValidatorAction{schema}, nil
}

func (a xmlValidatorAction) Process(ctx context.Context, m message.Message) (message.Message, error) {
	tree, err := xdm.ParseString(string(bytesOf(m[message.Body])), xdm.ParseOptions{TrackPositions: true})
	if err != nil {
		m[message.Body] = schemaValidationException + ": Validation failed: body is not XML: " + firstLine(err)
		return m, nil
	}
	err = a.schema.ValidateContext(ctx, tree.Root, xsd.ValidateOptions{})
	var problems *xsd.ValidationErrors
	switch {
	case err == nil:
		return m, nil
	case errors.As(err, &problems):
		var b strings.Builder
		fmt.Fprintf(&b, "%s: Validation failed with %d errors:", schemaValidationException, len(problems.Errors))
		for _, e := range problems.Errors {
			b.WriteString("\n  ")
			b.WriteString(e.Error())
		}
		m[message.Body] = b.String()
		return m, nil
	}
	return nil, fmt.Errorf("xmlvalidator: %s", firstLine(err))
}
