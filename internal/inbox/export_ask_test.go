package inbox

import "time"

// NewMatcherAsking is NewMatcherWith that asks at most asked messages a
// page (MatchMaxModelMessages), for the tests of asking the model (ADR
// 0028, A26).
func NewMatcherAsking(st ReadStore, eval Evaluate, mb MatchBounds, asked int) Matcher {
	m := NewMatcherWith(st, eval, mb).(*storeMatcher)
	if asked > 0 {
		m.bounds.asked = asked
	}
	return m
}

// NewMatcherAskWithin is NewMatcherWith whose asked messages each have
// deadline instead of decide.MessageDeadline, for the tests of a message's
// own deadline running out (A26).
func NewMatcherAskWithin(st ReadStore, eval Evaluate, mb MatchBounds, deadline time.Duration) Matcher {
	m := NewMatcherWith(st, eval, mb).(*storeMatcher)
	m.bounds.ask = deadline
	return m
}
