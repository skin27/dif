package impl

import (
	"encoding/json"
	"slices"
	"strings"

	"dif/message"
	stepdef "dif/steps/definition"
)

// stepEffects says what the built-in steps do to a message, for the static
// checks of a flow (dif validate), by name/kind as they are registered. Each
// entry declares only what the step's implementation guarantees; a step that is
// not here, or whose function returns nil, is unknown to the checks. The
// mapper and service steps get theirs from the core map or service they are
// bound to (see api).
var stepEffects = map[string]func(stepdef.Params) *stepdef.Effect{
	// Sources that make the message themselves: only the headers they set exist.
	"timer/source":       fixed(stepdef.Effect{Headers: stepdef.HeadersKeep}),
	"repeater/source":    fixed(stepdef.Effect{Headers: stepdef.HeadersKeep}),
	"counter/source":     fixed(stepdef.Effect{Headers: stepdef.HeadersKeep, Sets: []string{message.ContentType}}),
	"quartz/source":      fixed(stepdef.Effect{Headers: stepdef.HeadersKeep, Sets: []string{QuartzFireTime}}),
	"file/source":        fixed(fileRead),
	"ftp/source":         fixed(fileRead),
	"ftps/source":        fixed(fileRead),
	"sftp/source":        fixed(fileRead),
	"smb/source":         fixed(fileRead),
	"googledrive/source": fixed(stepdef.Effect{Headers: stepdef.HeadersKeep, Sets: []string{FileName, DriveID}}),
	// The sender of a request decides its headers (HeadersAdd); a service
	// contract the flow is bound to narrows them.
	"https/source": httpsSourceEffect,
	"rest/source":  fixed(stepdef.Effect{Headers: stepdef.HeadersAdd}),

	// Steps that leave the message as it is.
	"log/action":                  fixed(keepAll),
	"passthrough/action":          fixed(keepAll),
	"delay/action":                fixed(keepAll),
	"throttle/action":             fixed(keepAll),
	"setrequestreply/action":      fixed(keepAll),
	"removetenantvariable/action": fixed(keepAll),
	"setcookie/action":            fixed(keepAll),
	"removecookie/action":         fixed(keepAll),
	"smtp/action":                 fixed(keepAll),
	"smtps/action":                fixed(keepAll),
	"topic/action":                fixed(keepAll),
	"smb/action":                  fixed(keepAll),
	"file/sink":                   fixed(keepAll),
	"ftp/sink":                    fixed(keepAll),
	"ftps/sink":                   fixed(keepAll),
	"sftp/sink":                   fixed(keepAll),
	"smb/sink":                    fixed(keepAll),
	"deadletter/sink":             fixed(keepAll),
	"oauth2token/sink":            fixed(keepAll),
	"setoneway/action":            fixed(stepdef.Effect{KeepBody: true, Headers: stepdef.HeadersKeep, Reply: true}),
	"logger/action":               keepReading("language", "expression", false),
	"settenantvariable/action":    keepReading("language", "value", false),
	"simplevalidator/action": func(p stepdef.Params) *stepdef.Effect {
		e := keepAll
		e.Reads = headerReads(optString(p, "expression"), true)
		return &e
	},
	"guard/action": keepReading("language", "expression", true),
	"return/action": func(p stepdef.Params) *stepdef.Effect {
		e := keepAll
		e.Reply = true
		e.Reads = readsIn(optString(p, "language"), optString(p, "expression"), false)
		return &e
	},
	"flowlink/action": linkEffect,
	"queue/action":    linkEffect,

	// Steps that set headers.
	"setheader/action": func(p stepdef.Params) *stepdef.Effect {
		e := keepAll
		e.Sets = []string{optString(p, "name")}
		e.Reads = readsIn(optString(p, "language"), optString(p, "value"), false)
		return &e
	},
	"setheaders/action": func(p stepdef.Params) *stepdef.Effect {
		var defs []struct{ Name, Value, Language string }
		if json.Unmarshal([]byte(optString(p, "headers")), &defs) != nil {
			return nil
		}
		e := keepAll
		set := map[string]bool{} // the headers are set in order: a value may read one set before it
		for _, d := range defs {
			if d.Language == "" {
				d.Language = "simple"
			}
			for _, names := range readsIn(d.Language, d.Value, false) {
				if !slices.ContainsFunc(names, func(n string) bool { return set[strings.ToLower(n)] }) {
					e.Reads = append(e.Reads, names)
				}
			}
			e.Sets = append(e.Sets, d.Name)
			set[strings.ToLower(d.Name)] = true
		}
		return &e
	},
	"setuuid/action":           setsOption("headerName"),
	"setheaderbybody/action":   setsOption("headerName"),
	"gettenantvariable/action": setsOption("headerName"),
	"store/action":             storeEffect,
	"googledrive/action":       fixed(stepdef.Effect{KeepBody: true, Headers: stepdef.HeadersKeep, MaySet: []string{DriveID}}),
	"as2/action": fixed(stepdef.Effect{KeepBody: true, Headers: stepdef.HeadersKeep,
		Sets: []string{"AS2-Message-Id", "http.status"}, MaySet: []string{"AS2-MDN-Disposition", "AS2-MDN-Signed"}}),
	"removeheaders/action": fixed(stepdef.Effect{KeepBody: true, Headers: stepdef.HeadersRemove}),

	// Steps that change the body and keep the headers.
	"setbody/action": func(p stepdef.Params) *stepdef.Effect {
		return &stepdef.Effect{Headers: stepdef.HeadersKeep, Reads: readsIn(optString(p, "language"), optString(p, "expression"), false)}
	},
	"setbodybyheader/action": func(p stepdef.Params) *stepdef.Effect {
		return &stepdef.Effect{Headers: stepdef.HeadersKeep, Reads: [][]string{{optString(p, "headerName")}}}
	},
	"setbodyasstring/action": fixed(keepAll), // the body's text: JSON stays JSON
	"simplereplace/action":   fixed(newBody),
	"replace/action":         fixed(newBody),
	"encoder/action":         fixed(newBody),
	"base64totext/action":    fixed(newBody),
	"base64tobinary/action":  fixed(newBody),
	"texttobase64/action":    fixed(stepdef.Effect{Headers: stepdef.HeadersKeep, Body: stepdef.BodyType{Family: stepdef.FamilyText}}),
	"velocity/action":        fixed(newBody),
	"multipart/action":       fixed(stepdef.Effect{Headers: stepdef.HeadersKeep, Sets: []string{message.ContentType}}),
	"zip/action": fixed(stepdef.Effect{Headers: stepdef.HeadersKeep, Sets: []string{FileName},
		Body: stepdef.BodyType{Family: stepdef.FamilyBinary, Media: "application/zip"}}),
	"unzip/action":        fixed(stepdef.Effect{Headers: stepdef.HeadersKeep, Sets: []string{FileName}, MaySet: []string{message.ContentType}}),
	"fileenrich/action":   fixed(enrichFile),
	"ftpenrich/action":    fixed(enrichFile),
	"ftpsenrich/action":   fixed(enrichFile),
	"sftpenrich/action":   fixed(enrichFile),
	"smbenrich/action":    fixed(enrichFile),
	"https/action":        fixed(stepdef.Effect{Headers: stepdef.HeadersKeep, Sets: []string{"http.status"}, MaySet: []string{message.ContentType}}),
	"rest/action":         restEffect,
	"graphql/action":      fixed(stepdef.Effect{Headers: stepdef.HeadersKeep, Sets: []string{"http.status", message.ContentType}}),
	"soap/action":         fixed(stepdef.Effect{Headers: stepdef.HeadersKeep, Sets: []string{"http.status"}, MaySet: []string{message.ContentType}}),
	"sql/action":          fixed(sqlRun),
	"sql2/action":         fixed(sqlRun),
	"xslt/action":         fixed(stepdef.Effect{Headers: stepdef.HeadersKeep, Requires: xmlBody}),
	"xmlvalidator/action": fixed(stepdef.Effect{Headers: stepdef.HeadersKeep, Requires: xmlBody, Tolerant: true}), // a body that is no XML becomes the report
	"schematron/action": fixed(stepdef.Effect{KeepBody: true, Headers: stepdef.HeadersKeep, Requires: xmlBody,
		Sets: []string{schematronStatus, schematronReport}}),
	"validate/action":      fixed(stepdef.Effect{KeepBody: true, Headers: stepdef.HeadersKeep, Requires: jsonBody}),
	"jsonvalidator/action": fixed(stepdef.Effect{KeepBody: true, Headers: stepdef.HeadersKeep, Requires: jsonBody}),

	// Converters: they fail on a body of another format and set Content-Type.
	"xmltojson/action":       fixed(converter(stepdef.FamilyXML, stepdef.FamilyJSON, "application/json")),
	"xmltojsonsimple/action": fixed(converter(stepdef.FamilyXML, stepdef.FamilyJSON, "application/json")),
	"jsontoxml/action":       fixed(converter(stepdef.FamilyJSON, stepdef.FamilyXML, "application/xml")),
	"jsontoxmlsimple/action": func(p stepdef.Params) *stepdef.Effect {
		to := ""
		if b, _ := p["addRoot"].(bool); b { // without a root it may write text or nothing
			to = stepdef.FamilyXML
		}
		e := converter(stepdef.FamilyJSON, to, "application/xml")
		return &e
	},
	"csvtoxml/action":     fixed(converter(stepdef.FamilyCSV, stepdef.FamilyXML, "application/xml")),
	"xmltocsv/action":     fixed(converter(stepdef.FamilyXML, stepdef.FamilyCSV, "text/csv")),
	"editoxml/action":     fixed(converter("", stepdef.FamilyXML, "application/xml")),
	"xmltoedi/action":     fixed(converter(stepdef.FamilyXML, stepdef.FamilyText, "text/plain")),
	"xmltoedifact/action": fixed(converter(stepdef.FamilyXML, stepdef.FamilyText, "application/edifact")),
	"formtoxml/action":    fixed(converter("", stepdef.FamilyXML, "application/xml")),
	"flv/action":          fixed(converter("", stepdef.FamilyXML, "application/xml")),
	"exceltoxml/action":   fixed(converter("", stepdef.FamilyXML, "application/xml")),
	"xmltoexcel/action":   fixed(converter(stepdef.FamilyXML, stepdef.FamilyBinary, "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")),
	"docconverter/action": docConverterEffect,

	// Routers.
	"wiretap/router":   fixed(passOn),
	"recipient/router": fixed(passOn),
	"content/router": func(p stepdef.Params) *stepdef.Effect {
		e := conditions(p)
		e.Otherwise = true
		return e
	},
	"if/router": conditions,
	"filter/router": func(p stepdef.Params) *stepdef.Effect {
		e := passOn
		e.Reads = readsIn(optString(p, "language"), optString(p, "expression"), true)
		return &e
	},
	"wastebin/router":                       fixed(stepdef.Effect{Stops: true, Reply: true}),
	"wastebin/sink":                         fixed(stepdef.Effect{Stops: true, Reply: true}),
	"split/router":                          splitEffect(false),
	"splitwithnamespace/router":             splitEffect(false),
	"splitandaggregate/router":              splitEffect(true),
	"splitandaggregatewithnamespace/router": splitEffect(true),
	"loop/router":                           loopEffect("loop", loopIndex, loopSize),
	"dowhile/router":                        loopEffect("dowhile", loopIndex),
}

