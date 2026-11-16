package mcpapi_test

import (
	"context"
	"crypto/rand"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/mcpapi"
	"github.com/permaily-org/permaily/internal/oauth"
	"github.com/permaily-org/permaily/internal/query"
	"github.com/permaily-org/permaily/internal/services/connections"
	"github.com/permaily-org/permaily/internal/services/connections/connectionstest"
)

// The review of pull request 7 (#135): its reproductions, as tests.

// V50 (review 1): a lease cut while a write of the answer is blocked (a
// peer that reads nothing) sends no byte more on the wire, over HTTP/1.1
// with TLS, as the enclave serves it: the socket's write is interrupted, so
// nothing the server holds leaves when the peer reads again. The lease is
// finished once, promptly and not sent, and the request is logged as cut.
// Over HTTP/2 a tool call is refused before the SDK runs
// (TestV50HTTP2ReleasesNoMail).
func TestV50ACutDuringABlockedWriteSendsNoByteAfterIt(t *testing.T) {
	t.Run("http1", func(t *testing.T) {
		data := make([]byte, contracts.VendorAttachmentMax)
		_, _ = rand.Read(data)
		w := newWorld()
		finished := make(chan struct{})
		w.leaseFor = func() *countingLease {
			return &countingLease{Lease: &connectionstest.Lease{}, onFinish: func() { close(finished) }}
		}
		w.fake.AttachmentFunc = func(context.Context, *oauth.Grant, string, int) (*inbox.Attachment, connections.Release, error) {
			return &inbox.Attachment{Name: "a.bin", Type: "application/octet-stream", Data: data}, w.lease(), nil
		}
		var lines logs
		srv := httptest.NewUnstartedServer(w.handler(mcpapi.WithLog(lines.logger())))
		srv.Config.ConnContext = mcpapi.ConnContext
		srv.Config.ErrorLog = log.New(io.Discard, "", 0)
		ln := &pauseListener{Listener: srv.Listener, left: 256 << 10, paused: make(chan struct{}), resume: make(chan struct{})}
		srv.Listener = ln
		srv.StartTLS()
		t.Cleanup(srv.Close)
		t.Cleanup(ln.unpause) // first: a failure never leaves a write blocked

		done := make(chan struct{})
		var n int64
		var proto int
		var readErr error
		go func() {
			defer close(done)
			r := attachmentCall(tokenMCPAttached).http()
			r.URL.Host = srv.Listener.Addr().String()
			r.RequestURI = ""
			resp, err := srv.Client().Do(r)
			if err != nil {
				readErr = err
				return
			}
			defer resp.Body.Close()
			proto = resp.ProtoMajor
			n, readErr = io.Copy(io.Discard, resp.Body)
		}()
		select {
		case <-ln.paused:
		case <-time.After(10 * time.Second):
			t.Fatal("the transfer never blocked")
		}
		ln.cut()
		w.Leases()[0].Cut()
		select {
		case <-finished:
		case <-time.After(2 * time.Second):
			t.Error("the lease was not finished after the cut while the peer read nothing")
		}
		ln.unpause() // the peer reads again
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Fatal("the response was not aborted")
		}
		srv.Close() // waits for the handler
		if after := ln.afterCut(); after != 0 {
			t.Errorf("%d bytes left on the wire after the cut", after)
		}
		if readErr == nil || n >= int64(len(data)) || (proto != 0 && proto != 1) {
			t.Errorf("HTTP/%d: the client read %d bytes, error %v", proto, n, readErr)
		}
		l := w.Leases()[0]
		if c, args := l.Finishes(); c != 1 || args[0] {
			t.Errorf("finished %d %v", c, args)
		}
		if s := lines.String(); !strings.Contains(s, `"outcome":"cut"`) || strings.Contains(s, `"outcome":"answered"`) {
			t.Errorf("the request was logged %s", s)
		}
	})
}

