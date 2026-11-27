package channels

import (
	"errors"
	"fmt"
	"net"
	"strconv"
)

// configureSTOMP checks the listener against the served channels: one needs
// the other. Without TLS the listener must stay on a loopback address.
func (r *Runtime) configureSTOMP() error {
	served := false
	for _, q := range r.queues {
		served = served || q.config.Served
	}
	for _, t := range r.topics {
		served = served || t.served
	}
	c := r.config.STOMP
	if c == nil {
		if served {
			return errors.New("served channels require a stomp listener (channels.stomp)")
		}
		return nil
	}
	if !served {
		return errors.New("stomp: no queue or topic is served; set served on at least one")
	}
	cfg := *c // callers may reuse or modify their Config after Open
	host, port, err := net.SplitHostPort(cfg.Address)
	if n, perr := strconv.Atoi(port); err != nil || perr != nil || n < 0 || n > 65535 {
		return errors.New("stomp: address must be host:port")
	}
	if cfg.Login == "" {
		return errors.New("stomp: login is required")
	}
	if cfg.MaxConnections < 0 || cfg.MaxConnections > 100000 {
		return fmt.Errorf("stomp: maxConnections must be between 1 and 100000")
	}
	if cfg.MaxConnections == 0 {
		cfg.MaxConnections = DefaultMaxConnections
	}
	if cfg.TLS == nil {
		if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
			return errors.New("stomp: an address that is not loopback requires tls")
		}
	} else {
		if cfg.TLS.Identity == "" {
			return errors.New("stomp: tls.identity is required")
		}
		tlsConfig := *cfg.TLS
		cfg.TLS = &tlsConfig
	}
	r.config.STOMP = &cfg
	return nil
}

// STOMP returns the listener configuration with its defaults, or nil when no
// channel is served.
func (r *Runtime) STOMP() *STOMPConfig {
	if r.config.STOMP == nil {
		return nil
	}
	c := *r.config.STOMP
	return &c
}

// MaxMessageBytes is the largest encoded message the runtime stores.
func (r *Runtime) MaxMessageBytes() int { return r.config.MaxMessageBytes }

// ServedQueue returns a queue that the configuration declares served. Unlike
// Queue it never creates one, so a name from outside cannot add a channel.
func (r *Runtime) ServedQueue(name string) (*Queue, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if q := r.queues[name]; q != nil && q.config.Served {
		return q, true
	}
	return nil, false
}

// ServedTopic returns a topic that the configuration declares served, without
// creating one.
func (r *Runtime) ServedTopic(name string) (*Topic, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if t := r.topics[name]; t != nil && t.served {
		return t, true
	}
	return nil, false
}
