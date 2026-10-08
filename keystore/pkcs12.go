// Package keystore reads PKCS#12 (.p12) keystores with the standard library
// only: a server identity (private key and certificate chain) or a trust
// store (certificates).
//
// It supports the format modern tools write by default (Java keytool since
// JDK 18, OpenSSL 3): PBES2 encryption with PBKDF2 (HMAC-SHA1 or HMAC-SHA256)
// and AES-CBC, and an HMAC-SHA1 or HMAC-SHA256 integrity MAC. Legacy
// encryption (3DES, RC2) is rejected with an error.
package keystore

import (
	"bytes"
	"crypto"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"errors"
	"fmt"
	"hash"
	"os"
	"unicode/utf16"
)

// ErrWrongPassword is returned when the password does not open the keystore.
var ErrWrongPassword = errors.New("wrong password or corrupt keystore")

var (
	oidData                = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 7, 1}
	oidEncryptedData       = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 7, 6}
	oidKeyBag              = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 12, 10, 1, 1}
	oidShroudedKeyBag      = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 12, 10, 1, 2}
	oidCertBag             = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 12, 10, 1, 3}
	oidX509Certificate     = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 22, 1}
	oidPBES2               = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 5, 13}
	oidPBKDF2              = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 5, 12}
	oidHMACWithSHA1        = asn1.ObjectIdentifier{1, 2, 840, 113549, 2, 7}
	oidHMACWithSHA256      = asn1.ObjectIdentifier{1, 2, 840, 113549, 2, 9}
	oidSHA1                = asn1.ObjectIdentifier{1, 3, 14, 3, 2, 26}
	oidSHA256              = asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 2, 1}
	oidAES128CBC           = asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 1, 2}
	oidAES192CBC           = asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 1, 22}
	oidAES256CBC           = asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 1, 42}
	maxIterations          = 10_000_000 // guards against keystores that would take forever to open
	errUnsupportedEncoding = errors.New("unsupported encryption; re-export the keystore with AES (PBES2), e.g. keytool on JDK 18+ or OpenSSL 3")
)

type pfx struct {
	Version  int
	AuthSafe contentInfo
	MacData  macData `asn1:"optional"`
}

type contentInfo struct {
	ContentType asn1.ObjectIdentifier
	Content     asn1.RawValue `asn1:"tag:0,explicit,optional"`
}

type macData struct {
	Mac        digestInfo
	MacSalt    []byte
	Iterations int `asn1:"optional,default:1"`
}

type digestInfo struct {
	Algorithm pkix.AlgorithmIdentifier
	Digest    []byte
}

type encryptedData struct {
	Version              int
	EncryptedContentInfo encryptedContentInfo
}

type encryptedContentInfo struct {
	ContentType                asn1.ObjectIdentifier
	ContentEncryptionAlgorithm pkix.AlgorithmIdentifier
	EncryptedContent           []byte `asn1:"tag:0,optional"`
}

type safeBag struct {
	ID         asn1.ObjectIdentifier
	Value      asn1.RawValue `asn1:"tag:0,explicit"`
	Attributes []attribute   `asn1:"set,optional"`
}

type attribute struct {
	ID    asn1.ObjectIdentifier
	Value asn1.RawValue `asn1:"set"`
}

type encryptedPrivateKeyInfo struct {
	Algorithm     pkix.AlgorithmIdentifier
	EncryptedData []byte
}

type certBag struct {
	ID   asn1.ObjectIdentifier
	Data []byte `asn1:"tag:0,explicit"`
}

type pbes2Params struct {
	KDF              pkix.AlgorithmIdentifier
	EncryptionScheme pkix.AlgorithmIdentifier
}

type pbkdf2Params struct {
	Salt       []byte
	Iterations int
	KeyLength  int                      `asn1:"optional"`
	PRF        pkix.AlgorithmIdentifier `asn1:"optional"`
}

