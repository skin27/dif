package impl

import (
	"strings"
	"testing"

	"dif/message"
)

var testFlow = &flowProperties{id: "6a9f", name: "ExchangePropCustomSimple", version: "9", tenant: "regressiontests", environment: "test"}

func evalFlowExpr(t *testing.T, flow *flowProperties, expr string, m message.Message) (string, error) {
	t.Helper()
	x, err := compileExpressionIn(flow, "simple", expr)
	if err != nil {
		t.Fatalf("compile %q: %v", expr, err)
	}
	return x.eval(m)
}

func TestSimpleFlowProperties(t *testing.T) {
	for expr, want := range map[string]string{
		"Flow Version: ${flowVersion}\nFlow ID: ${flowId}\nFlow Name: ${flowName}\nTenant: ${tenant}\nEnvironment: ${environment}": "Flow Version: 9\nFlow ID: 6a9f\nFlow Name: ExchangePropCustomSimple\nTenant: regressiontests\nEnvironment: test",
		"${variable:group:694aca3eda8dd5001600023e:MetaData.FlowVersion}":                                                          "9",
		"${variable:group:694aca3eda8dd5001600023e:MetaData.TenantName}":                                                           "regressiontests",
		"${variable:group:694aca3eda8dd5001600023e:MetaData.EnvironmentName}":                                                      "test",
		"${variable:group:694aca3eda8dd5001600023e:MetaData.FlowName}":                                                             "ExchangePropCustomSimple",
		"${variable:group:694aca3eda8dd5001600023e:MetaData.FlowID}":                                                               "6a9f",
		"${variable:group:694aca3eda8dd5001600023e:MetaData.Frontend}":                                                             "",
	} {
		if got, err := evalFlowExpr(t, testFlow, expr, message.New("")); err != nil || got != want {
			t.Errorf("%s = %q, %v; want %q", expr, got, err, want)
		}
	}
	// Without a flow there is no property, and nothing fails.
	if got, err := evalFlowExpr(t, nil, "[${flowName}]", message.New("")); err != nil || got != "[]" {
		t.Errorf("without a flow: %q, %v", got, err)
	}
	if _, err := compileExpressionIn(testFlow, "simple", "${variable:group:x:Other.FlowName}"); err == nil {
		t.Error("only MetaData names are known")
	}
}

func TestSimpleException(t *testing.T) {
	m := message.New("x")
	for expr, want := range map[string]string{"[${exception}]": "[]", "[${exception.message}]": "[]", "[${exception.class}]": "[]"} {
		if got, err := evalFlowExpr(t, nil, expr, m); err != nil || got != want {
			t.Errorf("without an error, %s = %q, %v; want %q", expr, got, err, want)
		}
	}
	m[message.ErrorMessage] = "Cannot write new file"
	m[message.ErrorClass] = "*fs.PathError"
	m[message.ErrorStackTrace] = "step f: Cannot write new file"
	for expr, want := range map[string]string{
		"${exception}":            "*fs.PathError: Cannot write new file",
		"${exception.message}":    "Cannot write new file",
		"${exception.class}":      "*fs.PathError",
		"${exception.stacktrace}": "step f: Cannot write new file",
	} {
		if got, err := evalFlowExpr(t, nil, expr, m); err != nil || got != want {
			t.Errorf("%s = %q, %v; want %q", expr, got, err, want)
		}
	}
	if _, err := compileExpression("simple", "${exception.cause}"); err == nil || !strings.Contains(err.Error(), "unsupported simple expression ${exception.cause}") {
		t.Errorf("err = %v", err)
	}
}

func TestSimpleHeadersAndVariables(t *testing.T) {
	m := message.Message{message.Body: "body"}
	m["b"] = "2"
	m["a"] = "1"
	m[message.Trail] = "private"
	m["metadata.variable.v"] = "set"
	for expr, want := range map[string]string{
		"${headers}":        "{a=1, b=2}", // not the body, not the metadata
		"${headers.a}":      "1",
		"${headers.size()}": "2",
		"${variable.v}":     "set",
		"${variable:v}":     "set",
		"${variables}":      "{v=set}",
		"${variable.none}":  "",
	} {
		if got, err := evalFlowExpr(t, nil, expr, m); err != nil || got != want {
			t.Errorf("%s = %q, %v; want %q", expr, got, err, want)
		}
	}
}

func TestSimpleInitBlock(t *testing.T) {
	expr := "$init{\n  // minimum age to drive\n  $minAge := 18;\n\n  // say hello\n  $foo := ${uppercase('Hello ${body}')};\n\n  $bar := ${header.code > 999 ? 'Gold' : 'Silver'};\n  $n := 7;\n  $nn := 'x;y';\n}init$\n$foo is $bar, age $minAge, $n$nn."
	m := message.New("Norman")
	m["code"] = "1111"
	got, err := evalFlowExpr(t, nil, expr, m)
	if err != nil || got != "HELLO NORMAN is Gold, age 18, 7x;y." {
		t.Errorf("init = %q, %v", got, err)
	}
	if m["metadata.variable.minAge"] != int64(18) || m["metadata.variable.bar"] != "Gold" {
		t.Errorf("the variables stay on the message: %v", m)
	}
	// A later step reads them.
	if got, err := evalFlowExpr(t, nil, "${variable.bar}/${variable.minAge}", m); err != nil || got != "Gold/18" {
		t.Errorf("later step: %q, %v", got, err)
	}

	for _, bad := range []string{"$init{ $a := 1; ", "$init{ a := 1; }init$", "$init{ $f(x) := 1; }init$"} {
		if _, err := compileExpression("simple", bad); err == nil {
			t.Errorf("%q must not compile", bad)
		}
	}
}
