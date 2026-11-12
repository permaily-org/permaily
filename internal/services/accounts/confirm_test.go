package accounts

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/domain"
	"github.com/permaily-org/permaily/internal/envelope"
	"github.com/permaily-org/permaily/internal/idp"
	"github.com/permaily-org/permaily/internal/store"
	"github.com/permaily-org/permaily/internal/webauthn/webauthntest"
)

// confirmByMail confirms the session behind a cookie with the code sent to
// the account's address, and returns the session as it is now.
func (w *world) confirmByMail(svc *Service, cookie string) *Authenticated {
	w.t.Helper()
	who := w.who(cookie)
	started, err := svc.StartConfirmEmail(context.Background(), who)
	if err != nil {
		w.t.Fatalf("starting to confirm: %v", err)
	}
	svc.Wait()
	sent := w.out.Sent()
	if _, err := svc.FinishConfirmEmail(context.Background(), who, contracts.ConfirmFinish{Attempt: started.Attempt, Code: code(&sent[len(sent)-1])}); err != nil {
		w.t.Fatalf("confirming: %v", err)
	}
	return w.who(cookie)
}

// confirmWith confirms the session behind who with a passkey.
func (w *world) confirmWith(svc *Service, who *Authenticated, auth *webauthntest.Authenticator, id []byte) (*contracts.Me, error) {
	w.t.Helper()
	started, err := svc.StartConfirmPasskey(context.Background(), who)
	if err != nil {
		return nil, err
	}
	in := contracts.PasskeySignIn{Ceremony: started.Ceremony}
	in.Credential.RawID = id
	r := &in.Credential.Response
	r.ClientDataJSON, r.AuthenticatorData, r.Signature, r.UserHandle = auth.Get(started.Options.RPID, testOrigin, started.Options.Challenge, id)
	return svc.FinishConfirmPasskey(context.Background(), who, in)
}

// later moves the clock past the proof a sign-in gave.
func (w *world) later() { w.now = w.now.Add(domain.ConfirmationLifetime + time.Second) }

