// Package cli implements the dif command line.
package cli

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/signal"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"dif/api"
	"dif/engine"
	"dif/message"
)

const usage = `usage: dif                        open the CLI without flows; add them with "load" or "run"
       dif start <flow.json>...    load and start the flows, then open the CLI`

// followLines is the number of past lines "log <flow>" shows before following, as tail -f does.
const followLines = 10

// Run executes the command line args and returns the process exit code.
// dif runs until "exit" or Ctrl+C; flows can be loaded, started and stopped in between.
// Flows run in the background and write their output to their log file, not to the console.
func Run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	var paths []string
	switch {
	case len(args) == 0:
	case len(args) >= 2 && args[0] == "start":
		paths = args[1:]
	default:
		fmt.Fprintln(stderr, usage)
		return 2
	}

	con := &console{w: stdout, interactive: isTerminal(stdin), color: useColor(stdout)}
	eng := api.NewEngine()

	var logsMu sync.Mutex
	logs := map[string]*flowLog{} // by flow id
	flowLogOf := func(id string) (*flowLog, error) {
		logsMu.Lock()
		defer logsMu.Unlock()
		if l := logs[id]; l != nil {
			return l, nil
		}
		_, err := eng.GetFlow(id) // every registered flow has a log, so this explains the miss
		if err == nil {
			err = fmt.Errorf("flow %s has no log", id)
		}
		return nil, err
	}

	// load reads the flow in path and registers it; it stays stopped.
	// Its results and the lines of its log steps go to its log file.
	load := func(path string) (*api.Flow, error) {
		var fl *flowLog      // set before the flow starts, so onResult can use it
		var seq atomic.Int64 // numbers the messages in the log; the engine counts them
		flow, err := api.Load(path, func(res *api.Result, err error) {
			n := seq.Add(1)
			if err != nil {
				fl.logger.Printf("message %d failed: %v", n, err)
				return
			}
			// The original body repeats a body; leave it out of the log.
			m := maps.Clone(res.Message)
			delete(m, message.OriginalBody)
			msg, _ := json.Marshal(m)
			handled := ""
			if res.Err != nil {
				handled = fmt.Sprintf(" (error route handled: %v)", res.Err)
			}
			fl.logger.Printf("message %d: %s trail: %s (%d ms)%s", n, msg, strings.Join(res.Trail, " -> "), res.Duration.Milliseconds(), handled)
		})
		if err != nil {
			return nil, err
		}

		fl, err = openFlowLog(flow.ID())
		if err != nil {
			return nil, err
		}
		flow.SetLogger(fl.logger)
		if err := eng.Add(flow.Runner); err != nil {
			fl.Close()
			return nil, err
		}
		logsMu.Lock()
		logs[flow.ID()] = fl
		logsMu.Unlock()
		fl.logger.Printf("flow %s loaded from %s", flow.ID(), path)
		return flow, nil
	}

	// closeLogs closes the flows' logs once they have stopped; flows that were
	// still running when dif exits get a last line saying so.
	closeLogs := func(before []api.FlowStatus) {
		logsMu.Lock()
		defer logsMu.Unlock()
		for _, f := range before {
			if l := logs[f.ID]; l != nil && f.State != api.Stopped {
				l.logger.Printf("flow %s stopped (dif exits)", f.ID)
			}
		}
		for _, l := range logs {
			l.Close()
		}
	}

	// lifecycle runs a lifecycle operation on a flow and logs its new state.
	lifecycle := func(op func(string) error, id, note string) error {
		if err := op(id); err != nil {
			return err
		}
		flow, err := eng.GetFlow(id)
		if err != nil {
			return err
		}
		if l, err := flowLogOf(id); err == nil {
			l.logger.Printf("flow %s %s%s", id, flow.State(), note)
		}
		con.say("flow %s %s%s", id, flow.State(), note)
		return nil
	}

	for _, path := range paths {
		flow, err := load(path)
		if err == nil {
			err = flow.Start()
		}
		if err != nil {
			fmt.Fprintln(stderr, "Error:", err)
			running := eng.ListFlows("")
			eng.Shutdown()
			closeLogs(running)
			return 1
		}
		if l, err := flowLogOf(flow.ID()); err == nil {
			l.logger.Printf("flow %s started", flow.ID())
		}
	}
	fmt.Fprintf(stdout, "DIF - Data Integration Framework\nVersion: %s\n\n", Version)
	if len(paths) == 0 {
		fmt.Fprintln(stdout, "No flows loaded.")
	} else {
		fmt.Fprintf(stdout, "Flows: %d\n", len(paths))
	}
	fmt.Fprint(stdout, "Use 'help' for available commands.\n\n")

	// flowError reports err, explaining an unknown flow.
	flowError := func(err error) {
		var nf *engine.NotFoundError
		if !errors.As(err, &nf) {
			con.fail("%v", err)
			return
		}
		hint := "Use 'list' to see loaded flows."
		if len(nf.Suggestions) > 0 {
			hint = "Did you mean: " + strings.Join(nf.Suggestions, ", ") + "?\n" + hint
		}
		con.fail("flow '%s' not found\n\n%s", nf.ID, hint)
	}

	quit := make(chan struct{})
	var quitOnce sync.Once
	exit := func() { quitOnce.Do(func() { close(quit) }) }

	go func() {
		sc := bufio.NewScanner(stdin)
		for {
			line, ok := con.read(sc)
			if !ok {
				return // end of input: dif keeps running until Ctrl+C
			}
			cmd, rest, _ := strings.Cut(line, " ")
			fields := strings.Fields(rest)
			id, body, _ := strings.Cut(strings.TrimSpace(rest), " ")
			// args checks that the command has min to max fields; missing names a missing one.
			args := func(min, max int, missing string) bool {
				switch {
				case len(fields) < min:
					con.usage(cmd, "missing %s argument", missing)
				case len(fields) > max:
					con.usage(cmd, "unexpected argument '%s'", fields[max])
				default:
					return true
				}
				return false
			}
			switch cmd {
			case "":
			case "load", "run":
				if !args(1, len(fields), "flow file") {
					break
				}
				for _, path := range fields {
					flow, err := load(path)
					if err != nil {
						con.fail("%v", err)
					} else if cmd == "load" {
						con.say("flow %s loaded from %s; it is %s", flow.ID(), path, flow.State())
					} else if err := lifecycle(eng.StartFlow, flow.ID(), " (loaded from "+path+")"); err != nil {
						flowError(err)
					}
				}
			case "send":
				if !args(1, len(fields), "flow") {
					break
				}
				flow, err := eng.GetFlow(id)
				if err != nil {
					flowError(err)
					break
				}
				m := flow.NewMessage()
				if body != "" {
					m[api.Body] = body
				}
				if err := flow.Send(m); err != nil {
					con.fail("%v", err)
				} else {
					con.say("message sent to flow %s; its result is in the flow's log", id)
				}
			case "start", "pause", "resume":
				if !args(1, 1, "flow") {
					break
				}
				op := map[string]func(string) error{"start": eng.StartFlow, "pause": eng.PauseFlow, "resume": eng.ResumeFlow}[cmd]
				if err := lifecycle(op, id, ""); err != nil {
					flowError(err)
				}
			case "stop":
				force := slices.Contains(fields, "--force")
				fields = slices.DeleteFunc(fields, func(f string) bool { return f == "--force" })
				if !args(1, 1, "flow") {
					break
				}
				op, note := eng.StopFlow, ""
				if force {
					op, note = eng.ForceStopFlow, " (forced)"
				}
				if err := lifecycle(op, fields[0], note); err != nil {
					flowError(err)
				}
			case "log":
				id, lines, err := parseLog(fields)
				if err != nil {
					con.usage(cmd, "%v", err)
					break
				}
				l, err := flowLogOf(id)
				if err != nil {
					flowError(err)
					break
				}
				if lines > 0 {
					tail, err := l.Tail(lines)
					if err != nil {
						con.fail("%v", err)
					}
					for _, line := range tail {
						con.say("%s", line)
					}
					break
				}
				con.say("following %s; press Enter to stop", l.path)
				stop, err := l.Follow(followLines, func(line string) { con.say("%s", line) })
				if err != nil {
					con.fail("%v", err)
					break
				}
				more := sc.Scan() // any line, usually empty, ends following
				stop()
				con.say("stopped following %s", l.path)
				if !more {
					return
				}
			case "list", "ps":
				if !args(0, 1, "state") {
					break
				}
				state := api.State(id)
				if state != "" && state != api.Started && state != api.Paused && state != api.Stopped {
					con.usage(cmd, "unknown state '%s'; use started, paused or stopped", id)
					break
				}
				if len(eng.ListFlows("")) == 0 {
					con.block("No flows loaded.")
					break
				}
				con.block(flowsText(eng.ListFlows(state), time.Now(), con.color))
			case "stats":
				if !args(0, 1, "flow") {
					break
				}
				if id == "" {
					con.block(statsText(eng.ListFlows("")))
					break
				}
				flow, err := eng.GetFlow(id)
				if err != nil {
					flowError(err)
					break
				}
				con.block(flowStatsText(flow.Status(), time.Now(), con.color))
			case "status":
				flows := eng.ListFlows("")
				completed, failed := totals(flows)
				con.say("%d flows: %d messages processed, %d failed", len(flows), completed+failed, failed)
			case "catalog":
				if !args(0, 1, "step") {
					break
				}
				steps := api.StepCatalog()
				if id == "" {
					con.block(catalogText(steps))
					break
				}
				var texts []string
				for _, s := range steps {
					if s.Name == id {
						texts = append(texts, stepText(s))
					}
				}
				if texts == nil {
					con.fail("step '%s' not found\n\nUse 'catalog' to see available steps.", id)
					break
				}
				con.block(strings.Join(texts, "\n\n"))
			case "help":
				if !args(0, 1, "command") {
					break
				}
				if id == "" {
					con.block(helpText())
				} else if c, ok := findCommand(id); ok {
					con.block(commandHelp(c))
				} else {
					con.fail("unknown command '%s'\n\nUse 'help' to see available commands.", id)
				}
			case "exit":
				exit()
				return
			default:
				con.fail("unknown command '%s'\n\nUse 'help' to see available commands.", cmd)
			}
		}
	}()

	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, os.Interrupt)
	defer signal.Stop(interrupt)
	go func() {
		select {
		case <-interrupt:
			exit()
		case <-quit:
		}
	}()

	<-quit
	running := eng.ListFlows("")
	srcErr := eng.Shutdown()
	if srcErr != nil {
		con.fail("%v", srcErr)
	}
	closeLogs(running)
	completed, failed := totals(eng.ListFlows(""))
	con.close("exit: %d messages processed, %d failed", completed+failed, failed)

	if srcErr != nil || failed > 0 {
		return 1
	}
	return 0
}

