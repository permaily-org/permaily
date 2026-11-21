// Package rulewords turns a person's words into a rule's draft through the
// model (ADR 0040, "Plain words"): the prompt pinned in the image, the
// answer's schema and its check, pure (depguard's rulewords-is-pure).
//
// Writer, which services/decisions implements (Write, ADR 0040's pull
// request 3, through the spend guard and the Responses API's adapter
// internal/rulewords/openai) and the rules routes call (pull request 6), with
// rulewordstest.Fake for the routes' tests. Model is what the adapter
// implements: one attempt a call. prompt.go holds the developer message, the
// schema and the request's bytes, named by PromptVersion; check.go reads an
// answer into a draft.
package rulewords

import (
	"context"

	"github.com/permaily-org/permaily/internal/contracts"
)

// Writer sends one person's words to the model and answers the draft it
// proposed, as the schema bound it, before any check of the account's: the
// caller (the rules route) checks it with accountrules.Rules.Check, with
// scope ScopeAll and On true, and answers contracts.ErrRuleWordsUnusable
// when that check refuses it. Write keeps nothing of the words: not in a
// record, a receipt, a log line or a cache.
//
// Before it sends anything, Write reserves the call through the spend guard
// at its upper bound (the developer message, the schema and the words at
// the provider's input price, plus the most output at its output price), in
// the account's day share, and counts it against the account's words calls
// of the day in this enclave; after, it settles the reservation once, at
// what the answer reports, or at the bound when it cannot tell.
//
// Its errors are of a fixed set, which the route answers as they are:
// contracts.ErrModelUnavailable when nothing could be sent or nothing usable
// came back for a reason of the model's (no key, a refused key, a limit, the
// account's share, a failure, a timeout); contracts.ErrTooManyRequests past
// the account's words calls of the day; contracts.ErrRuleWordsUnusable for
// an answer outside the schema, and for words that are not 1 to
// contracts.MaxRuleWordsRunes characters of text (nothing sent, nothing
// counted); the context's own error.
type Writer interface {
	Write(ctx context.Context, account, words string) (*Written, error)
}

// Written is what the model proposed: the draft, within the schema and not
// yet checked, and the model and prompt version that wrote it (a
// RuleProposal's Model and Prompt).
type Written struct {
	Draft  contracts.RuleDraft
	Model  string
	Prompt string
}

// CallsPerDay bounds an account's words calls a day in one enclave, besides
// its share of the spend (ADR 0040, choice 19).
const CallsPerDay = 30

// Model is the Responses API's client (internal/rulewords/openai in the
// image): it sends Request(words) once and returns what came back. It never
// tries again by itself, so every transmission is reserved for before it is
// sent. Its errors are *decide.AttemptError, read as internal/decide's
// Model's are (Sent, the failures by errors.Is); a refusal, an answer cut
// at MaxOutputTokens or an answer whose text is outside the schema is an
// Answer, never an error.
type Model interface {
	Write(ctx context.Context, words string) (*Answer, error)
	// Name is the model asked, ModelName; an Answer naming another is
	// never used.
	Name() string
}

// Answer is what one call answered: the text of its message (the JSON the
// schema bound, which Read checks), or that the model refused, or that the
// answer was cut; the model that answered; and what it cost.
type Answer struct {
	Text       string
	Refused    bool
	Incomplete bool
	Model      string
	Usage      Usage
}

// Usage is what the provider counted for a call; Cost turns it into money.
// Reported is false when the answer said nothing of it, and the call is
// then charged its Estimate.
type Usage struct {
	InputTokens  int64
	CachedTokens int64
	OutputTokens int64
	Reported     bool
}