// A sign-in is a proof for ten minutes. Then the person confirms again, by
// mail or with a passkey, and the proof lasts ten minutes more; it is kept in
// the session's entry, so every request reads it.
func TestConfirmingAgain(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	me, cookie := w.signUp("ana@example.org")
	if want := w.now.Add(10 * time.Minute); !me.Session.ConfirmedUntil.Equal(want) {
		t.Fatalf("a sign-in confirms until %v", me.Session.ConfirmedUntil)
	}
	who := w.who(cookie)
	if got, _ := w.svc.Me(ctx, who); !got.Session.ConfirmedUntil.Equal(me.Session.ConfirmedUntil) {
		t.Fatalf("me: %+v", got.Session)
	}
	ways, err := w.svc.ConfirmWays(ctx, who)
	if err != nil || !slices.Equal(ways.Ways, []string{"email"}) || !ways.ConfirmedUntil.Equal(me.Session.ConfirmedUntil) {
		t.Fatalf("ways while fresh: %+v %v", ways, err)
	}
	// The other sessions do not say it: it is the one that asks that counts.
	_, other := w.signIn("ana@example.org")
	list, _ := w.svc.Sessions(ctx, w.who(other))
	for _, s := range list.Sessions {
		if s.Current == s.ConfirmedUntil.IsZero() {
			t.Fatalf("the sessions: %+v", list.Sessions)
		}
	}

	w.later()
	who = w.who(cookie)
	if !who.Session.ConfirmedUntil.IsZero() {
		t.Fatalf("still fresh: %+v", who.Session)
	}
	if ways, _ := w.svc.ConfirmWays(ctx, who); !ways.ConfirmedUntil.IsZero() {
		t.Fatalf("ways after: %+v", ways)
	}
	if _, err := w.svc.StartPasskey(ctx, who); !errors.Is(err, contracts.ErrConfirmationNeeded) {
		t.Fatalf("a passkey without a fresh proof: %v", err)
	}
	if _, err := w.svc.StartConfirmPasskey(ctx, who); !errors.Is(err, contracts.ErrPasskeyNeeded) {
		t.Fatalf("a passkey to confirm with, without one: %v", err)
	}

	// By mail: a code and no link, to the contact address.
	n := w.count()
	started, err := w.svc.StartConfirmEmail(ctx, who)
	if err != nil || started.ExpiresIn != 600 || !validID("att", started.Attempt) {
		t.Fatalf("starting: %+v %v", started, err)
	}
	one := w.one(n, "ana@example.org", "Confirm it is you on Permaily")
	m := &one
	if code(m) == "" || strings.Contains(m.Text, "http") || !strings.Contains(m.Text, "sign in at app.permaily.test, open Settings") {
		t.Fatalf("the message:\n%s", m.Text)
	}
	if _, err := w.svc.FinishConfirmEmail(ctx, who, contracts.ConfirmFinish{Attempt: started.Attempt, Code: w.wrongCode(m)}); !errors.Is(err, contracts.ErrInvalidCode) {
		t.Fatalf("a wrong code: %v", err)
	}
	w.now = w.now.Add(time.Minute)
	got, err := w.svc.FinishConfirmEmail(ctx, who, contracts.ConfirmFinish{Attempt: started.Attempt, Code: code(m)})
	if want := w.now.Add(10 * time.Minute); err != nil || !got.Session.ConfirmedUntil.Equal(want) || got.Account.ID != me.Account.ID || !got.Session.Current {
		t.Fatalf("confirmed: %+v %v", got, err)
	}
	if _, err := w.svc.FinishConfirmEmail(ctx, who, contracts.ConfirmFinish{Attempt: started.Attempt, Code: code(m)}); !errors.Is(err, contracts.ErrInvalidCode) {
		t.Fatalf("the code twice: %v", err)
	}
	// Every request reads it from the session's entry.
	who = w.who(cookie)
	if !who.Session.ConfirmedUntil.Equal(got.Session.ConfirmedUntil) {
		t.Fatalf("read back: %+v", who.Session)
	}
	auth := webauthntest.New(webauthntest.ES256)
	key := w.addPasskey(who, auth, "laptop")
	other2 := webauthntest.New(webauthntest.ES256)
	second := w.addPasskey(who, other2, "phone")
	if ways, _ := w.svc.ConfirmWays(ctx, who); !slices.Equal(ways.Ways, []string{"passkey", "email"}) {
		t.Fatalf("ways with a passkey: %+v", ways)
	}

	// With a passkey: the options name the account's own.
	w.later()
	who = w.who(cookie)
	req, err := w.svc.StartConfirmPasskey(ctx, who)
	if err != nil || len(req.Options.AllowCredentials) != 2 || !slices.Equal(req.Options.AllowCredentials[0].ID, key.ID) ||
		!slices.Equal(req.Options.AllowCredentials[1].ID, second.ID) || req.Options.RPID != testRP || req.Options.UserVerification != "required" {
		t.Fatalf("the options: %+v %v", req, err)
	}
	if got, err := w.confirmWith(w.svc, who, auth, key.ID); err != nil || !got.Session.ConfirmedUntil.Equal(w.now.Add(10*time.Minute)) {
		t.Fatalf("confirmed with a passkey: %+v %v", got, err)
	}
	who = w.who(cookie)
	w.passkeyOnly(who)
	if ways, _ := w.svc.ConfirmWays(ctx, who); !slices.Equal(ways.Ways, []string{"passkey"}) {
		t.Fatalf("ways with passkey only: %+v", ways)
	}
	if _, err := w.svc.StartConfirmEmail(ctx, who); !errors.Is(err, contracts.ErrPasskeyRequired) {
		t.Fatalf("by mail with passkey only: %v", err)
	}

	// A closing account confirms too; a blocked one is nobody's.
	if _, err := w.svc.StartDeletion(ctx, who); err != nil {
		t.Fatal(err)
	}
	w.later()
	who = w.who(cookie)
	if _, err := w.confirmWith(w.svc, who, auth, key.ID); err != nil {
		t.Fatalf("confirming while closing: %v", err)
	}
	w.operator(w.svc.BlockAccount, me.Account.ID)
	for what, err := range map[string]error{
		"ways":          second2(w.svc.ConfirmWays(ctx, who)),
		"passkey start": second2(w.svc.StartConfirmPasskey(ctx, who)),
		"email start":   second2(w.svc.StartConfirmEmail(ctx, who)),
		"email finish":  second2(w.svc.FinishConfirmEmail(ctx, who, contracts.ConfirmFinish{Attempt: newID("att"), Code: "123456"})),
	} {
		if !errors.Is(err, contracts.ErrUnauthorized) {
			t.Errorf("%s for a blocked account: %v", what, err)
		}
	}
	w.quiet("ana@", "example.org", me.Account.ID, "att_", "cer_")
}

