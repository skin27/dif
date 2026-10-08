package impl

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"dif/message"
)

func TestReplyCarriesTheHeadersOfTheMessage(t *testing.T) {
	m := message.New("answer")
	m["resultHeader1"] = "You Nique It"
	m["Amount"] = 12
	m["valid"] = true
	m["ratio"] = 1.5
	m[message.ContentType] = "text/x-answer"
	w := httptest.NewRecorder()
	writeReply(w, m, "")

	h := w.Header()
	for name, want := range map[string]string{"Resultheader1": "You Nique It", "Amount": "12", "Valid": "true", "Ratio": "1.5", "Content-Type": "text/x-answer"} {
		if got := h.Get(name); got != want {
			t.Errorf("header %s = %q, want %q", name, got, want)
		}
	}
	if h.Get(message.MessageID) != m[message.MessageID] {
		t.Errorf("identity header missing: %v", h)
	}
	if w.Body.String() != "answer" {
		t.Errorf("body = %q", w.Body.String())
	}
}

func TestReplyKeepsInternalsAndCredentialsOut(t *testing.T) {
	m := message.New("answer")
	for k, v := range map[string]any{
		"Authorization":       "Bearer secret",
		"authorization":       "Bearer secret",
		"Cookie":              "session=secret",
		"Proxy-Authorization": "Basic secret",
		"Content-Length":      "999",
		"Host":                "example.org",
		"Connection":          "close",
		"Transfer-Encoding":   "chunked",
		"Date":                "yesterday",
		"http.method":         "POST",
		"error.message":       "dial tcp 10.0.0.1: connection refused",
		"error.step":          "step-1",
		"metadata.secret":     "x",
		"tags":                []string{"a"},
		"data":                map[string]any{"a": 1},
		"raw":                 []byte("x"),
		"nothing":             nil,
		"bad name":            "x",
	} {
		m[k] = v
	}
	m["Set-Cookie"] = "set=by-the-flow" // a flow may set cookies on purpose
	w := httptest.NewRecorder()
	writeReply(w, m, "")

	h := w.Header()
	for _, name := range []string{"Authorization", "Cookie", "Proxy-Authorization", "Host", "Connection", "Transfer-Encoding", "Date",
		"Http.method", "Error.message", "Error.step", "Metadata.secret", "Tags", "Data", "Raw", "Nothing", "Bad name"} {
		if v, ok := h[name]; ok {
			t.Errorf("header %s = %q was returned", name, v)
		}
	}
	if got := h.Get("Content-Length"); got == "999" {
		t.Errorf("Content-Length of the request was returned")
	}
	if h.Get("Set-Cookie") != "set=by-the-flow" {
		t.Errorf("Set-Cookie of the flow = %q", h.Get("Set-Cookie"))
	}
	if strings.Contains(w.Body.String(), "secret") {
		t.Errorf("body = %q", w.Body.String())
	}
}

// A line break in a value never starts another header: it becomes a space.
func TestReplyHeaderValuesHaveOneLine(t *testing.T) {
	m := message.New("answer")
	m["Resultheader4"] = "\"'Hey Netherlands'\nHey Netherlands\""
	m["injected"] = "a\r\nSet-Cookie: x=y"
	w := httptest.NewRecorder()
	writeReply(w, m, "")

	h := w.Header()
	if got, want := h.Get("Resultheader4"), "\"'Hey Netherlands' Hey Netherlands\""; got != want {
		t.Errorf("Resultheader4 = %q, want %q", got, want)
	}
	if got, want := h.Get("Injected"), "a  Set-Cookie: x=y"; got != want {
		t.Errorf("Injected = %q, want %q", got, want)
	}
	if h.Get("Set-Cookie") != "" {
		t.Errorf("a value started the header Set-Cookie: %v", h)
	}
}

// A one-way source replies with the request: its credentials must not come back.
func TestOneWayReplyDoesNotEchoCredentials(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("body"))
	r.Header.Set("Authorization", "Bearer secret")
	r.Header.Set("Cookie", "session=secret")
	r.Header.Set("X-Custom", "kept")
	w := httptest.NewRecorder()
	httpsSource{oneWay: true}.handler(func(m message.Message, reply func(message.Message, error)) error { return nil })(w, r)

	h := w.Header()
	if h.Get("Authorization") != "" || h.Get("Cookie") != "" {
		t.Errorf("credentials returned: %v", h)
	}
	if h.Get("X-Custom") != "kept" {
		t.Errorf("X-Custom = %q, want it back", h.Get("X-Custom"))
	}
}
