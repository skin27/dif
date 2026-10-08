package impl

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"
	"time"

	"dif/keystore"
	"dif/message"
	stepdef "dif/steps/definition"
)

// httpsSource receives HTTPS requests on host:port/path and replies with the
// outcome of the flow (request-reply). The body of a request is the message
// body and its headers are message headers; the reply is the final body with
// the message's headers (see writeMessageHeaders), its Content-Type and its
// identity headers. A one-way (InOnly) source replies at once with the
// request instead.
type httpsSource struct {
	addr, path          string
	matchPrefix         bool
	oneWay              bool
	preserveHTTPHeaders bool
	method              string // the only HTTP method served; "" for any
	produces            string // Content-Type of a reply that sets none; "" for text
	identity            string // absolute path of the keystore
	cert                tls.Certificate
}

func newHTTPSSource(_ string, p stepdef.Params) (stepdef.Processor, error) {
	addr, path, err := splitAddress(p["path"].(string))
	if err != nil {
		return nil, err
	}
	abs, cert, err := loadServerIdentity(p)
	if err != nil {
		return nil, err
	}
	return httpsSource{
		addr:                addr,
		path:                path,
		matchPrefix:         p["matchPrefix"].(bool) || p["matchOnUriPrefix"].(bool),
		oneWay:              p["exchangePattern"] == message.InOnly,
		preserveHTTPHeaders: p["preserveHttpHeaders"].(bool),
		identity:            abs,
		cert:                cert,
	}, nil
}

// loadServerIdentity loads the keystore of the options serverIdentityFile and
// serverIdentityPassword, and returns its absolute path and certificate.
func loadServerIdentity(p stepdef.Params) (string, tls.Certificate, error) {
	file := p["serverIdentityFile"].(string)
	pw, given, err := serverIdentityPassword.get(p)
	if err != nil {
		return "", tls.Certificate{}, err
	}
	cert, err := keystore.LoadIdentity(file, pw)
	if err != nil {
		return "", cert, serverIdentityPassword.explain("server identity", err, given)
	}
	abs, err := filepath.Abs(file)
	return abs, cert, err
}

// Run serves the path until ctx is done. It fails when the address cannot be
// bound or the path is already served by another flow.
func (s httpsSource) Run(ctx context.Context, emit stepdef.Emit) error {
	return s.RunReady(ctx, emit, func() {})
}

func (s httpsSource) RunReady(ctx context.Context, emit stepdef.Emit, ready func()) error {
	unregister, server, err := serveHTTPS(s.addr, s.identity, s.cert, s.path, s.matchPrefix, s.handler(emit))
	if err != nil {
		return err
	}
	defer func() {
		shutdown := stepdef.ShutdownContext(ctx)
		if shutdown == nil {
			var cancel context.CancelFunc
			shutdown, cancel = context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
		}
		unregister(shutdown)
	}()
	ready()
	select {
	case <-ctx.Done():
		return nil
	case <-server.done:
		if ctx.Err() != nil {
			return nil
		}
		err := fmt.Errorf("HTTPS listener stopped: %w", server.err)
		stepdef.ReportSourceFailure(ctx, err)
		return err
	}
}

func (s httpsSource) handler(emit stepdef.Emit) http.HandlerFunc {
	type outcome struct {
		m   message.Message
		err error
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if s.method != "" && r.Method != s.method {
			w.Header().Set("Allow", s.method)
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodySize))
		if err != nil {
			http.Error(w, "cannot read request: "+err.Error(), http.StatusBadRequest)
			return
		}

		// Header names arrive canonicalized ("Content-Type"), so they never
		// clash with the body or metadata keys.
		m := message.Message{message.Body: string(body)}
		for k, v := range r.Header {
			m[k] = strings.Join(v, ",")
		}
		addQueryHeaders(m, r.URL.Query())
		m[message.Timestamp] = time.Now().Format(time.RFC3339Nano)
		if id := r.Header.Get(traceIDHeader); id != "" {
			m[message.TraceID] = id
		}
		delete(m, http.CanonicalHeaderKey(traceIDHeader))
		m.EnsureIdentity()
		if s.preserveHTTPHeaders {
			m["http.method"] = r.Method
			m["http.path"] = r.URL.Path
			m["http.query"] = r.URL.RawQuery
			m["http.uri"] = r.URL.RequestURI()
		}

		if s.oneWay {
			reply := m.Copy()
			if emit(m, nil) != nil {
				http.Error(w, "flow is not running", http.StatusServiceUnavailable)
				return
			}
			writeReply(w, reply, s.produces)
			return
		}

		done := make(chan outcome, 1)
		if emit(m, func(out message.Message, err error) { done <- outcome{out, err} }) != nil {
			http.Error(w, "flow is not running", http.StatusServiceUnavailable)
			return
		}
		select {
		case o := <-done:
			if o.err != nil {
				http.Error(w, o.err.Error(), http.StatusInternalServerError)
				return
			}
			writeReply(w, o.m, s.produces)
		case <-r.Context().Done(): // the client went away
		}
	}
}