// V50 (review 1, and #131's second review): over HTTP/2, with the
// connection's abort (mcpapi.ConnContext) or without it, a tool call is
// refused before the SDK runs: 503, nothing of the answer, no lease made.
func TestV50HTTP2ReleasesNoMail(t *testing.T) {
	for _, hook := range []bool{false, true} {
		t.Run(map[bool]string{false: "without ConnContext", true: "with ConnContext"}[hook], func(t *testing.T) {
			w := newWorld()
			srv := httptest.NewUnstartedServer(w.handler())
			srv.EnableHTTP2 = true
			if hook {
				srv.Config.ConnContext = mcpapi.ConnContext
			}
			srv.StartTLS()
			t.Cleanup(srv.Close)
			r := toolCall(1, "read_message", `{"id":"mbx_A.msg_1"}`).http()
			r.URL.Host = srv.Listener.Addr().String()
			r.RequestURI = ""
			resp, err := srv.Client().Do(r)
			if err != nil {
				t.Fatal(err)
			}
			body := readAll(t, resp.Body)
			_ = resp.Body.Close()
			srv.Close()
			if resp.ProtoMajor != 2 || resp.StatusCode != http.StatusServiceUnavailable || strings.Contains(string(body), subject) {
				t.Fatalf("HTTP/%d %d %s", resp.ProtoMajor, resp.StatusCode, body)
			}
			if n := len(w.Leases()); n != 0 {
				t.Fatalf("%d leases made", n)
			}
		})
	}
}

// lateWriter cuts the lease as the answer is flushed, and records a write
// deadline set after the handler returned.
type lateWriter struct {
	*httptest.ResponseRecorder
	w         *world
	returned  atomic.Bool
	deadlines atomic.Int32
	late      atomic.Bool
}

func (l *lateWriter) Flush() {
	for _, lease := range l.w.Leases() {
		lease.Cut()
	}
}

func (l *lateWriter) SetWriteDeadline(time.Time) error {
	if l.returned.Load() {
		l.late.Store(true)
	}
	l.deadlines.Add(1)
	return nil
}

// V50 (review 1): the abort a cut sets off acts while the handler runs and
// never after it returned, when the writer is no longer its own.
func TestV50TheCutNeverActsOnTheWriterAfterTheHandlerReturned(t *testing.T) {
	prev := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(prev)
	w := newWorld()
	h := w.handler()
	lw := &lateWriter{ResponseRecorder: httptest.NewRecorder(), w: w}
	func() {
		defer func() {
			if v := recover(); v != nil {
				if err, _ := v.(error); !errors.Is(err, http.ErrAbortHandler) {
					panic(v)
				}
			}
		}()
		h.ServeHTTP(lw, toolCall(1, "search_mail", `{}`).http())
	}()
	lw.returned.Store(true)
	for end := time.Now().Add(time.Second); lw.deadlines.Load() == 0 && time.Now().Before(end); {
		runtime.Gosched()
	}
	if lw.deadlines.Load() == 0 || lw.late.Load() {
		t.Fatalf("deadlines %d, one after the handler returned: %v", lw.deadlines.Load(), lw.late.Load())
	}
}

