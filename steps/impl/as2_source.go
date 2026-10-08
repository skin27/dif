package impl

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/textproto"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"dif/keystore"
	"dif/message"
	stepdef "dif/steps/definition"
)

// as2Source receives documents by AS2 (RFC 4130), as the platform's as2 source
// does with Camel's AS2 server: an HTTP server on serverPortNumber that takes
// POSTs to requestUriPattern, decrypts them with decryptingPrivateKey, checks
// the signature, decompresses, and makes a message of the document. The
// answer, the MDN (receipt), is written once the flow has processed the message:
// it says processed, or the error. It is signed with signingPrivateKey when the
// sender asks for a signed receipt.
//
// The body of the message is the document (text if it is UTF-8, else bytes);
// its Content-Type is the document's. The headers are AS2-From, AS2-To,
// AS2-Message-Id, Subject, file.name (from the Content-Disposition), AS2-Signed,
// AS2-Encrypted, AS2-Compressed and, for a signed message, AS2-Signer.
//
// as2MessageStructure says what a message must have: SIGNED, ENCRYPTED and the
// like require a valid signature, encryption; compression is read when it is
// there. A signature is checked against the certificates of
// validateSigningCertificateChain (the sender is one of them, or issued by
// one); without that option any valid signature passes, which proves integrity
// but not who sent it.
//
// Not supported: asynchronous receipts (a sender that asks for one gets the
// receipt in the response), and the options as2From and as2To are not checked.
type as2Source struct {
	addr, pattern string
	structure     as2Structure
	fqdn          string
	tlsCert       *tls.Certificate

	decrypt, signKey, signChain, validate *lazyKeys
}

func newAS2Source(_ string, p stepdef.Params) (stepdef.Processor, error) {
	s := &as2Source{
		pattern: p["requestUriPattern"].(string),
		fqdn:    p["serverFqdn"].(string),
	}
	var err error
	structure := p["as2MessageStructure"].(string)
	if structure == "" {
		structure = p["messageStructure"].(string)
	}
	if s.structure, err = parseAS2Structure(structure); err != nil {
		return nil, fmt.Errorf("option as2MessageStructure: %w", err)
	}
	port := p["serverPortNumber"].(int)
	if port < 1 || port > 65535 {
		return nil, fmt.Errorf("option serverPortNumber: %d is not a port", port)
	}
	s.addr = net.JoinHostPort(p["address"].(string), strconv.Itoa(port))
	if s.pattern == "" {
		s.pattern = "*"
	}
	if s.fqdn == "" {
		s.fqdn = "dif.invalid"
	}
	if file := p["serverIdentityFile"].(string); file != "" {
		pw, given, err := serverIdentityPassword.get(p)
		if err != nil {
			return nil, err
		}
		cert, err := keystore.LoadIdentity(file, pw)
		if err != nil {
			return nil, serverIdentityPassword.explain("server identity", err, given)
		}
		s.tlsCert = &cert
	}
	client, err := httpsClient(p)
	if err != nil {
		return nil, err
	}
	pw := p["password"].(string)
	lazy := func(opt string) *lazyKeys {
		return &lazyKeys{ref: p[opt].(string), password: pw, client: client}
	}
	s.decrypt, s.signKey, s.signChain, s.validate = lazy("decryptingPrivateKey"), lazy("signingPrivateKey"), lazy("signingCertificateChain"), lazy("validateSigningCertificateChain")
	if s.structure.encrypt && s.decrypt.ref == "" {
		return nil, fmt.Errorf("option decryptingPrivateKey: required with as2MessageStructure %s", structure)
	}
	return s, nil
}

func (s *as2Source) Run(ctx context.Context, emit stepdef.Emit) error {
	return s.RunReady(ctx, emit, func() {})
}

