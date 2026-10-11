package rediskv

import (
	"bufio"
	"errors"
	"net"
	"strings"
	"testing"
	"time"
)

func serveRedis(t *testing.T, handle func(net.Conn, *bufio.Reader)) (string, func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		handle(conn, bufio.NewReader(conn))
	}()
	return ln.Addr().String(), func() { ln.Close() }
}

func TestPingSuccess(t *testing.T) {
	addr, stop := serveRedis(t, func(conn net.Conn, r *bufio.Reader) {
		line, _ := r.ReadString('\n')
		if !strings.HasPrefix(line, "PING") {
			t.Errorf("unexpected command: %q", line)
		}
		conn.Write([]byte("+PONG\r\n"))
	})
	defer stop()

	if err := New(addr, time.Second).Ping(); err != nil {
		t.Fatalf("ping: %v", err)
	}
}

func TestPingConnectionRefused(t *testing.T) {
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := ln.Addr().String()
	ln.Close()

	err := New(addr, time.Second).Ping()
	if err == nil || err.Type != ErrorTypeConnectionRefused {
		t.Fatalf("want connection_refused, got %+v", err)
	}
}

func TestGetMissingKey(t *testing.T) {
	addr, stop := serveRedis(t, func(conn net.Conn, r *bufio.Reader) {
		r.ReadBytes('\n')
		r.ReadBytes('\n')
		r.ReadBytes('\n')
		conn.Write([]byte("$-1\r\n"))
	})
	defer stop()

	_, err := New(addr, time.Second).Get("missing")
	if !errors.Is(err, ErrKeyNotFound) {
		t.Fatalf("want ErrKeyNotFound, got %v", err)
	}
}

func TestGetValue(t *testing.T) {
	addr, stop := serveRedis(t, func(conn net.Conn, r *bufio.Reader) {
		r.ReadBytes('\n')
		r.ReadBytes('\n')
		r.ReadBytes('\n')
		conn.Write([]byte("$5\r\nhello\r\n"))
	})
	defer stop()

	v, err := New(addr, time.Second).Get("k")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if v != "hello" {
		t.Fatalf("want hello, got %q", v)
	}
}
