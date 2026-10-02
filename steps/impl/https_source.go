package impl

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"dif/keystore"
	"dif/message"
	stepdef "dif/steps/definition"
)

// httpsSource receives HTTPS requests on host:port/path and replies with the
// outcome of the flow (request-reply). The body of a request is the message
// body and its headers are message headers; the reply is the final body with
// its Content-Type header.
type httpsSource struct {
	addr, path          string
	matchPrefix         bool
	preserveHTTPHeaders bool
	identity            string // absolute path of the keystore
	cert                tls.Certificate
}

func newHTTPSSource(_ string, p stepdef.Params) (stepdef.Processor, error) {
	addr, path, err := splitAddress(p["path"].(string))
	if err != nil {
		return nil, err
	}
	file := p["serverIdentityFile"].(string)
	pw, given := serverIdentityPassword.get(p)
	cert, err := keystore.LoadIdentity(file, pw)
	if err != nil {
		return nil, serverIdentityPassword.explain("server identity", err, given)
	}
	abs, err := filepath.Abs(file)
	if err != nil {
		return nil, err
	}
	return httpsSource{
		addr:                addr,
		path:                path,
		matchPrefix:         p["matchPrefix"].(bool),
		preserveHTTPHeaders: p["preserveHttpHeaders"].(bool),
		identity:            abs,
		cert:                cert,
	}, nil
}

// Run serves the path until ctx is done. It fails when the address cannot be
// bound or the path is already served by another flow.
func (s httpsSource) Run(ctx context.Context, emit stepdef.Emit) error {
	unregister, err := serveHTTPS(s.addr, s.identity, s.cert, s.path, s.matchPrefix, s.handler(emit))
	if err != nil {
		return err
	}
	defer unregister()
	<-ctx.Done()
	return nil
}

func (s httpsSource) handler(emit stepdef.Emit) http.HandlerFunc {
	type outcome struct {
		m   message.Message
		err error
	}
	return func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodySize))
		if err != nil {
			http.Error(w, "cannot read request: "+err.Error(), http.StatusBadRequest)
			return
		}

		// Header names arrive canonicalized ("Content-Type"), so they never
		// clash with the body or metadata keys.
		m := message.New(string(body))
		for k, v := range r.Header {
			m[k] = strings.Join(v, ",")
		}
		if s.preserveHTTPHeaders {
			m["http.method"] = r.Method
			m["http.path"] = r.URL.Path
			m["http.query"] = r.URL.RawQuery
			m["http.uri"] = r.URL.RequestURI()
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
			ct, _ := o.m[message.ContentType].(string)
			if ct == "" {
				ct = "text/plain; charset=utf-8"
			}
			w.Header().Set("Content-Type", ct)
			w.Write(bytesOf(o.m[message.Body]))
		case <-r.Context().Done(): // the client went away
		}
	}
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
func (k keystorePassword) get(p stepdef.Params) (pw string, given bool) {
	if pw, ok := p[k.option].(string); ok {
		return pw, true
	}
	return os.LookupEnv(k.env)
}

// explain wraps a keystore error; when no password was given and the
// keystore needs one, it says how to give it.
func (k keystorePassword) explain(what string, err error, given bool) error {
	if !given && errors.Is(err, keystore.ErrWrongPassword) {
		return fmt.Errorf("%s: %w; no password was given: set the %s option or the %s environment variable", what, err, k.option, k.env)
	}
	return fmt.Errorf("%s: %w", what, err)
}
