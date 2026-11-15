package query

import (
	"strings"

	"github.com/permaily-org/permaily/internal/contracts"
)

// The relations between conditions that simplification and containment may
// use (ADR 0027, "Which relations between conditions it may use"). Each
// holds in three values: on every message, the value of the first condition
// is at most the second's, or the two are never both true, or never both
// false, whatever is unknown.

// implies reports a ⇒ b: on every message, a's value is at most b's (False <
// Unknown < True). For positive conditions, the pairs of the implication
// table; for negated ones, not a ⇒ not b exactly when b ⇒ a; between a
// positive and a negated condition, none. Both are canonical (positive for
// received and size).
func implies(a, b cond) bool {
	if a.Not != b.Not {
		return false
	}
	if a.Not {
		a, b = b, a
	}
	if a.Var == b.Var && a.Op == b.Op && a.Value == b.Value {
		return true // any condition implies itself; a model's, only itself
	}
	if isModel(a.Op) || isModel(b.Op) {
		return false
	}
	switch a.Var {
	case contracts.VarFromAddress:
		return b.Var == contracts.VarFromDomain && underDomain(a.domain, b.domain)
	case contracts.VarRecipientAddress:
		return b.Var == contracts.VarRecipientDomain && underDomain(a.domain, b.domain)
	case contracts.VarFromDomain, contracts.VarRecipientDomain, contracts.VarSignedBy:
		return b.Var == a.Var && underDomain(a.domain, b.domain)
	case contracts.VarSubject, contracts.VarBody:
		return b.Var == a.Var && b.Op == contracts.OpContains && strings.Contains(a.Value, b.Value)
	case contracts.VarAttachmentName:
		return (b.Var == a.Var && strings.Contains(a.Value, b.Value)) || b.Var == contracts.VarAttachments
	case contracts.VarAttachmentType:
		return (b.Var == a.Var && typeMatches(a.Value, b.Value)) || b.Var == contracts.VarAttachments
	case contracts.VarList:
		return b.Var == a.Var && a.Op == contracts.OpIs && b.Op == contracts.OpExists
	case contracts.VarReceived:
		if b.Var != a.Var || b.Op != a.Op {
			return false
		}
		if a.Op == contracts.OpSince {
			return !a.time.Before(b.time)
		}
		return !a.time.After(b.time)
	case contracts.VarSize:
		if b.Var != a.Var || b.Op != a.Op {
			return false
		}
		if a.Op == contracts.OpAtLeast {
			return a.size >= b.size
		}
		return a.size <= b.size
	}
	return false // attachments exists, mailbox: only themselves
}

// disjoint reports two conditions of a variable that is always known that
// no message satisfies both: received since s and before t with t ≤ s; two
// different mailboxes; a mailbox and its negation. Only these: for a
// variable that may be Unknown, two conditions may be Unknown together, and
// a rule acts on that.
func disjoint(a, b cond) bool {
	if a.Var != b.Var || !alwaysKnown(a.Var) {
		return false
	}
	if a.Var == contracts.VarReceived {
		if a.Op == b.Op {
			return false
		}
		since, before := a, b
		if a.Op == contracts.OpBefore {
			since, before = b, a
		}
		return !before.time.After(since.time)
	}
	switch {
	case !a.Not && !b.Not:
		return a.Value != b.Value
	case a.Not != b.Not:
		return a.Value == b.Value
	}
	return false
}

// covering reports two conditions of a variable that is always known of
// which every message satisfies one: received before t or since s with s ≤
// t; a mailbox or its negation.
func covering(a, b cond) bool {
	if a.Var != b.Var || !alwaysKnown(a.Var) {
		return false
	}
	if a.Var == contracts.VarReceived {
		if a.Op == b.Op {
			return false
		}
		since, before := a, b
		if a.Op == contracts.OpBefore {
			since, before = b, a
		}
		return !since.time.After(before.time)
	}
	return a.Not != b.Not && a.Value == b.Value
}