func second2[T any](_ T, err error) error { return err }

// What decides who gets into an account is refused, and changes nothing,
// while the session's proof is not fresh: no way in is added or removed,
// "passkey only" stays as it is, no code, no address and no deletion.
func TestASessionThatIsNotFreshlyConfirmedChangesNoWayIn(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	w.on(idp.Google)
	me, cookie := w.signUp("ana@example.org")
	who := w.who(cookie)
	auth := webauthntest.New(webauthntest.ES256)
	key := w.addPasskey(who, auth, "laptop")
	if _, err := w.linkFrom(who, person(idp.Google, "g-ana", "ana@example.org", true)); err != nil {
		t.Fatal(err)
	}
	w.later()
	who = w.who(cookie)
	before, version, n := w.ways(me.Account.ID), w.version(me.Account.ID), w.count()
	for what, err := range map[string]error{
		"a passkey":         second2(w.svc.StartPasskey(ctx, who)),
		"removing one":      w.svc.RemovePasskey(ctx, who, key.ID.String()),
		"passkey only on":   second2(w.svc.SetPasskeyOnly(ctx, who, contracts.PasskeyOnly{On: true})),
		"passkey only off":  second2(w.svc.SetPasskeyOnly(ctx, who, contracts.PasskeyOnly{On: false})),
		"recovery codes":    second2(w.svc.NewRecoveryCodes(ctx, who)),
		"linking":           thirdErr(w.svc.StartProvider(ctx, who, contracts.ProviderStart{Provider: "google"})),
		"unlinking":         w.svc.UnlinkProvider(ctx, who, "google"),
		"a new address":     second2(w.svc.StartAddressChange(ctx, who, contracts.AddressStart{Address: "new@example.org"})),
		"deleting":          second2(w.svc.StartDeletion(ctx, who)),
		"the name, nothing": nil,
	} {
		if what == "the name, nothing" {
			continue
		}
		if !errors.Is(err, contracts.ErrConfirmationNeeded) {
			t.Errorf("%s: %v", what, err)
		}
	}
	if after := w.ways(me.Account.ID); !waysEqual(before, after) || w.version(me.Account.ID) != version || w.doc(me.Account.ID).State != contracts.AccountActive {
		t.Fatalf("the account changed: %+v", after)
	}
	if got, _ := w.sent(n); len(got) != 0 {
		t.Fatalf("sent %+v", got)
	}
	// What only protects needs nothing more than the session.
	if _, err := w.svc.UpdateAccount(ctx, who, contracts.AccountUpdate{Name: text("Ana")}); err != nil {
		t.Fatalf("the name: %v", err)
	}
	_, other := w.signIn("ana@example.org")
	if err := w.svc.CloseSession(ctx, who, w.who(other).Session.ID); err != nil {
		t.Fatalf("closing a session: %v", err)
	}
	// Confirmed, the same changes go through.
	who = w.confirmByMail(w.svc, cookie)
	if err := w.svc.UnlinkProvider(ctx, who, "google"); err != nil {
		t.Fatalf("unlinking once confirmed: %v", err)
	}
	// And the proof ends ten minutes after it was made, to the second.
	w.now = w.now.Add(domain.ConfirmationLifetime - time.Second)
	if _, err := w.svc.StartPasskey(ctx, w.who(cookie)); err != nil {
		t.Fatalf("a second before the end: %v", err)
	}
	w.now = w.now.Add(time.Second)
	if _, err := w.svc.StartPasskey(ctx, w.who(cookie)); !errors.Is(err, contracts.ErrConfirmationNeeded) {
		t.Fatalf("at the end: %v", err)
	}
}

func thirdErr[A, B any](_ A, _ B, err error) error { return err }

func waysEqual(a, b ways) bool {
	return a.Address == b.Address && a.VerifiedAt.Equal(b.VerifiedAt) && len(a.Passkeys) == len(b.Passkeys) && a.PasskeyOnly == b.PasskeyOnly &&
		len(a.Codes) == len(b.Codes) && len(a.Providers) == len(b.Providers) && a.Moving == b.Moving
}

