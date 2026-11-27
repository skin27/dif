package stomp

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"dif/internal/channels"
	"dif/message"
)

type stopReason int

const (
	// stopClient: the client disconnected, lost the connection or broke the
	// protocol. Its unacknowledged deliveries count as failed.
	stopClient stopReason = iota
	// stopServer: the server shuts down. Deliveries go back unchanged.
	stopServer
)

type outcome int

const (
	acked    outcome = iota // processed: terminal
	rejected                // failed: a durable queue counts an attempt
	returned                // not the client's fault: back unchanged
)

var (
	errStopped        = errors.New("connection closed")
	errNacked         = errors.New("rejected by STOMP client (NACK)")
	errUnacknowledged = errors.New("STOMP client went away without acknowledging")
)

// unknownDestination answers a destination that is not served, whether the
// channel exists or not, and never echoes the name.
const unknownDestination = "unknown destination"

type subscription struct {
	id, destination, mode string
	queue                 *channels.Queue // a queue, or the topic subscription's buffer
	durable               bool
	topic                 *channels.Topic
	topicSub              *channels.Subscription
	releaseConsumer       func()
	slots                 chan struct{} // prefetch bound in the client modes
	ctx                   context.Context
	cancel                context.CancelFunc
	done                  chan struct{}
	pending               []*delivery // taken and not settled, in delivery order
}

type delivery struct {
	sub   *subscription
	ackID string
	entry channels.Entry
}

type session struct {
	srv    *Server
	conn   net.Conn
	in     *idleReader
	r      *reader
	remote string
	id     string

	ctx       context.Context
	cancel    context.CancelFunc
	stopOnce  sync.Once
	reason    stopReason
	connected bool

	wmu       sync.Mutex
	w         *bufio.Writer
	werr      error
	lastWrite time.Time

	mu      sync.Mutex
	subs    map[string]*subscription
	acks    map[string]*delivery
	nextAck uint64

	wg sync.WaitGroup
}

// idleReader renews the read deadline on every read once the client has
// promised heart-beats: a client silent for timeout is gone.
type idleReader struct {
	conn    net.Conn
	timeout time.Duration
}

func (r *idleReader) Read(p []byte) (int, error) {
	if r.timeout > 0 {
		r.conn.SetReadDeadline(time.Now().Add(r.timeout))
	}
	return r.conn.Read(p)
}

func newSession(srv *Server, conn net.Conn) *session {
	in := &idleReader{conn: conn}
	ctx, cancel := context.WithCancel(context.Background())
	var id [8]byte
	rand.Read(id[:])
	return &session{
		srv:    srv,
		conn:   conn,
		in:     in,
		r:      newReader(in, maxLine, maxHeaders, min(maxLine, srv.maxBody)), // before login: CONNECT has no body
		remote: conn.RemoteAddr().String(),
		id:     hex.EncodeToString(id[:]),
		ctx:    ctx,
		cancel: cancel,
		w:      bufio.NewWriter(conn),
		subs:   map[string]*subscription{},
		acks:   map[string]*delivery{},
	}
}

// stop ends the session once: it cancels the subscriptions and closes the
// connection, which ends the read loop. The first reason counts.
func (s *session) stop(reason stopReason) {
	s.stopOnce.Do(func() {
		s.reason = reason
		s.cancel()
		s.conn.Close()
	})
}

func (s *session) run() {
	defer s.cleanup()
	if !s.connect() {
		return
	}
	for {
		f, err := s.r.read()
		if err != nil {
			s.readFailed(err)
			return
		}
		if !s.handle(f) {
			return
		}
	}
}

// cleanup runs after the read loop: it waits for the subscriptions and
// settles what the client did not acknowledge.
func (s *session) cleanup() {
	s.stop(stopClient)
	s.wg.Wait()
	s.mu.Lock()
	subs := s.subs
	s.subs = nil
	s.mu.Unlock()
	for _, sub := range subs {
		s.end(sub, s.reason)
	}
	if s.connected {
		s.srv.Logger.Info("STOMP connection closed", "remote", s.remote, "session", s.id)
	}
}

