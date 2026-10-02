package impl

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"dif/keystore"
	"dif/message"
	stepdef "dif/steps/definition"
)

// httpsAction calls an HTTPS endpoint with the message and replaces the body
// with the response. The message body is sent (except with GET and HEAD) and
// its string headers become HTTP headers; metadata and http.* headers are
// never sent. The response sets the body, http.status and Content-Type.
// Only servers whose certificate chains to the trust store are trusted.
type httpsAction struct {
	url            string
	method         string
	client         *http.Client
	throwOnFailure bool
}

// notForwarded are headers that belong to one HTTP hop, so a message carrying
// them from an https source does not pass them on.
var notForwarded = map[string]bool{
	"Accept-Encoding": true, "Connection": true, "Content-Length": true, "Host": true, "Keep-Alive": true,
	"Proxy-Authorization": true, "Proxy-Connection": true, "Te": true, "Trailer": true, "Transfer-Encoding": true, "Upgrade": true,
}

func newHTTPSAction(_ string, p stepdef.Params) (stepdef.Processor, error) {
	u, err := url.Parse("https:" + p["path"].(string))
	if err != nil || u.Host == "" {
		return nil, fmt.Errorf("uri: want https://host[:port]/path")
	}
	pw, given := trustStorePassword.get(p)
	pool, err := keystore.LoadTrustPool(p["trustStoreFile"].(string), pw)
	if err != nil {
		return nil, trustStorePassword.explain("trust store", err, given)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
	return httpsAction{
		url:            u.String(),
		method:         p["httpMethod"].(string),
		client:         &http.Client{Transport: transport, Timeout: time.Duration(p["socketTimeout"].(int)) * time.Millisecond},
		throwOnFailure: p["throwExceptionOnFailure"].(bool),
	}, nil
}

func (a httpsAction) Process(ctx context.Context, m message.Message) (message.Message, error) {
	var body io.Reader
	if a.method != http.MethodGet && a.method != http.MethodHead {
		body = bytes.NewReader(bytesOf(m[message.Body]))
	}
	req, err := http.NewRequestWithContext(ctx, a.method, a.url, body)
	if err != nil {
		return nil, err
	}
	for k, v := range m {
		s, ok := v.(string)
		if !ok || k == message.Body || message.IsMetadata(k) || strings.HasPrefix(k, "http.") ||
			notForwarded[http.CanonicalHeaderKey(k)] || !validHeader(k, s) {
			continue
		}
		req.Header.Set(k, s)
	}

	resp, err := a.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBodySize))
	if err != nil {
		return nil, fmt.Errorf("%s %s: reading response: %w", a.method, a.url, err)
	}
	if a.throwOnFailure && resp.StatusCode >= 400 {
		return nil, fmt.Errorf("%s %s: %s", a.method, a.url, resp.Status)
	}

	m[message.Body] = string(data)
	m["http.status"] = resp.StatusCode
	if ct := resp.Header.Get("Content-Type"); ct != "" {
		m["Content-Type"] = ct
	} else {
		delete(m, "Content-Type")
	}
	return m, nil
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
