package accounts

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/domain"
	"github.com/permaily-org/permaily/internal/idp"
	"github.com/permaily-org/permaily/internal/store"
	"github.com/permaily-org/permaily/internal/webauthn/webauthntest"
)

// Sign-ins in progress across two enclaves (pending.go): each is started
// through one enclave and ended through the other, ends once, is refused when
// it is put back after it was used, and is refused by an enclave that started
// after it was made. And what the table shows of one.

// mailed starts a sign-in by mail through an enclave and returns the attempt
// and its message.
func (e *enclave) mailed(address, invitation string) (string, *Mail) {
	e.w.t.Helper()
	res, err := e.svc.StartEmail(context.Background(), contracts.EmailStart{Address: address, Invitation: invitation, Terms: e.w.terms})
	if err != nil {
		e.w.t.Fatal(err)
	}
	mail := e.w.mailOf(e.svc, res.Attempt)
	if mail == nil {
		e.w.t.Fatalf("no sign-in message for %s", address)
	}
	return res.Attempt, mail
}

// quiet keeps an enclave's log out of the world's, for the tests that run two
// enclaves at the same moment.
func (e *enclave) quiet() *enclave {
	e.svc.cfg.Log = slog.New(slog.DiscardHandler)
	return e
}

// hashes are the ways a hash of a secret could be written in the table.
func hashes(secrets ...string) []string {
	var out []string
	for _, s := range secrets {
		sum := sha256.Sum256([]byte(s))
		out = append(out, s, hex.EncodeToString(sum[:]), base64.StdEncoding.EncodeToString(sum[:]), base64.RawURLEncoding.EncodeToString(sum[:]))
	}
	return out
}

func TestASignInByMailStartedThroughOneEnclaveEndsThroughTheOther(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	one, two := w.enclave(), w.enclave()
	// A sign-up with an invitation, by the code.
	attempt, mail := one.mailed("ana@example.org", w.invite())
	me, cookie, err := two.svc.FinishEmail(ctx, contracts.EmailFinish{Attempt: attempt, Code: code(mail)}, Browser{})
	if err != nil || me.Account.Address != "ana@example.org" {
		t.Fatalf("a sign-up started through one enclave: %+v %v", me, err)
	}
	if _, err := one.svc.Authenticate(ctx, cookie); err != nil {
		t.Fatalf("its session through the first enclave: %v", err)
	}
	// A sign-in by the link: looked at through one, ended through the other.
	attempt, mail = two.mailed("ana@example.org", "")
	token := linkRE.FindStringSubmatch(mail.Text)[2]
	if pending, err := one.svc.LookEmail(ctx, contracts.EmailLook{Attempt: attempt, Token: token}); err != nil || pending.Address != "ana@example.org" {
		t.Fatalf("the link looked at through another enclave: %+v %v", pending, err)
	}
	if again, _, err := one.svc.FinishEmail(ctx, contracts.EmailFinish{Attempt: attempt, Token: token}, Browser{}); err != nil || again.Account.ID != me.Account.ID {
		t.Fatalf("the link ended through another enclave: %+v %v", again, err)
	}
	// A change of address, and a confirmation by mail.
	who := w.who(cookie)
	attempt, mail = w.askFor(one.svc, who, "new@example.org")
	if got, err := two.svc.FinishAddressChange(ctx, who, contracts.AddressFinish{Attempt: attempt, Code: code(mail)}); err != nil || got.Address != "new@example.org" {
		t.Fatalf("a change of address ended through another enclave: %+v %v", got, err)
	}
	w.later()
	started, err := two.svc.StartConfirmEmail(ctx, w.who(cookie))
	if err != nil {
		t.Fatal(err)
	}
	two.svc.Wait()
	sent := w.out.Sent()
	confirmed, err := one.svc.FinishConfirmEmail(ctx, w.who(cookie), contracts.ConfirmFinish{Attempt: started.Attempt, Code: code(&sent[len(sent)-1])})
	if err != nil || confirmed.Session.ConfirmedUntil.IsZero() {
		t.Fatalf("a confirmation ended through another enclave: %+v %v", confirmed, err)
	}
}

