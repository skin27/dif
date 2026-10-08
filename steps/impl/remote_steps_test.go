package impl

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dif/message"
	stepdef "dif/steps/definition"
)

func sinkStep(t *testing.T, e remoteEnv, dir string, extra map[string]any) stepdef.SinkProcessor {
	t.Helper()
	return mustProcessor(t, stepdef.Sink, e.uri(e.scheme, dir), e.opts(extra)).(stepdef.SinkProcessor)
}

func TestRemoteSink(t *testing.T) {
	forEachProtocol(t, func(t *testing.T, e remoteEnv) {
		ctx := context.Background()
		send := func(p stepdef.SinkProcessor, body any, headers map[string]any) error {
			m := message.New(body)
			for k, v := range headers {
				m[k] = v
			}
			return p.Consume(ctx, m)
		}

		// The name: the option, else file.name, else CamelFileName, else the trace id.
		sink := sinkStep(t, e, "out/in", nil)
		if err := send(sink, "from header", map[string]any{FileName: "a.txt", "CamelFileName": "camel.txt"}); err != nil {
			t.Fatal(err)
		}
		send(sink, "camel", map[string]any{"CamelFileName": "camel.txt"})
		send(sink, "trace", map[string]any{message.TraceID: "t-1"})
		send(sinkStep(t, e, "out/in", map[string]any{"fileName": "RAW(fixed.txt)"}), "fixed", map[string]any{FileName: "a.txt"})
		send(sink, "nested", map[string]any{FileName: "sub/dir/n.txt"})
		for file, want := range map[string]string{"a.txt": "from header", "camel.txt": "camel", "t-1": "trace", "fixed.txt": "fixed", "sub/dir/n.txt": "nested"} {
			if got := read(t, e.local("out/in/"+file)); got != want {
				t.Errorf("%s = %q, want %q", file, got, want)
			}
		}

		// Without any name, not even a trace id, there is nothing to call the file.
		if err := sink.Consume(ctx, message.Message{message.Body: "x"}); err == nil || !strings.Contains(err.Error(), "no file name") {
			t.Errorf("no name: err = %v", err)
		}
		for _, name := range []string{"../escape.txt", "a/../../escape.txt", "/abs.txt", "."} {
			if err := send(sink, "x", map[string]any{FileName: name}); err == nil || !strings.Contains(err.Error(), "not a name below the directory") {
				t.Errorf("name %q: err = %v", name, err)
			}
		}
		if exists(e.local("escape.txt")) || exists(e.local("abs.txt")) {
			t.Error("a file was written outside the directory")
		}
	})
}

func TestRemoteSinkFileExist(t *testing.T) {
	forEachProtocol(t, func(t *testing.T, e remoteEnv) {
		ctx := context.Background()
		write(t, e.local("d/f.txt"), "old")
		put := func(mode string) error {
			m := message.New("new")
			m[FileName] = "f.txt"
			return sinkStep(t, e, "d", map[string]any{"fileExist": mode}).Consume(ctx, m)
		}

		if err := put("Ignore"); err != nil || read(t, e.local("d/f.txt")) != "old" {
			t.Errorf("Ignore: err %v, content %q, want the file untouched", err, read(t, e.local("d/f.txt")))
		}
		if err := put("Fail"); err == nil || !strings.Contains(err.Error(), "already exists") || read(t, e.local("d/f.txt")) != "old" {
			t.Errorf("Fail: err = %v", err)
		}
		if err := put("Append"); err != nil || read(t, e.local("d/f.txt")) != "oldnew" {
			t.Errorf("Append: err %v, content %q", err, read(t, e.local("d/f.txt")))
		}
		if err := put("Override"); err != nil || read(t, e.local("d/f.txt")) != "new" {
			t.Errorf("Override: err %v, content %q", err, read(t, e.local("d/f.txt")))
		}
		// Fail and Ignore write a file that is not there.
		m := message.New("fresh")
		m[FileName] = "g.txt"
		if err := sinkStep(t, e, "d", map[string]any{"fileExist": "Fail"}).Consume(ctx, m); err != nil || read(t, e.local("d/g.txt")) != "fresh" {
			t.Errorf("Fail, new file: err = %v", err)
		}
	})
}

