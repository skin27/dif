//go:build linux

package cli

import (
	"bufio"
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestServiceProcessHelper(t *testing.T) {
	if os.Getenv("DIF_SERVICE_TEST_PROCESS") != "1" {
		return
	}
	os.Exit(Run([]string{"run", "--file", os.Getenv("DIF_SERVICE_TEST_FLOW"), "--shutdown-timeout=1s"}, os.Stdin, os.Stdout, os.Stderr))
}

// Uses the real command dispatcher and OS signal delivery, with stdin closed.
func TestServiceSIGTERMWithoutStdin(t *testing.T) {
	flow, err := filepath.Abs("../testdata/hello.json")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestServiceProcessHelper$")
	cmd.Env = append(os.Environ(), "DIF_SERVICE_TEST_PROCESS=1", "DIF_SERVICE_TEST_FLOW="+flow)
	var diagnostic bytes.Buffer
	cmd.Stderr = &diagnostic
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	ready := make(chan struct{})
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			if strings.Contains(scanner.Text(), `"msg":"service ready"`) {
				close(ready)
				return
			}
		}
	}()
	select {
	case <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("service did not become ready")
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("SIGTERM exit: %v: %s", err, diagnostic.String())
		}
	case <-time.After(3 * time.Second):
		t.Fatal("SIGTERM did not terminate service")
	}
}