var (
	jsonBody = stepdef.BodyType{Family: stepdef.FamilyJSON}
	xmlBody  = stepdef.BodyType{Family: stepdef.FamilyXML}

	// keepAll leaves the message as it is.
	keepAll = stepdef.Effect{KeepBody: true, Headers: stepdef.HeadersKeep}
	// newBody makes a body nothing is known of, and keeps the headers.
	newBody = stepdef.Effect{Headers: stepdef.HeadersKeep}
	// passOn is a router that sends the message on as it is.
	passOn = stepdef.Effect{KeepBody: true, Headers: stepdef.HeadersKeep, Reply: true}

	fileRead   = stepdef.Effect{Headers: stepdef.HeadersKeep, Sets: []string{FileName}, MaySet: []string{message.ContentType}}
	enrichFile = stepdef.Effect{Headers: stepdef.HeadersKeep, MaySet: []string{FileName, message.ContentType}}
	sqlRun     = stepdef.Effect{Headers: stepdef.HeadersKeep, Sets: []string{"numberOfRecords", "hasErrors"}, MaySet: []string{message.ContentType}}
)

// fixed is an effect that does not depend on the options.
func fixed(e stepdef.Effect) func(stepdef.Params) *stepdef.Effect {
	return func(stepdef.Params) *stepdef.Effect { c := e; return &c }
}

