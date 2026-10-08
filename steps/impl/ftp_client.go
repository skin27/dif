package impl

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/textproto"
	"path"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ftpClient is a small FTP client (RFC 959, with MLSD, EPSV and SIZE where
// the server has them): binary transfers over passive data connections, in
// plain text. It is a remoteFS.
type ftpClient struct {
	ctl     *textproto.Conn
	conn    net.Conn
	host    string // the server's address, where data connections go
	timeout time.Duration

	noMLSD, noEPSV bool // the server does not have them

	mu   sync.Mutex
	data net.Conn // the data connection of a transfer under way, which close ends

	wmu sync.Mutex // serializes the commands written, as close may write QUIT from another goroutine
}

// deadlineConn gives every read and write its own deadline, so a stalled
// server ends a transfer without a limit on how long a big one may take.
type deadlineConn struct {
	net.Conn
	d time.Duration
}

func (c deadlineConn) Read(p []byte) (int, error) {
	c.Conn.SetReadDeadline(time.Now().Add(c.d))
	return c.Conn.Read(p)
}

func (c deadlineConn) Write(p []byte) (int, error) {
	c.Conn.SetWriteDeadline(time.Now().Add(c.d))
	return c.Conn.Write(p)
}

// dialFTP connects to the server and logs in. An empty user logs in as
// anonymous.
func dialFTP(ctx context.Context, addr, user, password string, timeout time.Duration) (*ftpClient, error) {
	d := net.Dialer{Timeout: timeout}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}
	host, _, _ := net.SplitHostPort(conn.RemoteAddr().String())
	c := &ftpClient{ctl: textproto.NewConn(deadlineConn{conn, timeout}), conn: conn, host: host, timeout: timeout}
	if err := c.login(user, password); err != nil {
		conn.Close()
		return nil, err
	}
	return c, nil
}

func (c *ftpClient) login(user, password string) error {
	if _, _, err := c.ctl.ReadResponse(220); err != nil {
		return fmt.Errorf("greeting: %w", err)
	}
	if user == "" {
		user, password = "anonymous", "anonymous@"
	}
	code, msg, err := c.cmd("USER %s", user)
	if err == nil && code == 331 {
		code, msg, err = c.cmd("PASS %s", password)
	}
	if err != nil {
		return err
	}
	if code != 230 {
		return fmt.Errorf("login: %d %s", code, msg)
	}
	if _, _, err := c.expect(200, "TYPE I"); err != nil {
		return err
	}
	return nil
}

// cmd sends a command and reads its reply, whatever the code. Arguments that
// hold a line break are refused, so a file name cannot add commands.
func (c *ftpClient) cmd(format string, args ...any) (int, string, error) {
	line := fmt.Sprintf(format, args...)
	if strings.ContainsAny(line, "\r\n") {
		return 0, "", fmt.Errorf("line break in the command")
	}
	if err := c.send(line); err != nil {
		return 0, "", err
	}
	return c.readReply()
}

func (c *ftpClient) send(line string) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	return c.ctl.PrintfLine("%s", line)
}

// readReply reads a reply, whatever its code. A reply is not an error here.
func (c *ftpClient) readReply() (int, string, error) {
	code, msg, err := c.ctl.ReadResponse(0)
	if tp, ok := err.(*textproto.Error); ok {
		return tp.Code, tp.Msg, nil
	}
	return code, msg, err
}

// expect sends a command and requires the reply code (a class, if it is one
// digit: 2 for 2xx).
func (c *ftpClient) expect(code int, format string, args ...any) (int, string, error) {
	got, msg, err := c.cmd(format, args...)
	if err != nil {
		return 0, "", err
	}
	if got != code && (code >= 10 || got/100 != code) {
		return got, msg, ftpError(strings.SplitN(format, " ", 2)[0], got, msg)
	}
	return got, msg, nil
}

// ftpReplyError is an error reply of the server.
type ftpReplyError struct {
	cmd, msg string
	code     int
}

func (e *ftpReplyError) Error() string { return fmt.Sprintf("%s: %d %s", e.cmd, e.code, e.msg) }

// Is makes a 550 (file unavailable) reply a fs.ErrNotExist.
func (e *ftpReplyError) Is(target error) bool { return target == fs.ErrNotExist && e.code == 550 }

func ftpError(cmd string, code int, msg string) error { return &ftpReplyError{cmd, msg, code} }

