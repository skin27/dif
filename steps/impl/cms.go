package impl

import (
	"bytes"
	"compress/zlib"
	"crypto"
	"crypto/aes"
	"crypto/cipher"
	"crypto/des"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/subtle"
	"crypto/x509"
	"encoding/asn1"
	"errors"
	"fmt"
	"hash"
	"io"
	"math/big"
	"sort"
	"time"
)

// Cryptographic Message Syntax (RFC 5652), as much as AS2 (RFC 4130) uses it,
// with the standard library only:
//
//   - SignedData, detached: one signer, an RSA key, SHA-1 or SHA-2, with the
//     signed attributes content type, signing time and message digest;
//   - EnvelopedData: key transport to RSA recipients (PKCS #1 v1.5), content
//     encryption with AES-128/192/256-CBC or 3DES-CBC;
//   - CompressedData (RFC 3274), zlib.
//
// What is read may be BER, with indefinite lengths and constructed octet
// strings, as Java's BouncyCastle writes it; what is written is DER.

var (
	oidData       = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 7, 1}
	oidSignedData = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 7, 2}
	oidEnveloped  = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 7, 3}
	oidCompressed = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 16, 1, 9}
	oidZlib       = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 16, 3, 8}

	oidAttrContentType   = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 3}
	oidAttrMessageDgst   = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 4}
	oidAttrSigningTime   = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 5}
	oidRSAEncryption     = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 1, 1}
	oidSHA1, oidSHA256   = asn1.ObjectIdentifier{1, 3, 14, 3, 2, 26}, asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 2, 1}
	oidSHA384, oidSHA512 = asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 2, 2}, asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 2, 3}

	oidDESEDE3CBC = asn1.ObjectIdentifier{1, 2, 840, 113549, 3, 7}
	oidAES128CBC  = asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 1, 2}
	oidAES192CBC  = asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 1, 22}
	oidAES256CBC  = asn1.ObjectIdentifier{2, 16, 840, 1, 101, 3, 4, 1, 42}
)

// cmsHash describes a digest: its OID, the OID of its RSA signature, its name
// in a micalg parameter (RFC 5751) and how to make it.
type cmsHash struct {
	hash    crypto.Hash
	digest  asn1.ObjectIdentifier
	sigAlg  asn1.ObjectIdentifier
	micalg  string
	newHash func() hash.Hash
}

var cmsHashes = map[string]cmsHash{
	"SHA1":   {crypto.SHA1, oidSHA1, asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 1, 5}, "sha-1", sha1.New},
	"SHA256": {crypto.SHA256, oidSHA256, asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 1, 11}, "sha-256", sha256.New},
	"SHA384": {crypto.SHA384, oidSHA384, asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 1, 12}, "sha-384", sha512.New384},
	"SHA512": {crypto.SHA512, oidSHA512, asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 1, 13}, "sha-512", sha512.New},
}

// cmsHashByOID finds a digest by its OID.
func cmsHashByOID(oid asn1.ObjectIdentifier) (cmsHash, bool) {
	for _, h := range cmsHashes {
		if h.digest.Equal(oid) {
			return h, true
		}
	}
	return cmsHash{}, false
}

// cmsHashByMicalg finds a digest by its name in a micalg parameter.
func cmsHashByMicalg(name string) (cmsHash, bool) {
	for _, h := range cmsHashes {
		if h.micalg == name {
			return h, true
		}
	}
	return cmsHash{}, false
}

func (h cmsHash) sum(data []byte) []byte {
	w := h.newHash()
	w.Write(data)
	return w.Sum(nil)
}

// ---- a small DER writer

func derLen(n int) []byte {
	if n < 0x80 {
		return []byte{byte(n)}
	}
	var b []byte
	for ; n > 0; n >>= 8 {
		b = append([]byte{byte(n)}, b...)
	}
	return append([]byte{0x80 | byte(len(b))}, b...)
}

func derTLV(tag byte, content []byte) []byte {
	out := append([]byte{tag}, derLen(len(content))...)
	return append(out, content...)
}

