package impl

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"sort"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"dif/message"
	stepdef "dif/steps/definition"
)

// imapsSource polls a mailbox of an IMAP server over TLS (implicit TLS, port
// 993) and makes a message of every email it finds, by github.com/emersion/
// go-imap/v2 (see mail_parse.go for the message). The emails to take are the
// unseen ones (unseen), those that match the search options, the oldest first,
// at most fetchSize a poll. An email is marked as seen once the flow has
// processed it; one that failed stays unseen and is taken again.
//
// The login is the username and password (LOGIN), or with authenticationType
// oauth the username and the access token (XOAUTH2, as Gmail wants it); the token
// may be a tenant variable, @{name}, which an oauth2token step keeps fresh and
// which is looked up for every poll.
//
// The platform's bridgeErrorHandler, which sends a failure to connect to the
// flow's error route, is not done: a failed poll is logged, and tried again.
type imapsSource struct {
	host, addr     string
	user, password string
	oauth          bool
	token, tenant  string // token may hold @{variable}
	folder         string
	unseen         bool
	fetchSize      int // 0 for all
	both           bool
	subject, body  string
	from, to       string
	initialDelay   time.Duration
	delay          time.Duration
	timeout        time.Duration
	tlsConfig      *tls.Config
}

func newIMAPSSource(_ string, p stepdef.Params) (stepdef.Processor, error) {
	s := &imapsSource{
		user:      p["username"].(string),
		oauth:     p["authenticationType"] == "oauth",
		token:     p["accessToken"].(string),
		tenant:    p["tenantDbName"].(string),
		folder:    p["folderName"].(string),
		unseen:    p["unseen"].(bool),
		fetchSize: max(p["fetchSize"].(int), 0),
		both:      p["content"] == "both",
		subject:   p["searchSubject"].(string), body: p["searchBody"].(string),
		from: p["searchFrom"].(string), to: p["searchTo"].(string),
		initialDelay: time.Duration(p["initialDelay"].(int)) * time.Millisecond,
		delay:        time.Duration(p["delay"].(int)) * time.Millisecond,
		timeout:      time.Duration(p["socketTimeout"].(int)) * time.Millisecond,
	}
	host, port, err := net.SplitHostPort(strings.TrimPrefix(p["path"].(string), "//"))
	if err != nil {
		host, port = strings.TrimPrefix(p["path"].(string), "//"), "993"
	}
	if host == "" || strings.ContainsAny(host, "/@ ") {
		return nil, fmt.Errorf("uri: want imaps:<host>[:<port>]")
	}
	s.host, s.addr = host, net.JoinHostPort(host, port)
	if s.user == "" {
		return nil, fmt.Errorf("option username: required")
	}
	if s.oauth {
		if s.token == "" {
			return nil, fmt.Errorf("option accessToken: required with authenticationType oauth")
		}
	} else if pw, set := p["password"].(string); set {
		s.password = pw
	} else if s.password, _, err = environmentSecret("DIF_IMAPS_PASSWORD"); err != nil {
		return nil, err
	}
	roots, err := outboundRoots(p)
	if err != nil {
		return nil, err
	}
	s.tlsConfig = &tls.Config{RootCAs: roots, ServerName: host, MinVersion: tls.VersionTLS12}
	return s, nil
}

func (s *imapsSource) Run(ctx context.Context, emit stepdef.Emit) error {
	return s.RunReady(ctx, emit, func() {})
}

// RunReady emits the emails without waiting for their processing; they are
// marked as seen at once.
func (s *imapsSource) RunReady(ctx context.Context, emit stepdef.Emit, ready func()) error {
	return s.run(ctx, ready, func(m message.Message) (error, bool) { return nil, emit(m, nil) != nil })
}

// RunDelivery waits for the processing of each email, to mark it as seen.
func (s *imapsSource) RunDelivery(ctx context.Context, emit stepdef.EmitDelivery, ready func()) error {
	return s.run(ctx, ready, func(m message.Message) (error, bool) {
		done := make(chan error, 1)
		if emit(m, nil, func(err error) error { done <- err; return nil }) != nil {
			return nil, true
		}
		select {
		case err := <-done:
			return err, false
		case <-ctx.Done():
			return nil, true
		}
	})
}

func (s *imapsSource) run(ctx context.Context, ready func(), deliver func(message.Message) (outcome error, stopping bool)) error {
	ready()
	for wait := s.initialDelay; ; wait = s.delay {
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(wait):
		}
		stopping, err := s.poll(ctx, deliver)
		if stopping || ctx.Err() != nil {
			return nil
		}
		if err != nil {
			stepdef.Logger(ctx).Printf("imaps %s: %v", s.host, err)
		}
	}
}

// imapXOAuth2 is the SASL mechanism XOAUTH2, which Gmail and Outlook want for an
// OAuth2 access token (it has the shape of imapclient's sasl.Client).
type imapXOAuth2 struct{ user, token string }