// Decode parses a PKCS#12 keystore, verifying its MAC with password, and
// returns the private keys and certificates it holds.
func Decode(data []byte, password string) (keys []crypto.PrivateKey, certs []*x509.Certificate, err error) {
	var p pfx
	if rest, err := asn1.Unmarshal(data, &p); err != nil {
		return nil, nil, fmt.Errorf("not a PKCS#12 keystore: %w", err)
	} else if len(rest) > 0 {
		return nil, nil, errors.New("not a PKCS#12 keystore: trailing data")
	}
	if !p.AuthSafe.ContentType.Equal(oidData) {
		return nil, nil, errors.New("unsupported keystore: only password integrity is supported")
	}
	var authSafe []byte
	if _, err := asn1.Unmarshal(p.AuthSafe.Content.Bytes, &authSafe); err != nil {
		return nil, nil, fmt.Errorf("keystore: %w", err)
	}
	if len(p.MacData.Mac.Algorithm.Algorithm) > 0 {
		if err := verifyMAC(p.MacData, authSafe, password); err != nil {
			return nil, nil, err
		}
	}

	var infos []contentInfo
	if _, err := asn1.Unmarshal(authSafe, &infos); err != nil {
		return nil, nil, fmt.Errorf("keystore: %w", err)
	}
	for _, ci := range infos {
		var contents []byte
		switch {
		case ci.ContentType.Equal(oidData):
			if _, err := asn1.Unmarshal(ci.Content.Bytes, &contents); err != nil {
				return nil, nil, fmt.Errorf("keystore: %w", err)
			}
		case ci.ContentType.Equal(oidEncryptedData):
			var ed encryptedData
			if _, err := asn1.Unmarshal(ci.Content.Bytes, &ed); err != nil {
				return nil, nil, fmt.Errorf("keystore: %w", err)
			}
			eci := ed.EncryptedContentInfo
			if contents, err = decrypt(eci.ContentEncryptionAlgorithm, eci.EncryptedContent, password); err != nil {
				return nil, nil, err
			}
		default:
			return nil, nil, fmt.Errorf("keystore: unsupported content type %v", ci.ContentType)
		}

		var bags []safeBag
		if _, err := asn1.Unmarshal(contents, &bags); err != nil {
			return nil, nil, fmt.Errorf("keystore: %w", err)
		}
		for _, bag := range bags {
			switch {
			case bag.ID.Equal(oidCertBag):
				var cb certBag
				if _, err := asn1.Unmarshal(bag.Value.Bytes, &cb); err != nil {
					return nil, nil, fmt.Errorf("keystore: certificate: %w", err)
				}
				if !cb.ID.Equal(oidX509Certificate) {
					continue // e.g. SDSI certificates, which TLS cannot use
				}
				cert, err := x509.ParseCertificate(cb.Data)
				if err != nil {
					return nil, nil, fmt.Errorf("keystore: certificate: %w", err)
				}
				certs = append(certs, cert)
			case bag.ID.Equal(oidShroudedKeyBag):
				var epki encryptedPrivateKeyInfo
				if _, err := asn1.Unmarshal(bag.Value.Bytes, &epki); err != nil {
					return nil, nil, fmt.Errorf("keystore: private key: %w", err)
				}
				der, err := decrypt(epki.Algorithm, epki.EncryptedData, password)
				if err != nil {
					return nil, nil, err
				}
				key, err := x509.ParsePKCS8PrivateKey(der)
				if err != nil {
					return nil, nil, fmt.Errorf("keystore: private key: %w", err)
				}
				keys = append(keys, key)
			case bag.ID.Equal(oidKeyBag):
				key, err := x509.ParsePKCS8PrivateKey(bag.Value.Bytes)
				if err != nil {
					return nil, nil, fmt.Errorf("keystore: private key: %w", err)
				}
				keys = append(keys, key)
			}
		}
	}
	return keys, certs, nil
}

