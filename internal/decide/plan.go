package decide

import "github.com/permaily-org/permaily/internal/query"

// Plan is the calls a message's atoms make (ADR 0028, "Grouping"): its
// subject atoms in one call with the subject alone, its message atoms in
// one call with the subject and text, none past MaxStatementsPerCall, each
// atom once; and the atoms decided by rule, without a call, because their
// field is empty (0), established only from a text that was read. Message
// atoms are in neither when hasText is false (the text could not be read):
// they stay unknown.
//
// An atom a model does not decide (KindOf 0) is in neither. The subject
// call comes first; a group past MaxStatementsPerCall is split in order.
// byRule is nil when no atom is decided by rule. The rule's 0 is not
// something the model said, so it is never kept (ADR 0028, "Empty fields
// are decided by rule").
func Plan(atoms []query.Atom, subject, text string, hasText bool) (calls []Call, byRule query.Answers) {
	var onSubject, onMessage []query.Atom
	seen := make(map[query.Atom]bool, len(atoms))
	rule := func(a query.Atom) {
		if byRule == nil {
			byRule = query.Answers{}
		}
		byRule[a] = 0
	}
	for _, a := range atoms {
		if seen[a] {
			continue
		}
		seen[a] = true
		switch KindOf(a) {
		case ContentSubject:
			if subject == "" {
				rule(a)
				continue
			}
			onSubject = append(onSubject, a)
		case ContentMessage:
			switch {
			case !hasText:
			case subject == "" && text == "":
				rule(a)
			default:
				onMessage = append(onMessage, a)
			}
		}
	}
	calls = appendCalls(calls, Content{Kind: ContentSubject, Subject: subject}, onSubject)
	calls = appendCalls(calls, Content{Kind: ContentMessage, Subject: subject, Body: text}, onMessage)
	return calls, byRule
}

// appendCalls adds the calls that put atoms to the model about c, at most
// MaxStatementsPerCall statements each.
func appendCalls(calls []Call, c Content, atoms []query.Atom) []Call {
	for len(atoms) > 0 {
		n := min(len(atoms), MaxStatementsPerCall)
		calls = append(calls, Call{Content: c, Statements: atoms[:n:n]})
		atoms = atoms[n:]
	}
	return calls
}