func (x imapXOAuth2) Start() (string, []byte, error) {
	return "XOAUTH2", []byte("user=" + x.user + "\x01auth=Bearer " + x.token + "\x01\x01"), nil
}

// Next gets the server's error, a JSON text, when it refuses the token.
func (x imapXOAuth2) Next(challenge []byte) ([]byte, error) {
	return nil, fmt.Errorf("the server refused the token: %s", challenge)
}

// connect opens a connection and logs in.
func (s *imapsSource) connect(ctx context.Context) (*imapclient.Client, error) {
	setup, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	c, err := imapclient.DialTLS(s.addr, &imapclient.Options{TLSConfig: s.tlsConfig.Clone(), Dialer: &net.Dialer{Timeout: s.timeout}})
	if err != nil {
		return nil, err
	}
	stop := context.AfterFunc(setup, func() { c.Close() })
	if s.oauth {
		var token string
		if token, err = expandTenantVariables(s.tenant, s.token); err == nil && token == "" {
			err = fmt.Errorf("the access token is empty")
		}
		if err != nil {
			stop()
			c.Close()
			return nil, fmt.Errorf("access token: %w", err)
		}
		err = c.Authenticate(imapXOAuth2{s.user, token})
	} else {
		err = c.Login(s.user, s.password).Wait()
	}
	if err == nil {
		if _, err = c.Select(s.folder, nil).Wait(); err != nil {
			err = fmt.Errorf("folder %q: %w", s.folder, err)
		}
	}
	if !stop() || err != nil {
		c.Close()
		if err == nil {
			err = setup.Err()
		}
		return nil, err
	}
	return c, nil
}

// criteria are the search terms of the options.
func (s *imapsSource) criteria() *imap.SearchCriteria {
	c := &imap.SearchCriteria{}
	if s.unseen {
		c.NotFlag = []imap.Flag{imap.FlagSeen}
	}
	for key, value := range map[string]string{"Subject": s.subject, "From": s.from, "To": s.to} {
		if value != "" {
			c.Header = append(c.Header, imap.SearchCriteriaHeaderField{Key: key, Value: value})
		}
	}
	sort.Slice(c.Header, func(i, j int) bool { return c.Header[i].Key < c.Header[j].Key })
	if s.body != "" {
		c.Body = []string{s.body}
	}
	return c
}

func (s *imapsSource) poll(ctx context.Context, deliver func(message.Message) (error, bool)) (stopping bool, err error) {
	c, err := s.connect(ctx)
	if err != nil {
		return false, err
	}
	defer c.Close()
	stop := context.AfterFunc(ctx, func() { c.Close() })
	defer stop()

	found, err := c.UIDSearch(s.criteria(), nil).Wait()
	if err != nil {
		return false, fmt.Errorf("search: %w", err)
	}
	uids := found.AllUIDs()
	sort.Slice(uids, func(i, j int) bool { return uids[i] < uids[j] })
	if s.fetchSize > 0 && len(uids) > s.fetchSize {
		uids = uids[:s.fetchSize]
	}
	for _, uid := range uids {
		m, err := s.fetch(c, uid)
		if err != nil {
			stepdef.Logger(ctx).Printf("imaps %s: message %d: %v", s.host, uid, err)
			continue
		}
		outcome, stop := deliver(m)
		if stop {
			return true, nil // it stays unseen
		}
		if outcome != nil {
			continue // it stays unseen, to be taken again
		}
		flags := &imap.StoreFlags{Op: imap.StoreFlagsAdd, Silent: true, Flags: []imap.Flag{imap.FlagSeen}}
		if err := c.Store(imap.UIDSetNum(uid), flags, nil).Close(); err != nil {
			return false, fmt.Errorf("message %d: processed, but not marked as seen, so it comes again: %w", uid, err)
		}
	}
	return false, nil
}

// fetch reads an email, without marking it as seen, and makes a message of it.
func (s *imapsSource) fetch(c *imapclient.Client, uid imap.UID) (message.Message, error) {
	set := imap.UIDSetNum(uid)
	sizes, err := c.Fetch(set, &imap.FetchOptions{UID: true, RFC822Size: true}).Collect()
	if err != nil {
		return nil, fmt.Errorf("fetch: %w", err)
	}
	if len(sizes) == 1 && sizes[0].RFC822Size > 4*maxBodySize {
		return nil, fmt.Errorf("an email of %d bytes is too large", sizes[0].RFC822Size)
	}
	section := &imap.FetchItemBodySection{Peek: true}
	msgs, err := c.Fetch(set, &imap.FetchOptions{UID: true, BodySection: []*imap.FetchItemBodySection{section}}).Collect()
	if err != nil {
		return nil, fmt.Errorf("fetch: %w", err)
	}
	if len(msgs) != 1 || len(msgs[0].BodySection) != 1 {
		return nil, fmt.Errorf("the server sent no email")
	}
	return mailMessage(msgs[0].BodySection[0].Bytes, uint32(uid), s.both)
}
