package dil_test

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"dif/api"
	"dif/internal/sanitize"
	"dif/internal/secret"
)

// TestFlowsBuild builds every flow in testdata, as `dif run` does before it
// starts one. A flow that is in testdata must build: fix the step, or take the
// flow out.
func TestFlowsBuild(t *testing.T) {
	t.Chdir(workdir) // processors may create directories and read security/ relative to here

	files := flowFiles(t)
	if len(files) == 0 {
		t.Fatal("no flows in testdata")
	}
	var failed []string
	for _, f := range files {
		data, err := os.ReadFile(path(f))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := api.LoadBytes(data, nil); err != nil {
			failed = append(failed, fmt.Sprintf("  %s\n      %v", f, err))
		}
	}
	if len(failed) > 0 {
		t.Errorf("%d of the %d flows do not build:\n%s", len(failed), len(files), strings.Join(failed, "\n"))
	}
	t.Logf("%d flows build", len(files)-len(failed))
}

// TestFixturesHaveNoCredentials fails when a fixture holds a credential, so
// that a new flow cannot be committed with one. Fix it with:
//
//	go run ./cmd/sanitize
func TestFixturesHaveNoCredentials(t *testing.T) {
	found, err := sanitize.Run([]string{path("testdata")}, false)
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
		t.Fatalf("%d credentials in the fixtures; run: go run ./cmd/sanitize\n%s", len(found), b.String())
	}
}

var encValue = regexp.MustCompile(`ENC\([^)]*\)`)

// TestEncryptedFixturesDecrypt checks that every ENC(...) value in the fixtures
// is a real value that decrypts under the test password.
func TestEncryptedFixturesDecrypt(t *testing.T) {
	var n int
	err := filepath.WalkDir(path("testdata"), func(p string, d fs.DirEntry, err error) error {
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
	t.Setenv(secret.PasswordEnv, "")
	os.Unsetenv(secret.PasswordEnv)
	var failed []string
	for _, f := range flowFiles(t) {
		data, err := os.ReadFile(path(f))
		if err != nil {
			t.Fatal(err)
		}
		if !encValue.Match(data) {
			continue
		}
		_, err = api.LoadBytes(data, nil)
		switch {
		case err == nil:
			continue // the value is in a part of the file that no step reads, such as a connection
		case !strings.Contains(err.Error(), secret.PasswordEnv) || showsValue(err.Error()):
			t.Errorf("%s: unclear or unsafe error: %v", f, err)
		}
		failed = append(failed, f)
	}
	if len(failed) == 0 {
		t.Fatal("no flow with an ENC value needs the password")
	}
	sort.Strings(failed)
	t.Logf("%d flows with an ENC value need %s", len(failed), secret.PasswordEnv)
}
