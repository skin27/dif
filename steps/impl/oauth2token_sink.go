package impl

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"dif/message"
	stepdef "dif/steps/definition"
)

// oauth2TokenSink keeps an OAuth2 access token fresh in tenant variables.
// Every message that reaches it renews the token when it expires within
// expiryDelay; a repeater source in front of it makes a token service. The
// message itself is not used.
//
// The token goes to each variable named in tokenName and to the same name
// with the suffix _Temp, which the googledrive examples read as
// @{GoogleDriveAccessToken_Temp}.
type oauth2TokenSink struct {
	tenant   string
	names    []string // variables to set
	expiry   time.Duration
	tokenURL string
	form     url.Values // the request body, without credentials
	clientID string
	secret   string
	basic    bool // client credentials by HTTP Basic, else in the body
	client   *http.Client

	mu           sync.Mutex // serializes renewals
	refreshToken string     // replaced when the server issues a new one
	token        string
	validUntil   time.Time
}

func newOAuth2TokenSink(_ string, p stepdef.Params) (stepdef.Processor, error) {
	s := &oauth2TokenSink{
		tenant: p["tenantDbName"].(string),
		expiry: time.Duration(p["expiryDelay"].(int)) * time.Second,
		basic:  p["clientAuthentication"] == "basic",
	}
	var tokens []string // the names the settings are looked up by
	for _, name := range strings.Split(p["tokenName"].(string), ",") {
		if name = strings.TrimSpace(name); name != "" {
			tokens = append(tokens, name)
			s.names = append(s.names, name, name+"_Temp")
		}
	}
	if len(tokens) == 0 {
		return nil, fmt.Errorf("option tokenName: no variable name")
	}

	var err error
	setting := func(option, name string) (string, error) { return oauth2Setting(p, tokens, option, name) }
	if s.tokenURL, err = setting("tokenUrl", "TOKEN_URL"); err != nil {
		return nil, err
	}
	if s.tokenURL == "" {
		return nil, fmt.Errorf("option tokenUrl: required; the flow must name the OAuth2 token endpoint, or set %s", oauth2EnvHint(tokens, "TOKEN_URL"))
	}
	u, err := url.Parse(s.tokenURL)
	if err != nil || u.Host == "" || (u.Scheme != "https" && !(u.Scheme == "http" && isLoopback(u.Hostname()))) {
		return nil, fmt.Errorf("option tokenUrl: want https://host/path (http only for localhost), got %q", s.tokenURL)
	}
	if s.clientID, err = setting("clientId", "CLIENT_ID"); err != nil {
		return nil, err
	}
	if s.clientID == "" {
		return nil, fmt.Errorf("option clientId: required; or set %s", oauth2EnvHint(tokens, "CLIENT_ID"))
	}
	if s.secret, err = setting("clientSecret", "CLIENT_SECRET"); err != nil {
		return nil, err
	}
	if s.refreshToken, err = setting("refreshToken", "REFRESH_TOKEN"); err != nil {
		return nil, err
	}
	scope, err := setting("scope", "SCOPE")
	if err != nil {
		return nil, err
	}

	grant, _ := p["grantType"].(string)
	if grant == "" { // a refresh token means that grant
		if grant = "client_credentials"; s.refreshToken != "" {
			grant = "refresh_token"
		}
	}
	s.form = url.Values{"grant_type": {grant}}
	if scope != "" {
		s.form.Set("scope", scope)
	}
	switch {
	case grant == "client_credentials" && s.secret == "":
		return nil, fmt.Errorf("option clientSecret: required for client_credentials; or set %s", oauth2EnvHint(tokens, "CLIENT_SECRET"))
	case grant == "refresh_token" && s.refreshToken == "":
		return nil, fmt.Errorf("option refreshToken: required for refresh_token; or set %s", oauth2EnvHint(tokens, "REFRESH_TOKEN"))
	}
	if s.client, err = httpsClient(p); err != nil {
		return nil, err
	}
	return s, nil
}

// The settings of a token that the flow does not give are the tenant's OAuth
// configuration of the Java platform; DIF reads them from the environment,
// as DIF_OAUTH2_<TOKEN>_<SETTING>, where TOKEN is a name in tokenName in
// capitals and SETTING is TOKEN_URL, CLIENT_ID, CLIENT_SECRET, REFRESH_TOKEN
// or SCOPE. Each variable has a _FILE companion (see environmentSecret). The
// names are tried in order, and DIF_OAUTH2_<SETTING> is for all tokens.

