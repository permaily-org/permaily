package decisions

import (
	"github.com/permaily-org/permaily/internal/decide"
	"github.com/permaily-org/permaily/internal/query"
)

// Stand-ins for decide.Plan, decide.Cost and decide.Estimate, which ADR
// 0028's pull request 1 writes in internal/decide (plan.go, cost.go). They
// follow that record's definitions exactly ("The request", "Spend") so this
// pull request builds and is tested before pull request 1 merges; once it
// does, each becomes a call to its decide function and this file goes.

// plan is decide.Plan: a message's subject atoms in one call with the
// subject alone, its message atoms in one call with the subject and the
// text, none past decide.MaxStatementsPerCall, each atom once; and the atoms
// decided by rule because their field is empty (0), the message atoms only
// from a text that was read. Message atoms are in neither when hasText is
// false: they stay unknown.
func plan(atoms []query.Atom, subject, text string, hasText bool) ([]decide.Call, query.Answers) {
	byRule := query.Answers{}
	seen := map[query.Atom]bool{}
	var subjectAtoms, messageAtoms []query.Atom
	for _, a := range atoms {
		if seen[a] {
			continue
		}
		seen[a] = true
		switch decide.KindOf(a) {
		case decide.ContentSubject:
			if subject == "" {
				byRule[a] = 0
				continue
			}
			subjectAtoms = append(subjectAtoms, a)
		case decide.ContentMessage:
			if !hasText {
				continue
			}
			if subject == "" && text == "" {
				byRule[a] = 0
				continue
			}
			messageAtoms = append(messageAtoms, a)
		}
	}
	var calls []decide.Call
	add := func(c decide.Content, list []query.Atom) {
		for len(list) > 0 {
			n := min(len(list), decide.MaxStatementsPerCall)
			calls = append(calls, decide.Call{Content: c, Statements: list[:n:n]})
			list = list[n:]
		}
	}
	add(decide.Content{Kind: decide.ContentSubject, Subject: subject}, subjectAtoms)
	add(decide.Content{Kind: decide.ContentMessage, Subject: subject, Body: text}, messageAtoms)
	return calls, byRule
}

// estimateTokens is the upper bound of a call's input tokens the guard
// reserves for: 1,000, 400 a statement and one a byte of the content's
// JSON (ADR 0028, "Spend"; the re-measure's reported tokens were at most
// half of it).
func estimateTokens(c decide.Call) int64 {
	return 1000 + 400*int64(len(c.Statements)) + int64(len(decide.ContentJSON(c.Content)))
}

// estimate is decide.Estimate: what a call not sent yet may cost at most,
// in billionths of a US dollar, twice past decide.LongRequestTokens.
func estimate(c decide.Call) int64 {
	t := estimateTokens(c)
	nano := t * decide.NanoUSDPerInputToken
	if t > decide.LongRequestTokens {
		nano *= 2
	}
	return nano
}

// cost is decide.Cost: what a call's reported usage cost at the compiled
// prices, twice past decide.LongRequestTokens.
func cost(u decide.Usage) int64 {
	plain := max(u.InputTokens-u.CachedTokens-u.CacheWriteTokens, 0)
	nano := plain*decide.NanoUSDPerInputToken + u.CachedTokens*decide.NanoUSDPerCachedToken + u.CacheWriteTokens*decide.NanoUSDPerCacheWriteToken
	if u.InputTokens > decide.LongRequestTokens {
		nano *= 2
	}
	return nano
}
