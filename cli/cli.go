// Package cli implements the dif command line.
package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"

	"dif/api"
)

const usage = "usage: dif start <flow.json>"

const help = `commands:
  send [body]  send the flow's configured message; with <body>, use that as its body
  start        start the flow (again, after stop)
  pause        stop taking new messages until the flow is resumed
  resume       continue a paused flow
  stop         stop the flow; dif keeps running
  status       show the flow's state and message counts
  help         show this help
  exit         stop the flow and exit dif (Ctrl+C does the same)`

// Run executes the command line args and returns the process exit code.
// dif runs until "exit" or Ctrl+C; the flow can be stopped and started in between.
func Run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) != 2 || args[0] != "start" {
		fmt.Fprintln(stderr, usage)
		return 2
	}

	con := &console{w: stdout, interactive: isTerminal(stdin)}
	var processed, failed atomic.Int64

	flow, err := api.Load(args[1], func(res *api.Result, err error) {
		n := processed.Add(1)
		if err != nil {
			failed.Add(1)
			con.say("message %d failed: %v", n, err)
			return
		}
		msg, _ := json.Marshal(res.Message) // Message fields always marshal
		con.say("message %d: %s\n  trail: %s (%d ms)", n, msg, strings.Join(res.Trail, " -> "), res.Duration.Milliseconds())
	})
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	if err := flow.Start(); err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}

	fmt.Fprintf(stdout, "DIF CLI: flow %s is started.\n", args[1])
	fmt.Fprintf(stdout, "Type a command at the %q prompt; \"help\" lists all commands.\n", prompt)

	quit := make(chan struct{})
	var quitOnce sync.Once
	exit := func() { quitOnce.Do(func() { close(quit) }) }

	lifecycle := map[string]func() error{
		"start":  flow.Start,
		"pause":  flow.Pause,
		"resume": flow.Resume,
		"stop":   flow.Stop,
	}

	go func() {
		sc := bufio.NewScanner(stdin)
		for {
			line, ok := con.read(sc)
			if !ok {
				return // end of input: dif keeps running until Ctrl+C
			}
			cmd, body, _ := strings.Cut(line, " ")
			switch cmd {
			case "":
			case "send":
				m := flow.NewMessage()
				if body != "" {
					m.Body = body
				}
				if err := flow.Send(m); err != nil {
					con.say("error: %v", err)
				} // on success the result is shown once the flow has processed it
			case "start", "pause", "resume", "stop":
				if err := lifecycle[cmd](); err != nil {
					con.say("error: %v", err)
				} else {
					con.say("flow %s", flow.State())
				}
			case "status":
				con.say("flow is %s: %d messages processed, %d failed", flow.State(), processed.Load(), failed.Load())
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
	if flow.State() != api.Stopped {
		flow.Stop()
	}
	srcErr := flow.Wait()
	if srcErr != nil {
		con.say("error: %v", srcErr)
	}
	con.close("exit: %d messages processed, %d failed", processed.Load(), failed.Load())

	if srcErr != nil || failed.Load() > 0 {
		return 1
	}
	return 0
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