func TestRemoteSinkAutoCreateAndCharset(t *testing.T) {
	forEachProtocol(t, func(t *testing.T, e remoteEnv) {
		ctx := context.Background()
		m := message.New("café €")
		m[FileName] = "c.txt"

		// A missing directory fails the message without autoCreate.
		if err := sinkStep(t, e, "missing/deep", map[string]any{"autoCreate": false}).Consume(ctx, m); err == nil {
			t.Error("autoCreate false: want an error for a directory that is not there")
		}
		if exists(e.local("missing")) {
			t.Error("autoCreate false made the directory")
		}

		if err := sinkStep(t, e, "enc/deep", map[string]any{"charset": "ISO-8859-1"}).Consume(ctx, m); err != nil {
			t.Fatal(err)
		}
		if got := read(t, e.local("enc/deep/c.txt")); got != "caf\xe9 ?" {
			t.Errorf("ISO-8859-1 file = %q, want café with the euro sign as ?", got)
		}

		b := message.New([]byte{0, 1, 0xff})
		b[FileName] = "b.bin"
		if err := sinkStep(t, e, "bin", map[string]any{"binary": true}).Consume(ctx, b); err != nil {
			t.Fatal(err)
		}
		if got := read(t, e.local("bin/b.bin")); got != "\x00\x01\xff" {
			t.Errorf("binary file = %q", got)
		}
	})
}

// remoteSource creates the source step on dir, polling fast.
func remoteSourceStep(t *testing.T, e remoteEnv, dir string, extra map[string]any) stepdef.CompletionSourceProcessor {
	t.Helper()
	opts := e.opts(map[string]any{"initialDelay": 0, "delay": 5})
	for k, v := range extra {
		opts[k] = v
	}
	return mustProcessor(t, stepdef.Source, e.uri(e.scheme, dir), opts).(stepdef.CompletionSourceProcessor)
}

// runningSource is a source step that runs in the background, with the
// messages it emits. Each is completed with fail(m)'s error.
type runningSource struct {
	msgs   chan message.Message
	cancel context.CancelFunc
	done   chan error
}

func startRemote(t *testing.T, src stepdef.CompletionSourceProcessor, fail func(message.Message) error) *runningSource {
	t.Helper()
	return startRemoteCtx(context.Background(), src, fail)
}

func startRemoteCtx(parent context.Context, src stepdef.CompletionSourceProcessor, fail func(message.Message) error) *runningSource {
	ctx, cancel := context.WithCancel(parent)
	r := &runningSource{msgs: make(chan message.Message, 100), cancel: cancel, done: make(chan error, 1)}
	go func() {
		r.done <- src.RunDelivery(ctx, func(m message.Message, _ func(message.Message, error), complete func(error) error) error {
			r.msgs <- m
			var err error
			if fail != nil {
				err = fail(m)
			}
			return complete(err)
		}, func() {})
	}()
	return r
}

// take waits for n more messages.
func (r *runningSource) take(t *testing.T, n int) []message.Message {
	t.Helper()
	var got []message.Message
	timeout := time.After(5 * time.Second)
	for len(got) < n {
		select {
		case m := <-r.msgs:
			got = append(got, m)
		case <-timeout:
			t.Fatalf("got %d of %d messages: %s", len(got), n, fileNames(got))
		}
	}
	return got
}

// stop stops the source after a while, in which no more message may arrive
// (a file consumed twice).
func (r *runningSource) stop(t *testing.T) {
	t.Helper()
	time.Sleep(80 * time.Millisecond)
	r.cancel()
	if err := <-r.done; err != nil {
		t.Fatalf("RunDelivery = %v, want nil", err)
	}
	if extra := len(r.msgs); extra > 0 {
		var got []message.Message
		for len(r.msgs) > 0 {
			got = append(got, <-r.msgs)
		}
		t.Fatalf("%d more messages than expected: %s", extra, fileNames(got))
	}
}

