// Package cli implements the dif command line.
package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"text/tabwriter"
	"time"

	"dif/api"
)

const usage = `usage: dif                        open the CLI without flows; add them with "load" or "run"
       dif start <flow.json>...    load and start the flows, then open the CLI`

const help = `commands:
  load <flow.json>...     register the flows in the files; they stay stopped until started
  run <flow.json>...      load the flows and start them
  send <flow> [body]      send the flow's configured message; with <body>, use that as its body
  start <flow>            start the flow (again, after stop), or continue it after pause
  pause <flow>            stop taking new messages until the flow is started or resumed
  resume <flow>           continue a paused flow
  stop <flow> [--force]   stop the flow once its current message is done;
                          --force stops it at once, and that message may be lost
  log <flow> [--lines n]  follow the flow's log live (press Enter to stop);
                          with --lines, show its last n lines
  list [state]            list the flows and their state; state is started, paused or stopped
  status                  show the message counts
  help                    show this help
  exit                    stop all flows and exit dif (Ctrl+C does the same)
<flow> is the flow id from the DIL file. Each flow logs to its own file in ` + "`logs`" + `.`

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

	con := &console{w: stdout, interactive: isTerminal(stdin)}
	var processed, failed atomic.Int64

	eng := api.NewEngine()

	var logsMu sync.Mutex
	logs := map[string]*flowLog{} // by flow id
	flowLogOf := func(id string) (*flowLog, error) {
		logsMu.Lock()
		defer logsMu.Unlock()
		if l := logs[id]; l != nil {
			return l, nil
		}
		return nil, fmt.Errorf("flow %s not found", id)
	}

	// load reads the flow in path and registers it; it stays stopped.
	// Its results and the lines of its log steps go to its log file.
	load := func(path string) (*api.Flow, error) {
		var fl *flowLog // set before the flow starts, so onResult can use it
		var count atomic.Int64
		flow, err := api.Load(path, func(res *api.Result, err error) {
			n := count.Add(1)
			processed.Add(1)
			if err != nil {
				failed.Add(1)
				fl.logger.Printf("message %d failed: %v", n, err)
				return
			}
			msg, _ := json.Marshal(res.Message)
			fl.logger.Printf("message %d: %s trail: %s (%d ms)", n, msg, strings.Join(res.Trail, " -> "), res.Duration.Milliseconds())
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

	if len(paths) == 0 {
		fmt.Fprintln(stdout, `DIF CLI: no flows yet; add them with "load <flow.json>" or "run <flow.json>".`)
	}
	for _, path := range paths {
		flow, err := load(path)
		if err == nil {
			err = flow.Start()
		}
		if err != nil {
			fmt.Fprintln(stderr, "error:", err)
			running := eng.ListFlows("")
			eng.Shutdown()
			closeLogs(running)
			return 1
		}
		if l, err := flowLogOf(flow.ID()); err == nil {
			l.logger.Printf("flow %s started", flow.ID())
		}
		fmt.Fprintf(stdout, "DIF CLI: flow %s (%s) is started.\n", flow.ID(), path)
	}
	fmt.Fprintf(stdout, "Type a command at the %q prompt; \"help\" lists all commands.\n", prompt)

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
			switch cmd {
			case "":
			case "load", "run":
				if len(fields) == 0 {
					con.say("error: usage: %s <flow.json>...", cmd)
				}
				for _, path := range fields {
					flow, err := load(path)
					if err != nil {
						con.say("error: %v", err)
					} else if cmd == "load" {
						con.say("flow %s loaded from %s; it is %s", flow.ID(), path, flow.State())
					} else if err := lifecycle(eng.StartFlow, flow.ID(), " (loaded from "+path+")"); err != nil {
						con.say("error: %v", err)
					}
				}
			case "send":
				flow, err := eng.GetFlow(id)
				if err != nil {
					con.say("error: %v", err)
					break
				}
				m := flow.NewMessage()
				if body != "" {
					m[api.Body] = body
				}
				if err := flow.Send(m); err != nil {
					con.say("error: %v", err)
				} else {
					con.say("message sent to flow %s; its result is in the flow's log", id)
				}
			case "start", "pause", "resume":
				op := map[string]func(string) error{"start": eng.StartFlow, "pause": eng.PauseFlow, "resume": eng.ResumeFlow}[cmd]
				if err := lifecycle(op, id, ""); err != nil {
					con.say("error: %v", err)
				}
			case "stop":
				force := slices.Contains(fields, "--force")
				fields = slices.DeleteFunc(fields, func(f string) bool { return f == "--force" })
				if len(fields) != 1 {
					con.say("error: usage: stop <flow> [--force]")
					break
				}
				op, note := eng.StopFlow, ""
				if force {
					op, note = eng.ForceStopFlow, " (forced)"
				}
				if err := lifecycle(op, fields[0], note); err != nil {
					con.say("error: %v", err)
				}
			case "log":
				id, lines, err := parseLog(fields)
				if err != nil {
					con.say("error: %v", err)
					break
				}
				l, err := flowLogOf(id)
				if err != nil {
					con.say("error: %v", err)
					break
				}
				if lines > 0 {
					tail, err := l.Tail(lines)
					if err != nil {
						con.say("error: %v", err)
					}
					for _, line := range tail {
						con.say("%s", line)
					}
					break
				}
				con.say("following %s; press Enter to stop", l.path)
				stop, err := l.Follow(followLines, func(line string) { con.say("%s", line) })
				if err != nil {
					con.say("error: %v", err)
					break
				}
				more := sc.Scan() // any line, usually empty, ends following
				stop()
				con.say("stopped following %s", l.path)
				if !more {
					return
				}
			case "list":
				state := api.State(id)
				if state != "" && state != api.Started && state != api.Paused && state != api.Stopped {
					con.say("error: unknown state %q; use started, paused or stopped", id)
					break
				}
				con.say("%s", table(eng.ListFlows(state), time.Now()))
			case "status":
				con.say("%d flows: %d messages processed, %d failed", len(eng.ListFlows("")), processed.Load(), failed.Load())
			case "help":
				con.say("%s", help)
			case "exit":
				exit()
				return
			default:
				con.say("unknown command %q; type \"help\" for all commands", cmd)
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
		con.say("error: %v", srcErr)
	}
	closeLogs(running)
	con.close("exit: %d messages processed, %d failed", processed.Load(), failed.Load())

	if srcErr != nil || failed.Load() > 0 {
		return 1
	}
	return 0
}

// parseLog parses the arguments of the log command: the flow id and, with
// --lines, the number of last lines to show; 0 means follow the log.
func parseLog(args []string) (id string, lines int, err error) {
	for i := 0; i < len(args); i++ {
		switch {
		case args[i] == "--lines" && i+1 < len(args):
			i++
			if lines, err = strconv.Atoi(args[i]); err != nil || lines < 1 {
				return "", 0, fmt.Errorf("--lines needs a positive number, not %q", args[i])
			}
		case id == "" && !strings.HasPrefix(args[i], "-"):
			id = args[i]
		default:
			id = ""
			i = len(args)
		}
	}
	if id == "" {
		return "", 0, fmt.Errorf("usage: log <flow> [--lines n]")
	}
	return id, lines, nil
}

// table formats flows as a table with a header row, like docker ps.
func table(flows []api.FlowStatus, now time.Time) string {
	var b strings.Builder
	w := tabwriter.NewWriter(&b, 0, 0, 3, ' ', 0)
	fmt.Fprint(w, "ID\tSTATUS\tSTARTUP TIME\tUPTIME")
	for _, f := range flows {
		started, uptime := "-", "-"
		if !f.Since.IsZero() {
			started = f.Since.Format(time.DateTime)
			uptime = now.Sub(f.Since).Truncate(time.Second).String()
		}
		fmt.Fprintf(w, "\n%s\t%s\t%s\t%s", f.ID, f.State, started, uptime)
	}
	w.Flush()
	return b.String()
}

const prompt = "> "

// console keeps user input and program output apart: input follows the
// prompt, output is indented. Output can arrive at any time from the flow,
// so it is written above a fresh prompt when the user is typing.
type console struct {
	mu          sync.Mutex
	w           io.Writer
	interactive bool // a terminal echoes the typed commands itself
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

// say writes program output, indented.
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
		fmt.Fprintf(c.w, "  %s\n", line)
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
