package accounts

import (
	"context"
	"errors"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/domain"
	"github.com/permaily-org/permaily/internal/idp"
	"github.com/permaily-org/permaily/internal/store"
	"github.com/permaily-org/permaily/internal/webauthn/webauthntest"
)

// The subjects of the notices (notices.go): fixed words, which is how a test
// tells one from another.
const (
	subjectNewDevice = "A new device signed in to your Permaily account"
	subjectChange    = "A change to how you sign in to Permaily"
	subjectAddress   = "The contact address of your Permaily account changed"
	subjectDeleting  = "Your Permaily account is being deleted"
	subjectKept      = "Your Permaily account is no longer being deleted"
	subjectBlocked   = "Your Permaily account was blocked"
	subjectUnblocked = "Your Permaily account was unblocked"
)

// sent returns the messages sent since there were n, and how many there are.
func (w *world) sent(n int) ([]Mail, int) {
	w.t.Helper()
	w.svc.Wait()
	all := w.out.Sent()
	return all[n:], len(all)
}

// one fails unless exactly one message was sent since there were n, to that
// address and with that subject, and returns it.
func (w *world) one(n int, to, subject string) Mail {
	w.t.Helper()
	got, _ := w.sent(n)
	if len(got) != 1 || got[0].To != to || got[0].Subject != subject {
		w.t.Fatalf("want one message to %s, %q; got %+v", to, subject, got)
	}
	return got[0]
}

func (w *world) count() int { _, n := w.sent(0); return n }

// block and the rest are the operator's actions, which must work.
func (w *world) operator(act func(context.Context, string) (*contracts.AccountRecord, error), id string) *contracts.AccountRecord {
	w.t.Helper()
	rec, err := act(context.Background(), id)
	if err != nil || rec == nil || rec.ID != id {
		w.t.Fatalf("the operator's action: %+v %v", rec, err)
	}
	return rec
}

// ways is what of an account's record would be a way into it.
type ways struct {
	Address     string
	VerifiedAt  time.Time
	Passkeys    []passkeyDoc
	PasskeyOnly bool
	Codes       [][]byte
	Providers   []providerDoc
	Moving      string
}

func (w *world) ways(id string) ways {
	w.t.Helper()
	doc := w.doc(id)
	if doc == nil {
		w.t.Fatal("no such account")
	}
	return ways{doc.Address, doc.AddressVerifiedAt, doc.Passkeys, doc.PasskeyOnly, doc.RecoveryCodes, doc.Providers, doc.MovingTo}
}

