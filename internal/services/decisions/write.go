package decisions

import (
	"context"
	"errors"
	"log/slog"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/decide"
	"github.com/permaily-org/permaily/internal/rulewords"
)

var _ rulewords.Writer = (*Service)(nil)

// Write sends one person's words to the words' model and answers the draft
// it proposed (rulewords.Writer; ADR 0040, "Plain words"). It sends the
// words and nothing else of the account: account names whose share of the
// spend and whose count of words calls the call is in, and is never sent.
//
// In order: words that rulewords.ValidWords refuses are
// contracts.ErrRuleWordsUnusable, with nothing sent or counted; the call is
// counted against the account's rulewords.CallsPerDay of the day (UTC) in
// this enclave, past which it is contracts.ErrTooManyRequests; with no
// words' model or no key it is contracts.ErrModelUnavailable; it takes one
// of the enclave's slots and is reserved through the spend guard at
// rulewords.Estimate in the account's share, as an attempt of Ask is, and
// is not sent when the guard holds it (contracts.ErrModelUnavailable). One
// attempt is made, never tried again; its reservation is settled once: at
// what the answer reports, at the estimate when it reported nothing or the
// attempt failed after it may have been sent, at nothing when it certainly
// was not. A refused key and the provider's limit stop the guard as Ask's
// do. An answer naming another model is contracts.ErrModelUnavailable; a
// refusal, an answer cut at rulewords.MaxOutputTokens or one that
// rulewords.Read refuses is contracts.ErrRuleWordsUnusable.
func (s *Service) Write(ctx context.Context, account, words string) (*rulewords.Written, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !rulewords.ValidWords(words) {
		return nil, contracts.ErrRuleWordsUnusable
	}
	if account == "" {
		// Every call is in an account's share: one with none is never sent.
		return nil, contracts.ErrModelUnavailable
	}
	if !s.wordsCall(account) {
		return nil, contracts.ErrTooManyRequests
	}
	if s.writer == nil {
		return nil, contracts.ErrModelUnavailable
	}
	k := s.keyState(ctx)
	if !k.set {
		return nil, contracts.ErrModelUnavailable
	}
	select {
	case s.slots <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	est := rulewords.Estimate(words)
	r, h := s.reserve(ctx, est, account, k.fp)
	if r == nil || h != holdNone {
		<-s.slots
		s.logKind(slog.LevelInfo, "decisions: a words call was held by the spend guard", "words_held")
		return nil, contracts.ErrModelUnavailable
	}
	ans, err := s.writer.Write(ctx, words)
	<-s.slots
	if err != nil {
		return nil, s.wordsFailed(ctx, r, est, k.fp, err)
	}
	if ans == nil || ans.Model != s.writer.Name() {
		r.settle(est)
		s.logKind(slog.LevelWarn, "decisions: a words answer named another model", "words_other_model")
		return nil, contracts.ErrModelUnavailable
	}
	if ans.Usage.Reported {
		r.settle(rulewords.Cost(ans.Usage))
	} else {
		r.settle(est)
	}
	s.answered(r)
	if ans.Refused || ans.Incomplete {
		s.logKind(slog.LevelInfo, "decisions: a words answer was refused or cut", "words_unusable")
		return nil, contracts.ErrRuleWordsUnusable
	}
	d, err := rulewords.Read(ans.Text)
	if err != nil {
		s.logKind(slog.LevelInfo, "decisions: a words answer was outside its schema", "words_unusable")
		return nil, contracts.ErrRuleWordsUnusable
	}
	return &rulewords.Written{Draft: d, Model: ans.Model, Prompt: rulewords.PromptVersion}, nil
}

// wordsFailed settles a failed attempt and says what it means: the
// context's own error once the caller gave up, contracts.ErrModelUnavailable
// otherwise. A refused key and the provider's limit stop the guard as an
// attempt of Ask does.
func (s *Service) wordsFailed(ctx context.Context, r *reservation, est int64, fp string, err error) error {
	var ae *decide.AttemptError
	if !errors.As(err, &ae) {
		ae = &decide.AttemptError{Err: decide.ErrUnavailable, Sent: true}
	}
	if ae.Sent {
		r.settle(est)
	} else {
		r.settle(0)
	}
	switch {
	case errors.Is(ae.Err, decide.ErrKeyRefused):
		if ae.Sent {
			s.keyRefused(ctx, fp)
		}
		s.logKind(slog.LevelWarn, "decisions: a words call failed", "words_key_refused")
	case errors.Is(ae.Err, decide.ErrProviderLimit):
		s.providerLimit(ctx)
		s.logKind(slog.LevelWarn, "decisions: a words call failed", "words_provider_limit")
	case ctx.Err() != nil:
		return ctx.Err()
	default:
		s.logKind(slog.LevelWarn, "decisions: a words call failed", "words_failed")
	}
	return contracts.ErrModelUnavailable
}

// wordsCall counts one words call of account's today, or says the account
// has made rulewords.CallsPerDay of them in this enclave.
func (s *Service) wordsCall(account string) bool {
	day := dayOf(s.now())
	s.wordsMu.Lock()
	defer s.wordsMu.Unlock()
	if s.wordsDay != day || s.words == nil {
		s.wordsDay, s.words = day, map[string]int{}
	}
	if s.words[account] >= rulewords.CallsPerDay {
		return false
	}
	s.words[account]++
	return true
}
