package impl

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"

	"github.com/knroy/go-xml/xdm"

	"dif/message"
	stepdef "dif/steps/definition"
)

// soapAction calls a SOAP 1.1 web service, as the platform's soap step does.
// The platform's component is not open, so this follows what its flows and
// their expected answers show:
//
//   - The body of the message is the payload of the operation, such as
//     <ListOfCountryNamesByName/>. It goes in a SOAP envelope in a POST to the
//     address of the step, with the SOAPAction header and the HTTP headers of
//     the options. A body that already is an Envelope is sent as it is.
//   - With smart (the default) the WSDL, fetched once with ?wsdl, gives the
//     namespace of the operation, which a payload without one gets as its default
//     namespace, and its SOAPAction. Without smart the action is the SOAPAction.
//   - With extract the answer is the first element of the SOAP body, after an XML
//     declaration, with the namespace declarations it uses; without it the answer
//     as it came. A SOAP fault, and any status of 300 or more, fails the message.
//   - auth (the base64 of user:password) is sent as Basic credentials and token
//     as a Bearer token. The headers of the message are not sent.
//
// SOAP 1.2 and attachments are not supported.
type soapAction struct {
	target      string // the address of the service, with the query of params that is not the WSDL
	wsdlURL     string
	action      string
	extract     bool
	smart       bool
	soapHeaders []soapHeader
	httpHeaders [][2]string
	authorize   string // the Authorization header, if any
	client      *http.Client

	mu   sync.Mutex
	wsdl *soapWSDL // set once the WSDL has been read
}

type soapHeader struct {
	Name  string `json:"name"`
	Value any    `json:"value"`
	Attrs []struct {
		Name  string `json:"name"`
		Value any    `json:"value"`
	} `json:"attrs"`
}

const (
	soap11NS = "http://schemas.xmlsoap.org/soap/envelope/"
	soap12NS = "http://www.w3.org/2003/05/soap-envelope"
)

func newSOAPAction(_ string, p stepdef.Params) (stepdef.Processor, error) {
	a := soapAction{
		action:  strings.TrimSpace(p["action"].(string)),
		extract: p["extract"].(bool),
		smart:   p["smart"].(bool),
	}
	u, err := url.Parse(strings.TrimSpace(p["path"].(string)))
	if err != nil || u.Host == "" || u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("uri: want http://host[:port]/path or https://host[:port]/path")
	}
	wsdl := *u
	params := strings.TrimSpace(strings.TrimPrefix(p["params"].(string), "?"))
	if strings.ContainsFunc(params, func(r rune) bool { return r <= ' ' || r == 0x7f || r == '#' }) {
		return nil, fmt.Errorf("option params: %q must be URL-encoded", params)
	}
	switch {
	case params == "" || strings.EqualFold(params, "wsdl"):
		wsdl.RawQuery = "wsdl"
		if params != "" {
			wsdl.RawQuery = params
		}
	default:
		if u.RawQuery != "" {
			u.RawQuery += "&"
		}
		u.RawQuery += params
		wsdl.RawQuery = "wsdl"
	}
	a.target, a.wsdlURL = u.String(), wsdl.String()

	if s := strings.TrimSpace(p["headers"].(string)); s != "" {
		if err := json.Unmarshal([]byte(s), &a.soapHeaders); err != nil {
			return nil, fmt.Errorf("option headers: want a JSON array of {name, value, attrs}: %w", err)
		}
		for _, h := range a.soapHeaders {
			if !isXMLName(h.Name) {
				return nil, fmt.Errorf("option headers: %q is not an XML name", h.Name)
			}
			for _, at := range h.Attrs {
				if !isXMLName(at.Name) {
					return nil, fmt.Errorf("option headers: attribute %q is not an XML name", at.Name)
				}
			}
		}
	}
	if s := strings.TrimSpace(p["httpHeaders"].(string)); s != "" {
		var list []struct{ Name, Value string }
		if err := json.Unmarshal([]byte(s), &list); err != nil {
			return nil, fmt.Errorf("option httpHeaders: want a JSON array of {name, value}: %w", err)
		}
		for _, h := range list {
			if !validHeader(h.Name, h.Value) {
				return nil, fmt.Errorf("option httpHeaders: %q cannot be an HTTP header", h.Name)
			}
			a.httpHeaders = append(a.httpHeaders, [2]string{h.Name, h.Value})
		}
	}
	switch auth, token := strings.TrimSpace(p["auth"].(string)), strings.TrimSpace(p["token"].(string)); {
	case auth != "":
		a.authorize = "Basic " + auth
	case token != "":
		a.authorize = "Bearer " + token
	}
	if a.authorize != "" && !validHeader("Authorization", a.authorize) {
		return nil, fmt.Errorf("options auth and token cannot be an HTTP header")
	}
	if a.client, err = httpsClient(p); err != nil {
		return nil, err
	}
	return &a, nil
}

