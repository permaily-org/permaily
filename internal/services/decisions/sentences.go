package decisions

import (
	"context"
	"errors"
	"log/slog"
	"sync"

	"github.com/permaily-org/permaily/internal/decide"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/pkg/attestation"
)

// Asking sentence by sentence for a removal by meaning (ADR 0040, "What the
// model sees", "Cost bounds and the spend guard"): the sentences of one
// message's view and a removal's topics, one statement per sentence and
// topic, in calls of at most decide.MaxStatementsPerCall, each with every
// sentence, under decide.SentencesPromptVersion; every attempt reserved
// before it is sent, in the account's share, as Ask's are, and the calls
// sent at once within decide.MessageDeadline. Nothing is kept here: no
// answer is kept per statement, since the meaning decision that uses them
// is the record (ADR 0040, "Options considered", kept answers for each
// sentence statement).

// errSentences refuses a question that must not be asked: no account to
// count it in, more sentences than decide.MaxSentences, more topics than
// decide.MaxSentenceTopics, or a topic that is not a removal's. Nothing is
// asked; the discloser removes the whole subject and text.
var errSentences = errors.New("decisions: a removal's question that cannot be asked")

var _ inbox.SentenceAsker = (*Service)(nil)

// AskSentences implements inbox.SentenceAsker. Its error is only ever before
// anything was asked (a nil or refused question); what a call that failed,
// the guard held or the deadline ended did not answer is missing from the
// answers, and Outcome says why when the guard held it.
func (s *Service) AskSentences(ctx context.Context, q *inbox.SentenceQuestion) (*inbox.SentencesAsked, error) {
	if q == nil {
		return nil, errNoQuestion
	}
	calls, ok := decide.PlanSentences(q.Sentences, q.Topics)
	if !ok || q.Account == "" {
		s.logKind(slog.LevelWarn, "decisions: a removal's question cannot be asked; nothing is sent", "sentences_refused")
		return nil, errSentences
	}
	out := &inbox.SentencesAsked{Answers: map[inbox.SentenceTopic]float64{}, Refused: map[inbox.SentenceTopic]bool{}, Outcome: inbox.AskAnswered}
	if len(calls) == 0 {
		return out, nil
	}
	ctx, cancel := context.WithTimeout(ctx, decide.MessageDeadline)
	defer cancel()
	k := s.keyState(ctx)
	if s.model == nil || !k.set {
		out.Outcome = inbox.AskOff
		return out, nil
	}
	if h := s.held(k.fp); h != holdNone {
		out.Outcome = outcomeAfter(out.Outcome, h)
		return out, nil
	}
	results := make([]called, len(calls))
	var wg sync.WaitGroup
	for i, c := range calls {
		wg.Go(func() { results[i] = s.call(ctx, c, q.Account, false) })
	}
	wg.Wait()
	checked, change := s.canaryDays()
	out.Evidence = inbox.Evidence{Model: s.model.Name(), Prompt: decide.SentencesPromptVersion, At: s.now(), CanaryChecked: checked, CanaryChange: change}
	for i, r := range results {
		out.Called += r.attempts
		out.Outcome = outcomeAfter(out.Outcome, r.hold)
		if r.res == nil {
			continue
		}
		for j, ans := range r.res.Answers {
			on := calls[i].On[j]
			st := inbox.SentenceTopic{Sentence: on.Sentence, Topic: on.Topic}
			switch {
			case ans.Refused:
				out.Refused[st] = true
				s.count(attestation.ModelCallRefusal, 1)
			case ans.OK:
				out.Answers[st] = ans.P
			}
		}
	}
	return out, nil
}

// outcomeAfter is an ask's outcome once a call was held for h, as Ask's
// holdAll says it: off before limit before answered.
func outcomeAfter(o inbox.AskOutcome, h hold) inbox.AskOutcome {
	switch {
	case h == holdOff:
		return inbox.AskOff
	case h == holdLimit && o != inbox.AskOff:
		return inbox.AskLimit
	}
	return o
}