func TestAPasskeyCeremonyStartedThroughOneEnclaveEndsThroughTheOther(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	one, two := w.enclave(), w.enclave()
	_, cookie := w.signUp("ana@example.org")
	who := w.who(cookie)
	auth := webauthntest.New(webauthntest.ES256)
	// Making a passkey.
	creation, err := one.svc.StartPasskey(ctx, w.fresh(who))
	if err != nil {
		t.Fatal(err)
	}
	in := contracts.PasskeyFinish{Ceremony: creation.Ceremony, Name: "laptop"}
	o := creation.Options
	id, clientData, attestation := auth.Create(o.RP.ID, testOrigin, o.Challenge, o.User.ID)
	in.Credential.RawID, in.Credential.Response.ClientDataJSON, in.Credential.Response.AttestationObject = id, clientData, attestation
	if made, err := two.svc.FinishPasskey(ctx, who, in); err != nil || made.Name != "laptop" {
		t.Fatalf("a passkey made through another enclave: %+v %v", made, err)
	}
	// Signing in with it.
	request, err := two.svc.StartPasskeySignIn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	signIn := contracts.PasskeySignIn{Ceremony: request.Ceremony}
	signIn.Credential.RawID = id
	r := &signIn.Credential.Response
	r.ClientDataJSON, r.AuthenticatorData, r.Signature, r.UserHandle = auth.Get(request.Options.RPID, testOrigin, request.Options.Challenge, id)
	if me, _, err := one.svc.FinishPasskeySignIn(ctx, signIn, Browser{}); err != nil || me.Account.ID != who.AccountID {
		t.Fatalf("a sign-in with a passkey ended through another enclave: %+v %v", me, err)
	}
	// Confirming with it.
	w.later()
	stale := w.who(cookie)
	request, err = one.svc.StartConfirmPasskey(ctx, stale)
	if err != nil {
		t.Fatal(err)
	}
	confirm := contracts.PasskeySignIn{Ceremony: request.Ceremony}
	confirm.Credential.RawID = id
	r = &confirm.Credential.Response
	r.ClientDataJSON, r.AuthenticatorData, r.Signature, r.UserHandle = auth.Get(request.Options.RPID, testOrigin, request.Options.Challenge, id)
	if me, err := two.svc.FinishConfirmPasskey(ctx, stale, confirm); err != nil || me.Session.ConfirmedUntil.IsZero() {
		t.Fatalf("a confirmation with a passkey ended through another enclave: %+v %v", me, err)
	}
}

func TestATripToAProviderStartedThroughOneEnclaveEndsThroughTheOther(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	w.on(idp.Google, idp.Meta)
	one, two := w.enclave(), w.enclave()
	// A sign-up through a provider that vouches for the address.
	state, cookie := w.leaveFrom(one.svc, nil, contracts.ProviderStart{Provider: "google", Invitation: w.invite(), Terms: w.terms})
	done, err := w.comeBackTo(two.svc, state, cookie, person(idp.Google, "g-sub-nina", "nina@example.org", true))
	if err != nil || done.Session == "" {
		t.Fatalf("a trip ended through another enclave: %+v %v", done, err)
	}
	// One that does not: the sign-in waits for its address through one, the
	// message is asked through the other, and the code ends it through the first.
	state, cookie = w.leaveFrom(two.svc, nil, contracts.ProviderStart{Provider: "meta", Invitation: w.invite(), Terms: w.terms})
	done, err = w.comeBackTo(one.svc, state, cookie, person(idp.Meta, "77007700770099", "", false))
	if err != nil || done.Browser == "" {
		t.Fatalf("a trip that waits for its address: %+v %v", done, err)
	}
	if pending, err := two.svc.PendingProvider(ctx, done.Browser); err != nil || pending.Provider != "meta" {
		t.Fatalf("what waits, through another enclave: %+v %v", pending, err)
	}
	started, err := two.svc.StartProviderConfirm(ctx, done.Browser, contracts.ProviderConfirmStart{Address: "omar@example.org"})
	if err != nil {
		t.Fatal(err)
	}
	two.svc.Wait()
	sent := w.out.Sent()
	me, session, err := one.svc.FinishProviderConfirm(ctx, done.Browser, contracts.ProviderConfirm{Attempt: started.Attempt, Code: code(&sent[len(sent)-1])}, Browser{})
	if err != nil || session == "" || me.Account.Address != "omar@example.org" {
		t.Fatalf("an address confirmed through another enclave: %+v %v", me, err)
	}
	if _, err := two.svc.PendingProvider(ctx, done.Browser); !errors.Is(err, contracts.ErrProviderFailed) {
		t.Fatalf("what waited, after it ended: %v", err)
	}
}

