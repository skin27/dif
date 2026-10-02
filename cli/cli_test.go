package cli

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"dif/api"
)

// useLogDir makes the flows of the test log to a temporary directory.
func useLogDir(t *testing.T) string {
	t.Helper()
	dir, old := t.TempDir(), logDir
	logDir = dir
	t.Cleanup(func() { logDir = old })
	return dir
}

func readLog(t *testing.T, dir, id string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, id+".log"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestRun(t *testing.T) {
	tests := []struct {
		name  string
		args  []string
		stdin string
		code  int
		out   []string
		logs  map[string][]string // flow id -> lines in its log
	}{
		{"hello", []string{"start", "../examples/hello.json"}, "send hello\nsend hello bye\nexit\n", 0, []string{
			"DIF - Data Integration Framework\nVersion: " + Version + "\n\nFlows: 1\nUse 'help' for available commands.\n\n",
			"> send hello\nmessage sent to flow hello; its result is in the flow's log\n",
			"> exit\nexit: 2 messages processed, 0 failed\n",
		}, map[string][]string{"hello": {
			"flow hello loaded from ../examples/hello.json\n",
			"flow hello started\n",
			`message 1: {"body":"HELLO WORLD","greeting":"hello","metadata.timestamp":"`,
			"trail: source:hello-source -> action:hello-action -> sink:hello-sink (",
			`message 2: {"body":"bye"`,
			"flow hello stopped (dif exits)\n",
		}}},
		{"stop keeps the CLI", []string{"start", "../examples/hello.json"}, "stop hello\nstatus\nsend hello\nstart hello\nsend hello\nexit\n", 0, []string{
			"> stop hello\nflow hello stopped\n",
			"> status\n1 flows: 0 messages processed, 0 failed\n",
			"> send hello\nError: cannot send: flow is stopped\n",
			"> start hello\nflow hello started\n",
			"exit: 1 messages processed, 0 failed",
		}, map[string][]string{"hello": {"flow hello stopped\n", "flow hello started\n", `message 1: {"body":"HELLO WORLD"`}}},
		{"force stop", []string{"start", "../examples/hello.json"}, "stop hello --force\nstop --force hello\nstop\nstop a b\nexit\n", 0, []string{
			"> stop hello --force\nflow hello stopped (forced)\n",
			"> stop --force hello\nError: cannot stop: flow is stopped\n",
			"> stop\nError: missing flow argument\n\nUsage:\n  stop <flow> [--force]\n",
			"> stop a b\nError: unexpected argument 'b'\n\nUsage:\n  stop <flow> [--force]\n",
		}, map[string][]string{"hello": {"flow hello stopped (forced)\n"}}},
		{"pause and start", []string{"start", "../examples/hello.json"}, "pause hello\nsend hello\npause hello\nstart hello\nstart hello\npause hello\nresume hello\nexit\n", 0, []string{
			"> pause hello\nflow hello paused\n",
			"> send hello\nError: cannot send: flow is paused\n",
			"> pause hello\nError: cannot pause: flow is paused\n",
			"> start hello\nflow hello started\n> start hello\nError: cannot start: flow is started\n",
			"> resume hello\nflow hello started\n",
		}, map[string][]string{"hello": {"flow hello paused\n", "flow hello started\n"}}},
		{"multiple flows", []string{"start", "../examples/hello.json", "../examples/timer.json"}, "pause timer\nlist\nlist started\nlist paused\nlist stopped\nlist bogus\nlist a b\nstart timer\nexit\n", 0, []string{
			"Flows: 2\n",
			"> pause timer\nflow timer paused\n",
			"> list\n\nFLOWS\n\nID      STATUS      COMPLETED   FAILED   UPTIME\n─────",
			"\nhello   ● STARTED           0        0   0s\ntimer   ● PAUSED            0        0   0s\n\n2 flows\n\n> list started",
			"> list started\n\nFLOWS\n\nID      STATUS      COMPLETED   FAILED   UPTIME\n",
			"\nhello   ● STARTED           0        0   0s\n\n1 flow\n\n> list paused",
			"\ntimer   ● PAUSED           0        0   0s\n\n1 flow\n\n> list stopped",
			"> list stopped\n\nFLOWS\n\nID   STATUS   COMPLETED   FAILED   UPTIME\n",
			"\n\n0 flows\n\n> list bogus",
			"> list bogus\nError: unknown state 'bogus'; use started, paused or stopped\n\nUsage:\n  list [state]\n",
			"> list a b\nError: unexpected argument 'b'\n\nUsage:\n  list [state]\n",
			"> start timer\nflow timer started\n",
		}, map[string][]string{"hello": {"flow hello started\n"}, "timer": {"flow timer paused\n"}}},
		{"unknown flow", []string{"start", "../examples/hello.json"}, "start nope\npause nope\nsend nope\nsend\nlog nope\nstats nope\nexit\n", 0, []string{
			"> start nope\nError: flow 'nope' not found\n\nUse 'list' to see loaded flows.\n",
			"> pause nope\nError: flow 'nope' not found\n",
			"> send nope\nError: flow 'nope' not found\n",
			"> send\nError: missing flow argument\n\nUsage:\n  send <flow> [body]\n",
			"> log nope\nError: flow 'nope' not found\n",
			"> stats nope\nError: flow 'nope' not found\n\nUse 'list' to see loaded flows.\n",
		}, nil},
		{"suggestions", []string{"start", "../examples/hello.json", "../examples/timer.json"}, "pause time\npause timer\nlog helo --lines 1\nstop nope\nexit\n", 0, []string{
			"> pause time\nError: flow 'time' not found\n\nDid you mean: timer?\nUse 'list' to see loaded flows.\n",
			"> pause timer\nflow timer paused\n",
			"> log helo --lines 1\nError: flow 'helo' not found\n\nDid you mean: hello?\n",
			"> stop nope\nError: flow 'nope' not found\n",
		}, nil},
		{"missing and extra arguments", []string{"start", "../examples/hello.json"}, "start\npause\nresume\nstart hello extra\nstats a b\ncatalog a b\nhelp a b\nexit\n", 0, []string{
			"> start\nError: missing flow argument\n\nUsage:\n  start <flow>\n",
			"> pause\nError: missing flow argument\n\nUsage:\n  pause <flow>\n",
			"> resume\nError: missing flow argument\n\nUsage:\n  resume <flow>\n",
			"> start hello extra\nError: unexpected argument 'extra'\n\nUsage:\n  start <flow>\n",
			"> stats a b\nError: unexpected argument 'b'\n\nUsage:\n  stats [flow]\n",
			"> catalog a b\nError: unexpected argument 'b'\n\nUsage:\n  catalog [step]\n",
			"> help a b\nError: unexpected argument 'b'\n\nUsage:\n  help [command]\n",
		}, nil},
		{"help and unknown", []string{"start", "../examples/hello.json"}, "help\nhelp stop\nhelp nope\nquit\nexit\n", 0, []string{
			"> help\n\nDIF COMMANDS\n\nFLOW MANAGEMENT\n  load <flow.json>... ",
			"\n  run <flow.json>... ",
			"\nMESSAGING\n  send <flow> [body] ",
			"\n  stop <flow> [--force] ",
			"\nMONITORING\n  list [state] ",
			"\n  ps [state] ",
			"\n  stats [flow] ",
			"\n  log <flow> [--lines n] ",
			"\nDEVELOPMENT\n  catalog [step] ",
			"\n  exit ",
			"\nFLOW STATE\n  started ",
			"> help stop\n\nUsage:\n  stop <flow> [--force]\n\nStop a flow.\n\nThe flow stops once its current message is done.\n--force stops it at once",
			"> help nope\nError: unknown command 'nope'\n\nUse 'help' to see available commands.\n",
			"> quit\nError: unknown command 'quit'\n\nUse 'help' to see available commands.\n",
		}, nil},
		{"timer", []string{"start", "../examples/timer.json"}, "list\nexit\n", 0, []string{
			"> list\n\nFLOWS\n\nID      STATUS      COMPLETED   FAILED   UPTIME\n",
			"\ntimer   ● STARTED   ",
			"exit:",
		}, nil},
		{"duplicate flow id", []string{"start", "../examples/hello.json", "../examples/hello.json"}, "", 1, nil, nil},
		{"no args opens the CLI", nil, "list\nstats\nstart hello\nload ../examples/hello.json ../examples/timer.json\nlist\nstart hello\nsend hello\nlist started\nexit\n", 0, []string{
			"DIF - Data Integration Framework\nVersion: " + Version + "\n\nNo flows loaded.\nUse 'help' for available commands.\n\n",
			"> list\n\nNo flows loaded.\n\n",
			"> stats\n\nDIF MESSAGE STATISTICS\n\nFLOW    COMPLETED   FAILED   TOTAL\n",
			"\nTOTAL           0        0       0\n\n> start hello",
			"> start hello\nError: flow 'hello' not found\n",
			"flow hello loaded from ../examples/hello.json; it is stopped\nflow timer loaded from ../examples/timer.json; it is stopped\n",
			"\nhello   ● STOPPED           0        0   -\ntimer   ● STOPPED           0        0   -\n\n2 flows\n",
			"> start hello\nflow hello started\n",
			"exit: 1 messages processed, 0 failed",
		}, map[string][]string{"hello": {"message 1:"}, "timer": {"flow timer loaded from ../examples/timer.json\n"}}},
		{"load errors", nil, "load\nload nope.json\nload ../examples/hello.json\nload ../examples/hello.json\nexit\n", 0, []string{
			"> load\nError: missing flow file argument\n\nUsage:\n  load <flow.json>...\n",
			"> load nope.json\nError: open nope.json:",
			"> load ../examples/hello.json\nError: flow hello is already registered\n",
		}, nil},
		{"run", nil, "run\nrun ../examples/hello.json nope.json\nrun ../examples/hello.json\nsend hello\nexit\n", 0, []string{
			"> run\nError: missing flow file argument\n\nUsage:\n  run <flow.json>...\n",
			"> run ../examples/hello.json nope.json\nflow hello started (loaded from ../examples/hello.json)\nError: open nope.json:",
			"> run ../examples/hello.json\nError: flow hello is already registered\n",
			"> send hello\nmessage sent to flow hello",
			"exit: 1 messages processed, 0 failed",
		}, map[string][]string{"hello": {"flow hello loaded from ../examples/hello.json\n", "flow hello started (loaded from ../examples/hello.json)\n", "message 1:"}}},
		{"log usage", []string{"start", "../examples/hello.json"}, "log\nlog hello --lines\nlog hello --lines 0\nlog hello --lines x\nlog hello extra\nlog --lines 2\nlog hello -n\nexit\n", 0, []string{
			"> log\nError: missing flow argument\n\nUsage:\n  log <flow> [--lines n]\n",
			"> log hello --lines\nError: --lines needs a number\n\nUsage:\n  log <flow> [--lines n]\n",
			"> log hello --lines 0\nError: --lines needs a positive number, not '0'\n",
			"> log hello --lines x\nError: --lines needs a positive number, not 'x'\n",
			"> log hello extra\nError: unexpected argument 'extra'\n",
			"> log --lines 2\nError: missing flow argument\n",
			"> log hello -n\nError: unknown option '-n'\n",
		}, nil},
		{"start without files", []string{"start"}, "", 2, nil, nil},
		{"run is a command, not an argument", []string{"run", "../examples/hello.json"}, "", 2, nil, nil},
		{"missing file", []string{"start", "nope.json"}, "", 1, nil, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := useLogDir(t)
			var stdout, stderr bytes.Buffer
			if code := Run(tt.args, strings.NewReader(tt.stdin), &stdout, &stderr); code != tt.code {
				t.Errorf("exit code = %d, want %d (stderr: %s)", code, tt.code, stderr.String())
			}
			for _, want := range tt.out {
				if !strings.Contains(stdout.String(), want) {
					t.Errorf("stdout = %q\nwant containing %q", stdout.String(), want)
				}
			}
			for id, wants := range tt.logs {
				log := readLog(t, dir, id)
				for _, want := range wants {
					if !strings.Contains(log, want) {
						t.Errorf("log of %s = %q\nwant containing %q", id, log, want)
					}
				}
			}
		})
	}
}

