package stomp

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"math/big"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"dif/internal/channels"
	"dif/message"
)

const (
	testLogin    = "app"
	testPasscode = "test-passcode"
)

// start serves c on a loopback port. The server and runtime close when the
// test ends, after the test's clients.
func start(t *testing.T, c channels.Config, tune ...func(*Server)) (*Server, *channels.Runtime) {
	t.Helper()
	if c.STOMP == nil {
		c.STOMP = &channels.STOMPConfig{Address: "127.0.0.1:0", Login: testLogin}
	}
	r, err := channels.Open(c)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Close() })
	srv := New(r)
	srv.Passcode = testPasscode
	for _, f := range tune {
		f(srv)
	}
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })
	return srv, r
}

func served(queues ...string) channels.Config {
	c := channels.Config{Queues: map[string]channels.QueueConfig{}, Topics: map[string]channels.TopicConfig{"news": {Served: true}}}
	for _, q := range queues {
		c.Queues[q] = channels.QueueConfig{Served: true}
	}
	return c
}

type client struct {
	t    *testing.T
	conn net.Conn
	r    *reader
	w    *bufio.Writer
}

func dial(t *testing.T, srv *Server) *client {
	t.Helper()
	conn, err := net.Dial("tcp", srv.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return wrap(t, conn)
}

func wrap(t *testing.T, conn net.Conn) *client {
	return &client{t: t, conn: conn, r: newReader(conn, maxLine, maxHeaders, 1<<20), w: bufio.NewWriter(conn)}
}

// connect dials and logs in; headers are name, value pairs added to CONNECT.
func connect(t *testing.T, srv *Server, headers ...string) *client {
	t.Helper()
	c := dial(t, srv)
	c.send("CONNECT", "", append([]string{"accept-version", "1.2", "host", "dif", "login", testLogin, "passcode", testPasscode}, headers...)...)
	c.expect("CONNECTED")
	return c
}

func (c *client) send(command, body string, headers ...string) {
	c.t.Helper()
	f := &Frame{Command: command, Body: []byte(body)}
	for i := 0; i+1 < len(headers); i += 2 {
		f.add(headers[i], headers[i+1])
	}
	writeFrame(c.w, f)
	if err := c.w.Flush(); err != nil {
		c.t.Fatal(err)
	}
}

func (c *client) read() *Frame {
	c.t.Helper()
	c.conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	f, err := c.r.read()
	if err != nil {
		c.t.Fatal(err)
	}
	return f
}

func (c *client) expect(command string) *Frame {
	c.t.Helper()
	f := c.read()
	if f.Command != command {
		c.t.Fatalf("got %s %v %q, want %s", f.Command, f.Headers, f.Body, command)
	}
	return f
}

// failure expects an ERROR frame with message text, then the end of the connection.
func (c *client) failure(text string) {
	c.t.Helper()
	if f := c.expect("ERROR"); f.Value("message") != text {
		c.t.Fatalf("ERROR %q, want %q", f.Value("message"), text)
	}
	c.closed()
}

// closed expects the server to close the connection.
func (c *client) closed() {
	c.t.Helper()
	c.conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	f, err := c.r.read()
	var ne net.Error
	if err == nil || errors.As(err, &ne) && ne.Timeout() {
		c.t.Fatalf("connection still open: %v %v", f, err)
	}
}

// silent expects no frame for d.
func (c *client) silent(d time.Duration) {
	c.t.Helper()
	c.conn.SetReadDeadline(time.Now().Add(d))
	f, err := c.r.read()
	var ne net.Error
	if !errors.As(err, &ne) || !ne.Timeout() {
		c.t.Fatalf("unexpected %v %v", f, err)
	}
}

func await(t *testing.T, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatal("condition timed out")
		}
		time.Sleep(time.Millisecond)
	}
}

func queueStatus(r *channels.Runtime, name string) channels.QueueStatus {
	for _, q := range r.Snapshot().Queues {
		if q.Name == name {
			return q
		}
	}
	return channels.QueueStatus{}
}

func TestLoginRequired(t *testing.T) {
	srv, _ := start(t, served("orders"))
	for _, login := range [][]string{
		{"login", testLogin, "passcode", "wrong"},
		{"login", "other", "passcode", testPasscode},
		{"login", testLogin},
		{},
	} {
		c := dial(t, srv)
		c.send("CONNECT", "", append([]string{"accept-version", "1.2"}, login...)...)
		f := c.expect("ERROR")
		if f.Value("message") != "login failed" || strings.Contains(string(f.Body), "wrong") {
			t.Fatalf("%v %q", f.Headers, f.Body)
		}
		c.closed()
	}
	c := dial(t, srv)
	c.send("SEND", "x", "destination", "/queue/orders")
	c.failure("expected CONNECT")
}

