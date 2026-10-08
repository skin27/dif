// Package dil_test checks the DIL flows in testdata: every flow builds, and the
// fixtures hold no credentials. See README.md.
package dil_test

import (
	"bytes"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"dif/internal/sanitize"
	"dif/internal/secret"
	"dif/keystore"
)

var (
	repoRoot string // absolute path of the repository
	workdir  string // scratch directory with security/server-identity.p12 and outbound-truststore.p12, the working directory of the tests
)

func TestMain(m *testing.M) {
	code, err := run(m)
	if err != nil {
		fmt.Fprintln(os.Stderr, "dil:", err)
		code = 2
	}
	os.Exit(code)
}

func run(m *testing.M) (int, error) {
	var err error
	if repoRoot, err = filepath.Abs("../.."); err != nil {
		return 0, err
	}
	if workdir, err = os.MkdirTemp("", "dif-dil-"); err != nil {
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
	os.Setenv("DIF_OAUTH2_CLIENT_ID", "dil")
	os.Setenv("DIF_OAUTH2_CLIENT_SECRET", sanitize.DummyPassword)
	// The ENC(...) values in the fixtures are encrypted with the test password.
	os.Setenv(secret.PasswordEnv, sanitize.TestEncryptionPassword)
	return m.Run(), nil
}

// path returns the absolute path of a path below the repository.
func path(rel string) string { return filepath.Join(repoRoot, filepath.FromSlash(rel)) }

// notBuilt are the directories of testdata whose flows are not meant to build
// on their own. Other tests read them.
var notBuilt = map[string]bool{
	"testdata/reliable": true, // needs the channel runtime that its service.json configures; internal/service tests it
	"testdata/parse":    true, // steps that DIL exports as "unknown", which no processor runs; flows/impl tests how they parse
}

// flowFiles returns the DIL documents below testdata that must build, as slash
// separated paths below the repository.
func flowFiles(t *testing.T) []string {
	t.Helper()
	var files []string
	err := filepath.WalkDir(path("testdata"), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if rel, _ := filepath.Rel(repoRoot, p); d.IsDir() && notBuilt[filepath.ToSlash(rel)] {
			return fs.SkipDir
		}
		if d.IsDir() || filepath.Ext(p) != ".json" {
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		var doc map[string]json.RawMessage
		if json.Unmarshal(bytes.TrimPrefix(data, []byte("\xef\xbb\xbf")), &doc) == nil && doc["dil"] != nil {
			rel, _ := filepath.Rel(repoRoot, p)
			files = append(files, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(files)
	return files
}
