package impl

import (
	"context"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"dif/engine"
	flowdef "dif/flows/definition"
	"dif/message"
	stepdef "dif/steps/definition"
)

func httpRunner(t *testing.T, addr string, sink stepdef.Processor) *engine.Runner {
	t.Helper()
	source := mustProcessor(t, stepdef.Source, "https://"+addr+"/test", map[string]any{"serverIdentityFile": testIdentity, "serverIdentityPassword": testPassword})
	f := &flowdef.Flow{ID: "http-lifecycle", Source: &flowdef.Node{ID: "source", Kind: stepdef.Source, Processor: source, Next: []*flowdef.Node{{ID: "sink", Kind: stepdef.Sink, Processor: sink}}}}
	r := engine.NewRunner(f, nil)
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		r.Cancel()
		r.StopContext(ctx)
	})
	return r
}

func TestHTTPStartContextReportsBindFailure(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	r := httpRunner(t, ln.Addr().String(), passthrough{})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := r.StartContext(ctx); err == nil {
		t.Fatal("bind failure reported ready")
	}
}

func TestHTTPUnexpectedListenerFailureIsObservable(t *testing.T) {
	addr := freeAddr(t)
	r := httpRunner(t, addr, passthrough{})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := r.StartContext(ctx); err != nil {
		t.Fatal(err)
	}
	httpsServers.Lock()
	server := httpsServers.m[addr]
	httpsServers.Unlock()
	if err := server.ln.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-r.SourceFailed():
		if r.SourceError() == nil {
			t.Fatal("missing listener error")
		}
	case <-ctx.Done():
		t.Fatal("listener failure not reported")
	}
}

type heldHTTPReply struct{ entered, release chan struct{} }

func (a heldHTTPReply) Process(ctx context.Context, m message.Message) (message.Message, error) {
	close(a.entered)
	select {
	case <-a.release:
		return m, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestHTTPStopSourcePreservesAcceptedReply(t *testing.T) {
	addr := freeAddr(t)
	action := heldHTTPReply{make(chan struct{}), make(chan struct{})}
	r := httpRunner(t, addr, action)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := r.StartContext(ctx); err != nil {
		t.Fatal(err)
	}
	response := make(chan error, 1)
	client := trustingClient(t)
	go func() {
		res, err := client.Post("https://"+addr+"/test", "text/plain", strings.NewReader("accepted"))
		if err != nil {
			response <- err
			return
		}
		defer res.Body.Close()
		data, err := io.ReadAll(res.Body)
		if err == nil && (res.StatusCode != 200 || string(data) != "accepted") {
			err = io.ErrUnexpectedEOF
		}
		response <- err
	}()
	select {
	case <-action.entered:
	case <-ctx.Done():
		t.Fatal("request did not reach flow")
	}
	r.StopSource()
	select {
	case <-r.SourceDone():
		t.Fatal("source closed before HTTP reply drained")
	default:
	}
	close(action.release)
	select {
	case err := <-response:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("reply lost during shutdown")
	}
	if err := r.StopContext(ctx); err != nil {
		t.Fatal(err)
	}
}