func optString(p stepdef.Params, key string) string {
	s, _ := p[key].(string)
	return s
}

// keepReading leaves the message as it is and reads the expression in option
// expr, in the language of option lang.
func keepReading(lang, expr string, condition bool) func(stepdef.Params) *stepdef.Effect {
	return func(p stepdef.Params) *stepdef.Effect {
		e := keepAll
		e.Reads = readsIn(optString(p, lang), optString(p, expr), condition)
		return &e
	}
}

// setsOption leaves the message as it is and sets the header the option names.
func setsOption(option string) func(stepdef.Params) *stepdef.Effect {
	return func(p stepdef.Params) *stepdef.Effect {
		e := keepAll
		e.Sets = []string{optString(p, option)}
		return &e
	}
}

// convert is a converter from family from ("" for any) to family to ("" when
// it is not certain), which sets Content-Type to media.
func converter(from, to, media string) stepdef.Effect {
	return stepdef.Effect{Headers: stepdef.HeadersKeep, Requires: stepdef.BodyType{Family: from}, Body: stepdef.BodyType{Family: to, Media: media}}
}

func httpsSourceEffect(p stepdef.Params) *stepdef.Effect {
	e := &stepdef.Effect{Headers: stepdef.HeadersAdd}
	if b, _ := p["preserveHttpHeaders"].(bool); b {
		e.Sets = []string{"http.method", "http.path", "http.query", "http.uri"}
	}
	return e
}