// Of two enclaves that end the same sign-in at the same moment, one does.
func TestASignInInProgressEndsOnceThroughTwoEnclaves(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	w.signUp("ana@example.org")
	one, two := w.enclave(), w.enclave()
	// The second ends it whole in the moment the first is about to say it
	// was used: the first is told the code is wrong.
	attempt, mail := one.mailed("ana@example.org", "")
	in := contracts.EmailFinish{Attempt: attempt, Code: code(mail)}
	one.entries.partition = pendingPartition(kindAttempt, attempt)
	won := ""
	one.entries.beforePut = func() {
		me, _, err := two.svc.FinishEmail(ctx, in, Browser{})
		if err != nil {
			t.Fatalf("the enclave that came first: %v", err)
		}
		won = me.Session.ID
	}
	if me, _, err := one.svc.FinishEmail(ctx, in, Browser{}); !errors.Is(err, contracts.ErrInvalidCode) || won == "" {
		t.Fatalf("a sign-in ended twice: %+v %v", me, err)
	}

	// And at the same moment, as it happens: of every kind, one of each.
	one.quiet()
	two.quiet()
	for range 10 {
		// An hour apart: an address is sent five messages an hour.
		w.now = w.now.Add(time.Hour)
		attempt, mail := one.mailed("ana@example.org", "")
		in := contracts.EmailFinish{Attempt: attempt, Code: code(mail)}
		request, err := one.svc.StartPasskeySignIn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		results := make(chan error, 4)
		var wg sync.WaitGroup
		for _, e := range []*enclave{one, two} {
			wg.Go(func() {
				_, _, err := e.svc.FinishEmail(ctx, in, Browser{})
				results <- err
			})
			wg.Go(func() {
				_, ok, err := e.svc.end(ctx, request.Ceremony, "")
				if err == nil && !ok {
					err = contracts.ErrInvalidPasskey
				}
				results <- err
			})
		}
		wg.Wait()
		close(results)
		ended := 0
		for err := range results {
			switch {
			case err == nil:
				ended++
			case !errors.Is(err, contracts.ErrInvalidCode) && !errors.Is(err, contracts.ErrInvalidPasskey):
				t.Fatal(err)
			}
		}
		if ended != 2 {
			t.Fatalf("%d ends of a sign-in and a ceremony, each ended through two enclaves at once", ended)
		}
	}
}

// What was used never works again where it was used, whatever is put back in
// the table; and in no enclave while its mark is there.
func TestASignInPutBackAfterItWasUsedEndsNothing(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	w.signUp("ana@example.org")
	one, two := w.enclave(), w.enclave()
	attempt, mail := one.mailed("ana@example.org", "")
	in := contracts.EmailFinish{Attempt: attempt, Code: code(mail)}
	partition := pendingPartition(kindAttempt, attempt)
	kept, err := w.mem.GetEntry(ctx, partition, pendingKey)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := one.svc.FinishEmail(ctx, in, Browser{}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.mem.GetEntry(ctx, partition, pendingKey); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("what a used sign-in held is still in the table: %v", err)
	}
	// Put back, at whatever version.
	for _, version := range []uint64{kept.Version, kept.Version + 1} {
		back := *kept
		back.Version = version
		w.mem.SetEntry(back)
		for name, e := range map[string]*enclave{"the enclave that used it": one, "another": two} {
			if _, _, err := e.svc.FinishEmail(ctx, in, Browser{}); !errors.Is(err, contracts.ErrInvalidCode) {
				t.Fatalf("put back at version %d, through %s: %v", version, name, err)
			}
		}
	}
	// With its mark deleted too, the enclave that used it remembers.
	if err := w.mem.DeleteEntry(ctx, partition, usedKey); err != nil {
		t.Fatal(err)
	}
	if _, _, err := one.svc.FinishEmail(ctx, in, Browser{}); !errors.Is(err, contracts.ErrInvalidCode) {
		t.Fatalf("put back with its mark deleted, through the enclave that used it: %v", err)
	}
	// The limit, as ADR 0017 says: another enclave, which did not see it
	// used, ends it once more for whoever holds its code, and then never.
	if _, _, err := two.svc.FinishEmail(ctx, in, Browser{}); err != nil {
		t.Fatalf("the limit has moved: %v", err)
	}
	w.mem.SetEntry(*kept)
	if _, _, err := two.svc.FinishEmail(ctx, in, Browser{}); !errors.Is(err, contracts.ErrInvalidCode) {
		t.Fatalf("a third time: %v", err)
	}
}

