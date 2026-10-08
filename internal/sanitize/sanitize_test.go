package sanitize

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dif/keystore"
)

func write(t *testing.T, dir, name, content string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestCleanTextShapes(t *testing.T) {
	st := &state{table: map[string]string{}, counter: map[string]int{}}
	basic := base64.StdEncoding.EncodeToString([]byte("alice:s3cret-pw"))
	for _, tc := range []struct {
		name, in string
		keep     []string // must remain in the output
		gone     []string // must not remain
	}{
		{"ENC", "x ENC(AAAA|BBBB|CCCC) y", []string{"x " + DummyENC + " y"}, []string{"AAAA"}},
		{"Basic", "Authorization: Basic " + basic, []string{"Authorization: Basic "}, []string{basic}},
		{"Bearer", "Bearer abcdef0123456789.abcdef", []string{"Bearer dummy-token-"}, []string{"abcdef0123456789"}},
		{"userinfo", "sftp://bob:hunter22@host:22/dir", []string{"sftp://bob:dummy-password-", "@host:22/dir"}, []string{"hunter22"}},
		{"query", "https://h/x?apikey=0123456789abcdef&b=1", []string{"?apikey=dummy-token-", "&b=1"}, []string{"0123456789abcdef"}},
		{"hex key", "https://h/x/authenticate?key=0123456789abcdef0123456789abcdef", []string{"?key=dummy-apikey-"}, []string{"0123456789abcdef0123"}},
		{"placeholders", "https://h/x?apikey=${header.k}&token={{t}}&password=@{p}", []string{"${header.k}", "{{t}}", "@{p}"}, nil},
		{"prose", "Basic authentication and Bearer authentication", []string{"Basic authentication and Bearer authentication"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out, rules := st.cleanText(tc.in)
			for _, k := range tc.keep {
				if !strings.Contains(out, k) {
					t.Errorf("output %q lacks %q", out, k)
				}
			}
			for _, g := range tc.gone {
				if strings.Contains(out, g) {
					t.Errorf("output %q still has %q", out, g)
				}
			}
			if tc.gone == nil && len(rules) != 0 {
				t.Errorf("nothing should be replaced, got %v", rules)
			}
			if again, rules := st.cleanText(out); again != out || len(rules) != 0 {
				t.Errorf("not idempotent: %q -> %q (%v)", out, again, rules)
			}
		})
	}
}

func TestRunRewritesFixtures(t *testing.T) {
	dir := t.TempDir()
	flow := "\ufeff{\n\t\"dil\": {\n\t\t\"core\": {\n\t\t\t\"connections\": {\"connection\": {\"type\": \"basic\", \"keys\": {\"username\": \"u\", \"password\": \"hunter22\"}}},\n" +
		"\t\t\t\"messages\": {\"message\": {\"headers\": {\"header\": [{\"name\": \"apikey_film\", \"value\": \"abc123def456\", \"language\": \"constant\"}, {\"name\": \"author\", \"value\": \"Tolkien\"}]}}}\n\t\t},\n" +
		"\t\t\"steps\": [{\"uri\": \"sftp:h/d\", \"options\": {\"password\": \"ENC(AA|BB|CC)\", \"accessToken\": \"@{OauthToken}\", \"fileName\": \"a.txt\"}}, {\"uri\": \"sql\", \"options\": {\"password\": \"hunter22\"}}]\n\t}\n}\n"
	postman := "$kind: http-request\nurl: https://h/x\nheaders:\n  - key: Authorization\n    value: \"{{HC_token}}\"\n  - key: password\n    value: hunter22\nbody:\n  content: >-\n    password: not-a-header\n"
	env := "name: next\nvalues:\n  - key: HC_token\n    value: secretvalue12345\r\n  - key: HOST\n    value: https\n"
	fp := write(t, dir, "flows/a.json", flow)
	pp := write(t, dir, "postman/a.request.yaml", postman)
	ep := write(t, dir, "postman/env.yaml", env)

	found, err := Run([]string{dir}, false)
	if err != nil {
		t.Fatal(err)
	}
	if read(t, fp) != flow {
		t.Fatal("check mode must not write")
	}
	if len(found) == 0 {
		t.Fatal("found nothing")
	}
	for _, f := range found {
		if strings.Contains(f.String(), "hunter22") || strings.Contains(f.String(), "secretvalue") {
			t.Fatalf("a finding shows the credential: %s", f)
		}
	}

	if _, err := Run([]string{dir}, true); err != nil {
		t.Fatal(err)
	}
	gotFlow, gotPostman, gotEnv := read(t, fp), read(t, pp), read(t, ep)
	for _, secret := range []string{"hunter22", "abc123def456", "secretvalue12345", "ENC(AA|BB|CC)"} {
		for name, text := range map[string]string{"flow": gotFlow, "postman": gotPostman, "env": gotEnv} {
			if strings.Contains(text, secret) {
				t.Errorf("%s still has %q", name, secret)
			}
		}
	}
	// What is not a credential stays, byte for byte: BOM, tabs, other fields.
	for _, keep := range []string{"\ufeff{\n\t\"dil\"", "@{OauthToken}", "\"fileName\": \"a.txt\"", "Tolkien", "\"language\": \"constant\""} {
		if !strings.Contains(gotFlow, keep) {
			t.Errorf("flow lost %q:\n%s", keep, gotFlow)
		}
	}
	for _, keep := range []string{"{{HC_token}}", "password: not-a-header", "key: HOST\n    value: https", "\r\n"} {
		if !strings.Contains(gotPostman+gotEnv, keep) {
			t.Errorf("postman files lost %q:\n%s%s", keep, gotPostman, gotEnv)
		}
	}
	// The same original is the same dummy everywhere, so a flow and the request calling it still match.
	dummy := "dummy-password-1"
	if strings.Count(gotFlow, dummy) != 2 || !strings.Contains(gotPostman, dummy) {
		t.Errorf("hunter22 should be %s in the flow (twice) and in the request:\n%s\n%s", dummy, gotFlow, gotPostman)
	}
	if !strings.Contains(gotFlow, DummyENC) {
		t.Error("ENC value is not the dummy ENC")
	}

	again, err := Run([]string{dir}, true)
	if err != nil || len(again) != 0 {
		t.Fatalf("second run should find nothing: %v %v", again, err)
	}
}

func TestRunUnfoldsFoldedURLs(t *testing.T) {
	dir := t.TempDir()
	p := write(t, dir, "a.request.yaml", "url: \"https://h/x?t\\\n  oken=Hump2007\"\nqueryParams:\n  token: \"Hump2007\"\nother: \"keep \\\n  this\"\n")
	if _, err := Run([]string{dir}, true); err != nil {
		t.Fatal(err)
	}
	got := read(t, p)
	if strings.Contains(got, "Hump2007") {
		t.Fatalf("the credential is still there:\n%s", got)
	}
	if !strings.Contains(got, "url: \"https://h/x?token=dummy-token-1\"") || !strings.Contains(got, "token: \"dummy-token-1\"") {
		t.Fatalf("URL and query parameter must carry the same dummy:\n%s", got)
	}
	if !strings.Contains(got, "other: \"keep \\\n  this\"") {
		t.Fatalf("a folded line without a credential must stay folded:\n%s", got)
	}
}

func TestDummyENCHasTheJavaFormat(t *testing.T) {
	parts := strings.Split(strings.TrimSuffix(strings.TrimPrefix(DummyENC, "ENC("), ")"), "|")
	if len(parts) != 3 {
		t.Fatalf("want salt|iv|cipher: %s", DummyENC)
	}
	var sizes [3]int
	for i, p := range parts {
		b, err := base64.StdEncoding.DecodeString(p)
		if err != nil {
			t.Fatal(err)
		}
		sizes[i] = len(b)
	}
	if sizes[0] != 16 || sizes[1] != 16 || sizes[2] == 0 || sizes[2]%16 != 0 {
		t.Fatalf("sizes of salt, iv, cipher = %v", sizes)
	}
}

func TestDummyKeystoreOpensWithDIFsKeystoreReader(t *testing.T) {
	keys, certs, err := keystore.Decode(dummyIdentity, DummyPassword)
	if err != nil {
		t.Fatal(err)
	}
	if len(keys) != 1 || len(certs) != 1 {
		t.Fatalf("keys=%d certs=%d", len(keys), len(certs))
	}
	if !strings.HasPrefix(DummyKeystoreDataURI(), "data:application/x-pkcs12;base64,MII") {
		t.Fatal("unexpected data URI")
	}
}
