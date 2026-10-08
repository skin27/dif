// Package sanitize replaces credentials in the regression fixtures (the DIL
// flows in regressionTests and the Postman collections in postman) with
// dummies, so that no real secret is committed.
//
// The same original value always becomes the same dummy within a run, so a
// flow and the Postman request that calls it keep matching (a basic-auth
// password, for example). Values that are already dummies, placeholders such as
// ${header.x}, @{variable} and {{variable}}, and empty values stay as they are,
// so running the sanitizer twice changes nothing.
//
// This is a safety net and not a vault: it finds credentials by the names of
// options, headers and variables, and by well-known shapes (Basic and Bearer
// credentials, ENC(...) values, user:password@host and ?apikey=...). Run it
// before committing new fixtures, and review the report.
package sanitize

import (
	"bytes"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"dif/internal/secret"
)

// TestEncryptionPassword is the password that the ENC(...) values in the
// fixtures are encrypted with, to be given to DIF as DIF_ENCRYPTION_PASSWORD.
const TestEncryptionPassword = "dif-regression-test-key"

// DummyPassword is the plain text of DummyENC and the password of the dummy
// keystore that replaces the keystores in the fixtures.
const DummyPassword = "dummy-password"

//go:embed dummy-identity.p12
var dummyIdentity []byte

// dummyPrivateKey is a throwaway OpenSSH private key that replaces the private
// keys embedded in the fixtures.
//
//go:embed sftp-dummy.key
var dummyPrivateKey string

// DummyIdentityPKCS12 is the dummy PKCS#12 keystore (a throwaway self-signed
// key for localhost and 127.0.0.1, protected by DummyPassword). It replaces the
// keystores embedded in the fixtures, and serves as the TLS identity of the
// test server.
func DummyIdentityPKCS12() []byte { return dummyIdentity }

// DummyKeystoreDataURI is the data URI of the dummy PKCS#12 keystore (a
// throwaway self-signed key, protected by DummyPassword) that replaces the
// keystores embedded in the fixtures.
func DummyKeystoreDataURI() string {
	return "data:application/x-pkcs12;base64," + base64.StdEncoding.EncodeToString(dummyIdentity)
}

// DummyENC is DummyPassword encrypted in the format of the Java EncryptionUtil
// (see package secret) under TestEncryptionPassword. Salt and IV are fixed, so
// the value is the same on every run; that is fine for a throwaway fixture secret.
var DummyENC = mustEncrypt(TestEncryptionPassword, []byte("dif-regr-salt-16"), []byte("dif-regr-iv-0016"), DummyPassword)

func mustEncrypt(password string, salt, iv []byte, plain string) string {
	v, err := secret.EncryptWith(password, salt, iv, plain)
	if err != nil {
		panic(err)
	}
	return v
}

// Finding is one credential found (and, when applying, replaced). It never
// holds the credential itself.
type Finding struct {
	File  string
	Where string // the key path, or the line number
	Rule  string
	Len   int // length of the original value
}

func (f Finding) String() string {
	return fmt.Sprintf("%s: %s: %s (%d characters)", f.File, f.Where, f.Rule, f.Len)
}

// Run sanitizes every .json and .yaml file below the dirs. With apply, files
// are rewritten; without, nothing is written, so the result lists what would
// change. Files are handled in sorted order, which makes the dummies
// deterministic.
func Run(dirs []string, apply bool) ([]Finding, error) {
	var files []string
	for _, dir := range dirs {
		err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == ".git" || d.Name() == "node_modules" {
					return filepath.SkipDir
				}
				return nil
			}
			switch strings.ToLower(filepath.Ext(path)) {
			case ".json", ".yaml", ".yml":
				files = append(files, path)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	sort.Strings(files)

	st := &state{table: map[string]string{}, counter: map[string]int{}}
	contents := make(map[string][]byte, len(files))
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		contents[f] = data
		st.seedCounters(data)
	}

	var all []Finding
	for _, f := range files {
		data := contents[f]
		var out []byte
		var found []Finding
		var err error
		if strings.EqualFold(filepath.Ext(f), ".json") {
			out, found, err = st.cleanJSON(f, data)
		} else {
			out, found = st.cleanYAML(f, data)
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f, err)
		}
		all = append(all, found...)
		if apply && !bytes.Equal(out, data) {
			if err := os.WriteFile(f, out, 0o644); err != nil {
				return nil, err
			}
		}
	}
	return all, nil
}

