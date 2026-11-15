package decidetest

import (
	"context"
	"time"

	"github.com/permaily-org/permaily/internal/decide"
)

// Fake is a decide.Model that answers from a Table as the real client
// reads the played provider (Contract): one attempt a call, its failures
// the same AttemptErrors, its timeouts the same.
type Fake struct {
	tab *Table
	key decide.Credential
}

var _ decide.Model = (*Fake)(nil)

// NewFake is a model answering from tab. key is asked for each attempt as
// the real client asks it; nil is a key always set.
func NewFake(tab *Table, key decide.Credential) *Fake {
	return &Fake{tab: tab, key: key}
}

// Name is the model the fake answers as.
func (f *Fake) Name() string { return Name }

// Decide makes one attempt: what the real client does with what the played
// provider would answer.
func (f *Fake) Decide(ctx context.Context, call decide.Call) (*decide.Result, error) {
	if !sendable(call) {
		return nil, &decide.AttemptError{Err: decide.ErrRequest}
	}
	if ctx.Err() != nil {
		return nil, &decide.AttemptError{Err: decide.ErrUnavailable}
	}
	key, ok := "fake key", true
	if f.key != nil {
		key, ok = f.key(ctx)
	}
	if !ok || key == "" {
		return nil, &decide.AttemptError{Err: decide.ErrKeyRefused}
	}
	actx, cancel := context.WithTimeout(ctx, timeout(call))
	defer cancel()
	start := time.Now()
	o := f.tab.attempt(call, key)

	wait := o.delay
	if o.failure == Late {
		wait = time.Hour // until the attempt's timeout
	}
	if wait > 0 {
		t := time.NewTimer(wait)
		defer t.Stop()
		select {
		case <-actx.Done():
			return nil, unavailable(ctx, 0)
		case <-t.C:
		}
	}
	sent := func(err error) error { return &decide.AttemptError{Err: err, Sent: true} }
	switch o.failure {
	case Unavailable, SlowDown:
		return nil, unavailable(ctx, min(o.retryAfter, decide.MaxRetryAfter))
	case Drop, RefusedStream, ResetStream, GoAway:
		return nil, unavailable(ctx, 0)
	case KeyRefused:
		return nil, sent(decide.ErrKeyRefused)
	case ProviderLimit:
		return nil, sent(decide.ErrProviderLimit)
	case BadRequest, Redirect:
		return nil, sent(decide.ErrRequest)
	case Invalid:
		return nil, sent(decide.ErrInvalidAnswer)
	case OtherModel:
		return nil, sent(decide.ErrModelChanged)
	}
	answers := make([]decide.Answer, len(o.answers))
	for i, r := range o.answers {
		answers[i] = r.answer()
	}
	return &decide.Result{Answers: answers, Model: Name, Usage: o.usage, Took: time.Since(start)}, nil
}

// unavailable is no answer to an attempt that was sent: tried again only
// while the caller's context is alive.
func unavailable(ctx context.Context, after time.Duration) *decide.AttemptError {
	e := &decide.AttemptError{Err: decide.ErrUnavailable, Sent: true}
	if ctx.Err() == nil {
		e.Retry, e.RetryAfter = true, after
	}
	return e
}