func derCat(parts ...[]byte) []byte { return bytes.Join(parts, nil) }
func derSeq(parts ...[]byte) []byte { return derTLV(0x30, derCat(parts...)) }
func derSet(parts ...[]byte) []byte { return derTLV(0x31, derCat(parts...)) }

// derSetOf is a SET OF, in the order DER wants.
func derSetOf(parts [][]byte) []byte {
	sorted := append([][]byte(nil), parts...)
	sort.Slice(sorted, func(i, j int) bool { return bytes.Compare(sorted[i], sorted[j]) < 0 })
	return derSet(sorted...)
}
func derOctets(b []byte) []byte { return derTLV(0x04, b) }
func derNull() []byte           { return []byte{0x05, 0x00} }
func derInt(n int) []byte       { return derTLV(0x02, serialDER(big.NewInt(int64(n)))) }

func derOID(oid asn1.ObjectIdentifier) []byte {
	b, err := asn1.Marshal(oid)
	if err != nil {
		panic(err) // the OIDs are constants
	}
	return b
}

// derExplicit is [n] EXPLICIT: a constructed context tag around content.
func derExplicit(n int, content []byte) []byte { return derTLV(0xA0|byte(n), content) }

func derAlg(oid asn1.ObjectIdentifier, params []byte) []byte {
	if params == nil {
		params = derNull()
	}
	return derSeq(derOID(oid), params)
}

// ---- a small BER reader

// ber is an element of a BER encoding.
type ber struct {
	class       int
	tag         int
	constructed bool
	content     []byte // definite: the content; indefinite: the children as one encoding, without the end marker
	full        []byte // the whole element as it was, for a signature to cover
}

// parseBER reads the first element of b and returns the rest.
func parseBER(b []byte) (ber, []byte, error) { return parseBERDepth(b, 0) }

func parseBERDepth(b []byte, depth int) (ber, []byte, error) {
	if depth > 40 {
		return ber{}, nil, errors.New("nested too deep")
	}
	if len(b) < 2 {
		return ber{}, nil, errors.New("truncated")
	}
	var e ber
	e.class = int(b[0] >> 6)
	e.constructed = b[0]&0x20 != 0
	e.tag = int(b[0] & 0x1f)
	i := 1
	if e.tag == 0x1f { // a tag in several bytes
		e.tag = 0
		for {
			if i >= len(b) || e.tag > 1<<20 {
				return ber{}, nil, errors.New("bad tag")
			}
			c := b[i]
			i++
			e.tag = e.tag<<7 | int(c&0x7f)
			if c&0x80 == 0 {
				break
			}
		}
	}
	if i >= len(b) {
		return ber{}, nil, errors.New("truncated")
	}
	l := int(b[i])
	i++
	switch {
	case l == 0x80: // indefinite: children up to the end marker
		if !e.constructed {
			return ber{}, nil, errors.New("indefinite length on a primitive")
		}
		start := i
		rest := b[i:]
		for {
			if len(rest) >= 2 && rest[0] == 0 && rest[1] == 0 {
				e.content = b[start : len(b)-len(rest)]
				e.full = b[:len(b)-len(rest)+2]
				return e, rest[2:], nil
			}
			var err error
			if _, rest, err = parseBERDepth(rest, depth+1); err != nil {
				return ber{}, nil, err
			}
		}
	case l > 0x80:
		n := l & 0x7f
		if n > 4 || i+n > len(b) {
			return ber{}, nil, errors.New("bad length")
		}
		l = 0
		for _, c := range b[i : i+n] {
			l = l<<8 | int(c)
		}
		i += n
	}
	if l < 0 || i+l > len(b) {
		return ber{}, nil, errors.New("truncated")
	}
	e.content, e.full = b[i:i+l], b[:i+l]
	return e, b[i+l:], nil
}