func (s *session) readFailed(err error) {
	var fe frameError
	if errors.As(err, &fe) {
		s.abort(fe.Error(), nil)
		return
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() && s.connected {
		s.srv.Logger.Warn("STOMP client missed its heart-beats", "remote", s.remote, "session", s.id)
	}
	s.stop(stopClient)
}

// write sends one frame under the write deadline.
func (s *session) write(f *Frame) error {
	s.wmu.Lock()
	defer s.wmu.Unlock()
	if s.werr != nil {
		return s.werr
	}
	s.conn.SetWriteDeadline(time.Now().Add(s.srv.writeTimeout))
	writeFrame(s.w, f)
	if err := s.w.Flush(); err != nil {
		s.werr = err
		return err
	}
	s.lastWrite = time.Now()
	if f.Command == "ERROR" {
		s.werr = errStopped // nothing follows an ERROR frame
	}
	return nil
}

// abort sends an ERROR frame and closes the connection, as the specification
// requires. It lets the client read the frame before the socket closes.
func (s *session) abort(text string, cause *Frame, headers ...Header) {
	s.cancel()
	if s.write(errorFrame(text, cause, headers...)) == nil {
		if cw, ok := s.conn.(interface{ CloseWrite() error }); ok {
			cw.CloseWrite()
		}
		s.conn.SetReadDeadline(time.Now().Add(time.Second))
		io.Copy(io.Discard, io.LimitReader(s.conn, 1<<20))
	}
	s.stop(stopClient)
}

func errorFrame(text string, cause *Frame, headers ...Header) *Frame {
	f := &Frame{Command: "ERROR", Headers: append([]Header{{"message", text}}, headers...), Body: []byte(text)}
	if cause != nil {
		if receipt, ok := cause.Get("receipt"); ok {
			f.add("receipt-id", receipt)
		}
	}
	f.add("content-type", "text/plain")
	f.add("content-length", strconv.Itoa(len(f.Body)))
	return f
}

// refuse answers a connection over the limit with an ERROR frame.
func refuse(conn net.Conn, timeout time.Duration) {
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(min(timeout, time.Second)))
	w := bufio.NewWriter(conn)
	writeFrame(w, errorFrame("too many connections", nil))
	if w.Flush() == nil {
		if cw, ok := conn.(interface{ CloseWrite() error }); ok {
			cw.CloseWrite()
		}
		io.Copy(io.Discard, io.LimitReader(conn, 64<<10))
	}
}

func (s *session) connect() bool {
	s.conn.SetReadDeadline(time.Now().Add(s.srv.connectTimeout))
	f, err := s.r.read()
	if err != nil {
		s.readFailed(err)
		return false
	}
	if f.Command != "CONNECT" && f.Command != "STOMP" {
		s.abort("expected CONNECT", nil)
		return false
	}
	if !slices.Contains(strings.Split(f.Value("accept-version"), ","), "1.2") {
		s.abort("supported protocol version is 1.2", nil, Header{"version", "1.2"})
		return false
	}
	login := subtle.ConstantTimeCompare([]byte(f.Value("login")), []byte(s.srv.Config.Login))
	passcode := subtle.ConstantTimeCompare([]byte(f.Value("passcode")), []byte(s.srv.Passcode))
	if login&passcode != 1 {
		s.srv.Logger.Warn("STOMP login failed", "remote", s.remote)
		s.abort("login failed", nil)
		return false
	}
	cx, cy, ok := heartBeat(f.Value("heart-beat"))
	if !ok {
		s.abort("invalid heart-beat header", nil)
		return false
	}
	server := int(s.srv.heartBeat / time.Millisecond)
	out, in := 0, 0 // milliseconds; 0 is none
	if server > 0 && cy > 0 {
		out = max(server, cy)
	}
	if server > 0 && cx > 0 {
		in = max(server, cx)
	}
	connected := &Frame{Command: "CONNECTED"}
	connected.add("version", "1.2")
	connected.add("server", "dif")
	connected.add("session", s.id)
	connected.add("heart-beat", fmt.Sprintf("%d,%d", server, server))
	if s.write(connected) != nil {
		s.stop(stopClient)
		return false
	}
	s.connected = true
	s.r.maxBody = s.srv.maxBody
	s.srv.Logger.Info("STOMP connection opened", "remote", s.remote, "session", s.id)
	s.conn.SetReadDeadline(time.Time{})
	if in > 0 {
		s.in.timeout = 2 * time.Duration(in) * time.Millisecond
	}
	if out > 0 {
		s.wg.Add(1)
		go s.beat(time.Duration(out) * time.Millisecond)
	}
	return true
}

// heartBeat parses "cx,cy"; an absent header is "0,0".
func heartBeat(v string) (cx, cy int, ok bool) {
	if v == "" {
		return 0, 0, true
	}
	a, b, found := strings.Cut(v, ",")
	x, errX := strconv.ParseUint(a, 10, 31)
	y, errY := strconv.ParseUint(b, 10, 31)
	return int(x), int(y), found && errX == nil && errY == nil
}

