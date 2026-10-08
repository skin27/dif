package impl

import (
	"bufio"
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/textproto"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"dif/keystore"
)

// remoteEnv is a server for the tests of the ftp and sftp steps, which run
// the same tests against both.
type remoteEnv struct {
	scheme string
	root   string // the local directory the server serves, which is its login directory
	host   string // host:port
	login  map[string]any
	conns  func() int // connections the server has accepted
}

// uri returns the URI of a step working on dir of the server.
func (e remoteEnv) uri(step, dir string) string { return step + ":" + e.host + "/" + dir }

// opts returns the login options and extra.
func (e remoteEnv) opts(extra map[string]any) map[string]any {
	all := map[string]any{}
	for k, v := range e.login {
		all[k] = v
	}
	for k, v := range extra {
		if v == nil {
			delete(all, k)
		} else {
			all[k] = v
		}
	}
	return all
}

func (e remoteEnv) local(p string) string { return filepath.Join(e.root, filepath.FromSlash(p)) }

// forEachProtocol runs the test against an ftp, an ftps, an sftp server and a
// fake SMB share (see fakeSMBShare).
func forEachProtocol(t *testing.T, test func(t *testing.T, e remoteEnv)) {
	t.Helper()
	t.Run("ftp", func(t *testing.T) { test(t, newFTPEnv(t, nil)) })
	t.Run("ftps", func(t *testing.T) { test(t, newFTPSEnv(t, false, nil)) })
	t.Run("ftps-implicit", func(t *testing.T) { test(t, newFTPSEnv(t, true, nil)) })
	t.Run("sftp", func(t *testing.T) { test(t, newSFTPEnv(t)) })
	t.Run("smb", func(t *testing.T) { test(t, newSMBEnv(t)) })
}

func newFTPEnv(t *testing.T, configure func(*fakeFTP)) remoteEnv {
	f := newFakeFTP(t)
	if configure != nil {
		configure(f)
	}
	f.start()
	return remoteEnv{"ftp", f.root, f.ln.Addr().String(), map[string]any{"userName": f.user, "password": f.pass}, f.connCount}
}

// newFTPSEnv is an FTP server that speaks TLS, explicit (AUTH TLS) or implicit,
// with the identity of the https tests, which the client trusts.
func newFTPSEnv(t *testing.T, implicit bool, configure func(*fakeFTP)) remoteEnv {
	f := newFakeFTP(t)
	cert, err := keystore.LoadIdentity(testIdentity, testPassword)
	if err != nil {
		t.Fatal(err)
	}
	f.tlsConfig, f.implicit = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}, implicit
	if configure != nil {
		configure(f)
	}
	f.start()
	login := map[string]any{
		"userName": f.user, "password": f.pass, "implicit": implicit,
		"trustStoreFile": testTrustStore, "trustStorePassword": testPassword,
	}
	return remoteEnv{"ftps", f.root, f.ln.Addr().String(), login, f.connCount}
}

func newSFTPEnv(t *testing.T) remoteEnv {
	s := newFakeSFTP(t)
	return remoteEnv{"sftp", s.root, s.addr, map[string]any{"userName": s.user, "password": s.pass, "strictHostKeyChecking": false}, func() int { return int(s.conns.Load()) }}
}

// fakeFTP is an FTP server on a local directory. The client's working
// directory is always the root.
type fakeFTP struct {
	t          *testing.T
	root       string
	ln         net.Listener
	user, pass string

	noMLSD, noEPSV, noSIZE bool
	dosList                bool // LIST in the DOS format
	hang                   bool // RETR never completes
	stop                   chan struct{}

	// FTPS: tlsConfig makes it a server that speaks TLS, from the first byte if implicit,
	// else after AUTH TLS. Data connections are TLS after PROT P.
	tlsConfig    *tls.Config
	implicit     bool
	requireReuse bool // a data connection must resume the TLS session of the control connection
	noCloseNote  bool // data connections are closed without close_notify, as many servers do

	mu       sync.Mutex
	conns    int
	cmds     []string
	tlsData  int // data connections that were TLS
	resumed  int // of those, the ones that resumed a session
	authTLS  int // control connections upgraded with AUTH
	pbszProt []string
}