func TestVersionNegotiation(t *testing.T) {
	srv, _ := start(t, served("orders"))
	for _, versions := range []string{"1.0,1.1", ""} {
		c := dial(t, srv)
		headers := []string{"login", testLogin, "passcode", testPasscode}
		if versions != "" {
			headers = append(headers, "accept-version", versions)
		}
		c.send("CONNECT", "", headers...)
		if f := c.expect("ERROR"); f.Value("version") != "1.2" {
			t.Fatal(f.Headers)
		}
		c.closed()
	}
	c := dial(t, srv)
	c.send("STOMP", "", "accept-version", "1.1,1.2", "login", testLogin, "passcode", testPasscode)
	f := c.expect("CONNECTED")
	if f.Value("version") != "1.2" || f.Value("server") != "dif" || f.Value("session") == "" || f.Value("heart-beat") != "10000,10000" {
		t.Fatal(f.Headers)
	}
	c.send("CONNECT", "", "accept-version", "1.2")
	c.failure("already connected")
}

func TestUnservedAndUnknownDestinationsLookAlike(t *testing.T) {
	config := served("orders")
	config.Queues["private"] = channels.QueueConfig{}
	config.Topics["hidden"] = channels.TopicConfig{}
	srv, r := start(t, config)
	for _, destination := range []string{"/queue/private", "/queue/missing", "/topic/hidden", "/topic/missing", "/other/orders", "/queue/", ""} {
		c := connect(t, srv)
		c.send("SEND", "x", "destination", destination)
		c.failure(unknownDestination)
		c = connect(t, srv)
		c.send("SUBSCRIBE", "", "id", "1", "destination", destination)
		c.failure(unknownDestination)
	}
	status := r.Snapshot()
	for _, q := range status.Queues {
		if q.Name == "missing" || q.Name == "" {
			t.Fatal("a client created a queue")
		}
	}
	for _, topic := range status.Topics {
		if topic.Name == "missing" {
			t.Fatal("a client created a topic")
		}
	}
}

func TestSendToQueueConsumedBySubscriber(t *testing.T) {
	srv, _ := start(t, served("orders"))
	consumer := connect(t, srv)
	consumer.send("SUBSCRIBE", "", "id", "s1", "destination", "/queue/orders", "receipt", "r1")
	if f := consumer.expect("RECEIPT"); f.Value("receipt-id") != "r1" {
		t.Fatal(f.Headers)
	}
	producer := connect(t, srv)
	producer.send("SEND", "hello", "destination", "/queue/orders", "content-type", "text/plain", "orderId", "7", "Correlation-Id", "c1",
		"metadata.secret", "x", "DIF-Trace-Id", "trace1", "orderId", "8", "receipt", "r2")
	if f := producer.expect("RECEIPT"); f.Value("receipt-id") != "r2" {
		t.Fatal(f.Headers)
	}
	f := consumer.expect("MESSAGE")
	want := map[string]string{"subscription": "s1", "destination": "/queue/orders", "content-type": "text/plain", "content-length": "5", "orderId": "7", "Correlation-Id": "c1", "DIF-Trace-Id": "trace1"}
	for name, value := range want {
		if f.Value(name) != value {
			t.Fatalf("%s: %q in %v", name, f.Value(name), f.Headers)
		}
	}
	if string(f.Body) != "hello" || f.Value("message-id") == "" || f.Value("message-id") != f.Value("Message-Id") {
		t.Fatal(f.Headers, string(f.Body))
	}
	for _, h := range f.Headers {
		if strings.HasPrefix(h.Name, "metadata.") || h.Name == "ack" || h.Name == "receipt" {
			t.Fatal("leaked header", h)
		}
	}
	producer.send("DISCONNECT", "", "receipt", "bye")
	producer.expect("RECEIPT")
	producer.closed()
}

func TestTransactionsAndUnknownCommandsFail(t *testing.T) {
	srv, _ := start(t, served("orders"))
	c := connect(t, srv)
	c.send("BEGIN", "", "transaction", "t1", "receipt", "r")
	if f := c.expect("ERROR"); f.Value("message") != "transactions are not supported" || f.Value("receipt-id") != "r" {
		t.Fatal(f.Headers)
	}
	c.closed()
	c = connect(t, srv)
	c.send("PUBLISH", "")
	c.failure("unknown command")
}

