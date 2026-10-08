package cli

import (
	"cmp"
	"fmt"
	"strconv"
	"strings"
	"time"

	"dif/api"
)

// Version is the version of dif.
const Version = "0.1.0"

// cmdDoc documents a command for help and for usage errors.
type cmdDoc struct {
	group, name, args, desc string
	detail                  string // shown by "help <command>"
}

var commands = []cmdDoc{
	{"FLOW MANAGEMENT", "load", "<flow.json>...", "Load flows without starting them", "The flows stay stopped until started. Each flow logs to its own file in `logs`."},
	{"FLOW MANAGEMENT", "run", "<flow.json>...", "Load and start flows", ""},
	{"FLOW MANAGEMENT", "start", "<flow>", "Start a stopped flow", "Starting a paused flow resumes it."},
	{"FLOW MANAGEMENT", "pause", "<flow>", "Pause a flow", "A paused flow takes no new messages until it is started or resumed."},
	{"FLOW MANAGEMENT", "resume", "<flow>", "Resume a paused flow", ""},
	{"FLOW MANAGEMENT", "stop", "<flow> [--force]", "Stop a flow", "The flow stops once its current message is done.\n--force stops it at once; that message may be lost."},
	{"MESSAGING", "send", "<flow> [body]", "Send a message to a flow", "Sends the flow's configured message; with [body], that is its body.\nThe result is in the flow's log; use request to wait for the reply."},
	{"MESSAGING", "request", "<flow> [body]", "Send a message and show the reply", "Sends the flow's configured message, as send does, and shows the reply:\nthe message the flow ends with, or the message at a setoneway step.\nWaits at most 30 seconds."},
	{"MONITORING", "list", "[state]", "List flows and their state", "state is started, paused or stopped."},
	{"MONITORING", "ps", "[state]", "Alias for list", "state is started, paused or stopped."},
	{"MONITORING", "stats", "[flow]", "Show message statistics", "With [flow], show the details of one flow."},
	{"MONITORING", "status", "", "Show the message counts on one line", ""},
	{"MONITORING", "log", "<flow> [--lines n]", "Follow or display a flow's log", "Follows the log live; press Enter to stop.\nWith --lines, shows its last n lines."},
	{"DEVELOPMENT", "catalog", "[step]", "List available steps", "With [step], show its description and options."},
	{"DEVELOPMENT", "init", "<flow.json> [--template hello|timer|file|http] [--id name]", "Create a starter flow", "Refuses to overwrite an existing file. Default template: hello. File input moves to inbox/.done. The http template serves HTTPS on 127.0.0.1:9002 and needs security/server-identity.p12 and DIF_SERVER_IDENTITY_PASSWORD."},
	{"DEVELOPMENT", "validate", "<flow.json>... [--output text|json]", "Validate flow structure, references and option schemas", "Offline validation does not construct processors. Expressions, processor-specific semantics and external resources are checked when loading a flow."},
	{"DEVELOPMENT", "describe", "<flow.json> [--output text|json]", "Describe steps, links, error handling and configuration", "Option values, URI paths and expression text are omitted to avoid exposing credentials."},
	{"SYSTEM", "version", "[--output text|json]", "Show version and build information", "Includes revision, Go version and platform."},
	{"SYSTEM", "completion", "[command line]", "Suggest commands and arguments", "Examples: completion, completion val, completion init --template t, completion catalog pass. Lists suggestions without running a command."},
	{"SYSTEM", "help", "[command]", "Show this help", ""},
	{"SYSTEM", "exit", "", "Stop all flows and exit", "Ctrl+C does the same."},
}

func findCommand(name string) (cmdDoc, bool) {
	for _, c := range commands {
		if c.name == name {
			return c, true
		}
	}
	return cmdDoc{}, false
}

// usageLine returns the command with its arguments, such as "stop <flow> [--force]".
func (c cmdDoc) usageLine() string {
	return strings.TrimSpace(c.name + " " + c.args)
}

// helpText returns the overview of all commands.
func helpText() string {
	w := 0 // one column width for all groups
	for _, c := range commands {
		w = max(w, len(c.usageLine()))
	}
	var b strings.Builder
	b.WriteString("DIF COMMANDS\n")
	group := ""
	for _, c := range commands {
		if c.group != group {
			group = c.group
			b.WriteString("\n" + group + "\n")
		}
		fmt.Fprintf(&b, "  %-*s   %s\n", w, c.usageLine(), c.desc)
	}
	b.WriteString("\nFLOW STATE\n")
	b.WriteString(indent(renderTable(nil, [][]string{
		{"started", "Flow is processing messages"},
		{"paused", "Flow is loaded but takes no new messages"},
		{"stopped", "Flow is loaded but not running"},
	}, nil, nil), "  "))
	b.WriteString("\n\n<flow> is the flow id from the DIL file. Use 'help <command>' for details.")
	b.WriteString("\nFlow filenames default to .json; the extension can be omitted.")
	b.WriteString("\nStart this shell by running dif.exe without arguments (PowerShell: .\\dif.exe).\nTry: init hello.json, validate hello.json, describe hello.json, run hello.json, request hello.")
	return b.String()
}

