// Package regression runs DIF against the regression fixtures: the DIL flows
// in regressionTests and the Postman requests in postman, which state what
// each flow must answer. See README.md.
package regression

import (
	"encoding/json"
	"encoding/pem"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"dif/internal/secret"
	"dif/keystore"
	"dif/regression/sanitize"
)

var (
	update     = flag.Bool("update", false, "rewrite loadable.json and postman-passing.json to the current results")
	runPostman = flag.Bool("postman", false, "run the Postman requests with Newman (needs node and npm, and network access to install Newman; takes minutes)")
)

var (
	repoRoot string // absolute path of the repository
	workdir  string // scratch directory with security/server-identity.p12 and outbound-truststore.p12, the working directory of the tests
)

func TestMain(m *testing.M) {
	flag.Parse()
	code, err := run(m)
	if err != nil {
		fmt.Fprintln(os.Stderr, "regression:", err)
		code = 2
	}
	os.Exit(code)
}

func run(m *testing.M) (int, error) {
	var err error
	if repoRoot, err = filepath.Abs(".."); err != nil {
		return 0, err
	}
	if workdir, err = os.MkdirTemp("", "dif-regression-"); err != nil {
		return 0, err
	}
	defer os.RemoveAll(workdir)

	// The HTTPS sources need a server identity, and the flows that call each
	// other over HTTPS must trust it: Go reads extra roots from SSL_CERT_FILE.
	identity := sanitize.DummyIdentityPKCS12()
	if err := os.MkdirAll(filepath.Join(workdir, "security"), 0o755); err != nil {
		return 0, err
	}
	if err := os.WriteFile(filepath.Join(workdir, "security", "server-identity.p12"), identity, 0o600); err != nil {
		return 0, err
	}
	// The https and rest actions trust the certificates of security/outbound-truststore.p12,
	// which here is the dummy identity again, so that they trust the sources of these flows.
	if err := os.WriteFile(filepath.Join(workdir, "security", "outbound-truststore.p12"), identity, 0o600); err != nil {
		return 0, err
	}
	_, certs, err := keystore.Decode(identity, sanitize.DummyPassword)
	if err != nil || len(certs) == 0 {
		return 0, fmt.Errorf("dummy identity: %v", err)
	}
	ca := filepath.Join(workdir, "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certs[0].Raw}), 0o644); err != nil {
		return 0, err
	}
	os.Setenv("SSL_CERT_FILE", ca)
	os.Setenv("DIF_SERVER_IDENTITY_PASSWORD", sanitize.DummyPassword)
	os.Setenv("DIF_TRUSTSTORE_PASSWORD", sanitize.DummyPassword)
	// The oauth2token steps of the fixtures name only their token: a deployment
	// gives the endpoint and the client in DIF_OAUTH2_* (see the step). The
	// flows are token services, whose repeaters the tests do not start, so these
	// are never called.
	os.Setenv("DIF_OAUTH2_TOKEN_URL", "https://localhost:9/token")
	os.Setenv("DIF_OAUTH2_CLIENT_ID", "regression")
	os.Setenv("DIF_OAUTH2_CLIENT_SECRET", sanitize.DummyPassword)
	// The ENC(...) values in the fixtures are encrypted with the test password.
	os.Setenv(secret.PasswordEnv, sanitize.TestEncryptionPassword)
	return m.Run(), nil
}

// path returns the absolute path of a path below the repository.
func path(rel string) string { return filepath.Join(repoRoot, filepath.FromSlash(rel)) }

// flowFiles returns the flows in regressionTests, as slash separated paths below the repository.
func flowFiles(t *testing.T) []string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(path("regressionTests"), func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".json") {
			rel, _ := filepath.Rel(repoRoot, p)
			files = append(files, filepath.ToSlash(rel))
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(files)
	return files
}

// A skipped flow is not run, and neither are its Postman requests.
type skipped struct {
	File     string `json:"file"`
	Category string `json:"category"` // groovy, custom-step or broken-fixture
	Reason   string `json:"reason"`
}

func loadSkips(t *testing.T) map[string]skipped {
	t.Helper()
	data, err := os.ReadFile(path("regression/skip.json"))
	if err != nil {
		t.Fatal(err)
	}
	var list []skipped
	if err := json.Unmarshal(data, &list); err != nil {
		t.Fatalf("skip.json: %v", err)
	}
	skips := make(map[string]skipped, len(list))
	for _, s := range list {
		skips[s.File] = s
	}
	return skips
}

// readList reads a ratchet file: a JSON array of strings.
func readList(t *testing.T, name string) []string {
	t.Helper()
	data, err := os.ReadFile(path("regression/" + name))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	var list []string
	if err := json.Unmarshal(data, &list); err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return list
}

// writeList writes a ratchet file with one entry per line, which keeps diffs small.
func writeList(t *testing.T, name string, list []string) {
	t.Helper()
	list = slices.Clone(list)
	sort.Strings(list)
	var b strings.Builder
	b.WriteString("[\n")
	for i, s := range list {
		enc, _ := json.Marshal(s)
		b.WriteString("  " + string(enc))
		if i < len(list)-1 {
			b.WriteString(",")
		}
		b.WriteString("\n")
	}
	b.WriteString("]\n")
	if err := os.WriteFile(path("regression/"+name), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

// ratchet compares the passing set with the one in the file. A test that
// passed before and does not now is a regression; one that passes now and is
// not in the file must be added with -update, so that the file stays exact
// and later regressions are caught. why explains the failures it lists.
func ratchet(t *testing.T, name string, passing []string, why map[string]string) {
	t.Helper()
	if *update {
		writeList(t, name, passing)
		t.Logf("%s rewritten: %d passing", name, len(passing))
		return
	}
	want := readList(t, name)
	got := map[string]bool{}
	for _, s := range passing {
		got[s] = true
	}
	inWant := map[string]bool{}
	var regressed, improved []string
	for _, s := range want {
		inWant[s] = true
		if !got[s] {
			regressed = append(regressed, s)
		}
	}
	for _, s := range passing {
		if !inWant[s] {
			improved = append(improved, s)
		}
	}
	sort.Strings(regressed)
	sort.Strings(improved)
	if len(regressed) > 0 {
		t.Errorf("%d of the %d tests in %s no longer pass:\n%s", len(regressed), len(want), name, listWithReasons(regressed, why))
	}
	if len(improved) > 0 {
		t.Errorf("%d tests pass that are not in %s yet; run: go test ./regression -update\n%s", len(improved), name, listWithReasons(improved, nil))
	}
	t.Logf("%s: %d passing", name, len(passing))
}

func listWithReasons(items []string, why map[string]string) string {
	const max = 25
	var b strings.Builder
	for i, s := range items {
		if i == max {
			fmt.Fprintf(&b, "  ... and %d more\n", len(items)-max)
			break
		}
		fmt.Fprintf(&b, "  %s", s)
		if r := why[s]; r != "" {
			fmt.Fprintf(&b, "\n      %s", r)
		}
		b.WriteString("\n")
	}
	return b.String()
}