// children are the elements inside a constructed element.
func (e ber) children() ([]ber, error) {
	if !e.constructed {
		return nil, errors.New("not a constructed element")
	}
	var out []ber
	for rest := e.content; len(rest) > 0; {
		var c ber
		var err error
		if c, rest, err = parseBER(rest); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, nil
}

// octets are the bytes of an OCTET STRING, constructed or not.
func (e ber) octets() ([]byte, error) {
	if !e.constructed {
		return e.content, nil
	}
	kids, err := e.children()
	if err != nil {
		return nil, err
	}
	var out []byte
	for _, k := range kids {
		part, err := k.octets()
		if err != nil {
			return nil, err
		}
		out = append(out, part...)
	}
	return out, nil
}

func (e ber) isUniversal(tag int) bool { return e.class == 0 && e.tag == tag }

func (e ber) oid() (asn1.ObjectIdentifier, error) {
	var oid asn1.ObjectIdentifier
	if _, err := asn1.Unmarshal(e.full, &oid); err != nil || !e.isUniversal(6) {
		return nil, errors.New("not an object identifier")
	}
	return oid, nil
}

func (e ber) integer() (*big.Int, error) {
	if !e.isUniversal(2) || len(e.content) == 0 {
		return nil, errors.New("not an integer")
	}
	n := new(big.Int).SetBytes(e.content)
	if e.content[0]&0x80 != 0 {
		n.Sub(n, new(big.Int).Lsh(big.NewInt(1), uint(8*len(e.content))))
	}
	return n, nil
}

// contentInfo reads a ContentInfo, the content type and the content that
// follows it in [0].
func parseContentInfo(b []byte) (asn1.ObjectIdentifier, ber, error) {
	ci, _, err := parseBER(b)
	if err != nil || !ci.isUniversal(16) {
		return nil, ber{}, errors.New("not a CMS message")
	}
	kids, err := ci.children()
	if err != nil || len(kids) < 2 {
		return nil, ber{}, errors.New("not a CMS message")
	}
	oid, err := kids[0].oid()
	if err != nil {
		return nil, ber{}, err
	}
	inner, err := kids[1].children()
	if err != nil || len(inner) != 1 || kids[1].class != 2 || kids[1].tag != 0 {
		return nil, ber{}, errors.New("not a CMS message")
	}
	return oid, inner[0], nil
}

// ---- SignedData

// cmsSign signs content with the key of cert and returns a detached
// SignedData, a ContentInfo in DER. chain is what goes in the certificates
// field besides cert.
func cmsSign(content []byte, cert *x509.Certificate, key crypto.Signer, h cmsHash, chain []*x509.Certificate) ([]byte, error) {
	if _, ok := key.Public().(*rsa.PublicKey); !ok {
		return nil, errors.New("only RSA keys can sign")
	}
	digest := h.sum(content)
	attrs := [][]byte{
		derSeq(derOID(oidAttrContentType), derSet(derOID(oidData))),
		derSeq(derOID(oidAttrSigningTime), derSet(derTLV(0x17, []byte(time.Now().UTC().Format("060102150405Z"))))),
		derSeq(derOID(oidAttrMessageDgst), derSet(derOctets(digest))),
	}
	signedAttrs := derSetOf(attrs) // the signature covers this, with the tag of a SET
	signature, err := key.Sign(rand.Reader, h.sum(signedAttrs), h.hash)
	if err != nil {
		return nil, err
	}
	issuerAndSerial := derSeq(cert.RawIssuer, derTLV(0x02, serialDER(cert.SerialNumber)))
	signerInfo := derSeq(
		derInt(1), issuerAndSerial, derAlg(h.digest, nil),
		append([]byte{0xA0}, signedAttrs[1:]...), // [0] IMPLICIT instead of SET
		derAlg(h.sigAlg, nil), derOctets(signature),
	)
	certs := append([]byte(nil), cert.Raw...)
	for _, c := range chain {
		if !c.Equal(cert) {
			certs = append(certs, c.Raw...)
		}
	}
	signedData := derSeq(
		derInt(1), derSet(derAlg(h.digest, nil)), derSeq(derOID(oidData)),
		derTLV(0xA0, certs), derSet(signerInfo),
	)
	return derSeq(derOID(oidSignedData), derExplicit(0, signedData)), nil
}

// serialDER is a serial number as the content of an INTEGER.
func serialDER(n *big.Int) []byte {
	b := n.Bytes()
	if len(b) == 0 || b[0]&0x80 != 0 {
		b = append([]byte{0}, b...)
	}
	return b
}

// cmsVerified is the outcome of a verified signature.
type cmsVerified struct {
	signer *x509.Certificate
	hash   cmsHash
}

// cmsVerify checks a detached SignedData over content. The signer's
// certificate is in the message or one of known; if trusted is not nil it has
// to be one of those, or issued by one, or the signature is refused.
func cmsVerify(signedData, content []byte, known []*x509.Certificate, trusted *x509.CertPool) (cmsVerified, error) {
	oid, sd, err := parseContentInfo(signedData)
	if err != nil {
		return cmsVerified{}, err
	}
	if !oid.Equal(oidSignedData) {
		return cmsVerified{}, errors.New("not a SignedData")
	}
	parts, err := sd.children()
	if err != nil || len(parts) < 4 {
		return cmsVerified{}, errors.New("bad SignedData")
	}
	// version, digestAlgorithms, encapContentInfo, [0] certificates, [1] crls, signerInfos
	var certs []*x509.Certificate
	certs = append(certs, known...)
	var signerInfos ber
	for _, p := range parts[3:] {
		switch {
		case p.class == 2 && p.tag == 0 && p.constructed:
			for rest := p.content; len(rest) > 0; {
				var c ber
				if c, rest, err = parseBER(rest); err != nil {
					return cmsVerified{}, err
				}
				if cert, err := x509.ParseCertificate(c.full); err == nil {
					certs = append(certs, cert)
				}
			}
		case p.isUniversal(17):
			signerInfos = p
		}
	}
	infos, err := signerInfos.children()
	if err != nil || len(infos) == 0 {
		return cmsVerified{}, errors.New("no signer")
	}
	fields, err := infos[0].children()
	if err != nil || len(fields) < 5 {
		return cmsVerified{}, errors.New("bad SignerInfo")
	}
	// version, sid, digestAlgorithm, [0] signedAttrs, signatureAlgorithm, signature
	signer, err := findSigner(fields[1], certs)
	if err != nil {
		return cmsVerified{}, err
	}
	algParts, err := fields[2].children()
	if err != nil || len(algParts) == 0 {
		return cmsVerified{}, errors.New("bad digest algorithm")
	}
	digestOID, err := algParts[0].oid()
	if err != nil {
		return cmsVerified{}, err
	}
	h, ok := cmsHashByOID(digestOID)
	if !ok {
		return cmsVerified{}, fmt.Errorf("digest algorithm %v is not supported", digestOID)
	}
	i := 3
	var signedAttrs *ber
	if fields[i].class == 2 && fields[i].tag == 0 {
		signedAttrs = &fields[i]
		i++
	}
	if i+1 >= len(fields) {
		return cmsVerified{}, errors.New("bad SignerInfo")
	}
	signature, err := fields[i+1].octets()
	if err != nil {
		return cmsVerified{}, err
	}
	pub, ok := signer.PublicKey.(*rsa.PublicKey)
	if !ok {
		return cmsVerified{}, errors.New("only RSA signatures are supported")
	}

	signed := content
	if signedAttrs != nil {
		// The signature covers the attributes with the tag of a SET.
		covered := append([]byte{0x31}, signedAttrs.full[1:]...)
		attrs, err := signedAttrs.children()
		if err != nil {
			return cmsVerified{}, err
		}
		var messageDigest []byte
		for _, a := range attrs {
			kv, err := a.children()
			if err != nil || len(kv) != 2 {
				continue
			}
			if o, err := kv[0].oid(); err == nil && o.Equal(oidAttrMessageDgst) {
				vals, err := kv[1].children()
				if err != nil || len(vals) != 1 {
					return cmsVerified{}, errors.New("bad message digest attribute")
				}
				messageDigest, _ = vals[0].octets()
			}
		}
		if messageDigest == nil {
			return cmsVerified{}, errors.New("the signature has no message digest")
		}
		if subtle.ConstantTimeCompare(messageDigest, h.sum(content)) != 1 {
			return cmsVerified{}, errors.New("the digest of the content does not match the signature")
		}
		signed = covered
	}
	if err := rsa.VerifyPKCS1v15(pub, h.hash, h.sum(signed), signature); err != nil {
		return cmsVerified{}, errors.New("the signature is not valid")
	}
	if trusted != nil {
		inter := x509.NewCertPool()
		for _, c := range certs {
			inter.AddCert(c)
		}
		if _, err := signer.Verify(x509.VerifyOptions{Roots: trusted, Intermediates: inter, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny}}); err != nil {
			return cmsVerified{}, fmt.Errorf("the signer's certificate is not trusted: %w", err)
		}
	}
	return cmsVerified{signer, h}, nil
}