func restEffect(p stepdef.Params) *stepdef.Effect {
	e := &stepdef.Effect{Headers: stepdef.HeadersKeep, Sets: []string{"http.status"}, MaySet: []string{message.ContentType}}
	if optString(p, "consumes") != "" {
		e.Sets = append(e.Sets, "Accept")
	}
	return e
}

// linkEffect is a flowlink or queue action: one-way it passes the message on as
// it is; InOut the outcome of the other flow replaces it.
func linkEffect(p stepdef.Params) *stepdef.Effect {
	if p["exchangePattern"] == message.InOut {
		return nil
	}
	e := keepAll
	return &e
}

func docConverterEffect(p stepdef.Params) *stepdef.Effect {
	convert := strings.ToLower(optString(p, "convert"))
	var from, to string
	for _, f := range docFormats {
		if t, ok := strings.CutPrefix(convert, f+"to"); ok {
			from, to = f, t
		}
	}
	e := &stepdef.Effect{Headers: stepdef.HeadersKeep, Sets: []string{message.ContentType}}
	switch from {
	case "json":
		e.Requires = jsonBody
	case "xml":
		e.Requires = xmlBody
	case "csv":
		e.Requires.Family = stepdef.FamilyCSV
	}
	switch to {
	case "json":
		e.Body = stepdef.BodyType{Family: stepdef.FamilyJSON, Media: "application/json"}
	case "xml":
		e.Body.Media = "application/xml"
		if from == "xml" || from == "csv" { // one root element; from JSON or YAML it may be several
			e.Body.Family = stepdef.FamilyXML
		}
	case "csv":
		e.Body = stepdef.BodyType{Family: stepdef.FamilyCSV, Media: "text/csv"}
	case "yaml":
		e.Body = stepdef.BodyType{Family: stepdef.FamilyText, Media: "application/yaml"}
	default:
		return nil
	}
	return e
}

// links returns a router's outbound links.
func links(p stepdef.Params) []stepdef.Link {
	l, _ := p[stepdef.Links].([]stepdef.Link)
	return l
}

// conditions is a router that sends the message as it is along the links
// whose conditions hold, which it reads.
func conditions(p stepdef.Params) *stepdef.Effect {
	e := passOn
	for _, l := range links(p) {
		if l.Expression == "" {
			continue
		}
		lang := l.Language
		if lang == "" {
			lang = "simple"
		}
		e.Reads = append(e.Reads, readsIn(lang, l.Expression, true)...)
	}
	return &e
}

// splitEffect is a splitter: the parts go along the split link, with the split
// headers and a body nothing is known of. The message itself goes along the
// other link, or, when aggregated, the message with the aggregate.
func splitEffect(aggregated bool) func(stepdef.Params) *stepdef.Effect {
	return func(p stepdef.Params) *stepdef.Effect {
		e := passOn
		e.Reads = readsIn(optString(p, "language"), optString(p, "expression"), false)
		ls := links(p)
		e.Links = make([]*stepdef.Effect, len(ls))
		for i, l := range ls {
			switch {
			case l.Rule == "split" || len(ls) == 1:
				e.Links[i] = &stepdef.Effect{Headers: stepdef.HeadersKeep, Sets: []string{SplitIndex, SplitSize, SplitComplete}}
				if l.Expression != "" { // splitandaggregate may have it on the link
					e.Reads = append(e.Reads, readsIn(optString(p, "language"), l.Expression, false)...)
				}
			case aggregated:
				e.Links[i] = &stepdef.Effect{} // the aggregate: nothing is known
			}
		}
		return &e
	}
}

// loopEffect is a loop: each round gets the message the round before made, so
// nothing is known on the loop link but the loop headers, nor after the loop.
func loopEffect(rule string, sets ...string) func(stepdef.Params) *stepdef.Effect {
	return func(p stepdef.Params) *stepdef.Effect {
		e := &stepdef.Effect{Reply: true}
		ls := links(p)
		body := -1
		for i, l := range ls {
			if l.Rule == rule || (l.Rule == "" && l.Expression != "" && body < 0) {
				body = i
			}
		}
		if len(ls) == 1 {
			body = 0
		}
		if body < 0 {
			return e
		}
		expr, lang := ls[body].Expression, ls[body].Language
		if expr == "" {
			expr = optString(p, "expression")
		}
		if lang == "" {
			lang = optString(p, "language")
		}
		e.Reads = readsIn(lang, expr, rule == "dowhile")
		e.Links = make([]*stepdef.Effect, len(ls))
		e.Links[body] = &stepdef.Effect{Sets: sets}
		return e
	}
}
