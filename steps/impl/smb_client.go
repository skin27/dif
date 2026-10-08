package impl

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"time"

	"github.com/hirochachacha/go-smb2"

	"dif/message"
	stepdef "dif/steps/definition"
)

// The smb steps are the ftp and sftp steps on a share of a Windows (SMB 2/3)
// file server, by github.com/hirochachacha/go-smb2, which is pure Go. The
// first part of the path in the URI is the share: smb://user@host[:port]/share/dir.
// Login is NTLM with a user, a password and optionally a domain.

// smbLogin is who logs in to a share.
type smbLogin struct {
	user, password, domain string
}

// smbShare is what smbClient needs of a mounted share: the methods of
// *smb2.Share it uses, with the files as smbFile, so that a test can stand in
// for a server.
type smbShare interface {
	ReadDir(dir string) ([]os.FileInfo, error)
	Open(name string) (smbFile, error)
	OpenFile(name string, flag int, perm os.FileMode) (smbFile, error)
	Stat(name string) (os.FileInfo, error)
	Remove(name string) error
	Rename(from, to string) error
	MkdirAll(dir string, perm os.FileMode) error
}

type smbFile interface {
	io.Reader
	io.Closer
	WriteAt(b []byte, off int64) (int, error)
}

// smb2Share is a *smb2.Share as an smbShare.
type smb2Share struct{ *smb2.Share }

func (s smb2Share) Open(name string) (smbFile, error) { return s.Share.Open(name) }
func (s smb2Share) OpenFile(name string, flag int, perm os.FileMode) (smbFile, error) {
	return s.Share.OpenFile(name, flag, perm)
}

// smbDial connects to a share; a variable so that tests can stand in for one.
var smbDial = dialSMB

func dialSMB(ctx context.Context, addr, share string, l smbLogin, timeout time.Duration) (remoteFS, error) {
	d := net.Dialer{Timeout: timeout}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	// The negotiation and the login have the timeout; a connection that is
	// idle afterwards must not.
	conn.SetDeadline(time.Now().Add(timeout))
	session, err := (&smb2.Dialer{Initiator: &smb2.NTLMInitiator{User: l.user, Password: l.password, Domain: l.domain}}).DialContext(ctx, conn)
	if err != nil {
		conn.Close()
		return nil, err
	}
	mounted, err := session.Mount(share)
	if err != nil {
		session.Logoff()
		conn.Close()
		return nil, fmt.Errorf("share %q: %w", share, err)
	}
	conn.SetDeadline(time.Time{})
	return &smbClient{share: smb2Share{mounted}, closeAll: func() error {
		mounted.Umount()
		session.Logoff()
		return conn.Close()
	}}, nil
}

// smbClient is a remoteFS on a share. The paths are relative to the share.
type smbClient struct {
	share    smbShare
	closeAll func() error
}

// smbNotFound are the NT status codes for a file or directory that is not
// there: STATUS_NO_SUCH_FILE, STATUS_OBJECT_NAME_NOT_FOUND and
// STATUS_OBJECT_PATH_NOT_FOUND.
var smbNotFound = map[uint32]bool{0xC000000F: true, 0xC0000034: true, 0xC000003A: true}

// smbErr makes the server's "not found" an error that wraps fs.ErrNotExist.
func smbErr(err error) error {
	var re *smb2.ResponseError
	if err != nil && !errors.Is(err, fs.ErrNotExist) && errors.As(err, &re) && smbNotFound[re.Code] {
		return fmt.Errorf("%w: %v", fs.ErrNotExist, err)
	}
	return err
}

// smbPath is a path for the share: "" is its root.
func smbPath(p string) string {
	if p == "" {
		return "."
	}
	return p
}

func (c *smbClient) list(dir string) ([]remoteFile, error) {
	infos, err := c.share.ReadDir(smbPath(dir))
	if err != nil {
		return nil, smbErr(err)
	}
	files := make([]remoteFile, 0, len(infos))
	for _, fi := range infos {
		if fi.IsDir() || fi.Mode().IsRegular() { // not links or devices
			files = append(files, remoteFile{name: fi.Name(), size: fi.Size(), modTime: fi.ModTime(), dir: fi.IsDir()})
		}
	}
	return files, nil
}

func (c *smbClient) read(file string) ([]byte, error) {
	f, err := c.share.Open(file)
	if err != nil {
		return nil, smbErr(err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxBodySize+1))
	if err == nil && len(data) > maxBodySize {
		err = fmt.Errorf("%s: more than %d bytes", file, maxBodySize)
	}
	return data, err
}

// write writes the file, or adds to it by writing at its end.
func (c *smbClient) write(file string, data []byte, appendTo bool) error {
	var size int64
	if appendTo {
		if fi, err := c.share.Stat(file); err == nil {
			size = fi.Size()
		} else if err = smbErr(err); !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	flags := os.O_WRONLY | os.O_CREATE
	if !appendTo {
		flags |= os.O_TRUNC
	}
	f, err := c.share.OpenFile(file, flags, 0o666)
	if err != nil {
		return smbErr(err)
	}
	if _, err := f.WriteAt(data, size); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func (c *smbClient) exists(file string) (bool, error) {
	_, err := c.share.Stat(file)
	if err = smbErr(err); errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

func (c *smbClient) remove(file string) error { return smbErr(c.share.Remove(file)) }

// rename fails if the target is there; archive removes it first.
func (c *smbClient) rename(from, to string) error { return smbErr(c.share.Rename(from, to)) }

func (c *smbClient) mkdirAll(dir string) error {
	if dir == "" || dir == "." {
		return nil
	}
	return c.share.MkdirAll(dir, 0o777)
}

func (c *smbClient) close() error { return c.closeAll() }

var _ remoteFS = (*smbClient)(nil)

// sinkAsAction makes the constructor of an action of the constructor of a sink:
// the action does what the sink does, and passes the message on unchanged. The
// smb step is an action in the platform's flows, as the file is no end of them.
func sinkAsAction(newSink func(string, stepdef.Params) (stepdef.Processor, error)) func(string, stepdef.Params) (stepdef.Processor, error) {
	return func(id string, p stepdef.Params) (stepdef.Processor, error) {
		s, err := newSink(id, p)
		if err != nil {
			return nil, err
		}
		return sinkAction{s.(stepdef.SinkProcessor)}, nil
	}
}

type sinkAction struct{ sink stepdef.SinkProcessor }

func (a sinkAction) Process(ctx context.Context, m message.Message) (message.Message, error) {
	if err := a.sink.Consume(ctx, m); err != nil {
		return nil, err
	}
	return m, nil
}