// state is the dummy table of one run.
type state struct {
	table   map[string]string // original value -> dummy
	counter map[string]int    // role -> highest dummy number in use
}

var dummyRe = regexp.MustCompile(`dummy-(password|token|apikey|secret)-(\d+)`)

// seedCounters makes new dummies continue after those already in the files.
func (st *state) seedCounters(data []byte) {
	for _, m := range dummyRe.FindAllSubmatch(data, -1) {
		if n, _ := strconv.Atoi(string(m[2])); n > st.counter[string(m[1])] {
			st.counter[string(m[1])] = n
		}
	}
}

func (st *state) dummy(role, original string) string {
	if d, ok := st.table[original]; ok {
		return d
	}
	st.counter[role]++
	d := fmt.Sprintf("dummy-%s-%d", role, st.counter[role])
	st.table[original] = d
	return d
}

// keep reports whether a value needs no sanitizing: empty, already a dummy, or
// a reference that is resolved at run time.
func keep(v string) bool {
	return strings.TrimSpace(v) == "" || strings.HasPrefix(v, "dummy-") || v == DummyPassword || v == DummyKeystoreDataURI() ||
		strings.Contains(v, "${") || strings.Contains(v, "@{") || strings.Contains(v, "{{")
}

// optionRoles are the option and connection keys (lower case) that hold a
// secret, with the role of the dummy that replaces it.
var optionRoles = map[string]string{
	"password":        "password",
	"authpassword":    "password",
	"accesstoken":     "token",
	"token":           "token",
	"apikey":          "apikey",
	"websearchapikey": "apikey",
	"secret":          "secret",
	"clientsecret":    "secret",
	"auth":            "token", // soap: base64 of user:password
}

var secretNameRe = regexp.MustCompile(`(?i)password|passwd|^pwd$|secret|api[_-]?key|^token$|access[_-]?token|^hc_token$|^dovauth$`)

// roleOfName returns the dummy role for a header or variable name, or "".
func roleOfName(name string) string {
	n := strings.ToLower(strings.TrimSpace(name))
	switch {
	case !secretNameRe.MatchString(n):
		return ""
	case strings.Contains(n, "password"), strings.Contains(n, "passwd"), n == "pwd":
		return "password"
	case strings.Contains(n, "secret"):
		return "secret"
	case strings.Contains(n, "key"):
		return "apikey"
	}
	return "token"
}

