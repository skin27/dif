package impl

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	"dif/message"
	stepdef "dif/steps/definition"
)

// The rest steps are Camel's REST DSL on top of the https steps. The rest
// source serves one method on a path of the REST address, which all rest
// sources share (Camel's rest configuration: https on port 9002); the rest
// action calls a method on a path of a host.

// restMethod returns the HTTP method of a DIL method option, such as "get".
// The frontend writes "option" for OPTIONS.
func restMethod(m string) string {
	if m == "option" {
		return "OPTIONS"
	}
	return strings.ToUpper(m)
}

func newRestSource(_ string, p stepdef.Params) (stepdef.Processor, error) {
	path := strings.TrimPrefix(p["path"].(string), "/")
	if path == "" {
		return nil, fmt.Errorf("option path: empty path")
	}
	abs, cert, err := loadServerIdentity(p)
	if err != nil {
		return nil, err
	}
	return httpsSource{
		addr:     p["address"].(string),
		path:     "/" + path,
		oneWay:   p["exchangePattern"] == message.InOnly,
		method:   restMethod(p["method"].(string)),
		produces: p["produces"].(string),
		identity: abs,
		cert:     cert,
	}, nil
}

// restAction is the https action on host/path. produces is the Content-Type
// of the request when the message sets none; consumes is its Accept header.
type restAction struct {
	httpsAction
	produces, consumes string
}

func newRestAction(_ string, p stepdef.Params) (stepdef.Processor, error) {
	host := strings.TrimSuffix(p["host"].(string), "/")
	u, err := url.Parse(host + "/" + strings.TrimPrefix(p["path"].(string), "/"))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, fmt.Errorf("option host: want http(s)://host[:port], got %q", host)
	}
	client, err := httpsClient(p)
	if err != nil {
		return nil, err
	}
	return restAction{
		httpsAction: httpsAction{
			url:            u.String(),
			method:         restMethod(p["method"].(string)),
			client:         client,
			throwOnFailure: p["throwExceptionOnFailure"].(bool),
		},
		produces: p["produces"].(string),
		consumes: p["consumes"].(string),
	}, nil
}

func (a restAction) Process(ctx context.Context, m message.Message) (message.Message, error) {
	if ct, _ := m[message.ContentType].(string); ct == "" && a.produces != "" {
		m[message.ContentType] = a.produces
	}
	if a.consumes != "" {
		m["Accept"] = a.consumes
	}
	return a.httpsAction.Process(ctx, m)
}