// block returns the output of the command entered as "> cmd" in a transcript.
func block(t *testing.T, out, cmd string) string {
	t.Helper()
	_, after, ok := strings.Cut(out, "> "+cmd+"\n")
	if !ok {
		t.Fatalf("no command %q in output:\n%s", cmd, out)
	}
	result, _, _ := strings.Cut(after, "\n> ")
	return result
}

// TestStats sends messages to two flows, one of which fails them, and leaves a third without messages.
func TestStats(t *testing.T) {
	dir := useLogDir(t)
	hello, err := os.ReadFile("../examples/hello.json")
	must(t, err)
	failing := filepath.Join(t.TempDir(), "failing.json")
	idle := filepath.Join(t.TempDir(), "idle.json")
	failingFlow := strings.Replace(string(hello), `"id": "hello",`, `"id": "failing",`, 1)
	failingFlow = strings.Replace(failingFlow, `"uri": "passthrough"`, `"uri": "xmltojson"`, 1) // the body is no XML
	must(t, os.WriteFile(failing, []byte(failingFlow), 0o644))
	must(t, os.WriteFile(idle, []byte(strings.Replace(string(hello), `"id": "hello",`, `"id": "idle",`, 1)), 0o644))

	stdin, w := io.Pipe()
	var stdout, stderr bytes.Buffer
	done := make(chan int)
	go func() { done <- Run([]string{"start", "../examples/hello.json", failing}, stdin, &stdout, &stderr) }()
	fmt.Fprintf(w, "load %s\nsend hello\nsend hello\nsend failing\n", idle)
	// Messages are processed in the background; wait until all three are logged.
	logged := func(id, line string) bool {
		b, _ := os.ReadFile(filepath.Join(dir, id+".log"))
		return bytes.Contains(b, []byte(line))
	}
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		if logged("hello", "message 2:") && logged("failing", "message 1 failed") {
			break
		}
	}
	fmt.Fprint(w, "stats\nstats hello\nstats idle\nlist\nps\nstatus\nexit\n")
	w.Close()
	<-done
	out := stdout.String()

	stats := block(t, out, "stats")
	for _, want := range []string{
		"DIF MESSAGE STATISTICS\n",
		"\nFLOW      COMPLETED   FAILED   TOTAL\n",
		"\nfailing           0        1       1\nhello             2        0       2\nidle              0        0       0\n",
		"\nTOTAL             2        1       3\n",
	} {
		if !strings.Contains(stats, want) {
			t.Errorf("stats = %q\nwant containing %q", stats, want)
		}
	}
	for cmd, wants := range map[string][]string{
		"stats hello": {"FLOW: hello\n", "\nStatus      ● STARTED\n", "\nCompleted   2\n", "\nFailed      0\n", "\nTotal       2\n", "\nStarted     20"},
		"stats idle":  {"FLOW: idle\n", "\nStatus      ● STOPPED\n", "\nTotal       0\n", "\nStarted     -\n", "\nUptime      -\n"},
		"list":        {"\nfailing   ● STARTED           0        1   ", "\nhello     ● STARTED           2        0   ", "\nidle      ● STOPPED           0        0   -\n", "\n3 flows\n"},
	} {
		got := block(t, out, cmd)
		for _, want := range wants {
			if !strings.Contains(got, want) {
				t.Errorf("%s = %q\nwant containing %q", cmd, got, want)
			}
		}
	}
	if want := "> status\n3 flows: 3 messages processed, 1 failed\n"; !strings.Contains(out, want) {
		t.Errorf("stdout = %q\nwant containing %q", out, want)
	}
	if list, ps := block(t, out, "list"), block(t, out, "ps"); list != ps {
		t.Errorf("ps = %q\nwant the same as list: %q", ps, list)
	}
}

