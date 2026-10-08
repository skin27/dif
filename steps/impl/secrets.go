package impl

import (
	"fmt"
	"io"
	"os"
	"strings"
)

// environmentSecret preserves direct environment precedence, including an
// explicitly empty value. Mounted secret files may end in one LF or CRLF.
func environmentSecret(name string) (string, bool, error) {
	if value, ok := os.LookupEnv(name); ok {
		return value, true, nil
	}
	path, ok := os.LookupEnv(name + "_FILE")
	if !ok {
		return "", false, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return "", true, fmt.Errorf("cannot read %s_FILE", name)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return "", true, fmt.Errorf("%s_FILE must name a regular file", name)
	}
	data, err := io.ReadAll(io.LimitReader(f, (64<<10)+1))
	if err != nil || len(data) > 64<<10 {
		return "", true, fmt.Errorf("cannot read %s_FILE within 64 KiB limit", name)
	}
	value := string(data)
	if strings.HasSuffix(value, "\n") {
		value = strings.TrimSuffix(strings.TrimSuffix(value, "\n"), "\r")
	}
	return value, true, nil
}
