//go:build !windows && !linux && !darwin && !freebsd && !openbsd && !netbsd && !dragonfly

package channels

import (
	"fmt"
	"os"
)

func lockDirectory(string) (*os.File, error) {
	return nil, fmt.Errorf("durable storage locking is unsupported on this platform")
}
func syncDirectory(string) error           { return nil }
func replaceJournal(from, to string) error { return os.Rename(from, to) }