// consumeRemote runs the source until it has emitted want messages and
// stopped; it does not wait for the files to be moved.
func consumeRemote(t *testing.T, src stepdef.CompletionSourceProcessor, want int, fail func(message.Message) error) []message.Message {
	t.Helper()
	r := startRemote(t, src, fail)
	got := r.take(t, want)
	r.stop(t)
	return got
}

func fileNames(msgs []message.Message) string {
	var s []string
	for _, m := range msgs {
		s = append(s, m[FileName].(string))
	}
	return strings.Join(s, ",")
}

func TestRemoteSourceConsumesAndMoves(t *testing.T) {
	forEachProtocol(t, func(t *testing.T, e remoteEnv) {
		write(t, e.local("in/b.txt"), "B")
		write(t, e.local("in/a.json"), `{"a":1}`)
		write(t, e.local("in/.hidden"), "H")
		write(t, e.local("in/sub/c.txt"), "C")
		write(t, e.local("other/x.txt"), "X")

		r := startRemote(t, remoteSourceStep(t, e, "in", map[string]any{"maxMessagesPerPoll": 0}), nil)
		got := r.take(t, 2)
		if fileNames(got) != "a.json,b.txt" {
			t.Fatalf("files = %s, want a.json,b.txt: sorted, no hidden file or subdirectory", fileNames(got))
		}
		if m := got[0]; m[message.Body] != `{"a":1}` || m[message.ContentType] != "application/json" {
			t.Errorf("message = %v, want the content and its Content-Type", m)
		}
		eventually(t, "the files to be archived", func() bool { return exists(e.local("in/.archive/b.txt")) && exists(e.local("in/.archive/a.json")) })
		r.stop(t)

		if left := names(t, e.local("in")); strings.Join(left, ",") != ".archive,.hidden,sub" {
			t.Errorf("in has %v, want the archive, the hidden file and the subdirectory", left)
		}
		if read(t, e.local("in/.archive/b.txt")) != "B" || !exists(e.local("other/x.txt")) {
			t.Error("archived content changed, or another directory was touched")
		}
	})
}

func TestRemoteSourceFailedProcessingMovesToMoveFailed(t *testing.T) {
	forEachProtocol(t, func(t *testing.T, e remoteEnv) {
		write(t, e.local("in/bad.txt"), "bad")
		write(t, e.local("in/good.txt"), "good")
		fail := func(m message.Message) error {
			if m[FileName] == "bad.txt" {
				return errors.New("flow failed")
			}
			return nil
		}
		opts := map[string]any{"maxMessagesPerPoll": -1, "move": "RAW(done)", "moveFailed": "RAW(failed)"}
		r := startRemote(t, remoteSourceStep(t, e, "in", opts), fail)
		r.take(t, 2)
		eventually(t, "the files to be moved", func() bool { return exists(e.local("in/failed/bad.txt")) && exists(e.local("in/done/good.txt")) })
		r.stop(t)
		if left := names(t, e.local("in")); strings.Join(left, ",") != "done,failed" {
			t.Errorf("in has %v", left)
		}
	})
}

func TestRemoteSourceFailedFileStaysWithoutMoveFailed(t *testing.T) {
	forEachProtocol(t, func(t *testing.T, e remoteEnv) {
		write(t, e.local("in/bad.txt"), "bad")
		fail := func(message.Message) error { return errors.New("flow failed") }
		// Left where it is, the file is consumed again at the next poll.
		r := startRemote(t, remoteSourceStep(t, e, "in", map[string]any{"moveFailed": ""}), fail)
		r.take(t, 3)
		r.cancel()
		<-r.done
		if !exists(e.local("in/bad.txt")) {
			t.Errorf("in has %v, want bad.txt left", names(t, e.local("in")))
		}
	})
}