func TestCatalog(t *testing.T) {
	useLogDir(t)
	var stdout, stderr bytes.Buffer
	if code := Run(nil, strings.NewReader("catalog\ncatalog timer\ncatalog file\ncatalog nope\nexit\n"), &stdout, &stderr); code != 0 {
		t.Fatalf("exit code = %d (stderr: %s)", code, stderr.String())
	}
	out := stdout.String()

	catalog := block(t, out, "catalog")
	if !strings.HasPrefix(catalog, "\nDIF STEP CATALOG\n\nNAME ") {
		t.Errorf("catalog = %q, want the title and header first", catalog)
	}
	for _, s := range api.StepCatalog() {
		want := regexp.MustCompile(`\n` + s.Name + ` +` + strings.ToUpper(s.Kind) + ` +\S`)
		if !want.MatchString(catalog) {
			t.Errorf("catalog has no row for %s (%s):\n%s", s.Name, s.Kind, catalog)
		}
	}
	if want := fmt.Sprintf("\n%d steps\n", len(api.StepCatalog())); !strings.Contains(catalog, want) {
		t.Errorf("catalog = %q\nwant containing %q", catalog, want)
	}

	for cmd, wants := range map[string][]string{
		"catalog timer": {"STEP: timer\nTYPE: SOURCE\n\nProduces a message on every tick.", "\nOptions:\n  NAME ", "\n  period        integer   1000      no         Milliseconds between two ticks\n"},
		"catalog file":  {"STEP: file\nTYPE: SINK\n", "STEP: file\nTYPE: SOURCE\n", "\n  path ", " yes "},
	} {
		got := block(t, out, cmd)
		for _, want := range wants {
			if !strings.Contains(got, want) {
				t.Errorf("%s = %q\nwant containing %q", cmd, got, want)
			}
		}
	}
	if want := "> catalog nope\nError: step 'nope' not found\n\nUse 'catalog' to see available steps.\n"; !strings.Contains(out, want) {
		t.Errorf("stdout = %q\nwant containing %q", out, want)
	}
}

