package impl

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"dif/message"
	stepdef "dif/steps/definition"
)

const countryWSDL = `<?xml version="1.0" encoding="UTF-8"?>
<definitions xmlns="http://schemas.xmlsoap.org/wsdl/" xmlns:soap="http://schemas.xmlsoap.org/wsdl/soap/" xmlns:xs="http://www.w3.org/2001/XMLSchema"
	xmlns:tns="http://www.oorsprong.org/websamples.countryinfo" targetNamespace="http://www.oorsprong.org/websamples.countryinfo">
	<types>
		<xs:schema elementFormDefault="qualified" targetNamespace="http://www.oorsprong.org/websamples.countryinfo">
			<xs:element name="ListOfCountryNamesByName"><xs:complexType/></xs:element>
			<xs:element name="FullCountryInfo"><xs:complexType/></xs:element>
		</xs:schema>
		<xs:schema targetNamespace="http://other.example/types"><xs:element name="Odd"/></xs:schema>
	</types>
	<binding name="CountryInfoServiceSoapBinding" type="tns:CountryInfoServiceSoapType">
		<soap:binding style="document" transport="http://schemas.xmlsoap.org/soap/http"/>
		<operation name="ListOfCountryNamesByName"><soap:operation soapAction="" style="document"/></operation>
		<operation name="FullCountryInfo"><soap:operation soapAction="http://www.oorsprong.org/websamples.countryinfo/FullCountryInfo" style="document"/></operation>
	</binding>
</definitions>`

const countryAnswer = `<?xml version="1.0" encoding="utf-8"?>
<soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/" xmlns:unused="urn:unused">
	<soap:Body>
		<m:ListOfCountryNamesByNameResponse xmlns:m="http://www.oorsprong.org/websamples.countryinfo">
			<m:ListOfCountryNamesByNameResult>
				<m:tCountryCodeAndName><m:sISOCode>AX</m:sISOCode><m:sName>Åland &amp; Islands</m:sName></m:tCountryCodeAndName>
			</m:ListOfCountryNamesByNameResult>
		</m:ListOfCountryNamesByNameResponse>
	</soap:Body>
</soap:Envelope>`

const faultAnswer = `<soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/"><soap:Body><soap:Fault><faultcode>soap:Server</faultcode><faultstring>No such country</faultstring></soap:Fault></soap:Body></soap:Envelope>`

// soapServer is a SOAP service that keeps the calls it gets.
type soapServer struct {
	*httptest.Server
	mu      sync.Mutex
	calls   []soapCall
	wsdls   atomic.Int32
	wsdl    string
	answer  string
	status  int
	failWSD atomic.Bool
}

type soapCall struct {
	query  string
	header http.Header
	body   string
}

func newSOAPServer(t *testing.T) *soapServer {
	t.Helper()
	s := &soapServer{wsdl: countryWSDL, answer: countryAnswer, status: 200}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			s.wsdls.Add(1)
			if !strings.EqualFold(r.URL.RawQuery, "wsdl") || s.failWSD.Load() {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "text/xml")
			io.WriteString(w, s.wsdl)
			return
		}
		body, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.calls = append(s.calls, soapCall{r.URL.RawQuery, r.Header.Clone(), string(body)})
		s.mu.Unlock()
		w.Header().Set("Content-Type", "text/xml; charset=utf-8")
		w.WriteHeader(s.status)
		io.WriteString(w, s.answer)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *soapServer) last(t *testing.T) soapCall {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.calls) == 0 {
		t.Fatal("the service was not called")
	}
	return s.calls[len(s.calls)-1]
}

func soapStep(t *testing.T, s *soapServer, opts map[string]any) stepdef.ActionProcessor {
	t.Helper()
	o := map[string]any{"path": s.URL + "/CountryInfoService.wso", "action": "ListOfCountryNamesByName"}
	for k, v := range opts {
		o[k] = v
	}
	return mustProcessor(t, stepdef.Action, "soap", o).(stepdef.ActionProcessor)
}

