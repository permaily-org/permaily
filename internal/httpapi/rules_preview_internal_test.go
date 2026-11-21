package httpapi

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/inbox"
)

// id is a message's identifier arriving at minute n (larger is newer).
func id(n int) string {
	at := time.Date(2026, 11, 20, 0, n, 0, 0, time.UTC)
	return fmt.Sprintf("%016x%016x", at.UnixNano(), n)
}

func summaries(ns ...int) []contracts.MessageSummary {
	out := []contracts.MessageSummary{}
	for _, n := range ns {
		out = append(out, contracts.MessageSummary{ID: id(n)})
	}
	return out
}

func ids(ns ...int) []string {
	out := []string{}
	for _, n := range ns {
		out = append(out, id(n))
	}
	return out
}

func matched(p *contracts.MatchPage) []string {
	out := []string{}
	for _, m := range p.Messages {
		out = append(out, m.ID)
	}
	return out
}

// U27. The rule's page joins its criteria's pages: a match of any is a
// match, before needing the model, before unknown; only what every page
// considered is kept, down to the newest of their ends, which the joined
// page ends at; a page that went to the end does not bound it.
func TestU27TheRulePagesJoin(t *testing.T) {
	a := &contracts.MatchPage{Messages: summaries(9, 6), NeedsModel: ids(8), Unknown: ids(7), Next: id(5)}
	b := &contracts.MatchPage{Messages: summaries(8, 3), NeedsModel: ids(7, 2), Unknown: ids(9), Next: id(2)}
	c := &contracts.MatchPage{Unknown: ids(4)} // went to the end
	got := joinPages([]*contracts.MatchPage{a, b, c}, 50)
	if !slices.Equal(matched(got), ids(9, 8, 6)) || !slices.Equal(got.NeedsModel, ids(7)) || len(got.Unknown) != 0 {
		t.Fatalf("%+v", got)
	}
	if got.Next != id(5) || !got.SearchedTo.Equal(arrivedAt(id(5))) || got.SearchedTo.IsZero() {
		t.Fatalf("next %s, searched to %v", got.Next, got.SearchedTo)
	}
	// Every page went to the end: so does the rule's, with everything.
	got = joinPages([]*contracts.MatchPage{{Messages: summaries(3)}, {Unknown: ids(2, 1)}}, 50)
	if !slices.Equal(matched(got), ids(3)) || !slices.Equal(got.Unknown, ids(2, 1)) || got.Next != "" || !got.SearchedTo.IsZero() {
		t.Fatalf("%+v", got)
	}
	// No page: nothing, to the end.
	if got := joinPages(nil, 50); len(got.Messages) != 0 || got.Next != "" || got.Messages == nil || got.Unknown == nil || got.NeedsModel == nil {
		t.Fatalf("%+v", got)
	}
}

// U27. The joined page holds at most limit matches and
// inbox.MatchMaxUndecided undecided messages, ending at the last it keeps
// when anything is left below it, so the next page goes on there.
func TestU27TheRulePageEndsAtItsBounds(t *testing.T) {
	got := joinPages([]*contracts.MatchPage{{Messages: summaries(9, 7, 5)}, {Messages: summaries(8, 6)}}, 2)
	if !slices.Equal(matched(got), ids(9, 8)) || got.Next != id(8) {
		t.Fatalf("%+v", got)
	}
	// Exactly limit, and nothing below: the end.
	got = joinPages([]*contracts.MatchPage{{Messages: summaries(9)}, {Messages: summaries(8)}}, 2)
	if got.Next != "" {
		t.Fatalf("%+v", got)
	}
	// Exactly limit, and a page that did not go to the end: its end.
	got = joinPages([]*contracts.MatchPage{{Messages: summaries(9), Next: id(8)}, {Messages: summaries(8)}}, 2)
	if got.Next != id(8) {
		t.Fatalf("%+v", got)
	}
	var many []int
	for n := range inbox.MatchMaxUndecided + 10 {
		many = append([]int{n + 1}, many...)
	}
	got = joinPages([]*contracts.MatchPage{{Unknown: ids(many[:60]...)}, {NeedsModel: ids(many[60:]...)}}, 50)
	if len(got.Unknown)+len(got.NeedsModel) != inbox.MatchMaxUndecided || got.Next != id(many[inbox.MatchMaxUndecided-1]) {
		t.Fatalf("%d undecided, next %s", len(got.Unknown)+len(got.NeedsModel), got.Next)
	}
}

// U27. What became of the model is the least any page says: off, before
// the spend's limit, before asked; absent when no page asked.
func TestU27TheRulePageSaysTheLeastOfTheModel(t *testing.T) {
	for _, c := range []struct {
		pages []contracts.MatchModel
		want  contracts.MatchModel
	}{
		{nil, ""}, {[]contracts.MatchModel{""}, ""},
		{[]contracts.MatchModel{contracts.MatchModelAsked, contracts.MatchModelAsked}, contracts.MatchModelAsked},
		{[]contracts.MatchModel{contracts.MatchModelAsked, contracts.MatchModelLimit}, contracts.MatchModelLimit},
		{[]contracts.MatchModel{contracts.MatchModelOff, contracts.MatchModelLimit, contracts.MatchModelAsked}, contracts.MatchModelOff},
		{[]contracts.MatchModel{contracts.MatchModelLimit, contracts.MatchModelOff}, contracts.MatchModelOff},
	} {
		var pages []*contracts.MatchPage
		for _, m := range c.pages {
			pages = append(pages, &contracts.MatchPage{Model: m})
		}
		if got := joinPages(pages, 50).Model; got != c.want {
			t.Errorf("%v: %q, want %q", c.pages, got, c.want)
		}
	}
	if !arrivedAt("short").IsZero() || !arrivedAt("zzzzzzzzzzzzzzzzzz").IsZero() {
		t.Fatal("an identifier that is not one arrived")
	}
}
