// Package send is the sender of connections' webhooks (ADR 0039's pull
// request 1): a webhook.Sender that dials a vendor's host through the
// vendor egress (enclave.WebhookEgressDialer), does TLS 1.2 or later with
// ALPN "h2" alone, checked against the image's roots, and frames its one
// HTTP/2 request itself with golang.org/x/net/http2's framer and hpack. It
// uses no net/http transport, so nothing of it can follow a redirect, keep a
// connection, ask for compression, send a cookie or log what a receiver
// sends; it logs nothing at all.
//
// The request (HEADERS and its one DATA frame with END_STREAM) is written
// in a single Write of at most webhook.RequestWriteBytesMax, the most
// plaintext one TLS record carries, on a TLS connection whose dynamic
// record sizing is off, so it leaves in exactly one record (re-review N1:
// Go's default sizes the first records of a connection at about 1 KiB and
// would split it, an earlier record then whole and readable while a later
// one is cut). That Write is admitted only after the server's SETTINGS,
// only when the peer's windows and header list limit take the whole
// request, and only while the permit lives and WriteBy has not passed;
// admission and a cut are serialized (guard). A cut or WriteBy that comes
// while the Write blocks closes the connection: if part of the record was
// handed to it, the record is not whole and the peer opens none of it
// (HandedPart, cut or too_late). Once the whole record was handed, closing
// cannot retract it (HandedWhole): the call is never cut or too_late then,
// its answer is waited for within webhook.AttemptTimeout, and an answer
// lost is no_answer.
package send

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"sync"
	"syscall"
	"time"

	"github.com/permaily-org/permaily/internal/webhook"
)

// Handed is how much of a call's request was handed to the connection, the
// difference between "not written in full" and "nothing written".
type Handed int

const (
	// HandedNothing: no byte of the request's record left: what cut and
	// too_late say when nothing was admitted.
	HandedNothing Handed = iota
	// HandedPart: part of the request's one TLS record, never all of it, was
	// handed to the connection before it was closed: a record that is not
	// whole opens to nothing, so the vendor learns nothing of the request.
	HandedPart
	// HandedWhole: the request's whole record was handed to the
	// connection. Closing cannot retract it (a host that holds it can
	// deliver it later), so the outcome is the answer's, or no_answer.
	HandedWhole
)

// Result is one attempt: its outcome and how much of its request left.
type Result struct {
	Outcome webhook.Outcome
	Handed  Handed
}

// Outcomes are every outcome a call can end in, the keys Counts always has.
var Outcomes = []webhook.Outcome{
	webhook.OutcomeSent, webhook.OutcomeGone, webhook.OutcomeRedirect, webhook.OutcomeStatus,
	webhook.OutcomeNoAnswer, webhook.OutcomeRefused, webhook.OutcomeTooLate, webhook.OutcomeCut,
}

// Counts are this sender's calls since it was made: by outcome, every key
// present from the start, and how many of them handed part of their
// request's record and never all of it (HandedPart).
type Counts struct {
	Outcomes   map[webhook.Outcome]uint64
	HandedPart uint64
}

// Sender is the webhook.Sender of the image.
type Sender struct {
	dial  func(ctx context.Context, host string) (net.Conn, error)
	roots *x509.CertPool
	now   func() time.Time

	// timeout bounds a call from the dial to the answer's status
	// (webhook.AttemptTimeout); tests shorten it.
	timeout time.Duration
	// beforeAdmit, when set, runs just before the request's Write is
	// admitted: tests cut a permit there.
	beforeAdmit func()

	mu     sync.Mutex
	counts Counts
}

var _ webhook.Sender = (*Sender)(nil)

// New returns a sender that dials a webhook's host with dial (the vendor
// egress's tunnel to the host's port 443), checks its certificate against
// roots (the image's when nil) and measures WriteBy with now.
func New(dial func(ctx context.Context, host string) (net.Conn, error), roots *x509.CertPool, now func() time.Time) *Sender {
	if now == nil {
		now = time.Now
	}
	s := &Sender{dial: dial, roots: roots, now: now, timeout: webhook.AttemptTimeout}
	s.counts.Outcomes = map[webhook.Outcome]uint64{}
	for _, o := range Outcomes {
		s.counts.Outcomes[o] = 0
	}
	return s
}

// Send implements webhook.Sender.
func (s *Sender) Send(ctx context.Context, c webhook.Call) webhook.Outcome {
	return s.Attempt(ctx, c).Outcome
}

// Attempt calls the webhook once, as Send, and says how much of the request
// was handed to the connection.
func (s *Sender) Attempt(ctx context.Context, c webhook.Call) Result {
	r := s.attempt(ctx, c)
	s.mu.Lock()
	s.counts.Outcomes[r.Outcome]++
	if r.Handed == HandedPart {
		s.counts.HandedPart++
	}
	s.mu.Unlock()
	return r
}

// Counts are the calls made so far.
func (s *Sender) Counts() Counts {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := Counts{Outcomes: make(map[webhook.Outcome]uint64, len(s.counts.Outcomes)), HandedPart: s.counts.HandedPart}
	for o, n := range s.counts.Outcomes {
		c.Outcomes[o] = n
	}
	return c
}

