// Package rulewords turns a person's words into a rule's draft through the
// model (ADR 0040, "Plain words"): the prompt pinned in the image, the
// answer's schema and its check, pure (depguard's rulewords-is-pure).
//
// This file is the seam, committed with the design after its review
// (finding 7): Writer, which services/decisions implements (Write, ADR
// 0040's pull request 3, through the spend guard and the Responses API's
// adapter internal/rulewords/openai) and the rules routes call (pull request
// 6), with rulewordstest.Fake for the routes' tests. Pull request 3 writes
// the prompt, its version and the schema here.
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
// an answer outside the schema; the context's own error.
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
