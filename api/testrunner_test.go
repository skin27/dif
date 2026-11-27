package api

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunTestsExampleOrders(t *testing.T) {
	path := filepath.Join("testdata", "testing", "orders_test.dil")
	report, err := RunTests(context.Background(), []ProgramSource{{Name: path, Path: path}}, ProgramLimits{})
	if err != nil {
		t.Fatalf("RunTests failed: %v", err)
	}
	if report.Total != 2 || report.Passed != 2 || report.Failed != 0 {
		t.Fatalf("expected 2/2 passed, got %+v", report)
	}
	for _, r := range report.Results {
		if !r.Passed || len(r.Trace) == 0 {
			t.Fatalf("expected passed test with trace: %+v", r)
		}
	}
}

func TestRunTestsMockSQLAndAssertions(t *testing.T) {
	src := `flow lookup {
  "https://127.0.0.1:9443/lookup" as src
  -> sql(query: "SELECT name FROM users WHERE id = 1", connectionType: "postgres", host: "db.internal", database: "app") as queryDb
  -> setheader(name: "source", language: "constant", value: "db") as tag
  -> passthrough as done
}

test "mocked sql structured body and headers" {
  flow: "lookup"
  mocks: [
    {
      step: "queryDb"
      body: "{\"name\":\"Ada\",\"count\":2}"
      headers: {rows: 1}
      called: 1
    }
  ]
  expect: {
    status: "ok"
    body: {name: "Ada", count: 2}
    headers: {source: "db", rows: 1}
    trailContains: ["action:queryDb", "done"]
  }
}

test "failing assertions and called mismatch" {
  flow: "lookup"
  mocks: [
    {
      step: "queryDb"
      body: "actual-body"
      called: 2
    }
  ]
  expect: {
    status: "ok"
    body: "expected-body"
    bodyContains: "missing-sub"
    headers: {source: "wrong", absentHeader: "x"}
    trailContains: ["nonexistent-step"]
  }
}

test "mock unknown step fails cleanly" {
  flow: "lookup"
  mocks: [
    {step: "doesNotExist", body: "x"}
  ]
  expect: {status: "ok"}
}

test "unhandled error status" {
  flow: "lookup"
  mocks: [
    {step: "queryDb", error: "connection refused"}
  ]
  expect: {
    status: "error"
    errorContains: "connection refused"
  }
}
`
	report, err := RunTests(context.Background(), []ProgramSource{{Name: "sql_test.dil", Data: []byte(src)}}, ProgramLimits{})
	if err != nil {
		t.Fatalf("RunTests failed: %v", err)
	}
	if report.Total != 4 || report.Passed != 2 || report.Failed != 2 {
		t.Fatalf("expected 4 total (2 passed, 2 failed), got %+v", report)
	}
	if !report.Results[0].Passed {
		t.Fatalf("test 0 should pass, failures: %v", report.Results[0].Failures)
	}
	if report.Results[1].Passed {
		t.Fatal("test 1 should fail")
	}
	joined := strings.Join(report.Results[1].Failures, "\n")
	for _, want := range []string{"body:", "expected substring", `header "source"`, `header "absentHeader": missing`, "trail:", `mock "queryDb": expected 2 call(s), got 1`} {
		if !strings.Contains(joined, want) {
			t.Fatalf("test 1 failures missing %q:\n%s", want, joined)
		}
	}
	if report.Results[2].Passed || !strings.Contains(strings.Join(report.Results[2].Failures, "\n"), `unknown step "doesNotExist"`) {
		t.Fatalf("test 2 should fail on unknown mock step: %+v", report.Results[2])
	}
	if !report.Results[3].Passed {
		t.Fatalf("test 3 should pass on expected error status, failures: %v", report.Results[3].Failures)
	}
}

func TestRunTestsHermeticCapabilitySandbox(t *testing.T) {
	src := `flow externalCall {
  message:externalCall as src
  -> "https://api.example.com/v1/items"(httpMethod: "GET") as fetch
  -> passthrough as done
}

test "unmocked https step is rejected" {
  flow: "externalCall"
  expect: {status: "ok"}
}
`
	report, err := RunTests(context.Background(), []ProgramSource{{Name: "cap_test.dil", Data: []byte(src)}}, ProgramLimits{})
	if err != nil {
		t.Fatalf("RunTests returned error: %v", err)
	}
	if report.Total != 1 || report.Failed != 1 {
		t.Fatalf("expected 1 failed test, got %+v", report)
	}
	msg := strings.Join(report.Results[0].Failures, "\n")
	if !strings.Contains(msg, `step "fetch" requires net.connect api.example.com:443`) || !strings.Contains(msg, "add a mock") {
		t.Fatalf("expected actionable capability failure, got: %s", msg)
	}
}

func TestRunTestsEmptyAndNullBodyExpectations(t *testing.T) {
	src := `flow blank {
  message:blank as src
  -> setbody(language: "constant", expression: "") as clear
  -> passthrough as done
}

test "expects empty string body" {
  flow: "blank"
  input: {body: "non-empty"}
  expect: {
    status: "ok"
    body: ""
  }
}

test "expects null body from mock" {
  flow: "blank"
  mocks: [
    {step: "clear", body: null}
  ]
  expect: {
    status: "ok"
    body: null
  }
}
`
	report, err := RunTests(context.Background(), []ProgramSource{{Name: "blank.dil", Data: []byte(src)}}, ProgramLimits{})
	if err != nil {
		t.Fatalf("RunTests failed: %v", err)
	}
	if report.Total != 2 || report.Passed != 2 {
		t.Fatalf("expected 2/2 passed, got %+v", report)
	}
}

func TestRunTestsImportsExcludeLibraryTests(t *testing.T) {
	dir := t.TempDir()
	libPath := filepath.Join(dir, "lib.dil")
	testPath := filepath.Join(dir, "suite_test.dil")

	libSrc := `flow shout {
  message:shout as src
  -> setbody(language: "simple", expression: "${uppercase(${body})}") as upper
  -> passthrough as done
}

test "library internal test" {
  flow: "shout"
  input: {body: "lib"}
  expect: {body: "LIB"}
}
`
	suiteSrc := `import "./lib.dil" as lib

test "suite test" {
  flow: "shout"
  input: {body: "suite"}
  expect: {body: "SUITE"}
}
`
	if err := os.WriteFile(libPath, []byte(libSrc), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(testPath, []byte(suiteSrc), 0644); err != nil {
		t.Fatal(err)
	}

	// Passing only suite_test.dil runs ONLY suite_test.dil's tests, not lib.dil's.
	report, err := RunTests(context.Background(), []ProgramSource{{Name: testPath, Path: testPath}}, ProgramLimits{})
	if err != nil {
		t.Fatalf("RunTests failed: %v", err)
	}
	if report.Total != 1 || report.Passed != 1 || report.Results[0].Name != "suite test" {
		t.Fatalf("expected only suite test to run, got %+v", report)
	}

	// Passing both runs both.
	reportBoth, err := RunTests(context.Background(), []ProgramSource{
		{Name: libPath, Path: libPath},
		{Name: testPath, Path: testPath},
	}, ProgramLimits{})
	if err != nil {
		t.Fatalf("RunTests both failed: %v", err)
	}
	if reportBoth.Total != 2 || reportBoth.Passed != 2 {
		t.Fatalf("expected 2/2 passed when both files passed, got %+v", reportBoth)
	}
}