var epsvPort = regexp.MustCompile(`\(\|\|\|(\d+)\|\)`)
var pasvPort = regexp.MustCompile(`(\d+),(\d+),(\d+),(\d+),(\d+),(\d+)`)

// openData opens a passive data connection to the server's own address; the
// address in its reply is ignored, which also keeps the server from sending
// the client elsewhere.
func (c *ftpClient) openData(ctx context.Context) (net.Conn, error) {
	var port string
	if !c.noEPSV {
		code, msg, err := c.cmd("EPSV")
		if err != nil {
			return nil, err
		}
		if m := epsvPort.FindStringSubmatch(msg); code == 229 && m != nil {
			port = m[1]
		} else if code/100 == 5 {
			c.noEPSV = true
		} else {
			return nil, ftpError("EPSV", code, msg)
		}
	}
	if port == "" {
		code, msg, err := c.cmd("PASV")
		if err != nil {
			return nil, err
		}
		m := pasvPort.FindStringSubmatch(msg)
		if code != 227 || m == nil {
			return nil, ftpError("PASV", code, msg)
		}
		hi, _ := strconv.Atoi(m[5])
		lo, _ := strconv.Atoi(m[6])
		port = strconv.Itoa(hi*256 + lo)
	}
	d := net.Dialer{Timeout: c.timeout}
	conn, err := d.DialContext(ctx, "tcp", net.JoinHostPort(c.host, port))
	if err != nil {
		return nil, fmt.Errorf("data connection: %w", err)
	}
	return deadlineConn{conn, c.timeout}, nil
}

// transfer runs a command that moves data: it sends data (an upload) or
// returns what the server sends.
func (c *ftpClient) transfer(cmd string, upload bool, data []byte) ([]byte, error) {
	dc, err := c.openData(context.Background())
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.data = dc
	c.mu.Unlock()
	defer dc.Close()
	name, _, _ := strings.Cut(cmd, " ")
	if code, msg, err := c.cmd("%s", cmd); err != nil {
		return nil, err
	} else if code != 125 && code != 150 {
		return nil, ftpError(name, code, msg)
	}

	var got []byte
	if upload {
		_, err = dc.Write(data)
	} else {
		got, err = io.ReadAll(io.LimitReader(dc, maxBodySize+1))
		if err == nil && len(got) > maxBodySize {
			err = fmt.Errorf("%s: more than %d bytes", name, maxBodySize)
		}
	}
	dc.Close() // ends an upload
	if err != nil {
		return nil, err
	}
	if code, msg, err := c.readReply(); err != nil {
		return nil, err
	} else if code != 226 && code != 250 {
		return nil, ftpError(name, code, msg)
	}
	return got, nil
}

func (c *ftpClient) read(file string) ([]byte, error) { return c.transfer("RETR "+file, false, nil) }

func (c *ftpClient) write(file string, data []byte, appendTo bool) error {
	verb := "STOR "
	if appendTo {
		verb = "APPE "
	}
	_, err := c.transfer(verb+file, true, data)
	return err
}

func (c *ftpClient) remove(file string) error {
	_, _, err := c.expect(2, "DELE %s", file)
	return err
}

func (c *ftpClient) rename(from, to string) error {
	if _, _, err := c.expect(3, "RNFR %s", from); err != nil {
		return err
	}
	_, _, err := c.expect(2, "RNTO %s", to)
	return err
}

// mkdirAll makes dir and its parents. A refusal is taken to mean the folder
// exists; if it does not, the transfer into it fails with the server's reason.
func (c *ftpClient) mkdirAll(dir string) error {
	var p string
	if strings.HasPrefix(dir, "/") {
		p = "/"
	}
	for _, part := range strings.Split(dir, "/") {
		if part == "" || part == "." {
			continue
		}
		p = path.Join(p, part)
		code, msg, err := c.cmd("MKD %s", p)
		if err != nil {
			return err
		}
		if code/100 != 2 && code != 550 && code != 521 {
			return ftpError("MKD", code, msg)
		}
	}
	return nil
}

func (c *ftpClient) exists(file string) (bool, error) {
	code, msg, err := c.cmd("SIZE %s", file)
	switch {
	case err != nil:
		return false, err
	case code == 213:
		return true, nil
	case code == 550:
		return false, nil
	case code/100 == 5 && code != 530:
		// The server has no SIZE: look in the folder.
		entries, err := c.list(path.Dir(file))
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil // no folder, no file
		}
		if err != nil {
			return false, err
		}
		for _, e := range entries {
			if e.name == path.Base(file) {
				return true, nil
			}
		}
		return false, nil
	}
	return false, ftpError("SIZE", code, msg)
}