// findSigner finds the certificate a SignerInfo names, by issuer and serial
// number, or by subject key identifier.
func findSigner(sid ber, certs []*x509.Certificate) (*x509.Certificate, error) {
	if sid.isUniversal(16) {
		kids, err := sid.children()
		if err != nil || len(kids) != 2 {
			return nil, errors.New("bad signer identifier")
		}
		serial, err := kids[1].integer()
		if err != nil {
			return nil, err
		}
		for _, c := range certs {
			if bytes.Equal(c.RawIssuer, kids[0].full) && c.SerialNumber.Cmp(serial) == 0 {
				return c, nil
			}
		}
	} else if sid.class == 2 && sid.tag == 0 {
		for _, c := range certs {
			if bytes.Equal(c.SubjectKeyId, sid.content) {
				return c, nil
			}
		}
	}
	return nil, errors.New("the certificate of the signer is not in the message")
}

// ---- EnvelopedData

// cmsCipher is a content encryption algorithm.
type cmsCipher struct {
	oid     asn1.ObjectIdentifier
	keyLen  int
	newBlk  func(key []byte) (cipher.Block, error)
	ivLen   int
	display string
}

var cmsCiphers = map[string]cmsCipher{
	"AES128_CBC":   {oidAES128CBC, 16, aes.NewCipher, 16, "AES-128-CBC"},
	"AES192_CBC":   {oidAES192CBC, 24, aes.NewCipher, 16, "AES-192-CBC"},
	"AES256_CBC":   {oidAES256CBC, 32, aes.NewCipher, 16, "AES-256-CBC"},
	"DES_EDE3_CBC": {oidDESEDE3CBC, 24, des.NewTripleDESCipher, 8, "3DES-CBC"},
}