// An account that the operator blocked is signed in to by no way: not by
// mail, with a passkey, with a provider, by confirming an address for a
// provider, or with a recovery code. Whoever proves who they are is told it
// is blocked, and nobody gets a session. A session that is still in the
// table stands for nothing.
func TestABlockedAccountSignsInByNoWay(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	w.on(idp.Google, idp.GitHub, idp.Meta)

	// Ana signs in by mail, with a passkey and with Google.
	ana, anaCookie := w.signUp("ana@example.org")
	anaWho := w.who(anaCookie)
	key := webauthntest.New(webauthntest.ES256)
	passkey := w.addPasskey(anaWho, key, "laptop")
	google := person(idp.Google, "g-ana", "ana@example.org", true)
	if _, err := w.linkFrom(anaWho, google); err != nil {
		t.Fatal(err)
	}
	// Bea signs in with a passkey only, and has recovery codes.
	bea, beaCookie := w.signUp("bea@example.org")
	beaKey := webauthntest.New(webauthntest.ES256)
	w.addPasskey(w.who(beaCookie), beaKey, "phone")
	codes := w.passkeyOnly(w.who(beaCookie))

	// A sign-in by mail that was started before the block.
	early, earlyMail := w.start("ana@example.org", "")
	before := w.snapshot()

	n := w.count()
	rec := w.operator(w.svc.BlockAccount, ana.Account.ID)
	if !rec.BlockedAt.Equal(w.now) || rec.Sessions != 0 || rec.State != contracts.AccountActive {
		t.Fatalf("the record after blocking: %+v", rec)
	}
	told := w.one(n, "ana@example.org", subjectBlocked)
	if !strings.Contains(told.Text, "Permaily's operator blocked your Permaily account") || !strings.Contains(told.Text, "support@permaily.com") || !strings.Contains(told.Text, "11 November 2026, 09:00 UTC") {
		t.Fatalf("the notice: %s", told.Text)
	}
	w.operator(w.svc.BlockAccount, bea.Account.ID)
	afterBlock := w.count()

	// Its sessions are closed, and one put back in the table is no session.
	if _, err := w.svc.Authenticate(ctx, anaCookie); !errors.Is(err, contracts.ErrUnauthorized) {
		t.Fatalf("a session of a blocked account: %v", err)
	}
	for _, e := range before.entries {
		if e.Partition == accountPartition(ana.Account.ID) && strings.HasPrefix(e.Key, sessionPrefix) {
			w.mem.SetEntry(e)
		}
	}
	if _, err := w.svc.Authenticate(ctx, anaCookie); !errors.Is(err, contracts.ErrUnauthorized) {
		t.Fatalf("a session put back for a blocked account: %v", err)
	}
	// Nor does a request that was let in a moment before the block do anything.
	refusals := map[string]error{
		"me":           second(w.svc.Me(ctx, anaWho)),
		"name":         second(w.svc.UpdateAccount(ctx, anaWho, contracts.AccountUpdate{Name: text("Ana")})),
		"passkeys":     second(w.svc.Passkeys(ctx, anaWho)),
		"new passkey":  second(w.svc.StartPasskey(ctx, w.fresh(anaWho))),
		"rename":       second(w.svc.RenamePasskey(ctx, anaWho, passkey.ID.String(), contracts.PasskeyRename{Name: "x"})),
		"remove":       w.svc.RemovePasskey(ctx, w.fresh(anaWho), passkey.ID.String()),
		"passkey only": second(w.svc.SetPasskeyOnly(ctx, w.fresh(anaWho), contracts.PasskeyOnly{On: true})),
		"delete":       second(w.svc.StartDeletion(ctx, w.fresh(anaWho))),
		"cancel":       second(w.svc.CancelDeletion(ctx, anaWho)),
		"providers":    second(w.svc.LinkedProviders(ctx, anaWho)),
		"unlink":       w.svc.UnlinkProvider(ctx, w.fresh(anaWho), "google"),
		"address":      second(w.svc.StartAddressChange(ctx, w.fresh(anaWho), contracts.AddressStart{Address: "new@example.org"})),
		"address ends": second(w.svc.FinishAddressChange(ctx, anaWho, contracts.AddressFinish{Attempt: early, Code: "000000"})),
		"link provider": func() error {
			_, _, err := w.svc.StartProvider(ctx, w.fresh(anaWho), contracts.ProviderStart{Provider: "github"})
			return err
		}(),
	}
	for what, err := range refusals {
		if !errors.Is(err, contracts.ErrUnauthorized) {
			t.Errorf("%s with the session of a blocked account: %v", what, err)
		}
	}
	w.mem.DeletePartition(ctx, accountPartition(ana.Account.ID)) //nolint:errcheck // the sessions put back
	waysBefore := w.ways(ana.Account.ID)

	blocked := func(what string, err error) {
		t.Helper()
		if !errors.Is(err, contracts.ErrAccountBlocked) {
			t.Errorf("%s to a blocked account: %v, want account_blocked", what, err)
		}
	}
	// By mail: the sign-in started before, a new one by its code, and one by
	// its link.
	_, _, err := w.svc.FinishEmail(ctx, contracts.EmailFinish{Attempt: early, Code: code(earlyMail)}, Browser{})
	blocked("a sign-in by mail started before", err)
	attempt, mail := w.start("ana@example.org", "")
	if mail == nil {
		t.Fatal("no message for a blocked address: starting must not tell it apart")
	}
	_, _, err = w.svc.FinishEmail(ctx, contracts.EmailFinish{Attempt: attempt, Code: code(mail)}, Browser{})
	blocked("a sign-in by mail", err)
	attempt, mail = w.start("ana@example.org", "")
	_, _, err = w.svc.FinishEmail(ctx, contracts.EmailFinish{Attempt: attempt, Token: linkRE.FindStringSubmatch(mail.Text)[2]}, Browser{})
	blocked("a sign-in by link", err)
	// With a passkey.
	_, _, err = w.signInWith(key, passkey.ID)
	blocked("a sign-in with a passkey", err)
	// With the provider that is linked: the reason reaches the screen.
	done, err := w.through(google, "")
	w.refusedWith("a sign-in with a provider", done, err, contracts.ErrAccountBlocked, "/sign-in#provider_error=account_blocked")
	// With a provider that vouches for the address, for the first time:
	// nothing is linked.
	done, err = w.through(person(idp.GitHub, "gh-ana", "ana@example.org", true), "")
	w.refusedWith("a first sign-in with a provider", done, err, contracts.ErrAccountBlocked, "/sign-in#provider_error=account_blocked")
	// With a provider that does not, after the address is confirmed by mail.
	done, err = w.through(person(idp.Meta, "fb-ana", "ana@example.org", false), "")
	if err != nil || done.Browser == "" {
		t.Fatalf("a provider that does not vouch: %+v %v", done, err)
	}
	started, err := w.svc.StartProviderConfirm(ctx, done.Browser, contracts.ProviderConfirmStart{Address: "ana@example.org"})
	if err != nil {
		t.Fatal(err)
	}
	all, _ := w.sent(0)
	_, _, err = w.svc.FinishProviderConfirm(ctx, done.Browser, contracts.ProviderConfirm{Attempt: started.Attempt, Code: code(&all[len(all)-1])}, Browser{})
	blocked("confirming an address for a provider", err)
	// With a recovery code: the right one is told, and is not used up.
	_, _, err = w.recover("bea@example.org", codes[0])
	blocked("a recovery code", err)
	if left := len(w.doc(bea.Account.ID).RecoveryCodes); left != domain.RecoveryCodes {
		t.Fatalf("a blocked account has %d recovery codes left: one was used up", left)
	}
	_, _, err = w.signInWith(beaKey, w.doc(bea.Account.ID).Passkeys[0].ID)
	blocked("a passkey of a passkey-only account", err)

	// Nobody was signed in, nothing was linked, and nothing of how the
	// accounts are signed into changed.
	for _, id := range []string{ana.Account.ID, bea.Account.ID} {
		if list, err := w.svc.sessions(ctx, id); err != nil || len(list) != 0 {
			t.Fatalf("a blocked account has sessions: %+v %v", list, err)
		}
	}
	if got := w.ways(ana.Account.ID); !reflect.DeepEqual(got, waysBefore) {
		t.Fatalf("the ways into a blocked account changed:\n%+v\n%+v", got, waysBefore)
	}
	// No notice of a new device either: nobody signed in.
	refused, _ := w.sent(afterBlock)
	for _, m := range refused {
		if m.Subject == subjectNewDevice || m.Subject == subjectChange {
			t.Fatalf("a notice for a sign-in that was refused: %+v", m)
		}
	}

	// Blocking again changes nothing, and tells nobody again.
	n = w.count()
	if again := w.operator(w.svc.BlockAccount, ana.Account.ID); !again.BlockedAt.Equal(rec.BlockedAt) {
		t.Fatalf("blocked again: %+v", again)
	}
	if got, _ := w.sent(n); len(got) != 0 {
		t.Fatalf("blocking again sent %+v", got)
	}

	// Unblocked, every way works again.
	w.now = w.now.Add(time.Hour)
	n = w.count()
	if rec := w.operator(w.svc.UnblockAccount, ana.Account.ID); !rec.BlockedAt.IsZero() {
		t.Fatalf("unblocked: %+v", rec)
	}
	told = w.one(n, "ana@example.org", subjectUnblocked)
	if !strings.Contains(told.Text, "unblocked your Permaily account") || !strings.Contains(told.Text, "app.permaily.test") {
		t.Fatalf("the notice: %s", told.Text)
	}
	n = w.count()
	w.operator(w.svc.UnblockAccount, ana.Account.ID)
	if got, _ := w.sent(n); len(got) != 0 {
		t.Fatalf("unblocking again sent %+v", got)
	}
	w.operator(w.svc.UnblockAccount, bea.Account.ID)
	if _, _, err := w.signInWith(key, passkey.ID); err != nil {
		t.Fatalf("a passkey after unblocking: %v", err)
	}
	w.signIn("ana@example.org")
	if done, err := w.through(google, ""); err != nil || done.Session == "" {
		t.Fatalf("a provider after unblocking: %+v %v", done, err)
	}
	if _, _, err := w.recover("bea@example.org", codes[0]); err != nil {
		t.Fatalf("the recovery code after unblocking: %v", err)
	}
	// The cookie from before the block stays a cookie of nothing.
	if _, err := w.svc.Authenticate(ctx, anaCookie); !errors.Is(err, contracts.ErrUnauthorized) {
		t.Fatalf("a session from before the block: %v", err)
	}
	w.quiet("ana@", "bea@", "example.org", ana.Account.ID, bea.Account.ID)
}

