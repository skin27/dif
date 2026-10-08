//go:build windows

package cli

import "os"

func serviceSignals() []os.Signal { return []os.Signal{os.Interrupt} }
