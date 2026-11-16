package mcpapi_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/mcpapi"
	"github.com/permaily-org/permaily/internal/oauth"
	"github.com/permaily-org/permaily/internal/services/connections"
	"github.com/permaily-org/permaily/internal/services/connections/connectionstest"
)

// The second review of pull request 6 (#131), whose findings 1 and 2 hold
// for this adapter too (ADR 0030, "Changed after the second review of pull
// request 6"): its probes, adapted to tool calls, as V50 tests. The bound is
// in time, from the cut to the last byte handed to the socket: a chunk
// already admitted to the transport when the cut lands may still reach it
// (sendChunk, 32 KiB of the answer, with the response's header when it is
// the first), and nothing after it; a success is only an answer whose last
// byte the transport handed to the socket while the lease was held. Over
// HTTP/2 nothing confirms that (Go's server writes the stream's last bytes
// after the handler returns, from a buffer another goroutine flushes), so a
// tool call is refused there before the SDK runs.

// sendChunk is the adapter's (mcpapi.go): what may still leave after a cut.
const sendChunk = 32 << 10

var protocols = []bool{false, true}

func protoName(h2 bool) string { return map[bool]string{false: "http1", true: "http2"}[h2] }

// on is q sent to srv, a real server.
func on(srv *httptest.Server, q request) *http.Request {
	r := q.http()
	r.URL.Scheme = strings.SplitN(srv.URL, ":", 2)[0]
	r.URL.Host = srv.Listener.Addr().String()
	r.RequestURI = ""
	return r
}

// readingWith is the world whose read_message answers text under lease.
func readingWith(text string, lease connections.Release) *world {
	w := newWorld()
	w.fake.ReadFunc = func(context.Context, *oauth.Grant, string) (*contracts.VendorMessage, connections.Release, error) {
		m := message
		m.Text = &text
		return &m, lease, nil
	}
	return w
}

// notifiedLease is a connectionstest.Lease that counts its Finish calls and
// closes finished on the first.
type notifiedLease struct {
	*connectionstest.Lease
	finished chan struct{}
	once     sync.Once
	mu       sync.Mutex
	finishes []bool
	// before, when set, runs as Finish is called, before it takes the
	// lease's lock.
	before func()
}

func newNotifiedLease() *notifiedLease {
	return &notifiedLease{Lease: &connectionstest.Lease{}, finished: make(chan struct{})}
}

func (l *notifiedLease) Finish(sent bool) {
	if l.before != nil {
		l.before()
	}
	l.mu.Lock()
	l.finishes = append(l.finishes, sent)
	l.mu.Unlock()
	l.Lease.Finish(sent)
	l.once.Do(func() { close(l.finished) })
}

func (l *notifiedLease) finishedOnce(t *testing.T, sent bool) {
	t.Helper()
	l.mu.Lock()
	defer l.mu.Unlock()
	if fmt.Sprint(l.finishes) != fmt.Sprint([]bool{sent}) {
		t.Fatalf("Finish called %v, want once with %v", l.finishes, sent)
	}
}

func (l *notifiedLease) unused(t *testing.T) {
	t.Helper()
	if started, finished, _, _ := l.State(); started || finished {
		t.Fatalf("over HTTP/2 the lease was used: started %v, finished %v", started, finished)
	}
}

// wire records every byte the server writes to its connections.
type wire struct {
	net.Listener
	mu  sync.Mutex
	out strings.Builder
}

func (l *wire) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return &wireConn{c, l}, nil
}

func (l *wire) sent() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.out.String()
}

type wireConn struct {
	net.Conn
	l *wire
}

func (c *wireConn) Write(b []byte) (int, error) {
	c.l.mu.Lock()
	c.l.out.Write(b)
	c.l.mu.Unlock()
	return c.Conn.Write(b)
}