// Before someone proves who they are, nothing they are answered tells a
// blocked account from another, or from none: so nobody learns from the
// outside that an address has an account, or that it is blocked.
func TestNothingSaysAnAddressIsBlockedBeforeProof(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	ana, anaCookie := w.signUp("ana@example.org")
	key := webauthntest.New(webauthntest.ES256)
	passkey := w.addPasskey(w.who(anaCookie), key, "laptop")
	bea, beaCookie := w.signUp("bea@example.org")
	beaKey := webauthntest.New(webauthntest.ES256)
	w.addPasskey(w.who(beaCookie), beaKey, "phone")
	codes := w.passkeyOnly(w.who(beaCookie))
	_, eveCookie := w.signUp("eve@example.org")
	w.operator(w.svc.BlockAccount, ana.Account.ID)
	w.operator(w.svc.BlockAccount, bea.Account.ID)

	// Starting a sign-in by mail: the same answer, and the message is sent.
	attempt, mail := w.start("ana@example.org", "")
	if mail == nil || mail.To != "ana@example.org" || mail.Subject != "Sign in to Permaily" || strings.Contains(mail.Text, "block") {
		t.Fatalf("the message for a blocked address: %+v", mail)
	}
	// A wrong code is a wrong code.
	if _, _, err := w.svc.FinishEmail(ctx, contracts.EmailFinish{Attempt: attempt, Code: w.wrongCode(mail)}, Browser{}); !errors.Is(err, contracts.ErrInvalidCode) || errors.Is(err, contracts.ErrAccountBlocked) {
		t.Fatalf("a wrong code for a blocked address: %v", err)
	}
	if _, _, err := w.svc.FinishEmail(ctx, contracts.EmailFinish{Attempt: attempt, Token: "not the token"}, Browser{}); !errors.Is(err, contracts.ErrInvalidCode) {
		t.Fatalf("a wrong token for a blocked address: %v", err)
	}
	// An attempt that is not one, with the account's address nowhere near.
	if _, _, err := w.svc.FinishEmail(ctx, contracts.EmailFinish{Attempt: newID("att"), Code: "123456"}, Browser{}); !errors.Is(err, contracts.ErrInvalidCode) {
		t.Fatalf("no such attempt: %v", err)
	}
	// A passkey that is not the account's, naming the account.
	stranger := webauthntest.New(webauthntest.ES256)
	in := w.assert(key, passkey.ID)
	forged := w.assert(stranger, passkey.ID)
	forged.Credential.Response.UserHandle = in.Credential.Response.UserHandle
	if _, _, err := w.svc.FinishPasskeySignIn(ctx, forged, Browser{}); !errors.Is(err, contracts.ErrInvalidPasskey) {
		t.Fatalf("another key for a blocked account: %v", err)
	}
	in.Credential.RawID = []byte("no such passkey")
	if _, _, err := w.svc.FinishPasskeySignIn(ctx, in, Browser{}); !errors.Is(err, contracts.ErrInvalidPasskey) {
		t.Fatalf("no such passkey for a blocked account: %v", err)
	}
	// A wrong recovery code, and a right one with another address.
	if _, _, err := w.recover("bea@example.org", "2222-2222-2222-2222"); !errors.Is(err, contracts.ErrInvalidCode) {
		t.Fatalf("a wrong recovery code for a blocked account: %v", err)
	}
	if _, _, err := w.recover("ana@example.org", codes[0]); !errors.Is(err, contracts.ErrInvalidCode) {
		t.Fatalf("a recovery code of another account: %v", err)
	}
	// Someone else asks to move to the blocked account's address: the answer
	// and the message are as for any address.
	n := w.count()
	free, err := w.svc.StartAddressChange(ctx, w.fresh(w.who(eveCookie)), contracts.AddressStart{Address: "nobody@example.org"})
	if err != nil {
		t.Fatal(err)
	}
	taken, err := w.svc.StartAddressChange(ctx, w.fresh(w.who(eveCookie)), contracts.AddressStart{Address: "ana@example.org"})
	if err != nil || taken.ExpiresIn != free.ExpiresIn || !validID("att", taken.Attempt) {
		t.Fatalf("asking for a blocked account's address: %+v %v", taken, err)
	}
	if got, _ := w.sent(n); len(got) != 2 || got[0].Subject != got[1].Subject || len(got[0].Text) != len(got[1].Text) {
		t.Fatalf("the messages: %+v", got)
	}
	// The operator's own tool is what says it, to the operator.
	if list, err := w.svc.AccountRecordByAddress(ctx, "ana@example.org"); err != nil || len(list.Accounts) != 1 || list.Accounts[0].BlockedAt.IsZero() {
		t.Fatalf("the operator's tool: %+v %v", list, err)
	}
}

// Blocking is apart from deleting: a closing account can be blocked and a
// blocked one can be closing, and it is removed on its day all the same.
func TestBlockedAndClosing(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	ana, anaCookie := w.signUp("ana@example.org")
	bea, _ := w.signUp("bea@example.org")
	anaWho := w.who(anaCookie)

	// Closing, then blocked.
	if _, err := w.svc.StartDeletion(ctx, w.fresh(anaWho)); err != nil {
		t.Fatal(err)
	}
	rec := w.operator(w.svc.BlockAccount, ana.Account.ID)
	if rec.State != contracts.AccountClosing || rec.BlockedAt.IsZero() || rec.ClosingBy != contracts.ClosingByPerson || rec.ClosesAt.IsZero() {
		t.Fatalf("closing and then blocked: %+v", rec)
	}
	// Its person cannot cancel: they are not signed in.
	if _, err := w.svc.CancelDeletion(ctx, anaWho); !errors.Is(err, contracts.ErrUnauthorized) {
		t.Fatalf("cancelling with the session of a blocked account: %v", err)
	}
	if doc := w.doc(ana.Account.ID); doc.State != contracts.AccountClosing {
		t.Fatalf("a blocked account was kept by its person: %+v", doc)
	}
	// Blocked, then closing, by the operator: the notice does not offer what
	// cannot be done.
	w.operator(w.svc.BlockAccount, bea.Account.ID)
	n := w.count()
	rec = w.operator(w.svc.DeleteAccount, bea.Account.ID)
	if rec.State != contracts.AccountClosing || rec.BlockedAt.IsZero() || rec.ClosingBy != contracts.ClosingByOperator {
		t.Fatalf("blocked and then closing: %+v", rec)
	}
	told := w.one(n, "bea@example.org", subjectDeleting)
	if !strings.Contains(told.Text, "The account is blocked, so nobody can sign in to cancel the deletion") || strings.Contains(told.Text, "Until then you can sign in") {
		t.Fatalf("the notice: %s", told.Text)
	}
	// Unblocking leaves it closing, and the other way round.
	if rec := w.operator(w.svc.UnblockAccount, bea.Account.ID); rec.State != contracts.AccountClosing || !rec.BlockedAt.IsZero() {
		t.Fatalf("unblocked while closing: %+v", rec)
	}
	// What Ana asked, only Ana cancels, also while blocked: the operator
	// unblocks first, and she signs in and cancels.
	if rec, err := w.svc.KeepAccount(ctx, ana.Account.ID); !errors.Is(err, contracts.ErrNotCancellable) || rec != nil {
		t.Fatalf("the operator keeps what a blocked person asked to delete: %+v %v", rec, err)
	}
	w.operator(w.svc.UnblockAccount, ana.Account.ID)
	_, again := w.signIn("ana@example.org")
	if kept, err := w.svc.CancelDeletion(ctx, w.who(again)); err != nil || kept.State != contracts.AccountActive {
		t.Fatalf("Ana cancels once unblocked: %+v %v", kept, err)
	}
	if rec := w.operator(w.svc.BlockAccount, ana.Account.ID); rec.State != contracts.AccountActive || rec.BlockedAt.IsZero() || rec.ClosingBy != "" {
		t.Fatalf("kept and blocked again: %+v", rec)
	}
	// On its day a blocked, closing account is removed like any other.
	w.operator(w.svc.BlockAccount, bea.Account.ID)
	w.now = w.now.Add(domain.AccountClosing + time.Minute)
	if err := w.svc.RemoveDue(ctx); err != nil {
		t.Fatal(err)
	}
	if doc := w.doc(bea.Account.ID); doc != nil {
		t.Fatalf("a blocked account that was due is still there: %+v", doc)
	}
	if left := w.stored(bea.Account.ID, "bea@example.org"); len(left) != 0 {
		t.Fatalf("left of it: %v", left)
	}
	if doc := w.doc(ana.Account.ID); doc == nil || !doc.blocked() {
		t.Fatalf("the account that was kept: %+v", doc)
	}
}

