package cli

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"

	"dif/api"
	flowimpl "dif/flows/impl"
)

// Run dispatches foreground service commands, utilities, or the interactive shell.
func Run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return runShell(stdin, stdout, stderr)
	}
	switch args[0] {
	case "run":
		return runServiceCommand(args[1:], stdout, stderr, false)
	case "validate":
		return runServiceCommand(args[1:], stdout, stderr, true)
	case "version", "describe", "catalog", "init":
		return runUtility(args, stdout, stderr)
	case "--version":
		return runUtility(append([]string{"version"}, args[1:]...), stdout, stderr)
	}
	if len(args) == 1 && (args[0] == "help" || args[0] == "--help" || args[0] == "-h") {
		fmt.Fprintln(stdout, startupHelp())
		return 0
	}
	fmt.Fprintln(stderr, "Error: commands are entered inside the interactive shell.\n"+startupHelp())
	return 2
}

func startupHelp() string {
	return "Usage:\n  dif                       Start the interactive shell\n  dif run --file FLOW.json   Start a foreground service\n  dif run --dir DIRECTORY    Start a group of flows\n  dif run --help             Service options\n  dif validate [inputs]      Validate without starting flows\n  dif describe FLOW.json     Describe a flow\n  dif version               Show build information\n  dif help                  Show this help\n\nRun dif.exe (or .\\dif.exe in PowerShell) without arguments for the shell.\nAt the > prompt, type help to explore commands, or help <command> for details."
}