// V50 (#131's second review, finding 1): an answer's bytes leave while its
// lease is held: over HTTP/1.1 nothing reaches the wire once Finish is
// called, a cut placed there included; over HTTP/2 the tool call is refused
// before the SDK runs, and nothing of an answer leaves.
func TestV50BNothingLeavesAfterFinish(t *testing.T) {
	for _, h2 := range protocols {
		t.Run(protoName(h2), func(t *testing.T) {
			lease := newNotifiedLease()
			var before string
			w := readingWith("the text", lease)
			srv := httptest.NewUnstartedServer(w.handler())
			ln := &wire{Listener: srv.Listener}
			srv.Listener = ln
			lease.before = func() { before = ln.sent(); lease.Cut() }
			srv.Config.ConnContext = mcpapi.ConnContext
			srv.Config.ErrorLog = log.New(io.Discard, "", 0)
			tr := &http.Transport{}
			if h2 {
				srv.Config.Protocols = new(http.Protocols)
				srv.Config.Protocols.SetUnencryptedHTTP2(true)
				tr.Protocols = new(http.Protocols)
				tr.Protocols.SetUnencryptedHTTP2(true)
			}
			srv.Start()
			defer srv.Close()
			defer tr.CloseIdleConnections()
			resp, err := (&http.Client{Transport: tr, Timeout: 5 * time.Second}).Do(on(srv, toolCall(1, "read_message", `{"id":"mbx_A.msg_1"}`)))
			if err != nil {
				t.Fatal(err)
			}
			body := readAll(t, resp.Body)
			_ = resp.Body.Close()
			srv.Close()
			if h2 {
				if resp.ProtoMajor != 2 || resp.StatusCode != http.StatusServiceUnavailable || strings.Contains(string(body), "the text") {
					t.Fatalf("HTTP/%d %d %s", resp.ProtoMajor, resp.StatusCode, body)
				}
				lease.unused(t)
				return
			}
			lease.finishedOnce(t, true)
			if resp.StatusCode != http.StatusOK || len(ln.sent()) > len(before) {
				t.Errorf("HTTP/%d %d: %d wire bytes before Finish, %d after it", resp.ProtoMajor, resp.StatusCode, len(before), len(ln.sent())-len(before))
			}
		})
	}
}

// pauseNthListener's connections pause their nth write once armed.
type pauseNthListener struct {
	net.Listener
	accepted chan *pauseNthConn
}

type pauseNthConn struct {
	*keyUpdateConn
	left atomic.Int32
}

func (l *pauseNthListener) Accept() (net.Conn, error) {
	c, e := l.Listener.Accept()
	if e != nil {
		return nil, e
	}
	p := &pauseNthConn{keyUpdateConn: &keyUpdateConn{Conn: c, paused: make(chan struct{}), resume: make(chan struct{}), interrupted: make(chan struct{})}}
	l.accepted <- p
	return p, nil
}

func (p *pauseNthConn) Write(b []byte) (int, error) {
	if p.left.Add(-1) == 0 {
		p.armed.Store(true)
	}
	return p.keyUpdateConn.Write(b)
}

