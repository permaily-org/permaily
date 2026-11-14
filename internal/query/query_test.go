package query_test

import (
	"math"
	"testing"

	"github.com/permaily-org/permaily/internal/query"
)

// Q33 of ADR 0027: the bands of a model, at each edge, in both modes, for a
// condition and its negation, and a probability that is not one.
func TestClassifyBands(t *testing.T) {
	cases := []struct {
		p           float64
		query, rule query.Value
	}{
		{0, query.False, query.False},
		{0.19, query.False, query.False},
		{0.20, query.False, query.False},
		{0.21, query.Unknown, query.False},
		{0.29, query.Unknown, query.False},
		{0.30, query.Unknown, query.Unknown},
		{0.69, query.Unknown, query.Unknown},
		{0.70, query.Unknown, query.True},
		{0.79, query.Unknown, query.True},
		{0.80, query.True, query.True},
		{1, query.True, query.True},
		{math.NaN(), query.Unknown, query.Unknown},
		{math.Inf(1), query.Unknown, query.Unknown},
		{math.Inf(-1), query.Unknown, query.Unknown},
		{-0.01, query.Unknown, query.Unknown},
		{1.01, query.Unknown, query.Unknown},
	}
	for _, c := range cases {
		if got := query.Classify(c.p, query.ForQuery); got != c.query {
			t.Errorf("Classify(%v, ForQuery) = %v, want %v", c.p, got, c.query)
		}
		if got := query.Classify(c.p, query.ForRule); got != c.rule {
			t.Errorf("Classify(%v, ForRule) = %v, want %v", c.p, got, c.rule)
		}
	}
	if got := query.Classify(0.9, query.Mode(7)); got != query.Unknown {
		t.Errorf("Classify in an unknown mode = %v, want Unknown", got)
	}
}

// The negation of a model's condition, as Eval applies it: a query delivers on
// "not about" only at 0.20 or less, and a rule applies it below 0.70.
func TestClassifyNegated(t *testing.T) {
	not := func(v query.Value) query.Value { return query.True - v }
	delivers := func(v query.Value) bool { return v == query.True }
	applies := func(v query.Value) bool { return v != query.False }
	for _, c := range []struct {
		p              float64
		deliver, apply bool
	}{
		{0.20, true, true},
		{0.21, false, true},
		{0.69, false, true},
		{0.70, false, false},
	} {
		if got := delivers(not(query.Classify(c.p, query.ForQuery))); got != c.deliver {
			t.Errorf("a query's not-about at %v delivers = %v, want %v", c.p, got, c.deliver)
		}
		if got := applies(not(query.Classify(c.p, query.ForRule))); got != c.apply {
			t.Errorf("a rule's not-about at %v applies = %v, want %v", c.p, got, c.apply)
		}
	}
}
