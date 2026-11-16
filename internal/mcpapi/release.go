package mcpapi

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/permaily-org/permaily/internal/oauth"
)

// recorder is the SDK's writer for a tool call: the answer is made into
// memory, at most max bytes, and sent by answer under the lease. Nothing the
// SDK writes reaches the network on its own.
type recorder struct {
	header   http.Header
	status   int
	body     []byte
	max      int
	overflow bool
}

func newRecorder(max int) *recorder { return &recorder{header: http.Header{}, max: max} }

func (r *recorder) Header() http.Header { return r.header }

func (r *recorder) WriteHeader(status int) {
	if r.status == 0 {
		r.status = status
	}
}

func (r *recorder) Write(p []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	if r.overflow || len(r.body)+len(p) > r.max {
		r.overflow = true
		return 0, errAnswerTooLarge
	}
	r.body = append(r.body, p...)
	return len(p), nil
}

// Flush is a no-op: the answer leaves only through answer.
func (r *recorder) Flush() {}

var errAnswerTooLarge = errors.New("mcpapi: the answer is larger than AnswerMax")

// answer sends what the SDK made for a tool call and returns the request's
// outcome, and whether the response must be aborted. A step-up, a refused
// token or a storage that could not be read, asked by the tool, replace the
// SDK's answer; an answer with no lease (a refusal, connection_info) holds
// no mail and is sent as it is; an answer of mail is sent only under its
// lease (connections.Release): Start immediately before the first byte,
// every chunk admitted under its context, the response aborted if that
// context ends before the last one left, and Finish(true) only once it did.
// Only HTTP/1.1 gets here with a lease: ServeHTTP refuses a tool call over
// HTTP/2.
func (h *handler) answer(w http.ResponseWriter, r *http.Request, c *call, rec *recorder) (outcome string, abort bool) {
	rel, stepUp, status, outcome := c.taken()
	finish := func() {
		if rel != nil {
			rel.Finish(false)
		}
	}
	switch {
	case len(stepUp) > 0:
		finish()
		h.stepUp(w, c.grant, stepUp)
		return "step_up", false
	case status == http.StatusUnauthorized:
		finish()
		h.challenge(w, http.StatusUnauthorized, oauth.ErrInvalidToken, oauth.DefaultScopes)
		return outcome, false
	case status == http.StatusServiceUnavailable:
		finish()
		unavailable(w)
		return outcome, false
	case rec.overflow || rec.status == 0:
		finish()
		writeError(w, http.StatusInternalServerError, "internal", "internal error")
		return "internal", false
	}
	if outcome == "" {
		outcome = "answered"
	}
	if rel == nil {
		copyHeader(w.Header(), rec.header)
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(rec.status)
		_, _ = w.Write(rec.body)
		return outcome, false
	}
	conn, _ := r.Context().Value(connKey{}).(net.Conn)
	lctx, err := rel.Start(r.Context())
	if err != nil {
		rel.Finish(false)
		unavailable(w)
		return "too_late", false
	}
	sent, ended := send(lctx, r.Context(), w, conn, rec)
	rel.Finish(sent)
	if ended != "" {
		// The lease ended before the last byte left, or as it did, or a
		// write failed: the response is aborted, its connection closed with
		// nothing more.
		return ended, true
	}
	return outcome, false
}

