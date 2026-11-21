package accountrules

import (
	"context"
	"errors"
	"testing"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/domain"
)

// U5, the service's half: the rules' notice (ADR 0040, "The rules' notice").

func about(name, topic string) contracts.RuleDraft {
	d := hold(name, "x")
	d.Criteria[0].All[0].Any[0] = contracts.Condition{Var: contracts.VarMessage, Op: contracts.OpAbout, Value: topic}
	return d
}

func agree(t *testing.T, w *TestWorld, s *Service, account string) {
	t.Helper()
	if _, err := s.AgreeNotice(context.Background(), w.fresh(account), w.notice); err != nil {
		t.Fatal(err)
	}
}

func TestU5TheNoticeInForceIsTheDomainsByDefault(t *testing.T) {
	w := NewTestWorld(t)
	s, err := New(Config{Records: w.Enclave().cfg.Records, Keys: testKeys{"root"}, Receipts: w.Receipts, Mailboxes: w.mailboxes})
	if err != nil {
		t.Fatal(err)
	}
	if s.cfg.Notice != domain.RulesNoticeVersion {
		t.Fatalf("the notice in force is %q", s.cfg.Notice)
	}
}

// A rule a model decides any of needs the notice in force agreed, and saved
// under it; agreeing records the version and its time, once.
func TestU5ARuleAModelDecidesNeedsTheNotice(t *testing.T) {
	ctx := context.Background()
	w := NewTestWorld(t)
	s := w.Enclave()
	if _, err := trySave(w, s, accountA, "", about("travel", "travel")); !errors.Is(err, contracts.ErrRulesNoticeNeeded) {
		t.Fatalf("saved before the notice was agreed: %v", err)
	}
	if _, err := s.AgreeNotice(ctx, Person{Account: accountA}, w.notice); !errors.Is(err, contracts.ErrConfirmationNeeded) {
		t.Fatalf("agreed without a fresh proof: %v", err)
	}
	agree(t, w, s, accountA)
	n := w.Receipts.Count(accountA)
	again, err := s.AgreeNotice(ctx, w.fresh(accountA), w.notice)
	if err != nil || again.Version != w.notice || !again.At.Equal(start) || w.Receipts.Count(accountA) != n {
		t.Fatalf("agreed twice: %+v, %v, %d receipts", again, err, w.Receipts.Count(accountA)-n)
	}
	// The screen showed another notice than the one in force.
	c, _ := s.Check(ctx, accountA, about("travel", "travel"))
	l, _ := s.List(ctx, accountA)
	if _, err := s.Save(ctx, w.fresh(accountA), "", contracts.RuleSave{DraftID: c.DraftID, Draft: c.Draft, Version: l.Version, Notice: "2026-01-01"}); !errors.Is(err, contracts.ErrRulesNoticeNeeded) {
		t.Fatalf("saved under a notice not in force: %v", err)
	}
	r := save(t, w, s, accountA, "", about("travel", "travel"))
	if r.Notice != w.notice || r.ModelOff {
		t.Fatalf("saved under the notice: %+v", r)
	}
	// A rule no model decides needs nothing.
	if r := save(t, w, s, accountB, "", hold("exact", "x")); r.Notice != "" {
		t.Fatalf("an exact rule records a notice: %+v", r)
	}
}

// A release with a new notice: a rule saved under the older one asks
// nothing (ModelOff, applied wherever it would need the model) until the
// person agrees to the new one and saves it again.
func TestU5ARuleUnderAnotherNoticeIsModelOff(t *testing.T) {
	ctx := context.Background()
	w := NewTestWorld(t)
	s := w.Enclave()
	agree(t, w, s, accountA)
	r := save(t, w, s, accountA, "", about("travel", "travel"))

	w.notice = "2027-01-01"
	next := w.Enclave()
	modelOff := func(want bool) {
		t.Helper()
		a, err := next.Applying(ctx, accountA, claude)
		if err != nil || len(a.Rules) != 2 || a.Rules[1].ID != r.ID || a.Rules[1].ModelOff != want {
			t.Fatalf("applying: %+v, %v", a, err)
		}
		l, err := next.List(ctx, accountA)
		if err != nil || len(l.Rules) != 1 || l.Rules[0].ModelOff != want {
			t.Fatalf("listed: %+v, %v", l, err)
		}
	}
	modelOff(true)
	if _, err := trySave(w, next, accountA, r.ID, about("travel", "travel")); !errors.Is(err, contracts.ErrRulesNoticeNeeded) {
		t.Fatalf("saved again before agreeing to the new notice: %v", err)
	}
	if _, err := next.AgreeNotice(ctx, w.fresh(accountA), "2026-11-20"); !errors.Is(err, contracts.ErrRulesNoticeNeeded) {
		t.Fatalf("agreed to the older notice: %v", err)
	}
	agree(t, w, next, accountA)
	modelOff(true)
	again := save(t, w, next, accountA, r.ID, about("travel", "travel"))
	if again.Version != r.Version || again.Notice != "2027-01-01" {
		t.Fatalf("saved again under the new notice: %+v", again)
	}
	modelOff(false)
}