// The operator asks to delete an account and cancels: the same seven days
// and the same removal as when its person asks. The record says who asked,
// the person is told by mail, and can cancel what the operator asked.
func TestTheOperatorDeletesAndKeeps(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	ana, anaCookie := w.signUp("ana@example.org")
	_, other := w.signIn("ana@example.org")
	anaWho := w.who(anaCookie)

	n := w.count()
	rec := w.operator(w.svc.DeleteAccount, ana.Account.ID)
	day := w.now.Add(7 * 24 * time.Hour)
	if rec.State != contracts.AccountClosing || !rec.ClosesAt.Equal(day) || rec.ClosingBy != contracts.ClosingByOperator || rec.Sessions != 2 {
		t.Fatalf("after the operator asked: %+v", rec)
	}
	told := w.one(n, "ana@example.org", subjectDeleting)
	for _, want := range []string{"Permaily's operator asked to delete your Permaily account.", "on 18 November 2026, 09:00 UTC", "Asked: 11 November 2026, 09:00 UTC", "Until then you can sign in at app.permaily.test and cancel the deletion.", "To ask why, write to support@permaily.com."} {
		if !strings.Contains(told.Text, want) {
			t.Fatalf("the notice lacks %q:\n%s", want, told.Text)
		}
	}
	// No device was signed out, and the person sees who asked.
	me, err := w.svc.Me(ctx, w.who(other))
	if err != nil || me.Account.State != contracts.AccountClosing || me.Account.ClosingBy != contracts.ClosingByOperator || !me.Account.ClosesAt.Equal(day) {
		t.Fatalf("what the person sees: %+v %v", me, err)
	}
	if w.listed(closingPartition)[0] != closingKey(w.doc(ana.Account.ID)) {
		t.Fatal("not in the list of closing accounts")
	}
	// Asked again, by either, the day and who asked stay.
	w.now = w.now.Add(time.Hour)
	n = w.count()
	if rec := w.operator(w.svc.DeleteAccount, ana.Account.ID); !rec.ClosesAt.Equal(day) || rec.ClosingBy != contracts.ClosingByOperator {
		t.Fatalf("asked again by the operator: %+v", rec)
	}
	if got, err := w.svc.StartDeletion(ctx, w.fresh(anaWho)); err != nil || !got.ClosesAt.Equal(day) || got.ClosingBy != contracts.ClosingByOperator {
		t.Fatalf("asked again by the person: %+v %v", got, err)
	}
	if got, _ := w.sent(n); len(got) != 0 {
		t.Fatalf("asking again sent %+v", got)
	}
	// Everything else is refused, as for a deletion the person asked for.
	if _, err := w.svc.UpdateAccount(ctx, anaWho, contracts.AccountUpdate{Name: text("Ana")}); !errors.Is(err, contracts.ErrAccountClosing) {
		t.Fatalf("a change while closing: %v", err)
	}

	// The person cancels what the operator asked.
	n = w.count()
	kept, err := w.svc.CancelDeletion(ctx, anaWho)
	if err != nil || kept.State != contracts.AccountActive || kept.ClosingBy != "" || !kept.ClosesAt.IsZero() {
		t.Fatalf("the person cancels: %+v %v", kept, err)
	}
	told = w.one(n, "ana@example.org", subjectKept)
	if !strings.Contains(told.Text, "You cancelled the deletion of your Permaily account.") || !strings.Contains(told.Text, "When: 11 November 2026, 10:00 UTC") {
		t.Fatalf("the notice: %s", told.Text)
	}
	if doc := w.doc(ana.Account.ID); doc.ClosingBy != "" || len(w.listed(closingPartition)) != 0 {
		t.Fatalf("after the person cancelled: %+v, list %v", doc, w.listed(closingPartition))
	}

	// The person asks, and the operator cannot cancel it: only the person
	// does. Nothing changes and nothing is counted.
	n = w.count()
	if got, err := w.svc.StartDeletion(ctx, w.fresh(anaWho)); err != nil || got.ClosingBy != contracts.ClosingByPerson {
		t.Fatalf("the person asks: %+v %v", got, err)
	}
	told = w.one(n, "ana@example.org", subjectDeleting)
	if !strings.Contains(told.Text, "You asked to delete your Permaily account.") || !strings.Contains(told.Text, "If it was not you, sign in and cancel it") {
		t.Fatalf("the notice: %s", told.Text)
	}
	n, version := w.count(), w.version(ana.Account.ID)
	_, counted := w.svc.OperatorActions()
	if rec, err := w.svc.KeepAccount(ctx, ana.Account.ID); !errors.Is(err, contracts.ErrNotCancellable) || rec != nil {
		t.Fatalf("the operator cancels what the person asked: %+v %v", rec, err)
	}
	if doc := w.doc(ana.Account.ID); doc.State != contracts.AccountClosing || doc.closingBy() != contracts.ClosingByPerson || w.version(ana.Account.ID) != version {
		t.Fatalf("after the refusal: %+v", doc)
	}
	if _, now := w.svc.OperatorActions(); now[ActionDeleteCancel] != counted[ActionDeleteCancel] {
		t.Fatalf("a refusal was counted: %v", now)
	}
	if got, _ := w.sent(n); len(got) != 0 {
		t.Fatalf("a refusal sent %+v", got)
	}
	if _, err := w.svc.CancelDeletion(ctx, anaWho); err != nil {
		t.Fatal(err)
	}

	// The operator asks, and the operator cancels.
	w.operator(w.svc.DeleteAccount, ana.Account.ID)
	n = w.count()
	if rec := w.operator(w.svc.KeepAccount, ana.Account.ID); rec.State != contracts.AccountActive || rec.ClosingBy != "" || !rec.ClosesAt.IsZero() {
		t.Fatalf("the operator cancels: %+v", rec)
	}
	told = w.one(n, "ana@example.org", subjectKept)
	if !strings.Contains(told.Text, "Permaily's operator cancelled the deletion of your Permaily account.") || !strings.Contains(told.Text, "To ask why, write to support@permaily.com.") {
		t.Fatalf("the notice: %s", told.Text)
	}
	// Cancelling what is not closing changes nothing and tells nobody.
	n = w.count()
	w.operator(w.svc.KeepAccount, ana.Account.ID)
	if got, _ := w.sent(n); len(got) != 0 {
		t.Fatalf("keeping an active account sent %+v", got)
	}

	// What the operator asked for is carried out as what a person asks for:
	// on its day nothing of the account is left, and nothing brings it back.
	before := w.snapshot()
	w.operator(w.svc.DeleteAccount, ana.Account.ID)
	w.now = w.now.Add(domain.AccountClosing)
	if err := w.svc.RemoveDue(ctx); err != nil {
		t.Fatal(err)
	}
	if left := w.stored(ana.Account.ID, "ana@example.org"); len(left) != 0 {
		t.Fatalf("left of the account: %v", left)
	}
	w.putBack(before)
	if _, err := w.svc.Authenticate(ctx, anaCookie); err == nil {
		t.Fatal("an account the operator deleted came back with an earlier copy of the storage")
	}
	// An account that is not there is not found by any of it.
	for what, act := range map[string]func(context.Context, string) (*contracts.AccountRecord, error){
		"block": w.svc.BlockAccount, "unblock": w.svc.UnblockAccount, "delete": w.svc.DeleteAccount,
		"keep": w.svc.KeepAccount, "close sessions": w.svc.CloseAccountSessions,
	} {
		for _, id := range []string{"", "acc_x", ana.Account.ID, newID("acc"), "../" + ana.Account.ID, newID("ses")} {
			if rec, err := act(ctx, id); !errors.Is(err, contracts.ErrNotFound) || rec != nil {
				t.Errorf("%s of %q: %+v %v", what, id, rec, err)
			}
		}
	}
	w.quiet("ana@", "example.org", ana.Account.ID)
}

