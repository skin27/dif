// Package impl contains the built-in step processors. Each step's options are
// described by a JSON Schema in schemas/<name>-<kind>.json.
package impl

import (
	"embed"

	stepdef "dif/steps/definition"
	"dif/steps/registry"
)

//go:embed schemas/*.json
var schemas embed.FS

// Register adds the built-in steps to r.
func Register(r *registry.Registry) error {
	builtins := []struct {
		name, kind string
		new        func(string, stepdef.Params) (stepdef.Processor, error)
	}{
		{"timer", stepdef.Source, newTimerSource},
		{"file", stepdef.Source, newFileSource},
		{"message", stepdef.Source, newMessageSource},
		{"https", stepdef.Source, newHTTPSSource},
		{"queue", stepdef.Source, newQueueSource},
		{"flowlink", stepdef.Source, newFlowLinkSource},
		{"repeater", stepdef.Source, newTimerSource},
		{"log", stepdef.Action, newLogAction},
		{"setbody", stepdef.Action, newSetBodyAction},
		{"setheader", stepdef.Action, newSetHeaderAction},
		{"passthrough", stepdef.Action, newPassthrough},
		{"setheaders", stepdef.Action, newSetHeadersAction},
		{"base64totext", stepdef.Action, newBase64ToTextAction},
		{"texttobase64", stepdef.Action, newTextToBase64Action},
		{"https", stepdef.Action, newHTTPSAction},
		{"flowlink", stepdef.Action, newFlowLinkAction},
		{"removeheaders", stepdef.Action, newRemoveHeadersAction},
		{"replace", stepdef.Action, newReplaceAction},
		{"simplereplace", stepdef.Action, newSimpleReplaceAction},
		{"zip", stepdef.Action, newZipAction},
		{"unzip", stepdef.Action, newUnzipAction},
		{"throttle", stepdef.Action, newThrottleAction},
		{"encoder", stepdef.Action, newEncoderAction},
		{"xmltojson", stepdef.Action, newXMLToJSONAction},
		{"jsontoxml", stepdef.Action, newJSONToXMLAction},
		{"xmltojsonsimple", stepdef.Action, newXMLToJSONSimpleAction},
		{"jsontoxmlsimple", stepdef.Action, newJSONToXMLSimpleAction},
		{"csvtoxml", stepdef.Action, newCSVToXMLAction},
		{"xmltocsv", stepdef.Action, newXMLToCSVAction},
		{"validate", stepdef.Action, newValidateAction},
		{"wiretap", stepdef.Router, newWireTapRouter},
		{"recipient", stepdef.Router, newRecipientRouter},
		{"content", stepdef.Router, newContentRouter},
		{"filter", stepdef.Router, newFilterRouter},
		{"split", stepdef.Router, newSplitRouter},
		{"enrich", stepdef.Router, newEnrichRouter},
		{"aggregate", stepdef.Router, newAggregateRouter},
		{"splitandaggregate", stepdef.Router, newSplitAndAggregateRouter},
		{"file", stepdef.Sink, newFileSink},
		{"deadletter", stepdef.Sink, newDeadLetterSink},
	}
	for _, b := range builtins {
		schema, err := schemas.ReadFile("schemas/" + b.name + "-" + b.kind + ".json")
		if err != nil {
			return err
		}
		if err := r.Register(stepdef.Definition{Name: b.name, Kind: b.kind, Schema: schema, New: b.new}); err != nil {
			return err
		}
	}
	return nil
}
