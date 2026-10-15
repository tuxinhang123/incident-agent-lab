// Package evals implements the Day 5 offline evaluation suite: it prepares
// a demo-api + redis target, injects a case-specific fault, runs the real
// bin/agent diagnose CLI against it, asserts the deterministic rule
// diagnosis and emits a JSON report.
package evals

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"time"
)

// FaultTarget is a synthetic HTTP target whose lifetime the harness owns.
type FaultTarget interface {
	URL() string
	Close() error
}

// startBlackhole starts a TCP server that accepts connections and never
// answers, so the agent's HTTP probes block until their per-request timeout
// and observe error_type=timeout (app_endpoint_timeout case).
func startBlackhole() (FaultTarget, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("blackhole listen: %w", err)
	}
	bh := &blackhole{ln: ln}
	go bh.serve()
	return bh, nil
}

type blackhole struct {
	ln    net.Listener
	mu    sync.Mutex
	conns []net.Conn
}

func (b *blackhole) serve() {
	for {
		c, err := b.ln.Accept()
		if err != nil {
			return
		}
		b.mu.Lock()
		b.conns = append(b.conns, c)
		b.mu.Unlock()
		go func(c net.Conn) {
			// Drain the request but never write a byte; unblock on Close.
			_, _ = io.Copy(io.Discard, c)
		}(c)
	}
}

func (b *blackhole) URL() string { return "http://" + b.ln.Addr().String() }

func (b *blackhole) Close() error {
	b.mu.Lock()
	for _, c := range b.conns {
		_ = c.Close()
	}
	b.mu.Unlock()
	return b.ln.Close()
}

// startStatusStub starts an HTTP server that answers every request with a
// fixed status code. A 404-on-every-endpoint target matches no diagnosis
// rule, forcing the insufficient_evidence/unknown conclusion.
func startStatusStub(code int) (FaultTarget, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("status stub listen: %w", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_, _ = w.Write([]byte(`{"status":"fault stub"}`))
	})
	srv := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() { _ = srv.Serve(ln) }()
	return &statusStub{ln: ln, srv: srv}, nil
}

type statusStub struct {
	ln  net.Listener
	srv *http.Server
}

func (s *statusStub) URL() string { return "http://" + s.ln.Addr().String() }

func (s *statusStub) Close() error { return s.srv.Close() }