func TestRemoteSourceDelete(t *testing.T) {
	forEachProtocol(t, func(t *testing.T, e remoteEnv) {
		write(t, e.local("in/a.txt"), "A")
		write(t, e.local("in/b.txt"), "B")
		r := startRemote(t, remoteSourceStep(t, e, "in", map[string]any{"delete": true, "maxMessagesPerPoll": 0, "move": ""}), nil)
		r.take(t, 2)
		eventually(t, "the files to be deleted", func() bool { return len(names(t, e.local("in"))) == 0 })
		r.stop(t)
	})
}

func TestRemoteSourceSelection(t *testing.T) {
	forEachProtocol(t, func(t *testing.T, e remoteEnv) {
		for i, tc := range []struct {
			name string
			opts map[string]any
			want string
		}{
			{"file name", map[string]any{"fileName": "b1.xml"}, "b1.xml"},
			{"include", map[string]any{"include": `a.*\.xml`}, "a1.xml,a2.xml"},
			{"include and exclude", map[string]any{"include": `.*\.xml`, "exclude": `skip.*|b.*`}, "a1.xml,a2.xml"},
			{"exclude", map[string]any{"exclude": `.*\.xml`}, "c1.txt"},
			{"the pattern matches the whole name", map[string]any{"include": `a1`}, ""},
		} {
			dir := fmt.Sprintf("in%d", i)
			for _, f := range []string{"a1.xml", "a2.xml", "b1.xml", "c1.txt", "skip.xml"} {
				write(t, e.local(dir+"/"+f), f)
			}
			opts := map[string]any{"maxMessagesPerPoll": 0, "delete": true, "move": ""}
			for k, v := range tc.opts {
				opts[k] = v
			}
			n := 0
			if tc.want != "" {
				n = len(strings.Split(tc.want, ","))
			}
			got := consumeRemote(t, remoteSourceStep(t, e, dir, opts), n, nil)
			if fileNames(got) != tc.want {
				t.Errorf("%s: files = %s, want %s", tc.name, fileNames(got), tc.want)
			}
		}
	})
}

func TestRemoteSourceSortAndLimit(t *testing.T) {
	forEachProtocol(t, func(t *testing.T, e remoteEnv) {
		for i, tc := range []struct{ sortBy, want string }{
			{"", "a,b,c"}, {"file:name", "a,b,c"}, {"reverse:file:name", "c,b,a"},
			{"file:modified", "b,c,a"}, {"reverse:file:modified", "a,c,b"},
		} {
			dir := fmt.Sprintf("in%d", i)
			for name, day := range map[string]string{"a": "2024-03-03", "b": "2024-01-01", "c": "2024-02-02"} {
				write(t, e.local(dir+"/"+name), name)
				if err := os.Chtimes(e.local(dir+"/"+name), mustTime(day), mustTime(day)); err != nil {
					t.Fatal(err)
				}
			}
			// One file per poll, so the order of the polls is the sort order.
			opts := map[string]any{"sortBy": tc.sortBy, "maxMessagesPerPoll": 1, "delete": true, "move": "", "delay": 40}
			got := consumeRemote(t, remoteSourceStep(t, e, dir, opts), 3, nil)
			if fileNames(got) != tc.want {
				t.Errorf("sortBy %q: files = %s, want %s", tc.sortBy, fileNames(got), tc.want)
			}
		}
	})
}

func mustTime(day string) time.Time {
	t, err := time.Parse("2006-01-02", day)
	if err != nil {
		panic(err)
	}
	return t
}

func TestRemoteSourceRecursive(t *testing.T) {
	forEachProtocol(t, func(t *testing.T, e remoteEnv) {
		write(t, e.local("in/top.txt"), "T")
		write(t, e.local("in/a/mid.txt"), "M")
		write(t, e.local("in/a/b/deep.txt"), "D")
		write(t, e.local("in/.skip/x.txt"), "X")

		// "archive" does not start with a dot, but the source must not look in it,
		// or it would consume the same files again (stop fails on a second message).
		r := startRemote(t, remoteSourceStep(t, e, "in", map[string]any{"recursive": true, "maxMessagesPerPoll": 0, "move": "archive"}), nil)
		got := r.take(t, 3)
		if fileNames(got) != "a/b/deep.txt,a/mid.txt,top.txt" {
			t.Errorf("files = %s", fileNames(got))
		}
		eventually(t, "the files to be moved", func() bool {
			return exists(e.local("in/archive/a/b/deep.txt")) && exists(e.local("in/archive/top.txt"))
		})
		r.stop(t)
	})
}

