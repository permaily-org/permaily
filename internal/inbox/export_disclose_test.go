package inbox

import "time"

// NewDiscloserWith is NewDiscloser with a page's bounds and the clock of
// the test's (zero fields keep the defaults; asked bounds the messages a
// page asks about, MatchMaxModelMessages).
func NewDiscloserWith(st ReadStore, eval Evaluate, d Decisions, mb MatchBounds, asked int) Discloser {
	s := NewDiscloser(st, eval, d).(*storeDiscloser)
	if mb.Read > 0 {
		s.bounds.read = mb.Read
	}
	if mb.TextReads > 0 {
		s.bounds.textReads = mb.TextReads
	}
	if mb.Soft > 0 {
		s.bounds.soft = mb.Soft
	}
	if mb.Now != nil {
		s.now = mb.Now
	}
	if asked > 0 {
		s.bounds.asked = asked
	}
	return s
}

// SettleWithin sets how long a page's settlement may take once its request
// ended.
func SettleWithin(d Discloser, within time.Duration) { d.(*storeDiscloser).settle = within }

// AskWithin sets each asked message's own deadline (decide.MessageDeadline).
func AskWithin(d Discloser, within time.Duration) { d.(*storeDiscloser).bounds.ask = within }