func (a *soapAction) Process(ctx context.Context, m message.Message) (message.Message, error) {
	payload := strings.TrimSpace(string(bytesOf(m[message.Body])))
	root, err := soapRoot(payload)
	if err != nil {
		return nil, err
	}
	operation := a.action
	if operation == "" {
		operation = root.Name.Local
	}
	soapAction := a.action
	var info *soapWSDL
	if a.smart {
		if info, err = a.readWSDL(ctx); err != nil {
			return nil, err
		}
		if s, ok := info.actions[operation]; ok {
			soapAction = s
		}
	}

	envelope := payload
	if !(root.Name.Local == "Envelope" && (root.Name.URI == soap11NS || root.Name.URI == soap12NS)) {
		ns := ""
		if info != nil {
			ns = info.namespaceOf(operation)
		}
		envelope = a.envelope(payload, root, ns)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.target, strings.NewReader(envelope))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "text/xml; charset=utf-8")
	req.Header.Set("Accept", "text/xml, application/soap+xml, */*")
	req.Header.Set("SOAPAction", `"`+strings.ReplaceAll(soapAction, `"`, "")+`"`)
	a.addHeaders(req)

	data, resp, err := a.do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("soap %s: %s: %s", a.target, resp.Status, soapProblem(data))
	}

	out := string(data)
	if a.extract {
		if out, err = soapExtract(data); err != nil {
			return nil, fmt.Errorf("soap %s: %w", a.target, err)
		}
	}
	m[message.Body] = out
	m["http.status"] = resp.StatusCode
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		m[message.ContentType] = ct
	}
	return m, nil
}

func (a *soapAction) addHeaders(req *http.Request) {
	for _, h := range a.httpHeaders {
		req.Header.Set(h[0], h[1])
	}
	if a.authorize != "" {
		req.Header.Set("Authorization", a.authorize)
	}
}

// do sends req and reads the whole answer.
func (a *soapAction) do(req *http.Request) ([]byte, *http.Response, error) {
	resp, err := a.client.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("soap %s: %w", req.URL, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBodySize+1))
	if err == nil && len(data) > maxBodySize {
		err = fmt.Errorf("more than %d bytes", maxBodySize)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("soap %s: reading the answer: %w", req.URL, err)
	}
	return data, resp, nil
}

// soapRoot parses the payload and returns its root element.
func soapRoot(payload string) (*xdm.Node, error) {
	if payload == "" {
		return nil, fmt.Errorf("soap: the body is empty; it must be the XML of the operation")
	}
	tree, err := xdm.ParseString(payload, xdm.ParseOptions{})
	if err != nil {
		return nil, fmt.Errorf("soap: body is not XML: %s", firstLine(err))
	}
	for _, c := range tree.Root.Children {
		if c.Kind == xdm.KindElement {
			return c, nil
		}
	}
	return nil, fmt.Errorf("soap: body has no element")
}

var (
	xmlDeclaration = regexp.MustCompile(`^<\?xml[^>]*\?>\s*`)
	rootStartTag   = regexp.MustCompile(`<[A-Za-z_][^\s/>]*`)
)

// envelope wraps the payload (an XML document whose root element is root) in a
// SOAP envelope with the configured headers. ns, if it is not empty, becomes the
// default namespace of the payload and the headers that have no namespace.
func (a *soapAction) envelope(payload string, root *xdm.Node, ns string) string {
	payload = xmlDeclaration.ReplaceAllString(payload, "")
	if ns != "" && root.Name.Prefix == "" && root.Name.URI == "" {
		// The root element has no namespace: give it one, in front of its attributes.
		if loc := rootStartTag.FindStringIndex(payload); loc != nil {
			payload = payload[:loc[1]] + ` xmlns="` + xmlEscape(ns) + `"` + payload[loc[1]:]
		}
	}
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<soap:Envelope xmlns:soap="` + soap11NS + `">`)
	if len(a.soapHeaders) > 0 {
		b.WriteString("<soap:Header>")
		for _, h := range a.soapHeaders {
			b.WriteString("<" + h.Name)
			if ns != "" {
				b.WriteString(` xmlns="` + xmlEscape(ns) + `"`)
			}
			for _, at := range h.Attrs {
				if at.Value != nil {
					b.WriteString(" " + at.Name + `="` + xmlEscape(fmt.Sprint(at.Value)) + `"`)
				}
			}
			if h.Value == nil {
				b.WriteString("/>")
			} else {
				b.WriteString(">" + xmlEscape(fmt.Sprint(h.Value)) + "</" + h.Name + ">")
			}
		}
		b.WriteString("</soap:Header>")
	}
	b.WriteString("<soap:Body>" + payload + "</soap:Body></soap:Envelope>")
	return b.String()
}

func xmlEscape(s string) string {
	var b bytes.Buffer
	xml.EscapeText(&b, []byte(s))
	return strings.ReplaceAll(b.String(), `"`, "&#34;")
}