func receiveBodies(c *client, n int) []*Frame {
	var frames []*Frame
	for range n {
		frames = append(frames, c.expect("MESSAGE"))
	}
	return frames
}

func TestClientAcknowledgementModes(t *testing.T) {
	for _, tc := range []struct{ mode, ack, left string }{
		{"client", "b", "c"},             // cumulative: a and b
		{"client-individual", "b", "ac"}, // b only
	} {
		srv, _ := start(t, served("jobs"))
		producer := connect(t, srv)
		for _, body := range []string{"a", "b", "c"} {
			producer.send("SEND", body, "destination", "/queue/jobs")
		}
		producer.send("DISCONNECT", "", "receipt", "sent")
		producer.expect("RECEIPT")
		c := connect(t, srv)
		c.send("SUBSCRIBE", "", "id", "s", "destination", "/queue/jobs", "ack", tc.mode)
		frames := receiveBodies(c, 3)
		if frames[1].Value("ack") == "" || string(frames[1].Body) != tc.ack {
			t.Fatal(frames[1].Headers)
		}
		c.send("ACK", "", "id", frames[1].Value("ack"), "receipt", "acked")
		c.expect("RECEIPT")
		c.send("DISCONNECT", "", "receipt", "bye")
		c.expect("RECEIPT")
		// What the client did not acknowledge goes back to the memory queue, in order.
		await(t, func() bool { return srv.Status().Connections == 0 })
		c = connect(t, srv)
		c.send("SUBSCRIBE", "", "id", "s", "destination", "/queue/jobs")
		var left string
		for _, f := range receiveBodies(c, len(tc.left)) {
			left += string(f.Body)
		}
		if left != tc.left {
			t.Fatalf("%s: left %q, want %q", tc.mode, left, tc.left)
		}
		c.silent(50 * time.Millisecond)
	}
}

func TestUnknownAckFails(t *testing.T) {
	srv, _ := start(t, served("jobs"))
	c := connect(t, srv)
	c.send("ACK", "", "id", "42")
	c.failure("unknown ack id")
}

func durable(dir string, maxDeliveries int) channels.Config {
	return channels.Config{Directory: dir, Queues: map[string]channels.QueueConfig{
		"jobs":     {Durable: true, Served: true, MaxDeliveries: maxDeliveries, RetryDelayMS: 20},
		"jobs.DLQ": {Durable: true, Served: true},
	}}
}

func TestDurableNackRedeliversThenDeadLetters(t *testing.T) {
	srv, r := start(t, durable(t.TempDir(), 2))
	c := connect(t, srv)
	c.send("SUBSCRIBE", "", "id", "s", "destination", "/queue/jobs", "ack", "client-individual")
	c.send("SEND", "poison", "destination", "/queue/jobs", "receipt", "stored")
	c.expect("RECEIPT")
	first := c.expect("MESSAGE")
	c.send("NACK", "", "id", first.Value("ack"))
	again := c.expect("MESSAGE")
	if again.Value("message-id") != first.Value("message-id") || string(again.Body) != "poison" {
		t.Fatal(again.Headers)
	}
	if queueStatus(r, "jobs").Redeliveries != 1 {
		t.Fatal(queueStatus(r, "jobs"))
	}
	c.send("NACK", "", "id", again.Value("ack"), "receipt", "nacked")
	c.expect("RECEIPT")
	c.send("SUBSCRIBE", "", "id", "dlq", "destination", "/queue/jobs.DLQ")
	dead := c.expect("MESSAGE")
	if string(dead.Body) != "poison" || dead.Value("error.queue") != "jobs" || dead.Value("error.message") != errNacked.Error() {
		t.Fatal(dead.Headers)
	}
}

func TestDroppedConnectionCountsAnAttempt(t *testing.T) {
	srv, r := start(t, durable(t.TempDir(), 1))
	c := connect(t, srv)
	c.send("SUBSCRIBE", "", "id", "s", "destination", "/queue/jobs", "ack", "client")
	c.send("SEND", "crash", "destination", "/queue/jobs")
	c.expect("MESSAGE")
	c.conn.Close() // the client crashes on the message
	await(t, func() bool { return queueStatus(r, "jobs.DLQ").Waiting == 1 })
	c = connect(t, srv)
	c.send("SUBSCRIBE", "", "id", "dlq", "destination", "/queue/jobs.DLQ")
	if f := c.expect("MESSAGE"); string(f.Body) != "crash" || f.Value("error.message") != errUnacknowledged.Error() {
		t.Fatal(f.Headers)
	}
}