func TestRemoteSourceReadLockChanged(t *testing.T) {
	forEachProtocol(t, func(t *testing.T, e remoteEnv) {
		write(t, e.local("in/a.txt"), "A")
		// The first poll sees the file for the first time; a later one finds it unchanged.
		got := consumeRemote(t, remoteSourceStep(t, e, "in", map[string]any{"readLock": "changed", "delay": 30}), 1, nil)
		if got[0][FileName] != "a.txt" {
			t.Errorf("file = %v", got[0][FileName])
		}
	})
}

func TestRemoteSourceEligible(t *testing.T) {
	src := &remoteSource{remoteFiles: remoteFiles{changed: true}, stamps: map[string]remoteStamp{}, stuck: map[string]remoteStamp{}}
	files := []remoteEntry{{remoteFile{name: "x", size: 1}, "x"}}
	if out := src.eligible(files); len(out) != 0 {
		t.Errorf("a new file is eligible: %v", out)
	}
	files[0].size = 2 // it is still being written
	if out := src.eligible(files); len(out) != 0 {
		t.Errorf("a changed file is eligible: %v", out)
	}
	if out := src.eligible(files); len(out) != 1 {
		t.Errorf("an unchanged file is not eligible: %v", out)
	}
}

func TestRemoteSourceStuckFilesAreNotConsumedAgain(t *testing.T) {
	src := &remoteSource{remoteFiles: remoteFiles{}, stamps: map[string]remoteStamp{}, stuck: map[string]remoteStamp{}}
	file := remoteEntry{remoteFile{name: "a", size: 1}, "a"}
	src.stuck["a"] = stampOf(file)
	if out := src.eligible([]remoteEntry{file}); len(out) != 0 {
		t.Errorf("a stuck file is eligible: %v", out)
	}
	file.size = 2 // it changes
	if out := src.eligible([]remoteEntry{file}); len(out) != 1 || len(src.stuck) != 0 {
		t.Errorf("a changed file: eligible %v, stuck %v, want it eligible and forgotten", out, src.stuck)
	}
	src.max = 1
	if out := src.eligible([]remoteEntry{file, {remoteFile{name: "b"}, "b"}}); len(out) != 1 {
		t.Errorf("max 1: %v", out)
	}
}

func TestRemoteSourceKeepsPollingAfterAFailedPoll(t *testing.T) {
	forEachProtocol(t, func(t *testing.T, e remoteEnv) {
		var logged lockedBuffer
		ctx := stepdef.WithLogger(context.Background(), log.New(&logged, "", 0))
		// The directory does not exist and is not created.
		r := startRemoteCtx(ctx, remoteSourceStep(t, e, "later", map[string]any{"autoCreate": false}), nil)

		waitForLog(t, &logged, e.scheme+" 127.0.0.1")
		write(t, e.local("later/a.txt"), "A")
		if got := r.take(t, 1); got[0][FileName] != "a.txt" {
			t.Errorf("file = %v", got[0][FileName])
		}
		r.stop(t)
	})
}

func TestRemoteSourceAutoCreate(t *testing.T) {
	forEachProtocol(t, func(t *testing.T, e remoteEnv) {
		consumeRemote(t, remoteSourceStep(t, e, "new/dir", nil), 0, nil)
		if info, err := os.Stat(e.local("new/dir")); err != nil || !info.IsDir() {
			t.Errorf("the directory was not created: %v", err)
		}
	})
}

