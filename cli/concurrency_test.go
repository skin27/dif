package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDescribeConcurrencyAndPartitionedWithoutLeakingExpr(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "orders.dil")
	src := `flow orders(concurrency: 8, partitionBy: "${header.secretCustomerKey}") {
  message:in -> passthrough
}
`
	if err := os.WriteFile(path, []byte(src), 0644); err != nil {
		t.Fatal(err)
	}

	var out, errOut bytes.Buffer
	if code := Run([]string{"describe", path}, strings.NewReader(""), &out, &errOut); code != 0 {
		t.Fatalf("describe text failed (%d): %s", code, errOut.String())
	}
	text := out.String()
	if !strings.Contains(text, "CONCURRENCY: 8 (partitioned)") {
		t.Errorf("describe text missing CONCURRENCY line:\n%s", text)
	}
	if strings.Contains(text, "secretCustomerKey") || strings.Contains(text, "${header") {
		t.Errorf("describe text leaked partitionBy expression:\n%s", text)
	}

	out.Reset()
	errOut.Reset()
	if code := Run([]string{"describe", "--output", "json", path}, strings.NewReader(""), &out, &errOut); code != 0 {
		t.Fatalf("describe json failed (%d): %s", code, errOut.String())
	}
	js := out.String()
	if !strings.Contains(js, `"concurrency": 8`) || !strings.Contains(js, `"partitioned": true`) {
		t.Errorf("describe json missing concurrency/partitioned:\n%s", js)
	}
	if strings.Contains(js, "secretCustomerKey") || strings.Contains(js, "${header") {
		t.Errorf("describe json leaked partitionBy expression:\n%s", js)
	}
}