// An enclave ends nothing that was made before it started: restarting one
// gives nobody more tries.
func TestAnEnclaveEndsNoSignInMadeBeforeItStarted(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	w.on(idp.Google)
	w.signUp("ana@example.org")
	one := w.enclave()
	attempt, mail := one.mailed("ana@example.org", "")
	in := contracts.EmailFinish{Attempt: attempt, Code: code(mail)}
	for range domain.EmailCodeTries - 1 {
		if _, _, err := one.svc.FinishEmail(ctx, contracts.EmailFinish{Attempt: attempt, Code: w.wrongCode(mail)}, Browser{}); !errors.Is(err, contracts.ErrInvalidCode) {
			t.Fatal(err)
		}
	}
	request, err := one.svc.StartPasskeySignIn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	state, cookie := w.leaveFrom(one.svc, nil, contracts.ProviderStart{Provider: "google"})
	// The enclave restarts, or another starts, a second later.
	w.now = w.now.Add(time.Second)
	restarted := w.enclave()
	if _, _, err := restarted.svc.FinishEmail(ctx, in, Browser{}); !errors.Is(err, contracts.ErrInvalidCode) {
		t.Fatalf("a sign-in made before the enclave started: %v", err)
	}
	if _, ok, err := restarted.svc.end(ctx, request.Ceremony, ""); ok || err != nil {
		t.Fatalf("a ceremony made before the enclave started: %v", err)
	}
	done, err := w.comeBackTo(restarted.svc, state, cookie, person(idp.Google, "g-sub-ana", "ana@example.org", true))
	w.refusedWith("a trip made before the enclave started", done, err, contracts.ErrProviderFailed, "/sign-in#provider_error=provider_failed")
	// The enclave that made it still ends it, with the one try it has left.
	if _, _, err := one.svc.FinishEmail(ctx, in, Browser{}); err != nil {
		t.Fatalf("through the enclave that made it: %v", err)
	}
}

// The tries on a code are counted by each enclave, for as long as the attempt
// lives: at most five in each.
func TestTheTriesOnACodeAreCountedInEachEnclave(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	w.signUp("ana@example.org")
	one, two := w.enclave(), w.enclave()
	wrong := func(e *enclave, attempt string, mail *Mail, n int) {
		t.Helper()
		for range n {
			if _, _, err := e.svc.FinishEmail(ctx, contracts.EmailFinish{Attempt: attempt, Code: w.wrongCode(mail)}, Browser{}); !errors.Is(err, contracts.ErrInvalidCode) {
				t.Fatal(err)
			}
		}
	}
	// Four in each, and the right code still works.
	attempt, mail := one.mailed("ana@example.org", "")
	wrong(one, attempt, mail, domain.EmailCodeTries-1)
	wrong(two, attempt, mail, domain.EmailCodeTries-1)
	if _, _, err := one.svc.FinishEmail(ctx, contracts.EmailFinish{Attempt: attempt, Code: code(mail)}, Browser{}); err != nil {
		t.Fatalf("after four wrong codes in each enclave: %v", err)
	}
	// Five in one: it is over there, and gone from the table.
	attempt, mail = two.mailed("ana@example.org", "")
	right := contracts.EmailFinish{Attempt: attempt, Code: code(mail)}
	kept, err := w.mem.GetEntry(ctx, pendingPartition(kindAttempt, attempt), pendingKey)
	if err != nil {
		t.Fatal(err)
	}
	wrong(one, attempt, mail, domain.EmailCodeTries)
	for name, e := range map[string]*enclave{"one": one, "two": two} {
		if _, _, err := e.svc.FinishEmail(ctx, right, Browser{}); !errors.Is(err, contracts.ErrInvalidCode) {
			t.Fatalf("the right code after five wrong ones, through %s: %v", name, err)
		}
	}
	// Put back: not there, where it ran out; the other has its own five, so
	// each limit is at most as many times what it says as there are enclaves.
	w.mem.SetEntry(*kept)
	if _, _, err := one.svc.FinishEmail(ctx, right, Browser{}); !errors.Is(err, contracts.ErrInvalidCode) {
		t.Fatalf("put back, where it ran out of tries: %v", err)
	}
	if _, _, err := two.svc.FinishEmail(ctx, right, Browser{}); err != nil {
		t.Fatalf("put back, through the other enclave: %v", err)
	}
}

