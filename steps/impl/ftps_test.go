package impl

import (
	"context"
	"strings"
	"testing"

	"dif/message"
	stepdef "dif/steps/definition"
)

// ftpsEnv starts an FTPS server and returns it with its environment.
func ftpsEnv(t *testing.T, implicit bool, configure func(*fakeFTP)) (*fakeFTP, remoteEnv) {
	t.Helper()
	var f *fakeFTP
	e := newFTPSEnv(t, implicit, func(ff *fakeFTP) {
		f = ff
		if configure != nil {
			configure(ff)
		}
	})
	return f, e
}

func readFTPS(t *testing.T, e remoteEnv, extra map[string]any) (message.Message, error) {
	t.Helper()
	write(t, e.local("in/a.txt"), "A")
	return remoteEnrichStep(t, e, "in", extra).Process(context.Background(), message.New("r"))
}

func TestFTPSExplicitSecuresTheControlAndDataConnections(t *testing.T) {
	f, e := ftpsEnv(t, false, func(f *fakeFTP) { f.requireReuse = true })
	out, err := readFTPS(t, e, nil)
	if err != nil || out[message.Body] != "A" {
		t.Fatalf("body %v, err %v", out[message.Body], err)
	}

	// The server was asked for TLS before the login, so the password never goes in the clear,
	// and for private data connections.
	got := f.commands("AUTH", "PBSZ", "PROT", "USER", "PASS")
	if want := "AUTH TLS|PBSZ 0|PROT P|USER dif|PASS s3cret"; strings.Join(got, "|") != want {
		t.Errorf("commands = %q, want %q", got, want)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.authTLS != 1 {
		t.Errorf("%d connections upgraded, want 1", f.authTLS)
	}
	if f.tlsData == 0 || f.resumed != f.tlsData {
		t.Errorf("%d data connections in TLS, %d resumed the session of the control connection, want all", f.tlsData, f.resumed)
	}
}

func TestFTPSImplicitNeedsNoAuth(t *testing.T) {
	f, e := ftpsEnv(t, true, func(f *fakeFTP) { f.requireReuse = true })
	out, err := readFTPS(t, e, nil)
	if err != nil || out[message.Body] != "A" {
		t.Fatalf("body %v, err %v", out[message.Body], err)
	}
	if got := f.commands("AUTH"); len(got) != 0 {
		t.Errorf("sent %q on a connection that is TLS from the start", got)
	}
	if got := f.commands("PBSZ", "PROT"); len(got) != 2 {
		t.Errorf("commands = %q, want PBSZ 0 and PROT P", got)
	}
}

func TestFTPSImplicitPortIs990(t *testing.T) {
	// Nothing listens on 990 here: the error names the address that was dialled.
	p := mustProcessor(t, stepdef.Action, "ftpsenrich:127.0.0.1/in", map[string]any{
		"implicit": true, "userName": "u", "password": "p", "socketTimeout": 500, "move": "", "trustStoreFile": "",
	}).(stepdef.ActionProcessor)
	if _, err := p.Process(context.Background(), message.New("r")); err == nil || !strings.Contains(err.Error(), "127.0.0.1:990") {
		t.Errorf("err = %v, want the connection to 127.0.0.1:990 refused", err)
	}
	p = mustProcessor(t, stepdef.Action, "ftpsenrich:127.0.0.1/in", map[string]any{
		"userName": "u", "password": "p", "socketTimeout": 500, "move": "", "trustStoreFile": "",
	}).(stepdef.ActionProcessor)
	if _, err := p.Process(context.Background(), message.New("r")); err == nil || !strings.Contains(err.Error(), "127.0.0.1:21") {
		t.Errorf("err = %v, want the connection to 127.0.0.1:21 refused", err)
	}
}

func TestFTPSServerThatClosesWithoutCloseNotify(t *testing.T) {
	_, e := ftpsEnv(t, false, func(f *fakeFTP) { f.noCloseNote = true })
	out, err := readFTPS(t, e, nil)
	if err != nil || out[message.Body] != "A" {
		t.Errorf("body %v, err %v: a data connection that ends without close_notify is complete when the server says so", out[message.Body], err)
	}
}

func TestFTPSDistrustsAServerItDoesNotKnow(t *testing.T) {
	_, e := ftpsEnv(t, false, nil)
	// The system's roots do not include the test identity.
	_, err := readFTPS(t, e, map[string]any{"trustStoreFile": ""})
	if err == nil || !strings.Contains(err.Error(), "certificate") {
		t.Errorf("err = %v, want an untrusted certificate", err)
	}
}

func TestFTPSAgainstAServerWithoutTLS(t *testing.T) {
	plain := newFTPEnv(t, nil)
	write(t, plain.local("in/a.txt"), "A")
	opts := plain.opts(map[string]any{"trustStoreFile": testTrustStore, "trustStorePassword": testPassword, "move": "", "socketTimeout": 2000})
	p := mustProcessor(t, stepdef.Action, "ftpsenrich:"+plain.host+"/in", opts).(stepdef.ActionProcessor)
	if _, err := p.Process(context.Background(), message.New("r")); err == nil || !strings.Contains(err.Error(), "does not do FTPS") {
		t.Errorf("err = %v, want that the server does not do FTPS", err)
	}
}

func TestFTPSPasswordFromTheEnvironment(t *testing.T) {
	_, e := ftpsEnv(t, false, nil)
	t.Setenv("DIF_FTPS_PASSWORD", "s3cret")
	out, err := readFTPS(t, e, map[string]any{"password": nil})
	if err != nil || out[message.Body] != "A" {
		t.Errorf("body %v, err %v", out[message.Body], err)
	}
}

func TestFTPSOptions(t *testing.T) {
	login := map[string]any{"userName": "u", "password": "p"}
	with := func(extra map[string]any) map[string]any {
		o := map[string]any{}
		for k, v := range login {
			o[k] = v
		}
		for k, v := range extra {
			o[k] = v
		}
		return o
	}
	wantInvalid(t, stepdef.Source, "ftps:h/d", with(map[string]any{"passiveMode": false}), "active mode is not supported")
	wantInvalid(t, stepdef.Sink, "ftps:h/d", with(map[string]any{"trustStoreFile": "missing.p12"}), "trust store: open missing.p12")
	mustProcessor(t, stepdef.Sink, "ftps:h/d", with(map[string]any{"implicit": true, "trustStoreFile": ""}))
	// The default trust store is the one of the https steps.
	if _, err := newProcessor(stepdef.Sink, "ftps:h/d", with(nil)); err == nil || !strings.Contains(err.Error(), "security/outbound-truststore.p12") {
		t.Errorf("err = %v, want the default trust store security/outbound-truststore.p12", err)
	}
}