// parseLog parses the arguments of the log command: the flow id and, with
// --lines, the number of last lines to show; 0 means follow the log.
func parseLog(args []string) (id string, lines int, err error) {
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--lines":
			if i++; i == len(args) {
				return "", 0, fmt.Errorf("--lines needs a number")
			}
			if lines, err = strconv.Atoi(args[i]); err != nil || lines < 1 {
				return "", 0, fmt.Errorf("--lines needs a positive number, not '%s'", args[i])
			}
		case strings.HasPrefix(args[i], "-"):
			return "", 0, fmt.Errorf("unknown option '%s'", args[i])
		case id == "":
			id = args[i]
		default:
			return "", 0, fmt.Errorf("unexpected argument '%s'", args[i])
		}
	}
	if id == "" {
		return "", 0, fmt.Errorf("missing flow argument")
	}
	return id, lines, nil
}

// totals returns the message counts of all flows together.
func totals(flows []api.FlowStatus) (completed, failed int64) {
	for _, f := range flows {
		completed += f.Completed
		failed += f.Failed
	}
	return completed, failed
}

const prompt = "> "

// console keeps user input and program output apart: input follows the
// prompt, output does not. Output can arrive at any time from the flow,
// so it is written above a fresh prompt when the user is typing.
type console struct {
	mu          sync.Mutex
	w           io.Writer
	interactive bool // a terminal echoes the typed commands itself
	color       bool // the terminal shows ANSI colors
	reading     bool // the prompt is showing
	closed      bool // nothing is written after close
}