// beat sends an EOL whenever the connection was idle for half the interval,
// so the client hears from the server at least once per interval.
func (s *session) beat(interval time.Duration) {
	defer s.wg.Done()
	t := time.NewTicker(interval / 2)
	defer t.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-t.C:
		}
		s.wmu.Lock()
		err := s.werr
		if err == nil && time.Since(s.lastWrite) >= interval/2 {
			s.conn.SetWriteDeadline(time.Now().Add(s.srv.writeTimeout))
			s.w.WriteByte('\n')
			if err = s.w.Flush(); err != nil {
				s.werr = err
			}
			s.lastWrite = time.Now()
		}
		s.wmu.Unlock()
		if err != nil {
			if err != errStopped {
				s.stop(stopClient)
			}
			return
		}
	}
}

// handle processes one frame after CONNECT; false ends the session.
func (s *session) handle(f *Frame) bool {
	var text string
	switch f.Command {
	case "SEND":
		text = s.send(f)
	case "SUBSCRIBE":
		text = s.subscribe(f)
	case "UNSUBSCRIBE":
		s.mu.Lock()
		sub := s.subs[f.Value("id")]
		delete(s.subs, f.Value("id"))
		s.mu.Unlock()
		if sub == nil {
			text = "unknown subscription"
		} else {
			s.end(sub, stopClient)
		}
	case "ACK", "NACK":
		text = s.acknowledge(f.Value("id"), f.Command == "ACK")
	case "DISCONNECT":
		s.receipt(f)
		s.stop(stopClient)
		return false
	case "BEGIN", "COMMIT", "ABORT":
		text = "transactions are not supported"
	case "CONNECT", "STOMP":
		text = "already connected"
	default:
		text = "unknown command"
	}
	if text != "" {
		s.abort(text, f)
		return false
	}
	if s.receipt(f) != nil {
		s.stop(stopClient)
		return false
	}
	return true
}

func (s *session) receipt(f *Frame) error {
	id, ok := f.Get("receipt")
	if !ok {
		return nil
	}
	r := &Frame{Command: "RECEIPT"}
	r.add("receipt-id", id)
	return s.write(r)
}

// resolve returns the served queue or topic of a destination.
func (s *session) resolve(destination string) (*channels.Queue, *channels.Topic, string) {
	if name, ok := strings.CutPrefix(destination, "/queue/"); ok {
		if q, ok := s.srv.channels.ServedQueue(name); ok {
			return q, nil, "queue:" + name
		}
	} else if name, ok := strings.CutPrefix(destination, "/topic/"); ok {
		if t, ok := s.srv.channels.ServedTopic(name); ok {
			return nil, t, "topic:" + name
		}
	}
	return nil, nil, ""
}

// protocol are the SEND headers that are not message headers.
var protocol = map[string]bool{"destination": true, "content-length": true, "content-type": true, "receipt": true, "transaction": true}

// inbound builds a message as the https source does: the body, content-type
// as Content-Type, the other headers as user headers (never metadata), and
// identity headers kept when given and created otherwise.
func inbound(f *Frame) message.Message {
	m := message.Message{message.Body: string(f.Body)}
	for _, h := range f.Headers {
		key := h.Name
		switch {
		case key == "content-type":
			key = message.ContentType
		case key == traceIDHeader:
			key = message.TraceID
		case protocol[key] || key == "" || key == message.Body || message.IsMetadata(key):
			continue
		}
		if _, set := m[key]; !set {
			m[key] = h.Value
		}
	}
	m[message.Timestamp] = time.Now().Format(time.RFC3339Nano)
	m.EnsureIdentity()
	m[message.WireHeaders] = true
	return m
}

// send admits a message to a queue or topic; the receipt follows admission,
// which for a durable queue means journaled and synced.
func (s *session) send(f *Frame) string {
	q, t, channel := s.resolve(f.Value("destination"))
	if channel == "" {
		return unknownDestination
	}
	m := inbound(f)
	var track func() func()
	if s.srv.Track != nil {
		track = func() func() { return s.srv.Track(channel) }
	}
	var err error
	if q != nil {
		var release func()
		if track != nil {
			release = track()
		}
		if err = q.Enqueue(s.ctx, channels.Entry{Message: m, Release: release}, channels.Policy{}); err != nil && release != nil {
			release()
		}
	} else {
		err = t.Publish(s.ctx, m, channels.Policy{}, track)
	}
	if err != nil {
		if s.srv.channels.Err() != nil {
			return "send failed: channel storage unavailable"
		}
		return "send failed: " + err.Error()
	}
	return ""
}