// The operator signs every device out of an account.
func TestTheOperatorClosesSessions(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	ana, first := w.signUp("ana@example.org")
	_, second := w.signIn("ana@example.org")
	bea, beaCookie := w.signUp("bea@example.org")
	// More than one page of them, as whoever runs the storage could leave.
	for range 2*maxSessions + 5 {
		if _, _, err := w.svc.newSession(ctx, bea.Account.ID, "x"); err != nil {
			t.Fatal(err)
		}
		w.now = w.now.Add(time.Second)
	}
	before := w.ways(ana.Account.ID)
	n := w.count()
	if rec := w.operator(w.svc.CloseAccountSessions, ana.Account.ID); rec.Sessions != 0 || !rec.BlockedAt.IsZero() || rec.State != contracts.AccountActive {
		t.Fatalf("after closing the sessions: %+v", rec)
	}
	for _, cookie := range []string{first, second} {
		if _, err := w.svc.Authenticate(ctx, cookie); !errors.Is(err, contracts.ErrUnauthorized) {
			t.Fatalf("a session that was closed: %v", err)
		}
	}
	// It is not a block: the person signs in again, and nothing else changed.
	if got, _ := w.sent(n); len(got) != 0 {
		t.Fatalf("closing the sessions sent %+v", got)
	}
	if got := w.ways(ana.Account.ID); !reflect.DeepEqual(got, before) {
		t.Fatalf("closing the sessions changed the account: %+v", got)
	}
	w.signIn("ana@example.org")
	// Every one of them goes, however many.
	w.svc.cfg.Entries = w.mem
	if rec := w.operator(w.svc.CloseAccountSessions, bea.Account.ID); rec.Sessions != 0 {
		t.Fatalf("sessions left: %+v", rec)
	}
	if _, err := w.svc.Authenticate(ctx, beaCookie); !errors.Is(err, contracts.ErrUnauthorized) {
		t.Fatalf("a session that was closed: %v", err)
	}
	// With none, it is done all the same.
	w.operator(w.svc.CloseAccountSessions, bea.Account.ID)
}

// The operator finds the account of exactly one address.
func TestTheOperatorFindsAnAddress(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	w.on(idp.Google)
	ana, anaCookie := w.signUp("ana@example.org")
	w.addPasskey(w.who(anaCookie), webauthntest.New(webauthntest.ES256), "laptop")
	if _, err := w.linkFrom(w.who(anaCookie), person(idp.Google, "the-subject-at-google", "ana@example.org", true)); err != nil {
		t.Fatal(err)
	}
	w.signUp("anabel@example.org")

	find := func(address string) *contracts.AccountList {
		t.Helper()
		list, err := w.svc.AccountRecordByAddress(ctx, address)
		if err != nil || list.Accounts == nil || list.Next != "" {
			t.Fatalf("finding %q: %+v %v", address, list, err)
		}
		return list
	}
	// As a sign-in reads an address: trimmed, in lower case.
	for _, address := range []string{"ana@example.org", "  Ana@Example.ORG "} {
		list := find(address)
		if len(list.Accounts) != 1 || list.Unreadable != 0 {
			t.Fatalf("%q: %+v", address, list)
		}
		rec := list.Accounts[0]
		want := contracts.AccountRecord{
			ID: ana.Account.ID, State: contracts.AccountActive, Address: "ana@example.org", Language: "en",
			CreatedAt: ana.Account.CreatedAt, AddressVerifiedAt: ana.Account.CreatedAt,
			Passkeys: 1, Providers: []string{"google"}, Sessions: 1,
		}
		if !reflect.DeepEqual(rec, want) {
			t.Fatalf("%q: %+v, want %+v", address, rec, want)
		}
	}
	// Exactly that address: not a part of one, and not one like it.
	for _, address := range []string{"an@example.org", "ana@example.com", "nobody@example.org", "ana+x@example.org"} {
		if list := find(address); len(list.Accounts) != 0 || list.Unreadable != 0 {
			t.Fatalf("%q found %+v", address, list)
		}
	}
	for _, address := range []string{"", "ana", "ana@", "ana@example", "ana@example.org,bea@example.org", "Ana <ana@example.org>"} {
		if _, err := w.svc.AccountRecordByAddress(ctx, address); !errors.Is(err, contracts.ErrInvalidAddress) {
			t.Errorf("%q: %v", address, err)
		}
	}
	// It only looks: an account that is due is shown, and stays.
	if _, err := w.svc.StartDeletion(ctx, w.fresh(w.who(anaCookie))); err != nil {
		t.Fatal(err)
	}
	w.now = w.now.Add(domain.AccountClosing + time.Hour)
	if list := find("ana@example.org"); len(list.Accounts) != 1 || list.Accounts[0].State != contracts.AccountClosing || list.Accounts[0].ClosingBy != contracts.ClosingByPerson {
		t.Fatalf("an account that is due: %+v", list)
	}
	if w.doc(ana.Account.ID) == nil {
		t.Fatal("looking removed an account")
	}
	w.now = w.now.Add(-domain.AccountClosing - time.Hour)

	// The pointer proves nothing. One written by hand, or one of another
	// address copied here, finds nothing.
	pointer, err := w.mem.GetEntry(ctx, loginPartition("ana@example.org"), loginKey)
	if err != nil {
		t.Fatal(err)
	}
	copied := *pointer
	copied.Partition = loginPartition("mallory@example.org")
	w.mem.SetEntry(copied)
	w.mem.SetEntry(store.Entry{Partition: loginPartition("byhand@example.org"), Key: loginKey, Value: []byte(`{"account":"` + ana.Account.ID + `"}`)})
	for _, address := range []string{"mallory@example.org", "byhand@example.org"} {
		if list := find(address); len(list.Accounts) != 0 {
			t.Fatalf("%q found %+v", address, list)
		}
	}
	// A pointer that leads to an account with another address finds nothing.
	if err := w.svc.point(ctx, loginPartition("other@example.org"), loginKey, ana.Account.ID, 0); err != nil {
		t.Fatal(err)
	}
	if list := find("other@example.org"); len(list.Accounts) != 0 {
		t.Fatalf("a pointer the record does not bear out found %+v", list)
	}
	// One that leads to no account at all.
	if err := w.svc.point(ctx, loginPartition("gone@example.org"), loginKey, newID("acc"), 0); err != nil {
		t.Fatal(err)
	}
	if list := find("gone@example.org"); len(list.Accounts) != 0 || list.Unreadable != 0 {
		t.Fatalf("a pointer to no account found %+v", list)
	}
	// An account whose record was changed in the table is there, and nothing
	// is said of it: never "no such account".
	rec := w.record(w.newest(ana.Account.ID))
	rec.Value = []byte(strings.Replace(string(rec.Value), "ana@example.org", "eve@example.org", 1))
	w.mem.SetRecord(rec)
	if list := find("ana@example.org"); len(list.Accounts) != 0 || list.Unreadable != 1 {
		t.Fatalf("a record that does not verify: %+v", list)
	}
	if !strings.Contains(w.logs.String(), "records of accounts could not be read for the operator's tool") {
		t.Fatalf("logs:\n%s", w.logs)
	}
	// What the tool shows of the providers is which, never who there.
	w.quiet("ana@", "example.org", ana.Account.ID, "the-subject-at-google")
}

