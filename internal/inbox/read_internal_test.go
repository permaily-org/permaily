package inbox

import (
	"errors"
	"strings"
	"testing"
	"unicode"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/envelope"
	"github.com/permaily-org/permaily/internal/store"
)

// The times a message identifier carries, and a meta that opens but is not
// one: what the reader makes of what whoever writes to the table put there.
func TestWhatTheTableCanHold(t *testing.T) {
	for _, id := range []string{"", "0123", strings.Repeat("z", 32)} {
		if !timeOfMessageID(id).IsZero() {
			t.Errorf("%q has a time", id)
		}
	}
	b := Box{MailboxID: "mbx_a", Key: make([]byte, envelope.KeySize)}
	id := strings.Repeat("0", 31) + "1"
	sealed, err := envelope.Seal(b.Key, []byte("not json"), envelope.AAD(PurposeMeta, b.MailboxID, id))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := openMeta(b, &store.Message{ID: id, Meta: sealed}); !errors.Is(err, contracts.ErrNotFound) {
		t.Fatalf("%v", err)
	}
}

// The code review of PR #90: folding is Unicode simple case folding, the
// smallest rune of each orbit, the same as the standard library's
// definition of "equal under simple folding" (strings.EqualFold) for every
// rune; and matching folds each field once per entry, so more terms cost no
// more allocation and a 16 KiB text is folded once.
func TestFoldCase(t *testing.T) {
	for r := rune(0); r <= unicode.MaxRune; r++ {
		if r >= 0xd800 && r <= 0xdfff {
			continue
		}
		f := foldRune(r)
		if !strings.EqualFold(string(r), string(f)) {
			t.Fatalf("%U folds to %U, which is not equal under folding", r, f)
		}
		for g := unicode.SimpleFold(r); g != r; g = unicode.SimpleFold(g) {
			if foldRune(g) != f {
				t.Fatalf("%U and %U fold apart", r, g)
			}
		}
	}
	if foldCase("plain ASCII 123") != "PLAIN ASCII 123" || foldCase("ος") != foldCase("ΟΣ") || foldCase("İ") == foldCase("i") || foldCase("ı") == foldCase("i") {
		t.Fatal("the orbits")
	}
	text := strings.Repeat("Ωmega ", SearchTextMax/7)
	e := &searchEntry{SearchEntry{From: []contracts.MailAddress{{Name: "Ann", Address: "ann@example.org"}}, Subject: "Hi", Text: text}}
	oneTerm, tenTerms := []string{"ZZZ"}, strings.Fields("ZZZ YYY XXX WWW VVV UUU TTT SSS RRR QQQ")
	one := testing.AllocsPerRun(20, func() { e.matches(oneTerm) })
	many := testing.AllocsPerRun(20, func() { e.matches(tenTerms) })
	if many > one || one > 6 {
		t.Fatalf("%v allocations for one term, %v for ten", one, many)
	}
}
