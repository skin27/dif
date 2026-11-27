package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"

	"dif/api"
	"dif/engine"
	"dif/internal/dilsyntax"
	"dif/message"
)

type headerFlag struct {
	values map[string]any
}

func (h *headerFlag) String() string { return "" }

func (h *headerFlag) Set(raw string) error {
	k, v, ok := strings.Cut(raw, "=")
	if !ok || strings.TrimSpace(k) == "" {
		return fmt.Errorf("header %q must be key=value", raw)
	}
	if h.values == nil {
		h.values = map[string]any{}
	}
	h.values[k] = v
	return nil
}

// runTestUtility executes `dif test <file|dir>... [--output text|json]`.
func runTestUtility(args []string, stdout, stderr io.Writer) int {
	doc, _ := findCommand("test")
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprintln(stderr, commandHelp(doc)) }
	output := "text"
	fs.StringVar(&output, "output", "text", "Output format: text or json")

	options, positionals, helpShown, code := splitUtilityArgs(args, fs, doc, stdout, stderr)
	if helpShown {
		return 0
	}
	if code != 0 {
		return code
	}
	if err := fs.Parse(options); err != nil {
		return 2
	}
	if output != "text" && output != "json" {
		fmt.Fprintf(stderr, "Error: --output must be text or json\n\n%s\n", commandHelp(doc))
		return 2
	}
	if len(positionals) == 0 {
		fmt.Fprintf(stderr, "Error: missing test file or directory argument\n\n%s\n", commandHelp(doc))
		return 2
	}

	sources, err := resolveTestSources(positionals)
	if err != nil {
		fmt.Fprintln(stderr, "Error:", err)
		return 1
	}

	report, err := api.RunTests(context.Background(), sources, api.ProgramLimits{})
	if err != nil {
		fmt.Fprintln(stderr, "Error:", err)
		return 1
	}
	if report.Total == 0 {
		if output == "json" {
			writeJSON(stdout, stderr, report)
		}
		fmt.Fprintln(stderr, "Error: no tests found")
		return 1
	}
	if output == "json" {
		if c := writeJSON(stdout, stderr, report); c != 0 {
			return c
		}
	} else {
		fmt.Fprintln(stdout, formatTestReport(report, useColor(stdout)))
	}
	if report.Failed > 0 {
		return 1
	}
	return 0
}

// runTraceUtility executes `dif trace <file> [--flow id] [--body text] [--header k=v]... [--output text|json]`.
func runTraceUtility(args []string, stdout, stderr io.Writer) int {
	doc, _ := findCommand("trace")
	fs := flag.NewFlagSet("trace", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { fmt.Fprintln(stderr, commandHelp(doc)) }
	var (
		flowID  string
		body    string
		hasBody bool
		output  = "text"
		headers headerFlag
	)
	fs.StringVar(&flowID, "flow", "", "Flow id to trace when a file defines more than one flow")
	fs.Func("body", "Override the input message body", func(v string) error {
		body = v
		hasBody = true
		return nil
	})
	fs.Var(&headers, "header", "Set an input message header as key=value (repeatable)")
	fs.StringVar(&output, "output", "text", "Output format: text or json")

	options, positionals, helpShown, code := splitUtilityArgs(args, fs, doc, stdout, stderr)
	if helpShown {
		return 0
	}
	if code != 0 {
		return code
	}
	if err := fs.Parse(options); err != nil {
		return 2
	}
	if output != "text" && output != "json" {
		fmt.Fprintf(stderr, "Error: --output must be text or json\n\n%s\n", commandHelp(doc))
		return 2
	}
	if len(positionals) != 1 {
		fmt.Fprintf(stderr, "Error: expected exactly one flow file argument\n\n%s\n", commandHelp(doc))
		return 2
	}

	path := flowFilePath(positionals[0])
	prepared, err := api.PrepareProgram(context.Background(), []api.ProgramSource{{Name: path, Path: path}}, api.ProgramLimits{})
	if err != nil {
		fmt.Fprintln(stderr, "Error:", err)
		return 1
	}
	resolvedID, res, tr, runErr := prepared.TraceFlow(context.Background(), flowID, body, hasBody, headers.values)
	if tr == nil && runErr != nil {
		fmt.Fprintln(stderr, "Error:", runErr)
		return 1
	}
	if output == "json" {
		payload := buildTraceJSON(resolvedID, res, tr, runErr)
		if c := writeJSON(stdout, stderr, payload); c != 0 {
			return c
		}
	} else {
		fmt.Fprintln(stdout, formatTraceText(resolvedID, res, tr, runErr, useColor(stdout)))
	}
	if runErr != nil {
		return 1
	}
	return 0
}

func splitUtilityArgs(args []string, fs *flag.FlagSet, doc cmdDoc, stdout, stderr io.Writer) (options, positionals []string, helpShown bool, exitCode int) {
	for i := 1; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			positionals = append(positionals, args[i+1:]...)
			break
		}
		if a == "--help" || a == "-h" {
			fmt.Fprintln(stdout, commandHelp(doc))
			return nil, nil, true, 0
		}
		if strings.HasPrefix(a, "-") {
			options = append(options, a)
			if f := fs.Lookup(strings.TrimLeft(a, "-")); !strings.Contains(a, "=") && f != nil && takesValue(f) {
				if i+1 == len(args) {
					fmt.Fprintf(stderr, "Error: %s needs a value\n", a)
					return nil, nil, false, 2
				}
				i++
				options = append(options, args[i])
			}
		} else {
			positionals = append(positionals, a)
		}
	}
	return options, positionals, false, 0
}

