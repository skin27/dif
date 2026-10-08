package secret

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// javaVectors were made by the Java EncryptionUtil of Assimbly (JDK 21; its
// encrypt and decrypt methods as they are, without the jasypt field they do not
// use), which also decrypted each of them back to its plain text.
var javaVectors = []struct{ password, plain, enc string }{
	{"vector-password-1", "hunter22", "ENC(MUIgE3IHqgPmUQ9qyyOdtw==|3v7+OIgbaGdiodkVvrY4XQ==|jjpsKDsxY7aaQYFZU5yz8A==)"},
	{"vector-password-1", "pässwörd ✓ 日本語", "ENC(AJvy9zkn2WZhGVe2luGJeg==|Sve0UU9xYqXnrvcoe+v00Q==|fn+9Eeo3+Aa1dy9Q2qKGbV8uacyWoc3wCGiK7bml0/o=)"},
	{"vector-password-1", "", "ENC(Q89WmSo7/uCEaASBaGuwdQ==|RwjhOlVV7ogMMleALhG1Bw==|3j6Wa0Kw4hOkw2r5T6ZieA==)"},
	{"vector-password-1", "a", "ENC(zKEClpOq2PjIqmB3tPqfmg==|y+vftxcw1uxUxKw4GGcI5A==|lW3gHfQAwPgdkutNUfPcIw==)"},
	{"vector-password-1", "exactly-16-bytes!", "ENC(QDsHPy7r/63ntLAydWMKIQ==|uHgHKPxv0RkVxR/DM8kuZA==|DkxsoiLV7lrYUfMKQYqPZAMEBEE8ZoNl/3oRBNC7m1c=)"},
	{"vector-password-1", `p"a\ss{w}ord=&`, "ENC(C36VEoYkecQXSZVOUQDHgg==|iPXHqzG7JMMkPxc1+5hZBQ==|GN88kEOp8OuZojhP2LnFGw==)"},
	{"vector-password-1", "sixteen-bytes-ok", "ENC(JdUJcV3mnmAOgCkZtT251g==|c90Ero3EjqiJT9wxSLKLMQ==|E9EB+wlrwjleTqMZfGI+/MvN8+lnEw6NFyn/ZEduhyo=)"},
	{"vector-password-1", "https://example.org/path?token=abc&x=1 — a longer text, with several words, to span more than a few AES blocks.", "ENC(m07wnF10bH9lM/t+aiw5UA==|H1gfqq+8apa9Zysri6CIyg==|SyMlX4/Urvkqkic6m0hK9Dh9WxWnNGWNXeKuqgn+p1LUwPv4opxnhrJmZJZIZ2JQdWuUaTGZLbwm6r38/h4Ccd/d+KfzXaBfZn6nLcNyEHED2DvZTKs6RdZPGj0ZfQDOrWWSzL9av8w/LAwp2+OJ2sJOf5H3kLqL7aI9oq5uoBk=)"},
	{"pässwörd-✓", "hunter22", "ENC(6OjETUig5VtLrMyKplhlzw==|D7IOSWXDWq5pcwvE72JB9Q==|9303UXIkS3tQsYhJ+RU67A==)"},
}

func TestDecryptsWhatJavaEncrypted(t *testing.T) {
	for _, v := range javaVectors {
		got, err := Decrypt(v.password, v.enc)
		if err != nil || got != v.plain {
			t.Errorf("Decrypt(%q, %.30s...) = %q, %v; want %q", v.password, v.enc, got, err, v.plain)
		}
	}
}

func TestEncryptsWhatJavaDecrypts(t *testing.T) {
	// Java checked these against its own decrypt, so a value that we make from the
	// same salt and iv must be the very value Java made.
	for _, v := range javaVectors {
		p, err := parse(v.enc)
		if err != nil {
			t.Fatal(err)
		}
		got, err := EncryptWith(v.password, p.salt, p.iv, v.plain)
		if err != nil || got != v.enc {
			t.Errorf("EncryptWith(%q, %q) = %q, %v; want %q", v.password, v.plain, got, err, v.enc)
		}
	}
}

