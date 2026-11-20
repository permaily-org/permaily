package inbox

import (
	"context"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/query"
	"github.com/permaily-org/permaily/internal/rules"
)

// Removing details (ADR 0040, "Removing details"). The seam between the
// discloser, which takes out what a person's rule removes, and the model,
// which says sentence by sentence what a removal's topics mention:
// committed with the design, so ADR 0040's pull request 4 (services/decisions
// implements SentenceAsker, and the decisions' blocks keep which sentences a
// decision kept) and pull request 5 (the discloser) are written at once.
// Nothing implements SentenceAsker yet, and nothing reads Disclosed.Removed.

// SentenceAsker asks the model, for one message, which of its sentences
// mention any of a removal's topics. It asks within the spend guard and the
// account's share of it, as Asker does; it keeps nothing itself: the
// decision that uses its answers is the record (ADR 0040, "Holds that
// cannot be undone, for removals"). An error is the request's, and only ever
// before the model was asked anything; a failure after that is an answer
// missing from SentencesAsked, which the decision then counts as a sentence
// to remove.
type SentenceAsker interface {
	AskSentences(ctx context.Context, q *SentenceQuestion) (*SentencesAsked, error)
}

// SentenceQuestion is all the model is given of one message for a removal:
// the sentences, as the text left by the fixed shapes was split
// (redact.Sentences, at most redact.MeaningSentencesMax), the subject first
// when the connection reads it, and the topics. Never a sender, a recipient,
// an attachment or an identifier.
type SentenceQuestion struct {
	// Account is whose share of the spend guard the asking is counted in.
	Account   string
	Sentences []string
	Topics    []query.Atom
}

// SentenceTopic names one statement: sentence Sentence (its index in
// SentenceQuestion.Sentences) mentions topic Topic (its index in Topics).
type SentenceTopic struct {
	Sentence int
	Topic    int
}

// SentencesAsked is what AskSentences got: a probability for every
// statement answered, and those the model refused. A statement in neither
// is unknown (the guard held it, a call failed, or the deadline came).
// Evidence is the one ask's model, prompt version, when and the canary's
// days; Outcome says why statements were not asked, as Asked.Outcome does.
type SentencesAsked struct {
	Answers  map[SentenceTopic]float64
	Refused  map[SentenceTopic]bool
	Evidence Evidence
	Outcome  AskOutcome
	Called   int
}

// KeptSentences is which sentences a removal keeps: a sentence is kept only
// when the model answered below the rule's band for every topic
// (query.Classify with query.ForRule is False); a refusal, an answer in the
// band of doubt or above, or no answer removes it. A nil asked keeps none.
// Rollback resistance is not here but in the decision that records what it
// kept, once (ADR 0040).
func KeptSentences(asked *SentencesAsked, sentences, topics int) []bool {
	kept := make([]bool, sentences)
	if asked == nil {
		return kept
	}
	for s := range sentences {
		keep := topics > 0
		for t := range topics {
			st := SentenceTopic{Sentence: s, Topic: t}
			p, ok := asked.Answers[st]
			if !ok || asked.Refused[st] || query.Classify(p, query.ForRule) != query.False {
				keep = false
				break
			}
		}
		kept[s] = keep
	}
	return kept
}

// RemovedBy is what one rule removed from a message that left, for the
// record (receipts' "what a rule removed", ADR 0019): the summary, never the
// content, and the model's answers the rule's decisions rested on, as their
// records keep them (for a removal by meaning, the highest probability among
// the sentences kept, for each topic: the closest call).
type RemovedBy struct {
	Rule    rules.Ref
	Removed contracts.RemovedSummary
	Decided []Decided
}