// An attempt or a ceremony made to confirm a session works for that and for
// nothing else: it signs nobody in, changes no address and confirms no other
// session; and nothing made for those confirms. Tried as something else it
// is no attempt at all, and still works for what it was made for.
func TestAnAttemptMadeToConfirmSignsNobodyInAndChangesNoAddress(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	me, cookie := w.signUp("ana@example.org")
	_, otherCookie := w.signIn("ana@example.org")
	bea, beaCookie := w.signUp("bea@example.org")
	w.later()
	who, other, beaWho := w.who(cookie), w.who(otherCookie), w.who(beaCookie)

	started, err := w.svc.StartConfirmEmail(ctx, who)
	if err != nil {
		t.Fatal(err)
	}
	w.svc.Wait()
	sent := w.out.Sent()
	m := sent[len(sent)-1]
	sessions, _ := w.svc.sessions(ctx, me.Account.ID, nil)
	finish := contracts.EmailFinish{Attempt: started.Attempt, Code: code(&m)}
	if got, cookie, err := w.svc.FinishEmail(ctx, finish, Browser{UserAgent: "x"}); !errors.Is(err, contracts.ErrInvalidCode) || got != nil || cookie != "" {
		t.Fatalf("signing in with it: %+v %v", got, err)
	}
	if _, err := w.svc.LookEmail(ctx, contracts.EmailLook{Attempt: started.Attempt, Token: "x"}); !errors.Is(err, contracts.ErrInvalidCode) {
		t.Fatalf("looking at it: %v", err)
	}
	if got, err := w.svc.FinishAddressChange(ctx, w.fresh(who), contracts.AddressFinish{Attempt: started.Attempt, Code: code(&m)}); !errors.Is(err, contracts.ErrInvalidCode) || got != nil {
		t.Fatalf("changing the address with it: %+v %v", got, err)
	}
	for _, s := range []*Authenticated{other, beaWho} {
		if got, err := w.svc.FinishConfirmEmail(ctx, s, contracts.ConfirmFinish{Attempt: started.Attempt, Code: code(&m)}); !errors.Is(err, contracts.ErrInvalidCode) || got != nil {
			t.Fatalf("confirming another session with it: %+v %v", got, err)
		}
	}
	if now, _ := w.svc.sessions(ctx, me.Account.ID, nil); len(now) != len(sessions) || w.doc(me.Account.ID).Address != "ana@example.org" {
		t.Fatalf("something changed: %d sessions", len(now))
	}
	if w.who(otherCookie).Session.ConfirmedUntil.After(w.now) || w.who(beaCookie).Session.ConfirmedUntil.After(w.now) {
		t.Fatal("another session was confirmed")
	}
	// It still confirms its own session.
	if _, err := w.svc.FinishConfirmEmail(ctx, who, contracts.ConfirmFinish{Attempt: started.Attempt, Code: code(&m)}); err != nil {
		t.Fatalf("its own session: %v", err)
	}

	// Nothing made for a sign-in or an address confirms.
	attempt, signIn := w.start("ana@example.org", "")
	if _, err := w.svc.FinishConfirmEmail(ctx, other, contracts.ConfirmFinish{Attempt: attempt, Code: code(signIn)}); !errors.Is(err, contracts.ErrInvalidCode) {
		t.Fatalf("a sign-in's code confirms: %v", err)
	}
	addr, mail := w.askFor(w.svc, other, "ana.new@example.org")
	if _, err := w.svc.FinishConfirmEmail(ctx, other, contracts.ConfirmFinish{Attempt: addr, Code: code(mail)}); !errors.Is(err, contracts.ErrInvalidCode) {
		t.Fatalf("an address's code confirms: %v", err)
	}
	if w.who(otherCookie).Session.ConfirmedUntil.After(w.now) {
		t.Fatal("confirmed by what was not made for it")
	}
	// And both still work for what they were made for.
	if _, _, err := w.svc.FinishEmail(ctx, contracts.EmailFinish{Attempt: attempt, Code: code(signIn)}, Browser{}); err != nil {
		t.Fatalf("the sign-in after: %v", err)
	}

	// Ceremonies: one made to confirm signs nobody in and makes no passkey,
	// and confirms only the session that started it, with that account's
	// passkey.
	w.later()
	who, other, beaWho = w.who(cookie), w.who(otherCookie), w.who(beaCookie)
	auth := webauthntest.New(webauthntest.ES256)
	key := w.addPasskey(w.fresh(who), auth, "laptop")
	beaAuth := webauthntest.New(webauthntest.ES256)
	beaKey := w.addPasskey(w.fresh(beaWho), beaAuth, "bea's")
	answer := func(s *Authenticated, a *webauthntest.Authenticator, id []byte) contracts.PasskeySignIn {
		req, err := w.svc.StartConfirmPasskey(ctx, s)
		if err != nil {
			t.Fatal(err)
		}
		in := contracts.PasskeySignIn{Ceremony: req.Ceremony}
		in.Credential.RawID = id
		r := &in.Credential.Response
		r.ClientDataJSON, r.AuthenticatorData, r.Signature, r.UserHandle = a.Get(testRP, testOrigin, req.Options.Challenge, id)
		return in
	}
	if got, cookie, err := w.svc.FinishPasskeySignIn(ctx, answer(who, auth, key.ID), Browser{}); !errors.Is(err, contracts.ErrInvalidPasskey) || got != nil || cookie != "" {
		t.Fatalf("signing in with a ceremony made to confirm: %v", err)
	}
	if _, err := w.svc.FinishConfirmPasskey(ctx, other, answer(who, auth, key.ID)); !errors.Is(err, contracts.ErrInvalidPasskey) {
		t.Fatalf("confirming another session: %v", err)
	}
	if _, err := w.svc.FinishConfirmPasskey(ctx, who, answer(who, beaAuth, beaKey.ID)); !errors.Is(err, contracts.ErrInvalidPasskey) {
		t.Fatalf("with another account's passkey: %v", err)
	}
	wrong := answer(who, auth, key.ID)
	wrong.Credential.Response.Signature[len(wrong.Credential.Response.Signature)-1] ^= 1
	if _, err := w.svc.FinishConfirmPasskey(ctx, who, wrong); !errors.Is(err, contracts.ErrInvalidPasskey) {
		t.Fatalf("a wrong signature: %v", err)
	}
	unknown := answer(who, auth, key.ID)
	unknown.Credential.RawID = []byte("not a passkey of the account")
	if _, err := w.svc.FinishConfirmPasskey(ctx, who, unknown); !errors.Is(err, contracts.ErrInvalidPasskey) {
		t.Fatalf("a passkey the account does not have: %v", err)
	}
	made := w.create(w.fresh(who), auth, "x")
	if _, err := w.svc.FinishConfirmPasskey(ctx, who, contracts.PasskeySignIn{Ceremony: made.Ceremony}); !errors.Is(err, contracts.ErrInvalidPasskey) {
		t.Fatalf("the ceremony of making one confirms: %v", err)
	}
	if _, err := w.svc.FinishConfirmPasskey(ctx, who, w.assert(auth, key.ID)); !errors.Is(err, contracts.ErrInvalidPasskey) {
		t.Fatalf("a sign-in's ceremony confirms: %v", err)
	}
	if !w.who(cookie).Session.ConfirmedUntil.IsZero() || !w.who(otherCookie).Session.ConfirmedUntil.IsZero() || len(w.doc(me.Account.ID).Passkeys) != 1 {
		t.Fatal("something was confirmed or made")
	}
	if _, err := w.confirmWith(w.svc, who, auth, key.ID); err != nil {
		t.Fatalf("the right one: %v", err)
	}
	_ = bea
}

