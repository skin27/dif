package impl

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"dif/message"
	stepdef "dif/steps/definition"
)

// tokenServer is a token endpoint that records its requests and answers with
// the next token; fail makes it answer 400 invalid_client.
type tokenServer struct {
	*httptest.Server
	mu        sync.Mutex
	forms     []url.Values
	basicAuth []string // "id:secret" of each request, "" for none
	expiresIn string   // expires_in as JSON, "" to leave it out
	refresh   string   // refresh_token to issue, "" for none
	fail      bool
}

func newTokenServer(t *testing.T) *tokenServer {
	t.Helper()
	ts := &tokenServer{expiresIn: "3600"}
	ts.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ts.mu.Lock()
		defer ts.mu.Unlock()
		r.ParseForm()
		ts.forms = append(ts.forms, r.PostForm)
		id, secret, ok := r.BasicAuth()
		if ok {
			ts.basicAuth = append(ts.basicAuth, id+":"+secret)
		} else {
			ts.basicAuth = append(ts.basicAuth, "")
		}
		w.Header().Set("Content-Type", "application/json")
		if ts.fail || r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/x-www-form-urlencoded" {
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, `{"error":"invalid_client","error_description":"bad credentials"}`)
			return
		}
		fmt.Fprintf(w, `{"access_token":"token-%d","token_type":"Bearer"`, len(ts.forms))
		if ts.expiresIn != "" {
			fmt.Fprintf(w, `,"expires_in":%s`, ts.expiresIn)
		}
		if ts.refresh != "" {
			fmt.Fprintf(w, `,"refresh_token":%q`, ts.refresh)
		}
		fmt.Fprint(w, "}")
	}))
	t.Cleanup(ts.Close)
	return ts
}

func (ts *tokenServer) requests() int {
	ts.mu.Lock()
	defer ts.mu.Unlock()
	return len(ts.forms)
}

// tokenSink creates the oauth2token sink for tenant t.Name() and token server.
func tokenSink(t *testing.T, ts *tokenServer, opts map[string]any) stepdef.SinkProcessor {
	t.Helper()
	all := map[string]any{
		"tokenName": "access", "tenantDbName": t.Name(), "tokenUrl": ts.URL,
		"clientId": "me", "clientSecret": "s3cret",
	}
	for k, v := range opts {
		if v == nil {
			delete(all, k)
		} else {
			all[k] = v
		}
	}
	return mustProcessor(t, stepdef.Sink, "oauth2token:id", all).(stepdef.SinkProcessor)
}

func variable(t *testing.T, name string) string {
	t.Helper()
	v, _ := tenantVariables.get(t.Name(), name)
	return v
}

func TestOAuth2TokenClientCredentials(t *testing.T) {
	ts := newTokenServer(t)
	sink := tokenSink(t, ts, map[string]any{"tokenName": "a, b,", "scope": "read write"})
	if err := sink.Consume(context.Background(), message.New("")); err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"a", "a_Temp", "b", "b_Temp"} {
		if v := variable(t, name); v != "token-1" {
			t.Errorf("variable %s = %q, want token-1", name, v)
		}
	}
	form := ts.forms[0]
	if form.Get("grant_type") != "client_credentials" || form.Get("scope") != "read write" {
		t.Errorf("form = %v, want client_credentials with the scope", form)
	}
	if form.Has("client_id") || form.Has("client_secret") || form.Has("refresh_token") {
		t.Errorf("form = %v: the credentials belong in the Authorization header", form)
	}
	if ts.basicAuth[0] != "me:s3cret" {
		t.Errorf("basic auth = %q, want me:s3cret", ts.basicAuth[0])
	}
}

