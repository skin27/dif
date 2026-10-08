package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"time"

	"dif/internal/service"
)

func runServiceCommand(args []string, stdout, stderr io.Writer, validation bool) int {
	o, err := service.ParseOptions(args, os.Getenv, validation)
	if errors.Is(err, flag.ErrHelp) {
		fmt.Fprintln(stdout, service.Usage)
		return 0
	}
	if err != nil {
		fmt.Fprintln(stderr, "Error:", err)
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), serviceSignals()...)
	defer stop()
	if validation {
		d, _ := time.ParseDuration(o.StartupTimeout)
		ctx, cancel := context.WithTimeout(ctx, d)
		defer cancel()
		docs, err := service.Resolve(ctx, o)
		if err != nil {
			fmt.Fprintln(stderr, "Error:", err)
			if o.Output == "json" {
				json.NewEncoder(stdout).Encode([]validationInfo{{Valid: false, Error: err.Error()}})
			}
			return 1
		}
		results := make([]validationInfo, 0, len(docs))
		for _, doc := range docs {
			results = append(results, validationInfo{Path: doc.Name, Valid: true})
			if o.Output != "json" {
				fmt.Fprintf(stdout, "%s: valid (structure and option schemas)\n", doc.Name)
			}
		}
		if o.Output == "json" {
			return writeJSON(stdout, stderr, results)
		}
		return 0
	}
	if err := service.New(o, stdout).Run(ctx); err != nil {
		fmt.Fprintln(stderr, "Error:", err)
		return 1
	}
	return 0
}