func newFakeFTP(t *testing.T) *fakeFTP {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeFTP{t: t, root: t.TempDir(), ln: ln, user: "dif", pass: "s3cret", stop: make(chan struct{})}
	t.Cleanup(func() { close(f.stop); ln.Close() })
	return f
}

func (f *fakeFTP) start() { go f.serve() }

func (f *fakeFTP) connCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.conns
}

// commands returns the commands received, for those that start with a verb.
func (f *fakeFTP) commands(verbs ...string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var got []string
	for _, c := range f.cmds {
		for _, v := range verbs {
			if strings.HasPrefix(c, v) {
				got = append(got, c)
			}
		}
	}
	return got
}

func (f *fakeFTP) serve() {
	for {
		c, err := f.ln.Accept()
		if err != nil {
			return
		}
		f.mu.Lock()
		f.conns++
		f.mu.Unlock()
		if f.tlsConfig != nil && f.implicit {
			c = tls.Server(c, f.tlsConfig)
		}
		go f.handle(c)
	}
}

func (f *fakeFTP) path(p string) string {
	return filepath.Join(f.root, filepath.FromSlash(path.Clean("/"+p)))
}

func (f *fakeFTP) handle(c net.Conn) {
	defer func() { c.Close() }()
	r := textproto.NewReader(bufio.NewReader(c))
	w := func(format string, args ...any) { fmt.Fprintf(c, format+"\r\n", args...) }
	w("220 fake ftp")

	var (
		data       net.Listener
		user       string
		authed     bool
		renameFrom string
		protected  bool // PROT P: the data connections are TLS
	)
	defer func() {
		if data != nil {
			data.Close()
		}
	}()
	accept := func() net.Conn {
		if data == nil {
			return nil
		}
		data.(*net.TCPListener).SetDeadline(time.Now().Add(5 * time.Second))
		dc, err := data.Accept()
		data.Close()
		data = nil
		if err != nil {
			return nil
		}
		if protected {
			tc := tls.Server(dc, f.tlsConfig)
			tc.SetDeadline(time.Now().Add(5 * time.Second))
			if err := tc.Handshake(); err != nil {
				dc.Close()
				w("425 TLS negotiation failed")
				return nil
			}
			tc.SetDeadline(time.Time{})
			resumed := tc.ConnectionState().DidResume
			f.mu.Lock()
			f.tlsData++
			if resumed {
				f.resumed++
			}
			f.mu.Unlock()
			if f.requireReuse && !resumed {
				tc.Close()
				w("425 TLS session reuse required")
				return nil
			}
			return tc
		}
		return dc
	}
	// closeData ends a data connection, with close_notify unless the server is
	// one that does not send it.
	closeData := func(dc net.Conn) {
		if tc, ok := dc.(*tls.Conn); ok && f.noCloseNote {
			tc.NetConn().Close()
			return
		}
		dc.Close()
	}
	listen := func() int {
		if data != nil {
			data.Close()
		}
		data, _ = net.Listen("tcp", "127.0.0.1:0")
		return data.Addr().(*net.TCPAddr).Port
	}

	for {
		line, err := r.ReadLine()
		if err != nil {
			return
		}
		f.mu.Lock()
		f.cmds = append(f.cmds, line)
		f.mu.Unlock()
		verb, arg, _ := strings.Cut(line, " ")
		verb = strings.ToUpper(verb)

		switch verb {
		case "AUTH":
			if f.tlsConfig == nil || f.implicit || (strings.ToUpper(arg) != "TLS" && strings.ToUpper(arg) != "SSL") {
				w("504 not supported")
				continue
			}
			w("234 AUTH %s ok", arg)
			c = tls.Server(c, f.tlsConfig)
			r = textproto.NewReader(bufio.NewReader(c))
			f.mu.Lock()
			f.authTLS++
			f.mu.Unlock()
			continue
		case "PBSZ":
			w("200 PBSZ=0")
			continue
		case "PROT":
			protected = strings.ToUpper(arg) == "P"
			w("200 protection set")
			continue
		case "USER":
			user = arg
			w("331 password please")
			continue
		case "PASS":
			if user == f.user && arg == f.pass || user == "anonymous" && f.user == "" {
				authed = true
				w("230 logged in")
			} else {
				w("530 login incorrect")
			}
			continue
		case "QUIT":
			w("221 bye")
			return
		}
		if !authed {
			w("530 please log in")
			continue
		}

		p := f.path(arg)
		switch verb {
		case "TYPE":
			w("200 type set")
		case "EPSV":
			if f.noEPSV {
				w("500 unknown command")
				continue
			}
			w("229 Entering Extended Passive Mode (|||%d|)", listen())
		case "PASV":
			port := listen()
			w("227 Entering Passive Mode (10,9,9,9,%d,%d)", port/256, port%256) // an address that is not the server's
		case "SIZE":
			if f.noSIZE {
				w("500 unknown command")
			} else if fi, err := os.Stat(p); err != nil || fi.IsDir() {
				w("550 not a file")
			} else {
				w("213 %d", fi.Size())
			}
		case "MLSD", "LIST":
			if verb == "MLSD" && f.noMLSD {
				w("500 unknown command")
				continue
			}
			entries, err := os.ReadDir(p)
			if err != nil {
				w("550 no such directory")
				continue
			}
			w("150 here comes the listing")
			dc := accept()
			if dc == nil {
				continue
			}
			for _, e := range entries {
				fi, err := e.Info()
				if err != nil { // the entry was deleted since the directory was read
					continue
				}
				fmt.Fprint(dc, f.listLine(verb, fi), "\r\n")
			}
			closeData(dc)
			w("226 done")
		case "RETR":
			b, err := os.ReadFile(p)
			if err != nil {
				w("550 no such file")
				continue
			}
			w("150 sending")
			dc := accept()
			if dc == nil {
				continue
			}
			if f.hang {
				<-f.stop
				dc.Close()
				return
			}
			dc.Write(b)
			closeData(dc)
			w("226 done")
		case "STOR", "APPE":
			if _, err := os.Stat(filepath.Dir(p)); err != nil {
				w("550 no such directory")
				continue
			}
			w("150 receiving")
			dc := accept()
			if dc == nil {
				continue
			}
			b, _ := io.ReadAll(dc)
			dc.Close()
			flags := os.O_WRONLY | os.O_CREATE | os.O_TRUNC
			if verb == "APPE" {
				flags = os.O_WRONLY | os.O_CREATE | os.O_APPEND
			}
			out, err := os.OpenFile(p, flags, 0o644)
			if err != nil {
				w("550 cannot write")
				continue
			}
			out.Write(b)
			out.Close()
			w("226 done")
		case "DELE":
			if err := os.Remove(p); err != nil {
				w("550 cannot delete")
			} else {
				w("250 deleted")
			}
		case "RNFR":
			if _, err := os.Stat(p); err != nil {
				w("550 no such file")
			} else {
				renameFrom = p
				w("350 ready")
			}
		case "RNTO":
			if _, err := os.Stat(p); err == nil {
				w("550 target exists") // as strict servers do
			} else if err := os.Rename(renameFrom, p); err != nil {
				w("550 cannot rename")
			} else {
				w("250 renamed")
			}
		case "MKD":
			if err := os.Mkdir(p, 0o755); err != nil {
				w("550 cannot create")
			} else {
				w(`257 "%s" created`, arg)
			}
		default:
			w("502 not implemented")
		}
	}
}

