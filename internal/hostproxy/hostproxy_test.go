package hostproxy

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

func echoServer(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); _, _ = io.Copy(c, c) }()
		}
	}()
	t.Cleanup(func() { ln.Close() })
	return ln
}

func TestForward(t *testing.T) {
	echo := echoServer(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = Forward(ctx, ln, func(ctx context.Context) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "tcp", echo.Addr().String())
		}, slog.New(slog.DiscardHandler))
	}()

	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	fmt.Fprint(c, "ping\n")
	line, err := bufio.NewReader(c).ReadString('\n')
	if err != nil || line != "ping\n" {
		t.Fatalf("got %q, %v", line, err)
	}
}

func TestConnectProxy(t *testing.T) {
	echo := echoServer(t)
	allowed := echo.Addr().String()
	proxy := &ConnectProxy{Allow: []string{allowed}, Log: slog.New(slog.DiscardHandler)}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: proxy}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()

	connect := func(target string) (net.Conn, *bufio.Reader, int) {
		c, err := net.Dial("tcp", ln.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		_ = c.SetDeadline(time.Now().Add(5 * time.Second))
		fmt.Fprintf(c, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target)
		br := bufio.NewReader(c)
		res, err := http.ReadResponse(br, &http.Request{Method: http.MethodConnect})
		if err != nil {
			t.Fatal(err)
		}
		return c, br, res.StatusCode
	}

	c, br, code := connect(allowed)
	defer c.Close()
	if code != http.StatusOK {
		t.Fatalf("allowed target: status %d", code)
	}
	fmt.Fprint(c, "hello\n")
	if line, err := br.ReadString('\n'); err != nil || line != "hello\n" {
		t.Fatalf("tunnel echo: %q, %v", line, err)
	}

	d, _, code := connect("example.com:443")
	defer d.Close()
	if code != http.StatusForbidden {
		t.Fatalf("denied target: status %d, want 403", code)
	}
}

// syncBuffer is a bytes.Buffer safe for the goroutines of Logs.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestLogs(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	var out syncBuffer
	done := make(chan error, 1)
	go func() { done <- Logs(ctx, ln, &out) }()

	send := func(text string) {
		conn, err := net.Dial("tcp", ln.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		if _, err := conn.Write([]byte(text)); err != nil {
			t.Fatal(err)
		}
	}
	waitFor := func(want string) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for !strings.Contains(out.String(), want) {
			if time.Now().After(deadline) {
				t.Fatalf("output %q lacks %q", out.String(), want)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}

	send("{\"msg\":\"starting\"}\n{\"msg\":\"ready\"}\n")
	waitFor("enclave: {\"msg\":\"starting\"}\nenclave: {\"msg\":\"ready\"}\n")

	// A line longer than the limit is cut to the limit and does not swallow the next one.
	send(strings.Repeat("x", logLineLimit+500) + "\nafter\n")
	waitFor("enclave: after\n")
	for _, line := range strings.Split(out.String(), "\n") {
		if len(line) > len("enclave: ")+logLineLimit {
			t.Fatalf("a %d byte line was printed", len(line))
		}
	}
	if strings.Count(out.String(), "enclave: ") != 4 {
		t.Fatalf("output has %d lines, want 4:\n%.300s", strings.Count(out.String(), "enclave: "), out.String())
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Logs returned %v after shutdown", err)
	}
}
