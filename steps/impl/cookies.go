package impl

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"dif/message"
	stepdef "dif/steps/definition"
)

// cookieStore holds the cookies the https and rest actions send, as Camel's
// cookie store does: setcookie adds one, removecookie removes them by name
// and domain, and the cookies a server sets are kept as well. It is shared by
// all flows of the process and kept in memory.
type cookieStore struct {
	mu      sync.Mutex
	cookies map[cookieKey]*http.Cookie
}

type cookieKey struct{ name, domain, path string }

var cookies = &cookieStore{cookies: map[cookieKey]*http.Cookie{}}

// set adds c, or replaces the cookie with its name, domain and path.
func (s *cookieStore) set(c *http.Cookie) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cookies[cookieKey{c.Name, strings.ToLower(c.Domain), c.Path}] = c
}

// remove removes the cookies with name and domain, whatever their path.
func (s *cookieStore) remove(name, domain string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	domain = strings.ToLower(domain)
	for k := range s.cookies {
		if k.name == name && k.domain == domain {
			delete(s.cookies, k)
		}
	}
}

// addTo adds the cookies for req's URL to req: those of its host or a domain
// it is in, on a path the request's path starts with, secure ones only over
// https, and not expired.
func (s *cookieStore) addTo(req *http.Request) {
	host, path := strings.ToLower(req.URL.Hostname()), req.URL.Path
	if path == "" {
		path = "/"
	}
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	for k, c := range s.cookies {
		switch {
		case !c.Expires.IsZero() && c.Expires.Before(now):
			delete(s.cookies, k)
		case host != k.domain && !strings.HasSuffix(host, "."+k.domain),
			!strings.HasPrefix(path, k.path),
			c.Secure && req.URL.Scheme != "https":
		default:
			req.AddCookie(&http.Cookie{Name: c.Name, Value: c.Value})
		}
	}
}

// keep stores the cookies a response to u set; one with Max-Age 0 or less
// removes the cookie.
func (s *cookieStore) keep(u *url.URL, set []*http.Cookie) {
	for _, c := range set {
		if c.Domain == "" {
			c.Domain = u.Hostname()
		}
		c.Domain = strings.TrimPrefix(c.Domain, ".")
		if c.Path == "" {
			c.Path = "/"
		}
		if c.MaxAge < 0 {
			s.mu.Lock()
			delete(s.cookies, cookieKey{c.Name, strings.ToLower(c.Domain), c.Path})
			s.mu.Unlock()
			continue
		}
		if c.MaxAge > 0 {
			c.Expires = time.Now().Add(time.Duration(c.MaxAge) * time.Second)
		}
		s.set(c)
	}
}

// setCookieAction adds a cookie to the cookie store.
type setCookieAction struct{ cookie http.Cookie }

func newSetCookieAction(_ string, p stepdef.Params) (stepdef.Processor, error) {
	c := http.Cookie{
		Name:   p["name"].(string),
		Value:  p["value"].(string),
		Domain: strings.TrimPrefix(p["domain"].(string), "."),
		Path:   p["path"].(string),
		Secure: p["isSecure"].(bool),
	}
	if c.Path == "" {
		c.Path = "/"
	}
	if err := c.Valid(); err != nil {
		return nil, err
	}
	if c.Domain == "" {
		return nil, fmt.Errorf("option domain: empty domain")
	}
	return setCookieAction{c}, nil
}

func (a setCookieAction) Process(_ context.Context, m message.Message) (message.Message, error) {
	c := a.cookie
	cookies.set(&c)
	return m, nil
}

// removeCookieAction removes the cookies with a name and domain from the
// cookie store.
type removeCookieAction struct{ name, domain string }

func newRemoveCookieAction(_ string, p stepdef.Params) (stepdef.Processor, error) {
	a := removeCookieAction{p["name"].(string), strings.TrimPrefix(p["domain"].(string), ".")}
	if a.name == "" {
		return nil, fmt.Errorf("option name: empty cookie name")
	}
	return a, nil
}

func (a removeCookieAction) Process(_ context.Context, m message.Message) (message.Message, error) {
	cookies.remove(a.name, a.domain)
	return m, nil
}