// LoadIdentity reads a keystore holding one private key and returns it with
// its certificate chain, the key's own certificate first.
func LoadIdentity(path, password string) (tls.Certificate, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return tls.Certificate{}, err
	}
	keys, certs, err := Decode(data, password)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("%s: %w", path, err)
	}
	if len(keys) != 1 {
		return tls.Certificate{}, fmt.Errorf("%s: holds %d private keys, want 1", path, len(keys))
	}
	pub, ok := keys[0].(interface{ Public() crypto.PublicKey })
	if !ok {
		return tls.Certificate{}, fmt.Errorf("%s: unsupported private key type %T", path, keys[0])
	}

	id := tls.Certificate{PrivateKey: keys[0]}
	var chain [][]byte
	for _, c := range certs {
		if k, ok := c.PublicKey.(interface{ Equal(crypto.PublicKey) bool }); ok && k.Equal(pub.Public()) && id.Leaf == nil {
			id.Leaf = c
			id.Certificate = append([][]byte{c.Raw}, id.Certificate...)
		} else {
			chain = append(chain, c.Raw)
		}
	}
	if id.Leaf == nil {
		return tls.Certificate{}, fmt.Errorf("%s: no certificate for the private key", path)
	}
	id.Certificate = append(id.Certificate, chain...)
	return id, nil
}

// LoadTrustPool reads a keystore and returns a pool of all its certificates.
func LoadTrustPool(path, password string) (*x509.CertPool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	_, certs, err := Decode(data, password)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if len(certs) == 0 {
		return nil, fmt.Errorf("%s: holds no certificates", path)
	}
	pool := x509.NewCertPool()
	for _, c := range certs {
		pool.AddCert(c)
	}
	return pool, nil
}

// verifyMAC checks the keystore's integrity, which also checks the password.
func verifyMAC(md macData, content []byte, password string) error {
	var h func() hash.Hash
	switch alg := md.Mac.Algorithm.Algorithm; {
	case alg.Equal(oidSHA1):
		h = sha1.New
	case alg.Equal(oidSHA256):
		h = sha256.New
	default:
		return fmt.Errorf("unsupported keystore MAC algorithm %v", alg)
	}
	if md.Iterations < 1 || md.Iterations > maxIterations {
		return fmt.Errorf("keystore MAC: invalid iteration count %d", md.Iterations)
	}

	// An empty password is encoded as two zero bytes by most tools, but as no
	// bytes at all by some; accept both.
	candidates := [][]byte{bmpString(password)}
	if password == "" {
		candidates = append(candidates, nil)
	}
	for _, pw := range candidates {
		key := pkcs12KDF(h, 64, md.MacSalt, pw, md.Iterations, 3, h().Size())
		mac := hmac.New(h, key)
		mac.Write(content)
		if hmac.Equal(mac.Sum(nil), md.Mac.Digest) {
			return nil
		}
	}
	return ErrWrongPassword
}

