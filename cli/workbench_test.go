package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"dif/api"
)

func TestCLITestCommand(t *testing.T) {
	exampleFile := filepath.Join("..", "api", "testdata", "testing", "orders_test.dil")
	exampleDir := filepath.Join("..", "api", "testdata", "testing")

	// 1. Passing text output.
	var out, errBuf bytes.Buffer
	code := Run([]string{"test", exampleFile}, strings.NewReader(""), &out, &errBuf)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d; stdout=%s stderr=%s", code, out.String(), errBuf.String())
	}
	if !strings.Contains(out.String(), "PASS") || !strings.Contains(out.String(), "2/2 passed") {
		t.Fatalf("unexpected test output:\n%s", out.String())
	}

	// 2. Directory input + JSON output.
	out.Reset()
	errBuf.Reset()
	code = Run([]string{"test", exampleDir, "--output", "json"}, strings.NewReader(""), &out, &errBuf)
	if code != 0 {
		t.Fatalf("expected exit 0 for dir, got %d; stderr=%s", code, errBuf.String())
	}
	var report api.TestReport
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("invalid JSON report: %v\n%s", err, out.String())
	}
	if report.Total != 2 || report.Passed != 2 || report.Failed != 0 {
		t.Fatalf("unexpected JSON report: %+v", report)
	}

	// 3. Failing test returns exit code 1 with trace.
	dir := t.TempDir()
	failFile := filepath.Join(dir, "fail_test.dil")
	failSrc := `flow echo {
  message:echo as src
  -> setbody(language: "simple", expression: "${uppercase(${body})}") as upper
  -> passthrough as done
}

test "wrong expectation" {
  flow: "echo"
  input: {body: "hi"}
  expect: {body: "WRONG"}
}
`
	if err := os.WriteFile(failFile, []byte(failSrc), 0644); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	errBuf.Reset()
	code = Run([]string{"test", failFile}, strings.NewReader(""), &out, &errBuf)
	if code != 1 {
		t.Fatalf("expected exit 1 on failure, got %d", code)
	}
	if !strings.Contains(out.String(), "FAIL") || !strings.Contains(out.String(), "Trace:") || !strings.Contains(out.String(), "upper") {
		t.Fatalf("expected FAIL with trace output, got:\n%s", out.String())
	}

	// 4. Zero tests returns exit code 1.
	noTestFile := filepath.Join(dir, "notests.dil")
	if err := os.WriteFile(noTestFile, []byte("flow empty {\n  message:empty as src\n  -> passthrough as done\n}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	errBuf.Reset()
	code = Run([]string{"test", noTestFile}, strings.NewReader(""), &out, &errBuf)
	if code != 1 || !strings.Contains(errBuf.String(), "no tests found") {
		t.Fatalf("expected exit 1 + no tests found, got code=%d stderr=%s", code, errBuf.String())
	}

	// 5. Invalid args return exit code 2.
	out.Reset()
	errBuf.Reset()
	if code := Run([]string{"test"}, strings.NewReader(""), &out, &errBuf); code != 2 {
		t.Fatalf("expected exit 2 for missing args, got %d", code)
	}
	out.Reset()
	errBuf.Reset()
	if code := Run([]string{"test", exampleFile, "--output", "xml"}, strings.NewReader(""), &out, &errBuf); code != 2 {
		t.Fatalf("expected exit 2 for invalid --output, got %d", code)
	}
}

func TestCLITraceCommand(t *testing.T) {
	exampleFile := filepath.Join("..", "api", "testdata", "testing", "orders.dil")

	// Multi-flow file without --flow fails cleanly.
	var out, errBuf bytes.Buffer
	code := Run([]string{"trace", exampleFile}, strings.NewReader(""), &out, &errBuf)
	if code != 1 || !strings.Contains(errBuf.String(), "--flow") {
		t.Fatalf("expected exit 1 mentioning --flow, got code=%d stderr=%s", code, errBuf.String())
	}

	// Trace with --flow, --body, --header in text mode.
	out.Reset()
	errBuf.Reset()
	code = Run([]string{"trace", exampleFile, "--flow", "normalize", "--body", "hello trace", "--header", "env=dev", "--header", "x=1"}, strings.NewReader(""), &out, &errBuf)
	if code != 0 {
		t.Fatalf("expected exit 0, got %d; stderr=%s", code, errBuf.String())
	}
	if !strings.Contains(out.String(), "TRACE: normalize") || !strings.Contains(out.String(), "HELLO TRACE") || !strings.Contains(out.String(), "+normalized=") {
		t.Fatalf("unexpected trace output:\n%s", out.String())
	}

	// Trace in JSON mode + bypassing live https source.
	dir := t.TempDir()
	httpsFile := filepath.Join(dir, "https_flow.dil")
	httpsSrc := `flow web {
  "https://127.0.0.1:19999/hook" as src
  -> setheader(name: "seen", language: "constant", value: "yes") as mark
  -> setbody(language: "simple", expression: "echo:${body}") as echo
  -> passthrough as done
}`
	if err := os.WriteFile(httpsFile, []byte(httpsSrc), 0644); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	errBuf.Reset()
	code = Run([]string{"trace", httpsFile, "--body", "ping", "--output", "json"}, strings.NewReader(""), &out, &errBuf)
	if code != 0 {
		t.Fatalf("expected exit 0 for https source trace, got %d; stderr=%s", code, errBuf.String())
	}
	var payload traceJSON
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("invalid trace JSON: %v\n%s", err, out.String())
	}
	if payload.Flow != "web" || payload.Status != "ok" || len(payload.Steps) != 3 || payload.Output == nil || payload.Output.Body != "echo:ping" {
		t.Fatalf("unexpected trace JSON: %+v", payload)
	}
}