// What the operator can do changes no contact address and no way to sign in:
// after every one of the actions, on every kind of account, the address, the
// passkeys, "passkey only", the recovery codes and the providers are as they
// were, and nobody was signed in.
func TestTheOperatorsActionsChangeNoAddressAndNoWayIn(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	w.on(idp.Google)
	ana, anaCookie := w.signUp("ana@example.org")
	w.addPasskey(w.who(anaCookie), webauthntest.New(webauthntest.ES256), "laptop")
	if _, err := w.linkFrom(w.who(anaCookie), person(idp.Google, "g-ana", "ana@example.org", true)); err != nil {
		t.Fatal(err)
	}
	bea, beaCookie := w.signUp("bea@example.org")
	w.addPasskey(w.who(beaCookie), webauthntest.New(webauthntest.ES256), "phone")
	w.passkeyOnly(w.who(beaCookie))

	for _, id := range []string{ana.Account.ID, bea.Account.ID} {
		before := w.ways(id)
		for _, step := range []struct {
			name string
			act  func(context.Context, string) (*contracts.AccountRecord, error)
		}{
			{"close sessions", w.svc.CloseAccountSessions}, {"block", w.svc.BlockAccount}, {"delete", w.svc.DeleteAccount},
			{"block again", w.svc.BlockAccount}, {"keep", w.svc.KeepAccount}, {"unblock", w.svc.UnblockAccount},
			{"delete again", w.svc.DeleteAccount}, {"keep again", w.svc.KeepAccount}, {"close sessions again", w.svc.CloseAccountSessions},
		} {
			w.operator(step.act, id)
			if got := w.ways(id); !reflect.DeepEqual(got, before) {
				t.Fatalf("%s changed how the account is signed into:\n%+v\n%+v", step.name, got, before)
			}
			if list, err := w.svc.sessions(ctx, id); err != nil || len(list) != 0 {
				t.Fatalf("%s left or made a session: %+v %v", step.name, list, err)
			}
		}
		if _, err := w.svc.AccountRecordByAddress(ctx, before.Address); err != nil {
			t.Fatal(err)
		}
		if got := w.ways(id); !reflect.DeepEqual(got, before) {
			t.Fatalf("looking changed the account: %+v", got)
		}
	}
	// Nothing the operator's routes are given can do more (httpapi's
	// OperatorService): that is checked where the routes are.
}

