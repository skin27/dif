package impl

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"dif/message"
	stepdef "dif/steps/definition"
)

// The FTP servers differ in what they support; the client has to work with
// each of them.
func TestFTPServerVariants(t *testing.T) {
	for name, configure := range map[string]func(*fakeFTP){
		"MLSD and EPSV":    func(*fakeFTP) {},
		"no MLSD (LIST)":   func(f *fakeFTP) { f.noMLSD = true },
		"no MLSD, DOS":     func(f *fakeFTP) { f.noMLSD, f.dosList = true, true },
		"no EPSV (PASV)":   func(f *fakeFTP) { f.noEPSV = true },
		"no SIZE":          func(f *fakeFTP) { f.noSIZE = true },
		"everything plain": func(f *fakeFTP) { f.noMLSD, f.noEPSV, f.noSIZE = true, true, true },
	} {
		t.Run(name, func(t *testing.T) {
			e := newFTPEnv(t, configure)
			ctx := context.Background()
			write(t, e.local("in/a.txt"), "A")
			write(t, e.local("in/sub/b.txt"), "B")

			// Listing and reading, moving and the existence check (SIZE or a listing).
			out, err := remoteEnrichStep(t, e, "in", map[string]any{"recursive": true}).Process(ctx, message.New("r"))
			if err != nil || out[message.Body] != "A" || out[FileName] != "a.txt" {
				t.Fatalf("enrich: body %v, file %v, err %v", out[message.Body], out[FileName], err)
			}
			// A second file with the name of the first: moving it replaces the archived one.
			write(t, e.local("in/a.txt"), "A2")
			if _, err := remoteEnrichStep(t, e, "in", nil).Process(ctx, message.New("r")); err != nil {
				t.Fatal(err)
			}
			if read(t, e.local("in/.archive/a.txt")) != "A2" || exists(e.local("in/a.txt")) {
				t.Errorf("in has %v, want the second a.txt archived over the first", names(t, e.local("in")))
			}

			// Writing, with each fileExist.
			m := message.New("body")
			m[FileName] = "w.txt"
			if err := sinkStep(t, e, "out", map[string]any{"fileExist": "Fail"}).Consume(ctx, m); err != nil {
				t.Fatal(err)
			}
			if err := sinkStep(t, e, "out", map[string]any{"fileExist": "Fail"}).Consume(ctx, m); err == nil || !strings.Contains(err.Error(), "already exists") {
				t.Errorf("second Fail: err = %v", err)
			}
		})
	}
}

func TestFTPUsesTheControlAddressForData(t *testing.T) {
	// The fake reports 10.9.9.9 in its PASV reply, which is not routable here: a
	// client that went there would hang. The client uses the address it connected to.
	e := newFTPEnv(t, func(f *fakeFTP) { f.noEPSV = true })
	write(t, e.local("in/a.txt"), "A")
	start := time.Now()
	out, err := remoteEnrichStep(t, e, "in", map[string]any{"socketTimeout": 3000}).Process(context.Background(), message.New("r"))
	if err != nil || out[message.Body] != "A" || time.Since(start) > 2*time.Second {
		t.Errorf("body %v, err %v after %v", out[message.Body], err, time.Since(start))
	}
}