func TestLogLines(t *testing.T) {
	useLogDir(t)
	var stdout, stderr bytes.Buffer
	stdin := "stop hello\nstart hello\nlog hello --lines 2\nlog hello --lines 100\nexit\n"
	if code := Run([]string{"start", "../examples/hello.json"}, strings.NewReader(stdin), &stdout, &stderr); code != 0 {
		t.Fatalf("exit code = %d (stderr: %s)", code, stderr.String())
	}
	const ts = `\d{4}/\d\d/\d\d \d\d:\d\d:\d\d\.\d{6} `
	for _, want := range []string{
		`> log hello --lines 2\n` + ts + `flow hello stopped\n` + ts + `flow hello started\n> `,
		`> log hello --lines 100\n` + ts + `flow hello loaded from \.\./examples/hello\.json\n` + ts + `flow hello started\n` +
			ts + `flow hello stopped\n` + ts + `flow hello started\n> `,
	} {
		if !regexp.MustCompile(want).MatchString(stdout.String()) {
			t.Errorf("stdout = %q\nwant matching %q", stdout.String(), want)
		}
	}
}

// TestFlowsRunInTheBackground runs a fast timer flow: its output goes to its
// log file, not to the console, until the log is followed.
func TestFlowsRunInTheBackground(t *testing.T) {
	dir := useLogDir(t)
	timer, err := os.ReadFile("../examples/timer.json")
	if err != nil {
		t.Fatal(err)
	}
	good := filepath.Join(t.TempDir(), "good.json")
	bad := filepath.Join(t.TempDir(), "bad.json")
	fast := strings.Replace(string(timer), `"period": 5000`, `"period": 10, "repeatCount": 3`, 1)
	invalid := strings.Replace(string(timer), `"period": 5000`, `"period": "x"`, 1)
	if fast == string(timer) || invalid == string(timer) {
		t.Fatal("examples/timer.json has no period 5000 to replace")
	}
	invalid = strings.Replace(invalid, `"id": "timer",`, `"id": "bad",`, 1)
	must(t, os.WriteFile(good, []byte(fast), 0o644))
	must(t, os.WriteFile(bad, []byte(invalid), 0o644))

	stdin, w := io.Pipe()
	go func() {
		fmt.Fprintf(w, "load %s\nrun %s\n", bad, good)
		// The timer runs in the background; wait until its three messages are logged.
		for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
			if b, _ := os.ReadFile(filepath.Join(dir, "timer.log")); bytes.Contains(b, []byte("message 3:")) {
				break
			}
		}
		fmt.Fprint(w, "list\nlog timer\n\nlog timer --lines 1\nexit\n") // the empty line (Enter) stops following
		w.Close()
	}()
	var stdout, stderr bytes.Buffer
	if code := Run(nil, stdin, &stdout, &stderr); code != 0 {
		t.Errorf("exit code = %d, want 0 (stderr: %s)", code, stderr.String())
	}
	out := stdout.String()

	for _, want := range []string{
		"\nError: flow bad: step timer-source: timer: option period: want integer, got \"x\"\n",
		"\nflow timer started (loaded from " + good + ")\n",
		"> list\n\nFLOWS\n\nID      STATUS      COMPLETED   FAILED   UPTIME\n",
		"\ntimer   ● STARTED           3        0   ",
		"> log timer\nfollowing " + filepath.Join(dir, "timer.log") + "; press Enter to stop\n",
		" step timer-log: traceid=",
		"source=timer} body=tick 3\n",
		`message 3: {"body":"tick 3"`,
		"\nstopped following " + filepath.Join(dir, "timer.log") + "\n> log timer --lines 1\n",
		"exit: 3 messages processed, 0 failed",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout = %q\nwant containing %q", out, want)
		}
	}
	if i, j := strings.Index(out, "step timer-log"), strings.Index(out, "following "); i < j {
		t.Errorf("flow output appeared on the console before the log was followed:\n%s", out)
	}

	log := readLog(t, dir, "timer")
	if n := strings.Count(log, " step timer-log: traceid="); n != 3 {
		t.Errorf("log has %d log step lines, want 3:\n%s", n, log)
	}
	if _, err := os.Stat(filepath.Join(dir, "bad.log")); err == nil {
		t.Error("the invalid flow got a log file")
	}
}

