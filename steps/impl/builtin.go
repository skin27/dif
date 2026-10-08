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
		{"rest", stepdef.Source, requestReply, newRestSource, nil},
		{"queue", stepdef.Source, pointToPointChannel, newQueueSource, nil},
		{"reply", stepdef.Source, requestReply, newReplySource, nil},
		{"request", stepdef.Action, requestReply, newRequestAction, nil},
		{"reply", stepdef.Sink, requestReply, newReplySink, nil},
		{"topic", stepdef.Source, "Publish-Subscribe Channel", newTopicSource, nil},
		{"googledrive", stepdef.Source, pollingConsumer, newDriveSource, nil},
		{"ftp", stepdef.Source, pollingConsumer, newRemoteSource(ftpProtocol), nil},
		{"ftps", stepdef.Source, pollingConsumer, newRemoteSource(ftpsProtocol), nil},
		{"sftp", stepdef.Source, pollingConsumer, newRemoteSource(sftpProtocol), nil},
		{"flowlink", stepdef.Source, "", newFlowLinkSource, []string{"flowlink-async"}},
		{"repeater", stepdef.Source, "", newTimerSource, nil},
		{"quartz", stepdef.Source, "", newQuartzSource, nil},
		{"counter", stepdef.Source, "", newCounterSource, nil},
		{"log", stepdef.Action, "", newLogAction, nil},
		{"setbody", stepdef.Action, "", newSetBodyAction, nil},
		{"setheader", stepdef.Action, "", newSetHeaderAction, nil},
		{"passthrough", stepdef.Action, "", newPassthrough, nil},
		{"setheaders", stepdef.Action, "", newSetHeadersAction, nil},
		{"base64totext", stepdef.Action, messageTranslator, newBase64ToTextAction, nil},
		{"base64tobinary", stepdef.Action, messageTranslator, newBase64ToBinaryAction, nil},
		{"texttobase64", stepdef.Action, messageTranslator, newTextToBase64Action, []string{"binarytobase64"}},
		{"setbodyasstring", stepdef.Action, messageTranslator, newSetBodyAsStringAction, nil},
		{"https", stepdef.Action, "", newHTTPSAction, nil},
		{"rest", stepdef.Action, requestReply, newRestAction, nil},
		{"graphql", stepdef.Action, requestReply, newGraphQLAction, nil},
		{"smtp", stepdef.Action, "", newSMTPAction(false), nil},
		{"googledrive", stepdef.Action, "", newDriveAction, nil},
		{"flowlink", stepdef.Action, "", newFlowLinkAction, []string{"flowlink-async"}},
		{"queue", stepdef.Action, pointToPointChannel, newQueueAction, nil},
		{"topic", stepdef.Action, "Publish-Subscribe Channel", newTopicAction, nil},
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
		{"editoxml", stepdef.Action, messageTranslator, newEDIToXMLAction, nil},
		{"xmltoedi", stepdef.Action, messageTranslator, newXMLToEDIAction, nil},
		{"xmltoedifact", stepdef.Action, messageTranslator, newXMLToEDIFACTAction, nil},
		{"docconverter", stepdef.Action, messageTranslator, newDocConverterAction, nil},
		{"xslt", stepdef.Action, messageTranslator, newXSLTAction, nil},
		{"velocity", stepdef.Action, messageTranslator, newVelocityAction, nil},
		{"xmlvalidator", stepdef.Action, "", newXMLValidatorAction, nil},
		{"soap", stepdef.Action, requestReply, newSOAPAction, nil},
		{"formtoxml", stepdef.Action, messageTranslator, newFormToXMLAction, nil},
		{"flv", stepdef.Action, messageTranslator, newFlvAction, nil},
		{"exceltoxml", stepdef.Action, messageTranslator, newExcelToXMLAction, nil},
		{"xmltoexcel", stepdef.Action, messageTranslator, newXMLToExcelAction, nil},
		{"multipart", stepdef.Action, messageTranslator, newMultipartAction, nil},
		{"validate", stepdef.Action, "", newValidateAction, nil},
		{"jsonvalidator", stepdef.Action, "", newJSONValidator, nil},
		{"fileenrich", stepdef.Action, contentEnricher, newFileEnrichAction, nil},
		{"ftpenrich", stepdef.Action, contentEnricher, newRemoteEnrich(ftpProtocol), nil},
		{"ftpsenrich", stepdef.Action, contentEnricher, newRemoteEnrich(ftpsProtocol), nil},
		{"sftpenrich", stepdef.Action, contentEnricher, newRemoteEnrich(sftpProtocol), nil},
		{"settenantvariable", stepdef.Action, "", newSetTenantVariableAction, nil},
		{"gettenantvariable", stepdef.Action, "", newGetTenantVariableAction, nil},
		{"removetenantvariable", stepdef.Action, "", newRemoveTenantVariableAction, nil},
		{"setcookie", stepdef.Action, "", newSetCookieAction, nil},
		{"removecookie", stepdef.Action, "", newRemoveCookieAction, nil},
		{"setoneway", stepdef.Action, eventMessage, newSetOneWayAction, []string{"setfireandforget"}},
		{"setrequestreply", stepdef.Action, requestReply, newSetRequestReplyAction, []string{"settwoways", "setrequestandreply"}},
		{"setuuid", stepdef.Action, "", newSetUUIDAction, nil},
		{"setbodybyheader", stepdef.Action, "", newSetBodyByHeaderAction, nil},
		{"setheaderbybody", stepdef.Action, "", newSetHeaderByBodyAction, nil},
		{"delay", stepdef.Action, "", newDelayAction, nil},
		{"logger", stepdef.Action, "", newLoggerAction, nil},
		{"simplevalidator", stepdef.Action, "", newSimpleValidator, nil},
		{"wiretap", stepdef.Router, wireTap, newWireTapRouter, nil},
		{"recipient", stepdef.Router, recipientList, newRecipientRouter, nil},
		{"content", stepdef.Router, contentBasedRouter, newContentRouter, nil},
		{"if", stepdef.Router, contentBasedRouter, newContentRouter, nil},
		{"loop", stepdef.Router, "", newLoopRouter, nil},
		{"dowhile", stepdef.Router, "", newDoWhileRouter, nil},
		{"wastebin", stepdef.Router, "", newWastebin, nil},
		{"filter", stepdef.Router, messageFilter, newFilterRouter, nil},
		{"split", stepdef.Router, splitter, newSplitRouter, nil},
		{"splitwithnamespace", stepdef.Router, splitter, newSplitWithNamespaceRouter, nil},
		{"enrich", stepdef.Router, contentEnricher, newEnrichRouter, nil},
		{"aggregate", stepdef.Router, aggregator, newAggregateRouter, nil},
		{"idempotent", stepdef.Router, "Idempotent Receiver", newIdempotentRouter, nil},
		{"splitandaggregate", stepdef.Router, composedMessageProcessor, newSplitAndAggregateRouter, nil},
		{"splitandaggregatewithnamespace", stepdef.Router, composedMessageProcessor, newSplitAndAggregateWithNamespaceRouter, nil},
		{"file", stepdef.Sink, "", newFileSink, nil},
		{"deadletter", stepdef.Sink, deadLetterChannel, newDeadLetterSink, nil},
		{"wastebin", stepdef.Sink, "", newWastebinSink, nil},
		{"oauth2token", stepdef.Sink, "", newOAuth2TokenSink, nil},
		{"ftp", stepdef.Sink, "", newRemoteSink(ftpProtocol), nil},
		{"ftps", stepdef.Sink, "", newRemoteSink(ftpsProtocol), nil},
		{"sftp", stepdef.Sink, "", newRemoteSink(sftpProtocol), nil},
	}
	for _, b := range builtins {
		// Every built-in step may refer to its flow in an expression.
		bindings := []string{FlowRuntimeKey}
		switch b.name {
		case "queue", "topic", "flowlink", "deadletter", "idempotent", "request", "reply":
			bindings = append(bindings, ChannelRuntimeKey)
		}
		schema, err := schemas.ReadFile("schemas/" + b.name + "-" + b.kind + ".json")
		if err != nil {
			return err
		}
		for _, name := range append([]string{b.name}, b.aliases...) {
			if err := r.Register(stepdef.Definition{Name: name, Kind: b.kind, Schema: schema, Pattern: b.pattern, RuntimeBindings: bindings, New: b.new}); err != nil {
				return err
			}
		}
	}

	// smtps is smtp with TLS from the start, so it shares smtp's schema.
	schema, err := schemas.ReadFile("schemas/smtp-action.json")
	if err != nil {
		return err
	}
	return r.Register(stepdef.Definition{Name: "smtps", Kind: stepdef.Action, Schema: schema, New: newSMTPAction(true)})
}
