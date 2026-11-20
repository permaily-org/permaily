package accountrules

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/rules"
	"github.com/permaily-org/permaily/internal/store"
)

// U29: two enclaves (ADR 0040, "The rules' record").

// Both read the same version and save at once: one wins, the other is
// answered rules_changed, and both then apply the winner's rule alone.
func TestU29TwoEnclavesSavingAtOnceOneWins(t *testing.T) {
	ctx := context.Background()
	for _, first := range []bool{true, false} {
		w := NewTestWorld(t)
		a, ha := w.enclave()
		b, hb := w.enclave()
		save(t, w, a, accountA, "", hold("first", "x"))
		ca, _ := a.Check(ctx, accountA, hold("from a", "a"))
		cb, _ := b.Check(ctx, accountA, hold("from b", "b"))
		la, _ := a.List(ctx, accountA)
		lb, _ := b.List(ctx, accountA)
		if la.Version != lb.Version {
			t.Fatalf("the two read %d and %d", la.Version, lb.Version)
		}
		var errOther error
		winner, loser, hl := a, b, hb
		cw, cl := ca, cb
		if !first {
			winner, loser, hl, cw, cl = b, a, ha, cb, ca
		}
		// The loser has read and is about to write when the winner writes.
		hl.beforeWrite = func(string) {
			hl.beforeWrite = nil
			_, errOther = winner.Save(ctx, w.fresh(accountA), "", contracts.RuleSave{DraftID: cw.DraftID, Draft: cw.Draft, Version: la.Version})
		}
		_, err := loser.Save(ctx, w.fresh(accountA), "", contracts.RuleSave{DraftID: cl.DraftID, Draft: cl.Draft, Version: la.Version})
		if errOther != nil || !errors.Is(err, contracts.ErrRulesChanged) {
			t.Fatalf("the winner: %v; the loser: %v", errOther, err)
		}
		for _, s := range []*Service{a, b} {
			l, err := s.List(ctx, accountA)
			if err != nil || len(l.Rules) != 2 || l.Rules[1].Check.Draft.Name != cw.Draft.Name {
				t.Fatalf("after the race: %+v, %v", l, err)
			}
		}
	}
}

// On real strict histories, a forked record gives nothing through either
// enclave, whatever each had cached, never the default alone; nor does one
// whose newest value was replaced by an earlier one, read by an enclave that
// started after.
func TestU29AForkOrARollbackGivesNothingThroughEitherEnclave(t *testing.T) {
	ctx := context.Background()
	w := NewTestWorld(t)
	a, b := w.Enclave(), w.Enclave()
	save(t, w, a, accountA, "", hold("a", "x"))
	for _, s := range []*Service{a, b} {
		if _, err := s.Applying(ctx, accountA, claude); err != nil {
			t.Fatal(err)
		}
	}
	w.Break(t, accountA)
	unavailable(t, a, accountA)
	unavailable(t, b, accountA)

	w = NewTestWorld(t)
	a = w.Enclave()
	save(t, w, a, accountB, "", hold("a", "x"))
	save(t, w, a, accountB, "", hold("b", "y"))
	var names []string
	for _, n := range w.Mem.RecordNames() {
		if strings.HasPrefix(n, rules.RecordName(accountB, 0)+"@") {
			names = append(names, n)
		}
	}
	slices.Sort(names)
	older, _ := w.Mem.GetRecord(ctx, names[0])
	w.Mem.SetRecord(store.Record{Name: names[1], Value: older.Value, UpdatedAt: older.UpdatedAt})
	restarted := w.Enclave()
	unavailable(t, restarted, accountB)
	w.Clock.Move(Fresh)
	unavailable(t, a, accountB)
}
