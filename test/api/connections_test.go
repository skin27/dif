package api_test

import (
	"encoding/json"
	"strings"
	"testing"

	"dif/api"
)

// The example imports typed connections; steps refer to them by namespace.
func TestProgramTypedConnections(t *testing.T) {
	t.Chdir("../..") // option paths, such as the trust store, are relative to the working directory
	t.Setenv("DIF_TRUSTSTORE_PASSWORD", "changeit")
	info, err := api.InspectProgram("api/testdata/connections/orders.json")
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if strings.Contains(text, "dummy-password") || strings.Contains(text, "partner.example") {
		t.Errorf("the description shows connection values: %s", text)
	}
	for _, want := range []string{
		`"kind":"connection","name":"common:partnerSftp"`,
		`"kind":"connection","name":"common:partnerApi"`,
		`"kind":"connection","name":"common:ordersDb"`,
		`{"id":"partnerSftp","type":"sftp"}`,
		`{"id":"ordersDb","type":"sql"}`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("the description has no %s: %s", want, text)
		}
	}

	p, err := api.LoadProgram("api/testdata/connections/orders.json", nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Flows) != 3 {
		t.Errorf("%d flows", len(p.Flows))
	}
}