func TestOAuth2TokenKeepsTheTokenUntilItExpires(t *testing.T) {
	ts := newTokenServer(t)
	ts.expiresIn = "100"
	sink := tokenSink(t, ts, map[string]any{"expiryDelay": 60})
	consume := func() {
		t.Helper()
		if err := sink.Consume(context.Background(), message.New("")); err != nil {
			t.Fatal(err)
		}
	}

	consume()
	consume()
	if n := ts.requests(); n != 1 {
		t.Fatalf("%d token requests for two messages, want 1: the token is valid for 100s and renewed 60s before", n)
	}

	// A removed variable is set again from the kept token.
	tenantVariables.remove(t.Name(), "access")
	consume()
	if v := variable(t, "access"); v != "token-1" || ts.requests() != 1 {
		t.Errorf("access = %q after %d requests, want token-1 after 1", v, ts.requests())
	}

	// With the renewal 100s before the end, every message renews.
	renewing := tokenSink(t, ts, map[string]any{"expiryDelay": 100, "tokenName": "other"})
	renewing.Consume(context.Background(), message.New(""))
	renewing.Consume(context.Background(), message.New(""))
	if n := ts.requests(); n != 3 {
		t.Errorf("%d token requests, want 3", n)
	}
	if v := variable(t, "other"); v != "token-3" {
		t.Errorf("other = %q, want token-3", v)
	}
}

func TestOAuth2TokenWithoutExpiryIsNotKept(t *testing.T) {
	ts := newTokenServer(t)
	ts.expiresIn = ""
	sink := tokenSink(t, ts, nil)
	sink.Consume(context.Background(), message.New(""))
	sink.Consume(context.Background(), message.New(""))
	if n := ts.requests(); n != 2 {
		t.Errorf("%d token requests, want 2: a token without expires_in is used once", n)
	}
}

func TestOAuth2TokenExpiresInAsString(t *testing.T) {
	ts := newTokenServer(t)
	ts.expiresIn = `"3600"`
	sink := tokenSink(t, ts, nil)
	sink.Consume(context.Background(), message.New(""))
	sink.Consume(context.Background(), message.New(""))
	if n := ts.requests(); n != 1 {
		t.Errorf("%d token requests, want 1: expires_in may be a string", n)
	}
}

func TestOAuth2TokenCredentialsInTheBody(t *testing.T) {
	ts := newTokenServer(t)
	sink := tokenSink(t, ts, map[string]any{"clientAuthentication": "post"})
	if err := sink.Consume(context.Background(), message.New("")); err != nil {
		t.Fatal(err)
	}
	form := ts.forms[0]
	if form.Get("client_id") != "me" || form.Get("client_secret") != "s3cret" || ts.basicAuth[0] != "" {
		t.Errorf("form = %v, basic auth = %q, want the credentials in the body only", form, ts.basicAuth[0])
	}
}

func TestOAuth2TokenRefreshToken(t *testing.T) {
	ts := newTokenServer(t)
	ts.expiresIn = "1"
	sink := tokenSink(t, ts, map[string]any{
		"grantType": "refresh_token", "refreshToken": "r-0", "clientSecret": "", "clientAuthentication": "post", "expiryDelay": 60,
	})
	ts.refresh = "r-1"
	sink.Consume(context.Background(), message.New(""))
	ts.refresh = ""
	sink.Consume(context.Background(), message.New(""))

	if got := ts.forms[0].Get("refresh_token"); got != "r-0" || ts.forms[0].Get("grant_type") != "refresh_token" {
		t.Errorf("first request = %v, want the refresh_token grant with r-0", ts.forms[0])
	}
	if got := ts.forms[1].Get("refresh_token"); got != "r-1" {
		t.Errorf("second request used refresh token %q, want the one the server issued, r-1", got)
	}
}

func TestOAuth2TokenFailure(t *testing.T) {
	ts := newTokenServer(t)
	ts.fail = true
	sink := tokenSink(t, ts, nil)
	err := sink.Consume(context.Background(), message.New(""))
	if err == nil || !strings.Contains(err.Error(), "invalid_client") || !strings.Contains(err.Error(), "bad credentials") {
		t.Fatalf("err = %v, want the server's error", err)
	}
	if strings.Contains(err.Error(), "s3cret") {
		t.Errorf("err = %v reveals the secret", err)
	}
	if v := variable(t, "access"); v != "" {
		t.Errorf("access = %q after a failure, want it unset", v)
	}

	// The next message tries again and succeeds.
	ts.fail = false
	if err := sink.Consume(context.Background(), message.New("")); err != nil {
		t.Fatal(err)
	}
	if v := variable(t, "access"); v != "token-2" {
		t.Errorf("access = %q, want token-2", v)
	}
}

func TestOAuth2TokenBadResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "<html>") }))
	defer srv.Close()
	sink := tokenSink(t, &tokenServer{Server: srv}, nil)
	err := sink.Consume(context.Background(), message.New(""))
	if err == nil || !strings.Contains(err.Error(), "no access_token") {
		t.Errorf("err = %v, want no access_token", err)
	}
}

