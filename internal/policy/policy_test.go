package policy

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	stepdef "dif/steps/definition"
)

func TestParseGrants(t *testing.T) {
	bad := map[string]map[string][]string{
		"net.connect: no port":      {stepdef.NetConnect: {"api.example.com"}},
		"net.connect: bad port":     {stepdef.NetConnect: {"api.example.com:http"}},
		"net.connect: port 0":       {stepdef.NetConnect: {"api.example.com:0"}},
		"net.connect: inner *":      {stepdef.NetConnect: {"api.*.com:443"}},
		"net.listen: empty host":    {stepdef.NetListen: {":443"}},
		"empty grant":               {stepdef.FSRead: {" "}},
		"env with =":                {stepdef.EnvVar: {"A=B"}},
		"unknown kind":              {"exec": {"ls"}},
		"unknown kind, misspelling": {"net.connnect": {"*"}},
	}
	for name, grants := range bad {
		if _, err := New(grants); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	p, err := New(map[string][]string{
		stepdef.NetConnect: {"API.Example.com:443", "*:5432", "[::1]:8443", "*.internal.example:*"},
		stepdef.NetListen:  {"127.0.0.1:9002"},
		stepdef.FSRead:     {"inbox"},
		stepdef.EnvVar:     {"*"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(p.Grants(stepdef.NetConnect)); got != "[api.example.com:443 *:5432 [::1]:8443 *.internal.example:*]" {
		t.Errorf("net.connect grants: %s", got)
	}
	abs, _ := filepath.Abs("inbox")
	if got := p.Grants(stepdef.FSRead); len(got) != 1 || got[0] != abs {
		t.Errorf("fs.read grants: %v, want [%s]", got, abs)
	}
	if empty, err := New(nil); err != nil || empty.Allows(stepdef.Capability{Kind: stepdef.EnvVar, Target: "HOME"}) {
		t.Errorf("an empty policy: %v, or it allows something", err)
	}
}

func TestMatchNetwork(t *testing.T) {
	p, err := New(map[string][]string{
		stepdef.NetConnect: {"api.example.com:443", "*.internal.example:5432", "db.example.com:*", "10.0.0.1:21", "[2001:db8::1]:22"},
		stepdef.NetListen:  {"127.0.0.1:9002", "*:8080"},
	})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		kind, target string
		want         bool
	}{
		{stepdef.NetConnect, "api.example.com:443", true},
		{stepdef.NetConnect, "API.EXAMPLE.COM:443", true},  // case does not count
		{stepdef.NetConnect, "api.example.com.:443", true}, // nor a final dot
		{stepdef.NetConnect, "api.example.com:80", false},
		{stepdef.NetConnect, "evil.example.com:443", false},
		{stepdef.NetConnect, "api.example.com.evil.example:443", false},
		{stepdef.NetConnect, "pg.internal.example:5432", true},
		{stepdef.NetConnect, "a.b.internal.example:5432", true},
		{stepdef.NetConnect, "internal.example:5432", false}, // *. covers subdomains only
		{stepdef.NetConnect, "xinternal.example:5432", false},
		{stepdef.NetConnect, "pg.internal.example:5433", false},
		{stepdef.NetConnect, "db.example.com:1", true},
		{stepdef.NetConnect, "10.0.0.1:21", true},
		{stepdef.NetConnect, "localhost:21", false}, // names are not resolved
		{stepdef.NetConnect, "[2001:db8::1]:22", true},
		{stepdef.NetConnect, "[2001:db8::2]:22", false},
		{stepdef.NetConnect, "no port", false},
		{stepdef.NetListen, "127.0.0.1:9002", true},
		{stepdef.NetListen, "0.0.0.0:9002", false},
		{stepdef.NetListen, "0.0.0.0:8080", true},
		{stepdef.NetListen, "api.example.com:443", false}, // a connect grant is no listen grant
	}
	for _, tc := range tests {
		c := stepdef.Capability{Kind: tc.kind, Target: tc.target}
		if got := p.Allows(c); got != tc.want {
			t.Errorf("%s: %v, want %v", c, got, tc.want)
		}
		err := p.Permit(c)
		var ce *stepdef.CapabilityError
		if (err == nil) != tc.want || (err != nil && (!errors.As(err, &ce) || ce.Target != tc.target)) {
			t.Errorf("Permit(%s) = %v", c, err)
		}
	}
	all, _ := New(map[string][]string{stepdef.NetConnect: {"*"}})
	if !all.Allows(stepdef.Capability{Kind: stepdef.NetConnect, Target: "anything:1"}) {
		t.Error("* does not grant everything")
	}
}

func TestMatchPaths(t *testing.T) {
	tests := []struct {
		grant, target string
		windows, want bool
	}{
		{"/data/in", "/data/in", false, true},
		{"/data/in", "/data/in/a.txt", false, true},
		{"/data/in", "/data/in/sub/a.txt", false, true},
		{"/data/in", "/data/inbox/a.txt", false, false}, // a prefix of the name is not the directory
		{"/data/in", "/data/in/../out/a.txt", false, false},
		{"/data/in", "/data/in/sub/../a.txt", false, true},
		{"/data/in/", "/data/in/a.txt", false, true},
		{"/data/in", "/data", false, false},
		{"/data/In", "/data/in/a.txt", false, false}, // POSIX paths keep their case
		{"/", "/etc/passwd", false, true},
		{`C:\data\in`, `C:\data\in\a.txt`, true, true},
		{`C:\data\in`, `c:\DATA\IN\a.txt`, true, true}, // Windows paths do not
		{`C:\data\in`, `C:\data\in\..\..\Windows\win.ini`, true, false},
		{`C:\data\in`, `C:\data\inbox`, true, false},
		{`C:\data\in`, `D:\data\in\a.txt`, true, false},
		{`C:\`, `C:\Windows\win.ini`, true, true},
		{`C:\data\in`, `C:/data/in/a.txt`, true, true},
	}
	for _, tc := range tests {
		if got := covers(tc.grant, tc.target, tc.windows); got != tc.want {
			t.Errorf("covers(%q, %q, windows %v) = %v, want %v", tc.grant, tc.target, tc.windows, got, tc.want)
		}
	}

	// Relative paths are taken from the working directory, after cleaning.
	dir := t.TempDir()
	t.Chdir(dir)
	p, err := New(map[string][]string{stepdef.FSWrite: {"outbox"}, stepdef.FSRead: {filepath.Join(dir, "inbox")}})
	if err != nil {
		t.Fatal(err)
	}
	for target, want := range map[string]bool{
		"outbox":                         true,
		filepath.Join("outbox", "a.txt"): true,
		filepath.Join(dir, "outbox", "x", "a.txt"):  true,
		filepath.Join("outbox", "..", "secret.txt"): false,
		filepath.Join("outbox", "..", "inbox"):      false, // fs.read does not grant fs.write
		"inbox":                                     false,
	} {
		if got := p.Allows(stepdef.Capability{Kind: stepdef.FSWrite, Target: target}); got != want {
			t.Errorf("fs.write %s: %v, want %v", target, got, want)
		}
	}
	if !p.Allows(stepdef.Capability{Kind: stepdef.FSRead, Target: "inbox"}) || p.Allows(stepdef.Capability{Kind: stepdef.FSRead, Target: "outbox"}) {
		t.Error("fs.read inbox, and only that, should be granted")
	}
}

func TestMatchEnvironment(t *testing.T) {
	p, _ := New(map[string][]string{stepdef.EnvVar: {"DIF_SMTP_PASSWORD"}})
	for target, want := range map[string]bool{
		"DIF_SMTP_PASSWORD":      true,
		"DIF_SMTP_PASSWORD_FILE": true, // the companion that names a secret's file
		"DIF_SQL_PASSWORD":       false,
		"dif_smtp_password":      false,
		"DIF_SMTP":               false,
	} {
		if got := p.Allows(stepdef.Capability{Kind: stepdef.EnvVar, Target: target}); got != want {
			t.Errorf("env %s: %v, want %v", target, got, want)
		}
	}
}

func TestCheck(t *testing.T) {
	p, _ := New(map[string][]string{stepdef.NetConnect: {"api.example.com:443"}})
	uses := []Use{
		{Flow: "f", Step: "a", Capability: stepdef.Capability{Kind: stepdef.NetConnect, Target: "api.example.com:443"}},
		{Flow: "f", Step: "b", Capability: stepdef.Capability{Kind: stepdef.NetConnect, Target: "evil.example:443"}},
		{Flow: "f", Step: "c", Capability: stepdef.Capability{Kind: stepdef.NetConnect, Target: "${header.host}:443", Dynamic: true}},
		{Flow: "f", Step: "d", Capability: stepdef.Capability{Kind: stepdef.FSRead, Target: "*", Dynamic: true}},
		{Flow: "f", Step: "e", Undeclared: true},
	}
	var got []string
	for _, v := range p.Check(uses) {
		got = append(got, v.Step+": "+v.Reason)
	}
	want := []string{
		"b: net.connect evil.example:443 is not granted",
		"d: fs.read * is known only per message, and nothing of fs.read is granted",
		"e: the step declares no capabilities, so the policy cannot tell what it touches",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("violations:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}

	var none *Policy // no policy: nothing is checked
	if v := none.Check(uses); v != nil || none.Permit(uses[1].Capability) != nil || !none.Allows(uses[1].Capability) {
		t.Errorf("a nil policy refuses: %v", v)
	}
}