// V49 (review 2): search_mail's arguments are read as the API reads its
// body, from the bytes the client sent: a formula is read by the formula's
// parser before anything else (the SDK neither validates nor normalises it
// first), so every refusal of one is invalid_formula with where and why, the
// parser's, and a formula the parser refuses never reaches the service, even
// when a reader that keeps the last of two members would take it. Anything
// else that is not search_mail's arguments is invalid_body, as the API
// answers. No refusal says a word of what was sent.
func TestV49SearchMailInputIsReadByTheFormulasParser(t *testing.T) {
	for _, c := range []struct{ name, args string }{
		{"no version", `{"query":{"all":[]}}`},
		{"a number as a value", `{"query":{"v":1,"all":[{"any":[{"var":"subject","op":"contains","value":42}]}]}}`},
		{"an unknown member", `{"query":{"v":1,"all":[],"sender-supplied-field":true}}`},
		{"a version twice", `{"query":{"v":2,"v":1,"all":[]}}`},
		{"a lone surrogate", `{"query":{"v":1,"all":[{"any":[{"var":"subject","op":"contains","value":"\ud800"}]}]}}`},
		{"a member twice in a condition", `{"query":{"v":1,"all":[{"any":[{"var":"subject","var":"body","op":"contains","value":"x"}]}]}}`},
		{"not an object", `{"query":["sender-supplied-field"]}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			raw := strings.TrimSuffix(strings.TrimPrefix(c.args, `{"query":`), "}")
			_, perr := query.Parse([]byte(raw), nil)
			want, ok := errors.AsType[*query.Problem](perr)
			if !ok {
				t.Fatalf("the parser takes %s: %v", raw, perr)
			}
			w := newWorld()
			res := callTool(t, w.handler(), toolCall(1, "search_mail", c.args))
			e := errorOf(t, res)
			if e.Error.Code != "invalid_formula" || e.Error.Message != contracts.ErrInvalidFormula.Message || e.Error.Details == nil || *e.Error.Details != want.FormulaProblem {
				t.Fatalf("want %+v, got %s", want.FormulaProblem, res.Content[0].Text)
			}
			if calls := w.fake.Calls(); len(calls) != 1 {
				t.Fatalf("the service was asked %v", calls)
			}
		})
	}
	for _, c := range []struct{ name, args string }{
		{"an unknown argument", `{"sender-supplied-field":1}`},
		{"an argument twice", `{"limit":5,"limit":6}`},
		// encoding/json reads a name in any case, and the long s as an s.
		{"an argument twice in another case", `{"limit":5,"LIMIT":6}`},
		{"an argument twice in other letters", `{"cursor":"pmk_a","cur\u017for":"pmk_b"}`},
		{"a limit as text", `{"limit":"sender-supplied"}`},
		{"a cursor as a number", `{"cursor":42}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld()
			res := callTool(t, w.handler(), toolCall(1, "search_mail", c.args))
			e := errorOf(t, res)
			if e.Error.Code != "invalid_body" || e.Error.Details != nil || strings.Contains(res.Content[0].Text, "sender-supplied") || strings.Contains(res.Content[0].Text, "42") {
				t.Fatalf("got %s", res.Content[0].Text)
			}
			if calls := w.fake.Calls(); len(calls) != 1 {
				t.Fatalf("the service was asked %v", calls)
			}
		})
	}
	t.Run("a formula the parser takes reaches the service as sent", func(t *testing.T) {
		w := newWorld()
		var got contracts.VendorSearch
		w.fake.SearchFunc = func(_ context.Context, _ *oauth.Grant, in contracts.VendorSearch) (*contracts.VendorPage, connections.Release, error) {
			got = in
			p := page
			return &p, w.lease(), nil
		}
		res := callTool(t, w.handler(), toolCall(1, "search_mail", `{"query":{"v":1,"all":[{"any":[{"var":"subject","op":"contains","value":"invoice","not":true}]}]},"limit":5,"cursor":"pmk_x"}`))
		if res.IsError || got.Query == nil || got.Limit != 5 || got.Cursor != "pmk_x" || len(got.Query.All) != 1 || !got.Query.All[0].Any[0].Not || got.Query.All[0].Any[0].Value != "invoice" {
			t.Fatalf("%+v %+v", res, got)
		}
	})
}

