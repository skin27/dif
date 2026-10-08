// Package secret reads secrets from the environment and decrypts the ENC(...)
// values that flows hold in place of passwords, tokens and API keys.
//
// The format is that of the Java EncryptionUtil of Assimbly: ENC(salt|iv|cipher),
// each part in base64. The key is derived from the password and the salt with
// PBKDF2WithHmacSHA1 (10000 iterations, 256 bits), and the text, in UTF-8, is
// encrypted with AES in CBC mode with PKCS#5 padding. The format has no
// integrity check, as in the original: a wrong password almost always fails
// on the padding, but one time in 256 it gives garbage.
package secret

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
)

// PasswordEnv is the environment variable with the password that ENC(...)
// values are encrypted with. PasswordEnv+"_FILE" names a file with it.
const PasswordEnv = "DIF_ENCRYPTION_PASSWORD"

const (
	iterations = 10000
	keyBytes   = 32
	ivBytes    = aes.BlockSize
	saltBytes  = 16
)

// ErrNoPassword is returned when a value is encrypted and no password is set.
var ErrNoPassword = fmt.Errorf("set %s (or %s_FILE) to the password the value was encrypted with", PasswordEnv, PasswordEnv)

// ErrFormat is returned for text that is not an ENC(salt|iv|cipher) value.
var ErrFormat = errors.New("not an ENC(salt|iv|cipher) value")

// ErrDecrypt is returned when a value does not decrypt. The usual cause is a wrong password.
var ErrDecrypt = errors.New("cannot decrypt the value: the password is wrong, or the value is damaged")

var encRe = regexp.MustCompile(`ENC\([^)]*\)`)

type parts struct{ salt, iv, data []byte }

// parse splits an ENC(salt|iv|cipher) value. The iv must be one AES block and the
// cipher whole blocks, so that text that merely looks like ENC(...) is not taken for a value.
func parse(v string) (parts, error) {
	inner, ok := strings.CutPrefix(v, "ENC(")
	if !ok || !strings.HasSuffix(inner, ")") {
		return parts{}, ErrFormat
	}
	f := strings.Split(strings.TrimSuffix(inner, ")"), "|")
	if len(f) != 3 {
		return parts{}, ErrFormat
	}
	var p parts
	var err [3]error
	p.salt, err[0] = base64.StdEncoding.DecodeString(f[0])
	p.iv, err[1] = base64.StdEncoding.DecodeString(f[1])
	p.data, err[2] = base64.StdEncoding.DecodeString(f[2])
	if err[0] != nil || err[1] != nil || err[2] != nil || len(p.salt) == 0 || len(p.iv) != ivBytes || len(p.data) == 0 || len(p.data)%aes.BlockSize != 0 {
		return parts{}, ErrFormat
	}
	return p, nil
}

// IsEncrypted reports whether v is an ENC(salt|iv|cipher) value.
func IsEncrypted(v string) bool {
	_, err := parse(v)
	return err == nil
}

func block(password string, salt []byte) (cipher.Block, error) {
	key, err := pbkdf2.Key(sha1.New, password, salt, iterations, keyBytes)
	if err != nil {
		return nil, err
	}
	return aes.NewCipher(key)
}

// Decrypt decrypts an ENC(salt|iv|cipher) value.
func Decrypt(password, v string) (string, error) {
	p, err := parse(v)
	if err != nil {
		return "", err
	}
	b, err := block(password, p.salt)
	if err != nil {
		return "", err
	}
	plain := make([]byte, len(p.data))
	cipher.NewCBCDecrypter(b, p.iv).CryptBlocks(plain, p.data)
	pad := int(plain[len(plain)-1])
	if pad < 1 || pad > aes.BlockSize || pad > len(plain) {
		return "", ErrDecrypt
	}
	for _, c := range plain[len(plain)-pad:] {
		if int(c) != pad {
			return "", ErrDecrypt
		}
	}
	return string(plain[:len(plain)-pad]), nil
}

// Encrypt encrypts text with a random salt and iv, and returns an ENC(salt|iv|cipher)
// value. Like the Java EncryptionUtil, it returns a value that is already encrypted as it is.
func Encrypt(password, plain string) (string, error) {
	if strings.HasPrefix(plain, "ENC(") && strings.HasSuffix(plain, ")") {
		return plain, nil
	}
	salt, iv := make([]byte, saltBytes), make([]byte, ivBytes)
	if _, err := io.ReadFull(rand.Reader, salt); err != nil {
		return "", err
	}
	if _, err := io.ReadFull(rand.Reader, iv); err != nil {
		return "", err
	}
	return EncryptWith(password, salt, iv, plain)
}