func TestFollow(t *testing.T) {
	useLogDir(t)
	l, err := openFlowLog("f/1")
	must(t, err)
	defer l.Close()
	if filepath.Base(l.path) != "f_1.log" {
		t.Errorf("path = %s, want the flow id made safe as a file name", l.path)
	}
	for i := 1; i <= 12; i++ {
		l.logger.Printf("old %d", i)
	}

	var got []string
	stop, err := l.Follow(followLines, func(line string) { got = append(got, line[strings.Index(line, " ")+1:]) })
	must(t, err)
	l.logger.Print("new 1")
	l.logger.Print("new 2")
	stop()
	l.logger.Print("after stop")

	var want []string
	for i := 3; i <= 12; i++ {
		want = append(want, fmt.Sprintf("old %d", i))
	}
	want = append(want, "new 1", "new 2")
	// Lines are "<date> <time> <text>"; got holds "<time> <text>".
	for i := range got {
		got[i] = got[i][strings.Index(got[i], " ")+1:]
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("followed %q\nwant %q", got, want)
	}
	if lines, _ := l.Tail(1); len(lines) != 1 || !strings.HasSuffix(lines[0], " after stop") {
		t.Errorf("tail = %q, want the line written after stop in the file", lines)
	}
}

func TestTail(t *testing.T) {
	dir := t.TempDir()
	write := func(name, content string) string {
		path := filepath.Join(dir, name)
		must(t, os.WriteFile(path, []byte(content), 0o644))
		return path
	}
	var long strings.Builder
	for i := 1; i <= 2000; i++ {
		fmt.Fprintf(&long, "line %d\n", i)
	}

	tests := []struct {
		name, content string
		n             int
		want          []string
	}{
		{"last two", "a\nb\nc\n", 2, []string{"b", "c"}},
		{"more than there are", "a\nb\n", 5, []string{"a", "b"}},
		{"no trailing newline", "a\nb", 1, []string{"b"}},
		{"empty", "", 3, nil},
		{"many chunks", long.String(), 3, []string{"line 1998", "line 1999", "line 2000"}},
		{"across a chunk boundary", long.String(), 600, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tail(write(tt.name, tt.content), tt.n)
			must(t, err)
			if tt.want == nil && tt.content != "" {
				all := strings.Split(strings.TrimSuffix(tt.content, "\n"), "\n")
				tt.want = all[len(all)-tt.n:]
			}
			if strings.Join(got, "|") != strings.Join(tt.want, "|") {
				t.Errorf("tail = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRenderTable(t *testing.T) {
	rows := [][]string{
		{"hello", status(api.Started, true), "142", "3"},
		{"timer-long-id", status(api.Paused, true), "7", "0"},
	}
	want := "ID              STATUS      COMPLETED   FAILED\n" +
		"──────────────────────────────────────────────\n" +
		"hello           " + green + "● STARTED" + reset + "         142        3\n" +
		"timer-long-id   " + yellow + "● PAUSED" + reset + "            7        0\n" +
		"──────────────────────────────────────────────\n" +
		"TOTAL                             149        3"
	got := renderTable([]string{"ID", "STATUS", "COMPLETED", "FAILED"}, rows, []bool{false, false, true, true}, []string{"TOTAL", "", "149", "3"})
	if got != want {
		t.Errorf("table =\n%s\nwant\n%s", got, want)
	}
	if got := renderTable(nil, [][]string{{"a", "1"}, {"bcd", "2"}}, nil, nil); got != "a     1\nbcd   2" {
		t.Errorf("table without header = %q", got)
	}
	if got := status(api.Stopped, false); got != "● STOPPED" {
		t.Errorf("status without color = %q", got)
	}
}

func TestUptime(t *testing.T) {
	for d, want := range map[time.Duration]string{
		0:                                    "0s",
		9*time.Second + 900*time.Millisecond: "9s",
		2*time.Minute + 14*time.Second:       "2m 14s",
		time.Hour + 3*time.Minute + 59*time.Second: "1h 03m",
		50*time.Hour + 30*time.Minute:              "2d 02h",
	} {
		if got := uptime(d); got != want {
			t.Errorf("uptime(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestShorten(t *testing.T) {
	for _, tt := range []struct{ s, want string }{
		{"short", "short"},
		{"one two three four", "one two..."},
		{"one two, three", "one two..."},
		{"abcdefghijklmnop", "abcdefghi..."},
	} {
		if got := shorten(tt.s, 12); got != tt.want {
			t.Errorf("shorten(%q) = %q, want %q", tt.s, got, tt.want)
		}
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
