// Package rulewordstest plays rulewords.Writer for the tests of the rules
// routes (ADR 0040's pull request 6, written beside pull request 3). Never
// imported outside tests (depguard).
package rulewordstest

import (
	"context"
	"sync"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/rulewords"
)

// Fake answers Answer's draft for every words, or Err, and counts calls per
// account, refusing past rulewords.CallsPerDay as Write does.
type Fake struct {
	mu sync.Mutex
	// Answer is the draft proposed for words (a hold on everything when nil).
	Answer func(words string) contracts.RuleDraft
	// Err, when set, is every call's answer (contracts.ErrModelUnavailable,
	// contracts.ErrRuleWordsUnusable).
	Err error
	// Calls counts the calls by account; nothing of the words is kept.
	Calls map[string]int
}

var _ rulewords.Writer = (*Fake)(nil)

// Write implements rulewords.Writer.
func (f *Fake) Write(ctx context.Context, account, words string) (*rulewords.Written, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if f.Calls == nil {
		f.Calls = map[string]int{}
	}
	if f.Calls[account] >= rulewords.CallsPerDay {
		return nil, contracts.ErrTooManyRequests
	}
	f.Calls[account]++
	if f.Err != nil {
		return nil, f.Err
	}
	d := contracts.RuleDraft{Name: "From words", Effect: contracts.EffectHold, On: true, Scope: contracts.RuleScope{To: contracts.ScopeAll},
		Criteria: []contracts.Formula{{V: contracts.FormulaVersion, All: []contracts.Group{}}}}
	if f.Answer != nil {
		d = f.Answer(words)
	}
	return &rulewords.Written{Draft: d, Model: "played", Prompt: "w0-played"}, nil
}
