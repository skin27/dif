package impl

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"dif/keystore"
	"dif/message"
	stepdef "dif/steps/definition"
)

// httpsAction calls an HTTPS endpoint with the message and replaces the body
// with the response. The message body is sent (with POST, PUT, PATCH and
// DELETE) and its string headers become HTTP headers, except those that
// excludeHeaders matches; metadata and http.* headers are never sent directly.
// Trace identity is mapped explicitly to DIF-Trace-Id. With authMethod Basic
// the credentials are sent with every request. The response sets the body,
// http.status and Content-Type. Cookies of the cookie store go along, and
// cookies the server sets are kept. Only servers whose certificate chains to
// the trust store are trusted.
//
// With retryRequests a call that cannot connect, or that the server answers
// with 503, is tried again up to retryAttempts times, retryInterval apart.
type httpsAction struct {
	url            string      // the address, unless it has ${...} parts
	dynamicURL     *expression // the address with ${...} parts, evaluated for each message; nil if url is it
	method         string
	client         *http.Client
	throwOnFailure bool
	basicAuth      bool
	user, password string
	exclude        *regexp.Regexp // names of the headers not to send; nil for none
	retries        int
	retryInterval  time.Duration
}

// notForwarded are headers that belong to one HTTP hop, so a message carrying
// them from an https source does not pass them on.
var notForwarded = map[string]bool{
	"Accept-Encoding": true, "Connection": true, "Content-Length": true, "Host": true, "Keep-Alive": true,
	"Proxy-Authorization": true, "Proxy-Connection": true, "Te": true, "Trailer": true, "Transfer-Encoding": true, "Upgrade": true,
}

func newHTTPSAction(_ string, p stepdef.Params) (stepdef.Processor, error) {
	a := httpsAction{
		method:         p["httpMethod"].(string),
		throwOnFailure: p["throwExceptionOnFailure"].(bool) || p["useErrorRoute"].(bool),
		retryInterval:  time.Duration(p["retryInterval"].(int)) * time.Millisecond,
	}
	if p["retryRequests"].(bool) {
		a.retries = p["retryAttempts"].(int)
	}
	var err error
	if target := p["path"].(string); strings.Contains(target, "${") {
		x, err := compileExpressionIn(flowOf(p), "simple", target)
		if err != nil {
			return nil, fmt.Errorf("uri: %w", err)
		}
		a.dynamicURL = &x
	} else if a.url, err = httpsURL(target); err != nil {
		return nil, fmt.Errorf("uri: %w", err)
	}
	if p["mutualTls"].(bool) {
		return nil, fmt.Errorf("option mutualTls: mutual TLS is not supported yet")
	}
	if p["authMethod"] == "Basic" {
		if a.user, _ = p["authUsername"].(string); a.user == "" {
			return nil, fmt.Errorf("option authUsername: required with authMethod Basic")
		}
		a.basicAuth = true
		a.password, _ = p["authPassword"].(string)
	}
	if pattern, _ := p["excludeHeaders"].(string); pattern != "" {
		re, err := regexp.Compile("(?i)^(?:" + pattern + ")$")
		if err != nil {
			return nil, fmt.Errorf("option excludeHeaders: %w", err)
		}
		a.exclude = re
	}
	if a.client, err = httpsClient(p); err != nil {
		return nil, err
	}
	return a, nil
}

// httpsURL returns the address a step names: https://host[:port]/path, either
// as the DIL writes it, https:https://host/path, or as the URI of the step
// itself gives it, https://host/path (so s is //host/path).
func httpsURL(s string) (string, error) {
	s = strings.TrimSpace(s)
	if rest, ok := strings.CutPrefix(s, "//"); ok {
		s = "https://" + rest
	}
	u, err := url.Parse(s)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		return "", fmt.Errorf("want https://host[:port]/path")
	}
	return u.String(), nil
}