func TestFTPCommandInjection(t *testing.T) {
	var f *fakeFTP
	e := newFTPEnv(t, func(ff *fakeFTP) { f = ff })
	write(t, e.local("victim.txt"), "V")
	c, err := dialFTP(context.Background(), e.host, "dif", "s3cret", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer c.close()
	for _, evil := range []string{"x\r\nDELE victim.txt", "x\nDELE victim.txt", "a\rQUIT"} {
		if _, err := c.read(evil); err == nil || !strings.Contains(err.Error(), "line break") {
			t.Errorf("read(%q): err = %v, want a refusal", evil, err)
		}
		if err := c.remove(evil); err == nil {
			t.Errorf("remove(%q): want an error", evil)
		}
	}
	if !exists(e.local("victim.txt")) || len(f.commands("DELE", "QUIT")) != 0 {
		t.Errorf("an injected command reached the server: %v", f.commands("DELE", "QUIT"))
	}
}

func TestFTPAnonymousLogin(t *testing.T) {
	e := newFTPEnv(t, func(f *fakeFTP) { f.user, f.pass = "", "" })
	write(t, e.local("in/a.txt"), "A")
	out, err := remoteEnrichStep(t, e, "in", map[string]any{"userName": nil, "password": nil, "move": ""}).Process(context.Background(), message.New("r"))
	if err != nil || out[message.Body] != "A" {
		t.Errorf("body %v, err %v", out[message.Body], err)
	}
}

func TestFTPUserAndPasswordFromTheURIAndEnvironment(t *testing.T) {
	e := newFTPEnv(t, nil)
	write(t, e.local("in/a.txt"), "A")
	t.Setenv("DIF_FTP_PASSWORD", "s3cret")
	uri := "ftpenrich://dif@" + e.host + "/in"
	p := mustProcessor(t, stepdef.Action, uri, map[string]any{"move": ""}).(stepdef.ActionProcessor)
	out, err := p.Process(context.Background(), message.New("r"))
	if err != nil || out[message.Body] != "A" {
		t.Errorf("user in the URI, password from the environment: body %v, err %v", out[message.Body], err)
	}
}

func TestFTPAbsoluteDirectoryAfterADoubleSlash(t *testing.T) {
	var f *fakeFTP
	e := newFTPEnv(t, func(ff *fakeFTP) { f = ff })
	write(t, e.local("abs/a.txt"), "A")
	p := mustProcessor(t, stepdef.Action, "ftpenrich:"+e.host+"//abs", e.opts(map[string]any{"move": ""})).(stepdef.ActionProcessor)
	if _, err := p.Process(context.Background(), message.New("r")); err != nil {
		t.Fatal(err)
	}
	if got := f.commands("MLSD"); len(got) != 1 || got[0] != "MLSD /abs" {
		t.Errorf("listing commands = %v, want MLSD /abs", got)
	}
}

func TestParseListings(t *testing.T) {
	files, err := parseList([]byte("total 12\r\n" +
		"-rw-r--r--   1 dif staff      12 Jan  2 15:04 a file.txt\r\n" +
		"drwxr-xr-x   2 dif staff    4096 Jan  2  2020 sub\r\n" +
		"-rw-r--r--   1 dif      7 Feb 30 10:00 nogroup.txt\r\n" +
		"lrwxrwxrwx   1 dif staff       5 Jan  2 15:04 link -> a\r\n" +
		"-rw-r--r--+  1 dif staff       1 Jan  2  2021 acl.txt\r\n" +
		"drwxr-xr-x   2 dif staff    4096 Jan  2  2020 ..\r\n" +
		"01-02-24  03:04PM       <DIR>          dos dir\r\n" +
		"01-02-24  03:04PM               1234 dos file.txt\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range files {
		kind := "file"
		if f.dir {
			kind = "dir"
		}
		got = append(got, kind+":"+f.name+":"+strconv.FormatInt(f.size, 10))
	}
	want := "file:a file.txt:12 dir:sub:4096 file:nogroup.txt:7 file:acl.txt:1 dir:dos dir:0 file:dos file.txt:1234"
	if strings.Join(got, " ") != want {
		t.Errorf("listing:\n got %s\nwant %s", strings.Join(got, " "), want)
	}
	if files[2].modTime.IsZero() == false {
		t.Errorf("the impossible date Feb 30 gave a time: %v", files[2].modTime)
	}
	if files[1].modTime.Year() != 2020 {
		t.Errorf("sub modTime = %v, want 2020", files[1].modTime)
	}
	if _, err := parseList([]byte("this is no listing\r\n")); err == nil || !strings.Contains(err.Error(), "cannot parse listing line") {
		t.Errorf("err = %v", err)
	}

	mlsd, err := parseMLSD([]byte("type=cdir;modify=20240102150405; .\r\ntype=pdir; ..\r\n" +
		"type=file;size=12;modify=20240102150405.123; a b.txt\r\ntype=dir;modify=20240102150405; sub\r\n" +
		"Type=FILE;Size=3; upper.txt\r\ntype=OS.unix=symlink; link\r\n"))
	if err != nil {
		t.Fatal(err)
	}
	if len(mlsd) != 3 || mlsd[0].name != "a b.txt" || mlsd[0].size != 12 || mlsd[0].modTime.Format("2006-01-02 15:04:05") != "2024-01-02 15:04:05" ||
		!mlsd[1].dir || mlsd[2].name != "upper.txt" || mlsd[2].size != 3 {
		t.Errorf("mlsd = %+v", mlsd)
	}
	if _, err := parseMLSD([]byte("garbage")); err == nil {
		t.Error("garbage MLSD: want an error")
	}
}

func TestParseUnixTime(t *testing.T) {
	if got := parseUnixTime("Mar  5  2021"); got.Format("2006-01-02") != "2021-03-05" {
		t.Errorf("with a year: %v", got)
	}
	recent := time.Now().UTC().AddDate(0, 0, -10)
	if got := parseUnixTime(recent.Format("Jan _2 15:04")); got.Sub(recent) > time.Minute || recent.Sub(got) > time.Minute {
		t.Errorf("recent: %v, want about %v", got, recent)
	}
	future := time.Now().UTC().AddDate(0, 0, 40) // a time of the last year that looks like the future
	if got := parseUnixTime(future.Format("Jan _2 15:04")); got.After(time.Now().UTC().Add(48 * time.Hour)) {
		t.Errorf("a date ahead of now: %v, want the same date a year ago", got)
	}
	if got := parseUnixTime("nonsense"); !got.IsZero() {
		t.Errorf("nonsense: %v", got)
	}
}

func TestParseRemoteURI(t *testing.T) {
	for in, want := range map[string]remoteURI{
		"host/a/b":                    {"", "host", "21", "a/b"},
		"host:2121/a/b/":              {"", "host", "2121", "a/b"},
		"//dif@host:22/dir":           {"dif", "host", "22", "dir"},
		"host//abs/dir":               {"", "host", "21", "/abs/dir"},
		"host":                        {"", "host", "21", "."},
		"host/":                       {"", "host", "21", "."},
		"[::1]:2121/x":                {"", "::1", "2121", "x"},
		"[::1]/x":                     {"", "::1", "21", "x"},
		"api.example.com:2121/a/_b/c": {"", "api.example.com", "2121", "a/_b/c"},
		"u@h/../up":                   {"u", "h", "21", "../up"},
	} {
		got, err := parseRemoteURI(in, "21")
		if err != nil || got != want {
			t.Errorf("parseRemoteURI(%q) = %+v, %v, want %+v", in, got, err, want)
		}
	}
	for _, in := range []string{"", "/dir", ":21/dir", "host:0/x", "host:99999/x", "host:abc/x", "host:2:3/x", "ho\nst/x", "host/a\rb"} {
		if _, err := parseRemoteURI(in, "21"); err == nil {
			t.Errorf("parseRemoteURI(%q): want an error", in)
		}
	}
}

func TestRemoteHelpers(t *testing.T) {
	for in, want := range map[string]string{"RAW(secret)": "secret", "RAW()": "", "plain": "plain", "RAW(a(b)c)": "a(b)c", "RAW(open": "RAW(open", "xRAW(a)": "xRAW(a)", "": ""} {
		if got := unRaw(in); got != want {
			t.Errorf("unRaw(%q) = %q, want %q", in, got, want)
		}
	}
	for name, want := range map[string]bool{
		"a.txt": true, "sub/a.txt": true, "a..b": true, "": false, ".": false, "..": false, "../a": false, "a/../b": false, "a/..": false,
		"/abs": false, "a\nb": false, "a\x00b": false, "./a": true,
	} {
		if got := validRemoteName(name); got != want {
			t.Errorf("validRemoteName(%q) = %v, want %v", name, got, want)
		}
	}
	for _, tc := range []struct{ dir, folder, rel, want string }{
		{"in", ".archive", "a.txt", "in/.archive/a.txt"},
		{"in", ".archive", "x/a.txt", "in/.archive/x/a.txt"},
		{".", "done", "a.txt", "done/a.txt"},
		{"in", "/var/done", "x/a.txt", "/var/done/x/a.txt"},
		{"/abs/in", "done", "a.txt", "/abs/in/done/a.txt"},
	} {
		if got := moveTarget(tc.dir, tc.folder, tc.rel); got != tc.want {
			t.Errorf("moveTarget(%q, %q, %q) = %q, want %q", tc.dir, tc.folder, tc.rel, got, tc.want)
		}
	}
	if got := string(latin1Charset.encode("é€")); got != "\xe9?" {
		t.Errorf("encode = %q", got)
	}
}

func TestSFTPKeyLogin(t *testing.T) {
	for name, passphrase := range map[string]string{"plain key": "", "encrypted key": "open sesame"} {
		t.Run(name, func(t *testing.T) {
			s := newFakeSFTP(t)
			key := s.clientKeyFile(t, passphrase)
			write(t, s.root+"/in/a.txt", "A")
			e := remoteEnv{"sftp", s.root, s.addr, map[string]any{"userName": s.user, "privateKey": key, "strictHostKeyChecking": false}, func() int { return 0 }}
			if passphrase != "" {
				// Without the passphrase the flow does not load.
				wantInvalid(t, stepdef.Action, e.uri("sftpenrich", "in"), e.opts(nil), "the key is encrypted")
				t.Setenv("DIF_SFTP_PRIVATE_KEY_PASSPHRASE", passphrase)
			}
			out, err := remoteEnrichStep(t, e, "in", map[string]any{"move": ""}).Process(context.Background(), message.New("r"))
			if err != nil || out[message.Body] != "A" {
				t.Errorf("body %v, err %v", out[message.Body], err)
			}
		})
	}
}

func TestSFTPHostKeyChecking(t *testing.T) {
	s := newFakeSFTP(t)
	write(t, s.root+"/in/a.txt", "A")
	login := func(extra map[string]any) error {
		opts := map[string]any{"userName": s.user, "password": s.pass, "move": ""}
		for k, v := range extra {
			opts[k] = v
		}
		p := mustProcessor(t, stepdef.Action, "sftpenrich:"+s.addr+"/in", opts).(stepdef.ActionProcessor)
		_, err := p.Process(context.Background(), message.New("r"))
		return err
	}

	// Strict is the default. Nothing is known about this server.
	empty := t.TempDir() + "/known_hosts"
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := login(map[string]any{"knownHostsFile": empty}); err == nil || !strings.Contains(err.Error(), "knownhosts: key is unknown") {
		t.Errorf("unknown host: err = %v, want the key to be refused", err)
	}
	// A different key under its name.
	other := newFakeSFTP(t)
	if err := login(map[string]any{"knownHostsFile": s.knownHostsFile(t, other.hostKey)}); err == nil || !strings.Contains(err.Error(), "knownhosts: key mismatch") {
		t.Errorf("changed key: err = %v, want a mismatch", err)
	}
	if err := login(map[string]any{"knownHostsFile": s.knownHostsFile(t, s.hostKey)}); err != nil {
		t.Errorf("known host: %v", err)
	}
	if err := login(map[string]any{"strictHostKeyChecking": false}); err != nil {
		t.Errorf("strictHostKeyChecking false: %v", err)
	}
	// A missing file explains what to do, when the step is used, not when the flow is loaded.
	if err := login(map[string]any{"knownHostsFile": t.TempDir() + "/none"}); err == nil || !strings.Contains(err.Error(), "set knownHostsFile, or strictHostKeyChecking to false") {
		t.Errorf("no known_hosts: err = %v", err)
	}
}

func TestSFTPKeyAndPassword(t *testing.T) {
	// A wrong key falls back to the password.
	s := newFakeSFTP(t)
	otherKey := newFakeSFTP(t).clientKeyFile(t, "") // a key this server does not know
	write(t, s.root+"/in/a.txt", "A")
	p := mustProcessor(t, stepdef.Action, "sftpenrich:"+s.addr+"/in", map[string]any{
		"userName": s.user, "password": s.pass, "privateKey": otherKey, "strictHostKeyChecking": false, "move": "",
	}).(stepdef.ActionProcessor)
	if out, err := p.Process(context.Background(), message.New("r")); err != nil || out[message.Body] != "A" {
		t.Errorf("body %v, err %v", out[message.Body], err)
	}
}

func TestRemoteOptionValidation(t *testing.T) {
	base := map[string]any{"userName": "u", "password": "p"}
	with := func(extra map[string]any) map[string]any {
		o := map[string]any{}
		for k, v := range base {
			o[k] = v
		}
		for k, v := range extra {
			if v == nil {
				delete(o, k)
			} else {
				o[k] = v
			}
		}
		return o
	}
	invalid := func(kind, uri string, opts map[string]any, want string) {
		t.Helper()
		wantInvalid(t, kind, uri, opts, want)
	}

	// Common.
	for _, scheme := range []string{"ftp", "sftp"} {
		host := scheme + ":h/dir"
		invalid(stepdef.Source, scheme+":", with(nil), "missing required option path")
		invalid(stepdef.Source, scheme+":h:abc/dir", with(nil), "uri: want <host>[:<port>]/<directory>")
		invalid(stepdef.Source, host, with(map[string]any{"fileName": "a", "include": ".*"}), "cannot be used with include or exclude")
		invalid(stepdef.Source, host, with(map[string]any{"include": "("}), "option include")
		invalid(stepdef.Source, host, with(map[string]any{"exclude": "("}), "option exclude")
		invalid(stepdef.Source, host, with(map[string]any{"charset": "EBCDIC"}), "option charset")
		invalid(stepdef.Source, host, with(map[string]any{"delete": false, "move": ""}), "option move: empty")
		invalid(stepdef.Source, host, with(map[string]any{"readLock": "exclusive"}), "readLock")
		invalid(stepdef.Source, host, with(map[string]any{"sortBy": "size"}), "sortBy")
		invalid(stepdef.Source, host, with(map[string]any{"delay": 0}), "delay")
		invalid(stepdef.Source, host, with(map[string]any{"unknown": 1}), "unknown option")
		invalid(stepdef.Sink, host, with(map[string]any{"fileExist": "Rename"}), "fileExist")
		invalid(stepdef.Sink, host, with(map[string]any{"fileName": "../x"}), "is not a name below the directory")
		invalid(stepdef.Sink, host, with(map[string]any{"charset": "EBCDIC"}), "option charset")
		invalid(stepdef.Action, scheme+"enrich:h/dir", with(map[string]any{"abortMode": "perhaps"}), "abortMode")
	}
	// FTP.
	invalid(stepdef.Source, "ftp:h/d", with(map[string]any{"passiveMode": false}), "active mode is not supported")
	invalid(stepdef.Sink, "ftp:h/d", with(map[string]any{"implicit": true}), "FTPS is not supported")
	mustProcessor(t, stepdef.Sink, "ftp:h/d", with(map[string]any{"implicit": false}))
	invalid(stepdef.Source, "ftp:h/d", with(map[string]any{"privateKey": "k"}), "unknown option")
	// SFTP.
	mustProcessor(t, stepdef.Source, "sftp:h/d", with(map[string]any{"passiveMode": false})) // no effect
	invalid(stepdef.Source, "sftp:h/d", with(map[string]any{"userName": nil}), "option userName: required for sftp")
	invalid(stepdef.Source, "sftp:h/d", with(map[string]any{"password": nil}), "option password or privateKey")
	invalid(stepdef.Source, "sftp:h/d", with(map[string]any{"privateKey": t.TempDir() + "/none"}), "option privateKey")
	bad := t.TempDir() + "/bad"
	os.WriteFile(bad, []byte("not a key"), 0o600)
	invalid(stepdef.Source, "sftp:h/d", with(map[string]any{"privateKey": bad}), "option privateKey")
	mustProcessor(t, stepdef.Source, "sftp://u@h:2222/d", with(map[string]any{"userName": nil, "strictHostKeyChecking": true}))
}

// The options of examples/sftpInbound.json, sftpOutbound.json and sftpEnrich.json.
func TestRemoteExampleOptions(t *testing.T) {
	const host = "api-testing.dovetail.world"
	mustProcessor(t, stepdef.Source, "ftp:"+host+":2121/development/_New2/test/out/", map[string]any{
		"fileName": "FtpOutbound1.xml", "charset": "utf-8", "delay": 60000, "userName": "dovetail", "password": "RAW(secret)", "binary": false,
		"passiveMode": true, "disconnect": true, "autoCreate": true, "maxMessagesPerPoll": 1, "recursive": false, "delete": false,
		"move": "RAW(.dovetail)", "moveFailed": ".error", "readLock": "none", "initialDelay": "60000",
	})
	mustProcessor(t, stepdef.Sink, "ftp:"+host+":2121/development/_New2/test/out/", map[string]any{
		"fileName": "ComponentTest.txt", "binary": false, "charset": "utf-8", "fileExist": "Override", "userName": "dovetail",
		"password": "RAW(secret)", "implicit": false, "passiveMode": true, "disconnect": true, "autoCreate": true,
	})
	mustProcessor(t, stepdef.Action, "sftpenrich:"+host+":2022/development/_New2/test/out", map[string]any{
		"fileName": "ComponentTest.txt", "charset": "utf-8", "userName": "dovetail", "password": "RAW(secret)", "binary": false,
		"disconnect": true, "autoCreate": true, "passiveMode": true, "abortMode": true, "maxMessagesPerPoll": 1, "recursive": false,
		"delete": false, "move": "RAW(archive)", "moveFailed": "RAW(error)", "readLock": "none",
	})
}
