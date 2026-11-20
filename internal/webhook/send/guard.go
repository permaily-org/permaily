package send

import (
	"context"
	"net"
	"sync"
	"time"

	"github.com/permaily-org/permaily/internal/webhook"
)

// guard serializes the request's Write with what stops a call before it is
// whole: a cut of the permit, and WriteBy. Before the Write is admitted, a
// stop closes the connection and the Write is never admitted (nothing of
// the request left); while it runs, a stop closes the connection under it,
// so the record is handed in part or not at all; once the whole record was
// handed, a stop changes nothing, since nothing can retract it.
type guard struct {
	mu      sync.Mutex
	raw     net.Conn
	why     webhook.Outcome // cut or too_late, once stopped before the request was whole
	writing bool            // the request's Write was admitted
	whole   bool            // its whole record was handed
	handed  int             // bytes handed to raw while writing
	short   bool            // a write to raw while writing handed less than it was given
}

// stop ends the call with why unless its request is whole already.
func (g *guard) stop(why webhook.Outcome) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.whole {
		return
	}
	if g.why == "" {
		g.why = why
	}
	if g.raw != nil {
		_ = g.raw.Close()
	}
}

// stopped is why the call was stopped, if it was.
func (g *guard) stopped() webhook.Outcome {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.why
}

// attach watches raw: what the TLS connection writes to it through the
// answer is counted while the request is written. False when the call was
// stopped already (raw is closed then).
func (g *guard) attach(raw net.Conn) (net.Conn, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.raw = raw
	if g.why != "" {
		_ = raw.Close()
		return nil, false
	}
	return &handoff{Conn: raw, g: g}, true
}

// admit lets the request's Write start: the outcome that forbids it (cut or
// too_late, the connection closed, nothing of the request left), or "". It
// reads the permit's context itself, under the lock stop takes, so a cut
// that returned before admission forbids it even before its callback ran.
func (g *guard) admit(ctx context.Context, now func() time.Time, writeBy time.Time) webhook.Outcome {
	g.mu.Lock()
	defer g.mu.Unlock()
	if o, done := ended(ctx); done && g.why == "" {
		g.why = o
	}
	if g.why == "" && !now().Before(writeBy) {
		g.why = webhook.OutcomeTooLate
	}
	if g.why != "" {
		if g.raw != nil {
			_ = g.raw.Close()
		}
		return g.why
	}
	g.writing = true
	return ""
}

// written ends the Write and says how much of the request it handed: the
// whole record when every write to the connection took all it was given,
// part of it when a write was cut short.
func (g *guard) written() Handed {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.writing = false
	switch {
	case g.handed == 0:
		return HandedNothing
	case g.short:
		return HandedPart
	}
	g.whole = true
	return HandedWhole
}

// handoff is the connection under TLS, counting what the request's Write
// hands to it.
type handoff struct {
	net.Conn
	g *guard
}

func (h *handoff) Write(p []byte) (int, error) {
	n, err := h.Conn.Write(p)
	h.g.mu.Lock()
	if h.g.writing {
		h.g.handed += n
		if n < len(p) {
			h.g.short = true
		}
	}
	h.g.mu.Unlock()
	return n, err
}
