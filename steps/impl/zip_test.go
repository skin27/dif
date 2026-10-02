package impl

import (
	"archive/zip"
	"bytes"
	"context"
	"strings"
	"testing"

	"dif/message"
	stepdef "dif/steps/definition"
)

func TestZipRoundTrip(t *testing.T) {
	m := message.New("1234")
	m[FileName] = "content.txt"
	zipped := process(t, "zip", nil, m)
	if zipped[FileName] != "content.txt.zip" || zipped["Content-Type"] != "application/zip" {
		t.Errorf("zipped headers = %v", zipped)
	}
	if b, ok := zipped[message.Body].([]byte); !ok || !bytes.HasPrefix(b, []byte("PK")) {
		t.Fatalf("body is not a zip archive: %v", zipped[message.Body])
	}

	out := process(t, "unzip", nil, zipped)
	if string(bytesOf(out[message.Body])) != "1234" || out[FileName] != "content.txt" {
		t.Errorf("unzipped = %v", out)
	}
	if _, ok := out["Content-Type"]; ok {
		t.Error("unzip kept Content-Type application/zip")
	}
}

func TestZipNamesFileAfterTraceID(t *testing.T) {
	m := message.New("x")
	out := process(t, "unzip", nil, process(t, "zip", nil, m))
	if out[FileName] != m[message.TraceID] {
		t.Errorf("file name = %v, want the trace id %v", out[FileName], m[message.TraceID])
	}
}

func TestUnzipInvalid(t *testing.T) {
	archive := func(names ...string) []byte {
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		for _, n := range names {
			zw.Create(n)
		}
		zw.Close()
		return buf.Bytes()
	}
	p := mustProcessor(t, stepdef.Action, "unzip", nil).(stepdef.ActionProcessor)
	for _, tt := range []struct {
		body any
		want string
	}{
		{"not a zip", "body is not a zip archive"},
		{archive(), "zip archive holds no file"},
		{archive("dir/"), "zip archive holds no file"},
		{archive("a.txt", "b.txt"), "zip archive holds more than one file (a.txt, b.txt)"},
	} {
		if _, err := p.Process(context.Background(), message.New(tt.body)); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("err = %v, want containing %q", err, tt.want)
		}
	}
	if out := process(t, "unzip", nil, message.New(archive("dir/", "dir/a.txt"))); out[FileName] != "dir/a.txt" {
		t.Errorf("file name = %v, want dir/a.txt", out[FileName])
	}
	wantInvalid(t, stepdef.Action, "zip", map[string]any{"fileName": "x"}, "unknown option fileName")
}