func resolveTestSources(paths []string) ([]api.ProgramSource, error) {
	var sources []api.ProgramSource
	for _, raw := range paths {
		info, err := os.Stat(raw)
		if err == nil && info.IsDir() {
			entries, err := os.ReadDir(raw)
			if err != nil {
				return nil, fmt.Errorf("read test directory %q: %w", raw, err)
			}
			added := 0
			for _, e := range entries {
				if ext := filepath.Ext(e.Name()); strings.HasPrefix(e.Name(), ".") || ext != ".json" && ext != dilsyntax.Ext {
					continue
				}
				full := filepath.Join(raw, e.Name())
				fi, err := os.Stat(full)
				if err != nil {
					return nil, fmt.Errorf("stat %q: %w", full, err)
				}
				if fi.Mode().IsRegular() {
					sources = append(sources, api.ProgramSource{Name: full, Path: full, Discovered: true})
					added++
				}
			}
			if added == 0 {
				return nil, fmt.Errorf("directory %q contains no visible .json or .dil files", raw)
			}
			continue
		}
		p := flowFilePath(raw)
		sources = append(sources, api.ProgramSource{Name: p, Path: p})
	}
	return sources, nil
}

type traceJSON struct {
	Flow       string             `json:"flow"`
	Status     string             `json:"status"`
	DurationMs int64              `json:"durationMs"`
	Error      string             `json:"error,omitempty"`
	Trail      []string           `json:"trail,omitempty"`
	Output     *traceOutputJSON   `json:"output,omitempty"`
	Steps      []engine.StepTrace `json:"steps"`
}

type traceOutputJSON struct {
	Body    any            `json:"body"`
	Headers map[string]any `json:"headers,omitempty"`
}

func buildTraceJSON(flowID string, res *api.Result, tr *engine.Trace, runErr error) traceJSON {
	status := "ok"
	var errText string
	if runErr != nil {
		status = "error"
		errText = runErr.Error()
	} else if res != nil && res.Err != nil {
		status = "handled"
		errText = res.Err.Error()
	}
	var dur int64
	var trail []string
	var out *traceOutputJSON
	if res != nil {
		dur = res.Duration.Milliseconds()
		trail = res.Trail
		if res.Message != nil {
			out = &traceOutputJSON{
				Body:    res.Message[message.Body],
				Headers: userHeadersOf(res.Message),
			}
		}
	}
	steps := []engine.StepTrace{}
	if tr != nil {
		steps = tr.Snapshot()
	}
	return traceJSON{
		Flow:       flowID,
		Status:     status,
		DurationMs: dur,
		Error:      errText,
		Trail:      trail,
		Output:     out,
		Steps:      steps,
	}
}

