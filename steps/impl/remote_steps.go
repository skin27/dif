package impl

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"strings"
	"time"

	"dif/message"
	stepdef "dif/steps/definition"
)

// The ftp and sftp steps work on a directory of a remote server, as the file
// steps do on a local one:
//
//   - the source polls the directory and emits a message per file; once the
//     flow has processed it, the file is deleted or moved to the folder move,
//     or, if processing failed, to moveFailed;
//   - the sink writes the body to a file;
//   - the enrichers (ftpenrich, sftpenrich) replace the body with the content
//     of the first file the options select.
//
// Both protocols have the same options for these; only the connection differs.

// remoteProtocol is what differs between ftp and sftp.
type remoteProtocol struct {
	scheme, port string
}

var (
	ftpProtocol  = remoteProtocol{"ftp", "21"}
	ftpsProtocol = remoteProtocol{"ftps", "21"} // 990 with implicit TLS
	sftpProtocol = remoteProtocol{"sftp", "22"}
	smbProtocol  = remoteProtocol{"smb", "445"}
)

// rawOpt returns the string option without its RAW(...) marker.
func rawOpt(p stepdef.Params, name string) string {
	s, _ := p[name].(string)
	return unRaw(s)
}

// remoteTarget is the server and directory a step works on.
type remoteTarget struct {
	scheme, host string
	dir          string
	conn         *remoteConn
}

