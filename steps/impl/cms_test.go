package impl

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	mrand "math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// cmsParty is a key and its self-signed certificate.
type cmsParty struct {
	key  *rsa.PrivateKey
	cert *x509.Certificate
}

// 2048 bits keep the tests fast; generating takes a moment, so it is done once per name.
func newCMSParty(t testing.TB, name string) cmsParty {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: name, Organization: []string{"DIF test"}},
		NotBefore:    time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		BasicConstraintsValid: true, IsCA: true,
		SubjectKeyId: []byte{1, 2, 3, 4, byte(len(name))},
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return cmsParty{key, cert}
}

func (p cmsParty) pool() *x509.CertPool {
	pool := x509.NewCertPool()
	pool.AddCert(p.cert)
	return pool
}

func (p cmsParty) writePEM(t testing.TB, dir, base string) (certFile, keyFile string) {
	t.Helper()
	certFile, keyFile = filepath.Join(dir, base+".crt"), filepath.Join(dir, base+".key")
	if err := os.WriteFile(certFile, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: p.cert.Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(p.key)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		t.Fatal(err)
	}
	return
}

var cmsParties = map[string]cmsParty{}

func cmsTestParty(t testing.TB, name string) cmsParty {
	if p, ok := cmsParties[name]; ok {
		return p
	}
	p := newCMSParty(t, name)
	cmsParties[name] = p
	return p
}

func needOpenSSL(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("openssl")
	if err != nil {
		t.Skip("openssl is not installed")
	}
	return path
}

