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
// outcome. A step-up, a refused token or a storage that could not be read,
// asked by the tool, replace the SDK's answer; an answer with no lease (a
// refusal, connection_info) holds no mail and is sent as it is; an answer
// of mail is sent only under its lease (connections.Release): Start
// immediately before the first byte, every byte under its context, and the
// response aborted if that context ends before the last one left.
func (h *handler) answer(w http.ResponseWriter, r *http.Request, c *call, rec *recorder) string {
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
		return "step_up"
	case status == http.StatusUnauthorized:
		finish()
		h.challenge(w, http.StatusUnauthorized, oauth.ErrInvalidToken, oauth.DefaultScopes)
		return outcome
	case status == http.StatusServiceUnavailable:
		finish()
		unavailable(w)
		return outcome
	case rec.overflow || rec.status == 0:
		finish()
		writeError(w, http.StatusInternalServerError, "internal", "internal error")
		return "internal"
	}
	if outcome == "" {
		outcome = "answered"
	}
	if rel == nil {
		copyHeader(w.Header(), rec.header)
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(rec.status)
		_, _ = w.Write(rec.body)
		return outcome
	}
	lctx, err := rel.Start(r.Context())
	if err != nil {
		rel.Finish(false)
		unavailable(w)
		return "too_late"
	}
	sent, cut := send(lctx, w, rec)
	rel.Finish(sent)
	if !sent || cut {
		// The lease ended before the last byte left, or as it did: the
		// response is aborted, its connection closed with nothing more.
		panic(http.ErrAbortHandler)
	}
	return outcome
}

// send writes the recorded answer under ctx. When ctx ends during a write,
// the write deadline is set to now, so a write blocked on a client that
// reads nothing returns, and nothing more is written. sent says every byte
// was written and flushed; cut says ctx ended, whether or not they had.
func send(ctx context.Context, w http.ResponseWriter, rec *recorder) (sent, cut bool) {
	rc := http.NewResponseController(w)
	stop := context.AfterFunc(ctx, func() { _ = rc.SetWriteDeadline(time.Now()) })
	defer func() {
		if !stop() {
			cut = true
		}
	}()
	copyHeader(w.Header(), rec.header)
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Length", strconv.Itoa(len(rec.body)))
	if ctx.Err() != nil {
		return false, true
	}
	w.WriteHeader(rec.status)
	for off := 0; off < len(rec.body); off += sendChunk {
		if ctx.Err() != nil {
			return false, true
		}
		end := min(off+sendChunk, len(rec.body))
		if _, err := w.Write(rec.body[off:end]); err != nil {
			return false, ctx.Err() != nil
		}
	}
	if err := rc.Flush(); err != nil {
		return false, ctx.Err() != nil
	}
	return true, false
}

func copyHeader(dst, src http.Header) {
	for k, v := range src {
		dst[k] = append([]string(nil), v...)
	}
}

// ConnContext is the abort hook of the enclosing server (not wired yet).
func ConnContext(ctx context.Context, _ net.Conn) context.Context { return ctx }