func cmsCipherByOID(oid asn1.ObjectIdentifier) (cmsCipher, bool) {
	for _, c := range cmsCiphers {
		if c.oid.Equal(oid) {
			return c, true
		}
	}
	return cmsCipher{}, false
}

func pkcs7Pad(b []byte, size int) []byte {
	n := size - len(b)%size
	return append(append([]byte(nil), b...), bytes.Repeat([]byte{byte(n)}, n)...)
}

func pkcs7Unpad(b []byte, size int) ([]byte, error) {
	if len(b) == 0 || len(b)%size != 0 {
		return nil, errors.New("bad padding")
	}
	n := int(b[len(b)-1])
	if n == 0 || n > size || n > len(b) {
		return nil, errors.New("bad padding")
	}
	for _, c := range b[len(b)-n:] {
		if int(c) != n {
			return nil, errors.New("bad padding")
		}
	}
	return b[:len(b)-n], nil
}

// cmsEncrypt encrypts content for the RSA key of recipient: an EnvelopedData, a
// ContentInfo in DER.
func cmsEncrypt(content []byte, recipient *x509.Certificate, c cmsCipher) ([]byte, error) {
	pub, ok := recipient.PublicKey.(*rsa.PublicKey)
	if !ok {
		return nil, errors.New("only RSA certificates can receive an encrypted message")
	}
	cek := make([]byte, c.keyLen)
	iv := make([]byte, c.ivLen)
	if _, err := io.ReadFull(rand.Reader, cek); err != nil {
		return nil, err
	}
	if _, err := io.ReadFull(rand.Reader, iv); err != nil {
		return nil, err
	}
	blk, err := c.newBlk(cek)
	if err != nil {
		return nil, err
	}
	padded := pkcs7Pad(content, blk.BlockSize())
	encrypted := make([]byte, len(padded))
	cipher.NewCBCEncrypter(blk, iv).CryptBlocks(encrypted, padded)
	wrapped, err := rsa.EncryptPKCS1v15(rand.Reader, pub, cek)
	if err != nil {
		return nil, err
	}
	recipientInfo := derSeq(
		derInt(0), derSeq(recipient.RawIssuer, derTLV(0x02, serialDER(recipient.SerialNumber))),
		derAlg(oidRSAEncryption, nil), derOctets(wrapped),
	)
	envelope := derSeq(
		derInt(0), derSet(recipientInfo),
		derSeq(derOID(oidData), derAlg(c.oid, derOctets(iv)), derTLV(0x80, encrypted)), // [0] IMPLICIT OCTET STRING
	)
	return derSeq(derOID(oidEnveloped), derExplicit(0, envelope)), nil
}