// listLine formats one directory entry for MLSD or LIST.
func (f *fakeFTP) listLine(verb string, fi os.FileInfo) string {
	mod := fi.ModTime().UTC()
	if verb == "MLSD" {
		typ := "file"
		if fi.IsDir() {
			typ = "dir"
		}
		return fmt.Sprintf("type=%s;size=%d;modify=%s; %s", typ, fi.Size(), mod.Format("20060102150405"), fi.Name())
	}
	if f.dosList {
		size := fmt.Sprint(fi.Size())
		if fi.IsDir() {
			size = "<DIR>"
		}
		return fmt.Sprintf("%s  %-10s %s %s", mod.Format("01-02-06"), mod.Format("03:04PM"), size, fi.Name())
	}
	when := mod.Format("Jan _2 15:04")
	if time.Since(mod) > 150*24*time.Hour {
		when = mod.Format("Jan _2  2006")
	}
	mode := "-rw-r--r--"
	if fi.IsDir() {
		mode = "drwxr-xr-x"
	}
	return fmt.Sprintf("%s   1 dif staff %8d %s %s", mode, fi.Size(), when, fi.Name())
}

// fakeSFTP is an SSH server with the SFTP subsystem of pkg/sftp on a local directory.
type fakeSFTP struct {
	root, addr string
	user, pass string
	hostKey    ssh.PublicKey
	clientKey  ssh.PublicKey // accepted for public key login, if set
	conns      atomic.Int32
}