// Whoever reads the table learns nothing of a sign-in in progress: not the
// address, a code, a hash of one that can be tried, a token, a challenge, a
// state, a verifier, a nonce, an invitation or a browser's cookie. And what is
// sealed for one place opens nowhere else.
func TestWhatTheTableShowsOfASignInInProgress(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	w.on(idp.Meta)
	invitation := w.invite()
	code6, _ := domain.NormalizeInvitation(invitation)
	attempt, mail := w.start("nina@example.org", invitation)
	token := linkRE.FindStringSubmatch(mail.Text)[2]
	secrets := hashes(code(mail), token, "nina@example.org", code6)
	// The other kinds: a change of address, a confirmation, a ceremony, a
	// trip and a sign-in that waits for its address.
	_, cookie := w.signUp("ana@example.org")
	who := w.who(cookie)
	_, addressMail := w.askFor(w.svc, who, "elsewhere@example.org")
	if _, err := w.svc.StartConfirmEmail(ctx, who); err != nil {
		t.Fatal(err)
	}
	w.svc.Wait()
	sent := w.out.Sent()
	request, err := w.svc.StartPasskeySignIn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	state, browser := w.leave(nil, contracts.ProviderStart{Provider: "meta", Invitation: w.invite(), Terms: w.terms})
	waiting, err := w.through(person(idp.Meta, "77007700770055", "omar@example.org", false), "")
	if err != nil {
		t.Fatal(err)
	}
	secrets = append(secrets, hashes(code(addressMail), "elsewhere@example.org", code(&sent[len(sent)-1]), state, browser,
		"verifier of "+state, "nonce of "+state, waiting.Browser, "77007700770055", "omar@example.org")...)
	secrets = append(secrets, base64.StdEncoding.EncodeToString(request.Options.Challenge), base64.RawURLEncoding.EncodeToString(request.Options.Challenge))
	kinds := map[string]int{}
	for _, e := range w.mem.AllEntries() {
		for _, kind := range []string{kindAttempt, kindCeremony, kindTrip, kindUnconfirmed} {
			if len(e.Partition) > len(pendingPartition(kind, "")) && e.Partition[:len(pendingPartition(kind, ""))] == pendingPartition(kind, "") {
				if e.Key == usedKey {
					if string(e.Value) != "{}" {
						t.Fatalf("a mark that holds something: %s", e.Value)
					}
					continue
				}
				kinds[kind]++
				var sealed map[string]json.RawMessage
				if e.Key != pendingKey || json.Unmarshal(e.Value, &sealed) != nil || len(sealed) != 1 || sealed["sealed"] == nil {
					t.Fatalf("a sign-in in progress is not only sealed: %s %s %s", e.Partition, e.Key, e.Value)
				}
			}
		}
	}
	if kinds[kindAttempt] != 3 || kinds[kindCeremony] != 1 || kinds[kindTrip] != 1 || kinds[kindUnconfirmed] != 1 {
		t.Fatalf("the sign-ins in progress in the table: %v", kinds)
	}
	if found := w.stored(secrets...); len(found) != 0 {
		t.Fatalf("readable in the table: %q", found)
	}

	// Moved to another attempt, or to another kind, what was sealed opens
	// nowhere: the code of one attempt ends no other.
	other, otherMail := w.start("nina@example.org", invitation)
	moved, err := w.mem.GetEntry(ctx, pendingPartition(kindAttempt, attempt), pendingKey)
	if err != nil {
		t.Fatal(err)
	}
	for _, partition := range []string{pendingPartition(kindAttempt, other), pendingPartition(kindCeremony, request.Ceremony)} {
		copied := *moved
		copied.Partition = partition
		w.mem.SetEntry(copied)
	}
	if _, _, err := w.svc.FinishEmail(ctx, contracts.EmailFinish{Attempt: other, Code: code(mail)}, Browser{}); !errors.Is(err, contracts.ErrInvalidCode) {
		t.Fatalf("an attempt moved to another's place: %v", err)
	}
	if _, _, err := w.svc.FinishEmail(ctx, contracts.EmailFinish{Attempt: other, Code: code(otherMail)}, Browser{}); !errors.Is(err, contracts.ErrInvalidCode) {
		t.Fatalf("the attempt whose place was taken: %v", err)
	}
	if _, ok, _ := w.svc.end(ctx, request.Ceremony, ""); ok {
		t.Fatal("an attempt moved to the place of a ceremony")
	}
	// Where it was, it still is what it was.
	if me, _, err := w.svc.FinishEmail(ctx, contracts.EmailFinish{Attempt: attempt, Code: code(mail)}, Browser{}); err != nil || me.Account.Address != "nina@example.org" {
		t.Fatalf("the attempt where it was sealed: %+v %v", me, err)
	}
}