var (
	encRe      = regexp.MustCompile(`ENC\([^)]*\)`)
	basicRe    = regexp.MustCompile(`\b(Basic)\s+([A-Za-z0-9+/]{8,}={0,2})`)
	bearerRe   = regexp.MustCompile(`\b(Bearer)\s+([A-Za-z0-9._~+/-]{16,}={0,2})`)
	userinfoRe = regexp.MustCompile(`([A-Za-z][A-Za-z0-9+.-]*://[^/\s:@"'\\]+:)([^@/\s"'\\]+)(@)`)
	queryRe    = regexp.MustCompile(`(?i)([?&;](?:api[_-]?key|access[_-]?token|token|password|passwd|pwd|secret|client[_-]?secret|signature|sig)=)([^&\s"'<>#\\]{6,})`)
	hexKeyRe   = regexp.MustCompile(`([?&;]key=)([0-9a-fA-F]{24,})`)
	pemRe      = regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?-----END [A-Z ]*PRIVATE KEY-----\n?`)
)

// cleanText replaces the credentials that have a recognizable shape anywhere in
// s. rule names each kind it replaced.
func (st *state) cleanText(s string) (string, []string) {
	var rules []string
	note := func(r string) { rules = append(rules, r) }

	if strings.Contains(s, "ENC(") {
		s = encRe.ReplaceAllStringFunc(s, func(m string) string {
			if m == DummyENC {
				return m
			}
			note("ENC value")
			return DummyENC
		})
	}
	if strings.Contains(s, "PRIVATE KEY-----") {
		s = pemRe.ReplaceAllStringFunc(s, func(m string) string {
			if strings.TrimSpace(m) == strings.TrimSpace(dummyPrivateKey) {
				return m
			}
			note("private key")
			if strings.HasSuffix(m, "\n") {
				return dummyPrivateKey
			}
			return strings.TrimSuffix(dummyPrivateKey, "\n")
		})
	}
	if strings.Contains(s, "Basic") {
		s = basicRe.ReplaceAllStringFunc(s, func(m string) string {
			sub := basicRe.FindStringSubmatch(m)
			if v, ok := st.basic(sub[2]); ok && v != sub[2] {
				note("Basic credentials")
				return sub[1] + " " + v
			}
			return m
		})
	}
	if strings.Contains(s, "Bearer") {
		s = bearerRe.ReplaceAllStringFunc(s, func(m string) string {
			sub := bearerRe.FindStringSubmatch(m)
			if keep(sub[2]) || !strings.ContainsAny(sub[2], "0123456789._-") {
				return m
			}
			note("Bearer token")
			return sub[1] + " " + st.dummy("token", sub[2])
		})
	}
	if strings.Contains(s, "://") {
		s = userinfoRe.ReplaceAllStringFunc(s, func(m string) string {
			sub := userinfoRe.FindStringSubmatch(m)
			if keep(sub[2]) {
				return m
			}
			note("password in a URL")
			return sub[1] + st.dummy("password", sub[2]) + sub[3]
		})
	}
	if strings.ContainsAny(s, "?&;") {
		s = hexKeyRe.ReplaceAllStringFunc(s, func(m string) string {
			sub := hexKeyRe.FindStringSubmatch(m)
			note("API key in a URL query")
			return sub[1] + st.dummy("apikey", sub[2])
		})
		s = queryRe.ReplaceAllStringFunc(s, func(m string) string {
			sub := queryRe.FindStringSubmatch(m)
			if keep(sub[2]) {
				return m
			}
			note("secret in a URL query")
			return sub[1] + st.dummy("token", sub[2])
		})
	}
	return s, rules
}

// basic replaces the password of base64 encoded user:password, keeping the user.
func (st *state) basic(b64 string) (string, bool) {
	raw, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return "", false
	}
	user, pass, ok := strings.Cut(string(raw), ":")
	if !ok || !isText(user) {
		return "", false
	}
	if keep(pass) {
		return b64, true
	}
	return base64.StdEncoding.EncodeToString([]byte(user + ":" + st.dummy("password", pass))), true
}

func isText(s string) bool {
	for _, r := range s {
		if r < ' ' || r == 0x7f || r == 0xfffd {
			return false
		}
	}
	return true
}

// ---- DIL JSON ----

type edit struct {
	start, end int
	repl       string
}

func jsonString(s string) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return strings.TrimSuffix(b.String(), "\n")
}

func (st *state) cleanJSON(file string, src []byte) ([]byte, []Finding, error) {
	root, err := parseJSON(src)
	if err != nil {
		return nil, nil, err
	}
	var edits []edit
	var found []Finding
	done := map[*node]bool{}

	replace := func(n *node, where, rule, to string) {
		if n.str == to {
			return
		}
		edits = append(edits, edit{n.start, n.end, jsonString(to)})
		found = append(found, Finding{file, where, rule, len(n.str)})
		done[n] = true
	}

	var walk func(n *node, where string)
	walk = func(n *node, where string) {
		switch n.kind {
		case 'a':
			for i, it := range n.items {
				walk(it, where+"["+strconv.Itoa(i)+"]")
			}
		case 'o':
			cert, hasCert := n.get("certificate")
			pass, hasPass := n.get("password")
			if hasCert && hasPass && strings.HasPrefix(cert.str, "data:application/x-pkcs12") {
				replace(cert, where+".certificate", "embedded keystore", DummyKeystoreDataURI())
				replace(pass, where+".password", "keystore password", DummyPassword)
			}
			name, hasName := n.get("name")
			value, hasValue := n.get("value")
			if hasName && hasValue && !keep(value.str) && !strings.Contains(value.str, "ENC(") {
				if role := roleOfName(name.str); role != "" {
					replace(value, where+".value", "secret header "+name.str, st.dummy(role, value.str))
				}
			}
			for _, f := range n.fields {
				w := where + "." + f.key
				if f.val.kind == 's' && !done[f.val] && !keep(f.val.str) && !strings.Contains(f.val.str, "ENC(") {
					if role, ok := optionRoles[strings.ToLower(f.key)]; ok {
						to := st.dummy(role, f.val.str)
						if strings.ToLower(f.key) == "auth" {
							if v, ok := st.basic(f.val.str); ok {
								to = v
							}
						}
						replace(f.val, w, "secret option "+f.key, to)
					}
				}
				walk(f.val, w)
			}
		case 's':
			if done[n] {
				return
			}
			if to, rules := st.cleanText(n.str); len(rules) > 0 {
				replace(n, where, strings.Join(dedupe(rules), ", "), to)
			}
		}
	}
	walk(root, "$")

	return applyEdits(src, edits), found, nil
}

func dedupe(s []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, v := range s {
		if !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	return out
}

func applyEdits(src []byte, edits []edit) []byte {
	if len(edits) == 0 {
		return src
	}
	sort.Slice(edits, func(i, j int) bool { return edits[i].start < edits[j].start })
	var out bytes.Buffer
	pos := 0
	for _, e := range edits {
		out.Write(src[pos:e.start])
		out.WriteString(e.repl)
		pos = e.end
	}
	out.Write(src[pos:])
	return out.Bytes()
}

// ---- Postman YAML ----

var (
	yamlKeyRe   = regexp.MustCompile(`^(\s*(?:-\s+)?)key:\s*(.*?)\s*$`)
	yamlValueRe = regexp.MustCompile(`^(\s*)value:\s*(.*?)\s*$`)
	yamlPairRe  = regexp.MustCompile(`^(\s+)([A-Za-z_][\w-]*):\s*(\S.*?)\s*$`)
)

func unquote(s string) string {
	if len(s) >= 2 && (s[0] == '"' && s[len(s)-1] == '"' || s[0] == '\'' && s[len(s)-1] == '\'') {
		return s[1 : len(s)-1]
	}
	return s
}

// splitEOL splits a line from its line ending.
func splitEOL(line string) (body, eol string) {
	body = line
	if strings.HasSuffix(body, "\n") {
		eol = "\n"
		body = strings.TrimSuffix(body, "\n")
		if strings.HasSuffix(body, "\r") {
			eol = "\r\n"
			body = strings.TrimSuffix(body, "\r")
		}
	}
	return body, eol
}

// continues reports whether a line of a double-quoted scalar ends with an odd
// number of backslashes, which joins it to the next line.
func continues(body string) bool {
	n := 0
	for i := len(body) - 1; i >= 0 && body[i] == '\\'; i-- {
		n++
	}
	return n%2 == 1
}

// unfold joins the lines of a double-quoted scalar that is folded with a
// trailing backslash (Postman folds long URLs that way), where the joined text
// holds a credential that the parts do not show. Other lines stay as they are.
func (st *state) unfold(file string, lines [][]byte) ([][]byte, []Finding) {
	var out [][]byte
	var found []Finding
	for i := 0; i < len(lines); i++ {
		j, joined, eol := i, "", ""
		for ; j < len(lines); j++ {
			body, e := splitEOL(string(lines[j]))
			eol = e
			if j > i {
				body = strings.TrimLeft(body, " \t")
			}
			if continues(body) && j+1 < len(lines) {
				joined += body[:len(body)-1]
				continue
			}
			joined += body
			break
		}
		if j >= len(lines) {
			j = len(lines) - 1
		}
		if j > i {
			if to, rules := st.cleanText(joined); len(rules) > 0 {
				out = append(out, []byte(to+eol))
				found = append(found, Finding{file, "line " + strconv.Itoa(i+1), strings.Join(dedupe(rules), ", ") + " (in a folded line)", 0})
				i = j
				continue
			}
		}
		out = append(out, lines[i])
	}
	return out, found
}

var blockStartRe = regexp.MustCompile(`(?::|-)\s+[|>][+-]?\d?\s*$`)

func indentOf(s string) int { return len(s) - len(strings.TrimLeft(s, " \t")) }

// oddQuotes reports whether s has an odd number of unescaped double quotes,
// which opens or closes a multi-line quoted scalar.
func oddQuotes(s string) bool {
	n := 0
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case '"':
			n++
		}
	}
	return n%2 == 1
}

// cleanYAML works line by line: Postman writes variables and headers as
// "key: K" followed by "value: V", or as "K: V". Lines inside a block scalar
// or a multi-line quoted string are content (a request body, a script), so only
// the recognizable shapes are replaced there, not whole values by their key.
func (st *state) cleanYAML(file string, src []byte) ([]byte, []Finding) {
	lines, found := st.unfold(file, bytes.SplitAfter(src, []byte("\n")))
	pendingRole, pendingName, pendingLeft := "", "", 0
	blockIndent := -1 // indent of the line that opened the block scalar we are in, or -1
	inQuoted := false

	for i, raw := range lines {
		body, eol := splitEOL(string(raw))
		where := "line " + strconv.Itoa(i+1)
		ind := indentOf(body)
		blank := strings.TrimSpace(body) == ""
		if blockIndent >= 0 && !blank && ind <= blockIndent {
			blockIndent = -1
		}
		content := blockIndent >= 0 || inQuoted
		replaced := false

		if !content {
			if pendingLeft > 0 {
				pendingLeft--
				if m := yamlValueRe.FindStringSubmatch(body); m != nil {
					if v := unquote(m[2]); !keep(v) && v != "" && !strings.HasPrefix(v, "|") && !strings.HasPrefix(v, ">") {
						body = m[1] + "value: " + strconv.Quote(st.dummy(pendingRole, v))
						found = append(found, Finding{file, where, "secret variable " + pendingName, len(v)})
						replaced = true
					}
					pendingLeft = 0
				}
			}
			if !replaced {
				if m := yamlKeyRe.FindStringSubmatch(body); m != nil {
					if role := roleOfName(unquote(m[2])); role != "" {
						pendingRole, pendingName, pendingLeft = role, unquote(m[2]), 3
					} else {
						pendingLeft = 0
					}
				} else if m := yamlPairRe.FindStringSubmatch(body); m != nil {
					if role := roleOfName(m[2]); role != "" && !strings.HasPrefix(m[3], "|") && !strings.HasPrefix(m[3], ">") && !oddQuotes(m[3]) {
						if v := unquote(m[3]); !keep(v) {
							body = m[1] + m[2] + ": " + strconv.Quote(st.dummy(role, v))
							found = append(found, Finding{file, where, "secret header " + m[2], len(v)})
							replaced = true
						}
					}
				}
			}
		}
		if !replaced {
			if to, rules := st.cleanText(body); len(rules) > 0 {
				body = to
				found = append(found, Finding{file, where, strings.Join(dedupe(rules), ", "), 0})
			}
		}

		if !content && blockStartRe.MatchString(body) {
			blockIndent = ind
		}
		if blockIndent < 0 && oddQuotes(body) {
			inQuoted = !inQuoted
		}
		lines[i] = []byte(body + eol)
	}
	return bytes.Join(lines, nil), found
}
