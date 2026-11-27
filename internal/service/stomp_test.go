package service

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"dif/api"
)

// stompClient is a minimal STOMP client for driving a service.
type stompClient struct {
	t    *testing.T
	conn net.Conn
	r    *bufio.Reader
}

func (c *stompClient) send(command, body string, headers ...string) {
	c.t.Helper()
	var b strings.Builder
	b.WriteString(command + "\n")
	for i := 0; i+1 < len(headers); i += 2 {
		b.WriteString(headers[i] + ":" + headers[i+1] + "\n")
	}
	b.WriteString("\n" + body + "\x00")
	if _, err := io.WriteString(c.conn, b.String()); err != nil {
		c.t.Fatal(err)
	}
}

// expect reads a frame (without escapes or content-length) and returns its
// headers and body.
func (c *stompClient) expect(command string) (map[string]string, string) {
	c.t.Helper()
	c.conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	raw, err := c.r.ReadString(0)
	if err != nil {
		c.t.Fatal(err)
	}
	head, body, _ := strings.Cut(strings.TrimLeft(strings.TrimSuffix(raw, "\x00"), "\n"), "\n\n")
	lines := strings.Split(head, "\n")
	headers := map[string]string{}
	for _, line := range lines[1:] {
		name, value, _ := strings.Cut(line, ":")
		if _, set := headers[name]; !set {
			headers[name] = value
		}
	}
	if lines[0] != command {
		c.t.Fatalf("got %s %v, want %s", lines[0], headers, command)
	}
	return headers, body
}

func stompConfig() *api.ChannelConfig {
	return &api.ChannelConfig{
		Queues: map[string]api.QueueConfig{"orders": {Served: true}, "outbox": {Served: true}},
		Topics: map[string]api.TopicConfig{"audit": {Served: true}},
		STOMP:  &api.STOMPConfig{Address: "127.0.0.1:0", Login: "orders-app"},
	}
}

