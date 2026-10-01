// Command dif runs DIF flows.
package main

import (
	"os"

	"dif/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