// The invitations not used yet are listed for the operator, without their
// codes, and one can be revoked.
func TestTheOperatorListsAndRevokesInvitations(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	list := func() []contracts.Invitation {
		t.Helper()
		out, err := w.svc.Invitations(ctx)
		if err != nil || out.Invitations == nil || out.Truncated {
			t.Fatalf("the list: %+v %v", out, err)
		}
		return out.Invitations
	}
	if got := list(); len(got) != 0 {
		t.Fatalf("no invitation was made: %+v", got)
	}
	first, err := w.svc.CreateInvitations(ctx, 2)
	if err != nil || len(first.IDs) != 2 || first.IDs[0] == first.IDs[1] {
		t.Fatalf("two invitations: %+v %v", first, err)
	}
	w.now = w.now.Add(time.Hour)
	later, err := w.svc.CreateInvitations(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	got := list()
	if len(got) != 3 || got[2].ID != later.IDs[0] || !got[2].CreatedAt.Equal(w.now) || !got[2].ExpiresAt.Equal(later.ExpiresAt) || !got[0].ExpiresAt.Equal(first.ExpiresAt) || !got[0].CreatedAt.Before(got[2].CreatedAt) {
		t.Fatalf("the list: %+v", got)
	}
	// An identifier is not the code, and the code cannot be had from it or
	// from anything else that is stored: the code is in neither.
	codes := append(append([]string{}, first.Invitations...), later.Invitations...)
	for i, id := range append(append([]string{}, first.IDs...), later.IDs...) {
		raw, _ := domain.NormalizeInvitation(codes[i])
		if !validID("inv", id) || strings.Contains(id, raw) || len(w.stored(raw, codes[i])) != 0 {
			t.Fatalf("invitation %s: its code is told or stored", id)
		}
	}
	// Two made at the same moment are told apart by nothing but the identifier.
	if got[0].ID == got[1].ID || !got[0].CreatedAt.Equal(got[1].CreatedAt) || got[0].ID > got[1].ID {
		t.Fatalf("the order of two made together: %+v", got[:2])
	}

	// One that is used is not listed any more.
	attempt, mail := w.start("ana@example.org", first.Invitations[0])
	if _, _, err := w.svc.FinishEmail(ctx, contracts.EmailFinish{Attempt: attempt, Code: code(mail)}, Browser{}); err != nil {
		t.Fatal(err)
	}
	if got := list(); len(got) != 2 {
		t.Fatalf("after one was used: %+v", got)
	}
	if len(w.listed(invitationsPartition)) != 2 {
		t.Fatalf("the entry of a used invitation stays in the list: %v", w.listed(invitationsPartition))
	}

	// One that is revoked signs nobody up: not a sign-up started after, and
	// not one that was started before and has not ended.
	second := first.Invitations[1]
	attempt, mail = w.start("bea@example.org", second)
	if err := w.svc.RevokeInvitation(ctx, first.IDs[1]); err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.svc.FinishEmail(ctx, contracts.EmailFinish{Attempt: attempt, Code: code(mail)}, Browser{}); !errors.Is(err, contracts.ErrInvalidInvitation) {
		t.Fatalf("a sign-up with a revoked invitation: %v", err)
	}
	if _, err := w.svc.StartEmail(ctx, contracts.EmailStart{Address: "bea@example.org", Invitation: second}); !errors.Is(err, contracts.ErrInvalidInvitation) {
		t.Fatalf("starting with a revoked invitation: %v", err)
	}
	if got := list(); len(got) != 1 || got[0].ID != later.IDs[0] {
		t.Fatalf("after one was revoked: %+v", got)
	}
	// Revoked twice, used already, never made, or not an identifier at all.
	for _, id := range []string{first.IDs[1], first.IDs[0], newID("inv"), "", "inv_x", later.Invitations[0], newID("acc")} {
		if err := w.svc.RevokeInvitation(ctx, id); !errors.Is(err, contracts.ErrNotFound) {
			t.Errorf("revoking %q: %v", id, err)
		}
	}
	if got := list(); len(got) != 1 {
		t.Fatalf("revoking what is not there revoked something: %+v", got)
	}

	// The list proves nothing: an entry written by hand lists nothing and
	// revokes nothing, and neither does one copied to another name.
	entry, err := w.mem.GetEntry(ctx, invitationsPartition, later.IDs[0])
	if err != nil {
		t.Fatal(err)
	}
	fake, copied := newID("inv"), *entry
	copied.Key = newID("inv")
	w.mem.SetEntry(copied)
	w.mem.SetEntry(store.Entry{Partition: invitationsPartition, Key: fake, Value: []byte(`{"v":1,"id":"` + fake + `","invitation":"` + invitationPartition("x") + `"}`)})
	if got := list(); len(got) != 1 {
		t.Fatalf("entries written by hand are listed: %+v", got)
	}
	for _, id := range []string{fake, copied.Key} {
		if err := w.svc.RevokeInvitation(ctx, id); !errors.Is(err, contracts.ErrNotFound) {
			t.Errorf("revoking an entry written by hand: %v", err)
		}
	}
	// An invitation that was changed in the table is not shown as the table
	// says, and one whose time was set later by hand ends when it was signed to.
	stored, err := w.mem.GetEntry(ctx, entryOf(t, w, later.IDs[0]), invitationKey)
	if err != nil {
		t.Fatal(err)
	}
	broken := *stored
	broken.Value = []byte(`{"v":1}`)
	w.mem.SetEntry(broken)
	if got := list(); len(got) != 0 {
		t.Fatalf("an invitation that does not verify is listed: %+v", got)
	}
	longer := *stored
	longer.ExpiresAt = w.now.Add(10 * 365 * 24 * time.Hour)
	w.mem.SetEntry(longer)
	w.now = w.now.Add(domain.InvitationLifetime - time.Minute)
	listedEntry := *entry
	listedEntry.ExpiresAt = longer.ExpiresAt
	w.mem.SetEntry(listedEntry)
	if got := list(); len(got) != 1 {
		t.Fatalf("an invitation a minute before it ends: %+v", got)
	}
	w.now = w.now.Add(time.Minute)
	if got := list(); len(got) != 0 {
		t.Fatalf("an invitation that ended is listed: %+v", got)
	}
	w.quiet(first.IDs[0], first.IDs[1], later.IDs[0], "inv_")
}

// entryOf returns where the invitation of an identifier is kept.
func entryOf(t *testing.T, w *world, id string) string {
	t.Helper()
	e, err := w.mem.GetEntry(context.Background(), invitationsPartition, id)
	if err != nil {
		t.Fatal(err)
	}
	key, err := w.svc.signingKey(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	doc := listed(key, e)
	if doc == nil {
		t.Fatal("not an entry of the list")
	}
	return doc.Invitation
}

// The list of invitations is bounded, and says when there are more.
func TestInvitationsListIsBounded(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	for range domain.MaxInvitationsListed / MaxInvitations {
		if _, err := w.svc.CreateInvitations(ctx, MaxInvitations); err != nil {
			t.Fatal(err)
		}
	}
	out, err := w.svc.Invitations(ctx)
	if err != nil || len(out.Invitations) != domain.MaxInvitationsListed || out.Truncated {
		t.Fatalf("as many as the list holds: %d %v %v", len(out.Invitations), out.Truncated, err)
	}
	if _, err := w.svc.CreateInvitations(ctx, 1); err != nil {
		t.Fatal(err)
	}
	out, err = w.svc.Invitations(ctx)
	if err != nil || len(out.Invitations) != domain.MaxInvitationsListed || !out.Truncated {
		t.Fatalf("one more than the list holds: %d %v %v", len(out.Invitations), out.Truncated, err)
	}
}

// What the operator does through the enclave is counted by its kind, for
// everybody to see, and logged by its kind: never whose account it was.
func TestTheOperatorsActionsAreCounted(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	started := w.now
	want := map[string]int{}
	for _, kind := range OperatorActionKinds {
		want[kind] = 0
	}
	check := func(what string) {
		t.Helper()
		since, got := w.svc.OperatorActions()
		if !since.Equal(started) || !reflect.DeepEqual(got, want) {
			t.Fatalf("%s: since %v, %+v, want %+v", what, since, got, want)
		}
	}
	check("at the start")
	if len(want) != 11 {
		t.Fatalf("%d kinds", len(want))
	}

	w.now = w.now.Add(time.Hour)
	inv, err := w.svc.CreateInvitations(ctx, 3)
	if err != nil {
		t.Fatal(err)
	}
	want[ActionInvitationsMade] = 3
	check("three invitations")
	attempt, mail := w.start("ana@example.org", inv.Invitations[0])
	me, _, err := w.svc.FinishEmail(ctx, contracts.EmailFinish{Attempt: attempt, Code: code(mail)}, Browser{})
	if err != nil {
		t.Fatal(err)
	}
	id := me.Account.ID
	check("a sign-up is not the operator's")

	for _, step := range []struct {
		kind string
		act  func(context.Context, string) (*contracts.AccountRecord, error)
	}{
		{ActionBlock, w.svc.BlockAccount}, {ActionBlock, w.svc.BlockAccount}, {ActionUnblock, w.svc.UnblockAccount},
		{ActionDelete, w.svc.DeleteAccount}, {ActionDeleteCancel, w.svc.KeepAccount}, {ActionSessionsClose, w.svc.CloseAccountSessions},
	} {
		w.operator(step.act, id)
		want[step.kind]++
		check(step.kind)
	}
	// What is refused was not done.
	for _, act := range []func(context.Context, string) (*contracts.AccountRecord, error){w.svc.BlockAccount, w.svc.UnblockAccount, w.svc.DeleteAccount, w.svc.KeepAccount, w.svc.CloseAccountSessions} {
		if _, err := act(ctx, newID("acc")); !errors.Is(err, contracts.ErrNotFound) {
			t.Fatal(err)
		}
	}
	if err := w.svc.RevokeInvitation(ctx, newID("inv")); !errors.Is(err, contracts.ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := w.svc.CreateInvitations(ctx, 0); err == nil {
		t.Fatal("no invitations asked")
	}
	check("what was refused")
	if err := w.svc.RevokeInvitation(ctx, inv.IDs[1]); err != nil {
		t.Fatal(err)
	}
	want[ActionInvitationRevoked]++
	// One that was used already is not revoked.
	if err := w.svc.RevokeInvitation(ctx, inv.IDs[0]); !errors.Is(err, contracts.ErrNotFound) {
		t.Fatal(err)
	}
	check("an invitation revoked")
	w.on(idp.Google, idp.GitHub)
	want[ActionProviderAppSet] = 2
	if err := w.svc.RemoveProviderApp(ctx, "google"); err != nil {
		t.Fatal(err)
	}
	if err := w.svc.RemoveProviderApp(ctx, "google"); err != nil { // not set: nothing removed
		t.Fatal(err)
	}
	want[ActionProviderAppRemove] = 1
	check("the providers' applications")
	// What a person does to their own account is not counted here.
	w.signIn("ana@example.org")
	check("a sign-in")

	// What is handed out is a copy.
	_, got := w.svc.OperatorActions()
	got[ActionBlock] = 99
	check("after the copy was written to")

	// Logged by kind and count, in fixed words.
	line := regexp.MustCompile(`msg="accounts: the operator acted" kind=([a-z_]+) count=(\d+)$`)
	logged := map[string]int{}
	for _, l := range strings.Split(strings.TrimSpace(w.logs.String()), "\n") {
		if g := line.FindStringSubmatch(l); g != nil {
			logged[g[1]]++
		} else if strings.Contains(l, "the operator acted") {
			t.Errorf("a log line with more than the kind: %s", l)
		}
	}
	for kind, n := range want {
		times := n
		if kind == ActionInvitationsMade {
			times = 1 // three in one request
		}
		if logged[kind] != times {
			t.Errorf("%s logged %d times, want %d", kind, logged[kind], times)
		}
	}
	w.quiet("ana@", "example.org", id, inv.IDs[0], inv.IDs[1], "inv_", "acc_")

	// Another enclave counts what it did, from when it started.
	w.now = w.now.Add(time.Hour)
	other := w.enclave()
	if since, got := other.svc.OperatorActions(); !since.Equal(w.now) || got[ActionBlock] != 0 || len(got) != len(want) {
		t.Fatalf("another enclave: since %v, %+v", since, got)
	}
}

// The storage failing fails what the operator asked, and never as "no such
// account".
func TestOperatorFailures(t *testing.T) {
	down := errors.New("down")
	setup := func(t *testing.T) (*world, *failing, string, context.Context) {
		w := newWorld(t)
		me, _ := w.signUp("ana@example.org")
		w.signIn("ana@example.org")
		f := &failing{Records: w.svc.cfg.Records, Entries: w.mem, Keys: w.svc.cfg.Keys, fail: map[string]error{}, skip: map[string]int{}}
		w.svc.cfg.Records, w.svc.cfg.Entries, w.svc.cfg.Keys = f, f, f
		return w, f, me.Account.ID, context.Background()
	}
	actions := func(w *world) map[string]func(context.Context, string) (*contracts.AccountRecord, error) {
		return map[string]func(context.Context, string) (*contracts.AccountRecord, error){
			"block": w.svc.BlockAccount, "unblock": w.svc.UnblockAccount, "delete": w.svc.DeleteAccount,
			"keep": w.svc.KeepAccount, "close sessions": w.svc.CloseAccountSessions,
		}
	}
	t.Run("the record cannot be read", func(t *testing.T) {
		w, f, id, ctx := setup(t)
		f.fail["get record"] = down
		for what, act := range actions(w) {
			if rec, err := act(ctx, id); !errors.Is(err, down) || rec != nil {
				t.Errorf("%s: %+v %v", what, rec, err)
			}
		}
		if _, counted := w.svc.OperatorActions(); counted[ActionBlock]+counted[ActionDelete]+counted[ActionSessionsClose] != 0 {
			t.Fatalf("counted what failed: %+v", counted)
		}
	})
	t.Run("the record cannot be written", func(t *testing.T) {
		w, f, id, ctx := setup(t)
		f.fail["put record"] = down
		for _, what := range []string{"block", "delete"} {
			if rec, err := actions(w)[what](ctx, id); !errors.Is(err, down) || rec != nil {
				t.Errorf("%s: %+v %v", what, rec, err)
			}
		}
		delete(f.fail, "put record")
		w.operator(w.svc.BlockAccount, id)
		w.operator(w.svc.DeleteAccount, id)
		f.fail["put record"] = down
		for _, what := range []string{"unblock", "keep"} {
			if rec, err := actions(w)[what](ctx, id); !errors.Is(err, down) || rec != nil {
				t.Errorf("%s: %+v %v", what, rec, err)
			}
		}
		if doc := w.doc(id); !doc.blocked() || doc.State != contracts.AccountClosing {
			t.Fatalf("after writes that failed: %+v", doc)
		}
	})
	t.Run("the sessions cannot be listed or closed", func(t *testing.T) {
		for _, op := range []string{"list entries", "delete entry"} {
			w, f, id, ctx := setup(t)
			f.fail[op] = down
			if rec, err := w.svc.CloseAccountSessions(ctx, id); !errors.Is(err, down) || rec != nil {
				t.Errorf("%s, closing sessions: %+v %v", op, rec, err)
			}
			if rec, err := w.svc.BlockAccount(ctx, id); !errors.Is(err, down) || rec != nil {
				t.Errorf("%s, blocking: %+v %v", op, rec, err)
			}
			// It is blocked all the same, and its sessions are nothing: asking
			// again ends what was left.
			if doc := w.doc(id); !doc.blocked() {
				t.Fatalf("%s: not blocked", op)
			}
			delete(f.fail, op)
			if rec := w.operator(w.svc.BlockAccount, id); rec.Sessions != 0 {
				t.Fatalf("%s: sessions left after blocking again: %+v", op, rec)
			}
		}
	})
	t.Run("the sessions cannot be counted for the answer", func(t *testing.T) {
		w, f, id, ctx := setup(t)
		w.operator(w.svc.BlockAccount, id)
		// The first listing closes the sessions, the second counts them.
		f.fail["list entries"], f.skip["list entries"] = down, 1
		if rec, err := w.svc.BlockAccount(ctx, id); !errors.Is(err, down) || rec != nil {
			t.Fatalf("%+v %v", rec, err)
		}
	})
	t.Run("finding an address", func(t *testing.T) {
		w, f, id, ctx := setup(t)
		for _, op := range []string{"get entry", "derive"} {
			f.fail[op] = down
			if list, err := w.svc.AccountRecordByAddress(ctx, "ana@example.org"); !errors.Is(err, down) || list != nil {
				t.Errorf("%s: %+v %v", op, list, err)
			}
			delete(f.fail, op)
		}
		// The account is there and cannot be read: counted, never "none".
		for _, op := range []string{"get record", "list entries"} {
			f.fail[op] = down
			if list, err := w.svc.AccountRecordByAddress(ctx, "ana@example.org"); err != nil || len(list.Accounts) != 0 || list.Unreadable != 1 {
				t.Errorf("%s: %+v %v", op, list, err)
			}
			delete(f.fail, op)
		}
		w.quiet("ana@", id)
	})
	t.Run("invitations", func(t *testing.T) {
		w, f, _, ctx := setup(t)
		inv, err := w.svc.CreateInvitations(ctx, 2)
		if err != nil {
			t.Fatal(err)
		}
		for _, op := range []string{"derive", "list entries", "get entry"} {
			f.fail[op] = down
			if out, err := w.svc.Invitations(ctx); !errors.Is(err, down) || out != nil {
				t.Errorf("listing, %s: %+v %v", op, out, err)
			}
			delete(f.fail, op)
		}
		for _, op := range []string{"get entry", "derive", "delete entry"} {
			f.fail[op] = down
			if err := w.svc.RevokeInvitation(ctx, inv.IDs[0]); !errors.Is(err, down) {
				t.Errorf("revoking, %s: %v", op, err)
			}
			delete(f.fail, op)
		}
		// The invitation is deleted and its entry of the list is not: it is
		// revoked, and counted, and asked again it is gone.
		f.fail["delete "+invitationsPartition] = down
		if err := w.svc.RevokeInvitation(ctx, inv.IDs[0]); !errors.Is(err, down) {
			t.Fatalf("the entry of the list could not be deleted: %v", err)
		}
		delete(f.fail, "delete "+invitationsPartition)
		if _, counted := w.svc.OperatorActions(); counted[ActionInvitationRevoked] != 1 {
			t.Fatalf("a revoked invitation was not counted: %+v", counted)
		}
		if _, err := w.svc.StartEmail(ctx, contracts.EmailStart{Address: "bea@example.org", Invitation: inv.Invitations[0]}); !errors.Is(err, contracts.ErrInvalidInvitation) {
			t.Fatalf("a revoked invitation: %v", err)
		}
		if err := w.svc.RevokeInvitation(ctx, inv.IDs[0]); !errors.Is(err, contracts.ErrNotFound) {
			t.Fatalf("revoked again: %v", err)
		}
		if out, err := w.svc.Invitations(ctx); err != nil || len(out.Invitations) != 1 {
			t.Fatalf("the list: %+v %v", out, err)
		}
		// Making them: the ones made before the storage failed are made, and
		// counted.
		_, counted := w.svc.OperatorActions()
		made := counted[ActionInvitationsMade]
		f.fail["put entry"], f.skip["put entry"] = down, 4
		if out, err := w.svc.CreateInvitations(ctx, 5); !errors.Is(err, down) || out != nil {
			t.Fatalf("making invitations with the storage failing: %+v %v", out, err)
		}
		delete(f.fail, "put entry")
		if _, counted := w.svc.OperatorActions(); counted[ActionInvitationsMade] != made+2 {
			t.Fatalf("invitations made before a failure: %+v", counted)
		}
		if out, err := w.svc.Invitations(ctx); err != nil || len(out.Invitations) != 3 {
			t.Fatalf("the list: %+v %v", out, err)
		}
	})
}
