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
			"DIF CLI: flow hello (../examples/hello.json) is started.",
			"> send hello\n  message sent to flow hello; its result is in the flow's log\n",
			"> exit\n  exit: 2 messages processed, 0 failed\n",
		}, map[string][]string{"hello": {
			"flow hello loaded from ../examples/hello.json\n",
			"flow hello started\n",
			`message 1: {"body":"HELLO WORLD","greeting":"hello","metadata.timestamp":"`,
			"trail: source:hello-source -> action:hello-action -> sink:hello-sink (",
			`message 2: {"body":"bye"`,
			"flow hello stopped (dif exits)\n",
		}}},
		{"stop keeps the CLI", []string{"start", "../examples/hello.json"}, "stop hello\nstatus\nsend hello\nstart hello\nsend hello\nexit\n", 0, []string{
			"> stop hello\n  flow hello stopped\n",
			"> status\n  1 flows: 0 messages processed, 0 failed\n",
			"> send hello\n  error: cannot send: flow is stopped\n",
			"> start hello\n  flow hello started\n",
			"exit: 1 messages processed, 0 failed",
		}, map[string][]string{"hello": {"flow hello stopped\n", "flow hello started\n", `message 1: {"body":"HELLO WORLD"`}}},
		{"force stop", []string{"start", "../examples/hello.json"}, "stop hello --force\nstop --force hello\nstop\nstop a b\nexit\n", 0, []string{
			"> stop hello --force\n  flow hello stopped (forced)\n",
			"> stop --force hello\n  error: cannot stop: flow is stopped\n",
			"> stop\n  error: usage: stop <flow> [--force]\n",
			"> stop a b\n  error: usage: stop <flow> [--force]\n",
		}, map[string][]string{"hello": {"flow hello stopped (forced)\n"}}},
		{"pause and start", []string{"start", "../examples/hello.json"}, "pause hello\nsend hello\npause hello\nstart hello\nstart hello\npause hello\nresume hello\nexit\n", 0, []string{
			"> pause hello\n  flow hello paused\n",
			"> send hello\n  error: cannot send: flow is paused\n",
			"> pause hello\n  error: cannot pause: flow is paused\n",
			"> start hello\n  flow hello started\n> start hello\n  error: cannot start: flow is started\n",
			"> resume hello\n  flow hello started\n",
		}, map[string][]string{"hello": {"flow hello paused\n", "flow hello started\n"}}},
		{"multiple flows", []string{"start", "../examples/hello.json", "../examples/timer.json"}, "pause timer\nlist\nlist started\nlist paused\nlist stopped\nlist bogus\nstart timer\nlist paused\nexit\n", 0, []string{
			"DIF CLI: flow hello (../examples/hello.json) is started.\nDIF CLI: flow timer (../examples/timer.json) is started.\n",
			"> pause timer\n  flow timer paused\n",
			"> list\n  ID      STATUS    STARTUP TIME          UPTIME\n  hello   started   20",
			"\n  timer   paused    20",
			"> list started\n  ID      STATUS    STARTUP TIME          UPTIME\n  hello   started   20",
			"> list paused\n  ID      STATUS   STARTUP TIME          UPTIME\n  timer   paused   20",
			"> list stopped\n  ID   STATUS   STARTUP TIME   UPTIME\n> ",
			"> list bogus\n  error: unknown state \"bogus\"",
			"> start timer\n  flow timer started\n",
		}, map[string][]string{"hello": {"flow hello started\n"}, "timer": {"flow timer paused\n"}}},
		{"unknown flow", []string{"start", "../examples/hello.json"}, "start nope\npause nope\nsend nope\nsend\nlog nope\nexit\n", 0, []string{
			"> start nope\n  error: flow nope not found\n",
			"> pause nope\n  error: flow nope not found\n",
			"> send nope\n  error: flow nope not found\n",
			"> send\n  error: flow  not found\n",
			"> log nope\n  error: flow nope not found\n",
		}, nil},
		{"help and unknown", []string{"start", "../examples/hello.json"}, "help\nquit\nexit\n", 0, []string{
			"> help\n  commands:\n",
			"    run <flow.json>...",
			"    send <flow> [body]",
			"    stop <flow> [--force]",
			"    log <flow> [--lines n]",
			"    list [state]",
			"    exit ",
			"> quit\n  unknown command \"quit\"",
		}, nil},
		{"timer", []string{"start", "../examples/timer.json"}, "list\nexit\n", 0, []string{
			"> list\n  ID      STATUS    STARTUP TIME          UPTIME\n  timer   started   20",
			"exit:",
		}, nil},
		{"duplicate flow id", []string{"start", "../examples/hello.json", "../examples/hello.json"}, "", 1, nil, nil},
		{"no args opens the CLI", nil, "list\nstart hello\nload ../examples/hello.json ../examples/timer.json\nlist\nstart hello\nsend hello\nlist started\nexit\n", 0, []string{
			"DIF CLI: no flows yet; add them with \"load <flow.json>\" or \"run <flow.json>\".\n",
			"> list\n  ID   STATUS   STARTUP TIME   UPTIME\n",
			"> start hello\n  error: flow hello not found\n",
			"  flow hello loaded from ../examples/hello.json; it is stopped\n  flow timer loaded from ../examples/timer.json; it is stopped\n",
			"> list\n  ID      STATUS    STARTUP TIME   UPTIME\n  hello   stopped   -              -\n  timer   stopped   -              -\n",
			"> start hello\n  flow hello started\n",
			"> list started\n  ID      STATUS    STARTUP TIME          UPTIME\n  hello   started   20",
			"exit: 1 messages processed, 0 failed",
		}, map[string][]string{"hello": {"message 1:"}, "timer": {"flow timer loaded from ../examples/timer.json\n"}}},
		{"load errors", nil, "load\nload nope.json\nload ../examples/hello.json\nload ../examples/hello.json\nexit\n", 0, []string{
			"> load\n  error: usage: load <flow.json>...\n",
			"> load nope.json\n  error: open nope.json:",
			"> load ../examples/hello.json\n  error: flow hello is already registered\n",
		}, nil},
		{"run", nil, "run\nrun ../examples/hello.json nope.json\nrun ../examples/hello.json\nsend hello\nexit\n", 0, []string{
			"> run\n  error: usage: run <flow.json>...\n",
			"> run ../examples/hello.json nope.json\n  flow hello started (loaded from ../examples/hello.json)\n  error: open nope.json:",
			"> run ../examples/hello.json\n  error: flow hello is already registered\n",
			"> send hello\n  message sent to flow hello",
			"exit: 1 messages processed, 0 failed",
		}, map[string][]string{"hello": {"flow hello loaded from ../examples/hello.json\n", "flow hello started (loaded from ../examples/hello.json)\n", "message 1:"}}},
		{"log usage", []string{"start", "../examples/hello.json"}, "log\nlog hello --lines\nlog hello --lines 0\nlog hello --lines x\nlog hello extra\nlog --lines 2\nexit\n", 0, []string{
			"> log\n  error: usage: log <flow> [--lines n]\n",
			"> log hello --lines\n  error: usage: log <flow> [--lines n]\n",
			"> log hello --lines 0\n  error: --lines needs a positive number, not \"0\"\n",
			"> log hello --lines x\n  error: --lines needs a positive number, not \"x\"\n",
			"> log hello extra\n  error: usage: log <flow> [--lines n]\n",
			"> log --lines 2\n  error: usage: log <flow> [--lines n]\n",
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

func TestLogLines(t *testing.T) {
	useLogDir(t)
	var stdout, stderr bytes.Buffer
	stdin := "stop hello\nstart hello\nlog hello --lines 2\nlog hello --lines 100\nexit\n"
	if code := Run([]string{"start", "../examples/hello.json"}, strings.NewReader(stdin), &stdout, &stderr); code != 0 {
		t.Fatalf("exit code = %d (stderr: %s)", code, stderr.String())
	}
	const ts = `\d{4}/\d\d/\d\d \d\d:\d\d:\d\d\.\d{6} `
	for _, want := range []string{
		`> log hello --lines 2\n  ` + ts + `flow hello stopped\n  ` + ts + `flow hello started\n> `,
		`> log hello --lines 100\n  ` + ts + `flow hello loaded from \.\./examples/hello\.json\n  ` + ts + `flow hello started\n  ` +
			ts + `flow hello stopped\n  ` + ts + `flow hello started\n> `,
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
		`  error: flow bad: step timer-source: timer: option period: want integer, got "x"` + "\n",
		"  flow timer started (loaded from " + good + ")\n",
		"> list\n  ID      STATUS    STARTUP TIME          UPTIME\n  timer   started   20",
		"> log timer\n  following " + filepath.Join(dir, "timer.log") + "; press Enter to stop\n",
		" step timer-log: traceid=",
		"source=timer} body=tick 3\n",
		`message 3: {"body":"tick 3"`,
		"  stopped following " + filepath.Join(dir, "timer.log") + "\n> log timer --lines 1\n",
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

func TestTable(t *testing.T) {
	start := time.Date(2026, 10, 2, 9, 30, 0, 0, time.Local)
	flows := []api.FlowStatus{
		{ID: "hello", State: api.Started, Since: start},
		{ID: "timer-long-id", State: api.Paused, Since: start.Add(time.Minute)},
		{ID: "x", State: api.Stopped},
	}
	want := "ID              STATUS    STARTUP TIME          UPTIME\n" +
		"hello           started   2026-10-02 09:30:00   1h2m3s\n" +
		"timer-long-id   paused    2026-10-02 09:31:00   1h1m3s\n" +
		"x               stopped   -                     -"
	if got := table(flows, start.Add(time.Hour+2*time.Minute+3*time.Second+500*time.Millisecond)); got != want {
		t.Errorf("table =\n%s\nwant\n%s", got, want)
	}
	if got := table(nil, start); got != "ID   STATUS   STARTUP TIME   UPTIME" {
		t.Errorf("empty table = %q, want the header only", got)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