// Confirming by mail is counted like the rest: five messages an hour for the
// account, five for the address with the sign-in messages, and no code to an
// address that took ten wrong codes today.
func TestConfirmingByMailIsBounded(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	_, cookie := w.signUp("ana@example.org")
	w.later()
	who := w.who(cookie)
	for i := range domain.EmailStartsPerHour {
		if _, err := w.svc.StartConfirmEmail(ctx, who); err != nil {
			t.Fatalf("request %d: %v", i, err)
		}
	}
	if _, err := w.svc.StartConfirmEmail(ctx, who); !errors.Is(err, contracts.ErrTooManyRequests) {
		t.Fatalf("a sixth: %v", err)
	}
	w.svc.Wait()
	// The address had one sign-up message this hour: four more were sent.
	if n := len(w.out.Sent()); n != domain.EmailStartsPerHour {
		t.Fatalf("%d messages", n)
	}
	w.now = w.now.Add(time.Hour + time.Second)
	who = w.who(cookie)
	for range wrongCodesPerDay / domain.EmailCodeTries {
		started, err := w.svc.StartConfirmEmail(ctx, who)
		if err != nil {
			t.Fatal(err)
		}
		w.svc.Wait()
		sent := w.out.Sent()
		for range domain.EmailCodeTries {
			_, _ = w.svc.FinishConfirmEmail(ctx, who, contracts.ConfirmFinish{Attempt: started.Attempt, Code: w.wrongCode(&sent[len(sent)-1])})
		}
	}
	if _, err := w.svc.StartConfirmEmail(ctx, who); !errors.Is(err, contracts.ErrTooManyRequests) {
		t.Fatalf("after ten wrong codes: %v", err)
	}
	// A sign-in to that address carries only the link, as it does after ten
	// wrong sign-in codes.
	if _, m := w.start("ana@example.org", ""); m == nil || code(m) != "" {
		t.Fatalf("the sign-in after: %+v", m)
	}
}