// commandHelp returns the help of one command.
func commandHelp(c cmdDoc) string {
	s := "Usage:\n  " + c.usageLine() + "\n\n" + c.desc + "."
	if c.detail != "" {
		s += "\n\n" + c.detail
	}
	switch c.name {
	case "load", "run", "init", "validate", "describe":
		s += "\n\nFlow filenames default to .json: '" + c.name + " hello' uses hello.json. Explicit extensions are preserved."
	}
	return s
}

// flowsText returns the flows as a table, for list and ps.
func flowsText(flows []api.FlowStatus, now time.Time, color bool) string {
	rows := make([][]string, len(flows))
	for i, f := range flows {
		up := "-"
		if !f.Since.IsZero() {
			up = uptime(now.Sub(f.Since))
		}
		rows[i] = []string{f.ID, status(f.State, color), count(f.Completed), count(f.Failed), up}
	}
	return "FLOWS\n\n" +
		renderTable([]string{"ID", "STATUS", "COMPLETED", "FAILED", "UPTIME"}, rows, []bool{false, false, true, true}, nil) +
		"\n\n" + plural(len(flows), "flow")
}

// statsText returns the message statistics of the flows, with a total.
func statsText(flows []api.FlowStatus) string {
	rows := make([][]string, len(flows))
	var completed, failed int64
	for i, f := range flows {
		rows[i] = []string{f.ID, count(f.Completed), count(f.Failed), count(f.Completed + f.Failed)}
		completed += f.Completed
		failed += f.Failed
	}
	return "DIF MESSAGE STATISTICS\n\n" + renderTable(
		[]string{"FLOW", "COMPLETED", "FAILED", "TOTAL"}, rows, []bool{false, true, true, true},
		[]string{"TOTAL", count(completed), count(failed), count(completed + failed)})
}

// flowStatsText returns the statistics of one flow.
func flowStatsText(f api.FlowStatus, now time.Time, color bool) string {
	started, up := "-", "-"
	if !f.Since.IsZero() {
		started, up = f.Since.Format(time.DateTime), uptime(now.Sub(f.Since))
	}
	return "FLOW: " + f.ID + "\n\n" + renderTable(nil, [][]string{
		{"Status", status(f.State, color)},
		{"Completed", count(f.Completed)},
		{"Failed", count(f.Failed)},
		{"Total", count(f.Completed + f.Failed)},
		{"Started", started},
		{"Uptime", up},
	}, nil, nil)
}

// catalogText returns the steps as a table.
func catalogText(steps []api.StepInfo) string {
	rows := make([][]string, len(steps))
	for i, s := range steps {
		rows[i] = []string{s.Name, strings.ToUpper(s.Kind), cmp.Or(s.Pattern, "-"), shorten(firstSentence(s.Description), 60)}
	}
	return "DIF STEP CATALOG\n\n" + renderTable([]string{"NAME", "TYPE", "PATTERN", "DESCRIPTION"}, rows, nil, nil) +
		"\n\n" + plural(len(steps), "step")
}

// stepText returns the description and options of a step.
func stepText(s api.StepInfo) string {
	text := "STEP: " + s.Name + "\nTYPE: " + strings.ToUpper(s.Kind) + "\n"
	if s.Pattern != "" {
		text += "PATTERN: " + s.Pattern + "\n"
	}
	text += "\n" + s.Description + "\n\nOptions:"
	if len(s.Options) == 0 {
		return text + " none"
	}
	rows := make([][]string, len(s.Options))
	for i, o := range s.Options {
		def, req := "-", "no"
		switch {
		case o.Default == "":
			def = `""`
		case o.Default != nil:
			def = fmt.Sprint(o.Default)
		}
		if o.Required {
			req = "yes"
		}
		rows[i] = []string{o.Name, o.Type, def, req, o.Description}
	}
	return text + "\n" + indent(renderTable([]string{"NAME", "TYPE", "DEFAULT", "REQUIRED", "DESCRIPTION"}, rows, nil, nil), "  ")
}

func count(n int64) string { return strconv.FormatInt(n, 10) }
