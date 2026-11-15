package inbox

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
