package httpapi_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
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
	"github.com/permaily-org/permaily/internal/httpapi"
	"github.com/permaily-org/permaily/internal/oauth"
	"github.com/permaily-org/permaily/internal/services/connections"
	"github.com/permaily-org/permaily/internal/services/connections/connectionstest"
)

// The second review of pull request 6 (ADR 0030, "Changed after the second
// review of pull request 6"): its probes, kept as V45 tests. The bound is in
// time, from the cut to the last byte handed to the socket: a chunk already
// admitted to the transport when the cut lands may still reach it
// (sendChunk, 32 KiB of the answer, with the response's header when it is
// the first), and nothing after it; a success is only an answer whose last
// byte the transport handed to the socket while the lease was held. Over
// HTTP/2 nothing confirms that (Go's server writes the stream's end after
// the handler returns, from a buffer another goroutine flushes), so a leased
// answer is refused there before the token is checked.

// sendChunk is the API's (vendor_api.go): what may still leave after a cut.
const sendChunk = 32 << 10

// protocols are the two a test runs on: h2 is HTTP/2, with TLS or without.
var protocols = []bool{false, true}

func protoName(h2 bool) string { return map[bool]string{false: "http1", true: "http2"}[h2] }

// revokingFinish cuts the lease immediately before Finish takes its lock:
// what the transport sends after that was sent after the cut.
type revokingFinish struct {
	*countedLease
	before func()
}

func (l *revokingFinish) Finish(sent bool) {
	l.before()
	l.Cut()
	l.countedLease.Finish(sent)
}

// V45 (second review, finding 1): an empty answer's success headers leave
// while its lease is held, not after it ended. Over HTTP/1.1 nothing reaches
// the wire once Finish is called; over HTTP/2 the answer is refused before
// the token is checked, and no success header ever leaves.
func TestV45BAnEmptyAnswerFlushesItsHeadersUnderTheLease(t *testing.T) {
	for _, h2 := range protocols {
		t.Run(protoName(h2), func(t *testing.T) {
			lease := &revokingFinish{countedLease: newLease()}
			var before string
			api := attachmentAPI(nil, lease, io.Discard)
			done := make(chan struct{})
			srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(done)
				api.ServeHTTP(w, r)
			}))
			ln := &wire{Listener: srv.Listener}
			srv.Listener = ln
			lease.before = func() { before = ln.sent() }
			srv.Config.ConnContext = httpapi.ConnContext
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
			req, _ := http.NewRequest("GET", srv.URL+"/v1/messages/mbx_1.1/attachments/0", nil)
			req.Header.Set("Authorization", "Bearer "+apiToken)
			resp, err := (&http.Client{Transport: tr, Timeout: 5 * time.Second}).Do(req)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			<-done
			if h2 {
				if resp.ProtoMajor != 2 || resp.StatusCode != http.StatusServiceUnavailable || resp.Header.Get("Content-Disposition") != "" {
					t.Fatalf("HTTP/%d %d %v", resp.ProtoMajor, resp.StatusCode, resp.Header)
				}
				if started, finished, _, _ := lease.State(); started || finished {
					t.Fatal("over HTTP/2 the lease was used")
				}
				return
			}
			lease.finished(t, true)
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

// V45 (second review, finding 1): the lease outlives the transport's real
// completion. With the answer's last write blocked in the socket, the lease
// is not finished; a cut then aborts it and the client never reads the last
// chunk. Over HTTP/2, where Go's server writes the stream's last bytes after
// the handler returned, the answer is refused before the token is checked
// and nothing of it leaves.
func TestV45BTheLastBytesLeaveBeforeTheLeaseEnds(t *testing.T) {
	const secret = "private_last_chunk_marker"
	for _, h2 := range protocols {
		t.Run(protoName(h2), func(t *testing.T) {
			lease := newNotifiedLease()
			api := attachmentAPI([]byte(secret), lease, io.Discard)
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
				api.ServeHTTP(w, r)
			}))
			ln = &pauseNthListener{Listener: srv.Listener, accepted: make(chan *pauseNthConn, 1)}
			srv.Listener = ln
			srv.Config.ConnContext = httpapi.ConnContext
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
				req, _ := http.NewRequest("GET", srv.URL+"/v1/messages/mbx_1.1/attachments/0", nil)
				req.Header.Set("Authorization", "Bearer "+apiToken)
				resp, err := srv.Client().Do(req)
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
				if started, finished, _, _ := lease.State(); started || finished {
					t.Fatal("over HTTP/2 the lease was used")
				}
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
			lease.finishedUnsent(t)
			if strings.Contains(string(body), secret) {
				t.Errorf("the client read %q after the cut, err=%v", body, clientErr)
			}
		})
	}
}