func formatTestReport(report *api.TestReport, color bool) string {
	var b strings.Builder
	for _, r := range report.Results {
		if r.Passed {
			fmt.Fprintf(&b, "%s  %s (flow: %s, %dms)\n", paint("PASS", green, color), r.Name, r.Flow, r.DurationMs)
			continue
		}
		fmt.Fprintf(&b, "%s  %s (flow: %s, %dms)\n", paint("FAIL", red, color), r.Name, r.Flow, r.DurationMs)
		for _, f := range r.Failures {
			fmt.Fprintf(&b, "  - %s\n", f)
		}
		if len(r.Trace) > 0 {
			b.WriteString("  Trace:\n")
			b.WriteString(indent(formatStepsTrace(r.Trace, color), "    "))
			b.WriteByte('\n')
		}
	}
	fmt.Fprintf(&b, "\n%d/%d passed, %d failed (%dms)", report.Passed, report.Total, report.Failed, report.DurationMs)
	return b.String()
}

func formatTraceText(flowID string, res *api.Result, tr *engine.Trace, runErr error, color bool) string {
	statusLabel := "ok"
	var errText string
	if runErr != nil {
		statusLabel = "error"
		errText = runErr.Error()
	} else if res != nil && res.Err != nil {
		statusLabel = "handled"
		errText = res.Err.Error()
	}
	var dur int64
	if res != nil {
		dur = res.Duration.Milliseconds()
	}
	var b strings.Builder
	fmt.Fprintf(&b, "TRACE: %s (%s, %dms)\n", flowID, statusLabel, dur)
	var steps []engine.StepTrace
	if tr != nil {
		steps = tr.Snapshot()
	}
	if len(steps) == 0 {
		b.WriteString("\nNo steps executed.\n")
	} else {
		b.WriteByte('\n')
		b.WriteString(formatStepsTrace(steps, color))
		b.WriteByte('\n')
	}
	if errText != "" {
		fmt.Fprintf(&b, "\nError: %s\n", errText)
	}
	if res != nil && res.Message != nil {
		b.WriteString("\nRESULT\n")
		fmt.Fprintf(&b, "  body: %s\n", formatCompactValue(res.Message[message.Body]))
		hdrs := userHeadersOf(res.Message)
		if len(hdrs) > 0 {
			for _, k := range slices.Sorted(maps.Keys(hdrs)) {
				fmt.Fprintf(&b, "  header %s: %s\n", k, formatCompactValue(hdrs[k]))
			}
		}
	}
	return strings.TrimSuffix(b.String(), "\n")
}

func formatStepsTrace(steps []engine.StepTrace, color bool) string {
	rows := make([][]string, 0, len(steps))
	for i, s := range steps {
		stepLabel := s.StepID
		if s.Detached {
			stepLabel += " [detached]"
		}
		diff := formatStepDiff(s, color)
		rows = append(rows, []string{
			fmt.Sprintf("%d", i+1),
			stepLabel,
			s.Kind,
			s.URI,
			fmt.Sprintf("%dms", s.DurationMs),
			diff,
		})
	}
	return renderTable([]string{"#", "STEP", "KIND", "URI", "TIME", "DIFF"}, rows, []bool{true, false, false, false, true, false}, nil)
}

func formatStepDiff(s engine.StepTrace, color bool) string {
	var parts []string
	if s.Error != "" {
		parts = append(parts, paint("ERROR: "+s.Error, red, color))
	}
	if !reflect.DeepEqual(s.BodyBefore, s.BodyAfter) {
		parts = append(parts, fmt.Sprintf("body: %s -> %s", formatCompactValue(s.BodyBefore), formatCompactValue(s.BodyAfter)))
	}
	for _, k := range slices.Sorted(maps.Keys(s.HeadersAdded)) {
		parts = append(parts, fmt.Sprintf("+%s=%s", k, formatCompactValue(s.HeadersAdded[k])))
	}
	for _, k := range slices.Sorted(maps.Keys(s.HeadersChanged)) {
		d := s.HeadersChanged[k]
		parts = append(parts, fmt.Sprintf("~%s: %s -> %s", k, formatCompactValue(d.Before), formatCompactValue(d.After)))
	}
	for _, k := range s.HeadersRemoved {
		parts = append(parts, "-"+k)
	}
	if len(parts) == 0 {
		return "(no changes)"
	}
	return strings.Join(parts, "; ")
}

func formatCompactValue(v any) string {
	if v == nil {
		return "null"
	}
	var s string
	switch x := v.(type) {
	case string:
		b, _ := json.Marshal(x)
		s = string(b)
	case []byte:
		b, _ := json.Marshal(string(x))
		s = string(b)
	default:
		if b, err := json.Marshal(x); err == nil {
			s = string(b)
		} else {
			s = fmt.Sprint(x)
		}
	}
	return shorten(s, 80)
}