// Two enclaves set the applications of two providers at the same moment:
// both are kept, and a secret given through one is used through the other
// within appsFresh, without a restart.
func TestTwoEnclavesSetTheApplicationsOfTwoProviders(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	one, two := w.enclave(), w.enclave()
	set := func(e *enclave, p idp.Provider, secret string) {
		t.Helper()
		if _, err := e.svc.SetProviderApp(ctx, string(p), contracts.ProviderAppSet{ClientID: "client-of-" + string(p), ClientSecret: secret}); err != nil {
			t.Fatal(err)
		}
	}
	one.records.meanwhile = func() { set(two, idp.GitHub, "S3cret~github") }
	set(one, idp.Google, "S3cret~google")
	w.now = w.now.Add(appsFresh)
	for name, e := range map[string]*enclave{"one": one, "two": two} {
		if on, err := e.svc.ProvidersOn(ctx); err != nil || len(on.Providers) != 2 {
			t.Fatalf("through %s: %+v %v", name, on, err)
		}
	}
	// A new secret through one: the other uses the one it read for at most
	// appsFresh, then the new one.
	set(one, idp.Google, "S3cret~google-2")
	if app, _, _ := two.svc.app(ctx, idp.Google); app.ClientSecret != "S3cret~google" {
		t.Fatalf("read again before its time: %+v", app)
	}
	w.now = w.now.Add(appsFresh)
	if app, on, err := two.svc.app(ctx, idp.Google); err != nil || !on || app.ClientSecret != "S3cret~google-2" {
		t.Fatalf("the new secret, through the other enclave: %+v %v", app, err)
	}
	// What the other then writes keeps it.
	set(two, idp.Meta, "S3cret~meta")
	if app, _, _ := one.svc.app(ctx, idp.Google); app.ClientSecret != "S3cret~google-2" {
		t.Fatalf("a secret dropped by the other enclave's write: %+v", app)
	}
	// One that keeps changing under it fails, and changes nothing.
	one.records.always = true
	one.records.meanwhile = func() { set(two, idp.Microsoft, "S3cret~ms") }
	if _, err := one.svc.SetProviderApp(ctx, "github", contracts.ProviderAppSet{ClientID: "x", ClientSecret: "S3cret~lost"}); err == nil {
		t.Fatal("a change that never got its write in")
	}
}

// One provider account linked to two accounts at the same moment, through
// two enclaves, ends linked to one, and the other is told it is in use.
func TestOneProviderAccountLinkedToTwoAccountsAtOnce(t *testing.T) {
	ctx := context.Background()
	subject := person(idp.Google, "g-sub-shared", "", false)
	for _, when := range []string{"after the first read the pointer", "before the first writes the pointer", "before the first's record lists it"} {
		t.Run(when, func(t *testing.T) {
			w := newWorld(t)
			w.on(idp.Google)
			_, anaCookie := w.signUp("ana@example.org")
			_, beaCookie := w.signUp("bea@example.org")
			ana, bea := w.who(anaCookie), w.who(beaCookie)
			one, two := w.enclave(), w.enclave()
			anaState, anaBrowser := w.leaveFrom(one.svc, ana, contracts.ProviderStart{Provider: "google"})
			beaState, beaBrowser := w.leaveFrom(two.svc, bea, contracts.ProviderStart{Provider: "google"})
			var beaDone *ProviderDone
			var beaErr error
			other := func() { beaDone, beaErr = w.comeBackTo(two.svc, beaState, beaBrowser, subject) }
			one.entries.partition = providerPartition(idp.Google, subject.Subject)
			switch when {
			case "after the first read the pointer":
				one.entries.afterGet = other
			case "before the first writes the pointer":
				one.entries.beforePut = other
			default:
				one.records.onRecord = func(r *store.Record) {
					if beaDone == nil && strings.Contains(string(r.Value), `"subject":"g-sub-shared","linked_at"`) {
						other()
					}
				}
			}
			anaDone, anaErr := w.comeBackTo(one.svc, anaState, anaBrowser, subject)
			if beaDone == nil {
				t.Fatal("the other link did not land")
			}
			lists := func(id string) bool {
				for _, p := range w.providers(id) {
					if p.Subject == subject.Subject {
						return true
					}
				}
				return false
			}
			doc, err := w.svc.linked(ctx, idp.Google, subject.Subject)
			if err != nil || doc == nil {
				t.Fatalf("linked to no account: %v", err)
			}
			winner, loser, loserErr := ana.AccountID, bea.AccountID, beaErr
			if anaErr != nil {
				winner, loser, loserErr = bea.AccountID, ana.AccountID, anaErr
			}
			if doc.ID != winner || !lists(winner) || lists(loser) {
				t.Fatalf("linked to %s; ana lists it %v, bea %v (ana %v, bea %v)", doc.ID, lists(ana.AccountID), lists(bea.AccountID), anaErr, beaErr)
			}
			if !errors.Is(loserErr, contracts.ErrProviderInUse) || (anaErr == nil) == (beaErr == nil) {
				t.Fatalf("the other is not told it is in use: ana %v %+v, bea %v %+v", anaErr, anaDone, beaErr, beaDone)
			}
		})
	}
}