// V50 (#131's second review, finding 1): the lease outlives the transport's
// real completion. With the answer's last write blocked in the socket, the
// lease is not finished; a cut then aborts it and the client never reads
// the last chunk. Over HTTP/2 the tool call is refused before the SDK runs.
func TestV50BTheLastBytesLeaveBeforeTheLeaseEnds(t *testing.T) {
	const secret = "private_last_chunk_marker"
	for _, h2 := range protocols {
		t.Run(protoName(h2), func(t *testing.T) {
			lease := newNotifiedLease()
			h := readingWith(secret, lease).handler()
			var ln *pauseNthListener
			armed := make(chan *pauseNthConn, 1)
			handled := make(chan struct{})
			srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(handled)
				p := <-ln.accepted
				if !h2 {
					p.left.Store(1) // the answer's one write
					armed <- p
				}
				h.ServeHTTP(w, r)
			}))
			ln = &pauseNthListener{Listener: srv.Listener, accepted: make(chan *pauseNthConn, 1)}
			srv.Listener = ln
			srv.Config.ConnContext = mcpapi.ConnContext
			srv.Config.ErrorLog = log.New(io.Discard, "", 0)
			srv.EnableHTTP2 = h2
			srv.StartTLS()
			defer srv.Close()
			done := make(chan struct{})
			var body []byte
			var status, proto int
			var clientErr error
			go func() {
				defer close(done)
				resp, err := srv.Client().Do(on(srv, toolCall(1, "read_message", `{"id":"mbx_A.msg_1"}`)))
				clientErr = err
				if err == nil {
					status, proto = resp.StatusCode, resp.ProtoMajor
					body, clientErr = io.ReadAll(resp.Body)
					_ = resp.Body.Close()
				}
			}()
			if h2 {
				<-done
				<-handled
				if proto != 2 || status != http.StatusServiceUnavailable || strings.Contains(string(body), secret) {
					t.Fatalf("HTTP/%d %d %q %v", proto, status, body, clientErr)
				}
				lease.unused(t)
				return
			}
			p := <-armed
			defer p.Close()
			<-p.paused // the answer's write is blocked in the socket
			select {
			case <-lease.finished:
				t.Fatal("the lease was finished while the answer's last write was still in the socket")
			case <-time.After(100 * time.Millisecond):
			}
			lease.Cut()
			select {
			case <-lease.finished:
			case <-time.After(2 * time.Second):
				t.Error("the cut did not finish the lease")
			}
			close(p.resume)
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("the client is stuck")
			}
			<-handled
			lease.finishedOnce(t, false)
			if strings.Contains(string(body), secret) {
				t.Errorf("the client read %q after the cut, err=%v", body, clientErr)
			}
		})
	}
}

// cutBeforeIO cuts the lease as a write of the answer reaches the response
// writer, after every check of the adapter's own: the preemption window
// between the admission of a chunk and its write, made deterministic.
type cutBeforeIO struct {
	http.ResponseWriter
	cut func()
}

func (w *cutBeforeIO) Write(p []byte) (int, error) {
	w.cut()
	return w.ResponseWriter.Write(p)
}

func (w *cutBeforeIO) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// V50 (#131's second review, finding 2): a cut that lands between a chunk's
// admission and its write lets that chunk go, and nothing after it: the
// client reads at most sendChunk bytes of an answer of several chunks, and
// the lease is finished once, unsent.
func TestV50BACutBetweenTheAdmissionAndTheWriteSendsAtMostOneChunk(t *testing.T) {
	prev := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(prev)
	lease := newNotifiedLease()
	h := readingWith(strings.Repeat("private_message_marker", 3000), lease).handler()
	done := make(chan struct{})
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(done)
		h.ServeHTTP(&cutBeforeIO{ResponseWriter: w, cut: lease.Cut}, r)
	}))
	srv.Config.ConnContext = mcpapi.ConnContext
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	srv.Start()
	defer srv.Close()
	resp, err := srv.Client().Do(on(srv, toolCall(1, "read_message", `{"id":"mbx_A.msg_1"}`)))
	var body []byte
	length := int64(0)
	if err == nil {
		length = resp.ContentLength
		body, _ = io.ReadAll(resp.Body)
		_ = resp.Body.Close()
	}
	<-done
	lease.finishedOnce(t, false)
	if len(body) > sendChunk || length < 3*sendChunk || int64(len(body)) == length {
		t.Fatalf("the client read %d bytes of %d after the cut, more than one chunk (%d)", len(body), length, sendChunk)
	}
}

// cutAtSocketListener cuts the lease just before the real socket's write,
// beneath the unmodified TLS and HTTP servers, and counts what that socket
// writes after the cut.
type cutAtSocketListener struct {
	net.Listener
	cut   func()
	armed atomic.Bool
	after atomic.Int64
}

