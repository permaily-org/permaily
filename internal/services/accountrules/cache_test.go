package accountrules

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/rules"
	"github.com/permaily-org/permaily/internal/store"
)

// The cache keeps the newest record it was given: a higher epoch or
// version, or the same one read by a read that began later.
func TestU4TheCacheKeepsTheNewest(t *testing.T) {
	w := NewTestWorld(t)
	s := w.Enclave()
	rec := &rules.Record{}
	s.keep(accountA, cached{rec: rec, epoch: 2, version: 5, began: start})
	for _, older := range []cached{
		{epoch: 1, version: 9, began: start.Add(time.Minute)},
		{epoch: 2, version: 4, began: start.Add(time.Minute)},
		{epoch: 2, version: 5, began: start.Add(-time.Minute)},
	} {
		s.keep(accountA, older)
		if c := s.cache[accountA]; c.rec != rec || !c.began.Equal(start) {
			t.Fatalf("%+v replaced the newest", older)
		}
	}
	s.keep(accountA, cached{rec: rec, epoch: 2, version: 5, began: start.Add(time.Second)})
	if c := s.cache[accountA]; !c.began.Equal(start.Add(time.Second)) {
		t.Fatal("the same version read later did not make it younger")
	}
	for i := range cacheMax {
		s.keep("acc_"+strings.Repeat("x", 4)+string(rune('a'+i%26))+time.Duration(i).String(), cached{})
	}
	if len(s.cache) > cacheMax {
		t.Fatalf("%d accounts cached", len(s.cache))
	}
}

// A record past its bound is never written, and an epoch whose
// predecessor cannot be read is not made again.
func TestU2NothingPastTheBoundIsWritten(t *testing.T) {
	ctx := context.Background()
	w := NewTestWorld(t)
	s := w.Enclave()
	big := &rules.Record{V: rules.RecordFormat, Account: accountA, Rules: []rules.Stored{{ID: strings.Repeat("x", rules.RecordBytesMax)}}}
	if err := s.seal(ctx, big, store.Mark{}, true); !errors.Is(err, errDocument) {
		t.Fatalf("wrote a record past its bound: %v", err)
	}
	save(t, w, s, accountA, "", hold("a", "x"))
	w.Break(t, accountA)
	if err := s.remake(ctx, accountA, 1); !errors.Is(err, store.ErrForked) {
		t.Fatalf("made an epoch from a forked one: %v", err)
	}
	if err := s.prune(ctx, accountA, 1); !errors.Is(err, store.ErrForked) {
		t.Fatalf("pruned below a forked epoch: %v", err)
	}
	if err := s.prune(ctx, accountB, 2); !errors.Is(err, store.ErrRolledBack) {
		t.Fatalf("pruned epochs never made: %v", err)
	}
}
