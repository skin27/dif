package impl

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"dif/message"
	stepdef "dif/steps/definition"
)

// fakeDrive is a small Google Drive: files in folders, as the steps use it.
type fakeDrive struct {
	mu       sync.Mutex
	token    string // the access token it accepts
	files    map[string]*fakeFile
	next     int
	pageSize int
	uploads  []string // content types of the uploads, "name:type"
}

type fakeFile struct {
	ID, Name, MimeType, Parent string
	Content                    string
}

var (
	parentRE = regexp.MustCompile(`'([^']+)' in parents`)
	nameRE   = regexp.MustCompile(`name = '((?:[^'\\]|\\.)*)'`)
	mimeRE   = regexp.MustCompile(`mimeType = '([^']+)'`)
)

// newFakeDrive starts the fake and points the steps at it for the test.
func newFakeDrive(t *testing.T) *fakeDrive {
	t.Helper()
	d := &fakeDrive{token: "good-token", files: map[string]*fakeFile{}, pageSize: 100}
	srv := httptest.NewServer(d)
	old := googleDriveAPI
	googleDriveAPI = srv.URL
	t.Cleanup(func() { googleDriveAPI = old; srv.Close() })
	return d
}

// add puts a file in a folder and returns its id.
func (d *fakeDrive) add(parent, name, content string) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.put(parent, name, "text/plain", content)
}

func (d *fakeDrive) put(parent, name, mimeType, content string) string {
	d.next++
	id := "id" + strconv.Itoa(d.next)
	d.files[id] = &fakeFile{id, name, mimeType, parent, content}
	return id
}

// children returns the names of the files in a folder, with their content.
func (d *fakeDrive) children(parent string) map[string]string {
	d.mu.Lock()
	defer d.mu.Unlock()
	got := map[string]string{}
	for _, f := range d.files {
		if f.Parent == parent {
			got[f.Name] = f.Content
		}
	}
	return got
}

func (d *fakeDrive) idOf(parent, name string) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, f := range d.files {
		if f.Parent == parent && f.Name == name {
			return f.ID
		}
	}
	return ""
}

func (d *fakeDrive) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if r.Header.Get("Authorization") != "Bearer "+d.token {
		d.fail(w, http.StatusUnauthorized, "Invalid Credentials")
		return
	}
	if r.URL.Query().Get("supportsAllDrives") != "true" {
		d.fail(w, http.StatusBadRequest, "supportsAllDrives is required")
		return
	}
	path, q := r.URL.Path, r.URL.Query()
	id := path[strings.LastIndex(path, "/")+1:]
	switch {
	case r.Method == http.MethodGet && path == "/drive/v3/files":
		d.list(w, q)
	case r.Method == http.MethodGet && q.Get("alt") == "media":
		if f := d.files[id]; f != nil {
			io.WriteString(w, f.Content)
		} else {
			d.fail(w, http.StatusNotFound, "File not found")
		}
	case r.Method == http.MethodPost && path == "/drive/v3/files": // a folder
		var meta struct {
			Name, MimeType string
			Parents        []string
		}
		json.NewDecoder(r.Body).Decode(&meta)
		d.reply(w, d.put(meta.Parents[0], meta.Name, meta.MimeType, ""))
	case r.Method == http.MethodPatch && strings.HasPrefix(path, "/drive/v3/files/"): // a move
		f := d.files[id]
		if f == nil || f.Parent != q.Get("removeParents") {
			d.fail(w, http.StatusNotFound, "File not found")
			return
		}
		f.Parent = q.Get("addParents")
		d.reply(w, id)
	case r.Method == http.MethodPost && path == "/upload/drive/v3/files":
		d.upload(w, r)
	case r.Method == http.MethodPatch && strings.HasPrefix(path, "/upload/drive/v3/files/"):
		body, _ := io.ReadAll(r.Body)
		d.files[id].Content = string(body)
		d.uploads = append(d.uploads, d.files[id].Name+":"+r.Header.Get("Content-Type"))
		d.reply(w, id)
	default:
		d.fail(w, http.StatusNotFound, r.Method+" "+path)
	}
}

func (d *fakeDrive) fail(w http.ResponseWriter, status int, message string) {
	w.WriteHeader(status)
	fmt.Fprintf(w, `{"error":{"code":%d,"message":%q}}`, status, message)
}

