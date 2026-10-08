package impl

import (
	"strings"
	"testing"

	"dif/message"
	stepdef "dif/steps/definition"
)

func TestTenantVariables(t *testing.T) {
	name := "settenantvariable:" + t.Name()
	m := message.New("body")
	m["user"] = "ann"
	process(t, name, map[string]any{"value": "token-${header.user}", "encrypt": true, "flowName": "f", "tenantDbName": "_new2"}, m)

	get := func(tenant string) any {
		t.Helper()
		opts := map[string]any{"headerName": "v"}
		if tenant != "" {
			opts["tenantDbName"] = tenant
		}
		return process(t, "gettenantvariable:"+t.Name(), opts, message.New("x"))["v"]
	}
	if v := get("_new2"); v != "token-ann" {
		t.Errorf("value = %v, want token-ann", v)
	}
	if v := get(""); v != "" {
		t.Errorf("other tenant: value = %v, want empty", v)
	}

	process(t, "removetenantvariable:"+t.Name(), map[string]any{"tenantDbName": "_new2"}, message.New("x"))
	if v := get("_new2"); v != "" {
		t.Errorf("removed: value = %v, want empty", v)
	}

	wantInvalid(t, stepdef.Action, "settenantvariable", nil, "no variable name")
	wantInvalid(t, stepdef.Action, "gettenantvariable:x", nil, "missing required option headerName")
	wantInvalid(t, stepdef.Action, "gettenantvariable:x", map[string]any{"headerName": "body"}, "reserved for the body")
	wantInvalid(t, stepdef.Action, "settenantvariable:x", map[string]any{"language": "groovy"}, "option language")
}

func TestExpandTenantVariables(t *testing.T) {
	tenant := t.Name()
	tenantVariables.set(tenant, "a", "1")
	tenantVariables.set(tenant, "b", "")
	tenantVariables.set("other", "a", "2")

	for in, want := range map[string]string{
		"": "", "plain": "plain", "@{a}": "1", "x@{a}y@{b}z@{a}": "x1yz1", "@{a}@{a}": "11", "$@{a}": "$1", "{a}": "{a}", "a@": "a@",
	} {
		if got, err := expandTenantVariables(tenant, in); err != nil || got != want {
			t.Errorf("expand(%q) = %q, %v, want %q", in, got, err, want)
		}
	}
	for in, want := range map[string]string{
		"@{missing}": `tenant variable "missing" of tenant "TestExpandTenantVariables" is not set`,
		"ok @{a":     "unterminated",
	} {
		if _, err := expandTenantVariables(tenant, in); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("expand(%q) err = %v, want containing %q", in, err, want)
		}
	}
}