func callSOAP(t *testing.T, p stepdef.ActionProcessor, body string) message.Message {
	t.Helper()
	m := message.New(body)
	m["Content-Type"] = "Application/Xml"
	m["Authorization"] = "Bearer from-the-message"
	out, err := p.Process(context.Background(), m)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestSOAPSmartExtract(t *testing.T) {
	s := newSOAPServer(t)
	p := soapStep(t, s, map[string]any{"extract": true})
	out := callSOAP(t, p, "<ListOfCountryNamesByName/>")

	// The answer: the first element of the body, after an XML declaration,
	// with the namespace declarations it uses and no more.
	want := "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n" +
		"<m:ListOfCountryNamesByNameResponse xmlns:m=\"http://www.oorsprong.org/websamples.countryinfo\">\n" +
		"\t\t\t<m:ListOfCountryNamesByNameResult>\n" +
		"\t\t\t\t<m:tCountryCodeAndName><m:sISOCode>AX</m:sISOCode><m:sName>Åland &amp; Islands</m:sName></m:tCountryCodeAndName>\n" +
		"\t\t\t</m:ListOfCountryNamesByNameResult>\n" +
		"\t\t</m:ListOfCountryNamesByNameResponse>"
	if out[message.Body] != want {
		t.Errorf("body =\n%v\nwant\n%v", out[message.Body], want)
	}
	if out["http.status"] != 200 || out[message.ContentType] != "text/xml; charset=utf-8" {
		t.Errorf("status = %v, content type = %v", out["http.status"], out[message.ContentType])
	}

	// The call: an envelope, the payload in the namespace of the operation,
	// the SOAPAction of the WSDL (there it is empty).
	c := s.last(t)
	if !strings.Contains(c.body, `<soap:Body><ListOfCountryNamesByName xmlns="http://www.oorsprong.org/websamples.countryinfo"/></soap:Body>`) ||
		!strings.HasPrefix(c.body, "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<soap:Envelope xmlns:soap=\"http://schemas.xmlsoap.org/soap/envelope/\">") {
		t.Errorf("envelope = %s", c.body)
	}
	if c.header.Get("Content-Type") != "text/xml; charset=utf-8" || c.header.Get("SOAPAction") != `""` {
		t.Errorf("headers = %v", c.header)
	}
	if got := c.header.Get("Authorization"); got != "" {
		t.Errorf("the Authorization header of the message was sent: %q", got)
	}
	if c.query != "" {
		t.Errorf("query = %q", c.query)
	}

	// The WSDL is read once.
	callSOAP(t, p, "<ListOfCountryNamesByName/>")
	if n := s.wsdls.Load(); n != 1 {
		t.Errorf("the WSDL was read %d times, want once", n)
	}
}

func TestSOAPWithoutExtractAnswersAsItCame(t *testing.T) {
	s := newSOAPServer(t)
	out := callSOAP(t, soapStep(t, s, nil), "<ListOfCountryNamesByName/>")
	if out[message.Body] != countryAnswer {
		t.Errorf("body = %v", out[message.Body])
	}
}

func TestSOAPSOAPActionAndNamespaceFromTheWSDL(t *testing.T) {
	s := newSOAPServer(t)
	callSOAP(t, soapStep(t, s, map[string]any{"action": "FullCountryInfo"}), `<?xml version="1.0"?><FullCountryInfo a="1"><sCountryISOCode>NL</sCountryISOCode></FullCountryInfo>`)
	c := s.last(t)
	if got := c.header.Get("SOAPAction"); got != `"http://www.oorsprong.org/websamples.countryinfo/FullCountryInfo"` {
		t.Errorf("SOAPAction = %s", got)
	}
	if !strings.Contains(c.body, `<soap:Body><FullCountryInfo xmlns="http://www.oorsprong.org/websamples.countryinfo" a="1"><sCountryISOCode>NL</sCountryISOCode></FullCountryInfo></soap:Body>`) {
		t.Errorf("envelope = %s", c.body)
	}

	// Without an action it is the first element of the body; the namespace
	// is the one of the schema that declares the element.
	callSOAP(t, soapStep(t, s, map[string]any{"action": ""}), "<Odd/>")
	if body := s.last(t).body; !strings.Contains(body, `<Odd xmlns="http://other.example/types"/>`) {
		t.Errorf("envelope = %s", body)
	}

	// A payload that has a namespace keeps it.
	callSOAP(t, soapStep(t, s, nil), `<x:ListOfCountryNamesByName xmlns:x="urn:mine"/>`)
	if body := s.last(t).body; !strings.Contains(body, `<soap:Body><x:ListOfCountryNamesByName xmlns:x="urn:mine"/></soap:Body>`) {
		t.Errorf("envelope = %s", body)
	}
	callSOAP(t, soapStep(t, s, nil), `<ListOfCountryNamesByName xmlns="urn:mine"/>`)
	if body := s.last(t).body; strings.Contains(body, "oorsprong") {
		t.Errorf("a default namespace was replaced: %s", body)
	}
}

func TestSOAPNotSmart(t *testing.T) {
	s := newSOAPServer(t)
	p := soapStep(t, s, map[string]any{"smart": false, "action": "ListOfCountryNamesByName"})
	callSOAP(t, p, "<ListOfCountryNamesByName/>")
	c := s.last(t)
	if s.wsdls.Load() != 0 {
		t.Error("the WSDL was read")
	}
	if c.header.Get("SOAPAction") != `"ListOfCountryNamesByName"` || !strings.Contains(c.body, "<soap:Body><ListOfCountryNamesByName/></soap:Body>") {
		t.Errorf("SOAPAction = %s, envelope = %s", c.header.Get("SOAPAction"), c.body)
	}
}

func TestSOAPAnEnvelopeIsSentAsItIs(t *testing.T) {
	s := newSOAPServer(t)
	const env = `<soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/"><soap:Body><A xmlns="urn:a"/></soap:Body></soap:Envelope>`
	callSOAP(t, soapStep(t, s, map[string]any{"smart": false}), env)
	if got := s.last(t).body; got != env {
		t.Errorf("sent %s", got)
	}
}

func TestSOAPHeadersAndCredentials(t *testing.T) {
	s := newSOAPServer(t)
	headers := `[{"_id":"1","name":"AuthenticateToken","attrs":[{"name":"Username","value":"me & you"},{"name":"Store","value":null},{"name":"Company","value":2}]},{"name":"Plain","value":"a<b"}]`
	httpHeaders := `[{"_id":"2","name":"ApiToken","value":"123"}]`
	p := soapStep(t, s, map[string]any{"headers": headers, "httpHeaders": httpHeaders, "token": "tok"})
	callSOAP(t, p, "<ListOfCountryNamesByName/>")
	c := s.last(t)
	want := `<soap:Header><AuthenticateToken xmlns="http://www.oorsprong.org/websamples.countryinfo" Username="me &amp; you" Company="2"/>` +
		`<Plain xmlns="http://www.oorsprong.org/websamples.countryinfo">a&lt;b</Plain></soap:Header><soap:Body>`
	if !strings.Contains(c.body, want) {
		t.Errorf("envelope = %s\nwant it to contain %s", c.body, want)
	}
	if c.header.Get("ApiToken") != "123" || c.header.Get("Authorization") != "Bearer tok" {
		t.Errorf("headers = %v", c.header)
	}

	// auth (the base64 of user:password) wins over token.
	auth := base64.StdEncoding.EncodeToString([]byte("user:secret"))
	callSOAP(t, soapStep(t, s, map[string]any{"auth": auth, "token": "tok"}), "<ListOfCountryNamesByName/>")
	if got := s.last(t).header.Get("Authorization"); got != "Basic "+auth {
		t.Errorf("Authorization = %q", got)
	}
}

func TestSOAPFaults(t *testing.T) {
	s := newSOAPServer(t)
	s.answer, s.status = faultAnswer, 500
	p := soapStep(t, s, map[string]any{"smart": false})
	if _, err := p.Process(context.Background(), message.New("<A/>")); err == nil || !strings.Contains(err.Error(), "500") || !strings.Contains(err.Error(), "SOAP fault: No such country") {
		t.Errorf("err = %v, want the status and the fault", err)
	}

	// A fault in an answer with a good status fails an extract, not a plain call.
	s.status = 200
	pe := soapStep(t, s, map[string]any{"smart": false, "extract": true})
	if _, err := pe.Process(context.Background(), message.New("<A/>")); err == nil || !strings.Contains(err.Error(), "SOAP fault: No such country") {
		t.Errorf("err = %v, want the fault", err)
	}
	if out, err := p.Process(context.Background(), message.New("<A/>")); err != nil || out[message.Body] != faultAnswer {
		t.Errorf("out = %v, err = %v", out[message.Body], err)
	}

	for answer, want := range map[string]string{
		"not xml <": "the answer is not XML",
		"<other/>":  "not a SOAP envelope",
		`<e:Envelope xmlns:e="http://schemas.xmlsoap.org/soap/envelope/"><e:Body/></e:Envelope>`: "SOAP body of the answer is empty",
	} {
		s.answer = answer
		if _, err := pe.Process(context.Background(), message.New("<A/>")); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("answer %q: err = %v, want %q", answer, err, want)
		}
	}

	s.status, s.answer = 502, "<html>\n bad   gateway </html>"
	if _, err := p.Process(context.Background(), message.New("<A/>")); err == nil || !strings.Contains(err.Error(), "502") || !strings.Contains(err.Error(), "<html> bad gateway </html>") {
		t.Errorf("err = %v", err)
	}
}