func (c *ftpClient) list(dir string) ([]remoteFile, error) {
	arg := ""
	if dir != "." && dir != "" {
		arg = " " + dir
	}
	if !c.noMLSD {
		data, err := c.transfer("MLSD"+arg, false, nil)
		var re *ftpReplyError
		if errors.As(err, &re) && re.code != 550 && re.code/100 == 5 {
			c.noMLSD = true // the server does not know MLSD
		} else if err != nil {
			return nil, err
		} else {
			return parseMLSD(data)
		}
	}
	data, err := c.transfer("LIST"+arg, false, nil)
	if err != nil {
		return nil, err
	}
	return parseList(data)
}

func (c *ftpClient) close() error {
	c.mu.Lock()
	if c.data != nil {
		c.data.Close()
	}
	c.mu.Unlock()
	c.conn.SetDeadline(time.Now().Add(time.Second))
	c.send("QUIT") // best effort
	return c.conn.Close()
}

// parseMLSD parses the machine-readable listing: lines of "fact=value;...; name".
func parseMLSD(data []byte) ([]remoteFile, error) {
	var files []remoteFile
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" {
			continue
		}
		facts, name, ok := strings.Cut(line, " ")
		if !ok || name == "" {
			return nil, fmt.Errorf("cannot parse listing line %q", line)
		}
		f := remoteFile{name: name}
		skip := false
		for _, fact := range strings.Split(facts, ";") {
			key, value, _ := strings.Cut(fact, "=")
			switch strings.ToLower(key) {
			case "type":
				switch strings.ToLower(value) {
				case "file":
				case "dir":
					f.dir = true
				default: // cdir, pdir, links, devices
					skip = true
				}
			case "size":
				f.size, _ = strconv.ParseInt(value, 10, 64)
			case "modify":
				if len(value) >= 14 {
					f.modTime, _ = time.Parse("20060102150405", value[:14])
				}
			}
		}
		if !skip {
			files = append(files, f)
		}
	}
	return files, nil
}

var (
	unixList = regexp.MustCompile(`^([-dlbcps])[-rwxsStT]{9}[+@.]?\s+\d+\s+\S+\s+(?:\S+\s+)?(\d+)\s+(\w{3}\s+\d{1,2}\s+(?:\d{4}|\d{1,2}:\d{2}))\s(.+)$`)
	dosList  = regexp.MustCompile(`^(\d{2}-\d{2}-\d{2,4})\s+(\d{1,2}:\d{2}(?:AM|PM)?)\s+(<DIR>|\d+)\s+(.+)$`)
)

// parseList parses the listing LIST returns, in the Unix (ls -l) or DOS
// format. Symbolic links and other special files are left out.
func parseList(data []byte) ([]remoteFile, error) {
	var files []remoteFile
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimRight(line, "\r")
		if line == "" || strings.HasPrefix(line, "total ") {
			continue
		}
		var f remoteFile
		if m := unixList.FindStringSubmatch(line); m != nil {
			switch m[1] {
			case "-":
			case "d":
				f.dir = true
			default:
				continue
			}
			f.size, _ = strconv.ParseInt(m[2], 10, 64)
			f.modTime = parseUnixTime(m[3])
			f.name = m[4]
		} else if m := dosList.FindStringSubmatch(line); m != nil {
			f.dir = m[3] == "<DIR>"
			f.size, _ = strconv.ParseInt(m[3], 10, 64)
			f.name = m[4]
		} else {
			return nil, fmt.Errorf("cannot parse listing line %q", line)
		}
		if f.name != "." && f.name != ".." {
			files = append(files, f)
		}
	}
	return files, nil
}

// parseUnixTime parses the time of an ls -l line: "Jan  2 15:04" in the last
// six months or so, else "Jan  2  2023". The zone is not known; it is UTC.
func parseUnixTime(s string) time.Time {
	s = strings.Join(strings.Fields(s), " ")
	if t, err := time.Parse("Jan 2 2006", s); err == nil {
		return t
	}
	now := time.Now().UTC()
	t, err := time.Parse("Jan 2 15:04 2006", s+" "+strconv.Itoa(now.Year()))
	if err != nil {
		return time.Time{}
	}
	if t.After(now.Add(24 * time.Hour)) {
		t = t.AddDate(-1, 0, 0) // a time of the last year
	}
	return t
}

var _ remoteFS = (*ftpClient)(nil)
