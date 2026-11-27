package cli

import (
	"bytes"
	"context"
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
	"dif/internal/dilsyntax"
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
	case "version", "describe", "catalog", "init", "convert", "test", "trace":
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
	return "Usage:\n  dif                       Start the interactive shell\n  dif run --file FLOW.json   Start a foreground service\n  dif run --dir DIRECTORY    Start a group of flows\n  dif run --help             Service options\n  dif validate [inputs]      Validate without starting flows\n  dif describe FLOW.json     Describe a flow\n  dif convert FILE           Convert a flow between DIL JSON and DIL text (.dil)\n  dif test FILE|DIR...       Run declarative flow tests hermetically\n  dif trace FILE [options]   Run a flow once and show step-by-step diffs\n  dif version              Show build information\n  dif help                  Show this help\n\nRun dif.exe (or .\\dif.exe in PowerShell) without arguments for the shell.\nAt the > prompt, type help to explore commands, or help <command> for details."
}

// runUtility executes development commands in the existing shell session.
func runUtility(args []string, stdout, stderr io.Writer) int {
	doc, ok := findCommand(args[0])
	if !ok {
		fmt.Fprintf(stderr, "Error: unknown command %q\n", args[0])
		return 2
	}
	if doc.name == "test" {
		return runTestUtility(args, stdout, stderr)
	}
	if doc.name == "trace" {
		return runTraceUtility(args, stdout, stderr)
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
	output, template, id, to, target := "text", "hello", "", "", ""
	strict := false
	switch doc.name {
	case "validate", "describe", "catalog", "version":
		fs.StringVar(&output, "output", "text", "Output format: text or json")
		if doc.name == "validate" {
			fs.BoolVar(&strict, "strict", false, "Fail on warnings of the static checks too")
		}
	case "init":
		fs.StringVar(&template, "template", "hello", "Starter template")
		fs.StringVar(&id, "id", "", "Flow id (defaults to filename)")
	case "convert":
		fs.StringVar(&to, "to", "", "Target format: dil or json")
		fs.StringVar(&target, "output", "", "File to write; refuses to overwrite one")
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
			if f := fs.Lookup(strings.TrimLeft(a, "-")); !strings.Contains(a, "=") && f != nil && takesValue(f) {
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
	case "describe", "init", "convert":
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
	if doc.name == "init" || doc.name == "validate" || doc.name == "describe" || doc.name == "convert" {
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
		sources := []api.ProgramSource{}
		for _, path := range positionals {
			sources = append(sources, api.ProgramSource{Name: path, Path: path})
		}
		prepared, err := api.PrepareProgram(context.Background(), sources, api.ProgramLimits{})
		if err == nil {
			err = prepared.CheckFlowLinks(nil)
		}
		var diagnostics map[string][]api.Diagnostic
		if err == nil {
			diagnostics = diagnosticsOf(prepared.Info(), positionals)
		}
		results := []validationInfo{}
		failed := false
		for _, path := range positionals {
			r := validationInfo{Path: path, Valid: err == nil}
			if err != nil {
				r.Error = err.Error()
			} else {
				r = validationResult(path, diagnostics[path], strict)
				failed = failed || !r.Valid
				if output == "text" {
					printValidation(stdout, path, diagnostics[path], strict)
				}
			}
			results = append(results, r)
		}
		if err != nil {
			fmt.Fprintf(stderr, "Error: %v\n", err)
		} else if failed {
			fmt.Fprintln(stderr, staticChecksFailed(strict))
		}
		if output == "json" {
			if c := writeJSON(stdout, stderr, results); c != 0 {
				return c
			}
		}
		if err != nil || failed {
			return 1
		}
		return 0
	case "describe":
		program, err := api.InspectProgram(positionals[0])
		if err != nil {
			return fail(err)
		}
		single := len(program.Flows) == 1 && len(program.Documents) == 1 && len(program.Documents[0].Stores) == 0
		if output == "json" {
			if single {
				return writeJSON(stdout, stderr, program.Flows[0].Flow)
			}
			return writeJSON(stdout, stderr, program)
		}
		if !single {
			for _, doc := range program.Documents {
				fmt.Fprintf(stdout, "DOCUMENT: %s\n", doc.Name)
				for _, imp := range doc.Imports {
					fmt.Fprintf(stdout, "  IMPORT: %s -> %s\n", imp.Namespace, imp.Document)
				}
				for _, in := range doc.Integrations {
					fmt.Fprintf(stdout, "  INTEGRATION: %s (%s); type: %s; version: %s\n", in.ID, in.Name, in.Type, in.Version)
				}
				for _, f := range doc.Formats {
					fmt.Fprintf(stdout, "  FORMAT: %s (%s)\n", f.Name, f.MediaType)
				}
				for _, m := range doc.Maps {
					fmt.Fprintf(stdout, "  MAP: %s (%s); %s -> %s\n", m.Name, m.Language, m.Input, m.Output)
				}
				for _, svc := range doc.Services {
					fmt.Fprintf(stdout, "  SERVICE: %s (%s); %s -> %s\n", svc.Name, svc.ExchangePattern, contractPayloadLabel(svc.Input), contractPayloadLabel(svc.Output))
				}
				for _, s := range doc.Stores {
					maxKeys := "default"
					if s.MaxKeys > 0 {
						maxKeys = fmt.Sprint(s.MaxKeys)
					}
					fmt.Fprintf(stdout, "  STORE: %s; durable: %t; ttl: %d ms; maxKeys: %s\n", s.Name, s.Durable, s.TTL, maxKeys)
				}
			}
		}
		for _, flow := range program.Flows {
			if !single {
				fmt.Fprintf(stdout, "\nORIGIN: %s; INTEGRATION: %s\n", flow.Origin, flow.Integration.ID)
			}
			describeFlow(stdout, flow.Flow)
			if !single {
				for _, ref := range flow.References {
					fmt.Fprintf(stdout, "  REFERENCE: %s %s (step: %s)\n", ref.Kind, ref.Name, ref.Step)
				}
			}
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
		if dilsyntax.IsTextPath(path) {
			if data, err = dilsyntax.Print(data); err != nil {
				return fail(err)
			}
		}
		if err := createFile(path, data); err != nil {
			return fail(err)
		}
		fmt.Fprintf(stdout, "Created %s (%s template, flow %s)\n", path, template, id)
	case "convert":
		// The input is read as every DIL document is: a .dil file, or a file
		// that is not JSON and starts with a comment or a word, is DIL text.
		path := positionals[0]
		data, err := os.ReadFile(path)
		if err != nil {
			return fail(err)
		}
		text := dilsyntax.IsTextPath(path) || dilsyntax.IsText(data)
		if to == "" {
			to = "dil"
			if text {
				to = "json"
			}
		}
		if to != "dil" && to != "json" {
			return bad("--to must be dil or json")
		}
		if text == (to == "dil") {
			return fail(fmt.Errorf("%s is DIL %s already", path, map[bool]string{true: "text", false: "JSON"}[text]))
		}
		if data, err = dilsyntax.Document(path, path, data); err != nil {
			return fail(err)
		}
		if to == "dil" {
			data, err = dilsyntax.Print(data)
		} else {
			var b bytes.Buffer
			err = json.Indent(&b, data, "", "  ")
			data = append(b.Bytes(), '\n')
		}
		if err != nil {
			return fail(err)
		}
		if target == "" {
			if _, err := stdout.Write(data); err != nil {
				return fail(err)
			}
			return 0
		}
		if err := createFile(target, data); err != nil {
			return fail(err)
		}
		fmt.Fprintf(stdout, "Created %s (DIL %s, from %s)\n", target, map[string]string{"dil": "text", "json": "JSON"}[to], path)
	}
	return 0
}

// createFile writes data to a new file at path; it never overwrites one.
func createFile(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	return err
}

type versionInfo struct {
	Version  string `json:"version"`
	Revision string `json:"revision"`
	Modified bool   `json:"modified"`
	Go       string `json:"go"`
	Platform string `json:"platform"`
}

type validationInfo struct {
	Path        string           `json:"path"`
	Valid       bool             `json:"valid"`
	Error       string           `json:"error,omitempty"`
	Diagnostics []api.Diagnostic `json:"diagnostics,omitempty"` // of the static checks
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

func contractPayloadLabel(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

func describeFlow(out io.Writer, info *api.FlowInfo) {
	fmt.Fprintf(out, "FLOW: %s\nNAME: %s\nSOURCE: %s\n", info.ID, info.Name, info.Source)
	if info.Concurrency > 1 || info.Partitioned {
		conc := info.Concurrency
		if conc < 1 {
			conc = 1
		}
		if info.Partitioned {
			fmt.Fprintf(out, "CONCURRENCY: %d (partitioned)\n", conc)
		} else {
			fmt.Fprintf(out, "CONCURRENCY: %d\n", conc)
		}
	}
	if svc := info.Service; svc != nil {
		fmt.Fprintf(out, "SERVICE: %s; pattern: %s; origin: %s\n", svc.Name, svc.ExchangePattern, svc.Origin)
	}
	fmt.Fprintln(out)
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
	fmt.Fprintln(out, renderTable([]string{"ID", "KIND", "STEP", "NEXT"}, rows, nil, nil))
	for _, s := range info.Steps {
		if m := s.Mapping; m != nil {
			fmt.Fprintf(out, "MAP: %s; step: %s; language: %s; %s -> %s; origin: %s\n", m.Name, s.ID, m.Language, m.Input, m.Output, m.Origin)
		}
		if svc := s.Service; svc != nil && s.Kind != "source" {
			fmt.Fprintf(out, "SERVICE: %s; step: %s; pattern: %s; origin: %s\n", svc.Name, s.ID, svc.ExchangePattern, svc.Origin)
		}
	}
	describeError := func(e *api.ErrorInfo) {
		fmt.Fprintf(out, "\nERROR: %s; retries: %d; delay: %d ms; route: %s", e.ID, e.Redeliveries, e.DelayMillis, e.Target)
		if len(e.Matches) > 0 {
			fmt.Fprintf(out, "; takes errors by: %s", strings.Join(e.Matches, ", "))
		}
		if e.Unhandled {
			fmt.Fprint(out, "; unhandled: the message still fails")
		}
		fmt.Fprintln(out)
	}
	for i := range info.ErrorHandlers {
		describeError(&info.ErrorHandlers[i])
	}
	if info.Error != nil {
		describeError(info.Error)
	}
	fmt.Fprintln(out, "\nCONFIGURATION (values omitted):")
	if len(info.Configuration) == 0 {
		fmt.Fprintln(out, "  No required options or environment fallbacks.")
	}
	for _, c := range info.Configuration {
		fmt.Fprintf(out, "  %s: %s (required: %t)", c.StepID, c.Option, c.Required)
		if c.Environment != "" {
			fmt.Fprintf(out, "; environment fallback: %s", c.Environment)
		}
		fmt.Fprintln(out)
	}
	describeCapabilities(out, info)
}

// describeCapabilities writes the flow's part of the capability manifest, one
// line per capability.
func describeCapabilities(out io.Writer, info *api.FlowInfo) {
	fmt.Fprintln(out, "\nCAPABILITIES (outside the process):")
	n := 0
	for _, s := range info.Steps {
		if s.CapabilitiesUndeclared {
			fmt.Fprintf(out, "  %s: undeclared (the step declares no capabilities)\n", s.ID)
			n++
		}
		for _, c := range s.Capabilities {
			fmt.Fprintf(out, "  %s: %s\n", s.ID, c)
			n++
		}
	}
	if n == 0 {
		fmt.Fprintln(out, "  None: the flow touches nothing outside the process.")
	}
}