func TestOAuth2TokenSecretsFromTheEnvironment(t *testing.T) {
	ts := newTokenServer(t)
	t.Setenv("DIF_OAUTH2_CLIENT_SECRET", "from-env")
	sink := tokenSink(t, ts, map[string]any{"clientSecret": nil})
	if err := sink.Consume(context.Background(), message.New("")); err != nil {
		t.Fatal(err)
	}
	if ts.basicAuth[0] != "me:from-env" {
		t.Errorf("basic auth = %q, want me:from-env", ts.basicAuth[0])
	}
}

func TestOAuth2TokenInvalidOptions(t *testing.T) {
	base := func(extra map[string]any) map[string]any {
		o := map[string]any{"tokenName": "t", "tokenUrl": "https://auth.example.com/token", "clientId": "me", "clientSecret": "s"}
		for k, v := range extra {
			if v == nil {
				delete(o, k)
			} else {
				o[k] = v
			}
		}
		return o
	}
	sink := func(opts map[string]any, want string) {
		t.Helper()
		wantInvalid(t, stepdef.Sink, "oauth2token:id", opts, want)
	}

	if _, err := newProcessor(stepdef.Sink, "oauth2token:id", base(nil)); err != nil {
		t.Fatalf("valid options: %v", err)
	}
	if _, err := newProcessor(stepdef.Sink, "oauth2token:id", base(map[string]any{"tokenUrl": "http://127.0.0.1:8080/token"})); err != nil {
		t.Fatalf("http to localhost: %v", err)
	}
	sink(base(map[string]any{"tokenName": nil}), "missing required option tokenName")
	sink(base(map[string]any{"tokenName": " , "}), "no variable name")
	sink(base(map[string]any{"tokenUrl": nil}), "option tokenUrl: required")
	sink(base(map[string]any{"tokenUrl": "http://auth.example.com/token"}), "http only for localhost")
	sink(base(map[string]any{"tokenUrl": "ftp://x/token"}), "option tokenUrl")
	sink(base(map[string]any{"clientId": nil}), "option clientId: required")
	sink(base(map[string]any{"clientSecret": nil}), "option clientSecret: required")
	sink(base(map[string]any{"grantType": "refresh_token"}), "option refreshToken: required")
	sink(base(map[string]any{"grantType": "password"}), "grantType")
	sink(base(map[string]any{"clientAuthentication": "digest"}), "clientAuthentication")
	sink(base(map[string]any{"unknown": "x"}), "unknown option")
}

// The settings of a token come from the environment when the flow does not
// give them, as in the fixtures, which name only the token.
func TestOAuth2TokenSettingsFromTheEnvironment(t *testing.T) {
	ts := newTokenServer(t)
	t.Setenv("DIF_OAUTH2_OAUTHTOKEN_DRIVE_TOKEN_URL", ts.URL)
	t.Setenv("DIF_OAUTH2_OAUTHTOKEN_DRIVE_CLIENT_ID", "drive-client")
	t.Setenv("DIF_OAUTH2_OAUTHTOKEN_DRIVE_CLIENT_SECRET", "drive-secret")
	t.Setenv("DIF_OAUTH2_OAUTHTOKEN_DRIVE_REFRESH_TOKEN", "drive-refresh")
	t.Setenv("DIF_OAUTH2_OAUTHTOKEN_DRIVE_SCOPE", "drive.readonly")
	// Another token's, and the settings for all tokens, do not apply to it.
	t.Setenv("DIF_OAUTH2_OTHER_CLIENT_ID", "other")
	t.Setenv("DIF_OAUTH2_CLIENT_ID", "all")

	// The names are tried in turn: the first has no settings, the second has.
	sink := tokenSink(t, ts, map[string]any{
		"tokenName": "static.token, OauthToken-Drive", "tokenUrl": nil, "clientId": nil, "clientSecret": nil,
	})
	if err := sink.Consume(context.Background(), message.New("")); err != nil {
		t.Fatal(err)
	}
	form := ts.forms[0]
	if form.Get("grant_type") != "refresh_token" || form.Get("refresh_token") != "drive-refresh" || form.Get("scope") != "drive.readonly" {
		t.Errorf("form = %v, want the refresh_token grant, as a refresh token is set", form)
	}
	if ts.basicAuth[0] != "drive-client:drive-secret" {
		t.Errorf("basic auth = %q, want drive-client:drive-secret", ts.basicAuth[0])
	}
	for _, name := range []string{"static.token", "static.token_Temp", "OauthToken-Drive", "OauthToken-Drive_Temp"} {
		if v := variable(t, name); v != "token-1" {
			t.Errorf("variable %s = %q, want token-1", name, v)
		}
	}
}

