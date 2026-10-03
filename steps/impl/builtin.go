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

// Enterprise Integration Patterns the built-in steps implement.
const (
	contentBasedRouter       = "Content-Based Router"
	messageFilter            = "Message Filter"
	splitter                 = "Splitter"
	aggregator               = "Aggregator"
	composedMessageProcessor = "Composed Message Processor"
	recipientList            = "Recipient List"
	wireTap                  = "Wire Tap"
	contentEnricher          = "Content Enricher"
	deadLetterChannel        = "Dead Letter Channel"
	pointToPointChannel      = "Point-to-Point Channel"
	pollingConsumer          = "Polling Consumer"
	throttler                = "Throttler"
	contentFilter            = "Content Filter"
	messageTranslator        = "Message Translator"
	eventMessage             = "Event Message"
	requestReply             = "Request-Reply"
)

// Register adds the built-in steps to r.
func Register(r *registry.Registry) error {
	builtins := []struct {
		name, kind, pattern string
		new                 func(string, stepdef.Params) (stepdef.Processor, error)
		aliases             []string // other names of the same step, sharing its schema
	}{
		{"timer", stepdef.Source, "", newTimerSource, nil},
		{"file", stepdef.Source, pollingConsumer, newFileSource, nil},
		{"message", stepdef.Source, "", newMessageSource, nil},
		{"https", stepdef.Source, requestReply, newHTTPSSource, nil},
		{"queue", stepdef.Source, pointToPointChannel, newQueueSource, nil},
		{"flowlink", stepdef.Source, "", newFlowLinkSource, nil},
		{"repeater", stepdef.Source, "", newTimerSource, nil},
		{"quartz", stepdef.Source, "", newQuartzSource, nil},
		{"log", stepdef.Action, "", newLogAction, nil},
		{"setbody", stepdef.Action, "", newSetBodyAction, nil},
		{"setheader", stepdef.Action, "", newSetHeaderAction, nil},
		{"passthrough", stepdef.Action, "", newPassthrough, nil},
		{"setheaders", stepdef.Action, "", newSetHeadersAction, nil},
		{"base64totext", stepdef.Action, messageTranslator, newBase64ToTextAction, nil},
		{"texttobase64", stepdef.Action, messageTranslator, newTextToBase64Action, nil},
		{"https", stepdef.Action, "", newHTTPSAction, nil},
		{"flowlink", stepdef.Action, "", newFlowLinkAction, nil},
		{"removeheaders", stepdef.Action, contentFilter, newRemoveHeadersAction, nil},
		{"replace", stepdef.Action, messageTranslator, newReplaceAction, nil},
		{"simplereplace", stepdef.Action, messageTranslator, newSimpleReplaceAction, nil},
		{"zip", stepdef.Action, messageTranslator, newZipAction, nil},
		{"unzip", stepdef.Action, messageTranslator, newUnzipAction, nil},
		{"throttle", stepdef.Action, throttler, newThrottleAction, nil},
		{"encoder", stepdef.Action, messageTranslator, newEncoderAction, nil},
		{"xmltojson", stepdef.Action, messageTranslator, newXMLToJSONAction, nil},
		{"jsontoxml", stepdef.Action, messageTranslator, newJSONToXMLAction, nil},
		{"xmltojsonsimple", stepdef.Action, messageTranslator, newXMLToJSONSimpleAction, nil},
		{"jsontoxmlsimple", stepdef.Action, messageTranslator, newJSONToXMLSimpleAction, nil},
		{"csvtoxml", stepdef.Action, messageTranslator, newCSVToXMLAction, nil},
		{"xmltocsv", stepdef.Action, messageTranslator, newXMLToCSVAction, nil},
		{"validate", stepdef.Action, "", newValidateAction, nil},
		{"setoneway", stepdef.Action, eventMessage, newSetOneWayAction, []string{"setfireandforget"}},
		{"setrequestreply", stepdef.Action, requestReply, newSetRequestReplyAction, []string{"settwoways", "setrequestandreply"}},
		{"wiretap", stepdef.Router, wireTap, newWireTapRouter, nil},
		{"recipient", stepdef.Router, recipientList, newRecipientRouter, nil},
		{"content", stepdef.Router, contentBasedRouter, newContentRouter, nil},
		{"filter", stepdef.Router, messageFilter, newFilterRouter, nil},
		{"split", stepdef.Router, splitter, newSplitRouter, nil},
		{"enrich", stepdef.Router, contentEnricher, newEnrichRouter, nil},
		{"aggregate", stepdef.Router, aggregator, newAggregateRouter, nil},
		{"splitandaggregate", stepdef.Router, composedMessageProcessor, newSplitAndAggregateRouter, nil},
		{"file", stepdef.Sink, "", newFileSink, nil},
		{"deadletter", stepdef.Sink, deadLetterChannel, newDeadLetterSink, nil},
	}
	for _, b := range builtins {
		schema, err := schemas.ReadFile("schemas/" + b.name + "-" + b.kind + ".json")
		if err != nil {
			return err
		}
		for _, name := range append([]string{b.name}, b.aliases...) {
			if err := r.Register(stepdef.Definition{Name: name, Kind: b.kind, Schema: schema, Pattern: b.pattern, New: b.new}); err != nil {
				return err
			}
		}
	}
	return nil
}
