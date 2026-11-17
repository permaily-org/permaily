package accounts

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/domain"
)

// The model's notice (ADR 0028, "Consent"; pull request 4, A29): agreed by
// the version in force, recorded in the account's record with when, read
// through either enclave, and refused in the standings ADR 0026 refuses a
// change in.

// newNotice is a version of the model's notice published after the one in
// force when the tests' accounts were made.
const newNotice = "2027-02-01"

// oldNotice is the version of the model's notice before the one in force
// (2026-11-16, after the legal review of pull request 148).
const oldNotice = "2026-11-15"

func agreeNotice(version string) contracts.TermsAgree { return contracts.TermsAgree{Version: version} }

// A29: agreeing records the version and when in the account's record, the
// person's own account says it, and the matches route reads it.
func TestA29TheModelNoticeIsAgreedAndRecorded(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	me, cookie := w.signUp("ana@example.org")
	if me.Account.ModelNotice != "" || w.doc(me.Account.ID).ModelNotice != nil {
		t.Fatalf("a new account agreed to the notice: %+v", me.Account)
	}
	if agreed, err := w.svc.ModelNoticeAgreed(ctx, w.who(cookie)); err != nil || agreed {
		t.Fatalf("before agreeing: %v %v", agreed, err)
	}

	w.now = w.now.Add(time.Hour)
	account, err := w.svc.AgreeModelNotice(ctx, w.who(cookie), agreeNotice(domain.ModelNoticeVersion))
	if err != nil || account.ModelNotice != domain.ModelNoticeVersion || account.Terms != domain.TermsVersion {
		t.Fatalf("agreeing: %+v %v", account, err)
	}
	if doc := w.doc(me.Account.ID); doc.ModelNotice == nil || doc.ModelNotice.Version != domain.ModelNoticeVersion || !doc.ModelNotice.At.Equal(w.now) {
		t.Fatalf("the signed record: %+v", doc.ModelNotice)
	}
	mine, err := w.svc.Me(ctx, w.who(cookie))
	if err != nil || mine.Account.ModelNotice != domain.ModelNoticeVersion {
		t.Fatalf("me: %+v %v", mine, err)
	}
	if agreed, err := w.svc.ModelNoticeAgreed(ctx, w.who(cookie)); err != nil || !agreed {
		t.Fatalf("after agreeing: %v %v", agreed, err)
	}
	// Again changes nothing, the time included.
	version := w.version(me.Account.ID)
	w.now = w.now.Add(time.Hour)
	if account, err := w.svc.AgreeModelNotice(ctx, w.who(cookie), agreeNotice(domain.ModelNoticeVersion)); err != nil || account.ModelNotice != domain.ModelNoticeVersion || w.version(me.Account.ID) != version {
		t.Fatalf("again: %+v %v", account, err)
	}
	// Logged by its version once, and nothing of the account.
	if n := strings.Count(w.logs.String(), `msg="accounts: model notice agreed" version=`+domain.ModelNoticeVersion); n != 1 {
		t.Fatalf("logged %d times:\n%s", n, w.logs)
	}
	w.quiet("ana@", "example.org", me.Account.ID)
}

// A29: an agreement names the version the person was shown; one that is not
// the version in force is refused and changes nothing. A new version in
// force makes an earlier agreement no agreement until the person agrees
// again.
func TestA29AnAgreementToAnotherNoticeIsRefused(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	me, cookie := w.signUp("ana@example.org")
	version := w.version(me.Account.ID)
	for _, shown := range []string{"", newNotice, oldNotice, domain.ModelNoticeVersion + " "} {
		if _, err := w.svc.AgreeModelNotice(ctx, w.who(cookie), agreeNotice(shown)); !errors.Is(err, contracts.ErrModelNoticeNeeded) {
			t.Fatalf("agreeing to %q: %v", shown, err)
		}
	}
	if w.version(me.Account.ID) != version {
		t.Fatal("the record changed")
	}
	if _, err := w.svc.AgreeModelNotice(ctx, w.who(cookie), agreeNotice(domain.ModelNoticeVersion)); err != nil {
		t.Fatal(err)
	}
	// A release changes the notice: the earlier agreement does not count.
	w.notice = newNotice
	if agreed, err := w.svc.ModelNoticeAgreed(ctx, w.who(cookie)); err != nil || agreed {
		t.Fatalf("after the notice changed: %v %v", agreed, err)
	}
	if _, err := w.svc.AgreeModelNotice(ctx, w.who(cookie), agreeNotice(domain.ModelNoticeVersion)); !errors.Is(err, contracts.ErrModelNoticeNeeded) {
		t.Fatalf("agreeing to the earlier notice: %v", err)
	}
	account, err := w.svc.AgreeModelNotice(ctx, w.who(cookie), agreeNotice(newNotice))
	if err != nil || account.ModelNotice != newNotice {
		t.Fatalf("agreeing to the new notice: %+v %v", account, err)
	}
	if agreed, err := w.svc.ModelNoticeAgreed(ctx, w.who(cookie)); err != nil || !agreed {
		t.Fatalf("after agreeing again: %v %v", agreed, err)
	}
}

