package api

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"dif/internal/secret"
	"dif/message"
	stepdef "dif/steps/definition"
)

type passthrough struct{}

func (passthrough) Consume(context.Context, message.Message) error { return nil }

// TestEncryptedOptionsAreDecryptedWhenTheFlowIsLoaded checks the whole path: a DIL
// file with an ENC(...) value, the loader, the registry, and the step that gets the
// plain text. Inspection, which builds no processors, never needs the password.
func TestEncryptedOptionsAreDecryptedWhenTheFlowIsLoaded(t *testing.T) {
	got := make(chan stepdef.Params, 1)
	name := fmt.Sprintf("secretcheck%d", time.Now().UnixNano())
	err := RegisterStep(StepDefinition{
		Name:   name,
		Kind:   stepdef.Sink,
		Schema: []byte(`{"type": "object", "properties": {"password": {"type": "string"}}, "additionalProperties": false}`),
		New: func(_ string, p stepdef.Params) (stepdef.Processor, error) {
			got <- p
			return passthrough{}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	enc, err := secret.EncryptWith("flow-password", []byte("0123456789abcdef"), []byte("fedcba9876543210"), "s3cret!")
	if err != nil {
		t.Fatal(err)
	}
	path := dil(t, "enc",
		step{"src", "source", "message:in", nil},
		step{"out", "sink", name, map[string]any{"password": enc}},
	)

	info, err := Inspect(path)
	if err != nil {
		t.Fatalf("Inspect needs no password: %v", err)
	}
	if shown := fmt.Sprintf("%+v", info); strings.Contains(shown, enc) || strings.Contains(shown, "s3cret!") {
		t.Error("the description shows a value")
	}

	t.Setenv(secret.PasswordEnv, "")
	os.Unsetenv(secret.PasswordEnv)
	_, err = Load(path, nil)
	if err == nil || !strings.Contains(err.Error(), secret.PasswordEnv) || strings.Contains(err.Error(), enc) {
		t.Fatalf("without a password: %v", err)
	}
	select {
	case <-got:
		t.Fatal("the step was built without the password")
	default:
	}

	t.Setenv(secret.PasswordEnv, "flow-password")
	if _, err := Load(path, nil); err != nil {
		t.Fatal(err)
	}
	if p := <-got; p["password"] != "s3cret!" {
		t.Errorf("the step got password %q, want the plain text", p["password"])
	}
}
