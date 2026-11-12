package accounts

import (
	"context"
	"errors"
	"testing"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/domain"
	"github.com/permaily-org/permaily/internal/services/mailboxes"
	"github.com/permaily-org/permaily/internal/services/mailboxes/mailboxestest"
)

// What the mailboxes service reads of an account (ADR 0026, "Access while
// an account is closing or has not agreed").
func TestStanding(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	me, cookie := w.signUp("ana@example.org")
	id := me.Account.ID
	st, err := w.svc.Standing(ctx, id)
	if err != nil || st != (mailboxes.Standing{TermsAgreed: true}) {
		t.Fatalf("%+v %v", st, err)
	}
	w.terms = "2099-01-01"
	if st, _ := w.svc.Standing(ctx, id); st.TermsAgreed {
		t.Fatal("agreed to terms that changed")
	}
	w.terms = domain.TermsVersion
	who, err := w.svc.Authenticate(ctx, cookie)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.StartDeletion(ctx, w.fresh(who)); err != nil {
		t.Fatal(err)
	}
	if st, _ := w.svc.Standing(ctx, id); !st.Closing || !st.TermsAgreed {
		t.Fatalf("%+v", st)
	}
	for _, other := range []string{"", "acc_x", "acc_aaaaaaaaaaaaaaaaaaaaaaaaaa"} {
		if _, err := w.svc.Standing(ctx, other); !errors.Is(err, contracts.ErrUnauthorized) {
			t.Errorf("%q: %v", other, err)
		}
	}
	w.now = w.now.Add(domain.AccountClosing)
	if _, err := w.svc.Standing(ctx, id); !errors.Is(err, contracts.ErrUnauthorized) {
		t.Fatal("an account that is due", err)
	}
	bea, _ := w.signUp("bea@example.org")
	if _, err := w.svc.BlockAccount(ctx, bea.Account.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.Standing(ctx, bea.Account.ID); !errors.Is(err, contracts.ErrUnauthorized) {
		t.Fatal("a blocked account", err)
	}
}

// Removing an account ends its mailboxes first and leaves their list closed;
// a failure there fails the removal, which is tried again.
func TestRemovalEndsTheMailboxes(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	me, cookie := w.signUp("ana@example.org")
	id := me.Account.ID
	fake := mailboxestest.New()
	fake.Add(id, "Work")
	w.svc.SetMailboxes(failingMailboxes{fake})
	who, _ := w.svc.Authenticate(ctx, cookie)
	if _, err := w.svc.StartDeletion(ctx, w.fresh(who)); err != nil {
		t.Fatal(err)
	}
	w.now = w.now.Add(domain.AccountClosing)
	if err := w.svc.RemoveDue(ctx); !errors.Is(err, errMailboxesDown) {
		t.Fatalf("a removal whose mailboxes did not end: %v", err)
	}
	if doc, _ := w.svc.read(ctx, id); doc == nil {
		t.Fatal("the account went without its mailboxes")
	}
	w.svc.SetMailboxes(fake)
	if err := w.svc.RemoveDue(ctx); err != nil {
		t.Fatal(err)
	}
	if n, _ := fake.Count(ctx, id); n != 0 {
		t.Fatal("a mailbox stays")
	}
	if _, err := fake.Create(ctx, mailboxes.Caller{Account: id}, contracts.MailboxCreate{}); !errors.Is(err, contracts.ErrAccountClosing) {
		t.Fatal("the list is not closed", err)
	}
	// Config.Mailboxes is what is used when SetMailboxes gave none.
	w2 := newWorld(t)
	w2.svc.cfg.Mailboxes = fake
	if w2.svc.mailboxesOf() != fake {
		t.Fatal("Config.Mailboxes")
	}
}

var errMailboxesDown = errors.New("the mailboxes are down")

type failingMailboxes struct{ Mailboxes }

func (failingMailboxes) RemoveAccount(context.Context, string) error { return errMailboxesDown }
