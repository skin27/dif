package impl

import (
	"context"
	"fmt"
	"net"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// The ftp and sftp steps share this layer: a remoteFS is what they need of a
// remote file system, remoteConn manages its connection, and remoteSelect
// picks the files a source or an enricher takes. Remote paths are
// slash-separated, relative to the login directory unless they start with /.

// remoteFile is an entry of a remote directory.
type remoteFile struct {
	name    string
	size    int64
	modTime time.Time
	dir     bool
}

// remoteFS is a connection to a remote file system. An error for a missing
// file or directory wraps fs.ErrNotExist.
type remoteFS interface {
	list(dir string) ([]remoteFile, error) // the entries of dir, without . and ..
	read(file string) ([]byte, error)
	write(file string, data []byte, appendTo bool) error
	exists(file string) (bool, error)
	remove(file string) error
	rename(from, to string) error
	mkdirAll(dir string) error
	close() error
}

// remoteIdle is how long a connection that stays open (disconnect false)
// may be unused before it is closed; steps have no hook to close it when
// their flow stops.
var remoteIdle = 30 * time.Second

// remoteConn gives the steps a connection to use: a new one for every use, or
// with keep one that stays open between uses and is opened again after an
// error.
type remoteConn struct {
	dial func(ctx context.Context) (remoteFS, error)
	keep bool

	mu    sync.Mutex // serializes the uses of the kept connection
	cur   remoteFS
	last  time.Time
	timer *time.Timer
}

// with runs f on a connection. Cancelling ctx closes the connection, which
// ends the operation under way.
func (c *remoteConn) with(ctx context.Context, f func(remoteFS) error) error {
	if !c.keep {
		fs, err := c.dial(ctx)
		if err != nil {
			return err
		}
		stop := context.AfterFunc(ctx, func() { fs.close() })
		defer func() { stop(); fs.close() }()
		return f(fs)
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cur == nil {
		fs, err := c.dial(ctx)
		if err != nil {
			return err
		}
		c.cur = fs
	}
	fs := c.cur
	stop := context.AfterFunc(ctx, func() { fs.close() })
	err := f(fs)
	stop()
	if err != nil {
		fs.close()
		c.cur = nil
		return err
	}
	c.last = time.Now()
	if c.timer == nil {
		c.timer = time.AfterFunc(remoteIdle, c.reap)
	} else {
		c.timer.Reset(remoteIdle)
	}
	return nil
}

// reap closes the kept connection if it has been idle.
func (c *remoteConn) reap() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cur != nil && time.Since(c.last) >= remoteIdle {
		c.cur.close()
		c.cur = nil
	}
}

// shutdown closes the kept connection.
func (c *remoteConn) shutdown() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.cur != nil {
		c.cur.close()
		c.cur = nil
	}
	if c.timer != nil {
		c.timer.Stop()
	}
}

// remoteURI is the address in a step's URI: ftp:[//][user@]host[:port]/dir.
type remoteURI struct {
	user, host, port string
	dir              string // relative to the login directory, or absolute after a double slash
}

// parseRemoteURI parses the part of a step URI after the scheme. The
// directory follows Camel: ftp:host/a/b is a/b below the login directory,
// ftp:host//a/b the absolute /a/b.
func parseRemoteURI(s, defaultPort string) (remoteURI, error) {
	var u remoteURI
	s = strings.TrimPrefix(s, "//")
	authority, dir, _ := strings.Cut(s, "/")
	if at := strings.LastIndexByte(authority, '@'); at >= 0 {
		u.user, authority = authority[:at], authority[at+1:]
	}
	u.host, u.port = authority, defaultPort
	bad := fmt.Errorf("want <host>[:<port>]/<directory>, got %q", authority)
	switch {
	case strings.HasPrefix(authority, "["): // an IPv6 address
		end := strings.IndexByte(authority, ']')
		if end < 0 {
			return u, bad
		}
		u.host = authority[1:end]
		if rest := authority[end+1:]; rest != "" {
			if !strings.HasPrefix(rest, ":") {
				return u, bad
			}
			u.port = rest[1:]
		}
	case strings.Count(authority, ":") == 1:
		u.host, u.port, _ = strings.Cut(authority, ":")
	case strings.Contains(authority, ":"):
		return u, bad // an IPv6 address needs its brackets
	}
	if n, err := strconv.Atoi(u.port); u.host == "" || err != nil || n < 1 || n > 65535 {
		return u, bad
	}
	if strings.ContainsAny(s, "\r\n\x00") {
		return u, fmt.Errorf("control character in the address")
	}
	if strings.HasPrefix(dir, "/") {
		u.dir = path.Clean(dir)
	} else {
		u.dir = path.Clean("./" + dir)
	}
	return u, nil
}

