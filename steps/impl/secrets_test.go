package impl

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMountedSecretPrecedenceAndErrors(t *testing.T) {
	const name = "DIF_TEST_MOUNTED_SECRET"
	path := filepath.Join(t.TempDir(), "password")
	if err := os.WriteFile(path, []byte(" secret \r\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(name+"_FILE", path)
	value, given, err := environmentSecret(name)
	if err != nil || !given || value != " secret " {
		t.Fatalf("file: %q %t %v", value, given, err)
	}
	t.Setenv(name, "")
	value, given, err = environmentSecret(name)
	if err != nil || !given || value != "" {
		t.Fatalf("empty environment should win: %q %v", value, err)
	}
	const missing = "DIF_TEST_MISSING_SECRET"
	t.Setenv(missing+"_FILE", filepath.Join(t.TempDir(), "private-path"))
	_, _, err = environmentSecret(missing)
	if err == nil || strings.Contains(err.Error(), "private-path") {
		t.Fatalf("unsafe diagnostic: %v", err)
	}
}

func TestMountedKeystorePasswords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "password")
	if err := os.WriteFile(path, []byte(testPassword+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	// Do not mutate the real credential variables: exercise the same password
	// resolver with an isolated environment name and the real fixture keystore.
	k := keystorePassword{"password", "DIF_TEST_KEYSTORE_SECRET"}
	t.Setenv(k.env+"_FILE", path)
	pw, given, err := k.get(nil)
	if err != nil || !given || pw != testPassword {
		t.Fatalf("password: %t %v", given, err)
	}
	pw, _, err = k.get(map[string]any{"password": "explicit"})
	if err != nil || pw != "explicit" {
		t.Fatal("explicit option did not win")
	}
}