// decrypt decrypts PBES2 (PBKDF2 + AES-CBC) encrypted data.
func decrypt(alg pkix.AlgorithmIdentifier, data []byte, password string) ([]byte, error) {
	if !alg.Algorithm.Equal(oidPBES2) {
		return nil, fmt.Errorf("keystore: %w (algorithm %v)", errUnsupportedEncoding, alg.Algorithm)
	}
	var params pbes2Params
	if _, err := asn1.Unmarshal(alg.Parameters.FullBytes, &params); err != nil {
		return nil, fmt.Errorf("keystore: PBES2 parameters: %w", err)
	}
	if !params.KDF.Algorithm.Equal(oidPBKDF2) {
		return nil, fmt.Errorf("keystore: %w (key derivation %v)", errUnsupportedEncoding, params.KDF.Algorithm)
	}
	var kdf pbkdf2Params
	if _, err := asn1.Unmarshal(params.KDF.Parameters.FullBytes, &kdf); err != nil {
		return nil, fmt.Errorf("keystore: PBKDF2 parameters: %w", err)
	}
	if kdf.Iterations < 1 || kdf.Iterations > maxIterations {
		return nil, fmt.Errorf("keystore: invalid PBKDF2 iteration count %d", kdf.Iterations)
	}

	var prf func() hash.Hash
	switch {
	case len(kdf.PRF.Algorithm) == 0 || kdf.PRF.Algorithm.Equal(oidHMACWithSHA1):
		prf = sha1.New
	case kdf.PRF.Algorithm.Equal(oidHMACWithSHA256):
		prf = sha256.New
	default:
		return nil, fmt.Errorf("keystore: %w (PRF %v)", errUnsupportedEncoding, kdf.PRF.Algorithm)
	}

	var keyLen int
	switch enc := params.EncryptionScheme.Algorithm; {
	case enc.Equal(oidAES128CBC):
		keyLen = 16
	case enc.Equal(oidAES192CBC):
		keyLen = 24
	case enc.Equal(oidAES256CBC):
		keyLen = 32
	default:
		return nil, fmt.Errorf("keystore: %w (cipher %v)", errUnsupportedEncoding, enc)
	}
	var iv []byte
	if _, err := asn1.Unmarshal(params.EncryptionScheme.Parameters.FullBytes, &iv); err != nil || len(iv) != aes.BlockSize {
		return nil, errors.New("keystore: invalid AES IV")
	}

	key, err := pbkdf2.Key(prf, password, kdf.Salt, kdf.Iterations, keyLen)
	if err != nil {
		return nil, fmt.Errorf("keystore: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	if len(data) == 0 || len(data)%aes.BlockSize != 0 {
		return nil, errors.New("keystore: encrypted data is not a whole number of blocks")
	}
	out := make([]byte, len(data))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(out, data)

	// Remove the PKCS#7 padding; bad padding means a wrong key.
	n := int(out[len(out)-1])
	if n == 0 || n > aes.BlockSize || !bytes.Equal(out[len(out)-n:], bytes.Repeat([]byte{byte(n)}, n)) {
		return nil, ErrWrongPassword
	}
	return out[:len(out)-n], nil
}

// bmpString encodes s as UTF-16 big endian with a terminating zero, as the
// PKCS#12 key derivation expects.
func bmpString(s string) []byte {
	u := utf16.Encode([]rune(s))
	b := make([]byte, 0, 2*len(u)+2)
	for _, r := range u {
		b = append(b, byte(r>>8), byte(r))
	}
	return append(b, 0, 0)
}

// pkcs12KDF derives size bytes of key material as in RFC 7292, appendix B.2.
// v is the hash's block size in bytes, id the purpose (3 for MAC keys).
func pkcs12KDF(h func() hash.Hash, v int, salt, password []byte, iterations int, id byte, size int) []byte {
	fill := func(b []byte) []byte {
		if len(b) == 0 {
			return nil
		}
		out := make([]byte, v*((len(b)+v-1)/v))
		for i := range out {
			out[i] = b[i%len(b)]
		}
		return out
	}
	d := bytes.Repeat([]byte{id}, v)
	in := append(fill(salt), fill(password)...)

	var key []byte
	for len(key) < size {
		hh := h()
		hh.Write(d)
		hh.Write(in)
		a := hh.Sum(nil)
		for i := 1; i < iterations; i++ {
			hh.Reset()
			hh.Write(a)
			a = hh.Sum(a[:0])
		}
		key = append(key, a...)

		// in_j = (in_j + b + 1) mod 2^(8v) for every v-byte block of in.
		b := fill(a)[:v]
		for j := 0; j < len(in); j += v {
			carry := 1
			for k := v - 1; k >= 0; k-- {
				sum := int(in[j+k]) + int(b[k]) + carry
				in[j+k] = byte(sum)
				carry = sum >> 8
			}
		}
	}
	return key[:size]
}
