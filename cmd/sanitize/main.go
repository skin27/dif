// Command sanitize replaces credentials in the test fixtures with dummies. See
// package sanitize for the rules.
//
//	go run ./cmd/sanitize            # rewrite testdata
//	go run ./cmd/sanitize -check     # list what would change; exit 1 if anything
//	go run ./cmd/sanitize -report    # also list every finding (never the values)
package main

import (
	"flag"
	"fmt"
	"os"

	"dif/internal/sanitize"
)

func main() {
	check := flag.Bool("check", false, "do not write; exit 1 if any credential would be replaced")
	report := flag.Bool("report", false, "list every finding")
	flag.Parse()
	dirs := flag.Args()
	if len(dirs) == 0 {
		dirs = []string{"testdata"}
	}
	found, err := sanitize.Run(dirs, !*check)
	if err != nil {
		fmt.Fprintln(os.Stderr, "sanitize:", err)
		os.Exit(2)
	}
	if *report || *check {
		for _, f := range found {
			fmt.Println(f)
		}
	}
	verb := "replaced"
	if *check {
		verb = "found"
	}
	fmt.Printf("%d credentials %s\n", len(found), verb)
	if *check && len(found) > 0 {
		os.Exit(1)
	}
}