func TestSOAPParams(t *testing.T) {
	s := newSOAPServer(t)
	// wsdl, in any case, is the query that fetches the WSDL; the call has none.
	callSOAP(t, soapStep(t, s, map[string]any{"params": "WSDL"}), "<ListOfCountryNamesByName/>")
	if q := s.last(t).query; q != "" {
		t.Errorf("query of the call = %q", q)
	}
	// Anything else is a query of every call.
	callSOAP(t, soapStep(t, s, map[string]any{"params": "?key=1&b=x%20y"}), "<ListOfCountryNamesByName/>")
	if q := s.last(t).query; q != "key=1&b=x%20y" {
		t.Errorf("query of the call = %q", q)
	}
}

func TestSOAPWSDLProblems(t *testing.T) {
	s := newSOAPServer(t)
	s.failWSD.Store(true)
	p := soapStep(t, s, nil)
	if _, err := p.Process(context.Background(), message.New("<A/>")); err == nil || !strings.Contains(err.Error(), "WSDL") {
		t.Fatalf("err = %v, want the WSDL to fail the message", err)
	}
	if len(s.calls) != 0 {
		t.Error("the service was called without a WSDL")
	}
	// The next message tries again.
	s.failWSD.Store(false)
	callSOAP(t, p, "<ListOfCountryNamesByName/>")

	s2 := newSOAPServer(t)
	s2.wsdl = "<html/>"
	if _, err := soapStep(t, s2, nil).Process(context.Background(), message.New("<A/>")); err == nil || !strings.Contains(err.Error(), "not a WSDL") {
		t.Errorf("err = %v", err)
	}
}

