//go:build !windows

package cli

import "os"

// enableVT reports whether the terminal f handles ANSI escape sequences; Unix terminals do.
func enableVT(*os.File) bool { return true }
