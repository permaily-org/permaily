package inbox

import (
	"context"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/query"
	"github.com/permaily-org/permaily/internal/redact"
	"github.com/permaily-org/permaily/internal/rules"
)

// Removing details (ADR 0040, "The order" and "Removing by meaning"; revised
// after its design review, findings 1 and 7). The seam between the
// discloser, which takes out what a person's rules remove, and the model,
// which says sentence by sentence what a removal's topics mention: committed
// with the design, so ADR 0040's pull request 4 (services/decisions
// implements SentenceAsker, and the decisions' blocks keep MeaningKept) and
// pull request 5 (the discloser) share one contract. Nothing implements
// SentenceAsker in the image yet, and nothing reads Disclosed.Removed.
//
// Every removal is decided and applied on one View of the message
// (redact.NewView of its subject and its text as MessageText gives it),
// whatever parts the connection reads and whatever other rules apply: a
// meaning decision numbers the sentences of that View's SentenceMap, so the
// same decision means the same sentences for every connection and next to
// every other rule. The removals of every rule that applies (whole parts,
// fixed shapes, sentences) are spans of that View, joined by redact.Union,
// and only then are the connection's parts cut from what is left.

// SentenceAsker asks the model, for one message, which of its sentences
// mention any of a removal's topics. It asks within the spend guard and the
// account's share of it, as Asker does; it keeps nothing itself: the
// decision that uses its answers is the record (ADR 0040, "Decisions that
// cannot be undone"). An error is the request's, and only ever before the
// model was asked anything; a failure after that is an answer missing from
// SentencesAsked, which the decision then counts as a sentence to remove.
type SentenceAsker interface {
	AskSentences(ctx context.Context, q *SentenceQuestion) (*SentencesAsked, error)
}

// SentenceQuestion is all the model is given of one message for a removal:
// the sentences of its View's SentenceMap, in order (the subject's first,
// whichever parts the connection reads), each with every piece of every
// fixed shape kind masked (MaskedSentences), and the topics. Never a sender,
// a recipient, an attachment, an identifier, nor a piece a fixed shape
// matches.
type SentenceQuestion struct {
	// Account is whose share of the spend guard the asking is counted in.
	Account   string
	Sentences []string
	Topics    []query.Atom
}

// MaskedSentences is the text of each sentence of m as the model is given
// it: every piece of every one of contracts.ShapeKinds within it replaced by
// contracts.RemovedMarker, whatever the rules remove, so what OpenAI sees
// does not depend on which rules apply (ADR 0040, finding 1).
func MaskedSentences(r redact.Redactor, v redact.View, m redact.SentenceMap) []string {
	var pieces []redact.Span
	for _, p := range r.Shapes(v.Text, contracts.ShapeKinds) {
		pieces = append(pieces, p.Span)
	}
	out := make([]string, len(m.Sentences))
	for i, s := range m.Sentences {
		sv := redact.View{Text: v.Text, Subject: redact.Span{Start: s.Start, End: s.Start}, Body: s}
		_, out[i] = redact.Join(sv, pieces)
	}
	return out
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

// MeaningKept is what a meaning decision (rules.MeaningRef) keeps when it
// is settled Cleared: the digest of the SentenceMap its sentences are
// numbered in (redact.SentenceMap.Digest), and which of them its topics
// kept. It is bound to that map: For applies it only to the same View split
// the same way, and anything else removes the whole text, never an old
// bitmap read against other sentences (ADR 0040, finding 1).
type MeaningKept struct {
	Map  string
	Kept []bool
}

// MeaningKeptBytesMax bounds Encode's answer and the map's digest together:
// what a block reserves for a meaning decision's settlement beside its
// evidence (pull request 4 counts it in the Pending's room).
const MeaningKeptBytesMax = 64 + 4 + redact.MeaningSentencesMax/4

// ErrMeaningNotKept refuses a Settlement with Meaning, until ADR 0040's pull
// request 4 keeps it in the blocks.
var ErrMeaningNotKept = errors.New("inbox: a meaning decision's sentences are not kept yet")

// Encode is Kept as its count, ":" and a bitmap in hexadecimal, sentence 0
// the first byte's highest bit: what the blocks keep.
func (m *MeaningKept) Encode() string {
	b := make([]byte, (len(m.Kept)+7)/8)
	for i, k := range m.Kept {
		if k {
			b[i/8] |= 0x80 >> (i % 8)
		}
	}
	return strconv.Itoa(len(m.Kept)) + ":" + hex.EncodeToString(b)
}

// DecodeMeaningKept is Encode read back with its map's digest: an error for
// anything Encode would not write (a count past redact.MeaningSentencesMax,
// a bitmap of another length, a bit set past the count).
func DecodeMeaningKept(mapDigest, enc string) (*MeaningKept, error) {
	bad := errors.New("inbox: not a meaning decision's sentences")
	count, bits, ok := strings.Cut(enc, ":")
	n, err := strconv.Atoi(count)
	if !ok || err != nil || n < 0 || n > redact.MeaningSentencesMax || strconv.Itoa(n) != count || mapDigest == "" {
		return nil, bad
	}
	b, err := hex.DecodeString(bits)
	if err != nil || len(b) != (n+7)/8 || hex.EncodeToString(b) != bits {
		return nil, bad
	}
	m := &MeaningKept{Map: mapDigest, Kept: make([]bool, n)}
	for i := range b {
		for j := range 8 {
			set := b[i]&(0x80>>j) != 0
			if i*8+j >= n {
				if set {
					return nil, bad
				}
				continue
			}
			m.Kept[i*8+j] = set
		}
	}
	return m, nil
}

// For is which sentences of v, split as sm, the decision kept: only when it
// was made on that same map (its digest and its count); otherwise false,
// and the caller removes the whole text (and the subject).
func (m *MeaningKept) For(v redact.View, sm redact.SentenceMap) ([]bool, bool) {
	if m == nil || len(m.Kept) != len(sm.Sentences) || m.Map != sm.Digest(v) {
		return nil, false
	}
	return m.Kept, true
}

// MeaningSpans is what a meaning decision removes from v, as spans of its
// Text: every sentence of sm not kept, and everything from sm.Cut on; with
// kept nil (no decision, or one For refused), the subject and the text
// whole.
func MeaningSpans(v redact.View, sm redact.SentenceMap, kept []bool) []redact.Span {
	if kept == nil || len(kept) != len(sm.Sentences) {
		return []redact.Span{v.Subject, v.Body}
	}
	var out []redact.Span
	for i, s := range sm.Sentences {
		if !kept[i] {
			out = append(out, s)
		}
	}
	if sm.Cut < len(v.Text) {
		out = append(out, redact.Span{Start: sm.Cut, End: len(v.Text)})
	}
	return out
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
