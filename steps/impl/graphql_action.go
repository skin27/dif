package impl

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"dif/message"
	stepdef "dif/steps/definition"
)

// graphqlAction posts a GraphQL query to an endpoint and replaces the body
// with the response, as Camel's graphql component does. The query is the
// option query, else the body; variables, if set, is a JSON object. Message
// headers are not sent. An error status (400 or more) fails the message;
// GraphQL errors, which come with status 200, do not.
type graphqlAction struct {
	url, token string
	query      string
	variables  json.RawMessage
	client     *http.Client
}

func newGraphQLAction(_ string, p stepdef.Params) (stepdef.Processor, error) {
	raw := p["url"].(string)
	if raw == "" {
		raw = p["path"].(string) // graphql://<url> in a URI
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, fmt.Errorf("option url: want http(s)://host[:port]/path, got %q", raw)
	}
	a := graphqlAction{url: u.String(), token: p["accessToken"].(string), query: p["query"].(string)}
	if v := p["variables"].(string); v != "" {
		var obj map[string]any
		if err := json.Unmarshal([]byte(v), &obj); err != nil {
			return nil, fmt.Errorf("option variables: want a JSON object: %w", err)
		}
		a.variables = json.RawMessage(v)
	}
	if a.client, err = httpsClient(p); err != nil {
		return nil, err
	}
	return a, nil
}

func (a graphqlAction) Process(ctx context.Context, m message.Message) (message.Message, error) {
	q := a.query
	if q == "" {
		q = text(m[message.Body])
	}
	payload, err := json.Marshal(struct {
		Query     string          `json:"query"`
		Variables json.RawMessage `json:"variables,omitempty"`
	}{q, a.variables})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.url, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if a.token != "" {
		req.Header.Set("Authorization", "Bearer "+a.token)
	}

	resp, err := a.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBodySize))
	if err != nil {
		return nil, fmt.Errorf("POST %s: reading response: %w", a.url, err)
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("POST %s: %s", a.url, resp.Status)
	}
	m[message.Body] = string(data)
	m["http.status"] = resp.StatusCode
	m[message.ContentType] = resp.Header.Get("Content-Type")
	return m, nil
}
