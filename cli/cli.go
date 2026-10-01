// Package cli implements the dif command line.
package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"dif/api"
)

const usage = "usage: dif run <flow.json>"

// Run executes the command line args and returns the process exit code.
func Run(args []string, stdout, stderr io.Writer) int {
	if len(args) != 2 || args[0] != "run" {
		fmt.Fprintln(stderr, usage)
		return 2
	}

	res, err := api.Run(args[1])
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}

	msg, err := json.Marshal(res.Message)
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}

	fmt.Fprintln(stdout, "trail:", strings.Join(res.Trail, " -> "))
	fmt.Fprintln(stdout, "message:", string(msg))
	fmt.Fprintf(stdout, "flow has been executed in %d milliseconds\n", res.Duration.Milliseconds())
	return 0
}
