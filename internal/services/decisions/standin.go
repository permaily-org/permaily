package decisions

import (
	"github.com/permaily-org/permaily/internal/decide"
	"github.com/permaily-org/permaily/internal/query"
)

// Stand-ins for decide.Plan, decide.Cost and decide.Estimate, which ADR
// 0028's pull request 1 writes in internal/decide (plan.go, cost.go). They
// are copies of that pull request's definitions, edges included (Cost
// bounds each count to [0, 1<<40]; Plan's rule answers are nil when there
// are none; TestStandInsMatchPullRequest1), so this pull request builds and
// is tested before pull request 1 merges; once it does, plan, cost and
// estimate become decide.Plan, decide.Cost and decide.Estimate, with the
// same arguments, and this file goes.

// plan is decide.Plan: a message's subject atoms in one call with the
// subject alone, its message atoms in one call with the subject and the
// text, none past decide.MaxStatementsPerCall, each atom once; and the atoms
// decided by rule because their field is empty (0), the message atoms only
// from a text that was read. Message atoms are in neither when hasText is
// false: they stay unknown.
func plan(atoms []query.Atom, subject, text string, hasText bool) ([]decide.Call, query.Answers) {
	var byRule query.Answers
	rule := func(a query.Atom) {
		if byRule == nil {
			byRule = query.Answers{}
		}
		byRule[a] = 0
	}
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
				rule(a)
				continue
			}
			subjectAtoms = append(subjectAtoms, a)
		case decide.ContentMessage:
			if !hasText {
				continue
			}
			if subject == "" && text == "" {
				rule(a)
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
// in billionths of a US dollar: the cost of its estimated input tokens.
func estimate(c decide.Call) int64 {
	return cost(decide.Usage{InputTokens: estimateTokens(c)})
}

// maxTokens bounds a count of tokens, so that no usage reported overflows
// what it costs (decide's maxTokens).
const maxTokens = 1 << 40

// cost is decide.Cost: what a call's reported usage cost at the compiled
// prices, twice past decide.LongRequestTokens. Each count is first bounded
// to [0, maxTokens], so a count below zero is zero and none overflows.
func cost(u decide.Usage) int64 {
	in := tokens(u.InputTokens)
	cached := tokens(u.CachedTokens)
	written := tokens(u.CacheWriteTokens)
	plain := max(in-cached-written, 0)
	nano := plain*decide.NanoUSDPerInputToken + cached*decide.NanoUSDPerCachedToken + written*decide.NanoUSDPerCacheWriteToken
	if in > decide.LongRequestTokens {
		nano *= 2
	}
	return nano
}

func tokens(n int64) int64 { return min(max(n, 0), maxTokens) }