func (u remoteURI) addr() string { return net.JoinHostPort(u.host, u.port) }

// unRaw returns the value inside Camel's RAW(...) marker, which DIL puts
// around values (passwords, folder names) that must not be interpreted.
func unRaw(s string) string {
	if inner, ok := strings.CutSuffix(strings.TrimPrefix(s, "RAW("), ")"); ok && strings.HasPrefix(s, "RAW(") {
		return inner
	}
	return s
}

// validRemoteName reports whether name can be appended to a directory
// without leaving it: not empty, not absolute, no .. element.
func validRemoteName(name string) bool {
	if name == "" || strings.ContainsAny(name, "\r\n\x00") || strings.HasPrefix(name, "/") {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if part == ".." {
			return false
		}
	}
	return path.Clean(name) != "."
}

// moveTarget returns where a file goes when it is moved to folder: below dir
// if folder is relative, keeping the file's place rel in the directory.
func moveTarget(dir, folder, rel string) string {
	if strings.HasPrefix(folder, "/") {
		return path.Join(folder, rel)
	}
	return path.Join(dir, folder, rel)
}

// remoteEntry is a file selected for a source or an enricher.
type remoteEntry struct {
	remoteFile
	rel string // its path below the directory
}

// remoteSelect holds which files a source or an enricher takes.
type remoteSelect struct {
	dir              string
	fileName         string
	include, exclude *regexp.Regexp
	recursive        bool
	sortBy           string
	skipDirs         map[string]bool // directories below dir not to look in: the folders files are moved to
}

func (s remoteSelect) matches(name string) bool {
	switch {
	case s.fileName != "":
		return name == s.fileName
	case s.include != nil && !s.include.MatchString(name):
		return false
	case s.exclude != nil && s.exclude.MatchString(name):
		return false
	}
	return true
}

// candidates returns the files the options select, in the order of sortBy
// (by name if empty). Entries whose name starts with a dot are skipped.
func (s remoteSelect) candidates(fs remoteFS) ([]remoteEntry, error) {
	var files []remoteEntry
	var walk func(rel string) error
	walk = func(rel string) error {
		entries, err := fs.list(path.Join(s.dir, rel))
		if err != nil {
			return err
		}
		for _, e := range entries {
			if strings.HasPrefix(e.name, ".") {
				continue
			}
			p := path.Join(rel, e.name)
			switch {
			case e.dir:
				if s.recursive && !s.skipDirs[p] {
					if err := walk(p); err != nil {
						return err
					}
				}
			case s.matches(e.name):
				files = append(files, remoteEntry{e, p})
			}
		}
		return nil
	}
	if err := walk(""); err != nil {
		return nil, err
	}

	byName := func(i, j int) bool { return files[i].rel < files[j].rel }
	byTime := func(i, j int) bool {
		if !files[i].modTime.Equal(files[j].modTime) {
			return files[i].modTime.Before(files[j].modTime)
		}
		return byName(i, j)
	}
	switch s.sortBy {
	case "reverse:file:name":
		sort.Slice(files, func(i, j int) bool { return byName(j, i) })
	case "file:modified":
		sort.Slice(files, byTime)
	case "reverse:file:modified":
		sort.Slice(files, func(i, j int) bool { return byTime(j, i) })
	default:
		sort.Slice(files, byName)
	}
	return files, nil
}

// archive moves the file to target, making its folder and replacing a file
// that is there.
func archive(fs remoteFS, file, target string) error {
	if err := fs.mkdirAll(path.Dir(target)); err != nil {
		return err
	}
	if exists, err := fs.exists(target); err != nil {
		return err
	} else if exists {
		if err := fs.remove(target); err != nil {
			return err
		}
	}
	return fs.rename(file, target)
}