// cmsDecrypt decrypts an EnvelopedData with the key of cert (nil if it is not
// known: the recipient infos are tried). A recipient info that does not name
// cert is not tried, and a key that does not decrypt gives
// the same error as content that does not (as a padding oracle would need).
func cmsDecrypt(envelope []byte, cert *x509.Certificate, key *rsa.PrivateKey) ([]byte, error) {
	oid, ed, err := parseContentInfo(envelope)
	if err != nil {
		return nil, err
	}
	if !oid.Equal(oidEnveloped) {
		return nil, errors.New("not an EnvelopedData")
	}
	parts, err := ed.children()
	if err != nil || len(parts) < 3 {
		return nil, errors.New("bad EnvelopedData")
	}
	// version, [0] originatorInfo?, recipientInfos, encryptedContentInfo
	var recipients, eci ber
	for _, p := range parts[1:] {
		switch {
		case p.isUniversal(17):
			recipients = p
		case p.isUniversal(16):
			eci = p
		}
	}
	eciParts, err := eci.children()
	if err != nil || len(eciParts) < 3 {
		return nil, errors.New("bad encrypted content")
	}
	algParts, err := eciParts[1].children()
	if err != nil || len(algParts) != 2 {
		return nil, errors.New("bad content encryption algorithm")
	}
	algOID, err := algParts[0].oid()
	if err != nil {
		return nil, err
	}
	c, ok := cmsCipherByOID(algOID)
	if !ok {
		return nil, fmt.Errorf("the content encryption algorithm %v is not supported (AES-CBC and 3DES-CBC are)", algOID)
	}
	iv, err := algParts[1].octets()
	if err != nil || len(iv) != c.ivLen {
		return nil, errors.New("bad initialization vector")
	}
	var encrypted []byte
	if eciParts[2].class == 2 && eciParts[2].tag == 0 {
		if encrypted, err = eciParts[2].octets(); err != nil {
			return nil, err
		}
	} else {
		return nil, errors.New("the message has no encrypted content")
	}

	infos, err := recipients.children()
	if err != nil {
		return nil, errors.New("bad recipients")
	}
	var wrapped []byte
	for _, ri := range infos {
		if !ri.isUniversal(16) {
			continue // other kinds than key transport
		}
		f, err := ri.children()
		if err != nil || len(f) != 4 {
			continue
		}
		if cert != nil && !recipientIs(f[1], cert) {
			continue
		}
		if algOID, err := mustAlg(f[2]); err != nil || !algOID.Equal(oidRSAEncryption) {
			return nil, errors.New("the key is wrapped with RSA-OAEP or another algorithm than RSA PKCS #1 v1.5, which is not supported")
		}
		if wrapped, err = f[3].octets(); err != nil {
			return nil, err
		}
		if cert != nil {
			break
		}
		// Without the certificate the recipient is not known: the one whose
		// key unwraps to a key of the right length is ours.
		if k, err := rsa.DecryptPKCS1v15(nil, key, wrapped); err == nil && len(k) == c.keyLen {
			break
		}
		wrapped = nil
	}
	if wrapped == nil {
		return nil, errors.New("the message is not encrypted for this certificate")
	}
	cek := make([]byte, c.keyLen)
	if err := rsa.DecryptPKCS1v15SessionKey(rand.Reader, key, wrapped, cek); err != nil {
		return nil, errors.New("the key cannot be decrypted")
	}
	blk, err := c.newBlk(cek)
	if err != nil || len(encrypted) == 0 || len(encrypted)%blk.BlockSize() != 0 {
		return nil, errors.New("the content cannot be decrypted")
	}
	plain := make([]byte, len(encrypted))
	cipher.NewCBCDecrypter(blk, iv).CryptBlocks(plain, encrypted)
	out, err := pkcs7Unpad(plain, blk.BlockSize())
	if err != nil {
		return nil, errors.New("the content cannot be decrypted")
	}
	return out, nil
}