func TestRoundTrip(t *testing.T) {
	for _, plain := range []string{"", "x", "sixteen-bytes-ok", "pässwörd ✓", strings.Repeat("long ", 100)} {
		enc, err := Encrypt("pw", plain)
		if err != nil {
			t.Fatal(err)
		}
		again, _ := Encrypt("pw", plain)
		if enc == again {
			t.Error("two encryptions of the same text must differ (random salt and iv)")
		}
		got, err := Decrypt("pw", enc)
		if err != nil || got != plain {
			t.Errorf("round trip of %q: %q, %v", plain, got, err)
		}
	}
	if enc, _ := Encrypt("pw", javaVectors[0].enc); enc != javaVectors[0].enc {
		t.Error("a value that is already encrypted must stay as it is")
	}
}

func TestWrongPasswordAndDamagedValues(t *testing.T) {
	v := javaVectors[0]
	if got, err := Decrypt("not the password", v.enc); !errors.Is(err, ErrDecrypt) {
		t.Errorf("wrong password: %q, %v", got, err)
	}
	for name, enc := range map[string]string{
		"no prefix":         "MUIgE3IHqgPmUQ9qyyOdtw==|3v7+OIgbaGdiodkVvrY4XQ==|jjpsKDsxY7aaQYFZU5yz8A==",
		"no suffix":         "ENC(MUIgE3IHqgPmUQ9qyyOdtw==|3v7+OIgbaGdiodkVvrY4XQ==|jjpsKDsxY7aaQYFZU5yz8A==",
		"two parts":         "ENC(MUIgE3IHqgPmUQ9qyyOdtw==|3v7+OIgbaGdiodkVvrY4XQ==)",
		"four parts":        "ENC(MUIgE3IHqgPmUQ9qyyOdtw==|3v7+OIgbaGdiodkVvrY4XQ==|jjpsKDsxY7aaQYFZU5yz8A==|AAAA)",
		"not base64":        "ENC(MUIgE3IHqgPmUQ9qyyOdtw==|3v7+OIgbaGdiodkVvrY4XQ==|***)",
		"empty salt":        "ENC(|3v7+OIgbaGdiodkVvrY4XQ==|jjpsKDsxY7aaQYFZU5yz8A==)",
		"short iv":          "ENC(MUIgE3IHqgPmUQ9qyyOdtw==|AAAA|jjpsKDsxY7aaQYFZU5yz8A==)",
		"partial block":     "ENC(MUIgE3IHqgPmUQ9qyyOdtw==|3v7+OIgbaGdiodkVvrY4XQ==|AAAA)",
		"empty cipher":      "ENC(MUIgE3IHqgPmUQ9qyyOdtw==|3v7+OIgbaGdiodkVvrY4XQ==|)",
		"plain parentheses": "ENC(abc)",
	} {
		if _, err := Decrypt(v.password, enc); !errors.Is(err, ErrFormat) {
			t.Errorf("%s: err = %v, want ErrFormat", name, err)
		}
		if IsEncrypted(enc) {
			t.Errorf("%s: IsEncrypted = true", name)
		}
	}
	if !IsEncrypted(v.enc) {
		t.Error("IsEncrypted(valid) = false")
	}
}

func fixed(pw string) func() (string, error) { return func() (string, error) { return pw, nil } }

func TestResolveReplacesValuesInText(t *testing.T) {
	a, b := javaVectors[0], javaVectors[6] // hunter22, sixteen-bytes-ok
	in := "sftp:host/dir?password=" + a.enc + "&x=" + b.enc + "&y=ENC(not-a-value)&z=1"
	got, err := Resolve(in, fixed("vector-password-1"))
	want := "sftp:host/dir?password=hunter22&x=sixteen-bytes-ok&y=ENC(not-a-value)&z=1"
	if err != nil || got != want {
		t.Fatalf("Resolve = %q, %v; want %q", got, err, want)
	}
}