func userHeadersOf(m message.Message) map[string]any {
	var out map[string]any
	for k, v := range m {
		if k == message.Body || message.IsMetadata(k) {
			continue
		}
		if out == nil {
			out = map[string]any{}
		}
		out[k] = v
	}
	return out
}

func formatScratchMessage(m message.Message) string {
	var b strings.Builder
	b.WriteString("SCRATCH MESSAGE\n")
	fmt.Fprintf(&b, "  body: %s\n", formatFullValue(m[message.Body]))
	hdrs := userHeadersOf(m)
	if len(hdrs) == 0 {
		b.WriteString("  headers: (none)")
		return b.String()
	}
	b.WriteString("  headers:")
	for _, k := range slices.Sorted(maps.Keys(hdrs)) {
		fmt.Fprintf(&b, "\n    %s: %s", k, formatFullValue(hdrs[k]))
	}
	return b.String()
}

func formatFullValue(v any) string {
	if v == nil {
		return "null"
	}
	switch x := v.(type) {
	case []byte:
		b, _ := json.Marshal(string(x))
		return string(b)
	default:
		if b, err := json.Marshal(x); err == nil {
			return string(b)
		}
		return fmt.Sprint(x)
	}
}

func handleShellMsg(rest string, scratch *message.Message, con *console) {
	args, err := utilityArguments(rest)
	if err != nil {
		con.fail("%v", err)
		return
	}
	if len(args) == 0 {
		con.block(formatScratchMessage(*scratch))
		return
	}
	var (
		clearFlag bool
		headers   headerFlag
		bodyParts []string
	)
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--clear":
			clearFlag = true
		case a == "--header":
			if i+1 >= len(args) {
				con.usage("msg", "--header needs key=value")
				return
			}
			i++
			if err := headers.Set(args[i]); err != nil {
				con.usage("msg", "%v", err)
				return
			}
		case strings.HasPrefix(a, "--header="):
			if err := headers.Set(strings.TrimPrefix(a, "--header=")); err != nil {
				con.usage("msg", "%v", err)
				return
			}
		case strings.HasPrefix(a, "-"):
			con.usage("msg", "unknown option '%s'", a)
			return
		default:
			bodyParts = append(bodyParts, a)
		}
	}
	if clearFlag {
		*scratch = message.New(nil)
	}
	if len(bodyParts) > 0 {
		(*scratch)[message.Body] = strings.Join(bodyParts, " ")
	}
	for k, v := range headers.values {
		(*scratch)[k] = v
	}
	con.block(formatScratchMessage(*scratch))
}

func handleShellEval(rest string, scratch message.Message, con *console) {
	trimmed := strings.TrimSpace(rest)
	lang, expr, ok := strings.Cut(trimmed, " ")
	expr = strings.TrimSpace(expr)
	if lang == "" || !ok || expr == "" {
		con.usage("eval", "expected <simple|map|xpath|jsonpath> <expression>")
		return
	}
	val, typeName, err := api.EvalExpression(context.Background(), lang, expr, scratch)
	if err != nil {
		con.fail("%v", err)
		return
	}
	con.say("(%s) %s", typeName, formatFullValue(val))
}

func handleShellExec(rest string, scratch message.Message, con *console) {
	chain := strings.TrimSpace(rest)
	if chain == "" {
		con.usage("exec", "missing dil-chain argument")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	res, tr, err := api.ExecInline(ctx, chain, scratch)
	cancel()
	if tr == nil && err != nil {
		con.fail("%v", err)
		return
	}
	con.block(formatTraceText("_repl", res, tr, err, con.color))
}

func handleShellTrace(rest string, eng *api.Engine, flowError func(error), con *console) {
	trimmed := strings.TrimSpace(rest)
	id, body, _ := strings.Cut(trimmed, " ")
	if id == "" {
		con.usage("trace", "missing flow argument")
		return
	}
	flow, err := eng.GetFlow(id)
	if err != nil {
		flowError(err)
		return
	}
	m := flow.NewMessage()
	if body != "" {
		m[api.Body] = body
	}
	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	res, tr, err := flow.Trace(ctx, m)
	cancel()
	con.block(formatTraceText(id, res, tr, err, con.color))
}
