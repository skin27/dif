package impl

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"

	"dif/keystore"
)

// The keys and certificates of an AS2 step are options whose value says where
// they are, one of:
//
//   - PEM text (it contains -----BEGIN), with CERTIFICATE blocks and a PRIVATE
//     KEY (PKCS #8) or RSA PRIVATE KEY (PKCS #1) block
//   - an http:// or https:// address that answers with PEM, as the platform's
//     key service does; it is read when the key is first needed
//   - the path of a PEM file, or of a PKCS #12 keystore (.p12 or .pfx) opened
//     with the option password
//
// An encrypted PEM key is not supported (use a PKCS #12 keystore).

// keyMaterial is what a key option holds.
type keyMaterial struct {
	key   *rsa.PrivateKey
	certs []*x509.Certificate
}

// parseKeyMaterial reads PEM text.
func parseKeyMaterial(data []byte) (keyMaterial, error) {
	var m keyMaterial
	for {
		var block *pem.Block
		block, data = pem.Decode(data)
		if block == nil {
			break
		}
		switch {
		case block.Type == "CERTIFICATE":
			c, err := x509.ParseCertificate(block.Bytes)
			if err != nil {
				return m, fmt.Errorf("certificate: %w", err)
			}
			m.certs = append(m.certs, c)
		case strings.HasPrefix(block.Type, "ENCRYPTED") || block.Headers["Proc-Type"] != "":
			return m, errors.New("an encrypted private key is not supported; use a PKCS #12 keystore")
		case block.Type == "PRIVATE KEY":
			k, err := x509.ParsePKCS8PrivateKey(block.Bytes)
			if err != nil {
				return m, fmt.Errorf("private key: %w", err)
			}
			rk, ok := k.(*rsa.PrivateKey)
			if !ok {
				return m, errors.New("only RSA keys are supported")
			}
			m.key = rk
		case block.Type == "RSA PRIVATE KEY":
			k, err := x509.ParsePKCS1PrivateKey(block.Bytes)
			if err != nil {
				return m, fmt.Errorf("private key: %w", err)
			}
			m.key = k
		}
	}
	if m.key == nil && len(m.certs) == 0 {
		return m, errors.New("no certificate or private key found (PEM text is expected)")
	}
	return m, nil
}

// ownCertificate is the certificate that belongs to key, the first of the
// chain whose public key it is.
func (m keyMaterial) ownCertificate() *x509.Certificate {
	if m.key == nil {
		return nil
	}
	for _, c := range m.certs {
		if pub, ok := c.PublicKey.(*rsa.PublicKey); ok && pub.Equal(&m.key.PublicKey) {
			return c
		}
	}
	return nil
}

// loadKeyMaterial reads what ref points to. client fetches addresses.
func loadKeyMaterial(ctx context.Context, ref, password string, client *http.Client) (keyMaterial, error) {
	ref = strings.TrimSpace(ref)
	var data []byte
	switch {
	case strings.Contains(ref, "-----BEGIN"):
		data = []byte(ref)
	case strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "https://"):
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, ref, nil)
		if err != nil {
			return keyMaterial{}, err
		}
		resp, err := client.Do(req)
		if err != nil {
			return keyMaterial{}, redactURL(ref, err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return keyMaterial{}, fmt.Errorf("%s: %s", redactQuery(ref), resp.Status)
		}
		if data, err = io.ReadAll(io.LimitReader(resp.Body, 1<<20)); err != nil {
			return keyMaterial{}, err
		}
	default:
		var err error
		if data, err = os.ReadFile(ref); err != nil {
			return keyMaterial{}, err
		}
		if !strings.Contains(string(data), "-----BEGIN") { // a PKCS #12 keystore
			keys, certs, err := keystore.Decode(data, password)
			if err != nil {
				return keyMaterial{}, fmt.Errorf("%s: %w", ref, err)
			}
			m := keyMaterial{certs: certs}
			for _, k := range keys {
				if rk, ok := k.(*rsa.PrivateKey); ok {
					m.key = rk
					break
				}
			}
			if m.key == nil && len(certs) == 0 {
				return m, fmt.Errorf("%s: no RSA key or certificate", ref)
			}
			return m, nil
		}
	}
	m, err := parseKeyMaterial(data)
	if err != nil {
		return m, fmt.Errorf("%s: %w", redactQuery(ref), err)
	}
	return m, nil
}

// redactQuery leaves the query of an address out of an error: it can hold the
// names of keys.
func redactQuery(ref string) string {
	if i := strings.IndexByte(ref, '?'); i >= 0 {
		return ref[:i] + "?..."
	}
	return ref
}

func redactURL(ref string, err error) error {
	return errors.New(strings.ReplaceAll(err.Error(), ref, redactQuery(ref)))
}

// lazyKeys reads key material when it is first needed, and keeps it.
type lazyKeys struct {
	ref, password string
	client        *http.Client

	mu   sync.Mutex
	done bool
	m    keyMaterial
}

func (l *lazyKeys) get(ctx context.Context) (keyMaterial, error) {
	if l.ref == "" {
		return keyMaterial{}, nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.done {
		return l.m, nil
	}
	m, err := loadKeyMaterial(ctx, l.ref, l.password, l.client)
	if err != nil {
		return keyMaterial{}, err
	}
	l.m, l.done = m, true
	return m, nil
}

// as2Hash returns the digest for a signing algorithm name such as SHA256WITHRSA.
func as2Hash(name string) (cmsHash, error) {
	n := strings.ToUpper(strings.NewReplacer("-", "", "_", "").Replace(strings.TrimSpace(name)))
	n = strings.TrimSuffix(strings.TrimSuffix(n, "WITHRSAENCRYPTION"), "WITHRSA")
	if h, ok := cmsHashes[n]; ok {
		return h, nil
	}
	return cmsHash{}, fmt.Errorf("signing algorithm %q is not one of SHA1WITHRSA, SHA256WITHRSA, SHA384WITHRSA, SHA512WITHRSA", name)
}

// as2Cipher returns the content encryption algorithm for a name such as AES128_CBC.
func as2Cipher(name string) (cmsCipher, error) {
	n := strings.ToUpper(strings.ReplaceAll(strings.TrimSpace(name), "-", "_"))
	switch n {
	case "3DES", "DES_EDE3", "TRIPLEDES", "TRIPLE_DES":
		n = "DES_EDE3_CBC"
	case "AES128", "AES192", "AES256":
		n += "_CBC"
	}
	if c, ok := cmsCiphers[n]; ok {
		return c, nil
	}
	return cmsCipher{}, fmt.Errorf("encrypting algorithm %q is not one of AES128_CBC, AES192_CBC, AES256_CBC, DES_EDE3_CBC", name)
}

var _ crypto.Signer = (*rsa.PrivateKey)(nil)
