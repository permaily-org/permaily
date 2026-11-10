package enclave

import (
	"context"
	"fmt"
	"net"
	"sync/atomic"
	"time"

	"github.com/mdlayher/vsock"
)

// LogSink sends the enclave's log lines to the parent instance over vsock, so
// a production enclave (which has no console) can be diagnosed. Logs are
// public by rule: the parent is untrusted, so nothing sensitive may be logged.
//
// It never blocks the caller and never fails it: when the parent is not
// listening or reads too slowly, lines are dropped and counted, and the count
// is reported once the connection is back.
type LogSink struct {
	lines   chan []byte
	dial    func(ctx context.Context) (net.Conn, error)
	dropped atomic.Uint64
	backoff time.Duration
}

const (
	logQueue     = 512
	logLineLimit = 8 << 10
)

// NewLogSink starts a sink that writes to the parent's vsock port until ctx is done.
func NewLogSink(ctx context.Context, port uint32) *LogSink {
	return newLogSink(ctx, func(ctx context.Context) (net.Conn, error) {
		return vsock.DialContext(ctx, HostCID, port, nil)
	}, time.Second)
}

func newLogSink(ctx context.Context, dial func(ctx context.Context) (net.Conn, error), backoff time.Duration) *LogSink {
	s := &LogSink{lines: make(chan []byte, logQueue), dial: dial, backoff: backoff}
	go s.run(ctx)
	return s
}

// Write queues one log line. It always reports success.
func (s *LogSink) Write(p []byte) (int, error) {
	line := make([]byte, min(len(p), logLineLimit))
	copy(line, p)
	if len(p) > logLineLimit {
		line[logLineLimit-1] = '\n'
	}
	select {
	case s.lines <- line:
	default:
		s.dropped.Add(1)
	}
	return len(p), nil
}

func (s *LogSink) run(ctx context.Context) {
	wait := s.backoff
	for ctx.Err() == nil {
		dctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		conn, err := s.dial(dctx)
		cancel()
		if err != nil {
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
			wait = min(wait*2, 30*time.Second)
			continue
		}
		wait = s.backoff
		s.drain(ctx, conn)
		_ = conn.Close()
	}
}

// drain writes queued lines to conn until it fails or ctx is done.
func (s *LogSink) drain(ctx context.Context, conn net.Conn) {
	write := func(b []byte) bool {
		_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		_, err := conn.Write(b)
		return err == nil
	}
	for {
		if n := s.dropped.Swap(0); n > 0 {
			if !write(fmt.Appendf(nil, `{"level":"WARN","msg":"log lines dropped","count":%d}`+"\n", n)) {
				s.dropped.Add(n)
				return
			}
		}
		select {
		case <-ctx.Done():
			return
		case line := <-s.lines:
			if !write(line) {
				s.dropped.Add(1)
				return
			}
		}
	}
}
