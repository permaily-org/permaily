package disclosetest

import (
	"context"
	"slices"
	"sync"

	"github.com/permaily-org/permaily/internal/inbox"
)

// Sentences plays inbox.SentenceAsker (ADR 0040) for the discloser's tests,
// written beside pull request 4's: it answers P for each statement it is
// asked, keeps every question as it was given (what the model would have
// seen, U20), and asks nothing with Err set.
type Sentences struct {
	mu sync.Mutex
	// P answers sentence s about topic t: a probability, or ok false for a
	// statement left unanswered (a call that failed). Nil answers 0.01.
	P func(sentence string, topic int) (p float64, ok bool)
	// Err, when set, is every call's answer, nothing asked.
	Err error
	// Asked are the questions, in order.
	Asked []inbox.SentenceQuestion
}

var _ inbox.SentenceAsker = (*Sentences)(nil)

// AskSentences implements inbox.SentenceAsker.
func (s *Sentences) AskSentences(ctx context.Context, q *inbox.SentenceQuestion) (*inbox.SentencesAsked, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.Err != nil {
		return nil, s.Err
	}
	s.Asked = append(s.Asked, inbox.SentenceQuestion{Account: q.Account, Sentences: slices.Clone(q.Sentences), Topics: slices.Clone(q.Topics)})
	out := &inbox.SentencesAsked{Answers: map[inbox.SentenceTopic]float64{}, Refused: map[inbox.SentenceTopic]bool{}, Outcome: inbox.AskAnswered}
	for i, text := range q.Sentences {
		for t := range q.Topics {
			p, ok := 0.01, true
			if s.P != nil {
				p, ok = s.P(text, t)
			}
			if ok {
				out.Answers[inbox.SentenceTopic{Sentence: i, Topic: t}] = p
			}
		}
	}
	if len(q.Sentences) > 0 && len(q.Topics) > 0 {
		out.Called = 1
	}
	return out, nil
}
