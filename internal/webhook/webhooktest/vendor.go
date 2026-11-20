// Package webhooktest is the stand-ins of a webhook's two ends (ADR 0039):
// Vendor, a played vendor's receiver that verifies every notice as a vendor
// must, and Sender, a webhook.Sender a test scripts. Never imported outside
// tests (depguard).
package webhooktest

import (
	"context"
	"crypto/ed25519"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/webhook"
)

// Vendor is a played vendor's webhook: an http.Handler that does what ADR
// 0039 asks every vendor to do with a call (a POST, its timestamp and its
// "v2a" signature for Endpoint, the subscription it was given, verified
// against Keys, its body opened for Audience, each notice's identifier
// taken once and kept DedupFor), keeps what it took, and answers Status
// (200 when zero) to every call it took; a call it refuses is 400. Taking,
// when set, is called with each notice it takes, before it answers: what a
// vendor's fetch does then (ADR 0039's K37).
type Vendor struct {
	Keys     []ed25519.PublicKey
	Endpoint string
	Audience string
	Status   int
	Now      func() time.Time
	// DedupFor is how long it keeps an identifier after it took it:
	// webhook.DedupKeepFor when zero, which a vendor must; shorter only to
	// show what a vendor that forgets sooner does (it takes a retry twice).
	DedupFor time.Duration
	Taking   func(n contracts.WebhookNotice)

	mu      sync.Mutex
	taken   []contracts.WebhookNotice
	seen    map[string]time.Time
	refused int
	calls   int
}

func (v *Vendor) now() time.Time {
	if v.Now != nil {
		return v.Now()
	}
	return time.Now()
}

// ServeHTTP implements http.Handler.
func (v *Vendor) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	n, status := v.take(r)
	if n != nil && v.Taking != nil {
		v.Taking(*n)
	}
	w.WriteHeader(status)
}

func (v *Vendor) take(r *http.Request) (*contracts.WebhookNotice, int) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.calls++
	now := v.now()
	body, err := io.ReadAll(io.LimitReader(r.Body, webhook.NoticeBytesMax+1))
	id := r.Header.Get(contracts.WebhookHeaderID)
	if err != nil || r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" || r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" ||
		webhook.Verify(v.Keys, v.Endpoint, id, r.Header.Get(contracts.WebhookHeaderTimestamp), r.Header.Get(contracts.WebhookHeaderSignature), body, now) != nil {
		v.refused++
		return nil, http.StatusBadRequest
	}
	n, err := webhook.Open(body, id, v.Audience, now)
	if err != nil {
		v.refused++
		return nil, http.StatusBadRequest
	}
	keep := v.DedupFor
	if keep == 0 {
		keep = webhook.DedupKeepFor
	}
	if v.seen == nil {
		v.seen = map[string]time.Time{}
	}
	for seen, at := range v.seen {
		if now.Sub(at) > keep {
			delete(v.seen, seen)
		}
	}
	status := v.Status
	if status == 0 {
		status = http.StatusOK
	}
	if _, taken := v.seen[id]; taken {
		return nil, status
	}
	v.seen[id] = now
	v.taken = append(v.taken, *n)
	return n, status
}

// Taken are the notices it took, in order (one twice only when it forgot
// it, DedupFor); Refused how many calls it refused; Calls how many it was
// made.
func (v *Vendor) Taken() []contracts.WebhookNotice {
	v.mu.Lock()
	defer v.mu.Unlock()
	return append([]contracts.WebhookNotice(nil), v.taken...)
}

func (v *Vendor) Refused() int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.refused
}

func (v *Vendor) Calls() int {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.calls
}

// Sender is a webhook.Sender a test scripts: it keeps every call and
// answers Outcomes in turn (the last one again once they run out; sent when
// none).
type Sender struct {
	Outcomes []webhook.Outcome

	mu    sync.Mutex
	calls []webhook.Call
}

var _ webhook.Sender = (*Sender)(nil)

// Send implements webhook.Sender.
func (s *Sender) Send(_ context.Context, c webhook.Call) webhook.Outcome {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, c)
	if len(s.Outcomes) == 0 {
		return webhook.OutcomeSent
	}
	o := s.Outcomes[0]
	if len(s.Outcomes) > 1 {
		s.Outcomes = s.Outcomes[1:]
	}
	return o
}

// Calls are the calls it was made, in order.
func (s *Sender) Calls() []webhook.Call {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]webhook.Call(nil), s.calls...)
}
