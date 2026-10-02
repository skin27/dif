package impl

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// maxBodySize limits the HTTP bodies DIF reads, in and out.
const maxBodySize = 32 << 20

// httpsServers holds the running HTTPS listeners by address. Flows with an
// https source on the same host:port share one listener, each on its own path.
var httpsServers = struct {
	sync.Mutex
	m map[string]*httpsServer
}{m: map[string]*httpsServer{}}

type httpsServer struct {
	identity string // the keystore file of the server's certificate
	srv      *http.Server
	ln       net.Listener

	mu     sync.RWMutex
	routes map[string]httpsRoute // by path

	connsMu sync.Mutex
	fresh   map[net.Conn]bool // connections that have not sent a request yet
}

type httpsRoute struct {
	prefix  bool // also serve paths that start with the route's path
	handler http.Handler
}

// serveHTTPS serves handler on path at addr, starting the server for addr if
// it is not running yet; a bind error is returned. All routes at one address
// share its certificate, so they must use the same identity file.
// unregister removes the route and stops the server once it has none left.
func serveHTTPS(addr, identity string, cert tls.Certificate, path string, prefix bool, handler http.Handler) (unregister func(), err error) {
	httpsServers.Lock()
	defer httpsServers.Unlock()

	s := httpsServers.m[addr]
	if s == nil {
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			return nil, err
		}
		s = &httpsServer{identity: identity, ln: ln, routes: map[string]httpsRoute{}, fresh: map[net.Conn]bool{}}
		s.srv = &http.Server{
			Handler:           s,
			ReadHeaderTimeout: 10 * time.Second,
			TLSConfig:         &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12},
			ErrorLog:          log.New(io.Discard, "", 0), // e.g. TLS handshake errors of clients
			ConnState:         s.track,
		}
		go s.srv.ServeTLS(ln, "", "")
		httpsServers.m[addr] = s
	} else if s.identity != identity {
		return nil, fmt.Errorf("%s already serves with server identity %s, not %s", addr, s.identity, identity)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, used := s.routes[path]; used {
		return nil, fmt.Errorf("path %s on %s is already served by another flow", path, addr)
	}
	s.routes[path] = httpsRoute{prefix, handler}

	return func() {
		httpsServers.Lock()
		s.mu.Lock()
		delete(s.routes, path)
		empty := len(s.routes) == 0
		s.mu.Unlock()
		if empty {
			// Free the address at once, so a flow can bind it again, then let
			// requests in progress finish without holding up other flows.
			delete(httpsServers.m, addr)
			s.ln.Close()
		}
		httpsServers.Unlock()

		if empty {
			// Shutdown waits for requests in progress, but also up to 5 seconds
			// for connections without a request, such as the spare ones Go
			// clients open; those are closed first.
			s.closeFresh()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if s.srv.Shutdown(ctx) != nil {
				s.srv.Close()
			}
		}
	}, nil
}

// ServeHTTP routes a request by its exact path, else by the longest prefix route.
func (s *httpsServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	route, ok := s.routes[r.URL.Path]
	if !ok {
		best := -1
		for p, rt := range s.routes {
			if rt.prefix && strings.HasPrefix(r.URL.Path, p) && len(p) > best {
				route, ok, best = rt, true, len(p)
			}
		}
	}
	s.mu.RUnlock()

	if !ok {
		http.NotFound(w, r)
		return
	}
	route.handler.ServeHTTP(w, r)
}

// track records which connections have not sent a request yet.
func (s *httpsServer) track(c net.Conn, state http.ConnState) {
	s.connsMu.Lock()
	defer s.connsMu.Unlock()
	if state == http.StateNew {
		s.fresh[c] = true
	} else {
		delete(s.fresh, c)
	}
}

// closeFresh closes the connections that have not sent a request yet.
func (s *httpsServer) closeFresh() {
	s.connsMu.Lock()
	conns := make([]net.Conn, 0, len(s.fresh))
	for c := range s.fresh {
		conns = append(conns, c)
	}
	s.connsMu.Unlock()
	for _, c := range conns {
		c.Close()
	}
}