func newRemoteTarget(proto remoteProtocol, p stepdef.Params) (*remoteTarget, error) {
	port := proto.port
	if proto.scheme == "ftps" && p["implicit"] == true {
		port = "990"
	}
	uri := p["path"].(string)
	if proto.scheme == "smb" {
		uri = strings.ReplaceAll(uri, `\`, "/") // Windows paths
	}
	if strings.Contains(uri, "${") {
		return nil, fmt.Errorf("uri: %q: ${...} in the address is not supported; the address of a step is fixed when the flow is built", uri)
	}
	u, err := parseRemoteURI(uri, port)
	if err != nil {
		return nil, fmt.Errorf("uri: %w", err)
	}
	share := ""
	if proto.scheme == "smb" {
		// The first part of the path is the share; the rest is below it.
		share, u.dir, _ = strings.Cut(strings.Trim(u.dir, "/"), "/")
		if share == "" || share == "." { // no directory is "."
			share = ""
			return nil, fmt.Errorf("uri: want smb://[user@]host[:port]/<share>[/<directory>]; the first part of the path is the share")
		}
	}
	user := rawOpt(p, "userName")
	if user == "" {
		user = u.user
	}
	password := rawOpt(p, "password")
	if _, given := p["password"]; !given {
		if password, _, err = environmentSecret("DIF_" + strings.ToUpper(proto.scheme) + "_PASSWORD"); err != nil {
			return nil, err
		}
	}
	timeout := time.Duration(p["socketTimeout"].(int)) * time.Millisecond

	t := &remoteTarget{scheme: proto.scheme, host: u.host, dir: u.dir}
	var dial func(ctx context.Context) (remoteFS, error)
	switch proto.scheme {
	case "ftp", "ftps":
		if p["passiveMode"] == false {
			return nil, fmt.Errorf("option passiveMode: active mode is not supported")
		}
		var secure *ftpTLS
		if proto.scheme == "ftps" {
			roots, err := outboundRoots(p)
			if err != nil {
				return nil, err
			}
			secure = &ftpTLS{
				config: &tls.Config{
					RootCAs: roots, MinVersion: tls.VersionTLS12, ServerName: u.host,
					ClientSessionCache: tls.NewLRUClientSessionCache(4),
				},
				implicit: p["implicit"] == true,
			}
		} else if p["implicit"] == true {
			return nil, fmt.Errorf("option implicit: ftp is plain text; use ftps")
		}
		dial = func(ctx context.Context) (remoteFS, error) {
			c, err := dialFTP(ctx, u.addr(), user, password, timeout, secure)
			if err != nil {
				return nil, err
			}
			return c, nil
		}
	case "smb":
		login := smbLogin{user: user, password: password}
		if domain, name, ok := strings.Cut(user, `\`); ok { // DOMAIN\user
			login.domain, login.user = domain, name
		}
		if d := rawOpt(p, "domain"); d != "" {
			login.domain = d
		}
		if login.user == "" {
			return nil, fmt.Errorf("option userName: required for smb; or give it in the URI: smb://user@host/share")
		}
		dial = func(ctx context.Context) (remoteFS, error) { return smbDial(ctx, u.addr(), share, login, timeout) }
	case "sftp":
		s := sshSettings{
			user: user, password: password, timeout: timeout,
			knownHosts: rawOpt(p, "knownHostsFile"), strict: p["strictHostKeyChecking"].(bool),
		}
		if key := rawOpt(p, "privateKey"); key != "" {
			passphrase, err := optionOrEnv(p, "privateKeyPassphrase", "DIF_SFTP_PRIVATE_KEY_PASSPHRASE")
			if err != nil {
				return nil, err
			}
			if s.signer, err = loadSigner(key, passphrase); err != nil {
				return nil, fmt.Errorf("option privateKey: %w", err)
			}
		}
		if user == "" {
			return nil, fmt.Errorf("option userName: required for sftp")
		}
		if password == "" && s.signer == nil {
			return nil, fmt.Errorf("option password or privateKey: sftp needs one; or set DIF_SFTP_PASSWORD")
		}
		dial = func(ctx context.Context) (remoteFS, error) {
			c, err := dialSFTP(ctx, u.addr(), s)
			if err != nil {
				return nil, err
			}
			return c, nil
		}
	}
	t.conn = &remoteConn{keep: !p["disconnect"].(bool), dial: func(ctx context.Context) (remoteFS, error) {
		c, err := dial(ctx)
		if err != nil {
			return nil, fmt.Errorf("%s %s: %w", t.scheme, t.host, err)
		}
		return c, nil
	}}
	return t, nil
}

// remoteFiles are the options that select files and read them.
type remoteFiles struct {
	sel        remoteSelect
	binary     bool
	cs         charset
	del        bool
	move       string
	changed    bool // readLock changed
	autoCreate bool
}

func newRemoteFiles(t *remoteTarget, p stepdef.Params) (remoteFiles, error) {
	f := remoteFiles{
		sel: remoteSelect{
			dir: t.dir, fileName: rawOpt(p, "fileName"), recursive: p["recursive"].(bool), sortBy: p["sortBy"].(string),
			skipDirs: map[string]bool{},
		},
		binary:     p["binary"].(bool),
		del:        p["delete"].(bool),
		move:       rawOpt(p, "move"),
		changed:    p["readLock"] == "changed",
		autoCreate: p["autoCreate"].(bool),
	}
	var err error
	if f.cs, err = parseCharset(p["charset"].(string)); err != nil {
		return f, fmt.Errorf("option charset: %w", err)
	}
	for opt, re := range map[string]**regexp.Regexp{"include": &f.sel.include, "exclude": &f.sel.exclude} {
		if s := rawOpt(p, opt); s != "" {
			if *re, err = regexp.Compile("^(?:" + s + ")$"); err != nil {
				return f, fmt.Errorf("option %s: %w", opt, err)
			}
		}
	}
	if f.sel.fileName != "" && (f.sel.include != nil || f.sel.exclude != nil) {
		return f, fmt.Errorf("option fileName: cannot be used with include or exclude")
	}
	return f, nil
}

// skipFolder keeps the files in a folder files are moved to out of a recursive search.
func (f remoteFiles) skipFolder(folder string) {
	if folder != "" && !strings.HasPrefix(folder, "/") {
		f.sel.skipDirs[path.Clean(folder)] = true
	}
}

func (f remoteFiles) body(data []byte) any {
	if f.binary {
		return data
	}
	return f.cs.text(data)
}

// fill sets the body of m to the content of a file, and its name.
func (f remoteFiles) fill(m message.Message, e remoteEntry, data []byte) {
	m[message.Body] = f.body(data)
	m[FileName] = e.rel
	setContentType(m, e.rel)
}

// remoteStamp identifies a version of a file.
type remoteStamp struct{ size, mod int64 }

func stampOf(e remoteEntry) remoteStamp { return remoteStamp{e.size, e.modTime.UnixNano()} }

// remoteSource polls a remote directory.
type remoteSource struct {
	remoteFiles
	t                   *remoteTarget
	moveFailed          string
	max                 int // files per poll; 0 for all
	initialDelay, delay time.Duration

	// Used by the polling goroutine only.
	created bool
	stamps  map[string]remoteStamp // the files of the last poll, for readLock changed
	stuck   map[string]remoteStamp // files that could not be moved or deleted, not to be consumed again
}

func newRemoteSource(proto remoteProtocol) func(string, stepdef.Params) (stepdef.Processor, error) {
	return func(_ string, p stepdef.Params) (stepdef.Processor, error) {
		t, err := newRemoteTarget(proto, p)
		if err != nil {
			return nil, err
		}
		files, err := newRemoteFiles(t, p)
		if err != nil {
			return nil, err
		}
		s := &remoteSource{
			remoteFiles:  files,
			t:            t,
			moveFailed:   rawOpt(p, "moveFailed"),
			max:          max(p["maxMessagesPerPoll"].(int), 0),
			initialDelay: time.Duration(p["initialDelay"].(int)) * time.Millisecond,
			delay:        time.Duration(p["delay"].(int)) * time.Millisecond,
			stamps:       map[string]remoteStamp{},
			stuck:        map[string]remoteStamp{},
		}
		if !s.del && s.move == "" {
			return nil, fmt.Errorf("option move: empty; a consumed file must be deleted (delete) or moved, or it is consumed again")
		}
		s.skipFolder(s.move)
		s.skipFolder(s.moveFailed)
		return s, nil
	}
}

func (s *remoteSource) Run(ctx context.Context, emit stepdef.Emit) error {
	return s.RunReady(ctx, emit, func() {})
}

// RunReady emits the files without waiting for their processing.
func (s *remoteSource) RunReady(ctx context.Context, emit stepdef.Emit, ready func()) error {
	return s.run(ctx, ready, func(m message.Message) (error, bool) { return nil, emit(m, nil) != nil })
}

// RunDelivery waits for the processing of each file, to move it to move or moveFailed.
func (s *remoteSource) RunDelivery(ctx context.Context, emit stepdef.EmitDelivery, ready func()) error {
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

// run polls until ctx is done. deliver hands a message to the flow and returns
// the outcome of its processing; stopping is true if the flow is stopping.
func (s *remoteSource) run(ctx context.Context, ready func(), deliver func(message.Message) (outcome error, stopping bool)) error {
	defer s.t.conn.shutdown()
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
			stepdef.Logger(ctx).Printf("%s %s: %v", s.t.scheme, s.t.host, err)
		}
	}
}

// pulled is a file read from the server.
type pulled struct {
	remoteEntry
	data []byte
}

func (s *remoteSource) poll(ctx context.Context, deliver func(message.Message) (error, bool)) (stopping bool, err error) {
	var batch []pulled
	err = s.t.conn.with(ctx, func(rfs remoteFS) error {
		if s.autoCreate && !s.created {
			if err := rfs.mkdirAll(s.sel.dir); err != nil {
				return err
			}
			s.created = true
		}
		files, err := s.sel.candidates(rfs)
		if err != nil {
			return err
		}
		for _, e := range s.eligible(files) {
			data, err := rfs.read(path.Join(s.sel.dir, e.rel))
			if err != nil {
				return fmt.Errorf("%s: %w", e.rel, err)
			}
			batch = append(batch, pulled{e, data})
		}
		return nil
	})
	if err != nil {
		return false, err
	}

	for _, f := range batch {
		m := message.New(nil)
		s.fill(m, f.remoteEntry, f.data)
		outcome, stop := deliver(m)
		if stop {
			return true, nil // the file stays for the next run
		}
		if err := s.finish(ctx, f.remoteEntry, outcome); err != nil {
			s.stuck[f.rel] = stampOf(f.remoteEntry)
			stepdef.Logger(ctx).Printf("%s %s: %s: consumed, but not moved or deleted, so it is not consumed again until it changes: %v", s.t.scheme, s.t.host, f.rel, err)
		}
	}
	return false, nil
}

// eligible returns the files to consume of the files in the directory: not
// those that could not be moved, with readLock changed only those unchanged
// since the last poll, and at most max.
func (s *remoteSource) eligible(files []remoteEntry) []remoteEntry {
	now := make(map[string]remoteStamp, len(files))
	var out []remoteEntry
	for _, e := range files {
		st := stampOf(e)
		now[e.rel] = st
		if s.stuck[e.rel] == st {
			continue
		}
		if s.changed && s.stamps[e.rel] != st {
			continue
		}
		out = append(out, e)
	}
	s.stamps = now
	for rel, st := range s.stuck {
		if now[rel] != st {
			delete(s.stuck, rel) // it has gone or changed
		}
	}
	if s.max > 0 && len(out) > s.max {
		out = out[:s.max]
	}
	return out
}

// finish deletes or moves a consumed file; a file whose processing failed is
// moved to moveFailed, or left where it is if there is none.
func (s *remoteSource) finish(ctx context.Context, e remoteEntry, outcome error) error {
	file := path.Join(s.sel.dir, e.rel)
	folder := s.move
	if outcome != nil {
		folder = s.moveFailed
	} else if s.del {
		return s.t.conn.with(ctx, func(rfs remoteFS) error { return rfs.remove(file) })
	}
	if folder == "" {
		return nil
	}
	return s.t.conn.with(ctx, func(rfs remoteFS) error { return archive(rfs, file, moveTarget(s.sel.dir, folder, e.rel)) })
}

// remoteSink writes the body to a file.
type remoteSink struct {
	t          *remoteTarget
	fileName   string
	binary     bool
	cs         charset
	autoCreate bool
	fileExist  string // Override, Append, Fail or Ignore
}

func newRemoteSink(proto remoteProtocol) func(string, stepdef.Params) (stepdef.Processor, error) {
	return func(_ string, p stepdef.Params) (stepdef.Processor, error) {
		t, err := newRemoteTarget(proto, p)
		if err != nil {
			return nil, err
		}
		s := &remoteSink{
			t: t, fileName: rawOpt(p, "fileName"), binary: p["binary"].(bool),
			autoCreate: p["autoCreate"].(bool), fileExist: p["fileExist"].(string),
		}
		if s.fileName != "" && !validRemoteName(s.fileName) {
			return nil, fmt.Errorf("option fileName: %q is not a name below the directory", s.fileName)
		}
		if s.cs, err = parseCharset(p["charset"].(string)); err != nil {
			return nil, fmt.Errorf("option charset: %w", err)
		}
		return s, nil
	}
}

// Consume writes the file. Its name is the option fileName, else the header
// file.name (or CamelFileName), else the trace id.
func (s *remoteSink) Consume(ctx context.Context, m message.Message) error {
	name := s.fileName
	for _, h := range []string{FileName, "CamelFileName", message.TraceID} {
		if v, _ := m[h].(string); name == "" {
			name = v
		}
	}
	if name == "" {
		return fmt.Errorf("%s: no file name: set the fileName option or the %s header", s.t.scheme, FileName)
	}
	if !validRemoteName(name) {
		return fmt.Errorf("%s: file name %q is not a name below the directory", s.t.scheme, name)
	}

	data := bytesOf(m[message.Body])
	if _, isText := m[message.Body].(string); isText && !s.binary && s.cs != utf8Charset {
		data = s.cs.encode(string(data))
	}
	file := path.Join(s.t.dir, name)
	return s.t.conn.with(ctx, func(rfs remoteFS) error {
		if s.fileExist == "Fail" || s.fileExist == "Ignore" {
			exists, err := rfs.exists(file)
			if err != nil {
				return err
			}
			if exists && s.fileExist == "Ignore" {
				return nil
			}
			if exists {
				return fmt.Errorf("file %s already exists", file)
			}
		}
		if s.autoCreate {
			if err := rfs.mkdirAll(path.Dir(file)); err != nil {
				return err
			}
		}
		return rfs.write(file, data, s.fileExist == "Append")
	})
}

// encode returns s in c; characters c cannot hold become ?.
func (c charset) encode(s string) []byte {
	out := make([]byte, 0, len(s))
	for _, r := range s {
		out = c.append(out, r)
	}
	return out
}

// remoteLockWait is how long an enricher waits to see whether a file changes
// (readLock changed).
var remoteLockWait = time.Second

// remoteEnrich replaces the body with the content of the first file the
// options select, and then deletes the file or moves it to the folder move
// (unless move is empty). Without a file the message passes on unchanged, or
// fails if abortMode is set.
type remoteEnrich struct {
	remoteFiles
	t     *remoteTarget
	abort bool
}

func newRemoteEnrich(proto remoteProtocol) func(string, stepdef.Params) (stepdef.Processor, error) {
	return func(_ string, p stepdef.Params) (stepdef.Processor, error) {
		t, err := newRemoteTarget(proto, p)
		if err != nil {
			return nil, err
		}
		files, err := newRemoteFiles(t, p)
		if err != nil {
			return nil, err
		}
		files.skipFolder(files.move)
		return &remoteEnrich{remoteFiles: files, t: t, abort: p["abortMode"].(bool)}, nil
	}
}

func (a *remoteEnrich) Process(ctx context.Context, m message.Message) (message.Message, error) {
	var (
		picked *remoteEntry
		data   []byte
	)
	err := a.t.conn.with(ctx, func(rfs remoteFS) error {
		files, err := a.sel.candidates(rfs)
		if errors.Is(err, fs.ErrNotExist) {
			return nil // no directory holds no files
		}
		if err != nil {
			return err
		}
		for _, e := range files {
			if a.changed {
				if stable, err := a.stable(ctx, rfs, e); err != nil {
					return err
				} else if !stable {
					continue
				}
			}
			file := path.Join(a.sel.dir, e.rel)
			if data, err = rfs.read(file); err != nil {
				return fmt.Errorf("%s: %w", e.rel, err)
			}
			picked = &e
			switch {
			case a.del:
				return rfs.remove(file)
			case a.move != "":
				return archive(rfs, file, moveTarget(a.sel.dir, a.move, e.rel))
			}
			return nil
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if picked == nil {
		if a.abort {
			return nil, fmt.Errorf("%s %s: no file found in %s", a.t.scheme, a.t.host, a.sel.dir)
		}
		return m, nil
	}
	a.fill(m, *picked, data)
	return m, nil
}

// stable reports whether the file is unchanged after remoteLockWait.
func (a *remoteEnrich) stable(ctx context.Context, rfs remoteFS, e remoteEntry) (bool, error) {
	select {
	case <-ctx.Done():
		return false, ctx.Err()
	case <-time.After(remoteLockWait):
	}
	files, err := a.sel.candidates(rfs)
	if err != nil {
		return false, err
	}
	for _, f := range files {
		if f.rel == e.rel {
			return stampOf(f) == stampOf(e), nil
		}
	}
	return false, nil
}
