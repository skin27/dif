package postman

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const repoPostman = "../../postman/postman"

func TestLoadRepositoryCollections(t *testing.T) {
	if _, err := os.Stat(repoPostman); err != nil {
		t.Skip("no postman directory")
	}
	reqs, err := Load(repoPostman)
	if err != nil {
		t.Fatal(err)
	}
	problems := map[string]int{}
	var simple *Request
	for i, r := range reqs {
		if r.Problem != "" {
			problems[r.Problem]++
		}
		if r.Name == "SimpleReplace" && strings.HasPrefix(r.Collection, "Steps-Basic") {
			simple = &reqs[i]
		}
	}
	t.Logf("%d requests, problems: %v", len(reqs), problems)
	if len(reqs) < 2400 {
		t.Errorf("want at least 2400 requests, got %d", len(reqs))
	}
	for p, n := range problems {
		if strings.HasPrefix(p, "not valid YAML") && n > 0 {
			t.Errorf("%d requests are not valid YAML: %s", n, p)
		}
	}
	if simple == nil {
		t.Fatal("request SimpleReplace not found")
	}
	if simple.URL != "{{BASE}}/CB_SimpleReplace" || simple.Flow != "cb_simplereplace" || simple.Method != "POST" {
		t.Errorf("unexpected request: %+v", *simple)
	}
	if !strings.Contains(simple.Tests, "Pedro, welcome to Dovetail.") || !strings.Contains(simple.Tests, `pm.test("happyflow"`) {
		t.Errorf("the test script was not read:\n%s", simple.Tests)
	}
}

func TestItemAndCollection(t *testing.T) {
	r := Request{ID: "collections/c/a.request.yaml", Name: "a", Method: "POST", URL: "{{BASE}}/x?a=1", BodyMode: "raw", BodyLang: "xml", Body: "<a/>", Tests: "pm.test('x', () => {});\npm.expect(1).to.eql(1);"}
	data, err := Collection("c", []Request{r})
	if err != nil {
		t.Fatal(err)
	}
	var c struct {
		Item []struct {
			ID      string
			Request struct {
				Method string
				URL    string
				Body   struct{ Raw string }
				Header []struct{ Key, Value string }
			}
			Event []struct {
				Listen string
				Script struct{ Exec []string }
			}
		}
	}
	if err := json.Unmarshal(data, &c); err != nil {
		t.Fatal(err)
	}
	it := c.Item[0]
	if it.ID != r.ID || it.Request.Method != "POST" || it.Request.Body.Raw != "<a/>" {
		t.Errorf("item = %+v", it)
	}
	if len(it.Request.Header) != 1 || it.Request.Header[0].Value != "application/xml" {
		t.Errorf("an xml body should get a Content-Type: %+v", it.Request.Header)
	}
	if len(it.Event) != 1 || it.Event[0].Listen != "test" || len(it.Event[0].Script.Exec) != 2 {
		t.Errorf("events = %+v", it.Event)
	}
}

func TestLenientDecoding(t *testing.T) {
	dir := t.TempDir()
	body := "$kind: http-request\nurl: \"{{HOST}}://{{INSTANCE}}.{{BASE_URI}}/{{ENVIRONMENT}}/{{PATH}}/Flow\"\nmethod: POST\nbody:\n  type: text\n  content: \"a\x7fb\xffc\"\n"
	if err := os.MkdirAll(filepath.Join(dir, "collections", "c"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "collections", "c", "x.request.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	reqs, err := Load(dir)
	if err != nil || len(reqs) != 1 {
		t.Fatalf("%v %d", err, len(reqs))
	}
	r := reqs[0]
	if r.Problem != "" || r.Body != "a\x7fb�c" || r.Flow != "flow" {
		t.Errorf("%+v", r)
	}
}
