package accounts

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/domain"
	"github.com/permaily-org/permaily/internal/idp"
)

// newTerms is a version of the terms published after the one the accounts
// of a test agreed to.
const newTerms = "2027-01-15"

// agreedTo is the version of the terms an account's record says its person
// agreed to, and when; nil when it says none.
func (w *world) agreedTo(id string) *contracts.TermsAgreement {
	w.t.Helper()
	rec, err := w.svc.AccountRecord(context.Background(), id)
	if err != nil {
		w.t.Fatal(err)
	}
	return rec.Terms
}

// Making an account takes the person's agreement to the terms in force, by
// whichever way it is made: by mail, with a provider that vouches for the
// address, and with one whose address is confirmed by mail. Without it,
// nothing is made and the invitation is not used.
func TestNoAccountIsMadeWithoutAgreeingToTheTerms(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	w.on(idp.Google, idp.Meta)

	// By mail: refused when it is asked, before any message.
	invitation, n := w.invite(), w.count()
	for _, terms := range []string{"", "2020-01-01", domain.TermsVersion + " "} {
		if _, err := w.svc.StartEmail(ctx, contracts.EmailStart{Address: "ana@example.org", Invitation: invitation, Terms: terms}); !errors.Is(err, contracts.ErrTermsNeeded) {
			t.Fatalf("by mail with %q: %v", terms, err)
		}
	}
	w.svc.Wait()
	if w.count() != n || w.accounts() != 0 {
		t.Fatal("a message was sent, or an account made")
	}
	// The invitation was not used: with the agreement it makes the account.
	attempt, mail := w.startAgreeing("ana@example.org", invitation)
	me, _, err := w.svc.FinishEmail(ctx, contracts.EmailFinish{Attempt: attempt, Code: code(mail)}, Browser{})
	if err != nil || me.Account.Terms != domain.TermsVersion || me.Terms != domain.TermsVersion {
		t.Fatalf("by mail, agreeing: %+v %v", me, err)
	}

	// With a provider: refused before the browser leaves.
	invitation = w.invite()
	for _, terms := range []string{"", "2020-01-01"} {
		if _, _, err := w.svc.StartProvider(ctx, nil, contracts.ProviderStart{Provider: "google", Invitation: invitation, Terms: terms}); !errors.Is(err, contracts.ErrTermsNeeded) {
			t.Fatalf("with a provider and %q: %v", terms, err)
		}
	}
	state, cookie := w.leave(nil, contracts.ProviderStart{Provider: "google", Invitation: invitation, Terms: domain.TermsVersion})
	if done, err := w.comeBack(state, cookie, person(idp.Google, "g-bea", "bea@example.org", true)); err != nil || done.Session == "" {
		t.Fatalf("with a provider, agreeing: %+v %v", done, err)
	}
	bea := w.find("bea@example.org")
	if got := w.agreedTo(bea); got == nil || got.Version != domain.TermsVersion {
		t.Fatalf("bea agreed to %+v", got)
	}

	// With a provider that does not vouch for the address: the agreement
	// given at the start makes the account once the address is confirmed.
	state, cookie = w.leave(nil, contracts.ProviderStart{Provider: "meta", Invitation: w.invite(), Terms: domain.TermsVersion})
	done, err := w.comeBack(state, cookie, person(idp.Meta, "m-carl", "carl@example.org", false))
	if err != nil || done.Browser == "" {
		t.Fatalf("meta: %+v %v", done, err)
	}
	started, err := w.svc.StartProviderConfirm(ctx, done.Browser, contracts.ProviderConfirmStart{Address: "carl@example.org"})
	if err != nil {
		t.Fatal(err)
	}
	carl, _, err := w.svc.FinishProviderConfirm(ctx, done.Browser, contracts.ProviderConfirm{Attempt: started.Attempt, Code: code(w.mailOf(w.svc, started.Attempt))}, Browser{})
	if err != nil || carl.Account.Terms != domain.TermsVersion {
		t.Fatalf("meta, confirmed: %+v %v", carl, err)
	}

	// The terms changed between the start and the end (a new release): the
	// person agreed to a text that is not in force any more. Nothing is made,
	// and the invitation stays for a sign-up that agrees to the new one.
	invitation = w.invite()
	attempt, mail = w.startAgreeing("dan@example.org", invitation)
	w.terms = newTerms
	if _, _, err := w.svc.FinishEmail(ctx, contracts.EmailFinish{Attempt: attempt, Code: code(mail)}, Browser{}); !errors.Is(err, contracts.ErrTermsNeeded) {
		t.Fatalf("finished after the terms changed: %v", err)
	}
	if w.accounts() != 3 {
		t.Fatalf("accounts: %d", w.accounts())
	}
	res, err := w.svc.StartEmail(ctx, contracts.EmailStart{Address: "dan@example.org", Invitation: invitation, Terms: newTerms})
	if err != nil || res.Attempt == "" {
		t.Fatalf("the invitation after: %v", err)
	}
}

