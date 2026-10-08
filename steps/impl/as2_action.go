package impl

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"dif/message"
	stepdef "dif/steps/definition"
)

// as2Action sends the body to a trading partner by AS2 (RFC 4130), as the
// platform's as2 action does with Camel's AS2 component, and checks the
// receipt (the MDN) the partner answers with in the same HTTP exchange. The
// body goes on unchanged; the headers AS2-Message-Id, AS2-MDN-Disposition,
// AS2-MDN-Signed and http.status are set.
//
// What is done to the document depends on as2MessageStructure: compressed,
// signed with certificateForSigning (private key and certificate) and
// encrypted for certificateForEncrypt (the partner's certificate), in that
// order. A receipt that reports a problem, or whose integrity check (MIC)
// is not that of what was sent, fails the message; so does a receipt that is
// signed with another certificate than certificateForEncrypt, when that is set.
//
// Asynchronous receipts are not supported: no Receipt-Delivery-Option is sent,
// so the partner answers in the response.
type as2Action struct {
	structure   as2Structure
	target      string
	from, to    string
	subject     string
	contentType string
	hash        cmsHash
	cipher      cmsCipher
	signing     *lazyKeys
	encryptFor  *lazyKeys
	wantReceipt bool
	notifyTo    string
	client      *http.Client
}

func newAS2Action(_ string, p stepdef.Params) (stepdef.Processor, error) {
	a := &as2Action{
		from: strings.TrimSpace(p["as2From"].(string)), to: strings.TrimSpace(p["as2To"].(string)),
		subject: p["subject"].(string), wantReceipt: p["mdn"] != "none",
		notifyTo: p["dispositionNotificationTo"].(string),
	}
	var err error
	if a.structure, err = parseAS2Structure(p["as2MessageStructure"].(string)); err != nil {
		return nil, fmt.Errorf("option as2MessageStructure: %w", err)
	}
	if a.hash, err = as2Hash(p["signingAlgorithm"].(string)); err != nil {
		return nil, fmt.Errorf("option signingAlgorithm: %w", err)
	}
	if a.cipher, err = as2Cipher(p["encryptingAlgorithm"].(string)); err != nil {
		return nil, fmt.Errorf("option encryptingAlgorithm: %w", err)
	}
	if a.from == "" || a.to == "" {
		return nil, fmt.Errorf("options as2From and as2To: required")
	}
	if a.notifyTo == "" {
		a.notifyTo = a.from
	}
	if a.contentType = strings.TrimSpace(p["ediMessageContentType"].(string)); a.contentType == "" {
		if a.contentType = strings.TrimSpace(p["messageContentType"].(string)); a.contentType == "" {
			a.contentType = "application/edifact"
		}
	}

	host := strings.TrimSpace(p["hostName"].(string))
	port := strings.TrimSpace(p["targetPortNumber"].(string))
	if port == "" {
		port = strings.TrimSpace(p["port"].(string))
	}
	path := strings.TrimSpace(p["requestUri"].(string))
	if path == "" {
		path = strings.TrimSpace(p["uri"].(string))
	}
	scheme := "http"
	if port == "443" || port == "8443" || p["sslContext"].(string) != "" {
		scheme = "https"
	}
	if strings.HasPrefix(host, "http://") || strings.HasPrefix(host, "https://") {
		return nil, fmt.Errorf("option hostName: want a host name, without http:// or https://")
	}
	if host == "" || strings.ContainsAny(host, "/@ ") {
		return nil, fmt.Errorf("option hostName: want a host name, got %q", host)
	}
	target := url.URL{Scheme: scheme, Host: host, Path: "/" + strings.TrimPrefix(path, "/")}
	if port != "" {
		target.Host = host + ":" + port
		if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
			target.Host = "[" + host + "]:" + port
		}
	}
	a.target = target.String()

	if a.client, err = httpsClient(p); err != nil {
		return nil, err
	}
	pw := p["password"].(string)
	a.signing = &lazyKeys{ref: p["certificateForSigning"].(string), password: pw, client: a.client}
	a.encryptFor = &lazyKeys{ref: p["certificateForEncrypt"].(string), password: pw, client: a.client}
	if a.structure.sign && a.signing.ref == "" {
		return nil, fmt.Errorf("option certificateForSigning: required with as2MessageStructure %s", p["as2MessageStructure"])
	}
	if a.structure.encrypt && a.encryptFor.ref == "" {
		return nil, fmt.Errorf("option certificateForEncrypt: required with as2MessageStructure %s", p["as2MessageStructure"])
	}
	return a, nil
}

// setRaw sets a header with exactly this spelling: AS2 software has been known
// to compare AS2-From and Message-ID case sensitively.
func setRaw(h http.Header, name, value string) { h[name] = []string{value} }

func newAS2MessageID(domain string) string {
	b := make([]byte, 12)
	rand.Read(b)
	return "<dif-" + hex.EncodeToString(b) + "@" + domain + ">"
}

