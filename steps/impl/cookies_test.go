package impl

import (
	"context"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"dif/message"
	stepdef "dif/steps/definition"
)

func TestCookieStore(t *testing.T) {
	s := &cookieStore{cookies: map[cookieKey]*http.Cookie{}}
	s.set(&http.Cookie{Name: "a", Value: "1", Domain: "example.com", Path: "/"})
	s.set(&http.Cookie{Name: "api", Value: "2", Domain: "Example.com", Path: "/api"})
	s.set(&http.Cookie{Name: "sec", Value: "3", Domain: "example.com", Path: "/", Secure: true})
	s.set(&http.Cookie{Name: "old", Value: "4", Domain: "example.com", Path: "/", Expires: time.Now().Add(-time.Minute)})
	s.set(&http.Cookie{Name: "other", Value: "5", Domain: "other.com", Path: "/"})

	sent := func(rawURL string) string {
		req, _ := http.NewRequest("GET", rawURL, nil)
		s.addTo(req)
		var names []string
		for _, c := range req.Cookies() {
			names = append(names, c.Name+"="+c.Value)
		}
		slices.Sort(names)
		return strings.Join(names, " ")
	}
	for u, want := range map[string]string{
		"https://example.com/":         "a=1 sec=3",
		"http://example.com/api/x":     "a=1 api=2",
		"https://www.example.com/api":  "a=1 api=2 sec=3",
		"https://notexample.com/":      "",
		"https://example.com.evil.io/": "",
	} {
		if got := sent(u); got != want {
			t.Errorf("%s: cookies %q, want %q", u, got, want)
		}
	}
	if _, ok := s.cookies[cookieKey{"old", "example.com", "/"}]; ok {
		t.Error("the expired cookie was kept")
	}

	s.remove("api", "EXAMPLE.com")
	if got := sent("https://example.com/api"); got != "a=1 sec=3" {
		t.Errorf("after remove: %q", got)
	}

	u, _ := url.Parse("https://shop.io/cart")
	s.keep(u, []*http.Cookie{{Name: "session", Value: "s1"}, {Name: "a", MaxAge: -1, Domain: ".example.com", Path: "/"}})
	if got := sent("https://shop.io/"); got != "session=s1" {
		t.Errorf("kept from a response: %q", got)
	}
	if got := sent("https://example.com/"); got != "sec=3" {
		t.Errorf("removed by a response: %q", got)
	}
}

func TestSetCookieAndRemoveCookie(t *testing.T) {
	got := make(chan string, 2)
	srv := testServer(t, func(w http.ResponseWriter, r *http.Request) {
		c, _ := r.Cookie(t.Name())
		if c == nil {
			got <- ""
			return
		}
		got <- c.Value
	})
	call := newHTTPSActionT(t, srv.URL+"/x", map[string]any{"httpMethod": "GET"})
	t.Cleanup(func() { cookies.remove(t.Name(), "127.0.0.1") })

	process(t, "setcookie", map[string]any{"name": t.Name(), "value": "v1", "domain": "127.0.0.1", "isSecure": true}, message.New(""))
	if _, err := call.Process(context.Background(), message.New("")); err != nil {
		t.Fatal(err)
	}
	if v := <-got; v != "v1" {
		t.Errorf("cookie = %q, want v1", v)
	}

	process(t, "removecookie", map[string]any{"name": t.Name(), "domain": "127.0.0.1"}, message.New(""))
	if _, err := call.Process(context.Background(), message.New("")); err != nil {
		t.Fatal(err)
	}
	if v := <-got; v != "" {
		t.Errorf("cookie = %q after removecookie, want none", v)
	}

	wantInvalid(t, stepdef.Action, "setcookie", map[string]any{"name": "a"}, "missing required option domain")
	wantInvalid(t, stepdef.Action, "setcookie", map[string]any{"name": "a b", "domain": "x.io"}, "invalid Cookie.Name")
	wantInvalid(t, stepdef.Action, "setcookie", map[string]any{"name": "a", "domain": ""}, "option domain: empty domain")
	wantInvalid(t, stepdef.Action, "removecookie", map[string]any{"name": ""}, "option name: empty cookie name")
}
