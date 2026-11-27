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
	strict := false
	if validation {
		var err error
		if args, strict, err = strictFlag(args); err != nil {
			fmt.Fprintln(stderr, "Error:", err)
			return 2
		}
	}
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
		names := make([]string, 0, len(docs))
		for _, doc := range docs {
			names = append(names, doc.Name)
		}
		diagnostics := diagnosticsOf(docs[0].Program.Info(), names)
		results := make([]validationInfo, 0, len(docs))
		failed := false
		for _, doc := range docs {
			r := validationResult(doc.Name, diagnostics[doc.Name], strict)
			failed = failed || !r.Valid
			results = append(results, r)
			if o.Output != "json" {
				printValidation(stdout, doc.Name, diagnostics[doc.Name], strict)
			}
		}
		if o.Output == "json" {
			if c := writeJSON(stdout, stderr, results); c != 0 {
				return c
			}
		}
		if failed {
			fmt.Fprintln(stderr, staticChecksFailed(strict))
			return 1
		}
		return 0
	}
	if err := service.New(o, stdout).Run(ctx); err != nil {
		fmt.Fprintln(stderr, "Error:", err)
		return 1
	}
	return 0
}