func TestPrefetchBoundsUnacknowledgedDeliveries(t *testing.T) {
	srv, _ := start(t, served("jobs"))
	c := connect(t, srv)
	for _, body := range []string{"1", "2", "3"} {
		c.send("SEND", body, "destination", "/queue/jobs")
	}
	c.send("SUBSCRIBE", "", "id", "s", "destination", "/queue/jobs", "ack", "client-individual", "prefetch-count", "2")
	frames := receiveBodies(c, 2)
	c.silent(100 * time.Millisecond)
	c.send("ACK", "", "id", frames[0].Value("ack"))
	if f := c.expect("MESSAGE"); string(f.Body) != "3" {
		t.Fatal(string(f.Body))
	}
	c.send("SUBSCRIBE", "", "id", "t", "destination", "/queue/jobs", "ack", "client", "prefetch-count", "0")
	c.failure("prefetch-count must be between 1 and 1000")
}

func TestTopicFansOutToSubscribers(t *testing.T) {
	srv, _ := start(t, served())
	var subscribers []*client
	for i := range 2 {
		c := connect(t, srv)
		c.send("SUBSCRIBE", "", "id", "news", "destination", "/topic/news", "ack", "client", "receipt", "on")
		c.expect("RECEIPT")
		subscribers = append(subscribers, c)
		_ = i
	}
	if got := srv.Status(); got.Connections != 2 || got.Subscriptions != 2 {
		t.Fatal(got)
	}
	producer := connect(t, srv)
	producer.send("SEND", "extra", "destination", "/topic/news", "receipt", "sent")
	producer.expect("RECEIPT")
	for _, c := range subscribers {
		f := c.expect("MESSAGE")
		if string(f.Body) != "extra" || f.Value("destination") != "/topic/news" {
			t.Fatal(f.Headers)
		}
		c.send("ACK", "", "id", f.Value("ack"), "receipt", "done")
		c.expect("RECEIPT")
		c.send("UNSUBSCRIBE", "", "id", "news", "receipt", "off")
		c.expect("RECEIPT")
	}
	await(t, func() bool { return srv.Status().Subscriptions == 0 })
}

func TestHeartBeats(t *testing.T) {
	srv, _ := start(t, served("jobs"), func(s *Server) { s.heartBeat = 40 * time.Millisecond })
	// The server beats when the client asks for it.
	c := connect(t, srv, "heart-beat", "0,40")
	c.conn.SetReadDeadline(time.Now().Add(time.Second))
	if b, err := c.r.r.ReadByte(); err != nil || b != '\n' {
		t.Fatal(b, err)
	}
	// A client that promises beats and keeps them stays connected.
	c = connect(t, srv, "heart-beat", "40,0")
	for range 8 {
		time.Sleep(20 * time.Millisecond)
		c.conn.Write([]byte("\n"))
	}
	c.send("SEND", "alive", "destination", "/queue/jobs", "receipt", "r")
	c.expect("RECEIPT")
	// A client that falls silent is closed.
	c.closed()
}

func TestMaxConnections(t *testing.T) {
	config := served("jobs")
	config.STOMP = &channels.STOMPConfig{Address: "127.0.0.1:0", Login: testLogin, MaxConnections: 1}
	srv, _ := start(t, config)
	first := connect(t, srv)
	over := dial(t, srv)
	over.failure("too many connections")
	first.conn.Close()
	await(t, func() bool { return srv.Status().Connections == 0 })
	connect(t, srv)
}

func TestFrameLimits(t *testing.T) {
	config := served("jobs")
	config.MaxMessageBytes = 1024
	srv, _ := start(t, config)
	c := connect(t, srv)
	c.send("SEND", strings.Repeat("x", 2000), "destination", "/queue/jobs", "content-length", "2000")
	c.failure("frame body too large")
	c = connect(t, srv)
	headers := []string{"destination", "/queue/jobs"}
	for range maxHeaders {
		headers = append(headers, "h", "v")
	}
	c.send("SEND", "", headers...)
	c.failure("too many headers")
}

// Before login the body bound is that of a line, whatever maxMessageBytes is:
// the server reserves no message-sized buffer for a stranger.
func TestBodyBoundBeforeLogin(t *testing.T) {
	srv, _ := start(t, served("jobs"))
	c := dial(t, srv)
	c.send("CONNECT", "", "accept-version", "1.2", "login", testLogin, "passcode", testPasscode, "content-length", "1000000")
	c.failure("frame body too large")
	c = connect(t, srv)
	c.send("SEND", strings.Repeat("x", 2*maxLine), "destination", "/queue/jobs", "receipt", "r")
	c.expect("RECEIPT")
}