// send writes the recorded answer under ctx (the lease's; req is the
// request's) over HTTP/1.1, in chunks of sendChunk, and returns once the
// transport handed its last byte, the header included, to the socket: the
// last Flush of HTTP/1.1 returns only then. Every chunk, and every flush,
// enters the transport only through its admission (admission), which the
// abort shares: once the lease is seen to have ended, by send or by the
// context's callback, the abort (the socket's write deadline, the
// connection's close, the response's deadline) has completed and no write
// enters after it. A cut that lands between a chunk's admission and its
// write lets that one chunk go (at most sendChunk bytes, with the header
// when it is the first), and nothing more. sent says every byte was handed
// to the socket; ended, when not "", why the transfer ended otherwise: "cut"
// (ctx ended), "client_left" (ctx ended with req) or "write_failed". The
// abort never runs once send returned, on a writer its handler no longer
// has.
func send(ctx, req context.Context, w http.ResponseWriter, conn net.Conn, rec *recorder) (sent bool, ended string) {
	a := &admission{ctx: ctx, req: req, conn: conn, rc: http.NewResponseController(w), done: make(chan struct{})}
	stop := context.AfterFunc(ctx, a.abort)
	defer func() {
		if !stop() {
			<-a.done // the callback's abort: what it did is done
		}
		if why, cut := a.aborted(); cut {
			sent, ended = false, why
		}
	}()
	if !a.admit() {
		return false, "" // not even the header goes
	}
	copyHeader(w.Header(), rec.header)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Length", strconv.Itoa(len(rec.body)))
	w.WriteHeader(rec.status) // HTTP/1.1 holds it for the first write
	for off := 0; ; off += sendChunk {
		chunk := rec.body[off:min(off+sendChunk, len(rec.body))]
		if len(chunk) > 0 {
			if !a.admit() {
				return false, ""
			}
			if _, err := w.Write(chunk); err != nil {
				return false, "write_failed"
			}
		}
		if !a.admit() {
			return false, "" // what the write left buffered is never flushed
		}
		if a.rc.Flush() != nil {
			return false, "write_failed"
		}
		if off+len(chunk) == len(rec.body) {
			return true, ""
		}
	}
}

// admission is one transfer's state, shared by its writer and its abort:
// open until the lease's context is seen to have ended, then aborted, and
// never open again. internal/httpapi has its own copy (ADR 0030's rows keep
// each adapter's writer its own).
type admission struct {
	ctx, req context.Context
	conn     net.Conn
	rc       *http.ResponseController

	once sync.Once
	done chan struct{} // closed when the abort completed

	mu  sync.Mutex
	cut bool
	why string
}

// admit says a write may enter the transport: the lease's context has not
// ended and the transfer was not aborted. Otherwise it aborts the transfer,
// returning once the abort completed, and false.
func (a *admission) admit() bool {
	a.mu.Lock()
	open := !a.cut && a.ctx.Err() == nil
	a.mu.Unlock()
	if !open {
		a.abort()
	}
	return open
}

// abort closes the admission and the connection, once; a second call
// returns when the first completed. The socket's write deadline goes first:
// a TLS Close waits for TLS's output lock, which a write blocked on the
// socket (a KeyUpdate's reply among them) holds, and would hold the cut as
// long as the peer reads nothing.
func (a *admission) abort() {
	a.once.Do(func() {
		defer close(a.done)
		a.mu.Lock()
		a.cut, a.why = true, "cut"
		if a.req.Err() != nil {
			a.why = "client_left"
		}
		a.mu.Unlock()
		if a.conn != nil {
			_ = a.conn.SetWriteDeadline(time.Now())
			_ = a.conn.Close()
		}
		_ = a.rc.SetWriteDeadline(time.Now())
	})
}

// aborted says whether the transfer was aborted, and why.
func (a *admission) aborted() (why string, cut bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.why, a.cut
}

type connKey struct{}

// ConnContext is the enclosing http.Server's ConnContext (the composition
// sets it): it keeps each connection in its requests' context, so that a
// lease that ends while its answer is sent closes the connection itself,
// the socket's deadline set first, and nothing the server still holds of
// the response leaves when it closes it. Without it the response's write
// deadline alone aborts it.
func ConnContext(ctx context.Context, c net.Conn) context.Context {
	return context.WithValue(ctx, connKey{}, c)
}

func copyHeader(dst, src http.Header) {
	for k, v := range src {
		dst[k] = append([]string(nil), v...)
	}
}