func newFakeSFTP(t *testing.T) *fakeSFTP {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	hostSigner, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	s := &fakeSFTP{root: t.TempDir(), user: "dif", pass: "s3cret", hostKey: hostSigner.PublicKey()}
	cfg := &ssh.ServerConfig{
		PasswordCallback: func(c ssh.ConnMetadata, pw []byte) (*ssh.Permissions, error) {
			if c.User() == s.user && string(pw) == s.pass {
				return nil, nil
			}
			return nil, errors.New("denied")
		},
		PublicKeyCallback: func(c ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			if s.clientKey != nil && c.User() == s.user && bytes.Equal(key.Marshal(), s.clientKey.Marshal()) {
				return nil, nil
			}
			return nil, errors.New("denied")
		},
	}
	cfg.AddHostKey(hostSigner)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s.addr = ln.Addr().String()
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			s.conns.Add(1)
			go s.serve(conn, cfg)
		}
	}()
	return s
}

func (s *fakeSFTP) serve(conn net.Conn, cfg *ssh.ServerConfig) {
	defer conn.Close()
	sc, chans, reqs, err := ssh.NewServerConn(conn, cfg)
	if err != nil {
		return
	}
	defer sc.Close()
	go ssh.DiscardRequests(reqs)
	for nc := range chans {
		if nc.ChannelType() != "session" {
			nc.Reject(ssh.UnknownChannelType, "only sessions")
			continue
		}
		ch, creqs, err := nc.Accept()
		if err != nil {
			return
		}
		go func() {
			for req := range creqs {
				ok := req.Type == "subsystem" && len(req.Payload) > 4 && string(req.Payload[4:]) == "sftp"
				req.Reply(ok, nil)
				if ok {
					srv, err := sftp.NewServer(ch, sftp.WithServerWorkingDirectory(s.root))
					if err != nil {
						return
					}
					srv.Serve()
					srv.Close()
					ch.Close()
				}
			}
		}()
	}
}

// clientKeyFile makes a key pair, lets the server accept it and writes the
// private key to a file, encrypted if passphrase is set.
func (s *fakeSFTP) clientKeyFile(t *testing.T, passphrase string) string {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	s.clientKey = sshPub
	var block *pem.Block
	if passphrase == "" {
		block, err = ssh.MarshalPrivateKey(priv, "")
	} else {
		block, err = ssh.MarshalPrivateKeyWithPassphrase(priv, "", []byte(passphrase))
	}
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "id_ed25519")
	if err := os.WriteFile(file, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatal(err)
	}
	return file
}

// knownHostsFile writes a known_hosts file with the server's key.
func (s *fakeSFTP) knownHostsFile(t *testing.T, key ssh.PublicKey) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "known_hosts")
	if err := os.WriteFile(file, []byte(knownhosts.Line([]string{s.addr}, key)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return file
}

// names returns the names in a local directory, sorted.
func names(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, e := range entries {
		got = append(got, e.Name())
	}
	sort.Strings(got)
	return got
}

// eventually waits until cond holds.
func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