func (s *as2Source) RunReady(ctx context.Context, emit stepdef.Emit, ready func()) error {
	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		return fmt.Errorf("as2: %w", err)
	}
	if s.tlsCert != nil {
		ln = tls.NewListener(ln, &tls.Config{Certificates: []tls.Certificate{*s.tlsCert}, MinVersion: tls.VersionTLS12})
	}
	srv := &http.Server{Handler: s.handler(ctx, emit), ReadHeaderTimeout: 10 * time.Second}
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ln) }()
	ready()
	select {
	case <-ctx.Done():
		shutdown := stepdef.ShutdownContext(ctx)
		if shutdown == nil {
			var cancel context.CancelFunc
			shutdown, cancel = context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
		}
		srv.Shutdown(shutdown)
		return nil
	case err := <-done:
		if ctx.Err() != nil {
			return nil
		}
		err = fmt.Errorf("as2 listener stopped: %w", err)
		stepdef.ReportSourceFailure(ctx, err)
		return err
	}
}

// matches tells if a request path is served.
func (s *as2Source) matches(path string) bool {
	switch {
	case s.pattern == "*" || s.pattern == "/*":
		return true
	case strings.HasSuffix(s.pattern, "*"):
		return strings.HasPrefix(path, strings.TrimSuffix(s.pattern, "*"))
	}
	return path == s.pattern
}

// receiver builds what reads a message, from the keys.
func (s *as2Source) receiver(ctx context.Context) (as2Receiver, keyMaterial, error) {
	var r as2Receiver
	dec, err := s.decrypt.get(ctx)
	if err != nil {
		return r, keyMaterial{}, fmt.Errorf("decryptingPrivateKey: %w", err)
	}
	signKey, err := s.signKey.get(ctx)
	if err != nil {
		return r, keyMaterial{}, fmt.Errorf("signingPrivateKey: %w", err)
	}
	chain, err := s.signChain.get(ctx)
	if err != nil {
		return r, keyMaterial{}, fmt.Errorf("signingCertificateChain: %w", err)
	}
	valid, err := s.validate.get(ctx)
	if err != nil {
		return r, keyMaterial{}, fmt.Errorf("validateSigningCertificateChain: %w", err)
	}
	r.decryptKey = dec.key
	if r.decryptCert = dec.ownCertificate(); r.decryptCert == nil && dec.key != nil {
		r.decryptCert = keyMaterial{key: dec.key, certs: append(chain.certs, signKey.certs...)}.ownCertificate()
	}
	if len(valid.certs) > 0 {
		r.known = valid.certs
		r.trusted = x509.NewCertPool()
		for _, c := range valid.certs {
			r.trusted.AddCert(c)
		}
	}
	// The key that signs receipts is the signing key with its certificate.
	signer := keyMaterial{key: signKey.key, certs: append(append([]*x509.Certificate(nil), signKey.certs...), chain.certs...)}
	return r, signer, nil
}

func (s *as2Source) handler(ctx context.Context, emit stepdef.Emit) http.HandlerFunc {
	type outcome struct{ err error }
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			http.Error(w, "AS2 messages are POSTed", http.StatusMethodNotAllowed)
			return
		}
		if !s.matches(r.URL.Path) {
			http.NotFound(w, r)
			return
		}
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodySize))
		if err != nil {
			http.Error(w, "cannot read request: "+err.Error(), http.StatusBadRequest)
			return
		}
		from, to, messageID := r.Header.Get("AS2-From"), r.Header.Get("AS2-To"), r.Header.Get("Message-ID")
		if from == "" || to == "" {
			http.Error(w, "an AS2 message has the headers AS2-From and AS2-To", http.StatusBadRequest)
			return
		}
		want := parseMDNRequest(r.Header)

		receiver, signer, err := s.receiver(r.Context())
		if err != nil {
			stepdef.Logger(ctx).Printf("as2 %s: %v", s.addr, err)
			http.Error(w, "the endpoint is not configured: its keys cannot be read", http.StatusInternalServerError)
			return
		}
		top := mimeEntity{body: raw}
		for _, name := range []string{"Content-Type", "Content-Transfer-Encoding", "Content-Disposition"} {
			if v := r.Header.Get(name); v != "" {
				top.headers = append(top.headers, [2]string{name, v})
			}
		}

		reply := func(disposition string, mic string) {
			s.writeReceipt(w, r, want, signer, mdnReport{
				text:        "The AS2 message has been received and processed by DIF.",
				reportingUA: "DIF",
				recipient:   to, originalID: messageID, disposition: disposition, mic: mic, micAlgName: want.micalg.micalg,
			}, from, to)
		}

		got, err := receiver.unwrap(top)
		if err == nil {
			err = s.requireStructure(got)
		}
		if err != nil {
			stepdef.Logger(ctx).Printf("as2 %s: message %s from %s: %v", s.addr, messageID, from, err)
			modifier := "unexpected-processing-error"
			if f, ok := err.(*as2Failure); ok {
				modifier = f.modifier
			}
			reply(dispositionError(modifier), "")
			return
		}
		m, err := s.message(got, from, to, messageID, r.Header.Get("Subject"))
		if err != nil {
			stepdef.Logger(ctx).Printf("as2 %s: message %s from %s: %v", s.addr, messageID, from, err)
			reply(dispositionError("unexpected-processing-error"), "")
			return
		}

		done := make(chan outcome, 1)
		if emit(m, func(_ message.Message, err error) { done <- outcome{err} }) != nil {
			http.Error(w, "flow is not running", http.StatusServiceUnavailable)
			return
		}
		select {
		case o := <-done:
			mic := ""
			if want.receipt {
				mic = micOf(got.mic, want.micalg)
			}
			if o.err != nil {
				stepdef.Logger(ctx).Printf("as2 %s: message %s from %s: the flow failed: %v", s.addr, messageID, from, o.err)
				reply(dispositionError("unexpected-processing-error"), mic)
				return
			}
			reply(dispositionProcessed(), mic)
		case <-r.Context().Done(): // the sender went away
		}
	}
}