// read shows the prompt and returns the next command line, or false at end of input.
func (c *console) read(sc *bufio.Scanner) (string, bool) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return "", false
	}
	if c.interactive {
		fmt.Fprint(c.w, prompt)
		c.reading = true
	}
	c.mu.Unlock()

	ok := sc.Scan()

	c.mu.Lock()
	defer c.mu.Unlock()
	c.reading = false
	if !ok {
		return "", false
	}
	line := strings.TrimSpace(sc.Text())
	if !c.interactive && !c.closed {
		fmt.Fprintf(c.w, "%s%s\n", prompt, line) // echo, so a transcript shows what was entered
	}
	return line, true
}

// block writes a block of output, such as a table, between blank lines.
func (c *console) block(s string) { c.say("\n%s\n", s) }

// fail reports an error as "Error: <message>"; lines with a hint may follow the message.
func (c *console) fail(format string, a ...any) {
	c.say("%s %s", paint("Error:", red, c.color), fmt.Sprintf(format, a...))
}

// usage reports a wrong use of command cmd, followed by its usage.
func (c *console) usage(cmd, format string, a ...any) {
	doc, _ := findCommand(cmd)
	c.fail("%s\n\nUsage:\n  %s", fmt.Sprintf(format, a...), doc.usageLine())
}

// say writes program output.
func (c *console) say(format string, a ...any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.write(format, a...)
	if c.reading {
		fmt.Fprint(c.w, prompt)
	}
}

// close writes a last line of output; the console is silent afterwards.
func (c *console) close(format string, a ...any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.write(format, a...)
	c.closed = true
}

func (c *console) write(format string, a ...any) {
	if c.closed {
		return
	}
	if c.reading {
		fmt.Fprint(c.w, "\r") // print over the waiting prompt
	}
	for _, line := range strings.Split(fmt.Sprintf(format, a...), "\n") {
		fmt.Fprintln(c.w, line)
	}
}

// isTerminal reports whether r is an interactive terminal.
func isTerminal(r io.Reader) bool {
	f, ok := r.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	return err == nil && fi.Mode()&os.ModeCharDevice != 0
}