func TestRemoteSourceStopsWhenTheFlowStops(t *testing.T) {
	forEachProtocol(t, func(t *testing.T, e remoteEnv) {
		write(t, e.local("in/a.txt"), "A")
		src := remoteSourceStep(t, e, "in", nil)
		err := src.RunDelivery(context.Background(), func(message.Message, func(message.Message, error), func(error) error) error {
			return errors.New("stopping")
		}, func() {})
		if err != nil {
			t.Errorf("RunDelivery = %v, want nil", err)
		}
		if !exists(e.local("in/a.txt")) {
			t.Error("the file was moved although the flow did not take it")
		}
	})
}

func TestRemoteSourceWithoutCompletion(t *testing.T) {
	// Run and RunReady do not learn the outcome: a file is moved as processed.
	forEachProtocol(t, func(t *testing.T, e remoteEnv) {
		write(t, e.local("in/a.txt"), "A")
		src := remoteSourceStep(t, e, "in", nil)
		ctx, cancel := context.WithCancel(context.Background())
		got := make(chan message.Message, 1)
		done := make(chan error, 1)
		go func() {
			done <- src.Run(ctx, func(m message.Message, _ func(message.Message, error)) error { got <- m; return nil })
		}()
		select {
		case <-got:
		case <-time.After(5 * time.Second):
			t.Fatal("no message")
		}
		eventually(t, "the file to be archived", func() bool { return exists(e.local("in/.archive/a.txt")) })
		cancel()
		<-done
	})
}

func remoteEnrichStep(t *testing.T, e remoteEnv, dir string, extra map[string]any) stepdef.ActionProcessor {
	t.Helper()
	return mustProcessor(t, stepdef.Action, e.uri(e.scheme+"enrich", dir), e.opts(extra)).(stepdef.ActionProcessor)
}

func TestRemoteEnrich(t *testing.T) {
	forEachProtocol(t, func(t *testing.T, e remoteEnv) {
		ctx := context.Background()
		write(t, e.local("in/a.xml"), "<a/>")
		write(t, e.local("in/b.xml"), "<b/>")

		m := message.New("request")
		m["keep"] = "me"
		out, err := remoteEnrichStep(t, e, "in", nil).Process(ctx, m)
		if err != nil {
			t.Fatal(err)
		}
		if out[message.Body] != "<a/>" || out[FileName] != "a.xml" || out[message.ContentType] != "application/xml" || out["keep"] != "me" {
			t.Errorf("message = %v, want the first file as body and the headers kept", out)
		}
		// By default the file is moved to .archive after it is read.
		if exists(e.local("in/a.xml")) || read(t, e.local("in/.archive/a.xml")) != "<a/>" || !exists(e.local("in/b.xml")) {
			t.Errorf("in has %v, want a.xml archived and b.xml left", names(t, e.local("in")))
		}

		// The next one is the next file; delete removes it instead of moving it.
		out, _ = remoteEnrichStep(t, e, "in", map[string]any{"delete": true}).Process(ctx, message.New("r"))
		if out[message.Body] != "<b/>" || exists(e.local("in/b.xml")) || exists(e.local("in/.archive/b.xml")) {
			t.Errorf("delete: body %v, in has %v", out[message.Body], names(t, e.local("in")))
		}

		// No move, no delete: the file stays.
		write(t, e.local("in/c.xml"), "<c/>")
		out, _ = remoteEnrichStep(t, e, "in", map[string]any{"move": ""}).Process(ctx, message.New("r"))
		if out[message.Body] != "<c/>" || !exists(e.local("in/c.xml")) {
			t.Errorf("move empty: body %v", out[message.Body])
		}
	})
}