// An unlink whose change is run again, and refused the second time, deletes
// no pointer of the link that stayed.
func TestAnUnlinkRunAgainLeavesThePointerOfALinkThatStayed(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	w.on(idp.Google)
	ana, cookie := w.signUp("ana@example.org")
	who := w.who(cookie)
	// No address the provider vouches for: only the pointer leads to ana.
	google := person(idp.Google, "g-sub-ana", "", false)
	if _, err := w.linkFrom(who, google); err != nil {
		t.Fatal(err)
	}
	one, two := w.enclave(), w.enclave()
	// Between the unlink's read and its write, the operator blocks the
	// account through the other enclave: the second run is refused.
	one.records.meanwhile = func() {
		if _, err := two.svc.BlockAccount(ctx, ana.Account.ID); err != nil {
			t.Fatal(err)
		}
	}
	if err := one.svc.UnlinkProvider(ctx, w.fresh(who), "google"); !errors.Is(err, contracts.ErrUnauthorized) {
		t.Fatalf("an unlink of a blocked account: %v", err)
	}
	if _, err := two.svc.UnblockAccount(ctx, ana.Account.ID); err != nil {
		t.Fatal(err)
	}
	// Still linked, and the provider account still signs in.
	if len(w.providers(ana.Account.ID)) != 1 {
		t.Fatal("unlinked")
	}
	if done, err := w.through(google, ""); err != nil || w.who(done.Session).AccountID != ana.Account.ID {
		t.Fatalf("the link that stayed: %+v %v", done, err)
	}
}

// A deletion the person cancelled, and was told is cancelled, is not carried
// out by an enclave that read the account as due a moment before.
func TestACancelLandingWhileARemovalRuns(t *testing.T) {
	ctx := context.Background()
	for _, when := range []string{"before the record says it is being removed", "after"} {
		t.Run(when, func(t *testing.T) {
			w := newWorld(t)
			ana, cookie := w.signUp("ana@example.org")
			who := w.who(cookie)
			closing, err := w.svc.StartDeletion(ctx, w.fresh(who))
			if err != nil {
				t.Fatal(err)
			}
			// The day comes by the clock of one enclave, and not yet by the other's.
			w.now = closing.ClosesAt.Add(-time.Second)
			one, two := w.enclaveAt(2*time.Second), w.enclaveAt(0)
			var kept *contracts.Account
			var keptErr error
			ran := false
			cancel := func() { ran = true; kept, keptErr = two.svc.CancelDeletion(ctx, who) }
			if when == "after" {
				// Removing starts with the pointer from the address.
				one.entries.partition, one.entries.afterGet = loginPartition("ana@example.org"), cancel
			} else {
				one.records.meanwhile = cancel
			}
			if err := one.svc.RemoveDue(ctx); err != nil {
				t.Fatal(err)
			}
			if !ran {
				t.Fatal("the cancel did not land")
			}
			doc, err := w.enclaveAt(time.Minute).svc.read(ctx, ana.Account.ID)
			if err != nil {
				t.Fatal(err)
			}
			if keptErr == nil {
				if kept.State != contracts.AccountActive || doc == nil || doc.State != contracts.AccountActive {
					t.Fatalf("told it is kept (%+v), and it is %+v", kept, doc)
				}
			} else if doc != nil {
				t.Fatalf("the cancel failed (%v), and the account is %+v", keptErr, doc)
			}
		})
	}
}