type cutAtSocketConn struct {
	net.Conn
	l *cutAtSocketListener
}

func (l *cutAtSocketListener) Accept() (net.Conn, error) {
	c, e := l.Listener.Accept()
	if e != nil {
		return nil, e
	}
	return &cutAtSocketConn{c, l}, nil
}

func (c *cutAtSocketConn) Write(p []byte) (int, error) {
	cut := c.l.armed.CompareAndSwap(true, false)
	if cut {
		c.l.cut()
	}
	n, e := c.Conn.Write(p)
	if cut || c.l.after.Load() > 0 {
		c.l.after.Add(int64(n))
	}
	return n, e
}

// tlsAllowance is what TLS and HTTP add to a chunk on the wire: the
// response's header and every record's header and tag.
const tlsAllowance = 4 << 10

// V50 (#131's second review, finding 2): a cut that lands at the socket's
// write, beneath TLS, lets the chunk already admitted reach the socket and
// nothing after it: at most sendChunk bytes of the answer and tlsAllowance
// of TLS and HTTP. Over HTTP/2 the tool call is refused before the SDK runs.
func TestV50BACutAtTheSocketSendsAtMostOneChunk(t *testing.T) {
	prev := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(prev)
	const marker = "private_message_marker"
	for _, h2 := range protocols {
		t.Run(protoName(h2), func(t *testing.T) {
			lease := newNotifiedLease()
			h := readingWith(strings.Repeat(marker, 3000), lease).handler()
			var ln *cutAtSocketListener
			done := make(chan struct{})
			srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(done)
				ln.armed.Store(true)
				h.ServeHTTP(w, r)
			}))
			ln = &cutAtSocketListener{Listener: srv.Listener, cut: lease.Cut}
			srv.Listener = ln
			srv.Config.ConnContext = mcpapi.ConnContext
			srv.Config.ErrorLog = log.New(io.Discard, "", 0)
			srv.EnableHTTP2 = h2
			srv.StartTLS()
			defer srv.Close()
			resp, err := srv.Client().Do(on(srv, toolCall(1, "read_message", `{"id":"mbx_A.msg_1"}`)))
			var body []byte
			status := 0
			length := int64(0)
			if err == nil {
				status, length = resp.StatusCode, resp.ContentLength
				body, _ = io.ReadAll(resp.Body)
				_ = resp.Body.Close()
			}
			<-done
			if h2 {
				if status != http.StatusServiceUnavailable || strings.Contains(string(body), marker) {
					t.Fatalf("%d %q", status, body)
				}
				lease.unused(t)
				return
			}
			lease.finishedOnce(t, false)
			if n := ln.after.Load(); n > sendChunk+tlsAllowance || len(body) > sendChunk || int64(len(body)) == length {
				t.Errorf("the socket wrote %d bytes after the cut; the client read %d of %d", n, len(body), length)
			}
		})
	}
}

// chanContext is a context that is not one of package context's own: it
// ends when its channel is closed. Any context keeps Release's contract.
type chanContext struct {
	context.Context
	done chan struct{}
}

func (c *chanContext) Done() <-chan struct{} { return c.done }

func (c *chanContext) Err() error {
	select {
	case <-c.done:
		return context.Canceled
	default:
		return nil
	}
}

// chanLease is a lease whose Start answers a chanContext.
type chanLease struct {
	mu       sync.Mutex
	done     chan struct{}
	cut      sync.Once
	finishes []bool
}

var _ connections.Release = (*chanLease)(nil)

func (l *chanLease) Start(ctx context.Context) (context.Context, error) {
	return &chanContext{Context: ctx, done: l.done}, nil
}

func (l *chanLease) Finish(sent bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.finishes = append(l.finishes, sent)
}

func (l *chanLease) Cut() { l.cut.Do(func() { close(l.done) }) }

