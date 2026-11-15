package inbox

import "time"

// MatchBounds are a page of matches' bounds and clock, set low by the tests
// of Matcher (ADR 0027, Q28: MatchMaxRead 7, two raw reads). Zero fields
// keep the defaults.
type MatchBounds struct {
	Read, TextReads, Undecided int
	Soft                       time.Duration
	Now                        func() time.Time
}

// NewMatcherWith is NewMatcher with bounds of the test's.
func NewMatcherWith(st ReadStore, eval Evaluate, mb MatchBounds) Matcher {
	m := NewMatcher(st, eval).(*storeMatcher)
	if mb.Read > 0 {
		m.bounds.read = mb.Read
	}
	if mb.TextReads > 0 {
		m.bounds.textReads = mb.TextReads
	}
	if mb.Undecided > 0 {
		m.bounds.undecided = mb.Undecided
	}
	if mb.Soft > 0 {
		m.bounds.soft = mb.Soft
	}
	if mb.Now != nil {
		m.now = mb.Now
	}
	return m
}

// IDAt is the smallest message identifier of a nanosecond.
func IDAt(t time.Time) string { return idAt(t) }
