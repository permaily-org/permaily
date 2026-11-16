package mcpapi

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strconv"
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
// every byte under its context, and the response aborted if that context
// ends before the last one left. Over HTTP/2 that needs the connection
// (ConnContext): without it the answer is refused before its lease starts.
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
	if r.ProtoMajor >= 2 && conn == nil {
		// A cut could not stop a frame already handed to the connection.
		rel.Finish(false)
		unavailable(w)
		return "no_abort", false
	}
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

// send writes the recorded answer under ctx, and the lease is read before
// every write and again before every flush: each chunk is written, and
// flushed, only while ctx lasts. When ctx ends during the transfer, the
// connection is closed (conn, ConnContext's) and the write deadline set to
// now: a write blocked on a client that reads nothing returns, over HTTP/1.1
// as over HTTP/2, where the stream's deadline alone leaves a frame already
// handed to the connection to leave when the client reads again. That abort
// runs before send returns, never on a writer its handler no longer has.
// sent says every byte was written and flushed; ended, when not "", why
// the transfer ended otherwise or as it finished: "cut" (ctx ended),
// "client_left" (ctx ended with req, the request's) or "write_failed".
func send(ctx, req context.Context, w http.ResponseWriter, conn net.Conn, rec *recorder) (sent bool, ended string) {
	rc := http.NewResponseController(w)
	aborted := make(chan struct{})
	why := "cut"
	stop := context.AfterFunc(ctx, func() {
		defer close(aborted)
		if req.Err() != nil {
			why = "client_left"
		}
		if conn != nil {
			// The socket's write first: a TLS Close waits for TLS's output
			// lock, which a KeyUpdate reply blocked on the socket holds, and
			// would hold the cut as long as the peer reads nothing.
			_ = conn.SetWriteDeadline(time.Now())
			_ = conn.Close()
		}
		_ = rc.SetWriteDeadline(time.Now())
	})
	defer func() {
		if !stop() {
			<-aborted // the abort ran: what it did is done
			ended = why
		}
	}()
	copyHeader(w.Header(), rec.header)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Length", strconv.Itoa(len(rec.body)))
	if ctx.Err() != nil {
		return false, ""
	}
	w.WriteHeader(rec.status)
	for off := 0; off < len(rec.body); off += sendChunk {
		if ctx.Err() != nil {
			return false, ""
		}
		end := min(off+sendChunk, len(rec.body))
		if _, err := w.Write(rec.body[off:end]); err != nil || ctx.Err() != nil {
			return false, "write_failed"
		}
		if err := rc.Flush(); err != nil {
			return false, "write_failed"
		}
	}
	return true, ""
}

type connKey struct{}

// ConnContext is the enclosing http.Server's ConnContext (the composition
// sets it): it keeps each connection in its requests' context, so that a
// lease that ends while its answer is sent closes the connection itself.
// Over HTTP/2 that is the only way to stop a frame already handed to the
// connection; it ends the connection's other streams too, which their
// clients send again. Without it, a leased answer over HTTP/2 is 503.
func ConnContext(ctx context.Context, c net.Conn) context.Context {
	return context.WithValue(ctx, connKey{}, c)
}

func copyHeader(dst, src http.Header) {
	for k, v := range src {
		dst[k] = append([]string(nil), v...)
	}
}