// cutBeforeIO cuts the lease as a write of the answer reaches the response
// writer, after every check of the API's own: the preemption window between
// the admission of a chunk and its write, made deterministic. The writer,
// the socket and the deadlines are all real.
type cutBeforeIO struct {
	http.ResponseWriter
	cut func()
}

func (w *cutBeforeIO) Write(p []byte) (int, error) {
	w.cut()
	return w.ResponseWriter.Write(p)
}

func (w *cutBeforeIO) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// V45 (second review, finding 2): a cut that lands between a chunk's
// admission and its write lets that chunk go, and nothing after it: the
// client reads at most sendChunk bytes of an answer of three chunks, and the
// lease is finished once, unsent.
func TestV45BACutBetweenTheAdmissionAndTheWriteSendsAtMostOneChunk(t *testing.T) {
	prev := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(prev)
	lease := newLease()
	const marker = "private_attachment_marker"
	data := []byte(strings.Repeat(marker, 3000)) // 75,000 bytes: three chunks
	api := attachmentAPI(data, lease, io.Discard)
	done := make(chan struct{})
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(done)
		api.ServeHTTP(&cutBeforeIO{ResponseWriter: w, cut: lease.Cut}, r)
	}))
	srv.Config.ConnContext = httpapi.ConnContext
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	srv.Start()
	defer srv.Close()
	req, _ := http.NewRequest("GET", srv.URL+"/v1/messages/mbx_1.1/attachments/0", nil)
	req.Header.Set("Authorization", "Bearer "+apiToken)
	resp, err := srv.Client().Do(req)
	var body []byte
	if err == nil {
		body, _ = io.ReadAll(resp.Body)
		_ = resp.Body.Close()
	}
	<-done
	lease.finished(t, false)
	if len(body) > sendChunk || len(body) == len(data) {
		t.Fatalf("the client read %d bytes of %d after the cut, more than one chunk (%d)", len(body), len(data), sendChunk)
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

// V45 (second review, finding 2): a cut that lands at the socket's write,
// beneath TLS, lets the chunk already admitted reach the socket and nothing
// after it: at most sendChunk bytes of the answer and tlsAllowance of TLS
// and HTTP. Over HTTP/2 the answer is refused before the token is checked.
func TestV45BACutAtTheSocketSendsAtMostOneChunk(t *testing.T) {
	prev := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(prev)
	for _, h2 := range protocols {
		t.Run(protoName(h2), func(t *testing.T) {
			lease := newLease()
			const marker = "private_attachment_marker"
			data := []byte(strings.Repeat(marker, 3000))
			api := attachmentAPI(data, lease, io.Discard)
			var ln *cutAtSocketListener
			done := make(chan struct{})
			srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(done)
				ln.armed.Store(true)
				api.ServeHTTP(w, r)
			}))
			ln = &cutAtSocketListener{Listener: srv.Listener, cut: lease.Cut}
			srv.Listener = ln
			srv.Config.ConnContext = httpapi.ConnContext
			srv.Config.ErrorLog = log.New(io.Discard, "", 0)
			srv.EnableHTTP2 = h2
			srv.StartTLS()
			defer srv.Close()
			req, _ := http.NewRequest("GET", srv.URL+"/v1/messages/mbx_1.1/attachments/0", nil)
			req.Header.Set("Authorization", "Bearer "+apiToken)
			resp, err := srv.Client().Do(req)
			var body []byte
			status := 0
			if err == nil {
				status = resp.StatusCode
				body, _ = io.ReadAll(resp.Body)
				_ = resp.Body.Close()
			}
			<-done
			if h2 {
				if status != http.StatusServiceUnavailable || strings.Contains(string(body), marker) {
					t.Fatalf("%d %q", status, body)
				}
				if started, finished, _, _ := lease.State(); started || finished {
					t.Fatal("over HTTP/2 the lease was used")
				}
				return
			}
			lease.finished(t, false)
			if n := ln.after.Load(); n > sendChunk+tlsAllowance || len(body) > sendChunk || len(body) == len(data) {
				t.Errorf("the socket wrote %d bytes after the cut; the client read %d of %d", n, len(body), len(data))
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

// V45 (second review, finding 2): once the transfer sees its lease cut, the
// abort (the write deadline, then the connection's close) has completed
// before anything else happens and before the handler returns, whatever
// context the lease answered: the admission and the abort share one state,
// not only an asynchronous callback.
func TestV45BTheAbortCompletesBeforeTheHandlerReturns(t *testing.T) {
	prev := runtime.GOMAXPROCS(1) // the context's own watcher does not run first
	defer runtime.GOMAXPROCS(prev)
	lease := &chanLease{done: make(chan struct{})}
	h := httpapi.Hooked(attachmentAPI(make([]byte, 3*sendChunk), lease, io.Discard))
	w := &cutWriter{ResponseRecorder: httptest.NewRecorder(), cut: lease.Cut}
	r := httptest.NewRequest("GET", "https://"+apiHost+"/v1/messages/mbx_1.1/attachments/0", nil)
	r.Header.Set("Authorization", "Bearer "+apiToken)
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
		h.ServeHTTP(w, r)
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

// V45 (second review, finding 3): over HTTP/2 a leased route is refused
// before the token is checked, so nothing is prepared or recorded: the same
// generic 503 with Retry-After, logged no_abort, and no call to the service.
// A route that sends no mail (GET /v1/connection) still answers.
func TestV45BHTTP2IsRefusedBeforeTheTokenIsChecked(t *testing.T) {
	for _, route := range []struct{ method, path, body string }{
		{"POST", "/v1/messages/search", `{}`},
		{"GET", "/v1/messages/mbx_1.1", ""},
		{"GET", "/v1/messages/mbx_1.1/attachments/0", ""},
	} {
		t.Run(route.path, func(t *testing.T) {
			f := &connectionstest.Fake{}
			grants(f, apiGrant)
			var logs lockedBuffer
			h := httpapi.VendorAPI(httpapi.ConnectionsConfig{Service: f, OAuth: testOAuth, Challenge: oauth.Challenge, Log: slog.New(slog.NewJSONHandler(&logs, nil))})
			w := httptest.NewRecorder()
			r := httptest.NewRequest(route.method, "https://"+apiHost+route.path, strings.NewReader(route.body))
			r.Header.Set("Authorization", "Bearer "+apiToken)
			r.Proto, r.ProtoMajor, r.ProtoMinor = "HTTP/2.0", 2, 0
			h.ServeHTTP(w, r)
			if w.Code != http.StatusServiceUnavailable || w.Header().Get("Retry-After") != "5" || !strings.Contains(w.Body.String(), `"unavailable"`) || strings.Contains(w.Body.String(), "no_abort") {
				t.Fatalf("%d %v %s", w.Code, w.Header(), w.Body)
			}
			if calls := f.Calls(); len(calls) != 0 {
				t.Fatalf("the service was called: %v", calls)
			}
			if s := logs.String(); !strings.Contains(s, `"outcome":"no_abort"`) {
				t.Fatalf("logged %s", s)
			}
		})
	}
	f := &connectionstest.Fake{}
	grants(f, apiGrant)
	f.InfoFunc = func(context.Context, *oauth.Grant) (*contracts.VendorConnection, error) {
		return &contracts.VendorConnection{}, nil
	}
	h := httpapi.VendorAPI(httpapi.ConnectionsConfig{Service: f, OAuth: testOAuth, Challenge: oauth.Challenge, Log: slog.New(slog.DiscardHandler)})
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "https://"+apiHost+"/v1/connection", nil)
	r.Header.Set("Authorization", "Bearer "+apiToken)
	r.Proto, r.ProtoMajor, r.ProtoMinor = "HTTP/2.0", 2, 0
	h.ServeHTTP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("GET /v1/connection over HTTP/2: %d %s", w.Code, w.Body)
	}
}