// addQueryHeaders adds the query parameters as headers, as Camel's HTTP
// consumers do: ?config=A sets the header config. A parameter with several
// values becomes one comma-separated header. A request header of the same name
// stays, and the body and metadata are not reachable from outside.
func addQueryHeaders(m message.Message, query url.Values) {
	for k, v := range query {
		if _, set := m[k]; set || k == message.Body || strings.HasPrefix(k, message.MetadataPrefix) {
			continue
		}
		m[k] = strings.Join(v, ",")
	}
}

// notReturned are the message headers a reply never carries, besides those
// notForwarded names: the credentials of the request, which a flow that keeps
// the request's headers would otherwise send back (and into every log on the
// way), and the date, which the server writes itself.
var notReturned = map[string]bool{"Authorization": true, "Cookie": true, "Date": true}

// writeMessageHeaders adds the headers of m to h, as Camel's HTTP consumers
// return the headers of the message. Not returned are the body, metadata,
// the http.* and error.* headers (internal to DIF), the headers of one HTTP
// hop and the credentials (see notForwarded and notReturned), Content-Type
// (writeReply decides it), and anything whose name is no valid HTTP header
// name or whose value has no text form (maps, slices and bytes). Line breaks
// in a value become spaces, as servlet containers such as Jetty write them,
// so a value can never start another header.
func writeMessageHeaders(h http.Header, m message.Message) {
	for k, v := range m {
		name := http.CanonicalHeaderKey(k)
		if k == message.Body || message.IsMetadata(k) || strings.HasPrefix(k, "http.") || strings.HasPrefix(k, "error.") ||
			name == "Content-Type" || notForwarded[name] || notReturned[name] {
			continue
		}
		if s, ok := headerText(v); ok && validHeader(k, "") {
			h.Set(k, singleLine(s))
		}
	}
}

// singleLine replaces the control characters of a header value, such as line
// breaks, by spaces.
func singleLine(s string) string {
	return strings.Map(func(r rune) rune {
		if r < ' ' && r != '\t' || r == 0x7f {
			return ' '
		}
		return r
	}, s)
}

// headerText renders a header value that has a plain text form.
func headerText(v any) (string, bool) {
	switch x := v.(type) {
	case string:
		return x, true
	case bool, int, int64, float64:
		return fmt.Sprint(x), true
	}
	return "", false
}

// writeReply writes the body of m with its headers (see writeMessageHeaders),
// its Content-Type, else produces, else text.
func writeReply(w http.ResponseWriter, m message.Message, produces string) {
	writeMessageHeaders(w.Header(), m)
	writeIdentityHeaders(w.Header(), m)
	ct, _ := m[message.ContentType].(string)
	if ct == "" {
		ct = produces
	}
	if ct == "" {
		ct = "text/plain; charset=utf-8"
	}
	w.Header().Set("Content-Type", ct)
	w.Write(bytesOf(m[message.Body]))
}

// splitAddress splits the path of an https URI, //host[:port]/path, into the
// address to listen on (port 443 by default) and the path.
func splitAddress(uriPath string) (addr, path string, err error) {
	rest, ok := strings.CutPrefix(uriPath, "//")
	host, path, _ := strings.Cut(rest, "/")
	if !ok || host == "" {
		return "", "", fmt.Errorf("uri: want https://host:port/path")
	}
	if _, _, err := net.SplitHostPort(host); err != nil {
		host = net.JoinHostPort(host, "443")
	}
	return host, "/" + path, nil
}

// keystorePassword is where a step gets a keystore password: the option, or
// else the environment variable.
type keystorePassword struct{ option, env string }

var (
	serverIdentityPassword = keystorePassword{"serverIdentityPassword", "DIF_SERVER_IDENTITY_PASSWORD"}
	trustStorePassword     = keystorePassword{"trustStorePassword", "DIF_TRUSTSTORE_PASSWORD"}
)

// get returns the password; given reports whether one was set at all (a
// keystore may have an empty password).
func (k keystorePassword) get(p stepdef.Params) (pw string, given bool, err error) {
	if pw, ok := p[k.option].(string); ok {
		return pw, true, nil
	}
	return environmentSecret(k.env)
}

// explain wraps a keystore error; when no password was given and the
// keystore needs one, it says how to give it.
func (k keystorePassword) explain(what string, err error, given bool) error {
	if !given && errors.Is(err, keystore.ErrWrongPassword) {
		return fmt.Errorf("%s: %w; no password was given: set the %s option or the %s environment variable", what, err, k.option, k.env)
	}
	return fmt.Errorf("%s: %w", what, err)
}
