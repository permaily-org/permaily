package enclave

import (
	"bufio"
	"context"
	"errors"
	"net"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// reader accepts the sink's connections and collects the lines it receives.
func reader(t *testing.T) (dial func(context.Context) (net.Conn, error), lines chan string, ln net.Listener) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	lines = make(chan string, 4096)
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				sc := bufio.NewScanner(conn)
				sc.Buffer(make([]byte, 64<<10), 64<<10)
				for sc.Scan() {
					lines <- sc.Text()
				}
			}()
		}
	}()
	return func(ctx context.Context) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", ln.Addr().String())
	}, lines, ln
}

func next(t *testing.T, lines chan string) string {
	t.Helper()
	select {
	case l := <-lines:
		return l
	case <-time.After(5 * time.Second):
		t.Fatal("no log line arrived")
		return ""
	}
}

func TestLogSinkDelivers(t *testing.T) {
	dial, lines, _ := reader(t)
	s := newLogSink(t.Context(), dial, time.Millisecond)
	if n, err := s.Write([]byte(`{"msg":"one"}` + "\n")); n != 14 || err != nil {
		t.Fatalf("Write = %d, %v", n, err)
	}
	_, _ = s.Write([]byte(`{"msg":"two"}` + "\n"))
	if a, b := next(t, lines), next(t, lines); a != `{"msg":"one"}` || b != `{"msg":"two"}` {
		t.Fatalf("got %q, %q", a, b)
	}

	// A huge line cannot take the parent's memory: it is cut, and still ends the line.
	big := strings.Repeat("x", logLineLimit*2)
	if n, _ := s.Write([]byte(big)); n != len(big) {
		t.Fatalf("Write reported %d of %d bytes", n, len(big))
	}
	if got := next(t, lines); len(got) != logLineLimit-1 {
		t.Fatalf("long line arrived with %d bytes, want %d", len(got), logLineLimit-1)
	}
}

func TestLogSinkNeverBlocks(t *testing.T) {
	// The parent is not listening: writes still return at once, and the lines
	// that did not fit are counted and reported when the parent comes back.
	var up atomic.Bool
	dial, lines, _ := reader(t)
	s := newLogSink(t.Context(), func(ctx context.Context) (net.Conn, error) {
		if !up.Load() {
			return nil, errors.New("connection refused")
		}
		return dial(ctx)
	}, time.Millisecond)

	done := make(chan struct{})
	go func() {
		for range logQueue + 100 {
			_, _ = s.Write([]byte("line\n"))
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Write blocked while the parent was not listening")
	}
	up.Store(true)
	if got := next(t, lines); got != `{"level":"WARN","msg":"log lines dropped","count":100}` {
		t.Fatalf("first line after reconnecting: %q", got)
	}
	if got := next(t, lines); got != "line" {
		t.Fatalf("queued line: %q", got)
	}
}

func TestLogSinkReconnects(t *testing.T) {
	var conns atomic.Int32
	dial, lines, _ := reader(t)
	var current atomic.Pointer[net.Conn]
	s := newLogSink(t.Context(), func(ctx context.Context) (net.Conn, error) {
		c, err := dial(ctx)
		if err == nil {
			conns.Add(1)
			current.Store(&c)
		}
		return c, err
	}, time.Millisecond)

	_, _ = s.Write([]byte("before\n"))
	if next(t, lines) != "before" {
		t.Fatal("first line")
	}
	_ = (*current.Load()).Close() // the parent restarts
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		_, _ = s.Write([]byte("after\n"))
		select {
		case l := <-lines:
			if l == "after" && conns.Load() >= 2 {
				return
			}
		case <-time.After(20 * time.Millisecond):
		}
	}
	t.Fatalf("no line arrived after the parent restarted (connections: %d)", conns.Load())
}

func TestLogSinkStops(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	s := newLogSink(ctx, func(context.Context) (net.Conn, error) { return nil, errors.New("down") }, time.Hour)
	cancel()
	// After shutdown writes still succeed; nothing is waiting for them.
	for range logQueue * 2 {
		if _, err := s.Write([]byte("x\n")); err != nil {
			t.Fatal(err)
		}
	}
	if NewLogSink(ctx, 1) == nil {
		t.Fatal("NewLogSink")
	}
}

// A host that accepts the connection and closes it at once (its log reader
// crashed): the line being written is lost and counted, the others wait in the
// queue, and the sink pauses between attempts instead of spinning.
func TestLogSinkCountsFailedWrites(t *testing.T) {
	var healthy atomic.Bool
	var attempts atomic.Int32
	dial, lines, _ := reader(t)
	s := newLogSink(t.Context(), func(ctx context.Context) (net.Conn, error) {
		if healthy.Load() {
			return dial(ctx)
		}
		attempts.Add(1)
		a, b := net.Pipe()
		_ = b.Close()
		return a, nil
	}, 20*time.Millisecond)

	for range 3 {
		_, _ = s.Write([]byte("queued\n"))
	}
	time.Sleep(300 * time.Millisecond)
	if n := attempts.Load(); n < 2 || n > 6 {
		t.Fatalf("%d connection attempts in 300 ms with a 20 ms backoff that doubles: want a few, not a busy loop", n)
	}
	if got := s.dropped.Load(); got != 1 {
		t.Fatalf("dropped = %d, want 1 (only the line that was being written)", got)
	}
	healthy.Store(true)
	if got := next(t, lines); got != `{"level":"WARN","msg":"log lines dropped","count":1}` {
		t.Fatalf("first line once the host reads again: %q", got)
	}
	if a, b := next(t, lines), next(t, lines); a != "queued" || b != "queued" {
		t.Fatalf("queued lines: %q, %q", a, b)
	}
}