// Signing in to an account that exists asks nothing of the terms, also from
// the sign-up screen, and records nothing: the person agrees, if they have
// to, once they are in.
func TestSigningInAsksNothingOfTheTerms(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	w.signUp("ana@example.org")
	id := w.find("ana@example.org")
	w.terms = newTerms
	w.now = w.now.Add(time.Hour)
	attempt, mail := w.start("ana@example.org", "")
	me, _, err := w.svc.FinishEmail(ctx, contracts.EmailFinish{Attempt: attempt, Code: code(mail)}, Browser{})
	if err != nil || me.Account.Terms != domain.TermsVersion || me.Terms != newTerms {
		t.Fatalf("signing in: %+v %v", me, err)
	}
	// From the sign-up screen, with an invitation and the agreement.
	res, err := w.svc.StartEmail(ctx, contracts.EmailStart{Address: "ana@example.org", Invitation: w.invite(), Terms: newTerms})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.svc.FinishEmail(ctx, contracts.EmailFinish{Attempt: res.Attempt, Code: code(w.mailOf(w.svc, res.Attempt))}, Browser{}); err != nil {
		t.Fatal(err)
	}
	if got := w.agreedTo(id); got == nil || got.Version != domain.TermsVersion {
		t.Fatalf("the agreement after signing in: %+v", got)
	}
}

// The account's record says which version was agreed to and when; the
// person's own account says the version, and the operator's and the
// admins' record both.
func TestTheRecordSaysWhichTermsWereAgreedToAndWhen(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	made := w.now
	me, cookie := w.signUp("ana@example.org")
	got := w.agreedTo(me.Account.ID)
	if got == nil || got.Version != domain.TermsVersion || !got.At.Equal(made) {
		t.Fatalf("the record: %+v", got)
	}
	if doc := w.doc(me.Account.ID); doc.Terms == nil || doc.Terms.Version != domain.TermsVersion || !doc.Terms.At.Equal(made) {
		t.Fatalf("the signed record: %+v", doc.Terms)
	}
	mine, err := w.svc.Me(ctx, w.who(cookie))
	if err != nil || mine.Account.Terms != domain.TermsVersion || mine.Terms != domain.TermsVersion {
		t.Fatalf("me: %+v %v", mine, err)
	}

	// Agreeing to a newer version records it, and when.
	w.terms = newTerms
	w.now = w.now.Add(48 * time.Hour)
	account, err := w.svc.AgreeTerms(ctx, w.who(cookie), contracts.TermsAgree{Version: newTerms})
	if err != nil || account.Terms != newTerms {
		t.Fatalf("agreeing: %+v %v", account, err)
	}
	if got := w.agreedTo(me.Account.ID); got == nil || got.Version != newTerms || !got.At.Equal(w.now) {
		t.Fatalf("after agreeing: %+v", got)
	}
	// Again changes nothing, the time included.
	version := w.version(me.Account.ID)
	w.now = w.now.Add(time.Hour)
	if account, err := w.svc.AgreeTerms(ctx, w.who(cookie), contracts.TermsAgree{Version: newTerms}); err != nil || account.Terms != newTerms || w.version(me.Account.ID) != version {
		t.Fatalf("again: %+v %v", account, err)
	}
}