// soapExtract returns the first element of the SOAP body of an answer, after an
// XML declaration. A fault is an error.
func soapExtract(data []byte) (string, error) {
	tree, err := xdm.ParseString(string(data), xdm.ParseOptions{})
	if err != nil {
		return "", fmt.Errorf("the answer is not XML: %s", firstLine(err))
	}
	for _, env := range tree.Root.Children {
		if env.Kind != xdm.KindElement || env.Name.Local != "Envelope" || env.Name.URI != soap11NS && env.Name.URI != soap12NS {
			continue
		}
		for _, body := range env.Children {
			if body.Kind != xdm.KindElement || body.Name.Local != "Body" || body.Name.URI != env.Name.URI {
				continue
			}
			for _, c := range body.Children {
				if c.Kind != xdm.KindElement {
					continue
				}
				if c.Name.Local == "Fault" && c.Name.URI == env.Name.URI {
					return "", fmt.Errorf("SOAP fault: %s", faultText(c))
				}
				return `<?xml version="1.0" encoding="UTF-8"?>` + "\n" + nodeXML(c), nil
			}
			return "", fmt.Errorf("the SOAP body of the answer is empty")
		}
	}
	return "", fmt.Errorf("the answer is not a SOAP envelope")
}

// faultText is the faultstring of a fault (SOAP 1.1), or its reason (1.2).
func faultText(fault *xdm.Node) string {
	var find func(n *xdm.Node) string
	find = func(n *xdm.Node) string {
		for _, c := range n.Children {
			if c.Kind != xdm.KindElement {
				continue
			}
			if c.Name.Local == "faultstring" || c.Name.Local == "Text" {
				return strings.TrimSpace(c.StringValue())
			}
			if s := find(c); s != "" {
				return s
			}
		}
		return ""
	}
	if s := find(fault); s != "" {
		return s
	}
	return strings.TrimSpace(fault.StringValue())
}

// soapProblem says what an answer with an error status says: its fault, or
// the start of it.
func soapProblem(data []byte) string {
	if tree, err := xdm.ParseString(string(data), xdm.ParseOptions{}); err == nil {
		var walk func(n *xdm.Node) string
		walk = func(n *xdm.Node) string {
			for _, c := range n.Children {
				if c.Kind != xdm.KindElement {
					continue
				}
				if c.Name.Local == "Fault" {
					return "SOAP fault: " + faultText(c)
				}
				if s := walk(c); s != "" {
					return s
				}
			}
			return ""
		}
		if s := walk(tree.Root); s != "" {
			return s
		}
	}
	s := strings.Join(strings.Fields(string(data)), " ")
	if len(s) > 200 {
		s = s[:200] + "..."
	}
	return s
}

// ---- the WSDL

// soapWSDL is what the step needs of a WSDL: the namespace of the elements
// the schemas declare, by name, and the SOAPAction of each operation.
type soapWSDL struct {
	targetNS string
	elements map[string]string // element name -> the target namespace of its schema
	actions  map[string]string // operation -> soapAction
}

// namespaceOf is the namespace of the payload of an operation.
func (w *soapWSDL) namespaceOf(operation string) string {
	if ns, ok := w.elements[operation]; ok {
		return ns
	}
	return w.targetNS
}

type wsdlDocument struct {
	TargetNamespace string `xml:"targetNamespace,attr"`
	Types           []struct {
		Schemas []struct {
			TargetNamespace string `xml:"targetNamespace,attr"`
			Elements        []struct {
				Name string `xml:"name,attr"`
			} `xml:"element"`
		} `xml:"schema"`
	} `xml:"types"`
	Bindings []struct {
		Operations []struct {
			Name string `xml:"name,attr"`
			SOAP []struct {
				Action string `xml:"soapAction,attr"`
			} `xml:"operation"`
		} `xml:"operation"`
	} `xml:"binding"`
}

func parseWSDL(data []byte) (*soapWSDL, error) {
	var d wsdlDocument
	if err := xml.Unmarshal(data, &d); err != nil {
		return nil, fmt.Errorf("not XML: %w", err)
	}
	if d.TargetNamespace == "" && len(d.Bindings) == 0 {
		return nil, fmt.Errorf("not a WSDL")
	}
	w := &soapWSDL{targetNS: d.TargetNamespace, elements: map[string]string{}, actions: map[string]string{}}
	for _, t := range d.Types {
		for _, s := range t.Schemas {
			for _, e := range s.Elements {
				if _, dup := w.elements[e.Name]; !dup {
					w.elements[e.Name] = s.TargetNamespace
				}
			}
		}
	}
	for _, b := range d.Bindings {
		for _, op := range b.Operations {
			if _, dup := w.actions[op.Name]; !dup && len(op.SOAP) > 0 {
				w.actions[op.Name] = op.SOAP[0].Action
			}
		}
	}
	return w, nil
}

// readWSDL returns the WSDL, reading it the first time it is needed. A failure
// is not kept: the next message tries again.
func (a *soapAction) readWSDL(ctx context.Context) (*soapWSDL, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.wsdl != nil {
		return a.wsdl, nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, a.wsdlURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/xml, application/xml, */*")
	a.addHeaders(req)
	data, resp, err := a.do(req)
	if err != nil {
		return nil, fmt.Errorf("WSDL: %w", err)
	}
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("soap: WSDL %s: %s", a.wsdlURL, resp.Status)
	}
	w, err := parseWSDL(data)
	if err != nil {
		return nil, fmt.Errorf("soap: WSDL %s: %w", a.wsdlURL, err)
	}
	a.wsdl = w
	return w, nil
}
