// Package stomp serves the channels that the configuration marks served to
// STOMP 1.2 clients over TCP or TLS. Clients and flows share the same queues
// and topics. It depends on the channel runtime and messages only: not on the
// engine, the processor contracts or DIL.
package stomp

import (
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"dif/internal/channels"
)

// Limits of a frame besides its body, which is bounded by maxMessageBytes.
const (
	maxLine         = 8 << 10
	maxHeaders      = 100
	defaultPrefetch = 10
	maxPrefetch     = 1000
)

// Server accepts STOMP connections for one channel runtime. Set Passcode,
// and TLS when the configuration names an identity, before Start.
type Server struct {
	Config   channels.STOMPConfig // listener settings, with defaults
	Passcode string               // the passcode of Config.Login
	TLS      *tls.Config          // nil serves plain TCP
	// Track begins queued work for a message a client sends to channel,
	// "queue:<name>" or "topic:<name>", and returns its end; nil tracks none.
	Track  func(channel string) func()
	Logger *slog.Logger

	channels                                *channels.Runtime
	maxBody                                 int
	connectTimeout, heartBeat, writeTimeout time.Duration

	mu            sync.Mutex
	ln            net.Listener
	sessions      map[*session]bool
	closed        bool
	err           error
	done          chan struct{}
	doneOnce      sync.Once
	wg            sync.WaitGroup
	subscriptions atomic.Int64
}

// Status counts the connections and their subscriptions.
type Status struct {
	Connections   int   `json:"connections"`
	Subscriptions int64 `json:"subscriptions"`
}

// New returns a server for the served channels of r, or nil when r has no
// stomp configuration.
func New(r *channels.Runtime) *Server {
	c := r.STOMP()
	if c == nil {
		return nil
	}
	return &Server{
		Config:         *c,
		channels:       r,
		maxBody:        r.MaxMessageBytes(),
		connectTimeout: 10 * time.Second,
		heartBeat:      10 * time.Second,
		writeTimeout:   10 * time.Second,
		sessions:       map[*session]bool{},
		done:           make(chan struct{}),
	}
}

// Start binds the address and accepts connections until Close.
func (s *Server) Start() error {
	if s.Passcode == "" {
		return errors.New("no STOMP passcode set")
	}
	if s.Logger == nil {
		s.Logger = slog.New(slog.DiscardHandler)
	}
	ln, err := net.Listen("tcp", s.Config.Address)
	if err != nil {
		return err
	}
	if s.TLS != nil {
		ln = tls.NewListener(ln, s.TLS)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		ln.Close()
		return errors.New("STOMP server closed")
	}
	s.ln = ln
	s.wg.Add(1)
	go s.accept(ln)
	return nil
}

// Addr is the bound address, or nil before Start.
func (s *Server) Addr() net.Addr {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ln == nil {
		return nil
	}
	return s.ln.Addr()
}

// Done is closed when the server fails, or after Close.
func (s *Server) Done() <-chan struct{} { return s.done }

// Err returns why the server failed: the listener stopped accepting, or a
// delivery could not be settled.
func (s *Server) Err() error { s.mu.Lock(); defer s.mu.Unlock(); return s.err }

func (s *Server) fail(err error) {
	s.mu.Lock()
	if s.err == nil {
		s.err = err
	}
	s.mu.Unlock()
	s.doneOnce.Do(func() { close(s.done) })
}

// Status returns the current number of connections and subscriptions.
func (s *Server) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Status{Connections: len(s.sessions), Subscriptions: s.subscriptions.Load()}
}

// Close stops accepting, closes every connection and waits for them. The
// unacknowledged deliveries of a connection go back to their queue unchanged:
// a shutdown is not the client's fault.
func (s *Server) Close() error {
	s.mu.Lock()
	first := !s.closed
	s.closed = true
	if first && s.ln != nil {
		s.ln.Close()
	}
	sessions := make([]*session, 0, len(s.sessions))
	for sess := range s.sessions {
		sessions = append(sessions, sess)
	}
	s.mu.Unlock()
	for _, sess := range sessions {
		sess.stop(stopServer)
	}
	s.wg.Wait()
	s.doneOnce.Do(func() { close(s.done) })
	return nil
}

func (s *Server) accept(ln net.Listener) {
	defer s.wg.Done()
	var delay time.Duration
	for {
		conn, err := ln.Accept()
		if err != nil {
			s.mu.Lock()
			closed := s.closed
			s.mu.Unlock()
			if closed {
				return
			}
			if t, ok := err.(interface{ Temporary() bool }); ok && t.Temporary() {
				delay = min(max(2*delay, 5*time.Millisecond), time.Second)
				time.Sleep(delay)
				continue
			}
			s.Logger.Error("STOMP listener failed")
			s.fail(fmt.Errorf("STOMP listener: %w", err))
			return
		}
		delay = 0
		s.mu.Lock()
		if s.closed {
			s.mu.Unlock()
			conn.Close()
			return
		}
		s.wg.Add(1)
		if len(s.sessions) >= s.Config.MaxConnections {
			s.mu.Unlock()
			s.Logger.Warn("STOMP connection refused: too many connections", "remote", conn.RemoteAddr().String())
			go func() { defer s.wg.Done(); refuse(conn, s.writeTimeout) }()
			continue
		}
		sess := newSession(s, conn)
		s.sessions[sess] = true
		s.mu.Unlock()
		go func() {
			defer s.wg.Done()
			sess.run()
			s.mu.Lock()
			delete(s.sessions, sess)
			s.mu.Unlock()
		}()
	}
}
