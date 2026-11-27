package dilsyntax

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestTestDeclarationRoundTrip(t *testing.T) {
	cases := []struct {
		name string
		src  string
	}{
		{
			name: "inline test in flow file",
			src: `flow hello {
  message:hello as src
  -> setbody(language: "simple", expression: "${uppercase(${body})}") as upper
  -> passthrough as done
}

test "uppercases body" {
  flow: "hello"
  input: {
    body: "hello world"
    headers: {tenant: "acme"}
  }
  expect: {
    status: "ok"
    body: "HELLO WORLD"
    trailContains: ["action:upper"]
  }
}
`,
		},
		{
			name: "standalone test file with import and mocks",
			src: `import "./orders.dil" as orders

test "mocks external payment" {
  flow: "orders"
  input: {message: "orders:sample"}
  mocks: [
    {
      step: "charge"
      body: "OK"
      headers: {receipt: "r1"}
      called: 1
    }
  ]
  expect: {
    status: "ok"
    bodyContains: "OK"
  }
}
`,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, err := Parse("sample.dil", []byte(tc.src))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			j1, err := Lower("sample.dil", f)
			if err != nil {
				t.Fatalf("Lower: %v", err)
			}
			if !strings.Contains(string(j1), `"tests"`) || !strings.Contains(string(j1), `"test"`) {
				t.Fatalf("expected dil.core.tests.test in lowered JSON: %s", string(j1))
			}

			printed, err := Print(j1)
			if err != nil {
				t.Fatalf("Print: %v", err)
			}
			if !strings.Contains(string(printed), "test ") {
				t.Fatalf("expected test declaration in printed .dil:\n%s", string(printed))
			}

			f2, err := Parse("printed.dil", printed)
			if err != nil {
				t.Fatalf("re-Parse printed .dil: %v\n%s", err, string(printed))
			}
			j2, err := Lower("printed.dil", f2)
			if err != nil {
				t.Fatalf("re-Lower printed .dil: %v", err)
			}

			var v1, v2 any
			if err := json.Unmarshal(j1, &v1); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(j2, &v2); err != nil {
				t.Fatal(err)
			}
			b1, _ := json.Marshal(v1)
			b2, _ := json.Marshal(v2)
			if !bytes.Equal(b1, b2) {
				t.Fatalf("round-trip mismatch:\nfirst:  %s\nsecond: %s", b1, b2)
			}
		})
	}
}
