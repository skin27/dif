package impl

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/pkg/sftp"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

// sshSettings is how to log in to an SSH server.
type sshSettings struct {
	user       string
	password   string
	signer     ssh.Signer // the private key, if one is used
	knownHosts string     // known_hosts file; "" for the user's own
	strict     bool       // check the server's key against knownHosts
	timeout    time.Duration
}

// loadSigner reads a private key; passphrase may be empty.
func loadSigner(file, passphrase string) (ssh.Signer, error) {
	pem, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	if passphrase != "" {
		return ssh.ParsePrivateKeyWithPassphrase(pem, []byte(passphrase))
	}
	var missing *ssh.PassphraseMissingError
	signer, err := ssh.ParsePrivateKey(pem)
	if errors.As(err, &missing) {
		return nil, fmt.Errorf("the key is encrypted: set the privateKeyPassphrase option or the DIF_SFTP_PRIVATE_KEY_PASSPHRASE environment variable")
	}
	return signer, err
}

// config returns the client configuration. The known hosts are read now, not
// when the step is created, so that a flow loads without them.
func (s sshSettings) config() (*ssh.ClientConfig, error) {
	cfg := &ssh.ClientConfig{User: s.user, Timeout: s.timeout}
	if s.signer != nil {
		cfg.Auth = append(cfg.Auth, ssh.PublicKeys(s.signer))
	}
	if s.password != "" {
		cfg.Auth = append(cfg.Auth, ssh.Password(s.password))
	}
	if !s.strict {
		cfg.HostKeyCallback = ssh.InsecureIgnoreHostKey()
		return cfg, nil
	}
	file := s.knownHosts
	if file == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("known hosts: %w", err)
		}
		file = filepath.Join(home, ".ssh", "known_hosts")
	}
	callback, err := knownhosts.New(file)
	if err != nil {
		return nil, fmt.Errorf("known hosts: %w; set knownHostsFile, or strictHostKeyChecking to false to trust any server", err)
	}
	cfg.HostKeyCallback = callback
	return cfg, nil
}

// sftpClient is a remoteFS on an SFTP server.
type sftpClient struct {
	sf  *sftp.Client
	ssh *ssh.Client
}

func dialSFTP(ctx context.Context, addr string, s sshSettings) (*sftpClient, error) {
	cfg, err := s.config()
	if err != nil {
		return nil, err
	}
	d := net.Dialer{Timeout: s.timeout}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	// The handshake and the start of the subsystem have the timeout; a
	// connection that is idle afterwards must not.
	conn.SetDeadline(time.Now().Add(s.timeout))
	sc, chans, reqs, err := ssh.NewClientConn(conn, addr, cfg)
	if err != nil {
		conn.Close()
		return nil, err
	}
	client := ssh.NewClient(sc, chans, reqs)
	sf, err := sftp.NewClient(client)
	if err != nil {
		client.Close()
		return nil, err
	}
	conn.SetDeadline(time.Time{})
	return &sftpClient{sf, client}, nil
}

// notExist makes the server's "no such file" an error that wraps fs.ErrNotExist.
func notExist(err error) error {
	var se *sftp.StatusError
	if err != nil && !errors.Is(err, fs.ErrNotExist) && errors.As(err, &se) && se.Code == 2 { // SSH_FX_NO_SUCH_FILE
		return fmt.Errorf("%w: %v", fs.ErrNotExist, err)
	}
	return err
}

func (c *sftpClient) list(dir string) ([]remoteFile, error) {
	infos, err := c.sf.ReadDir(dir)
	if err != nil {
		return nil, notExist(err)
	}
	files := make([]remoteFile, 0, len(infos))
	for _, fi := range infos {
		if fi.IsDir() || fi.Mode().IsRegular() { // not links or devices
			files = append(files, remoteFile{name: fi.Name(), size: fi.Size(), modTime: fi.ModTime(), dir: fi.IsDir()})
		}
	}
	return files, nil
}

func (c *sftpClient) read(file string) ([]byte, error) {
	f, err := c.sf.Open(file)
	if err != nil {
		return nil, notExist(err)
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxBodySize+1))
	if err == nil && len(data) > maxBodySize {
		err = fmt.Errorf("%s: more than %d bytes", file, maxBodySize)
	}
	return data, err
}

// write writes the file, or adds to it. The append is done by writing at the
// end of the file, as servers differ in how they take the append flag.
func (c *sftpClient) write(file string, data []byte, appendTo bool) error {
	var size int64
	if appendTo {
		if fi, err := c.sf.Stat(file); err == nil {
			size = fi.Size()
		} else if err = notExist(err); !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	flags := os.O_WRONLY | os.O_CREATE
	if !appendTo {
		flags |= os.O_TRUNC
	}
	f, err := c.sf.OpenFile(file, flags)
	if err != nil {
		return notExist(err)
	}
	if _, err := f.WriteAt(data, size); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func (c *sftpClient) exists(file string) (bool, error) {
	_, err := c.sf.Lstat(file)
	if err = notExist(err); errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

func (c *sftpClient) remove(file string) error { return notExist(c.sf.Remove(file)) }

// rename renames, replacing the target where the server has posix-rename; archive removes the target itself.
func (c *sftpClient) rename(from, to string) error {
	if err := c.sf.PosixRename(from, to); err != nil {
		return c.sf.Rename(from, to) // the server has no posix-rename
	}
	return nil
}

func (c *sftpClient) mkdirAll(dir string) error { return c.sf.MkdirAll(dir) }

func (c *sftpClient) close() error {
	c.sf.Close()
	return c.ssh.Close()
}

var _ remoteFS = (*sftpClient)(nil)
