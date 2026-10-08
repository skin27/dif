package keystore

// The keystores in testdata (password "changeit") were created with OpenSSL 3.2,
// whose defaults match Java keytool's: PBES2 (PBKDF2-HMAC-SHA256, AES-256-CBC)
// and an HMAC-SHA256 MAC.
//
//	MSYS_NO_PATHCONV=1 openssl req -x509 -newkey rsa:2048 -nodes -keyout key.pem -out cert.pem -days 36500 \
//	    -subj "/CN=localhost" -addext "subjectAltName=DNS:localhost,IP:127.0.0.1"
//	openssl pkcs12 -export -in cert.pem -inkey key.pem -name localhost -out server-identity.p12 -passout pass:changeit
//	openssl pkcs12 -export -nokeys -in cert.pem -jdktrust anyExtendedKeyUsage -out truststore.p12 -passout pass:changeit
//
// legacy-3des.p12 uses the legacy encryption DIF does not support:
//
//	openssl pkcs12 -export ... -certpbe PBE-SHA1-3DES -keypbe PBE-SHA1-3DES -macalg sha1 -out legacy-3des.p12

import (
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"errors"
	"hash"
	"os"
	"strings"
	"testing"
)

const password = "changeit"

func TestLoadIdentity(t *testing.T) {
	id, err := LoadIdentity("testdata/server-identity.p12", password)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := id.PrivateKey.(*rsa.PrivateKey); !ok {
		t.Errorf("private key = %T, want RSA", id.PrivateKey)
	}
	if id.Leaf == nil || id.Leaf.Subject.CommonName != "localhost" || len(id.Certificate) != 1 {
		t.Fatalf("leaf = %v, chain length %d; want the localhost certificate only", id.Leaf, len(id.Certificate))
	}
	if err := id.Leaf.VerifyHostname("127.0.0.1"); err != nil {
		t.Error(err)
	}
}

func TestLoadTrustPool(t *testing.T) {
	pool, err := LoadTrustPool("testdata/truststore.p12", password)
	if err != nil {
		t.Fatal(err)
	}
	id, err := LoadIdentity("testdata/server-identity.p12", password)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := id.Leaf.Verify(x509.VerifyOptions{Roots: pool, DNSName: "localhost"}); err != nil {
		t.Errorf("the identity is not trusted by the trust store: %v", err)
	}

	// A trust store has no keys, so it cannot serve as an identity.
	if _, err := LoadIdentity("testdata/truststore.p12", password); err == nil || !strings.Contains(err.Error(), "holds 0 private keys") {
		t.Errorf("identity from trust store: err = %v", err)
	}
}

func TestErrors(t *testing.T) {
	identity, err := os.ReadFile("testdata/server-identity.p12")
	if err != nil {
		t.Fatal(err)
	}
	corrupt := append([]byte(nil), identity...)
	corrupt[len(corrupt)/2] ^= 0xff
	legacy, err := os.ReadFile("testdata/legacy-3des.p12")
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		data []byte
		pass string
		want string
	}{
		{"wrong password", identity, "wrong", "wrong password or corrupt keystore"},
		{"empty password", identity, "", "wrong password or corrupt keystore"},
		{"corrupt", corrupt, password, "wrong password or corrupt keystore"},
		{"not a keystore", []byte("hello"), password, "not a PKCS#12 keystore"},
		{"legacy encryption", legacy, password, "re-export the keystore with AES"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := Decode(tt.data, tt.pass)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want containing %q", err, tt.want)
			}
		})
	}

	if _, err := LoadIdentity("testdata/missing.p12", password); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("missing file: err = %v", err)
	}
}

func TestPKCS12KDF(t *testing.T) {
	// A key that differs per purpose id, salt and iteration count.
	a := pkcs12KDF(sha256New, 64, []byte("salt"), bmpString("pw"), 1, 3, 32)
	for _, b := range [][]byte{
		pkcs12KDF(sha256New, 64, []byte("salt"), bmpString("pw"), 1, 1, 32),
		pkcs12KDF(sha256New, 64, []byte("salz"), bmpString("pw"), 1, 3, 32),
		pkcs12KDF(sha256New, 64, []byte("salt"), bmpString("pw"), 2, 3, 32),
	} {
		if string(a) == string(b) {
			t.Error("different inputs gave the same key")
		}
	}
	if long := pkcs12KDF(sha256New, 64, []byte("salt"), bmpString("pw"), 1, 3, 80); string(long[:32]) != string(a) {
		t.Error("a longer key does not start with the shorter one")
	}
}

// TestSecurityKeystores reads the keystores in ../security when their
// passwords are set, as dif does by default.
func TestSecurityKeystores(t *testing.T) {
	if pw, ok := os.LookupEnv("DIF_SERVER_IDENTITY_PASSWORD"); ok {
		if _, err := LoadIdentity("../security/server-identity.p12", pw); err != nil {
			t.Error(err)
		}
	} else {
		t.Log("DIF_SERVER_IDENTITY_PASSWORD not set; skipping security/server-identity.p12")
	}
	if pw, ok := os.LookupEnv("DIF_TRUSTSTORE_PASSWORD"); ok {
		if _, err := LoadTrustPool("../security/outbound-truststore.p12", pw); err != nil {
			t.Error(err)
		}
	} else {
		t.Log("DIF_TRUSTSTORE_PASSWORD not set; skipping security/outbound-truststore.p12")
	}
}

func sha256New() hash.Hash { return sha256.New() }
