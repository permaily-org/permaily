package decisions

import (
	"context"
	"errors"
	"time"

	"github.com/permaily-org/permaily/internal/decide"
	"github.com/permaily-org/permaily/pkg/attestation"
)

// The attempts of one call (ADR 0028, "Failures"): each holds one of the
// enclave's slots (decide.Concurrent), is reserved before it is sent and
// settled once when it ends; the service, not the client, tries again, up
// to decide.Retries times after an AttemptError with Retry, with full
// jitter from retryBase and the provider's Retry-After, never past the
// message's deadline, which the context carries.

// retryBase is where the full jitter between attempts starts, doubling
// with each attempt.
const retryBase = 200 * time.Millisecond

// called is what became of one call: its result, or why it has none.
type called struct {
	res *decide.Result
	// hold is set when the guard, the provider's limit or the key kept the
	// call from being sent: its atoms are held, not unknown.
	hold hold
	// changed is an answer that named another model (decide.ErrModelChanged).
	changed  bool
	attempts int
}

// call makes the attempts of one call, account's ("" for the canary).
// canary counts its attempts as the canary's.
func (s *Service) call(ctx context.Context, c decide.Call, account string, canary bool) called {
	est := estimate(c)
	var out called
	var retryAfter time.Duration
	for attempt := 0; ; attempt++ {
		if attempt > 0 {
			wait := s.jitter(retryBase << (attempt - 1))
			if retryAfter > 0 {
				wait = min(retryAfter, decide.MaxRetryAfter)
			}
			if d, ok := ctx.Deadline(); ok && time.Until(d) <= wait {
				s.countFailure(canary, attestation.ModelCallTimeout)
				return out
			}
			if err := s.sleep(ctx, wait); err != nil {
				s.countFailure(canary, attestation.ModelCallTimeout)
				return out
			}
		}
		k := s.keyState(ctx)
		if !k.set {
			out.hold = holdOff
			s.countFailure(canary, attestation.ModelCallHeld)
			return out
		}
		select {
		case s.slots <- struct{}{}:
		case <-ctx.Done():
			s.countFailure(canary, attestation.ModelCallTimeout)
			return out
		}
		r, h := s.reserve(ctx, est, account, k.fp)
		if h != holdNone {
			<-s.slots
			out.hold = h
			s.countFailure(canary, attestation.ModelCallHeld)
			return out
		}
		out.attempts++
		if canary {
			s.count(attestation.ModelCallCanary, 1)
		}
		res, err := s.model.Decide(ctx, c)
		<-s.slots
		if err == nil && (res == nil || len(res.Answers) != len(c.Statements)) {
			err = &decide.AttemptError{Err: decide.ErrInvalidAnswer, Sent: true}
		}
		if err == nil && res.Model != s.model.Name() {
			err = &decide.AttemptError{Err: decide.ErrModelChanged, Sent: true}
		}
		if err == nil {
			if res.Usage.Reported {
				r.settle(cost(res.Usage))
			} else {
				r.settle(est)
			}
			s.answered(r)
			if !canary {
				s.count(attestation.ModelCallAnswered, 1)
			}
			out.res = res
			return out
		}
		var ae *decide.AttemptError
		if !errors.As(err, &ae) {
			// Read as ErrUnavailable, sent, and not tried again.
			ae = &decide.AttemptError{Err: decide.ErrUnavailable, Sent: true}
		}
		if ae.Sent {
			r.settle(est)
		} else {
			r.settle(0)
		}
		switch {
		case errors.Is(ae.Err, decide.ErrKeyRefused):
			out.hold = holdOff
			if ae.Sent {
				s.keyRefused(ctx, k.fp)
				s.countFailure(canary, attestation.ModelCallKeyRefused)
			} else {
				s.countFailure(canary, attestation.ModelCallHeld)
			}
			return out
		case errors.Is(ae.Err, decide.ErrProviderLimit):
			out.hold = holdLimit
			s.providerLimit(ctx)
			s.countFailure(canary, attestation.ModelCallLimit)
			return out
		case errors.Is(ae.Err, decide.ErrRequest):
			s.countFailure(canary, attestation.ModelCallRequest)
			return out
		case errors.Is(ae.Err, decide.ErrInvalidAnswer):
			s.countFailure(canary, attestation.ModelCallInvalid)
			return out
		case errors.Is(ae.Err, decide.ErrModelChanged):
			out.changed = true
			s.countFailure(canary, attestation.ModelCallOtherModel)
			return out
		case ae.Retry && errors.Is(ae.Err, decide.ErrUnavailable) && attempt < decide.Retries && ctx.Err() == nil:
			retryAfter = ae.RetryAfter
			continue
		case ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded):
			s.countFailure(canary, attestation.ModelCallTimeout)
			return out
		default:
			s.countFailure(canary, attestation.ModelCallUnavailable)
			return out
		}
	}
}

// countFailure counts what became of a call; the canary's are counted as
// the canary's alone.
func (s *Service) countFailure(canary bool, kind string) {
	if !canary {
		s.count(kind, 1)
	}
}