// openssl runs it and returns its output; a failure fails the test.
func openssl(t *testing.T, args ...string) []byte {
	t.Helper()
	out, err := exec.Command(needOpenSSL(t), args...).CombinedOutput()
	if err != nil {
		t.Fatalf("openssl %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

func TestCMSSignAndVerify(t *testing.T) {
	alice, bob := cmsTestParty(t, "alice"), cmsTestParty(t, "bob")
	content := []byte("Content-Type: application/edifact\r\n\r\nUNB+UNOC:3'UNZ+1'")
	for name, h := range cmsHashes {
		t.Run(name, func(t *testing.T) {
			sig, err := cmsSign(content, alice.cert, alice.key, h, nil)
			if err != nil {
				t.Fatal(err)
			}
			v, err := cmsVerify(sig, content, nil, nil)
			if err != nil || !v.signer.Equal(alice.cert) || v.hash.micalg != h.micalg {
				t.Fatalf("verify: %v, %+v", err, v)
			}
			if _, err := cmsVerify(sig, content, nil, alice.pool()); err != nil {
				t.Errorf("trusted: %v", err)
			}
			if _, err := cmsVerify(sig, content, nil, bob.pool()); err == nil || !strings.Contains(err.Error(), "not trusted") {
				t.Errorf("untrusted: %v", err)
			}
			if _, err := cmsVerify(sig, append(content[:len(content):len(content)], '!'), nil, nil); err == nil || !strings.Contains(err.Error(), "digest of the content") {
				t.Errorf("tampered content: %v", err)
			}
			bad := append([]byte(nil), sig...)
			bad[len(bad)-5] ^= 0xff // in the signature
			if _, err := cmsVerify(bad, content, nil, nil); err == nil {
				t.Error("a damaged signature was accepted")
			}
		})
	}

	// The certificate need not be in the message.
	sig, _ := cmsSign(content, alice.cert, alice.key, cmsHashes["SHA256"], nil)
	filler := bytes.Repeat([]byte{0x30, 0x00}, len(alice.cert.Raw)/2) // elements that are no certificates, as long as the certificate
	if len(alice.cert.Raw)%2 == 1 {
		filler = append([]byte{0x02, 0x01, 0x00}, filler[:len(filler)-2]...)
	}
	noCerts := bytes.Replace(sig, alice.cert.Raw, filler, 1)
	if _, err := cmsVerify(noCerts, content, nil, nil); err == nil || !strings.Contains(err.Error(), "not in the message") {
		t.Errorf("without the certificate: %v", err)
	}
	if _, err := cmsVerify(noCerts, content, []*x509.Certificate{alice.cert}, nil); err != nil {
		t.Errorf("with the certificate given: %v", err)
	}
	// Another party's signature does not pass as Alice's.
	other, _ := cmsSign(content, bob.cert, bob.key, cmsHashes["SHA256"], nil)
	if v, err := cmsVerify(other, content, nil, alice.pool()); err == nil {
		t.Errorf("bob's signature was trusted as alice's: %+v", v)
	}
}

func TestCMSEncryptAndDecrypt(t *testing.T) {
	alice, bob := cmsTestParty(t, "alice"), cmsTestParty(t, "bob")
	content := bytes.Repeat([]byte("secret EDI "), 1000)
	for name, c := range cmsCiphers {
		t.Run(name, func(t *testing.T) {
			enc, err := cmsEncrypt(content, alice.cert, c)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(enc, []byte("secret")) {
				t.Error("the content is in the clear")
			}
			got, err := cmsDecrypt(enc, alice.cert, alice.key)
			if err != nil || !bytes.Equal(got, content) {
				t.Fatalf("decrypt: %v (%d bytes)", err, len(got))
			}
			// Without the certificate the recipient is found by trying.
			if got, err := cmsDecrypt(enc, nil, alice.key); err != nil || !bytes.Equal(got, content) {
				t.Errorf("decrypt without the certificate: %v", err)
			}
			if _, err := cmsDecrypt(enc, bob.cert, bob.key); err == nil || !strings.Contains(err.Error(), "not encrypted for this certificate") {
				t.Errorf("decrypt by someone else: %v", err)
			}
			if _, err := cmsDecrypt(enc, nil, bob.key); err == nil {
				t.Error("decrypt by someone else, certificate unknown, worked")
			}
			// The same error for a key that does not unwrap and content that does not decrypt.
			bad := append([]byte(nil), enc...)
			bad[len(bad)-3] ^= 0xff
			if _, err := cmsDecrypt(bad, alice.cert, alice.key); err == nil {
				t.Error("damaged content decrypted")
			}
		})
	}
	// An empty content is padded to a block, and comes back empty.
	enc, _ := cmsEncrypt(nil, alice.cert, cmsCiphers["AES128_CBC"])
	if got, err := cmsDecrypt(enc, alice.cert, alice.key); err != nil || len(got) != 0 {
		t.Errorf("empty: %q, %v", got, err)
	}
}

func TestCMSCompress(t *testing.T) {
	content := bytes.Repeat([]byte("compress me "), 5000)
	z, err := cmsCompress(content)
	if err != nil {
		t.Fatal(err)
	}
	if len(z) > len(content)/10 {
		t.Errorf("%d bytes became %d", len(content), len(z))
	}
	if typ, err := cmsType(z); err != nil || !typ.Equal(oidCompressed) {
		t.Errorf("type = %v, %v", typ, err)
	}
	got, err := cmsDecompress(z)
	if err != nil || !bytes.Equal(got, content) {
		t.Errorf("decompress: %v", err)
	}
	if _, err := cmsDecompress(bytes.Replace(z, []byte{0x78, 0x9c}, []byte{0x00, 0x00}, 1)); err == nil {
		t.Error("damaged zlib stream decompressed")
	}
}

func TestCMSBER(t *testing.T) {
	// A constructed octet string of segments, and indefinite lengths, as Java writes them.
	octets := []byte{0x24, 0x80, 0x04, 0x03, 'a', 'b', 'c', 0x04, 0x02, 'd', 'e', 0x00, 0x00}
	e, rest, err := parseBER(append(octets, 0xff))
	if err != nil || len(rest) != 1 {
		t.Fatalf("parse: %v, rest %v", err, rest)
	}
	if got, err := e.octets(); err != nil || string(got) != "abcde" {
		t.Errorf("octets = %q, %v", got, err)
	}
	seq := []byte{0x30, 0x80, 0x02, 0x01, 0x05, 0x30, 0x80, 0x05, 0x00, 0x00, 0x00, 0x00, 0x00}
	e, _, err = parseBER(seq)
	if err != nil {
		t.Fatal(err)
	}
	kids, err := e.children()
	if err != nil || len(kids) != 2 {
		t.Fatalf("children: %v, %v", kids, err)
	}
	if n, err := kids[0].integer(); err != nil || n.Int64() != 5 {
		t.Errorf("integer = %v, %v", n, err)
	}
	if n, _ := (ber{class: 0, tag: 2, content: []byte{0xff}}).integer(); n.Int64() != -1 {
		t.Errorf("negative integer = %v", n)
	}
	for _, bad := range [][]byte{nil, {0x30}, {0x30, 0x05, 0x01}, {0x30, 0x80, 0x02, 0x01}, {0x02, 0x80, 0x00, 0x00}, {0x30, 0x84, 0xff, 0xff, 0xff, 0xff}, {0x1f}, {0x30, 0x85, 1, 2, 3, 4, 5}} {
		if _, _, err := parseBER(bad); err == nil {
			t.Errorf("% x parsed", bad)
		}
	}
	deep := bytes.Repeat([]byte{0x30, 0x80}, 100)
	if _, _, err := parseBER(deep); err == nil {
		t.Error("a very deep nesting parsed")
	}
}

// Whatever comes in, the readers return an error rather than panic or hang.
func TestCMSGarbage(t *testing.T) {
	alice := cmsTestParty(t, "alice")
	good, _ := cmsSign([]byte("x"), alice.cert, alice.key, cmsHashes["SHA256"], nil)
	enc, _ := cmsEncrypt([]byte("x"), alice.cert, cmsCiphers["AES128_CBC"])
	z, _ := cmsCompress([]byte("x"))
	rng := mrand.New(mrand.NewSource(1))
	for _, seed := range [][]byte{good, enc, z, {}, {0x30, 0x00}, bytes.Repeat([]byte{0xff}, 64)} {
		for range 300 {
			b := append([]byte(nil), seed...)
			for range 1 + rng.Intn(4) {
				if len(b) > 0 {
					b[rng.Intn(len(b))] = byte(rng.Intn(256))
				}
			}
			if len(b) > 0 && rng.Intn(3) == 0 {
				b = b[:rng.Intn(len(b))]
			}
			cmsVerify(b, []byte("x"), nil, nil)
			cmsDecrypt(b, alice.cert, alice.key)
			cmsDecrypt(b, nil, alice.key)
			cmsDecompress(b)
			cmsType(b)
		}
	}
}

// ---- against OpenSSL, which is what other AS2 software uses

func TestCMSWithOpenSSLSignatures(t *testing.T) {
	needOpenSSL(t)
	alice := cmsTestParty(t, "alice")
	dir := t.TempDir()
	certFile, keyFile := alice.writePEM(t, dir, "alice")
	content := []byte("Content-Type: application/edifact\r\n\r\nUNB+UNOC:3+SENDER+RECEIVER'UNZ+0+1'\r\n")
	data := filepath.Join(dir, "data")
	if err := os.WriteFile(data, content, 0o600); err != nil {
		t.Fatal(err)
	}

	for name, h := range cmsHashes {
		// ours, verified by openssl against the certificate as the only root
		sig, err := cmsSign(content, alice.cert, alice.key, h, nil)
		if err != nil {
			t.Fatal(err)
		}
		sigFile := filepath.Join(dir, "ours-"+name+".der")
		os.WriteFile(sigFile, sig, 0o600)
		out := openssl(t, "cms", "-verify", "-inform", "DER", "-in", sigFile, "-content", data, "-binary", "-CAfile", certFile, "-purpose", "any", "-out", os.DevNull)
		if !strings.Contains(string(out), "successful") {
			t.Errorf("%s: openssl says %s", name, out)
		}

		// openssl's, verified by us, in DER and in BER with indefinite lengths
		for _, stream := range []bool{false, true} {
			theirs := filepath.Join(dir, "theirs-"+name+".der")
			args := []string{"cms", "-sign", "-binary", "-in", data, "-signer", certFile, "-inkey", keyFile, "-outform", "DER", "-out", theirs, "-md", strings.ToLower(name)}
			if stream {
				args = append(args, "-stream")
			}
			openssl(t, args...)
			sig, _ := os.ReadFile(theirs)
			v, err := cmsVerify(sig, content, nil, alice.pool())
			if err != nil || !v.signer.Equal(alice.cert) {
				t.Errorf("%s (stream %v): openssl's signature: %v", name, stream, err)
			}
			if _, err := cmsVerify(sig, append(content[:len(content):len(content)], 'x'), nil, nil); err == nil {
				t.Errorf("%s: openssl's signature held for other content", name)
			}
		}
	}
}

func TestCMSWithOpenSSLEncryption(t *testing.T) {
	needOpenSSL(t)
	alice := cmsTestParty(t, "alice")
	dir := t.TempDir()
	certFile, keyFile := alice.writePEM(t, dir, "alice")
	content := bytes.Repeat([]byte("Content-Type: application/edifact\r\n\r\nUNB+UNOC:3'\r\n"), 200)
	data := filepath.Join(dir, "data")
	os.WriteFile(data, content, 0o600)

	for name, c := range cmsCiphers {
		// ours, decrypted by openssl
		enc, err := cmsEncrypt(content, alice.cert, c)
		if err != nil {
			t.Fatal(err)
		}
		encFile := filepath.Join(dir, "ours-"+name+".der")
		os.WriteFile(encFile, enc, 0o600)
		plain := openssl(t, "cms", "-decrypt", "-inform", "DER", "-in", encFile, "-recip", certFile, "-inkey", keyFile, "-binary")
		if !bytes.Equal(plain, content) {
			t.Errorf("%s: openssl decrypted to %d bytes, want %d", name, len(plain), len(content))
		}

		// openssl's, decrypted by us, in DER and BER
		flag := map[string]string{"AES128_CBC": "-aes128", "AES192_CBC": "-aes192", "AES256_CBC": "-aes256", "DES_EDE3_CBC": "-des3"}[name]
		for _, stream := range []bool{false, true} {
			theirs := filepath.Join(dir, "theirs-"+name+".der")
			args := []string{"cms", "-encrypt", "-binary", flag, "-in", data, "-outform", "DER", "-out", theirs}
			if stream {
				args = append(args, "-stream")
			}
			openssl(t, append(args, certFile)...)
			enc, _ := os.ReadFile(theirs)
			got, err := cmsDecrypt(enc, alice.cert, alice.key)
			if err != nil || !bytes.Equal(got, content) {
				t.Errorf("%s (stream %v): openssl's message: %v", name, stream, err)
			}
		}
	}
}

func TestCMSWithOpenSSLCompression(t *testing.T) {
	needOpenSSL(t)
	dir := t.TempDir()
	content := bytes.Repeat([]byte("compress "), 400)
	data, ours := filepath.Join(dir, "data"), filepath.Join(dir, "ours.der")
	os.WriteFile(data, content, 0o600)
	z, _ := cmsCompress(content)
	os.WriteFile(ours, z, 0o600)

	out, err := exec.Command("openssl", "cms", "-uncompress", "-inform", "DER", "-in", ours, "-binary").CombinedOutput()
	if err != nil && strings.Contains(string(out), "not supported") || err != nil && strings.Contains(string(out), "compression") {
		t.Skipf("this openssl cannot compress: %s", strings.TrimSpace(string(out)))
	}
	if err != nil || !bytes.Equal(out, content) {
		t.Errorf("openssl uncompress: %v, %d bytes\n%s", err, len(out), out[:min(len(out), 200)])
	}
	theirs := filepath.Join(dir, "theirs.der")
	openssl(t, "cms", "-compress", "-binary", "-in", data, "-outform", "DER", "-out", theirs)
	z, _ = os.ReadFile(theirs)
	if got, err := cmsDecompress(z); err != nil || !bytes.Equal(got, content) {
		t.Errorf("openssl's compressed data: %v", err)
	}
}
