// Package rediskv is a minimal Redis client covering the lab's needs:
// PING and GET over a single short-lived connection. It is intentionally
// dependency-free and not a general-purpose client.
package rediskv

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// ErrKeyNotFound is returned when GET references a missing key.
var ErrKeyNotFound = errors.New("redis: key not found")

type ErrorType string

const (
	ErrorTypeConnectionRefused ErrorType = "connection_refused"
	ErrorTypeTimeout           ErrorType = "timeout"
	ErrorTypeRedisError        ErrorType = "redis_error"
)

// Error wraps a Redis failure with a coarse-grained type used in structured logs.
type Error struct {
	Type    ErrorType
	Message string
}

func (e *Error) Error() string { return e.Message }

// Classify maps a dial/IO error to one of the coarse error types.
func Classify(err error) *Error {
	if err == nil {
		return nil
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return &Error{Type: ErrorTypeTimeout, Message: err.Error()}
	}
	if errors.Is(err, syscall.ECONNREFUSED) {
		return &Error{Type: ErrorTypeConnectionRefused, Message: "failed to connect to redis"}
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "connection refused") {
		return &Error{Type: ErrorTypeConnectionRefused, Message: "failed to connect to redis"}
	}
	if strings.Contains(msg, "i/o timeout") || strings.Contains(msg, "deadline exceeded") {
		return &Error{Type: ErrorTypeTimeout, Message: err.Error()}
	}
	return &Error{Type: ErrorTypeRedisError, Message: err.Error()}
}

type Client struct {
	addr    string
	timeout time.Duration
}

func New(addr string, timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	return &Client{addr: addr, timeout: timeout}
}

func (c *Client) dial() (net.Conn, *Error) {
	conn, err := net.DialTimeout("tcp", c.addr, c.timeout)
	if err != nil {
		return nil, Classify(err)
	}
	return conn, nil
}

func (c *Client) Ping() *Error {
	conn, err := c.dial()
	if err != nil {
		return err
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(c.timeout))

	r := bufio.NewReader(conn)
	if _, err := conn.Write([]byte("PING\r\n")); err != nil {
		return Classify(err)
	}
	reply, readErr := readLine(r)
	if readErr != nil {
		return Classify(readErr)
	}
	if string(reply) != "+PONG" {
		return &Error{Type: ErrorTypeRedisError, Message: "unexpected PING reply: " + string(reply)}
	}
	return nil
}

// Get fetches a key. Missing keys return ErrKeyNotFound.
func (c *Client) Get(key string) (string, error) {
	conn, dialErr := c.dial()
	if dialErr != nil {
		return "", dialErr
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(c.timeout))

	r := bufio.NewReader(conn)
	cmd := fmt.Sprintf("*2\r\n$3\r\nGET\r\n$%d\r\n%s\r\n", len(key), key)
	if _, err := conn.Write([]byte(cmd)); err != nil {
		return "", Classify(err)
	}
	line, err := readLine(r)
	if err != nil {
		return "", Classify(err)
	}
	switch line[0] {
	case '$':
		if string(line) == "$-1" {
			return "", ErrKeyNotFound
		}
		n, err := strconv.Atoi(string(line[1:]))
		if err != nil {
			return "", &Error{Type: ErrorTypeRedisError, Message: "bad bulk length: " + string(line)}
		}
		buf := make([]byte, n+2)
		if _, err := readFull(r, buf); err != nil {
			return "", Classify(err)
		}
		return string(buf[:n]), nil
	case '-':
		return "", &Error{Type: ErrorTypeRedisError, Message: string(line[1:])}
	default:
		return "", &Error{Type: ErrorTypeRedisError, Message: "unexpected GET reply: " + string(line)}
	}
}

func readLine(r *bufio.Reader) ([]byte, error) {
	line, err := r.ReadBytes('\n')
	if err != nil {
		return nil, err
	}
	return line[:len(line)-2], nil
}

func readFull(r *bufio.Reader, buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		n, err := r.Read(buf[total:])
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}