func mustAlg(e ber) (asn1.ObjectIdentifier, error) {
	kids, err := e.children()
	if err != nil || len(kids) == 0 {
		return nil, errors.New("bad algorithm")
	}
	return kids[0].oid()
}

// recipientIs tells if a recipient identifier names the certificate.
func recipientIs(rid ber, cert *x509.Certificate) bool {
	if rid.isUniversal(16) {
		kids, err := rid.children()
		if err != nil || len(kids) != 2 {
			return false
		}
		serial, err := kids[1].integer()
		return err == nil && bytes.Equal(kids[0].full, cert.RawIssuer) && serial.Cmp(cert.SerialNumber) == 0
	}
	return rid.class == 2 && rid.tag == 0 && len(cert.SubjectKeyId) > 0 && bytes.Equal(rid.content, cert.SubjectKeyId)
}

// ---- CompressedData

func cmsCompress(content []byte) ([]byte, error) {
	var z bytes.Buffer
	w := zlib.NewWriter(&z)
	if _, err := w.Write(content); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	compressed := derSeq(
		derInt(0), derAlg(oidZlib, []byte{}), // no parameters at all
		derSeq(derOID(oidData), derExplicit(0, derOctets(z.Bytes()))),
	)
	return derSeq(derOID(oidCompressed), derExplicit(0, compressed)), nil
}

// cmsMaxExpansion bounds what decompression may produce, against zip bombs.
const cmsMaxExpansion = 4 * maxBodySize

func cmsDecompress(data []byte) ([]byte, error) {
	oid, cd, err := parseContentInfo(data)
	if err != nil {
		return nil, err
	}
	if !oid.Equal(oidCompressed) {
		return nil, errors.New("not a CompressedData")
	}
	parts, err := cd.children()
	if err != nil || len(parts) != 3 {
		return nil, errors.New("bad CompressedData")
	}
	if alg, err := mustAlg(parts[1]); err != nil || !alg.Equal(oidZlib) {
		return nil, errors.New("only zlib compression is supported")
	}
	eci, err := parts[2].children()
	if err != nil || len(eci) != 2 {
		return nil, errors.New("bad CompressedData")
	}
	inner, err := eci[1].children()
	if err != nil || len(inner) != 1 {
		return nil, errors.New("bad CompressedData")
	}
	z, err := inner[0].octets()
	if err != nil {
		return nil, err
	}
	r, err := zlib.NewReader(bytes.NewReader(z))
	if err != nil {
		return nil, errors.New("the compressed content is not zlib")
	}
	defer r.Close()
	out, err := io.ReadAll(io.LimitReader(r, cmsMaxExpansion+1))
	if err != nil {
		return nil, errors.New("the compressed content is damaged")
	}
	if len(out) > cmsMaxExpansion {
		return nil, fmt.Errorf("the compressed content is more than %d bytes", cmsMaxExpansion)
	}
	return out, nil
}

// cmsType says what a ContentInfo holds.
func cmsType(data []byte) (asn1.ObjectIdentifier, error) {
	oid, _, err := parseContentInfo(data)
	return oid, err
}
