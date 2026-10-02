package impl

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"dif/message"
	stepdef "dif/steps/definition"
)

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// poll runs a file source on dir until it has emitted want messages (or
// 50ms more have passed when want is 0), then stops it and returns the
// messages by file name.
func poll(t *testing.T, dir string, opts map[string]any, want int) map[string]any {
	t.Helper()
	all := map[string]any{"initialDelay": 0.0, "delay": 5.0}
	for k, v := range opts {
		all[k] = v
	}
	src := mustProcessor(t, stepdef.Source, "file:"+dir, all).(stepdef.SourceProcessor)

	ctx, cancel := context.WithCancel(context.Background())
	msgs := make(chan message.Message, 100)
	done := make(chan error, 1)
	go func() {
		done <- src.Run(ctx, func(m message.Message, _ func(message.Message, error)) error { msgs <- m; return nil })
	}()

	got := map[string]any{}
	timeout := time.After(2 * time.Second)
	if want == 0 {
		timeout = time.After(50 * time.Millisecond)
	}
	for want == 0 || len(got) < want {
		select {
		case m := <-msgs:
			got[m[FileName].(string)] = m[message.Body]
			continue
		case <-timeout:
		}
		break
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if want > 0 && len(got) != want {
		t.Fatalf("got %d messages %v, want %d", len(got), got, want)
	}
	return got
}

func TestFileSourceMovesToDone(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "a.txt"), "A")
	write(t, filepath.Join(dir, "b.txt"), "B")
	write(t, filepath.Join(dir, ".hidden"), "H")
	write(t, filepath.Join(dir, "sub", "c.txt"), "C")

	got := poll(t, dir, nil, 2)
	if got["a.txt"] != "A" || got["b.txt"] != "B" {
		t.Errorf("messages = %v, want a.txt=A and b.txt=B", got)
	}
	for _, f := range []string{"a.txt", "b.txt"} {
		if exists(filepath.Join(dir, f)) || read(t, filepath.Join(dir, doneDir, f)) == "" {
			t.Errorf("%s was not moved to %s", f, doneDir)
		}
	}
	if !exists(filepath.Join(dir, ".hidden")) || !exists(filepath.Join(dir, "sub", "c.txt")) {
		t.Error("dot files and subdirectories must be left alone when not recursive")
	}
}

func TestFileSourceDelete(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "a.txt"), "A")

	poll(t, dir, map[string]any{"delete": true}, 1)
	if exists(filepath.Join(dir, "a.txt")) || exists(filepath.Join(dir, doneDir)) {
		t.Error("file was not deleted")
	}
}

func TestFileSourceFileName(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "test1234.txt"), "yes")
	write(t, filepath.Join(dir, "other.txt"), "no")

	got := poll(t, dir, map[string]any{"fileName": "test1234.txt"}, 1)
	if got["test1234.txt"] != "yes" {
		t.Errorf("messages = %v, want test1234.txt only", got)
	}
	if !exists(filepath.Join(dir, "other.txt")) {
		t.Error("other.txt was consumed")
	}
}

func TestFileSourceRecursive(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "a.txt"), "A")
	write(t, filepath.Join(dir, "sub", "deep", "c.txt"), "C")
	write(t, filepath.Join(dir, ".skip", "d.txt"), "D")

	got := poll(t, dir, map[string]any{"recursive": "true"}, 2)
	if got["a.txt"] != "A" || got["sub/deep/c.txt"] != "C" {
		t.Errorf("messages = %v, want a.txt and sub/deep/c.txt", got)
	}
	if !exists(filepath.Join(dir, doneDir, "sub", "deep", "c.txt")) {
		t.Error("sub/deep/c.txt was not moved to .done/sub/deep")
	}
	if !exists(filepath.Join(dir, ".skip", "d.txt")) {
		t.Error("dot directory was consumed")
	}
}

func TestFileSourceAutoCreate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "new", "in")
	poll(t, dir, nil, 0)
	if !exists(dir) {
		t.Error("directory was not created")
	}

	missing := filepath.Join(t.TempDir(), "missing")
	src := mustProcessor(t, stepdef.Source, "file:"+missing, map[string]any{"autoCreate": false, "initialDelay": 0.0}).(stepdef.SourceProcessor)
	if err := src.Run(context.Background(), func(message.Message, func(message.Message, error)) error { return nil }); err == nil {
		t.Error("missing directory without autoCreate: want error")
	}
}

func TestFileSourceStopsDuringInitialDelay(t *testing.T) {
	src := mustProcessor(t, stepdef.Source, "file:"+t.TempDir(), map[string]any{"initialDelay": 60000.0}).(stepdef.SourceProcessor)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	start := time.Now()
	src.Run(ctx, func(message.Message, func(message.Message, error)) error { return nil })
	if d := time.Since(start); d > time.Second {
		t.Errorf("stop took %v", d)
	}
}