func TestShellWorkbenchREPL(t *testing.T) {
	dir := t.TempDir()
	origWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer os.Chdir(origWD)

	flowPath := filepath.Join(dir, "sample.dil")
	flowSrc := `flow sample {
  message:sample as src
  -> setheader(name: "tag", language: "constant", value: "ok") as mark
  -> setbody(language: "simple", expression: "${uppercase(${body})}") as upper
  -> passthrough as done
}

test "sample passes" {
  flow: "sample"
  input: {body: "abc"}
  expect: {body: "ABC", headers: {tag: "ok"}}
}
`
	if err := os.WriteFile(flowPath, []byte(flowSrc), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}

	script := strings.Join([]string{
		`msg "hello world" --header tenant=acme`,
		`eval simple ${uppercase(${body})} ${headers.tenant}`,
		`msg '{"n":21}' --header factor=2`,
		`eval map { "doubled": payload.n * 2, "f": headers.factor }`,
		`eval jsonpath $.n`,
		`msg "<root><item>val</item></root>"`,
		`eval xpath /root/item`,
		`msg "abc" --clear --header k=v`,
		`exec setbody(language: "simple", expression: "ran:${body}:${headers.k}") as runStep -> passthrough as done`,
		`load sample.dil`,
		`trace sample hello`,
		`test sample.dil`,
		`completion eval s`,
		`help exec`,
		`exit`,
	}, "\n") + "\n"

	var stdout, stderr bytes.Buffer
	code := Run(nil, strings.NewReader(script), &stdout, &stderr)
	if code != 0 {
		t.Fatalf("shell exited %d\nstdout:\n%s\nstderr:\n%s", code, stdout.String(), stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{
		`SCRATCH MESSAGE`,
		`(string) "HELLO WORLD acme"`,
		`(object) {"doubled":42,"f":"2"}`,
		`(number) 21`,
		`(string) "val"`,
		`TRACE: _repl`,
		`ran:abc:v`,
		`TRACE: sample`,
		`HELLO`,
		`PASS  sample passes`,
		`1/1 passed, 0 failed`,
		`simple`,
		`Usage:`,
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("shell output missing %q:\n%s", want, out)
		}
	}
}