// No session made before every device is signed out survives it: not one
// that a device signing in at that moment, through the other enclave or
// through the same one, writes after.
func TestNoSessionMadeBeforeASignOutSurvivesIt(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		name     string
		same     bool
		signOut  func(*Service, *Authenticated) error
		afterIt  func(*Service, *Authenticated) error
		asked    bool // the session that asked stays
		blocking bool
	}{
		{name: "deleting asked", signOut: func(s *Service, who *Authenticated) error {
			_, err := s.StartDeletion(ctx, who)
			return err
		}, asked: true},
		{name: "deleting asked, in one enclave", same: true, signOut: func(s *Service, who *Authenticated) error {
			_, err := s.StartDeletion(ctx, who)
			return err
		}, asked: true},
		{name: "blocked and unblocked", signOut: func(s *Service, who *Authenticated) error {
			_, err := s.BlockAccount(ctx, who.AccountID)
			return err
		}, afterIt: func(s *Service, who *Authenticated) error {
			_, err := s.UnblockAccount(ctx, who.AccountID)
			return err
		}},
		{name: "every device signed out by the operator", signOut: func(s *Service, who *Authenticated) error {
			_, err := s.CloseAccountSessions(ctx, who.AccountID)
			return err
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t)
			ana, cookie := w.signUp("ana@example.org")
			who := w.fresh(w.who(cookie))
			one, two := w.enclave(), w.enclave()
			if c.same {
				one = two
			}
			attempt, mail := two.mailed("ana@example.org", "")
			two.entries.partition = accountPartition(ana.Account.ID)
			two.entries.beforePut = func() {
				if err := c.signOut(one.svc, who); err != nil {
					t.Fatal(err)
				}
			}
			_, late, err := two.svc.FinishEmail(ctx, contracts.EmailFinish{Attempt: attempt, Code: code(mail)}, Browser{})
			if err != nil {
				t.Fatal(err)
			}
			if c.afterIt != nil {
				if err := c.afterIt(one.svc, who); err != nil {
					t.Fatal(err)
				}
			}
			for _, e := range []*enclave{one, two} {
				if _, err := e.svc.Authenticate(ctx, late); !errors.Is(err, contracts.ErrUnauthorized) {
					t.Fatalf("a session made before the devices were signed out: %v", err)
				}
				if _, err := e.svc.Authenticate(ctx, cookie); (err == nil) != c.asked {
					t.Fatalf("the session that asked: %v", err)
				}
			}
			// A device that signs in after it does sign in.
			if _, cookie := w.signIn("ana@example.org"); cookie == "" {
				t.Fatal("no sign-in after")
			} else if _, err := one.svc.Authenticate(ctx, cookie); err != nil {
				t.Fatalf("a session made after the devices were signed out: %v", err)
			}
		})
	}
}

// The pointer of an address that led nowhere is replaced on sign-up only as
// it was read: not when another sign-up of the address made one in its place
// at the same version.
func TestAStalePointerReplacedOnSignUpIsNotAnotherAccounts(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	partition := loginPartition("ana@example.org")
	w.mem.SetEntry(store.Entry{Partition: partition, Key: loginKey, Value: []byte(`{"doc":{},"mac":""}`), Version: 1})
	one, two := w.enclave(), w.enclave()
	attempt, mail := one.mailed("ana@example.org", w.invite())
	var other *contracts.Me
	one.entries.partition = partition
	one.entries.afterGet = func() {
		// The stale pointer goes, and another sign-up writes the address's
		// pointer, at the version the first read.
		if err := w.mem.DeleteEntry(ctx, partition, loginKey); err != nil {
			t.Fatal(err)
		}
		in := two.start("ana@example.org", w.invite())
		var err error
		if other, _, err = two.svc.FinishEmail(ctx, in, Browser{}); err != nil {
			t.Fatal(err)
		}
		if e, err := w.mem.GetEntry(ctx, partition, loginKey); err != nil || e.Version != 1 {
			t.Fatalf("the new pointer: %+v %v", e, err)
		}
	}
	me, _, err := one.svc.FinishEmail(ctx, contracts.EmailFinish{Attempt: attempt, Code: code(mail)}, Browser{})
	if err != nil || other == nil {
		t.Fatal(err)
	}
	if me.Account.ID != other.Account.ID || w.found("ana@example.org") != other.Account.ID || w.accounts() != 1 {
		t.Fatalf("two sign-ups of one address: %s and %s, the address finds %s, %d accounts", me.Account.ID, other.Account.ID, w.found("ana@example.org"), w.accounts())
	}
	w.onePerAddress()
}