func (a *as2Action) Process(ctx context.Context, m message.Message) (message.Message, error) {
	fileName, _ := m[FileName].(string)
	entity := payloadEntity(a.contentType, fileName, bytesOf(m[message.Body]))
	// The MIC is over what is signed, or else over what is encrypted. Partners
	// differ on whether a compressed document counts compressed, so the MIC of
	// the document itself is good too.
	mics := []string{micOf(entity.bytes(), a.hash)}
	if a.structure.compress {
		var err error
		if entity, err = compressEntity(entity); err != nil {
			return nil, fmt.Errorf("as2: %w", err)
		}
		mics = append([]string{micOf(entity.bytes(), a.hash)}, mics...)
	}
	if a.structure.sign {
		signer, err := a.signing.get(ctx)
		if err != nil {
			return nil, fmt.Errorf("as2: certificateForSigning: %w", err)
		}
		if entity, err = signEntity(entity, signer, a.hash); err != nil {
			return nil, fmt.Errorf("as2: %w", err)
		}
	}
	if a.structure.encrypt {
		partner, err := a.encryptFor.get(ctx)
		if err != nil || len(partner.certs) == 0 {
			if err == nil {
				err = fmt.Errorf("no certificate found")
			}
			return nil, fmt.Errorf("as2: certificateForEncrypt: %w", err)
		}
		if entity, err = encryptEntity(entity, partner.certs[0], a.cipher); err != nil {
			return nil, fmt.Errorf("as2: %w", err)
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, a.target, bytes.NewReader(entity.body))
	if err != nil {
		return nil, err
	}
	for _, h := range entity.headers {
		req.Header.Set(h[0], h[1])
	}
	messageID := newAS2MessageID(req.URL.Hostname())
	setRaw(req.Header, "AS2-From", a.from)
	setRaw(req.Header, "AS2-To", a.to)
	setRaw(req.Header, "AS2-Version", "1.2")
	setRaw(req.Header, "Message-ID", messageID)
	setRaw(req.Header, "MIME-Version", "1.0")
	setRaw(req.Header, "Date", time.Now().UTC().Format(http.TimeFormat))
	subject := a.subject
	if v, ok := m["subject"].(string); ok && v != "" {
		subject = v // as the smtp step lets a header override
	}
	if subject != "" {
		setRaw(req.Header, "Subject", subject)
	}
	req.Header.Set("User-Agent", "DIF")
	if a.wantReceipt {
		setRaw(req.Header, "Disposition-Notification-To", a.notifyTo)
		if a.structure.sign {
			setRaw(req.Header, "Disposition-Notification-Options", "signed-receipt-protocol=optional, pkcs7-signature; signed-receipt-micalg=optional, "+a.hash.micalg)
		}
	}

	resp, err := a.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("as2 %s: %w", a.target, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBodySize+1))
	if err != nil || len(data) > maxBodySize {
		return nil, fmt.Errorf("as2 %s: reading the answer failed", a.target)
	}
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("as2 %s: %s: %s", a.target, resp.Status, soapProblem(data))
	}
	m["AS2-Message-Id"] = messageID
	m["http.status"] = resp.StatusCode
	if !a.wantReceipt {
		return m, nil
	}

	disposition, signed, err := a.checkReceipt(ctx, resp.Header, data, mics)
	if err != nil {
		return nil, fmt.Errorf("as2 %s: %w", a.target, err)
	}
	m["AS2-MDN-Disposition"] = disposition
	m["AS2-MDN-Signed"] = signed
	return m, nil
}

// checkReceipt reads the answer as an MDN: it has to say processed, and its
// MIC has to be one of mics.
func (a *as2Action) checkReceipt(ctx context.Context, h http.Header, body []byte, mics []string) (disposition string, signed bool, err error) {
	if len(bytes.TrimSpace(body)) == 0 {
		return "", false, fmt.Errorf("the partner answered without an MDN; set the option mdn to none if it sends none")
	}
	top := mimeEntity{body: body}
	for _, name := range []string{"Content-Type", "Content-Transfer-Encoding"} {
		if v := h.Get(name); v != "" {
			top.headers = append(top.headers, [2]string{name, v})
		}
	}
	recv := as2Receiver{}
	if a.encryptFor.ref != "" {
		if partner, err := a.encryptFor.get(ctx); err == nil && len(partner.certs) > 0 {
			recv.known = partner.certs
			recv.trusted = x509.NewCertPool()
			for _, c := range partner.certs {
				recv.trusted.AddCert(c)
			}
		}
	}
	got, err := recv.unwrap(top)
	if err != nil {
		return "", false, fmt.Errorf("the MDN: %w", err)
	}
	report, err := parseMDN(got.payload)
	if err != nil {
		return "", false, err
	}
	if !report.ok() {
		return report.disposition, got.signed, fmt.Errorf("the partner did not accept the message: %s", report.disposition)
	}
	if report.mic != "" && !slices.Contains(mics, report.mic) {
		return report.disposition, got.signed, fmt.Errorf("the partner received something else than was sent: its integrity check is %s, ours %s", report.mic, mics[0])
	}
	return report.disposition, got.signed, nil
}