// httpsClient returns an HTTP client that trusts the trust store of the options
// trustStoreFile and trustStorePassword (the system's roots if trustStoreFile is
// empty), takes at most connectTimeout ms (if the option is there) to connect,
// and times out after socketTimeout ms.
func httpsClient(p stepdef.Params) (*http.Client, error) {
	var pool *x509.CertPool // nil: the system's roots
	if file := p["trustStoreFile"].(string); file != "" {
		pw, given, err := trustStorePassword.get(p)
		if err != nil {
			return nil, err
		}
		if pool, err = keystore.LoadTrustPool(file, pw); err != nil {
			return nil, trustStorePassword.explain("trust store", err, given)
		}
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if ms, ok := p["connectTimeout"].(int); ok {
		transport.DialContext = (&net.Dialer{Timeout: time.Duration(ms) * time.Millisecond, KeepAlive: 30 * time.Second}).DialContext
	}
	transport.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	return &http.Client{Transport: transport, Timeout: time.Duration(p["socketTimeout"].(int)) * time.Millisecond}, nil
}

func (a httpsAction) Process(ctx context.Context, m message.Message) (message.Message, error) {
	m.EnsureIdentity()
	var body io.Reader
	switch a.method {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodTrace:
	default:
		body = bytes.NewReader(bytesOf(m[message.Body]))
	}
	target := a.url
	if a.dynamicURL != nil {
		v, err := a.dynamicURL.eval(m)
		if err != nil {
			return nil, err
		}
		if target, err = httpsURL(v); err != nil {
			return nil, fmt.Errorf("url %q: %w", v, err)
		}
	}
	req, err := http.NewRequestWithContext(ctx, a.method, target, body)
	if err != nil {
		return nil, err
	}
	for k, v := range m {
		s, ok := v.(string)
		if !ok || k == message.Body || message.IsMetadata(k) || strings.HasPrefix(k, "http.") || strings.EqualFold(k, traceIDHeader) ||
			notForwarded[http.CanonicalHeaderKey(k)] || !validHeader(k, s) {
			continue
		}
		req.Header.Set(k, s)
	}
	writeIdentityHeaders(req.Header, m)
	cookies.addTo(req)
	if a.basicAuth {
		req.SetBasicAuth(a.user, a.password)
	}
	for k := range req.Header {
		if a.exclude != nil && a.exclude.MatchString(k) {
			delete(req.Header, k)
		}
	}

	resp, err := a.send(ctx, req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	cookies.keep(req.URL, resp.Cookies())
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBodySize))
	if err != nil {
		return nil, fmt.Errorf("%s %s: reading response: %w", a.method, target, err)
	}
	if a.throwOnFailure && resp.StatusCode >= 400 {
		return nil, fmt.Errorf("%s %s: %s", a.method, target, resp.Status)
	}

	m[message.Body] = string(data)
	m["http.status"] = resp.StatusCode
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		m[message.ContentType] = ct
	} else {
		delete(m, message.ContentType)
	}
	return m, nil
}

// send makes the call, again if retryRequests asks for it and the call cannot
// connect or the server answers 503.
func (a httpsAction) send(ctx context.Context, req *http.Request) (*http.Response, error) {
	for attempt := 0; ; attempt++ {
		resp, err := a.client.Do(req)
		retry := err != nil && ctx.Err() == nil || err == nil && resp.StatusCode == http.StatusServiceUnavailable
		if !retry || attempt >= a.retries {
			return resp, err
		}
		if resp != nil {
			io.Copy(io.Discard, io.LimitReader(resp.Body, maxBodySize))
			resp.Body.Close()
		}
		select {
		case <-time.After(a.retryInterval):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		if req.GetBody != nil {
			body, err := req.GetBody()
			if err != nil {
				return nil, err
			}
			req.Body = body
		}
	}
}

// validHeader reports whether name and value can be sent as an HTTP header:
// the name is a token and the value has no line breaks.
func validHeader(name, value string) bool {
	if name == "" || strings.ContainsAny(value, "\r\n") {
		return false
	}
	for _, c := range name {
		if c <= ' ' || c >= 0x7f || strings.ContainsRune(`"(),/:;<=>?@[\]{}`, c) {
			return false
		}
	}
	return true
}