func TestServerShutdownReturnsDeliveries(t *testing.T) {
	config := durable(t.TempDir(), 1)
	config.Queues["mem"] = channels.QueueConfig{Served: true}
	srv, r := start(t, config)
	replies := make(chan error, 2)
	var released atomic.Int64
	mem, _ := r.ServedQueue("mem")
	err := mem.Enqueue(context.Background(), channels.Entry{
		Message: message.New("waiting"),
		Reply:   func(_ message.Message, err error) { replies <- err },
		Release: func() { released.Add(1) },
	}, channels.Policy{})
	if err != nil {
		t.Fatal(err)
	}
	c := connect(t, srv)
	c.send("SEND", "job", "destination", "/queue/jobs")
	c.send("SUBSCRIBE", "", "id", "jobs", "destination", "/queue/jobs", "ack", "client")
	c.send("SUBSCRIBE", "", "id", "mem", "destination", "/queue/mem", "ack", "client-individual")
	receiveBodies(c, 2)
	srv.Close()
	c.closed()
	if q := queueStatus(r, "jobs"); q.Waiting != 1 || q.InFlight != 0 {
		t.Fatal(q)
	}
	if q := queueStatus(r, "jobs.DLQ"); q.Waiting != 0 {
		t.Fatal("shutdown counted an attempt", q)
	}
	if mem.Depth() != 1 || len(replies) != 0 || released.Load() != 0 {
		t.Fatal(mem.Depth(), len(replies), released.Load())
	}
}

func TestMemoryQueueRepliesOnceOnAcknowledgement(t *testing.T) {
	srv, r := start(t, served("jobs"))
	replies := make(chan message.Message, 2)
	var released atomic.Int64
	jobs, _ := r.ServedQueue("jobs")
	jobs.Enqueue(context.Background(), channels.Entry{
		Message: message.New("request"),
		Reply: func(m message.Message, err error) {
			if err == nil {
				replies <- m
			}
		},
		Release: func() { released.Add(1) },
	}, channels.Policy{})
	c := connect(t, srv)
	c.send("SUBSCRIBE", "", "id", "s", "destination", "/queue/jobs", "ack", "client-individual")
	f := c.expect("MESSAGE")
	c.send("NACK", "", "id", f.Value("ack"))
	f = c.expect("MESSAGE") // returned to the queue and delivered again
	if len(replies) != 0 || released.Load() != 0 {
		t.Fatal("NACK replied or released")
	}
	c.send("ACK", "", "id", f.Value("ack"), "receipt", "r")
	c.expect("RECEIPT")
	if len(replies) != 1 || released.Load() != 1 {
		t.Fatal(len(replies), released.Load())
	}
	if m := <-replies; m[message.Body] != "request" {
		t.Fatal(m)
	}
}

func TestSentMessagesAreTrackedWork(t *testing.T) {
	var active atomic.Int64
	var channel atomic.Value
	srv, _ := start(t, served("jobs"), func(s *Server) {
		s.Track = func(ch string) func() {
			channel.Store(ch)
			active.Add(1)
			return func() { active.Add(-1) }
		}
	})
	c := connect(t, srv)
	c.send("SEND", "work", "destination", "/queue/jobs", "receipt", "r")
	c.expect("RECEIPT")
	if active.Load() != 1 || channel.Load() != "queue:jobs" {
		t.Fatal(active.Load(), channel.Load())
	}
	c.send("SUBSCRIBE", "", "id", "s", "destination", "/queue/jobs")
	c.expect("MESSAGE")
	await(t, func() bool { return active.Load() == 0 })
}

func TestTLS(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IPAddresses: []net.IP{net.IPv4(127, 0, 0, 1)}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	srv, _ := start(t, served("jobs"), func(s *Server) {
		s.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}, MinVersion: tls.VersionTLS12}
	})
	conn, err := tls.Dial("tcp", srv.Addr().String(), &tls.Config{RootCAs: pool})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	c := wrap(t, conn)
	c.send("CONNECT", "", "accept-version", "1.2", "login", testLogin, "passcode", testPasscode)
	c.expect("CONNECTED")
	c.send("SEND", "secure", "destination", "/queue/jobs", "receipt", "r")
	c.expect("RECEIPT")
}