func TestOAuth2TokenOptionsBeatTheEnvironment(t *testing.T) {
	ts := newTokenServer(t)
	t.Setenv("DIF_OAUTH2_ACCESS_TOKEN_URL", "https://unused.example.com/token")
	t.Setenv("DIF_OAUTH2_ACCESS_CLIENT_ID", "from-env")
	t.Setenv("DIF_OAUTH2_ACCESS_CLIENT_SECRET", "from-env")
	t.Setenv("DIF_OAUTH2_ACCESS_SCOPE", "from-env")
	sink := tokenSink(t, ts, map[string]any{"scope": "from-option"})
	if err := sink.Consume(context.Background(), message.New("")); err != nil {
		t.Fatal(err)
	}
	if ts.basicAuth[0] != "me:s3cret" || ts.forms[0].Get("scope") != "from-option" {
		t.Errorf("basic auth = %q, form = %v, want the options", ts.basicAuth[0], ts.forms[0])
	}
}

func TestOAuth2TokenSettingsForAllTokensAndFiles(t *testing.T) {
	ts := newTokenServer(t)
	dir := t.TempDir()
	secretFile := filepath.Join(dir, "secret")
	if err := os.WriteFile(secretFile, []byte("file-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DIF_OAUTH2_TOKEN_URL", ts.URL)
	t.Setenv("DIF_OAUTH2_CLIENT_ID", "all-client")
	t.Setenv("DIF_OAUTH2_ACCESS_CLIENT_SECRET_FILE", secretFile)
	sink := tokenSink(t, ts, map[string]any{"tokenUrl": nil, "clientId": nil, "clientSecret": nil})
	if err := sink.Consume(context.Background(), message.New("")); err != nil {
		t.Fatal(err)
	}
	if ts.basicAuth[0] != "all-client:file-secret" || ts.forms[0].Get("grant_type") != "client_credentials" {
		t.Errorf("basic auth = %q, form = %v", ts.basicAuth[0], ts.forms[0])
	}
}

func TestOAuth2TokenMissingSettingsNameTheVariables(t *testing.T) {
	opts := func(extra map[string]any) map[string]any {
		o := map[string]any{"tokenName": "Gmail.Noreply,other"}
		for k, v := range extra {
			o[k] = v
		}
		return o
	}
	wantInvalid(t, stepdef.Sink, "oauth2token:id", opts(nil), "set DIF_OAUTH2_GMAIL_NOREPLY_TOKEN_URL or DIF_OAUTH2_TOKEN_URL")
	wantInvalid(t, stepdef.Sink, "oauth2token:id", opts(map[string]any{"tokenUrl": "https://auth.example.com/token"}), "set DIF_OAUTH2_GMAIL_NOREPLY_CLIENT_ID or DIF_OAUTH2_CLIENT_ID")
	wantInvalid(t, stepdef.Sink, "oauth2token:id", opts(map[string]any{"tokenUrl": "https://auth.example.com/token", "clientId": "me"}), "set DIF_OAUTH2_GMAIL_NOREPLY_CLIENT_SECRET or DIF_OAUTH2_CLIENT_SECRET")
	wantInvalid(t, stepdef.Sink, "oauth2token:id", opts(map[string]any{"tokenUrl": "https://auth.example.com/token", "clientId": "me", "grantType": "refresh_token"}), "set DIF_OAUTH2_GMAIL_NOREPLY_REFRESH_TOKEN or DIF_OAUTH2_REFRESH_TOKEN")
}

// The flows of the fixtures name only the token and the tenant.
func TestOAuth2TokenExampleOptionsNeedAnEndpoint(t *testing.T) {
	wantInvalid(t, stepdef.Sink, "oauth2token:a75f1bad", map[string]any{
		"expiryDelay": "60", "tokenName": "GoogleDriveAccessToken", "tenantDbName": "_new2",
	}, "option tokenUrl: required")
}
