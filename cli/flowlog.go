package cli

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// logDir is the directory holding the flows' log files, <logDir>/<flow id>.log.
var logDir = "logs"

// flowLog is a flow's log file. Lines are appended to the file and, while the
// log is followed, passed to the follower too.
type flowLog struct {
	path   string
	logger *log.Logger // writes timestamped lines to the log

	mu     sync.Mutex
	file   *os.File
	follow func(line string) // nil when nobody follows the log
}

// openFlowLog opens the log of flow id for appending, creating it if needed.
func openFlowLog(id string) (*flowLog, error) {
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return nil, err
	}
	name := strings.NewReplacer("/", "_", `\`, "_", ":", "_").Replace(id) + ".log"
	path := filepath.Join(logDir, name)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	l := &flowLog{path: path, file: f}
	l.logger = log.New(l, "", log.LstdFlags|log.Lmicroseconds)
	return l, nil
}

// Write appends one log line; the logger calls it once per line.
func (l *flowLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	n, err := l.file.Write(p)
	if l.follow != nil {
		l.follow(strings.TrimSuffix(string(p), "\n"))
	}
	return n, err
}

// Tail returns the last n lines of the log.
func (l *flowLog) Tail(n int) ([]string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return tail(l.path, n)
}

// Follow passes the last n lines of the log to fn, then every new line until
// stop is called. Only one follower is supported at a time.
func (l *flowLog) Follow(n int, fn func(line string)) (stop func(), err error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	lines, err := tail(l.path, n)
	if err != nil {
		return nil, err
	}
	for _, line := range lines {
		fn(line)
	}
	l.follow = fn
	return func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		l.follow = nil
	}, nil
}

func (l *flowLog) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.file.Close()
}

// tail returns the last n lines of the file at path. It reads the file
// backwards in chunks, so a long log is not read completely.
func tail(path string, n int) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}

	const chunk = 4096
	var buf []byte
	// n+1 newlines guarantee that the first of the last n lines is complete.
	for off := fi.Size(); off > 0 && bytes.Count(buf, []byte("\n")) <= n; {
		size := min(chunk, off)
		off -= size
		b := make([]byte, size, int(size)+len(buf))
		if _, err := f.ReadAt(b, off); err != nil {
			return nil, err
		}
		buf = append(b, buf...)
	}

	text := strings.TrimSuffix(string(buf), "\n")
	if text == "" {
		return nil, nil
	}
	lines := strings.Split(text, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines, nil
}
