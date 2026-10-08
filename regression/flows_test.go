package regression

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"dif/api"
	"dif/internal/secret"
	"dif/regression/sanitize"
)

// TestFlowsLoad builds every flow that is not skipped, as `dif run` does before
// it starts one, and compares the flows that build with loadable.json.
func TestFlowsLoad(t *testing.T) {
	skips := loadSkips(t)
	t.Chdir(workdir) // processors may create directories and read security/ relative to here

	var loaded []string
	failed := map[string]string{}
	var nSkipped int
	for _, f := range flowFiles(t) {
		if _, ok := skips[f]; ok {
			nSkipped++
			continue
		}
		data, err := os.ReadFile(path(f))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := api.LoadBytes(data, nil); err != nil {
			failed[f] = err.Error()
			continue
		}
		loaded = append(loaded, f)
	}
	t.Logf("%d flows build, %d do not, %d are skipped", len(loaded), len(failed), nSkipped)
	ratchet(t, "loadable.json", loaded, failed)
}

// TestSkipManifest keeps skip.json honest: every entry names a flow that exists
// and says why it is skipped.
func TestSkipManifest(t *testing.T) {
	files := map[string]bool{}
	for _, f := range flowFiles(t) {
		files[f] = true
	}
	categories := map[string]bool{"groovy": true, "custom-step": true, "broken-fixture": true}
	for f, s := range loadSkips(t) {
		if !files[f] {
			t.Errorf("skip.json names %s, which does not exist", f)
		}
		if !categories[s.Category] || strings.TrimSpace(s.Reason) == "" {
			t.Errorf("skip.json: %s needs a category (groovy, custom-step or broken-fixture) and a reason", f)
		}
	}
}

// TestFixturesHaveNoCredentials fails when a fixture holds a credential, so
// that a new import cannot be committed with one. Fix it with:
//
//	go run ./regression/cmd/sanitize
func TestFixturesHaveNoCredentials(t *testing.T) {
	found, err := sanitize.Run([]string{path("regressionTests"), path("postman")}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(found) > 0 {
		var b strings.Builder
		for i, f := range found {
			if i == 20 {
				b.WriteString("  ...\n")
				break
			}
			b.WriteString("  " + strings.TrimPrefix(f.String(), repoRoot+"/") + "\n")
		}
		t.Fatalf("%d credentials in the fixtures; run: go run ./regression/cmd/sanitize\n%s", len(found), b.String())
	}
}

var encValue = regexp.MustCompile(`ENC\([^)]*\)`)

// TestEncryptedFixturesDecrypt checks that every ENC(...) value in the fixtures
// is a real value that decrypts under the test password.
func TestEncryptedFixturesDecrypt(t *testing.T) {
	var n int
	for _, root := range []string{"regressionTests", "postman"} {
		err := filepath.WalkDir(path(root), func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			data, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			for _, v := range encValue.FindAllString(string(data), -1) {
				n++
				if plain, err := secret.Decrypt(sanitize.TestEncryptionPassword, v); err != nil || plain != sanitize.DummyPassword {
					t.Errorf("%s: an ENC value does not decrypt to the dummy password under the test password: %v", p, err)
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if n == 0 {
		t.Fatal("no ENC values in the fixtures")
	}
	t.Logf("%d ENC values, all decrypt", n)
}

// showsValue reports whether s holds a real ENC(...) value; the text "ENC(...)" is no value.
func showsValue(s string) bool {
	for _, v := range encValue.FindAllString(s, -1) {
		if secret.IsEncrypted(v) {
			return true
		}
	}
	return false
}

// TestEncryptedFlowsNeedThePassword builds the flows that hold an ENC(...) value
// without a password: each must fail and say which variable to set, and show nothing.
func TestEncryptedFlowsNeedThePassword(t *testing.T) {
	t.Chdir(workdir)
	skips := loadSkips(t)
	t.Setenv(secret.PasswordEnv, "")
	os.Unsetenv(secret.PasswordEnv)
	var failed int
	for _, f := range readList(t, "loadable.json") {
		data, err := os.ReadFile(path(f))
		if err != nil {
			t.Fatal(err)
		}
		if _, skip := skips[f]; skip || !encValue.Match(data) {
			continue
		}
		_, err = api.LoadBytes(data, nil)
		switch {
		case err == nil:
			continue // the value is in a part of the file that no step reads, such as a connection
		case !strings.Contains(err.Error(), secret.PasswordEnv) || showsValue(err.Error()):
			t.Errorf("%s: unclear or unsafe error: %v", f, err)
		}
		failed++
	}
	if failed == 0 {
		t.Fatal("no flow with an ENC value needs the password")
	}
	t.Logf("%d flows with an ENC value need %s", failed, secret.PasswordEnv)
}