// requireStructure refuses a message that lacks the security the endpoint is
// set up for.
func (s *as2Source) requireStructure(got as2Received) error {
	if s.structure.sign && !got.signed {
		return failure("insufficient-message-security", "the message is not signed, and this endpoint requires it")
	}
	if s.structure.encrypt && !got.encrypted {
		return failure("insufficient-message-security", "the message is not encrypted, and this endpoint requires it")
	}
	return nil
}

// message makes a message of a received document.
func (s *as2Source) message(got as2Received, from, to, id, subject string) (message.Message, error) {
	data, err := got.payload.decoded()
	if err != nil {
		return nil, fmt.Errorf("the document is damaged: %w", err)
	}
	m := message.New(nil)
	if utf8.Valid(data) {
		m[message.Body] = string(data)
	} else {
		m[message.Body] = data
	}
	if ct := got.payload.header("Content-Type"); ct != "" {
		m[message.ContentType] = ct
	}
	if _, params, err := mime.ParseMediaType(got.payload.header("Content-Disposition")); err == nil && params["filename"] != "" {
		m[FileName] = params["filename"]
	}
	m["AS2-From"], m["AS2-To"] = from, to
	if id != "" {
		m["AS2-Message-Id"] = id
	}
	if subject != "" {
		m["Subject"] = subject
	}
	m["AS2-Signed"], m["AS2-Encrypted"], m["AS2-Compressed"] = got.signed, got.encrypted, got.compressed
	if got.signer != nil {
		m["AS2-Signer"] = got.signer.Subject.String()
	}
	return m, nil
}

// writeReceipt answers with an MDN, signed if the sender asked for a signed
// one and there is a key; without a request for a receipt the answer is empty.
func (s *as2Source) writeReceipt(w http.ResponseWriter, r *http.Request, want mdnWanted, signer keyMaterial, report mdnReport, from, to string) {
	if !want.receipt {
		w.WriteHeader(http.StatusOK)
		return
	}
	entity := report.entity()
	if want.signed && signer.key != nil && signer.ownCertificate() != nil {
		if signed, err := signEntity(entity, signer, want.micalg); err == nil {
			entity = signed
		}
	}
	h := w.Header()
	setRaw(h, "AS2-From", to)
	setRaw(h, "AS2-To", from)
	setRaw(h, "AS2-Version", "1.2")
	setRaw(h, "Message-ID", newAS2MessageID(s.fqdn))
	setRaw(h, "MIME-Version", "1.0")
	h.Set("Server", "DIF")
	for _, hd := range entity.headers {
		h.Set(textproto.CanonicalMIMEHeaderKey(hd[0]), hd[1])
	}
	h.Set("Content-Length", strconv.Itoa(len(entity.body)))
	w.WriteHeader(http.StatusOK)
	w.Write(entity.body)
}