// errNotHTTP2 refuses a receiver that did not negotiate HTTP/2.
var errNotHTTP2 = errors.New("send: the receiver did not negotiate HTTP/2")

// config is the TLS of a call to host: the image's roots, TLS 1.2 or later,
// ALPN h2 alone, and records of the full size from the first one, so the
// request's single Write is a single record.
func (s *Sender) config(host string) *tls.Config {
	return &tls.Config{
		ServerName:                  host,
		RootCAs:                     s.roots,
		MinVersion:                  tls.VersionTLS12,
		NextProtos:                  []string{"h2"},
		DynamicRecordSizingDisabled: true,
		VerifyConnection: func(cs tls.ConnectionState) error {
			if cs.NegotiatedProtocol != "h2" {
				return errNotHTTP2
			}
			return nil
		},
	}
}

func (s *Sender) attempt(ctx context.Context, c webhook.Call) Result {
	req, ok := frame(c)
	if !ok {
		return Result{Outcome: webhook.OutcomeRefused}
	}
	if o, done := ended(ctx); done {
		return Result{Outcome: o}
	}
	left := c.WriteBy.Sub(s.now())
	if c.WriteBy.IsZero() || left <= 0 {
		return Result{Outcome: webhook.OutcomeTooLate}
	}
	deadline := time.Now().Add(s.timeout)
	g := &guard{}
	defer context.AfterFunc(ctx, func() {
		o, _ := ended(ctx)
		g.stop(o)
	})()
	defer time.AfterFunc(left, func() { g.stop(webhook.OutcomeTooLate) }).Stop()
	// failed is the outcome of an attempt that ended before its request was
	// whole: the cut or WriteBy that ended it, else otherwise.
	failed := func(otherwise webhook.Outcome, handed Handed) Result {
		if o := g.stopped(); o != "" {
			return Result{Outcome: o, Handed: handed}
		}
		if o, done := ended(ctx); done {
			return Result{Outcome: o, Handed: handed}
		}
		if handed != HandedWhole && !s.now().Before(c.WriteBy) {
			return Result{Outcome: webhook.OutcomeTooLate, Handed: handed}
		}
		return Result{Outcome: otherwise, Handed: handed}
	}

	dialCtx, cancel := context.WithTimeout(ctx, left)
	raw, err := s.dial(dialCtx, req.host)
	cancel()
	if err != nil {
		return failed(webhook.OutcomeNoAnswer, HandedNothing)
	}
	defer raw.Close()
	conn, ok := g.attach(raw)
	if !ok {
		return failed(webhook.OutcomeNoAnswer, HandedNothing)
	}
	if err := raw.SetDeadline(deadline); err != nil {
		return failed(webhook.OutcomeNoAnswer, HandedNothing)
	}
	tc := tls.Client(conn, s.config(req.host))
	if err := tc.Handshake(); err != nil {
		return failed(handshakeOutcome(err), HandedNothing)
	}
	h := newConn(tc)
	if err := h.start(); err != nil {
		return failed(webhook.OutcomeNoAnswer, HandedNothing)
	}
	peer, err := h.settings()
	if err != nil {
		return failed(webhook.OutcomeNoAnswer, HandedNothing)
	}
	if err := h.ack(); err != nil {
		return failed(webhook.OutcomeNoAnswer, HandedNothing)
	}
	if !peer.takes(req) {
		return Result{Outcome: webhook.OutcomeRefused}
	}
	if s.beforeAdmit != nil {
		s.beforeAdmit()
	}
	if o := g.admit(ctx, s.now, c.WriteBy); o != "" {
		return Result{Outcome: o}
	}
	_, _ = tc.Write(req.frames)
	if handed := g.written(); handed != HandedWhole {
		return failed(webhook.OutcomeNoAnswer, handed)
	}
	return Result{Outcome: h.answer(), Handed: HandedWhole}
}

// ended is the outcome of a permit's context that ended: cut when it was
// cut (webhook.ErrCut) or cancelled, too_late when it ended at WriteBy.
func ended(ctx context.Context) (webhook.Outcome, bool) {
	if ctx.Err() == nil {
		return "", false
	}
	if errors.Is(context.Cause(ctx), context.DeadlineExceeded) {
		return webhook.OutcomeTooLate, true
	}
	return webhook.OutcomeCut, true
}

// handshakeOutcome is a failed handshake's: refused when TLS failed (an
// alert the receiver sent, a certificate that does not verify, no h2, not
// TLS at all), no_answer when the network did (not reached, reset, closed,
// timed out).
func handshakeOutcome(err error) webhook.Outcome {
	var op *net.OpError
	if errors.As(err, &op) && op.Op == "remote error" {
		return webhook.OutcomeRefused
	}
	var ne net.Error
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, net.ErrClosed) ||
		errors.Is(err, syscall.ECONNRESET) || errors.Is(err, io.ErrClosedPipe) || errors.As(err, &ne) {
		return webhook.OutcomeNoAnswer
	}
	return webhook.OutcomeRefused
}