func (d *fakeDrive) reply(w http.ResponseWriter, id string) {
	fmt.Fprintf(w, `{"id":%q}`, id)
}

// list answers a search by the parent, name and mimeType of its query.
func (d *fakeDrive) list(w http.ResponseWriter, q map[string][]string) {
	query := q["q"][0]
	var found []*fakeFile
	for _, f := range d.files {
		if m := parentRE.FindStringSubmatch(query); m == nil || f.Parent != m[1] {
			continue
		}
		if m := nameRE.FindStringSubmatch(query); m != nil && f.Name != strings.NewReplacer(`\'`, `'`, `\\`, `\`).Replace(m[1]) {
			continue
		}
		if m := mimeRE.FindStringSubmatch(query); m != nil && f.MimeType != m[1] {
			continue
		}
		found = append(found, f)
	}
	sort.Slice(found, func(i, j int) bool { return found[i].Name < found[j].Name })

	start := 0
	if tok := q["pageToken"]; len(tok) > 0 {
		start, _ = strconv.Atoi(tok[0])
	}
	end, next := min(start+d.pageSize, len(found)), ""
	if end < len(found) {
		next = strconv.Itoa(end)
	}
	page := []driveFile{}
	for _, f := range found[start:end] {
		page = append(page, driveFile{f.ID, f.Name, f.MimeType})
	}
	json.NewEncoder(w).Encode(map[string]any{"nextPageToken": next, "files": page})
}

// upload reads a multipart/related upload: the metadata, then the content.
func (d *fakeDrive) upload(w http.ResponseWriter, r *http.Request) {
	mt, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mt != "multipart/related" {
		d.fail(w, http.StatusBadRequest, "want multipart/related")
		return
	}
	mr := multipart.NewReader(r.Body, params["boundary"])
	var meta struct {
		Name    string
		Parents []string
	}
	part, err := mr.NextPart()
	if err != nil || json.NewDecoder(part).Decode(&meta) != nil {
		d.fail(w, http.StatusBadRequest, "bad metadata")
		return
	}
	part, err = mr.NextPart()
	if err != nil {
		d.fail(w, http.StatusBadRequest, "no content")
		return
	}
	content, _ := io.ReadAll(part)
	ct := part.Header.Get("Content-Type")
	d.uploads = append(d.uploads, meta.Name+":"+ct)
	d.reply(w, d.put(meta.Parents[0], meta.Name, ct, string(content)))
}

// driveOpts are the options of a drive step in tenant t.Name(), whose
// access token is the variable "tok".
func driveOpts(t *testing.T, extra map[string]any) map[string]any {
	t.Helper()
	opts := map[string]any{"accessToken": "@{tok}", "tenant": t.Name()}
	for k, v := range extra {
		opts[k] = v
	}
	return opts
}

func setToken(t *testing.T, token string) {
	t.Helper()
	tenantVariables.set(t.Name(), "tok", token)
}

// consume runs the googledrive source on folder until it has emitted want
// messages, then stops it. It returns the messages in order.
func consumeDrive(t *testing.T, folder string, opts map[string]any, want int) []message.Message {
	t.Helper()
	all := driveOpts(t, map[string]any{"initialDelay": 0, "delay": 5})
	for k, v := range opts {
		all[k] = v
	}
	src := mustProcessor(t, stepdef.Source, "googledrive:"+folder, all).(stepdef.SourceProcessor)

	ctx, cancel := context.WithCancel(context.Background())
	msgs := make(chan message.Message, 100)
	done := make(chan error, 1)
	go func() {
		done <- src.Run(ctx, func(m message.Message, _ func(message.Message, error)) error { msgs <- m; return nil })
	}()

	var got []message.Message
	timeout := time.After(2 * time.Second)
	for len(got) < want {
		select {
		case m := <-msgs:
			got = append(got, m)
			continue
		case <-timeout:
		}
		break
	}
	time.Sleep(50 * time.Millisecond) // the last file is moved after it is emitted
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if len(got) != want {
		t.Fatalf("got %d messages, want %d", len(got), want)
	}
	return got
}

func TestDriveSourceConsumesAndMovesFiles(t *testing.T) {
	d := newFakeDrive(t)
	setToken(t, d.token)
	d.add("F", "b.txt", "B")
	d.add("F", "a.json", `{"a":1}`)
	d.add("F", "photo.bin", "\x00\x01")
	d.add("other", "x.txt", "X")
	d.put("F", "Doc", driveNative+"document", "")
	d.put("F", "sub", driveNative+"folder", "")

	got := consumeDrive(t, "F", nil, 3)

	var names []string
	for _, m := range got {
		names = append(names, m[FileName].(string))
	}
	if strings.Join(names, ",") != "a.json,b.txt,photo.bin" {
		t.Fatalf("files = %v, want sorted by name, without the Google document and the subfolder", names)
	}
	if m := got[0]; m[message.Body] != `{"a":1}` || m[message.ContentType] != "application/json" || m[DriveID] == "" {
		t.Errorf("message = %v, want the content, its Content-Type and the file id", m)
	}
	if _, ok := got[2][message.ContentType]; ok {
		t.Errorf("photo.bin: Content-Type = %v, want none", got[2][message.ContentType])
	}

	if left := d.children("F"); len(left) != 3 || left["Doc"] != "" || left[".done"] != "" || left["sub"] != "" {
		t.Errorf("folder F has %v, want only the Google document, the subfolder and .done left", left)
	}
	done := d.idOf("F", ".done")
	if done == "" {
		t.Fatal("no .done folder was created")
	}
	if moved := d.children(done); len(moved) != 3 || moved["b.txt"] != "B" {
		t.Errorf(".done has %v, want the three files", moved)
	}
	if other := d.children("other"); other["x.txt"] != "X" {
		t.Errorf("another folder has %v, want it untouched", other)
	}
}

func TestDriveSourceFilterAndMoveTo(t *testing.T) {
	d := newFakeDrive(t)
	setToken(t, d.token)
	d.add("F", "O'Brien.txt", "1")
	d.add("F", "other.txt", "2")
	d.put("F", ".archive", driveNative+"folder", "")
	archive := d.idOf("F", ".archive")

	got := consumeDrive(t, "F", map[string]any{"filterFiles": "O'Brien.txt", "moveTo": ".archive"}, 1)

	if got[0][FileName] != "O'Brien.txt" || got[0][message.Body] != "1" {
		t.Errorf("message = %v, want O'Brien.txt", got[0])
	}
	if moved := d.children(archive); len(moved) != 1 || moved["O'Brien.txt"] != "1" {
		t.Errorf("the existing .archive folder has %v, want O'Brien.txt moved into it", moved)
	}
	if d.idOf("F", ".done") != "" {
		t.Error("a .done folder was created, want the existing .archive folder used")
	}
	if left := d.children("F"); left["other.txt"] != "2" {
		t.Errorf("folder has %v, want other.txt left", left)
	}
}

func TestDriveSourceReadsAllPages(t *testing.T) {
	d := newFakeDrive(t)
	setToken(t, d.token)
	d.pageSize = 2
	for _, n := range []string{"a", "b", "c", "d", "e"} {
		d.add("F", n, n)
	}
	got := consumeDrive(t, "F", nil, 5)
	if got[4][FileName] != "e" {
		t.Errorf("last file = %v, want e", got[4][FileName])
	}
}

func TestDriveSourceRetriesAFailedPoll(t *testing.T) {
	d := newFakeDrive(t)
	d.add("F", "a.txt", "A")
	var logged lockedBuffer

	// The token does not exist yet, as before the first oauth2token run.
	src := mustProcessor(t, stepdef.Source, "googledrive:F", driveOpts(t, map[string]any{"initialDelay": 0, "delay": 5})).(stepdef.SourceProcessor)
	ctx, cancel := context.WithCancel(stepdef.WithLogger(context.Background(), log.New(&logged, "", 0)))
	msgs := make(chan message.Message, 10)
	done := make(chan error, 1)
	go func() {
		done <- src.Run(ctx, func(m message.Message, _ func(message.Message, error)) error { msgs <- m; return nil })
	}()

	waitForLog(t, &logged, `googledrive F: access token: tenant variable "tok"`)
	setToken(t, "stale")
	waitForLog(t, &logged, "401 Unauthorized: Invalid Credentials")

	setToken(t, d.token) // the token service has refreshed it
	select {
	case m := <-msgs:
		if m[FileName] != "a.txt" {
			t.Errorf("file = %v, want a.txt", m[FileName])
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no message after the token was set")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run = %v, want nil", err)
	}
}

// lockedBuffer is a log destination that can be read while it is written.
type lockedBuffer struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// waitForLog waits until the log contains want.
func waitForLog(t *testing.T, buf *lockedBuffer, want string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !strings.Contains(buf.String(), want) {
		if time.Now().After(deadline) {
			t.Fatalf("log = %q, want it to contain %q", buf.String(), want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestDriveSourceStopsWhenTheFlowStops(t *testing.T) {
	d := newFakeDrive(t)
	setToken(t, d.token)
	d.add("F", "a.txt", "A")
	src := mustProcessor(t, stepdef.Source, "googledrive:F", driveOpts(t, map[string]any{"initialDelay": 0})).(stepdef.SourceProcessor)

	err := src.Run(context.Background(), func(message.Message, func(message.Message, error)) error { return fmt.Errorf("stopping") })
	if err != nil {
		t.Errorf("Run = %v, want nil", err)
	}
	if left := d.children("F"); left["a.txt"] != "A" {
		t.Errorf("folder has %v, want the file left for the next run", left)
	}
}

func TestDriveActionCreatesAndReplacesFiles(t *testing.T) {
	d := newFakeDrive(t)
	setToken(t, d.token)

	send := func(extra map[string]any, headers map[string]any, body string) message.Message {
		t.Helper()
		m := message.New(body)
		for k, v := range headers {
			m[k] = v
		}
		return process(t, "googledrive:F", driveOpts(t, extra), m)
	}

	m := send(nil, map[string]any{"CamelFileName": "camel.txt", message.ContentType: "text/plain"}, "first")
	if d.children("F")["camel.txt"] != "first" || m[DriveID] != d.idOf("F", "camel.txt") {
		t.Fatalf("folder has %v, id header %v, want camel.txt created", d.children("F"), m[DriveID])
	}
	if d.uploads[0] != "camel.txt:text/plain" {
		t.Errorf("upload = %q, want the message's Content-Type", d.uploads[0])
	}

	// file.name wins over CamelFileName, and the option over both.
	send(nil, map[string]any{FileName: "dif.txt", "CamelFileName": "camel.txt"}, "second")
	send(map[string]any{"fileName": "option.txt"}, map[string]any{FileName: "dif.txt"}, "third")
	want := map[string]string{"camel.txt": "first", "dif.txt": "second", "option.txt": "third"}
	got := d.children("F")
	for name, content := range want {
		if got[name] != content {
			t.Errorf("%s = %q, want %q (folder %v)", name, got[name], content, got)
		}
	}
	if d.uploads[1] != "dif.txt:application/octet-stream" {
		t.Errorf("upload = %q, want application/octet-stream without a Content-Type", d.uploads[1])
	}

	// Override replaces the content of the same file.
	id := d.idOf("F", "dif.txt")
	m = send(nil, map[string]any{FileName: "dif.txt", message.ContentType: "text/csv"}, "replaced")
	if m[DriveID] != id || d.children("F")["dif.txt"] != "replaced" || len(d.children("F")) != 3 {
		t.Errorf("id %v, folder %v, want dif.txt replaced in place", m[DriveID], d.children("F"))
	}
	if last := d.uploads[len(d.uploads)-1]; last != "dif.txt:text/csv" {
		t.Errorf("upload = %q, want text/csv", last)
	}
}

func TestDriveActionFileExist(t *testing.T) {
	d := newFakeDrive(t)
	setToken(t, d.token)
	d.add("F", "a.txt", "old")

	m := message.New("new")
	m[FileName] = "a.txt"
	out := process(t, "googledrive:F", driveOpts(t, map[string]any{"fileExist": "Ignore"}), m)
	if out[message.Body] != "new" || d.children("F")["a.txt"] != "old" {
		t.Errorf("Ignore: body %v, file %q, want the message passed on and the file untouched", out[message.Body], d.children("F")["a.txt"])
	}

	p := mustProcessor(t, stepdef.Action, "googledrive:F", driveOpts(t, map[string]any{"fileExist": "Fail"})).(stepdef.ActionProcessor)
	if _, err := p.Process(context.Background(), m); err == nil || !strings.Contains(err.Error(), "a.txt already exists") {
		t.Errorf("Fail: err = %v, want already exists", err)
	}
	if d.children("F")["a.txt"] != "old" {
		t.Error("Fail: the file was changed")
	}
}

func TestDriveActionErrors(t *testing.T) {
	d := newFakeDrive(t)
	p := mustProcessor(t, stepdef.Action, "googledrive:F", driveOpts(t, nil)).(stepdef.ActionProcessor)
	named := message.Message{message.Body: "x", FileName: "a.txt"}

	if _, err := p.Process(context.Background(), message.New("x")); err == nil || !strings.Contains(err.Error(), "no file name") {
		t.Errorf("no name: err = %v", err)
	}
	if _, err := p.Process(context.Background(), named.Copy()); err == nil || !strings.Contains(err.Error(), `tenant variable "tok"`) {
		t.Errorf("no token: err = %v, want the missing variable", err)
	}
	setToken(t, "wrong")
	if _, err := p.Process(context.Background(), named.Copy()); err == nil || !strings.Contains(err.Error(), "401 Unauthorized: Invalid Credentials") {
		t.Errorf("wrong token: err = %v, want the API's message", err)
	}
	if len(d.files) != 0 {
		t.Errorf("files = %v, want none", d.files)
	}

	// A token given in the option itself needs no variable.
	setToken(t, "")
	p = mustProcessor(t, stepdef.Action, "googledrive:F", driveOpts(t, map[string]any{"accessToken": d.token})).(stepdef.ActionProcessor)
	if _, err := p.Process(context.Background(), named.Copy()); err != nil {
		t.Errorf("literal token: err = %v", err)
	}
	// An empty variable is an empty token.
	p = mustProcessor(t, stepdef.Action, "googledrive:F", driveOpts(t, nil)).(stepdef.ActionProcessor)
	if _, err := p.Process(context.Background(), named.Copy()); err == nil || !strings.Contains(err.Error(), "access token is empty") {
		t.Errorf("empty variable: err = %v", err)
	}
}

func TestDriveInvalidOptions(t *testing.T) {
	ok := map[string]any{"accessToken": "@{tok}"}
	for _, kind := range []string{stepdef.Source, stepdef.Action} {
		wantInvalid(t, kind, "googledrive", ok, "missing required option path")
		wantInvalid(t, kind, "googledrive:", ok, "missing required option path")
		wantInvalid(t, kind, "googledrive:F", nil, "missing required option accessToken")
		wantInvalid(t, kind, "googledrive:F", map[string]any{"accessToken": ""}, "option accessToken: empty")
		wantInvalid(t, kind, "googledrive:F", map[string]any{"accessToken": "t", "unknown": 1}, "unknown option")
	}
	wantInvalid(t, stepdef.Source, "googledrive:F", map[string]any{"accessToken": "t", "gSuiteFiles": "Export"}, "gSuiteFiles")
	wantInvalid(t, stepdef.Source, "googledrive:F", map[string]any{"accessToken": "t", "delay": 0}, "delay")
	wantInvalid(t, stepdef.Action, "googledrive:F", map[string]any{"accessToken": "t", "fileExist": "Append"}, "fileExist")
}

// The options of the examples' flows are accepted.
func TestDriveExampleOptions(t *testing.T) {
	mustProcessor(t, stepdef.Source, "googledrive:1HYnwLTICJPLGbPL4AUcUJC-cRuGeTrG3", map[string]any{
		"accessToken": "@{GoogleDriveAccessToken_Temp}", "filterFiles": "BrunoDIL.txt", "moveTo": ".dovetail",
		"initialDelay": 1000, "delay": 1000, "gSuiteFiles": "Ignore", "flowId": "69316f11bb703e000c000023", "tenant": "_new2",
	})
	mustProcessor(t, stepdef.Action, "googledrive:1HYnwLTICJPLGbPL4AUcUJC-cRuGeTrG3", map[string]any{
		"accessToken": "@{GoogleDriveAccessToken_Temp}", "flowId": "69319540bb703e0017000103", "tenant": "_new2",
	})
}

func TestDriveQuote(t *testing.T) {
	if got := driveQuote(`O'Brien \ x`); got != `'O\'Brien \\ x'` {
		t.Errorf("driveQuote = %s", got)
	}
}