// V48 (review 3): every tool's work with the service keeps the HTTP
// request's deadline and ends when the request does, for both protocols
// the launch clients speak: a client that leaves stops what it asked.
func TestV48ToolsKeepTheRequestsCancellationAndDeadline(t *testing.T) {
	for _, c := range []struct{ name, tool string }{
		{"connection_info", "connection_info"},
		{"search_mail", "search_mail"},
		{"read_message", "read_message"},
		{"get_attachment", "get_attachment"},
		{"search_mail 2026-07-28", "search_mail"},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld()
			entered := make(chan context.Context, 1)
			wait := func(ctx context.Context) error {
				entered <- ctx
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(5 * time.Second):
					return errors.New("not canceled")
				}
			}
			w.fake.InfoFunc = func(ctx context.Context, _ *oauth.Grant) (*contracts.VendorConnection, error) {
				return nil, wait(ctx)
			}
			w.fake.SearchFunc = func(ctx context.Context, _ *oauth.Grant, _ contracts.VendorSearch) (*contracts.VendorPage, connections.Release, error) {
				return nil, nil, wait(ctx)
			}
			w.fake.ReadFunc = func(ctx context.Context, _ *oauth.Grant, _ string) (*contracts.VendorMessage, connections.Release, error) {
				return nil, nil, wait(ctx)
			}
			w.fake.AttachmentFunc = func(ctx context.Context, _ *oauth.Grant, _ string, _ int) (*inbox.Attachment, connections.Release, error) {
				return nil, nil, wait(ctx)
			}
			args := map[string]string{"connection_info": `{}`, "search_mail": `{}`, "read_message": `{"id":"mbx_A.msg_1"}`, "get_attachment": `{"id":"mbx_A.msg_1","index":0}`}[c.tool]
			q := toolCall(1, c.tool, args)
			if strings.HasSuffix(c.name, protocolClaudeCode) {
				q = claudeCall(1, "tools/call", `"name":"`+c.tool+`","arguments":`+args)
			}
			if c.tool == "get_attachment" {
				q.token = tokenMCPAttached
			}
			deadline := time.Now().Add(time.Minute).Truncate(time.Second)
			ctx, cancel := context.WithDeadline(context.Background(), deadline)
			defer cancel()
			done := make(chan struct{})
			go func() {
				defer close(done)
				w.handler().ServeHTTP(httptest.NewRecorder(), q.http().WithContext(ctx))
			}()
			var got context.Context
			select {
			case got = <-entered:
			case <-time.After(5 * time.Second):
				t.Fatal("the service was not asked")
			}
			if d, ok := got.Deadline(); !ok || !d.Equal(deadline) {
				t.Errorf("the service's deadline is %v %v, the request's %v", d, ok, deadline)
			}
			cancel()
			select {
			case <-got.Done():
			case <-time.After(time.Second):
				t.Error("the service's work goes on after the request ended")
			}
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("the handler did not return")
			}
		})
	}
}

// pauseListener's connections write until left bytes have gone, then block
// the next large write as a peer that reads nothing does, until the write
// is interrupted (a write deadline that passed, or Close) or the peer reads
// again (unpause); it counts what reaches the wire after cut.
type pauseListener struct {
	net.Listener
	paused, resume chan struct{}

	mu          sync.Mutex
	left        int
	isCut       bool
	after       int
	pauseOnce   sync.Once
	unpauseOnce sync.Once
}

func (l *pauseListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return &pauseConn{Conn: c, l: l, interrupted: make(chan struct{})}, nil
}

func (l *pauseListener) unpause() { l.unpauseOnce.Do(func() { close(l.resume) }) }

func (l *pauseListener) cut() {
	l.mu.Lock()
	l.isCut = true
	l.mu.Unlock()
}

func (l *pauseListener) afterCut() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.after
}

type pauseConn struct {
	net.Conn
	l           *pauseListener
	interrupted chan struct{}
	stop        sync.Once
}

func (c *pauseConn) interrupt() { c.stop.Do(func() { close(c.interrupted) }) }

func (c *pauseConn) Write(p []byte) (int, error) {
	c.l.mu.Lock()
	pause := c.l.left <= 0 && len(p) > 1024
	c.l.left -= len(p)
	c.l.mu.Unlock()
	if pause {
		c.l.pauseOnce.Do(func() { close(c.l.paused) })
		select {
		case <-c.l.resume:
		case <-c.interrupted:
			return 0, os.ErrDeadlineExceeded
		}
	}
	select {
	case <-c.interrupted:
		return 0, os.ErrDeadlineExceeded
	default:
	}
	n, err := c.Conn.Write(p)
	c.l.mu.Lock()
	if c.l.isCut {
		c.l.after += n
	}
	c.l.mu.Unlock()
	return n, err
}

func (c *pauseConn) SetWriteDeadline(t time.Time) error {
	if !t.IsZero() && !t.After(time.Now()) {
		c.interrupt()
	}
	return c.Conn.SetWriteDeadline(t)
}

func (c *pauseConn) SetDeadline(t time.Time) error {
	if err := c.SetWriteDeadline(t); err != nil {
		return err
	}
	return c.SetReadDeadline(t)
}

func (c *pauseConn) Close() error {
	c.interrupt()
	return c.Conn.Close()
}