func TestFileSourceInvalid(t *testing.T) {
	wantInvalid(t, stepdef.Source, "file", nil, "missing required option path")
	wantInvalid(t, stepdef.Source, "file:/tmp", map[string]any{"charset": "latin1"}, "option charset")
	wantInvalid(t, stepdef.Source, "file:/tmp", map[string]any{"delay": 0.0}, "option delay: 0 is less than 1")
	wantInvalid(t, stepdef.Source, "file:/tmp", map[string]any{"fileExist": "Append"}, "unknown option fileExist")
}

// sink returns a file sink on dir and a function consuming a message with body.
func sink(t *testing.T, dir string, opts map[string]any) func(m message.Message) error {
	t.Helper()
	p := mustProcessor(t, stepdef.Sink, "file:"+dir, opts).(stepdef.SinkProcessor)
	return func(m message.Message) error { return p.Consume(context.Background(), m) }
}

func msg(body any, name string) message.Message {
	m := message.New(body)
	if name != "" {
		m[FileName] = name
	}
	return m
}

func TestFileSinkFileExist(t *testing.T) {
	tests := []struct {
		mode    string
		want    string
		wantErr bool
	}{
		{"Override", "second", false},
		{"Append", "firstsecond", false},
		{"Fail", "first", true},
		{"Ignore", "first", false},
	}
	for _, tt := range tests {
		t.Run(tt.mode, func(t *testing.T) {
			dir := t.TempDir()
			consume := sink(t, dir, map[string]any{"fileName": "out.txt", "fileExist": tt.mode})
			if err := consume(msg("first", "")); err != nil {
				t.Fatal(err)
			}
			err := consume(msg("second", ""))
			if (err != nil) != tt.wantErr {
				t.Errorf("second write: err = %v, want error %v", err, tt.wantErr)
			}
			if got := read(t, filepath.Join(dir, "out.txt")); got != tt.want {
				t.Errorf("content = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestFileSinkDefaultIsOverride(t *testing.T) {
	dir := t.TempDir()
	consume := sink(t, dir, map[string]any{"fileName": "out.txt"})
	consume(msg("a longer first body", ""))
	consume(msg("short", ""))
	if got := read(t, filepath.Join(dir, "out.txt")); got != "short" {
		t.Errorf("content = %q, want short", got)
	}
}

func TestFileSinkFileName(t *testing.T) {
	dir := t.TempDir()

	sink(t, dir, map[string]any{"fileName": "option.txt"})(msg("1", "header.txt"))
	if read(t, filepath.Join(dir, "option.txt")) != "1" {
		t.Error("the fileName option must win over the header")
	}

	sink(t, dir, nil)(msg("2", "sub/header.txt"))
	if read(t, filepath.Join(dir, "sub", "header.txt")) != "2" {
		t.Error("the file.name header was not used")
	}

	m := msg("3", "")
	sink(t, dir, nil)(m)
	if read(t, filepath.Join(dir, m[message.TraceID].(string))) != "3" {
		t.Error("the trace id was not used")
	}
}

func TestFileSinkWritesBodyOnly(t *testing.T) {
	dir := t.TempDir()
	consume := sink(t, dir, map[string]any{"fileExist": "Append", "fileName": "out"})
	for _, body := range []any{"s", []byte("b"), 42, map[string]any{"k": "v"}, nil} {
		m := msg(body, "")
		m["secret"] = "header"
		if err := consume(m); err != nil {
			t.Fatal(err)
		}
	}
	if got, want := read(t, filepath.Join(dir, "out")), `sb42{"k":"v"}`; got != want {
		t.Errorf("content = %q, want %q", got, want)
	}
}

func TestFileSinkAutoCreate(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing")
	if err := sink(t, missing, map[string]any{"autoCreate": false, "fileName": "x"})(msg("x", "")); err == nil {
		t.Error("missing directory without autoCreate: want error")
	}
	if err := sink(t, missing, map[string]any{"fileName": "x"})(msg("x", "")); err != nil {
		t.Errorf("with autoCreate: %v", err)
	}
}

func TestFileSinkConcurrentAppend(t *testing.T) {
	dir := t.TempDir()
	consume := sink(t, dir, map[string]any{"fileExist": "Append", "fileName": "log"})
	var wg sync.WaitGroup
	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := consume(msg("line\n", "")); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if got := strings.Count(read(t, filepath.Join(dir, "log")), "line\n"); got != 50 {
		t.Errorf("found %d lines, want 50", got)
	}
}

func TestFileSinkInActionPosition(t *testing.T) {
	p, err := newProcessor(stepdef.Action, "file:"+t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := p.(stepdef.SinkProcessor); !ok {
		t.Errorf("processor = %T, want the file sink", p)
	}
}

func TestFileSinkInvalid(t *testing.T) {
	wantInvalid(t, stepdef.Sink, "file", nil, "missing required option path")
	wantInvalid(t, stepdef.Sink, "file:/tmp", map[string]any{"fileExist": "Replace"}, `option fileExist: "Replace" is not one of`)
	wantInvalid(t, stepdef.Sink, "file:/tmp", map[string]any{"delete": true}, "unknown option delete")
}