func TestResolveAsksForThePasswordOnlyWhenNeeded(t *testing.T) {
	calls := 0
	ask := func() (string, error) { calls++; return "vector-password-1", nil }
	for _, s := range []string{"", "plain", "ENC(abc)", "has ENC( in it", `{"a": "ENC(abc)"}`} {
		if got, err := Resolve(s, ask); err != nil || got != s {
			t.Errorf("Resolve(%q) = %q, %v", s, got, err)
		}
	}
	if calls != 0 {
		t.Fatalf("the password was asked for %d times for text without a value", calls)
	}
	in := javaVectors[0].enc + " " + javaVectors[6].enc
	if _, err := Resolve(in, ask); err != nil || calls != 1 {
		t.Fatalf("two values: err = %v, password asked %d times, want once", err, calls)
	}
}

func TestResolveErrors(t *testing.T) {
	enc := javaVectors[0].enc
	if _, err := Resolve("a "+enc, func() (string, error) { return "", ErrNoPassword }); !errors.Is(err, ErrNoPassword) {
		t.Errorf("no password: %v", err)
	}
	_, err := Resolve("a "+enc, fixed("wrong"))
	if !errors.Is(err, ErrDecrypt) {
		t.Errorf("wrong password: %v", err)
	}
	if err != nil && (strings.Contains(err.Error(), enc) || strings.Contains(err.Error(), "MUIgE3")) {
		t.Errorf("the error shows the value: %v", err)
	}
}

func TestResolveKeepsJSONValid(t *testing.T) {
	tricky := javaVectors[5] // p"a\ss{w}ord=&
	in := `[{"name":"password","value":"` + tricky.enc + `","language":"constant"},{"n":1.50,"big":12345678901234567890,"html":"<a&b>"}]`
	got, err := Resolve(in, fixed(tricky.password))
	if err != nil {
		t.Fatal(err)
	}
	var doc []map[string]any
	if err := json.Unmarshal([]byte(got), &doc); err != nil {
		t.Fatalf("the result is not JSON: %v\n%s", err, got)
	}
	if doc[0]["value"] != tricky.plain {
		t.Errorf("value = %q, want %q", doc[0]["value"], tricky.plain)
	}
	for _, keep := range []string{"1.50", "12345678901234567890", "<a&b>"} {
		if !strings.Contains(got, keep) {
			t.Errorf("%q was changed:\n%s", keep, got)
		}
	}
	// JSON in which nothing is replaced is returned byte for byte.
	same := `{ "a" :  [1, 2],  "b": "ENC(abc)" }`
	if got, err := Resolve(same, fixed("pw")); err != nil || got != same {
		t.Errorf("got %q, %v", got, err)
	}
}

func TestPasswordFromTheEnvironment(t *testing.T) {
	t.Setenv(PasswordEnv, "") // restored when the test ends
	os.Unsetenv(PasswordEnv)
	if _, err := Password(); !errors.Is(err, ErrNoPassword) {
		t.Errorf("unset: %v", err)
	}
	t.Setenv(PasswordEnv, "")
	if _, err := Password(); !errors.Is(err, ErrNoPassword) {
		t.Errorf("empty: %v", err)
	}
	t.Setenv(PasswordEnv, "from env")
	if pw, err := Password(); err != nil || pw != "from env" {
		t.Errorf("env: %q, %v", pw, err)
	}

	os.Unsetenv(PasswordEnv)
	path := filepath.Join(t.TempDir(), "pw")
	if err := os.WriteFile(path, []byte("from file\r\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(PasswordEnv+"_FILE", path)
	if pw, err := Password(); err != nil || pw != "from file" {
		t.Errorf("file: %q, %v", pw, err)
	}
}