// runUtility executes development commands in the existing shell session.
func runUtility(args []string, stdout, stderr io.Writer) int {
	doc, ok := findCommand(args[0])
	if !ok {
		fmt.Fprintf(stderr, "Error: unknown command %q\n", args[0])
		return 2
	}
	if doc.name == "completion" && !(len(args) == 2 && (args[1] == "--help" || args[1] == "-h")) {
		suggestions, err := completionSuggestions(strings.Join(args[1:], " "))
		if err != nil {
			fmt.Fprintln(stderr, "Error:", err)
			return 1
		}
		if len(suggestions) == 0 {
			fmt.Fprintln(stdout, "No completions found.")
		}
		for _, suggestion := range suggestions {
			fmt.Fprintln(stdout, suggestion)
		}
		return 0
	}
	fs := flag.NewFlagSet(doc.name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprintln(stderr, commandHelp(doc)) }
	output, template, id := "text", "hello", ""
	switch doc.name {
	case "validate", "describe", "catalog", "version":
		fs.StringVar(&output, "output", "text", "Output format: text or json")
	case "init":
		fs.StringVar(&template, "template", "hello", "Starter template")
		fs.StringVar(&id, "id", "", "Flow id (defaults to filename)")
	}
	// Allow flags before or after positional arguments; -- protects dash-prefixed paths.
	var options, positionals []string
	for i := 1; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			positionals = append(positionals, args[i+1:]...)
			break
		}
		if a == "--help" || a == "-h" {
			fmt.Fprintln(stdout, commandHelp(doc))
			return 0
		}
		if strings.HasPrefix(a, "-") {
			options = append(options, a)
			if !strings.Contains(a, "=") && fs.Lookup(strings.TrimLeft(a, "-")) != nil {
				if i+1 == len(args) {
					fmt.Fprintf(stderr, "Error: %s needs a value\n", a)
					return 2
				}
				i++
				options = append(options, args[i])
			}
		} else {
			positionals = append(positionals, a)
		}
	}
	if err := fs.Parse(options); err != nil {
		return 2
	}
	bad := func(message string) int {
		fmt.Fprintf(stderr, "Error: %s\n\n%s\n", message, commandHelp(doc))
		return 2
	}
	fail := func(err error) int { fmt.Fprintln(stderr, "Error:", err); return 1 }
	if output != "text" && output != "json" {
		return bad("--output must be text or json")
	}
	n := len(positionals)
	switch doc.name {
	case "validate":
		if n == 0 {
			return bad("missing flow file argument")
		}
	case "describe", "init":
		if n != 1 {
			return bad("expected exactly one argument")
		}
	case "version":
		if n != 0 {
			return bad("unexpected argument")
		}
	case "catalog":
		if n > 1 {
			return bad("expected at most one step argument")
		}
	}
	if doc.name == "init" || doc.name == "validate" || doc.name == "describe" {
		for i, path := range positionals {
			positionals[i] = flowFilePath(path)
		}
	}
	switch doc.name {
	case "version":
		v := versionInfo{Version: Version, Go: runtime.Version(), Platform: runtime.GOOS + "/" + runtime.GOARCH, Revision: "unknown"}
		if b, ok := debug.ReadBuildInfo(); ok {
			for _, s := range b.Settings {
				if s.Key == "vcs.revision" {
					v.Revision = s.Value
				}
				if s.Key == "vcs.modified" {
					v.Modified = s.Value == "true"
				}
			}
		}
		if output == "json" {
			return writeJSON(stdout, stderr, v)
		}
		fmt.Fprintf(stdout, "DIF %s\nRevision: %s (modified: %t)\nGo: %s\nPlatform: %s\n", v.Version, v.Revision, v.Modified, v.Go, v.Platform)
	case "catalog":
		steps := api.StepCatalog()
		if n == 1 {
			selected := []api.StepInfo{}
			for _, s := range steps {
				if s.Name == positionals[0] {
					selected = append(selected, s)
				}
			}
			if len(selected) == 0 {
				return fail(fmt.Errorf("step %q not found", positionals[0]))
			}
			steps = selected
		}
		if output == "json" {
			return writeJSON(stdout, stderr, steps)
		}
		if n == 0 {
			fmt.Fprintln(stdout, catalogText(steps))
		} else {
			for _, s := range steps {
				fmt.Fprintln(stdout, stepText(s))
			}
		}
	case "validate":
		results := []validationInfo{}
		ids := map[string]string{}
		code := 0
		for _, path := range positionals {
			info, err := api.Inspect(path)
			if err == nil {
				if previous, duplicate := ids[info.ID]; duplicate {
					err = fmt.Errorf("duplicate flow id %q (also in %s)", info.ID, previous)
				} else {
					ids[info.ID] = path
				}
			}
			r := validationInfo{Path: path, Valid: err == nil}
			if err != nil {
				r.Error = err.Error()
				code = 1
				fmt.Fprintf(stderr, "Error: %s: %v\n", path, err)
			} else if output == "text" {
				fmt.Fprintf(stdout, "%s: valid (structure and option schemas)\n", path)
			}
			results = append(results, r)
		}
		if output == "json" {
			if c := writeJSON(stdout, stderr, results); c != 0 {
				return c
			}
		}
		return code
	case "describe":
		info, err := api.Inspect(positionals[0])
		if err != nil {
			return fail(err)
		}
		if output == "json" {
			return writeJSON(stdout, stderr, info)
		}
		fmt.Fprintf(stdout, "FLOW: %s\nNAME: %s\nSOURCE: %s\n\n", info.ID, info.Name, info.Source)
		rows := [][]string{}
		for _, s := range info.Steps {
			links := []string{}
			for _, l := range s.Links {
				target := l.Target
				if l.Rule != "" {
					target += " (" + l.Rule + ")"
				}
				links = append(links, target)
			}
			rows = append(rows, []string{s.ID, s.Kind, s.Step, strings.Join(links, ", ")})
		}
		fmt.Fprintln(stdout, renderTable([]string{"ID", "KIND", "STEP", "NEXT"}, rows, nil, nil))
		if e := info.Error; e != nil {
			fmt.Fprintf(stdout, "\nERROR: %s; retries: %d; delay: %d ms; route: %s\n", e.ID, e.Redeliveries, e.DelayMillis, e.Target)
		}
		fmt.Fprintln(stdout, "\nCONFIGURATION (values omitted):")
		if len(info.Configuration) == 0 {
			fmt.Fprintln(stdout, "  No required options or environment fallbacks.")
		}
		for _, c := range info.Configuration {
			fmt.Fprintf(stdout, "  %s: %s (required: %t)", c.StepID, c.Option, c.Required)
			if c.Environment != "" {
				fmt.Fprintf(stdout, "; environment fallback: %s", c.Environment)
			}
			fmt.Fprintln(stdout)
		}
	case "init":
		path := positionals[0]
		if id == "" {
			id = strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
		}
		data, err := flowimpl.Template(template, id)
		if err != nil {
			return bad(err.Error())
		}
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
		if err != nil {
			return fail(err)
		}
		_, err = f.Write(data)
		closeErr := f.Close()
		if err != nil {
			return fail(err)
		}
		if closeErr != nil {
			return fail(closeErr)
		}
		fmt.Fprintf(stdout, "Created %s (%s template, flow %s)\n", path, template, id)
	}
	return 0
}

type versionInfo struct {
	Version  string `json:"version"`
	Revision string `json:"revision"`
	Modified bool   `json:"modified"`
	Go       string `json:"go"`
	Platform string `json:"platform"`
}

type validationInfo struct {
	Path  string `json:"path"`
	Valid bool   `json:"valid"`
	Error string `json:"error,omitempty"`
}

func writeJSON(stdout, stderr io.Writer, value any) int {
	enc := json.NewEncoder(stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(value); err != nil {
		fmt.Fprintln(stderr, "Error:", err)
		return 1
	}
	return 0
}
