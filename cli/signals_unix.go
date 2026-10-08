//go:build !windows

package cli

import (
	"os"
	"syscall"
)

func serviceSignals() []os.Signal { return []os.Signal{os.Interrupt, syscall.SIGTERM} }