func (s *session) subscribe(f *Frame) string {
	id := f.Value("id")
	if id == "" {
		return "SUBSCRIBE requires an id"
	}
	mode := f.Value("ack")
	if mode == "" {
		mode = "auto"
	}
	if mode != "auto" && mode != "client" && mode != "client-individual" {
		return "ack must be auto, client or client-individual"
	}
	prefetch := defaultPrefetch
	if v, ok := f.Get("prefetch-count"); ok {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > maxPrefetch {
			return "prefetch-count must be between 1 and 1000"
		}
		prefetch = n
	}
	s.mu.Lock()
	_, used := s.subs[id]
	s.mu.Unlock()
	if used {
		return "subscription id already in use"
	}
	q, t, channel := s.resolve(f.Value("destination"))
	if channel == "" {
		return unknownDestination
	}
	sub := &subscription{id: id, destination: f.Value("destination"), mode: mode, done: make(chan struct{})}
	sub.ctx, sub.cancel = context.WithCancel(s.ctx)
	if q != nil {
		release, err := q.AcquireConsumer(false)
		if err != nil {
			sub.cancel()
			return err.Error()
		}
		sub.queue, sub.durable, sub.releaseConsumer = q, q.Durable(), release
	} else {
		sub.topic, sub.topicSub = t, t.Subscribe(sub.ctx)
		sub.queue = sub.topicSub.Queue
	}
	if mode != "auto" {
		sub.slots = make(chan struct{}, prefetch)
	}
	s.mu.Lock()
	s.subs[id] = sub
	s.mu.Unlock()
	s.srv.subscriptions.Add(1)
	s.wg.Add(1)
	go s.deliver(sub)
	return ""
}

// deliver takes from the channel and writes MESSAGE frames. A delivery is
// pending from the moment it is taken, so the session settles it even when
// the write fails.
func (s *session) deliver(sub *subscription) {
	defer s.wg.Done()
	defer close(sub.done)
	for {
		if sub.slots != nil {
			select {
			case sub.slots <- struct{}{}:
			case <-sub.ctx.Done():
				return
			}
		}
		e, err := sub.queue.Take(sub.ctx)
		if err != nil {
			if sub.slots != nil {
				<-sub.slots
			}
			if sub.ctx.Err() == nil {
				s.srv.Logger.Error("STOMP subscription stopped: channel unavailable", "remote", s.remote, "session", s.id)
				s.write(errorFrame("destination unavailable", nil))
				s.stop(stopServer)
			}
			return
		}
		s.mu.Lock()
		s.nextAck++
		d := &delivery{sub: sub, ackID: strconv.FormatUint(s.nextAck, 10), entry: e}
		sub.pending = append(sub.pending, d)
		if sub.mode != "auto" {
			s.acks[d.ackID] = d
		}
		s.mu.Unlock()
		if err := s.write(s.messageFrame(sub, d)); err != nil {
			if err != errStopped {
				s.stop(stopClient)
			}
			return
		}
		if sub.mode == "auto" {
			s.mu.Lock()
			sub.pending = slices.DeleteFunc(sub.pending, func(p *delivery) bool { return p == d })
			s.mu.Unlock()
			s.settle(d, acked, nil)
		}
	}
}

// acknowledge settles the delivery ackID, and in client mode every earlier
// delivery of its subscription as well (cumulative, for ACK and NACK alike).
func (s *session) acknowledge(ackID string, ack bool) string {
	s.mu.Lock()
	d := s.acks[ackID]
	if d == nil {
		s.mu.Unlock()
		return "unknown ack id"
	}
	sub := d.sub
	i := slices.Index(sub.pending, d)
	var batch []*delivery
	if sub.mode == "client" {
		batch = slices.Clone(sub.pending[:i+1])
		sub.pending = slices.Delete(sub.pending, 0, i+1)
	} else {
		batch = []*delivery{d}
		sub.pending = slices.Delete(sub.pending, i, i+1)
	}
	for _, b := range batch {
		delete(s.acks, b.ackID)
	}
	s.mu.Unlock()
	failed := false
	if ack {
		for _, b := range batch {
			failed = s.settle(b, acked, nil) != nil || failed
		}
	} else {
		// Returned memory deliveries go to the head: return the last one first.
		for i := len(batch) - 1; i >= 0; i-- {
			failed = s.settle(batch[i], rejected, errNacked) != nil || failed
		}
	}
	if failed {
		return "acknowledgement failed"
	}
	return ""
}