func TestRemoteEnrichNoFile(t *testing.T) {
	forEachProtocol(t, func(t *testing.T, e remoteEnv) {
		ctx := context.Background()
		write(t, e.local("in/a.txt"), "A")
		for _, dir := range []string{"in", "no/such/dir"} {
			opts := map[string]any{"include": `.*\.xml`}
			out, err := remoteEnrichStep(t, e, dir, opts).Process(ctx, message.New("unchanged"))
			if err != nil || out[message.Body] != "unchanged" {
				t.Errorf("%s: body %v, err %v, want the message unchanged", dir, out[message.Body], err)
			}
			opts["abortMode"] = true
			if _, err := remoteEnrichStep(t, e, dir, opts).Process(ctx, message.New("x")); err == nil || !strings.Contains(err.Error(), "no file found in "+dir) {
				t.Errorf("%s with abortMode: err = %v", dir, err)
			}
		}
		if !exists(e.local("in/a.txt")) {
			t.Error("a file that does not match was touched")
		}
	})
}

func TestRemoteEnrichBinaryAndCharset(t *testing.T) {
	forEachProtocol(t, func(t *testing.T, e remoteEnv) {
		ctx := context.Background()
		write(t, e.local("in/l.txt"), "caf\xe9")
		out, _ := remoteEnrichStep(t, e, "in", map[string]any{"charset": "ISO-8859-1", "move": ""}).Process(ctx, message.New("r"))
		if out[message.Body] != "café" {
			t.Errorf("text body = %q, want café", out[message.Body])
		}
		out, _ = remoteEnrichStep(t, e, "in", map[string]any{"binary": true, "move": ""}).Process(ctx, message.New("r"))
		if b, ok := out[message.Body].([]byte); !ok || string(b) != "caf\xe9" {
			t.Errorf("binary body = %#v", out[message.Body])
		}
	})
}

func TestRemoteEnrichReadLockChanged(t *testing.T) {
	old := remoteLockWait
	remoteLockWait = 30 * time.Millisecond
	defer func() { remoteLockWait = old }()
	forEachProtocol(t, func(t *testing.T, e remoteEnv) {
		write(t, e.local("in/a.txt"), "A")
		out, err := remoteEnrichStep(t, e, "in", map[string]any{"readLock": "changed", "move": ""}).Process(context.Background(), message.New("r"))
		if err != nil || out[message.Body] != "A" {
			t.Errorf("stable file: body %v, err %v", out[message.Body], err)
		}
	})
}

func TestRemoteEnrichRecursiveSkipsMoveFolder(t *testing.T) {
	forEachProtocol(t, func(t *testing.T, e remoteEnv) {
		write(t, e.local("in/a/one.txt"), "1")
		write(t, e.local("in/b/two.txt"), "2")
		step := remoteEnrichStep(t, e, "in", map[string]any{"recursive": true, "move": "archive"})
		first, _ := step.Process(context.Background(), message.New("r"))
		second, _ := step.Process(context.Background(), message.New("r"))
		third, _ := step.Process(context.Background(), message.New("r"))
		if first[FileName] != "a/one.txt" || second[FileName] != "b/two.txt" || third[message.Body] != "r" {
			t.Errorf("files %v, %v and body %v, want each file once and then nothing: the archive is not searched", first[FileName], second[FileName], third[message.Body])
		}
	})
}

func TestRemoteConnectionReuse(t *testing.T) {
	forEachProtocol(t, func(t *testing.T, e remoteEnv) {
		ctx := context.Background()
		send := func(p stepdef.SinkProcessor, name string) {
			t.Helper()
			m := message.New("x")
			m[FileName] = name
			if err := p.Consume(ctx, m); err != nil {
				t.Fatal(err)
			}
		}
		each := sinkStep(t, e, "d", nil) // disconnect is true by default
		send(each, "1")
		send(each, "2")
		send(each, "3")
		if n := e.conns(); n < 3 {
			t.Errorf("%d connections for three uses with disconnect, want one each", n)
		}

		before := e.conns()
		kept := sinkStep(t, e, "d", map[string]any{"disconnect": false})
		send(kept, "4")
		send(kept, "5")
		send(kept, "6")
		if n := e.conns() - before; n != 1 {
			t.Errorf("%d connections for three uses without disconnect, want 1", n)
		}
		for _, f := range []string{"1", "2", "3", "4", "5", "6"} {
			if !exists(e.local("d/" + f)) {
				t.Errorf("file %s was not written", f)
			}
		}
	})
}

