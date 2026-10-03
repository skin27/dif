// Command dif runs DIF flows.
package main

import (
	"os"
	_ "time/tzdata" // time zones (the quartz source) without a zoneinfo database on the machine

	"dif/cli"
)

func main() {
	os.Exit(cli.Run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}