// cutWriter cuts the lease as the answer is written, and records the write
// deadlines set on it.
type cutWriter struct {
	*httptest.ResponseRecorder
	cut       func()
	deadlines atomic.Int32
}

func (w *cutWriter) Write(p []byte) (int, error) {
	w.cut()
	return w.ResponseRecorder.Write(p)
}

func (w *cutWriter) SetWriteDeadline(time.Time) error {
	w.deadlines.Add(1)
	return nil
}

// V50 (#131's second review, finding 2): once the transfer sees its lease
// cut, the abort (the write deadline, then the connection's close) has
// completed before anything else happens and before the handler returns,
// whatever context the lease answered: the admission and the abort share
// one state, not only an asynchronous callback.
func TestV50BTheAbortCompletesBeforeTheHandlerReturns(t *testing.T) {
	prev := runtime.GOMAXPROCS(1) // the context's own watcher does not run first
	defer runtime.GOMAXPROCS(prev)
	lease := &chanLease{done: make(chan struct{})}
	h := readingWith(strings.Repeat("x", 3*sendChunk), lease).handler()
	w := &cutWriter{ResponseRecorder: httptest.NewRecorder(), cut: lease.Cut}
	aborted := false
	func() {
		defer func() {
			if v := recover(); v != nil {
				err, _ := v.(error)
				if !errors.Is(err, http.ErrAbortHandler) {
					panic(v)
				}
				aborted = true
			}
		}()
		h.ServeHTTP(w, toolCall(1, "read_message", `{"id":"mbx_A.msg_1"}`).http())
	}()
	if n := w.deadlines.Load(); n == 0 || !aborted {
		t.Fatalf("the handler returned with %d write deadlines set, aborted %v", n, aborted)
	}
	if w.Body.Len() > sendChunk || w.Flushed {
		t.Fatalf("%d bytes written, flushed %v", w.Body.Len(), w.Flushed)
	}
	lease.mu.Lock()
	defer lease.mu.Unlock()
	if fmt.Sprint(lease.finishes) != "[false]" {
		t.Fatalf("Finish called %v", lease.finishes)
	}
}

// V50 (#131's second review, findings 1 and 3): over HTTP/2 a tool call is
// refused once the token is checked and before the SDK runs, so the service
// prepares nothing: 503 with Retry-After, logged no_abort. A request that is
// not a tool call (tools/list) still answers.
func TestV50BHTTP2ToolCallsAreRefusedBeforeTheSDK(t *testing.T) {
	h2 := func(q request) *http.Request {
		r := q.http()
		r.Proto, r.ProtoMajor, r.ProtoMinor = "HTTP/2.0", 2, 0
		return r
	}
	for _, tool := range []string{"search_mail", "read_message", "get_attachment", "connection_info"} {
		t.Run(tool, func(t *testing.T) {
			w := newWorld()
			var lines logs
			rec := httptest.NewRecorder()
			w.handler(mcpapi.WithLog(lines.logger())).ServeHTTP(rec, h2(toolCall(1, tool, `{"id":"mbx_A.msg_1","index":0}`)))
			if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") == "" || strings.Contains(rec.Body.String(), "no_abort") {
				t.Fatalf("%d %v %s", rec.Code, rec.Header(), rec.Body)
			}
			if calls := w.fake.Calls(); len(calls) != 1 || !strings.HasPrefix(calls[0], "Check") {
				t.Fatalf("the service was asked %v", calls)
			}
			if len(w.Leases()) != 0 {
				t.Fatal("a lease was made")
			}
			if s := lines.String(); !strings.Contains(s, `"outcome":"no_abort"`) {
				t.Fatalf("logged %s", s)
			}
		})
	}
	w := newWorld()
	rec := httptest.NewRecorder()
	w.handler().ServeHTTP(rec, h2(codexCall(1, "tools/list", "")))
	if rec.Code != http.StatusOK {
		t.Fatalf("tools/list over HTTP/2: %d %s", rec.Code, rec.Body)
	}
}