// end stops a subscription and settles its pending deliveries: rejected when
// the client ended it, returned when the server shuts down.
func (s *session) end(sub *subscription, reason stopReason) {
	sub.cancel()
	<-sub.done
	s.mu.Lock()
	pending := sub.pending
	sub.pending = nil
	for _, d := range pending {
		delete(s.acks, d.ackID)
	}
	s.mu.Unlock()
	o := rejected
	if reason == stopServer {
		o = returned
	}
	for i := len(pending) - 1; i >= 0; i-- {
		s.settle(pending[i], o, errUnacknowledged)
	}
	if sub.topic != nil {
		sub.topic.Unsubscribe(sub.topicSub)
	} else {
		sub.releaseConsumer()
	}
	s.srv.subscriptions.Add(-1)
}

// settle ends a delivery. Durable queues: acked completes it, rejected counts
// a failed attempt (retry delay, maxDeliveries, dead-letter queue), returned
// undoes the reservation. Memory queues: acked replies to a waiting sender
// with the message and ends its work; otherwise the entry goes back to the
// head of the queue with its reply and work intact, and only when that fails
// does the sender get an error and the work end. Topics: the work ends.
func (s *session) settle(d *delivery, o outcome, cause error) error {
	sub, e := d.sub, d.entry
	var err error
	switch {
	case sub.topic != nil:
		if e.Release != nil {
			e.Release()
		}
	case sub.durable:
		switch o {
		case acked:
			err = sub.queue.Complete(e.ID, nil)
		case rejected:
			err = sub.queue.Complete(e.ID, cause)
		default:
			err = sub.queue.Return(e)
		}
	case o == acked:
		if e.Reply != nil {
			e.Reply(e.Message, nil)
		}
		if e.Release != nil {
			e.Release()
		}
	default:
		if rerr := sub.queue.Return(e); rerr != nil {
			if e.Reply != nil {
				e.Reply(nil, fmt.Errorf("STOMP delivery not acknowledged: %w", rerr))
			}
			if e.Release != nil {
				e.Release()
			}
		}
	}
	if sub.slots != nil {
		<-sub.slots
	}
	if err != nil {
		s.srv.Logger.Error("STOMP delivery not settled", "remote", s.remote, "session", s.id, "error", err.Error())
		s.srv.fail(fmt.Errorf("settle STOMP delivery: %w", err))
	}
	return err
}

// traceIDHeader is the trace id on the wire, as for HTTP.
const traceIDHeader = "DIF-Trace-Id"

// reserved are MESSAGE headers that message headers never replace.
var reserved = map[string]bool{"subscription": true, "message-id": true, "destination": true, "ack": true, "content-type": true, "content-length": true, "receipt": true, "receipt-id": true, "transaction": true, traceIDHeader: true}

// messageFrame renders a delivery: scalar user headers become headers, the
// trace id becomes DIF-Trace-Id, other metadata stays inside.
func (s *session) messageFrame(sub *subscription, d *delivery) *Frame {
	m := d.entry.Message
	contentType, _ := m[message.ContentType].(string)
	var body []byte
	switch b := m[message.Body].(type) {
	case nil:
	case string:
		body = []byte(b)
	case []byte:
		body = b
	default:
		data, err := json.Marshal(b)
		if err != nil {
			data = []byte(fmt.Sprint(b))
		} else if contentType == "" {
			contentType = "application/json"
		}
		body = data
	}
	id, _ := m[message.MessageID].(string)
	if id == "" {
		id = d.ackID
	}
	f := &Frame{Command: "MESSAGE", Body: body}
	f.add("subscription", sub.id)
	f.add("message-id", id)
	f.add("destination", sub.destination)
	if sub.mode != "auto" {
		f.add("ack", d.ackID)
	}
	if contentType != "" {
		f.add("content-type", contentType)
	}
	f.add("content-length", strconv.Itoa(len(body)))
	if trace, _ := m[message.TraceID].(string); trace != "" {
		f.add(traceIDHeader, trace)
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		if k != "" && k != message.Body && k != message.ContentType && !message.IsMetadata(k) && !reserved[k] {
			keys = append(keys, k)
		}
	}
	slices.Sort(keys)
	for _, k := range keys {
		if v, ok := scalar(m[k]); ok {
			f.add(k, v)
		}
	}
	return f
}

// scalar renders strings, booleans and integers; other values stay inside.
func scalar(v any) (string, bool) {
	switch x := v.(type) {
	case string:
		return x, true
	case json.Number:
		return string(x), true
	case bool:
		return strconv.FormatBool(x), true
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return fmt.Sprint(x), true
	}
	return "", false
}