func TestSOAPBodyProblems(t *testing.T) {
	s := newSOAPServer(t)
	p := soapStep(t, s, map[string]any{"smart": false})
	for body, want := range map[string]string{
		"":              "body is empty",
		"just text":     "body is not XML",
		"<a><b></a>":    "body is not XML",
		"<!-- only -->": "body is not XML",
	} {
		if _, err := p.Process(context.Background(), message.New(body)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("body %q: err = %v, want %q", body, err, want)
		}
	}
}

func TestSOAPInvalidOptions(t *testing.T) {
	wantInvalid(t, stepdef.Action, "soap", nil, "missing required option path")
	wantInvalid(t, stepdef.Action, "soap", map[string]any{"path": "ftp://host/x"}, "uri: want http")
	wantInvalid(t, stepdef.Action, "soap", map[string]any{"path": "//host"}, "uri: want http")
	wantInvalid(t, stepdef.Action, "soap", map[string]any{"path": "http://h/x", "headers": "nope"}, "option headers")
	wantInvalid(t, stepdef.Action, "soap", map[string]any{"path": "http://h/x", "headers": `[{"name":"a b"}]`}, "is not an XML name")
	wantInvalid(t, stepdef.Action, "soap", map[string]any{"path": "http://h/x", "headers": `[{"name":"A","attrs":[{"name":"1x","value":"v"}]}]`}, "attribute")
	wantInvalid(t, stepdef.Action, "soap", map[string]any{"path": "http://h/x", "httpHeaders": "nope"}, "option httpHeaders")
	wantInvalid(t, stepdef.Action, "soap", map[string]any{"path": "http://h/x", "httpHeaders": `[{"name":"Bad Name","value":"v"}]`}, "cannot be an HTTP header")
	wantInvalid(t, stepdef.Action, "soap", map[string]any{"path": "http://h/x", "token": "a\nb"}, "cannot be an HTTP header")
	wantInvalid(t, stepdef.Action, "soap", map[string]any{"path": "http://h/x", "params": "a=b c"}, "must be URL-encoded")
	wantInvalid(t, stepdef.Action, "soap", map[string]any{"path": "http://h/x", "unknown": 1}, "unknown option")
}

func TestSOAPConcurrentFirstCalls(t *testing.T) {
	s := newSOAPServer(t)
	p := soapStep(t, s, map[string]any{"extract": true})
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := p.Process(context.Background(), message.New("<ListOfCountryNamesByName/>")); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if n := s.wsdls.Load(); n != 1 {
		t.Errorf("the WSDL was read %d times, want once", n)
	}
}
