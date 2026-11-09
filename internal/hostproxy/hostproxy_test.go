package hostproxy

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
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
	go Forward(ctx, ln, func(ctx context.Context) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", echo.Addr().String())
	}, slog.New(slog.DiscardHandler))

	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(5 * time.Second))
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
	go srv.Serve(ln)
	defer srv.Close()

	connect := func(target string) (net.Conn, *bufio.Reader, int) {
		c, err := net.Dial("tcp", ln.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		c.SetDeadline(time.Now().Add(5 * time.Second))
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