func TestServedChannelsConnectClientsAndFlows(t *testing.T) {
	t.Setenv(STOMPPasswordEnv, "test-passcode")
	dir := t.TempDir()
	// The flow consumes a served queue and publishes to a served topic that
	// only STOMP clients subscribe to.
	path := writeFlow(t, dir, "relay.json", "relay", "queue:orders", nil, []map[string]any{action("topic:audit", nil)})
	o := options(t, "--file", path, "--shutdown-timeout=2s")
	o.Channels = stompConfig()
	var logs bytes.Buffer
	s := New(o, &logs)
	cancel, done := startService(t, s)
	await(t, func() bool { ready, _, _, _ := s.snapshot(); return ready })
	conn, err := net.Dial("tcp", s.stomp.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	c := &stompClient{t: t, conn: conn, r: bufio.NewReader(conn)}
	c.send("CONNECT", "", "accept-version", "1.2", "host", "dif", "login", "orders-app", "passcode", "test-passcode")
	c.expect("CONNECTED")
	c.send("SUBSCRIBE", "", "id", "audit", "destination", "/topic/audit", "receipt", "subscribed")
	c.expect("RECEIPT")
	c.send("SEND", "order-1", "destination", "/queue/orders", "orderId", "1", "receipt", "sent")
	c.expect("RECEIPT")
	headers, body := c.expect("MESSAGE")
	if body != "order-1" || headers["orderId"] != "1" || headers["destination"] != "/topic/audit" || headers["DIF-Trace-Id"] == "" {
		t.Fatal(headers, body)
	}
	status := httptest.NewRecorder()
	s.Handler().ServeHTTP(status, httptest.NewRequest("GET", "/status", nil))
	if !strings.Contains(status.Body.String(), `"stomp":{"connections":1,"subscriptions":1}`) {
		t.Fatal(status.Body.String())
	}
	metrics := httptest.NewRecorder()
	s.Handler().ServeHTTP(metrics, httptest.NewRequest("GET", "/metrics", nil))
	if !strings.Contains(metrics.Body.String(), "dif_stomp_connections 1\n") {
		t.Fatal(metrics.Body.String())
	}
	// A memory queue that no flow consumes does not hold up the drain.
	c.send("SEND", "unread", "destination", "/queue/outbox", "receipt", "parked")
	c.expect("RECEIPT")
	cancel()
	if err := finish(t, done); err != nil {
		t.Fatal(err)
	}
	// The service closed the client before draining.
	conn.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := c.r.ReadString(0); err == nil {
		t.Fatal("client still connected")
	}
	if !strings.Contains(logs.String(), "STOMP connection opened") || strings.Contains(logs.String(), "test-passcode") || strings.Contains(logs.String(), "order-1") {
		t.Fatal(logs.String())
	}
}

func TestSTOMPExampleRelaysQueueToTopic(t *testing.T) {
	t.Setenv(STOMPPasswordEnv, "test-passcode")
	root := filepath.Join("..", "..")
	o, err := ParseOptions([]string{"--config", filepath.Join(root, "testdata", "reliable", "stomp-service.json")}, func(string) string { return "" }, false)
	if err != nil {
		t.Fatal(err)
	}
	for i, path := range o.Files {
		o.Files[i] = filepath.Join(root, path)
	}
	o.Channels.Directory = t.TempDir()
	o.Channels.STOMP.Address = "127.0.0.1:0"
	s := New(o, io.Discard)
	cancel, done := startService(t, s)
	await(t, func() bool { ready, _, _, _ := s.snapshot(); return ready })
	var clients []*stompClient
	for range 2 {
		conn, err := net.Dial("tcp", s.stomp.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		c := &stompClient{t: t, conn: conn, r: bufio.NewReader(conn)}
		c.send("CONNECT", "", "accept-version", "1.2", "host", "dif", "login", "orders-app", "passcode", "test-passcode")
		c.expect("CONNECTED")
		clients = append(clients, c)
	}
	subscriber, sender := clients[0], clients[1]
	subscriber.send("SUBSCRIBE", "", "id", "audit", "destination", "/topic/audit", "receipt", "on")
	subscriber.expect("RECEIPT")
	sender.send("SEND", "hello", "destination", "/queue/incoming", "content-type", "text/plain", "orderId", "42", "receipt", "sent")
	sender.expect("RECEIPT")
	headers, body := subscriber.expect("MESSAGE")
	if body != "hello" || headers["orderId"] != "42" || headers["relayedBy"] != "dif" || headers["content-type"] != "text/plain" {
		t.Fatal(headers, body)
	}
	cancel()
	if err := finish(t, done); err != nil {
		t.Fatal(err)
	}
}

func TestSTOMPRequiresPasscodeAtStart(t *testing.T) {
	t.Setenv(STOMPPasswordEnv, "")
	path := writeFlow(t, t.TempDir(), "relay.json", "relay", "queue:orders", nil, []map[string]any{action("topic:audit", nil)})
	o := options(t, "--file", path)
	o.Channels = stompConfig()
	if err := New(o, io.Discard).Run(t.Context()); err == nil || !strings.Contains(err.Error(), STOMPPasswordEnv) {
		t.Fatal(err)
	}
}

func TestSTOMPConfigurationValidatesWithoutPasscode(t *testing.T) {
	t.Setenv(STOMPPasswordEnv, "")
	dir := t.TempDir()
	path := filepath.Join(dir, "service.json")
	write := func(stomp map[string]any) error {
		config := map[string]any{"files": []string{"flow.json"}, "channels": map[string]any{
			"queues": map[string]any{"orders": map[string]any{"served": true}},
			"stomp":  stomp,
		}}
		data, _ := json.Marshal(config)
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		_, err := ParseOptions([]string{"--config", path}, func(string) string { return "" }, true)
		return err
	}
	if err := write(map[string]any{"address": "127.0.0.1:61613", "login": "orders-app", "maxConnections": 5}); err != nil {
		t.Fatal(err)
	}
	if err := write(map[string]any{"address": "0.0.0.0:61613", "login": "orders-app"}); err == nil || !strings.Contains(err.Error(), "tls") {
		t.Fatal("non-loopback without TLS accepted:", err)
	}
	if err := write(map[string]any{"address": "127.0.0.1:61613", "login": "orders-app", "passcode": "x"}); err == nil {
		t.Fatal("unknown field accepted")
	}
}