// A29: an agreement given through one enclave counts through the other at
// once (it is in the account's record), and two given through both at once
// leave one agreement.
func TestA29TheNoticeCountsThroughEitherEnclave(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	_, cookie := w.signUp("ana@example.org")
	one, other := w.enclave(), w.enclave()
	if agreed, err := other.svc.ModelNoticeAgreed(ctx, w.who(cookie)); err != nil || agreed {
		t.Fatalf("before: %v %v", agreed, err)
	}
	if _, err := one.svc.AgreeModelNotice(ctx, w.who(cookie), agreeNotice(domain.ModelNoticeVersion)); err != nil {
		t.Fatal(err)
	}
	if agreed, err := other.svc.ModelNoticeAgreed(ctx, w.who(cookie)); err != nil || !agreed {
		t.Fatalf("through the other: %v %v", agreed, err)
	}

	// Through both at once, after a new notice.
	w.notice = newNotice
	w.now = w.now.Add(time.Hour)
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, e := range []*enclave{one, other} {
		wg.Go(func() {
			_, errs[i] = e.svc.AgreeModelNotice(ctx, w.who(cookie), agreeNotice(newNotice))
		})
	}
	wg.Wait()
	if errs[0] != nil || errs[1] != nil {
		t.Fatalf("at once: %v", errs)
	}
	for _, e := range []*enclave{one, other, w.enclave()} {
		if agreed, err := e.svc.ModelNoticeAgreed(ctx, w.who(cookie)); err != nil || !agreed {
			t.Fatalf("after both: %v %v", agreed, err)
		}
	}
}

// A29: ADR 0026's standings. A blocked account agrees to nothing and is
// not read as having agreed; a closing one, and one that has not agreed to
// the terms in force, cannot agree.
func TestA29TheStandingsOfTheNotice(t *testing.T) {
	w, ctx := newWorld(t), context.Background()

	// The terms changed and were not agreed to.
	_, ana := w.signUp("ana@example.org")
	w.terms = newTerms
	if _, err := w.svc.AgreeModelNotice(ctx, w.who(ana), agreeNotice(domain.ModelNoticeVersion)); !errors.Is(err, contracts.ErrTermsChanged) {
		t.Fatalf("terms not agreed: %v", err)
	}
	if _, err := w.svc.AgreeTerms(ctx, w.who(ana), contracts.TermsAgree{Version: newTerms}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.AgreeModelNotice(ctx, w.who(ana), agreeNotice(domain.ModelNoticeVersion)); err != nil {
		t.Fatalf("once the terms are agreed: %v", err)
	}

	// Closing.
	_, bea := w.signUp("bea@example.org")
	if _, err := w.svc.StartDeletion(ctx, w.fresh(w.who(bea))); err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.AgreeModelNotice(ctx, w.who(bea), agreeNotice(domain.ModelNoticeVersion)); !errors.Is(err, contracts.ErrAccountClosing) {
		t.Fatalf("closing: %v", err)
	}

	// Blocked: its session stands for nothing.
	id := w.find("ana@example.org")
	if _, err := w.svc.BlockAccount(ctx, id); err != nil {
		t.Fatal(err)
	}
	blocked := &Authenticated{AccountID: id}
	if _, err := w.svc.AgreeModelNotice(ctx, blocked, agreeNotice(domain.ModelNoticeVersion)); !errors.Is(err, contracts.ErrUnauthorized) {
		t.Fatalf("blocked: %v", err)
	}
	if agreed, err := w.svc.ModelNoticeAgreed(ctx, blocked); !errors.Is(err, contracts.ErrUnauthorized) || agreed {
		t.Fatalf("blocked, read: %v %v", agreed, err)
	}
	// Gone: no account.
	if agreed, err := w.svc.ModelNoticeAgreed(ctx, &Authenticated{AccountID: "acc_" + strings.Repeat("z", 26)}); !errors.Is(err, contracts.ErrUnauthorized) || agreed {
		t.Fatalf("gone: %v %v", agreed, err)
	}
}
