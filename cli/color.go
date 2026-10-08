package cli

import (
	"io"
	"os"
)

// useColor reports whether w is a terminal that shows ANSI colors. NO_COLOR
// (https://no-color.org) and TERM=dumb turn colors off.
func useColor(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok || os.Getenv("NO_COLOR") != "" || os.Getenv("TERM") == "dumb" {
		return false
	}
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0 && enableVT(f)
}