// The proof is written where the session is, at the version read: when
// another request of the session touched it meanwhile, it is read again.
// A storage that fails, or a session that went, is said as such.
func TestConfirmFailures(t *testing.T) {
	ctx := context.Background()
	setup := func(t *testing.T) (*world, *failing, string) {
		w := newWorld(t)
		_, cookie := w.signUp("ana@example.org")
		w.later()
		f := &failing{Records: w.svc.cfg.Records, Entries: w.mem, Keys: w.svc.cfg.Keys, fail: map[string]error{}, skip: map[string]int{}}
		w.svc.cfg.Records, w.svc.cfg.Entries, w.svc.cfg.Keys = f, f, f
		return w, f, cookie
	}
	down := errors.New("down")
	t.Run("the storage", func(t *testing.T) {
		w, f, cookie := setup(t)
		who := w.who(cookie)
		for _, op := range []string{"get entry", "put entry", "derive"} {
			f.fail[op] = down
			if _, err := w.svc.confirm(ctx, who, "test"); !errors.Is(err, down) {
				t.Errorf("%s: %v", op, err)
			}
			delete(f.fail, op)
		}
		f.fail["get record"] = down
		for what, err := range map[string]error{
			"ways":           second2(w.svc.ConfirmWays(ctx, who)),
			"passkey start":  second2(w.svc.StartConfirmPasskey(ctx, who)),
			"passkey finish": second2(w.svc.FinishConfirmPasskey(ctx, who, w.answering(who))),
			"email start":    second2(w.svc.StartConfirmEmail(ctx, who)),
			"email finish":   second2(w.svc.FinishConfirmEmail(ctx, who, contracts.ConfirmFinish{})),
		} {
			if !errors.Is(err, down) {
				t.Errorf("%s with the records down: %v", what, err)
			}
		}
	})
	t.Run("signed out meanwhile", func(t *testing.T) {
		w, _, cookie := setup(t)
		who := w.who(cookie)
		if err := w.svc.CloseSession(ctx, who, who.Session.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := w.svc.confirm(ctx, who, "test"); !errors.Is(err, contracts.ErrUnauthorized) {
			t.Fatalf("%v", err)
		}
		// An entry put in its place by hand is no session.
		e := store.Entry{Partition: accountPartition(who.AccountID), Key: sessionPrefix + who.Session.ID, Value: []byte("{}")}
		w.mem.SetEntry(e)
		if _, err := w.svc.confirm(ctx, who, "test"); !errors.Is(err, contracts.ErrUnauthorized) {
			t.Fatalf("an entry written by hand: %v", err)
		}
	})
	t.Run("touched meanwhile", func(t *testing.T) {
		w := newWorld(t)
		_, cookie := w.signUp("ana@example.org")
		w.later()
		who := w.who(cookie)
		// Another request writes the session between the read and the write,
		// once, and then every time.
		e := &touching{Entries: w.mem, before: func() { w.touch(who) }}
		w.svc.cfg.Entries = e
		if got, err := w.svc.confirm(ctx, who, "test"); err != nil || got.Session.ConfirmedUntil.IsZero() {
			t.Fatalf("after one touch: %+v %v", got, err)
		}
		e.before, e.always = func() { w.touch(who) }, true
		if _, err := w.svc.confirm(ctx, who, "test"); err == nil || !strings.Contains(err.Error(), "keeps changing") {
			t.Fatalf("always touched: %v", err)
		}
	})
}

// touching runs before before a session is written, once unless always.
type touching struct {
	store.Entries
	before func()
	always bool
}

func (t *touching) PutEntry(ctx context.Context, e *store.Entry) error {
	if f := t.before; f != nil && strings.HasPrefix(e.Key, sessionPrefix) {
		if !t.always {
			t.before = nil
		}
		f()
	}
	return t.Entries.PutEntry(ctx, e)
}

// answering is an answer to a ceremony that confirms the session behind who,
// as far as it gets before the account is read.
func (w *world) answering(who *Authenticated) contracts.PasskeySignIn {
	id, _, _ := w.svc.begin(context.Background(), confirmFor(who))
	in := contracts.PasskeySignIn{Ceremony: id}
	in.Credential.Response.UserHandle = []byte(who.AccountID)
	return in
}

// touch writes the session behind who again, as a request that marks it
// used does.
func (w *world) touch(who *Authenticated) {
	w.t.Helper()
	ctx := context.Background()
	e, err := w.mem.GetEntry(ctx, accountPartition(who.AccountID), sessionPrefix+who.Session.ID)
	if err != nil {
		w.t.Fatal(err)
	}
	key, _ := w.svc.signingKey(ctx)
	doc := w.svc.readSession(key, e, who.AccountID)
	doc.LastUsedAt = doc.LastUsedAt.Add(time.Nanosecond)
	raw, _ := json.Marshal(doc)
	put := *e
	if put.Value, err = envelope.Sign(key, raw, entryAAD(e.Partition, e.Key)); err != nil {
		w.t.Fatal(err)
	}
	// Straight to the storage: not through the enclave whose write it races.
	if err := w.mem.PutEntry(ctx, &put); err != nil {
		w.t.Fatal(err)
	}
}

// A message to confirm that cannot be sent, or that the address has had too
// many of this hour, leaves no attempt; too much at once is dropped, and
// logged as that.
func TestConfirmingByMailWhenItCannotBeSent(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	_, cookie := w.signUp("ana@example.org")
	w.later()
	who := w.who(cookie)
	// The mail system is down.
	w.out.Fail = true
	started, err := w.svc.StartConfirmEmail(ctx, who)
	if err != nil {
		t.Fatal(err)
	}
	w.svc.Wait()
	if w.pending(kindAttempt) != 0 || !strings.Contains(w.logs.String(), "a message to confirm a session could not be sent") || strings.Contains(w.logs.String(), "ana@") {
		t.Fatalf("attempts %d, logs:\n%s", w.pending(kindAttempt), w.logs)
	}
	if _, err := w.svc.FinishConfirmEmail(ctx, who, contracts.ConfirmFinish{Attempt: started.Attempt, Code: "123456"}); !errors.Is(err, contracts.ErrInvalidCode) {
		t.Fatal(err)
	}
	w.out.Fail = false
	// The address had its messages this hour, with the sign-in messages.
	for range domain.EmailStartsPerHour {
		w.start("ana@example.org", "")
	}
	n := w.count()
	if _, err := w.svc.StartConfirmEmail(ctx, who); err != nil {
		t.Fatal(err)
	}
	if got, _ := w.sent(n); len(got) != 0 {
		t.Fatalf("sent over the address's limit: %+v", got)
	}
	// The storage cannot keep the attempt, and too many are being sent.
	w.now = w.now.Add(time.Hour + time.Second)
	who = w.who(cookie)
	w.svc.cfg.Entries = &failing{Entries: w.mem, fail: map[string]error{"put " + pendingKey: errors.New("down")}}
	if _, err := w.svc.StartConfirmEmail(ctx, who); err != nil {
		t.Fatal(err)
	}
	w.svc.Wait()
	w.svc.cfg.Entries = w.mem
	if !strings.Contains(w.logs.String(), "a sign-in in progress could not be kept") {
		t.Fatalf("logs:\n%s", w.logs)
	}
	for range maxSending {
		w.svc.sending <- struct{}{}
	}
	if _, err := w.svc.StartConfirmEmail(ctx, who); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(w.logs.String(), "too many sign-in messages being sent, one was dropped") {
		t.Fatalf("logs:\n%s", w.logs)
	}
}