// An agreement names the version the person was shown, and one that is not
// the version in force is refused: nobody agrees to a text they were not
// shown.
func TestAnAgreementToAnotherVersionIsRefused(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	me, cookie := w.signUp("ana@example.org")
	w.terms = newTerms
	version := w.version(me.Account.ID)
	for _, shown := range []string{"", domain.TermsVersion, "2099-01-01"} {
		// The account exists: it is told to agree to go on, not to make one.
		if _, err := w.svc.AgreeTerms(ctx, w.who(cookie), contracts.TermsAgree{Version: shown}); !errors.Is(err, contracts.ErrTermsNeeded) || err.Error() != contracts.ErrTermsChanged.Error() {
			t.Fatalf("agreeing to %q: %v", shown, err)
		}
	}
	if w.version(me.Account.ID) != version {
		t.Fatal("the record changed")
	}
	// A blocked account agrees to nothing: its session stands for nothing.
	if _, err := w.svc.BlockAccount(ctx, me.Account.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.AgreeTerms(ctx, &Authenticated{AccountID: me.Account.ID}, contracts.TermsAgree{Version: newTerms}); !errors.Is(err, contracts.ErrUnauthorized) {
		t.Fatalf("blocked: %v", err)
	}
}

// When the terms change, an account that agreed to an earlier version
// changes nothing until its person agrees to the new one. What stays open:
// reading the account, its sessions, passkeys and providers; signing out
// and signing a device out; confirming it's them; agreeing; deleting the
// account and cancelling that. The admin panel is closed to them too.
func TestAnAccountThatAgreedToEarlierTermsChangesNothingUntilItAgrees(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	w.on(idp.Google)
	boss := w.account(superAdmin, true)
	ana := w.account("ana@example.org", false)
	other, _ := w.signUp("bea@example.org")
	anaID := ana.me.Account.ID
	w.terms = newTerms
	who := w.fresh(w.who(ana.cookie))
	version, n := w.version(anaID), w.count()

	name := "Ana B"
	for what, err := range map[string]error{
		"name":            second2(w.svc.UpdateAccount(ctx, who, contracts.AccountUpdate{Name: &name})),
		"add a passkey":   second2(w.svc.StartPasskey(ctx, who)),
		"rename":          second2(w.svc.RenamePasskey(ctx, who, ana.key.ID.String(), contracts.PasskeyRename{Name: "other"})),
		"remove":          w.svc.RemovePasskey(ctx, who, ana.key.ID.String()),
		"passkey only":    second2(w.svc.SetPasskeyOnly(ctx, who, contracts.PasskeyOnly{On: true})),
		"address":         second2(w.svc.StartAddressChange(ctx, who, contracts.AddressStart{Address: "ana@example.net"})),
		"link":            third3(w.svc.StartProvider(ctx, who, contracts.ProviderStart{Provider: "google"})),
		"unlink":          w.svc.UnlinkProvider(ctx, who, "google"),
		"the admin panel": second2(w.svc.Admin(ctx, w.fresh(w.who(boss.cookie)))),
	} {
		if !errors.Is(err, contracts.ErrTermsChanged) {
			t.Errorf("%s: %v", what, err)
		}
	}
	w.svc.Wait()
	if w.version(anaID) != version || w.count() != n {
		t.Fatal("something changed, or a message was sent")
	}

	// What stays open.
	if me, err := w.svc.Me(ctx, who); err != nil || me.Account.Terms != domain.TermsVersion || me.Terms != newTerms {
		t.Fatalf("me: %+v %v", me, err)
	}
	if _, err := w.svc.Sessions(ctx, who); err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.Passkeys(ctx, who); err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.LinkedProviders(ctx, who); err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.ConfirmWays(ctx, who); err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.StartConfirmEmail(ctx, who); err != nil {
		t.Fatal(err)
	}
	if account, err := w.svc.StartDeletion(ctx, who); err != nil || account.State != contracts.AccountClosing {
		t.Fatalf("deleting: %+v %v", account, err)
	}
	if account, err := w.svc.CancelDeletion(ctx, who); err != nil || account.State != contracts.AccountActive {
		t.Fatalf("cancelling: %+v %v", account, err)
	}
	if err := w.svc.CloseSession(ctx, who, who.Session.ID); err != nil {
		t.Fatal(err)
	}

	// An account from before the terms existed agreed to none: asked the
	// same way.
	if _, err := w.svc.rewrite(ctx, other.Account.ID, func(doc *accountDoc) error { doc.Terms = nil; return nil }); err != nil {
		t.Fatal(err)
	}
	_, bea := w.signIn("bea@example.org")
	beaWho := w.fresh(w.who(bea))
	if me, _ := w.svc.Me(ctx, beaWho); me.Account.Terms != "" || w.agreedTo(other.Account.ID) != nil {
		t.Fatalf("an account from before says %q", me.Account.Terms)
	}
	if _, err := w.svc.UpdateAccount(ctx, beaWho, contracts.AccountUpdate{Name: &name}); !errors.Is(err, contracts.ErrTermsChanged) {
		t.Fatalf("an account from before: %v", err)
	}

	// Once agreed, it changes again.
	if _, err := w.svc.AgreeTerms(ctx, beaWho, contracts.TermsAgree{Version: newTerms}); err != nil {
		t.Fatal(err)
	}
	if account, err := w.svc.UpdateAccount(ctx, beaWho, contracts.AccountUpdate{Name: &name}); err != nil || account.Name != name {
		t.Fatalf("after agreeing: %+v %v", account, err)
	}
	if _, err := w.svc.AgreeTerms(ctx, w.fresh(w.who(boss.cookie)), contracts.TermsAgree{Version: newTerms}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.Admin(ctx, w.fresh(w.who(boss.cookie))); err != nil {
		t.Fatalf("the admin panel after agreeing: %v", err)
	}
}

// A closing account cancels before it agrees: agreeing is a change like the
// others, refused while the account is closing.
func TestAClosingAccountAgreesOnceItIsKept(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	_, cookie := w.signUp("ana@example.org")
	who := w.fresh(w.who(cookie))
	if _, err := w.svc.StartDeletion(ctx, who); err != nil {
		t.Fatal(err)
	}
	w.terms = newTerms
	if _, err := w.svc.AgreeTerms(ctx, who, contracts.TermsAgree{Version: newTerms}); !errors.Is(err, contracts.ErrAccountClosing) {
		t.Fatalf("closing: %v", err)
	}
}

func third3[A, B any](_ A, _ B, err error) error { return err }

// startAgreeing starts a sign-up by mail with the agreement to the terms in
// force.
func (w *world) startAgreeing(address, invitation string) (string, *Mail) {
	w.t.Helper()
	res, err := w.svc.StartEmail(context.Background(), contracts.EmailStart{Address: address, Invitation: invitation, Terms: w.terms})
	if err != nil {
		w.t.Fatal(err)
	}
	mail := w.mailOf(w.svc, res.Attempt)
	if mail == nil {
		w.t.Fatal("no message")
	}
	return res.Attempt, mail
}

// find is the identifier of the account of an address.
func (w *world) find(address string) string {
	w.t.Helper()
	list, err := w.svc.AccountRecordByAddress(context.Background(), address)
	if err != nil || len(list.Accounts) != 1 {
		w.t.Fatalf("finding %s: %+v %v", address, list, err)
	}
	return list.Accounts[0].ID
}
