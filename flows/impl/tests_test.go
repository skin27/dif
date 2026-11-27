package impl

import (
	"strings"
	"testing"

	"dif/message"
)

func TestDecodeAndValidateTests(t *testing.T) {
	validJSON := `{
		"dil": {
			"core": {
				"messages": {
					"message": {
						"name": "sample",
						"body": "hi",
						"headers": {"header": [{"name": "x", "value": "1"}]}
					}
				},
				"tests": {
					"test": [
						{
							"name": "empty body expectation",
							"flow": "f1",
							"input": {"body": ""},
							"mocks": [{"step": "s1", "body": null, "called": 0}],
							"expect": {"body": ""}
						},
						{
							"name": "null body expectation",
							"flow": "f1",
							"input": {"message": "sample", "body": null},
							"expect": {"status": "handled", "body": null, "trailContains": "s1"}
						}
					]
				}
			},
			"integrations": {
				"integration": {
					"id": "i1",
					"flows": {
						"flow": {
							"id": "f1",
							"steps": {
								"step": [
									{"id": "src", "type": "source", "uri": "message:f1", "links": {"link": {"id": "l1", "bound": "out"}}},
									{"id": "s1", "type": "sink", "uri": "passthrough", "links": {"link": {"id": "l1", "bound": "in"}}}
								]
							}
						}
					}
				}
			}
		}
	}`

	doc, err := DecodeDocument([]byte(validJSON))
	if err != nil {
		t.Fatalf("DecodeDocument failed: %v", err)
	}
	tests, err := doc.Tests(nil)
	if err != nil {
		t.Fatalf("Tests failed: %v", err)
	}
	if len(tests) != 2 {
		t.Fatalf("expected 2 tests, got %d", len(tests))
	}

	if !tests[0].Input.HasBody || tests[0].Input.Body != "" {
		t.Fatalf("test 0 input body should be present empty string: %+v", tests[0].Input)
	}
	if !tests[0].Mocks[0].HasBody || tests[0].Mocks[0].Body != nil {
		t.Fatalf("test 0 mock body should be present nil: %+v", tests[0].Mocks[0])
	}
	if tests[0].Expect.Status != "ok" {
		t.Fatalf("expected default status ok, got %q", tests[0].Expect.Status)
	}
	if !tests[0].Expect.HasBody || tests[0].Expect.Body != "" {
		t.Fatalf("test 0 expect body should be present empty string: %+v", tests[0].Expect)
	}

	if tests[1].Input.Base[message.Body] != "hi" || tests[1].Input.Base["x"] != "1" {
		t.Fatalf("test 1 input.message not resolved: %+v", tests[1].Input.Base)
	}
	if !tests[1].Input.HasBody || tests[1].Input.Body != nil {
		t.Fatalf("test 1 input null body override not captured: %+v", tests[1].Input)
	}
	if !tests[1].Expect.HasBody || tests[1].Expect.Body != nil {
		t.Fatalf("test 1 expect null body not captured: %+v", tests[1].Expect)
	}
	if len(tests[1].Expect.TrailContains) != 1 || tests[1].Expect.TrailContains[0] != "s1" {
		t.Fatalf("unexpected trailContains: %+v", tests[1].Expect.TrailContains)
	}

	// Verify production FlowPlan strips dil.core.tests.
	plans, err := doc.Plans(nil)
	if err != nil {
		t.Fatalf("Plans failed: %v", err)
	}
	if len(plans) != 1 {
		t.Fatalf("expected 1 plan, got %d", len(plans))
	}
	if strings.Contains(string(plans[0].data), "empty body expectation") {
		t.Fatalf("production FlowPlan should not retain dil.core.tests")
	}
}

func TestTestValidationErrors(t *testing.T) {
	cases := []struct {
		name string
		json string
		want string
	}{
		{
			name: "unknown field on test",
			json: `{"dil":{"core":{"tests":{"test":{"name":"t1","flow":"f1","extra":1,"expect":{}}}}}}`,
			want: "invalid test",
		},
		{
			name: "missing name",
			json: `{"dil":{"core":{"tests":{"test":{"flow":"f1","expect":{}}}}}}`,
			want: "empty test name",
		},
		{
			name: "duplicate test name",
			json: `{"dil":{"core":{"tests":{"test":[{"name":"dup","flow":"f1","expect":{}},{"name":"dup","flow":"f1","expect":{}}]}}}}`,
			want: "duplicate test",
		},
		{
			name: "missing flow",
			json: `{"dil":{"core":{"tests":{"test":{"name":"t1","expect":{}}}}}}`,
			want: "flow is required",
		},
		{
			name: "missing expect",
			json: `{"dil":{"core":{"tests":{"test":{"name":"t1","flow":"f1"}}}}}`,
			want: "expect is required",
		},
		{
			name: "invalid status",
			json: `{"dil":{"core":{"tests":{"test":{"name":"t1","flow":"f1","expect":{"status":"weird"}}}}}}`,
			want: "expect.status",
		},
		{
			name: "duplicate mock step",
			json: `{"dil":{"core":{"tests":{"test":{"name":"t1","flow":"f1","mocks":[{"step":"s1"},{"step":"s1"}],"expect":{}}}}}}`,
			want: "duplicate mock",
		},
		{
			name: "negative called",
			json: `{"dil":{"core":{"tests":{"test":{"name":"t1","flow":"f1","mocks":[{"step":"s1","called":-1}],"expect":{}}}}}}`,
			want: "called must be 0 or more",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := DecodeDocument([]byte(tc.json))
			if err == nil {
				t.Fatalf("expected error containing %q, got nil", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("expected error containing %q, got %v", tc.want, err)
			}
		})
	}
}