// oauth2Setting returns a setting of the token: the option if the flow gives
// it (an empty secret counts, as an option without a default; an empty
// tokenUrl, clientId or scope, which default to "", does not), else the
// environment's, else "".
func oauth2Setting(p stepdef.Params, tokens []string, option, setting string) (string, error) {
	if v, ok := p[option].(string); ok && (v != "" || option == "clientSecret" || option == "refreshToken") {
		return v, nil
	}
	for _, t := range tokens {
		if v, ok, err := environmentSecret(oauth2Env(t, setting)); err != nil || ok {
			return v, err
		}
	}
	v, _, err := environmentSecret("DIF_OAUTH2_" + setting)
	return v, err
}

// oauth2Env is the environment variable of a setting of a token.
func oauth2Env(token, setting string) string {
	name := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z':
			return r - 'a' + 'A'
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		}
		return '_'
	}, token)
	return "DIF_OAUTH2_" + name + "_" + setting
}

// oauth2EnvHint names the variables that give a setting, for an error.
func oauth2EnvHint(tokens []string, setting string) string {
	return oauth2Env(tokens[0], setting) + " or DIF_OAUTH2_" + setting
}

// optionOrEnv returns the string option, else the environment secret.
func optionOrEnv(p stepdef.Params, option, env string) (string, error) {
	if v, ok := p[option].(string); ok {
		return v, nil
	}
	v, _, err := environmentSecret(env)
	return v, err
}

// isLoopback reports whether host is the local machine, which may be called
// without TLS.
func isLoopback(host string) bool {
	return host == "localhost" || host == "127.0.0.1" || host == "::1"
}

func (s *oauth2TokenSink) Consume(ctx context.Context, _ message.Message) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.token == "" || !time.Now().Add(s.expiry).Before(s.validUntil) {
		if err := s.renew(ctx); err != nil {
			return fmt.Errorf("oauth2token: %w", err)
		}
	}
	for _, name := range s.names {
		tenantVariables.set(s.tenant, name, s.token)
	}
	return nil
}

// renew asks the token endpoint for a new access token. A response without
// expires_in is used once, not kept.
func (s *oauth2TokenSink) renew(ctx context.Context) error {
	form := url.Values{}
	for k, v := range s.form {
		form[k] = v
	}
	if form.Get("grant_type") == "refresh_token" {
		form.Set("refresh_token", s.refreshToken)
	}
	if !s.basic {
		form.Set("client_id", s.clientID)
		if s.secret != "" {
			form.Set("client_secret", s.secret)
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	if s.basic {
		// RFC 6749 section 2.3.1: the id and secret are form-urlencoded first.
		req.SetBasicAuth(url.QueryEscape(s.clientID), url.QueryEscape(s.secret))
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("token request: %w", err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("token request: reading response: %w", err)
	}
	var tr struct {
		AccessToken  string      `json:"access_token"`
		ExpiresIn    json.Number `json:"expires_in"`
		RefreshToken string      `json:"refresh_token"`
		Error        string      `json:"error"`
		Description  string      `json:"error_description"`
	}
	jsonErr := json.Unmarshal(data, &tr)
	if resp.StatusCode != http.StatusOK {
		// Only the error fields are reported, never the whole response.
		if tr.Error != "" {
			return fmt.Errorf("token endpoint: %s: %s %s", resp.Status, tr.Error, tr.Description)
		}
		return fmt.Errorf("token endpoint: %s", resp.Status)
	}
	if jsonErr != nil || tr.AccessToken == "" {
		return fmt.Errorf("token endpoint: no access_token in the response")
	}

	s.token = tr.AccessToken
	s.validUntil = time.Now()
	if secs, err := tr.ExpiresIn.Int64(); err == nil && secs > 0 {
		s.validUntil = s.validUntil.Add(time.Duration(secs) * time.Second)
	}
	if tr.RefreshToken != "" && s.refreshToken != "" {
		s.refreshToken = tr.RefreshToken
	}
	return nil
}
