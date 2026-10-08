package impl

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"dif/message"
	stepdef "dif/steps/definition"
)

// fakeSMBShare is a share of a file server that is a directory of the local
// disk, for the tests; go-smb2 has no server, so the SMB wire protocol is not
// tested. Like a Windows server it does not rename onto a file that is there.
type fakeSMBShare struct{ root string }

func (s fakeSMBShare) path(name string) string {
	return filepath.Join(s.root, filepath.FromSlash(strings.ReplaceAll(name, `\`, "/")))
}

func (s fakeSMBShare) ReadDir(dir string) ([]os.FileInfo, error) {
	entries, err := os.ReadDir(s.path(dir))
	if err != nil {
		return nil, err
	}
	var infos []os.FileInfo
	for _, e := range entries {
		fi, err := e.Info()
		if err != nil {
			return nil, err
		}
		infos = append(infos, fi)
	}
	return infos, nil
}

func (s fakeSMBShare) Open(name string) (smbFile, error) { return os.Open(s.path(name)) }
func (s fakeSMBShare) OpenFile(name string, flag int, perm os.FileMode) (smbFile, error) {
	return os.OpenFile(s.path(name), flag, perm)
}
func (s fakeSMBShare) Stat(name string) (os.FileInfo, error) { return os.Stat(s.path(name)) }
func (s fakeSMBShare) Remove(name string) error              { return os.Remove(s.path(name)) }
func (s fakeSMBShare) MkdirAll(dir string, perm os.FileMode) error {
	return os.MkdirAll(s.path(dir), perm)
}
func (s fakeSMBShare) Rename(from, to string) error {
	if _, err := os.Lstat(s.path(to)); err == nil {
		return fmt.Errorf("rename %s %s: file exists", from, to)
	}
	return os.Rename(s.path(from), s.path(to))
}

// newSMBEnv is a share, "testshare" of a server, that the smb steps log in to as u.
func newSMBEnv(t *testing.T) remoteEnv {
	root := t.TempDir()
	var conns atomic.Int32
	old := smbDial
	smbDial = func(_ context.Context, addr, share string, l smbLogin, _ time.Duration) (remoteFS, error) {
		conns.Add(1)
		if share != "testshare" || l.user != "u" || l.password != "p" || l.domain != "" {
			return nil, fmt.Errorf("access denied: share %q user %q", share, l.user)
		}
		return &smbClient{share: fakeSMBShare{root}, closeAll: func() error { return nil }}, nil
	}
	t.Cleanup(func() { smbDial = old })
	return remoteEnv{"smb", root, "127.0.0.1:445/testshare", map[string]any{"userName": "u", "password": "p"}, func() int { return int(conns.Load()) }}
}

func TestSMBTarget(t *testing.T) {
	e := newSMBEnv(t)
	var got struct {
		addr, share string
		login       smbLogin
	}
	smbDial = func(_ context.Context, addr, share string, l smbLogin, _ time.Duration) (remoteFS, error) {
		got.addr, got.share, got.login = addr, share, l
		return &smbClient{share: fakeSMBShare{e.root}, closeAll: func() error { return nil }}, nil
	}
	for _, c := range []struct {
		uri  string
		opts map[string]any
		want string // address, share, directory the step writes to, user, password, domain
		dir  string
	}{
		{"smb://dovetail@api.example:445/Data/next/Regression Tests/test", map[string]any{"password": "pw"}, "api.example:445|Data|dovetail|pw|", "next/Regression Tests/test"},
		{`smb://dovetail@api.example:445/Data\next\Regression Tests\test`, map[string]any{"password": "pw"}, "api.example:445|Data|dovetail|pw|", "next/Regression Tests/test"},
		{"smb:api.example/Data", map[string]any{"userName": "u", "password": "pw"}, "api.example:445|Data|u|pw|", ""},
		{"smb://api.example/Data/", map[string]any{"userName": "CORP\\ann", "password": "pw"}, "api.example:445|Data|ann|pw|CORP", ""},
		{"smb://x@api.example/Data/d", map[string]any{"userName": "u", "domain": "WORK", "password": ""}, "api.example:445|Data|u||WORK", "d"},
	} {
		opts := map[string]any{"fileName": "RAW(f.txt)"}
		for k, v := range c.opts {
			opts[k] = v
		}
		sink := mustProcessor(t, stepdef.Sink, c.uri, opts).(stepdef.SinkProcessor)
		if err := sink.Consume(context.Background(), message.New("hello")); err != nil {
			t.Fatalf("%s: %v", c.uri, err)
		}
		if line := strings.Join([]string{got.addr, got.share, got.login.user, got.login.password, got.login.domain}, "|"); line != c.want {
			t.Errorf("%s: dialed %s, want %s", c.uri, line, c.want)
		}
		if body := read(t, filepath.Join(e.root, filepath.FromSlash(c.dir), "f.txt")); body != "hello" {
			t.Errorf("%s: file = %q", c.uri, body)
		}
	}
}

func TestSMBPasswordFromTheEnvironment(t *testing.T) {
	e := newSMBEnv(t)
	t.Setenv("DIF_SMB_PASSWORD", "p")
	sink := mustProcessor(t, stepdef.Sink, "smb:"+e.host+"/d", map[string]any{"userName": "u", "fileName": "RAW(f.txt)"}).(stepdef.SinkProcessor)
	if err := sink.Consume(context.Background(), message.New("x")); err != nil {
		t.Fatal(err)
	}
}

func TestSMBInvalidOptions(t *testing.T) {
	wantInvalid(t, stepdef.Sink, "smb://u@host", nil, "the first part of the path is the share")
	wantInvalid(t, stepdef.Sink, "smb://u@host/", nil, "the first part of the path is the share")
	wantInvalid(t, stepdef.Sink, "smb://host/share/dir", nil, "option userName: required for smb")
	wantInvalid(t, stepdef.Source, "smb://u@host/share", map[string]any{"move": ""}, "option move")
	wantInvalid(t, stepdef.Action, "smbenrich://u@host/share", map[string]any{"charset": "nope"}, "option charset")
	wantInvalid(t, stepdef.Sink, "smb://u@host/share", map[string]any{"privateKey": "k"}, "unknown option privateKey")
	wantInvalid(t, stepdef.Sink, "smb://u@host/share", map[string]any{"fileExist": "Sometimes"}, "fileExist")
}

// The smb step is an action in the platform's flows: it writes and passes the
// message on, and the smbenrich action after it reads the file back.
func TestSMBActionWritesAndPassesOn(t *testing.T) {
	e := newSMBEnv(t)
	write := mustProcessor(t, stepdef.Action, "smb:u@"+e.host+"/out", e.opts(map[string]any{"fileName": "RAW(a.txt)", "fileExist": "Append"})).(stepdef.ActionProcessor)
	enrich := mustProcessor(t, stepdef.Action, "smbenrich:u@"+e.host+"/out", e.opts(map[string]any{"fileName": "RAW(a.txt)", "move": ""})).(stepdef.ActionProcessor)

	m := message.New("one")
	m["keep"] = "me"
	out, err := write.Process(context.Background(), m)
	if err != nil || out[message.Body] != "one" || out["keep"] != "me" {
		t.Fatalf("out = %v, err = %v", out, err)
	}
	if _, err := write.Process(context.Background(), message.New("two")); err != nil {
		t.Fatal(err)
	}
	out, err = enrich.Process(context.Background(), message.New("original"))
	if err != nil || out[message.Body] != "onetwo" || out[FileName] != "a.txt" {
		t.Errorf("out = %v, err = %v", out, err)
	}

	// A login that is refused fails the message, naming the server.
	bad := mustProcessor(t, stepdef.Action, "smb:u@"+e.host+"/out", map[string]any{"password": "wrong", "fileName": "RAW(a.txt)"}).(stepdef.ActionProcessor)
	if _, err := bad.Process(context.Background(), message.New("x")); err == nil || !strings.Contains(err.Error(), "smb 127.0.0.1") || !strings.Contains(err.Error(), "access denied") {
		t.Errorf("err = %v", err)
	}
}

func TestSMBNotFound(t *testing.T) {
	c := &smbClient{share: fakeSMBShare{t.TempDir()}, closeAll: func() error { return nil }}
	if _, err := c.list("nothing"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("list: %v", err)
	}
	if _, err := c.read("nothing.txt"); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("read: %v", err)
	}
	if ok, err := c.exists("nothing.txt"); ok || err != nil {
		t.Errorf("exists: %v, %v", ok, err)
	}
	if files, err := c.list(""); err != nil || len(files) != 0 {
		t.Errorf("list of the share's root: %v, %v", files, err)
	}
	if err := c.mkdirAll(""); err != nil {
		t.Errorf("mkdirAll of the root: %v", err)
	}
}