// EncryptWith encrypts text with the given salt and iv. The same arguments give the
// same value, which fixtures and tests need; use Encrypt otherwise.
func EncryptWith(password string, salt, iv []byte, plain string) (string, error) {
	if len(iv) != ivBytes || len(salt) == 0 {
		return "", errors.New("the iv must be 16 bytes and the salt must not be empty")
	}
	b, err := block(password, salt)
	if err != nil {
		return "", err
	}
	pad := aes.BlockSize - len(plain)%aes.BlockSize
	data := append([]byte(plain), bytes.Repeat([]byte{byte(pad)}, pad)...)
	cipher.NewCBCEncrypter(b, iv).CryptBlocks(data, data)
	enc := base64.StdEncoding.EncodeToString
	return "ENC(" + enc(salt) + "|" + enc(iv) + "|" + enc(data) + ")", nil
}

// Resolve replaces every ENC(salt|iv|cipher) value in s with its plain text, wherever
// it is in s. Text that looks like ENC(...) but is not a value is left as it is. password is called,
// once, when the first value is found; its error is returned as it is.
//
// If s is JSON (an object or an array), the values are replaced inside its strings,
// so that a plain text with a quote or a backslash cannot break the JSON.
func Resolve(s string, password func() (string, error)) (string, error) {
	if !strings.Contains(s, "ENC(") {
		return s, nil
	}
	var pw *string
	get := func() (string, error) {
		if pw == nil {
			p, err := password()
			if err != nil {
				return "", err
			}
			pw = &p
		}
		return *pw, nil
	}
	if t := strings.TrimSpace(s); (strings.HasPrefix(t, "{") || strings.HasPrefix(t, "[")) && json.Valid([]byte(t)) {
		return resolveJSON(t, s, get)
	}
	return resolveText(s, get)
}

func resolveText(s string, password func() (string, error)) (string, error) {
	var out strings.Builder
	last := 0
	for _, m := range encRe.FindAllStringIndex(s, -1) {
		v := s[m[0]:m[1]]
		if !IsEncrypted(v) {
			continue
		}
		pw, err := password()
		if err != nil {
			return "", err
		}
		plain, err := Decrypt(pw, v)
		if err != nil {
			return "", err
		}
		out.WriteString(s[last:m[0]])
		out.WriteString(plain)
		last = m[1]
	}
	if last == 0 {
		return s, nil
	}
	out.WriteString(s[last:])
	return out.String(), nil
}

func resolveJSON(trimmed, original string, password func() (string, error)) (string, error) {
	dec := json.NewDecoder(strings.NewReader(trimmed))
	dec.UseNumber()
	var doc any
	if err := dec.Decode(&doc); err != nil {
		return original, nil
	}
	changed := false
	var walk func(v any) (any, error)
	walk = func(v any) (any, error) {
		switch x := v.(type) {
		case string:
			r, err := resolveText(x, password)
			changed = changed || r != x
			return r, err
		case []any:
			for i := range x {
				r, err := walk(x[i])
				if err != nil {
					return nil, err
				}
				x[i] = r
			}
		case map[string]any:
			for k := range x {
				r, err := walk(x[k])
				if err != nil {
					return nil, err
				}
				x[k] = r
			}
		}
		return v, nil
	}
	doc, err := walk(doc)
	if err != nil {
		return "", err
	}
	if !changed {
		return original, nil
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(doc); err != nil {
		return "", err
	}
	return strings.TrimSuffix(b.String(), "\n"), nil
}

// Password returns the password for ENC(...) values from the environment.
func Password() (string, error) {
	pw, ok, err := Env(PasswordEnv)
	if err != nil {
		return "", err
	}
	if !ok || pw == "" {
		return "", ErrNoPassword
	}
	return pw, nil
}

// Env preserves direct environment precedence, including an explicitly empty
// value. Mounted secret files (name+"_FILE") may end in one LF or CRLF.
func Env(name string) (string, bool, error) {
	if value, ok := os.LookupEnv(name); ok {
		return value, true, nil
	}
	path, ok := os.LookupEnv(name + "_FILE")
	if !ok {
		return "", false, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return "", true, fmt.Errorf("cannot read %s_FILE", name)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return "", true, fmt.Errorf("%s_FILE must name a regular file", name)
	}
	data, err := io.ReadAll(io.LimitReader(f, (64<<10)+1))
	if err != nil || len(data) > 64<<10 {
		return "", true, fmt.Errorf("cannot read %s_FILE within 64 KiB limit", name)
	}
	value := string(data)
	if strings.HasSuffix(value, "\n") {
		value = strings.TrimSuffix(strings.TrimSuffix(value, "\n"), "\r")
	}
	return value, true, nil
}