func TestRemoteConnectionIdleClose(t *testing.T) {
	old := remoteIdle
	remoteIdle = 40 * time.Millisecond
	defer func() { remoteIdle = old }()
	forEachProtocol(t, func(t *testing.T, e remoteEnv) {
		kept := sinkStep(t, e, "d", map[string]any{"disconnect": false})
		m := message.New("x")
		m[FileName] = "1"
		if err := kept.Consume(context.Background(), m); err != nil {
			t.Fatal(err)
		}
		c := kept.(*remoteSink).t.conn
		eventually(t, "the idle connection to be closed", func() bool {
			c.mu.Lock()
			defer c.mu.Unlock()
			return c.cur == nil
		})
		// The next use connects again.
		before := e.conns()
		if err := kept.Consume(context.Background(), m); err != nil || e.conns() != before+1 {
			t.Errorf("after the idle close: err %v, %d new connections, want 1", err, e.conns()-before)
		}
	})
}

func TestRemoteConnectionRecoversAfterAnError(t *testing.T) {
	forEachProtocol(t, func(t *testing.T, e remoteEnv) {
		kept := sinkStep(t, e, "d", map[string]any{"disconnect": false, "fileExist": "Fail"})
		m := message.New("x")
		m[FileName] = "1"
		if err := kept.Consume(context.Background(), m); err != nil {
			t.Fatal(err)
		}
		if err := kept.Consume(context.Background(), m); err == nil {
			t.Fatal("want an error: the file exists")
		}
		m[FileName] = "2"
		if err := kept.Consume(context.Background(), m); err != nil {
			t.Errorf("after an error: %v", err)
		}
	})
}

func TestRemoteWrongPasswordAndNoServer(t *testing.T) {
	forEachProtocol(t, func(t *testing.T, e remoteEnv) {
		m := message.New("x")
		m[FileName] = "a"
		err := sinkStep(t, e, "d", map[string]any{"password": "wrong"}).Consume(context.Background(), m)
		if err == nil || !strings.Contains(err.Error(), e.scheme+" 127.0.0.1") {
			t.Errorf("wrong password: err = %v, want the failure with the server", err)
		}
		if err != nil && strings.Contains(err.Error(), "wrong") {
			t.Errorf("err = %v reveals the password", err)
		}

		down := e
		down.host = "127.0.0.1:1"
		if err := sinkStep(t, down, "d", nil).Consume(context.Background(), m); err == nil {
			t.Error("no server: want an error")
		}
	})
}

func TestRemoteCancelEndsAStalledOperation(t *testing.T) {
	f := newFakeFTP(t)
	f.hang = true
	f.start()
	e := remoteEnv{"ftp", f.root, f.ln.Addr().String(), map[string]any{"userName": f.user, "password": f.pass}, f.connCount}
	write(t, e.local("in/a.txt"), "A")

	ctx, cancel := context.WithCancel(context.Background())
	step := remoteEnrichStep(t, e, "in", map[string]any{"socketTimeout": 60000})
	done := make(chan error, 1)
	go func() {
		_, err := step.Process(ctx, message.New("r"))
		done <- err
	}()
	eventually(t, "the download to start", func() bool { return len(f.commands("RETR")) > 0 })
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Error("want an error from the cancelled download")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the cancelled download did not end")
	}
}

func TestRemoteFilePath(t *testing.T) {
	// The file path of the source, below the directory, is also what the sink takes as a name.
	forEachProtocol(t, func(t *testing.T, e remoteEnv) {
		write(t, e.local("in/a/b.txt"), "AB")
		got := consumeRemote(t, remoteSourceStep(t, e, "in", map[string]any{"recursive": true}), 1, nil)
		if err := sinkStep(t, e, "out", nil).Consume(context.Background(), got[0]); err != nil {
			t.Fatal(err)
		}
		if read(t, filepath.Join(e.root, "out", "a", "b.txt")) != "AB" {
			t.Errorf("out has %v", names(t, e.local("out")))
		}
	})
}
