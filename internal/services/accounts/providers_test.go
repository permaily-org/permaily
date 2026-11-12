package accounts

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/domain"
	"github.com/permaily-org/permaily/internal/idp"
	"github.com/permaily-org/permaily/internal/keys"
	"github.com/permaily-org/permaily/internal/store"
	"github.com/permaily-org/permaily/internal/webauthn/webauthntest"
)

// fakeProviders plays the providers: it sends the browser to an address of
// its own, and for a code it gave it says who it was told signed in.
type fakeProviders struct {
	mu sync.Mutex
	// who signed in, by the code that comes back with the browser.
	who   map[string]idp.Identity
	codes int
	// err makes Finish fail, and beginErr Begin.
	err, beginErr error
	// apps and started are what Finish was last given, and begun how many
	// trips were started.
	app     idp.App
	started idp.Started
	begun   int
}

func newFakeProviders() *fakeProviders { return &fakeProviders{who: map[string]idp.Identity{}} }

func (f *fakeProviders) Begin(p idp.Provider, app idp.App, state string) (*idp.Started, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.beginErr != nil {
		return nil, f.beginErr
	}
	f.begun++
	address := "https://" + string(p) + ".test/authorize?client_id=" + url.QueryEscape(app.ClientID) + "&state=" + url.QueryEscape(state)
	return &idp.Started{URL: address, Verifier: "verifier of " + state, Nonce: "nonce of " + state}, nil
}

func (f *fakeProviders) Finish(_ context.Context, p idp.Provider, app idp.App, code string, s idp.Started) (*idp.Identity, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.app, f.started = app, s
	if f.err != nil {
		return nil, f.err
	}
	who, ok := f.who[code]
	delete(f.who, code) // a code works once
	if !ok || who.Provider != p {
		return nil, fmt.Errorf("%w: a code that was not given", idp.ErrInvalid)
	}
	return &who, nil
}

// approve is the person signing in at the provider: the code the browser
// comes back with.
func (f *fakeProviders) approve(who idp.Identity) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.codes++
	code := fmt.Sprintf("code-%d-of-the-provider", f.codes)
	f.who[code] = who
	return code
}

// person is who a provider says signed in.
func person(p idp.Provider, subject, address string, verified bool) idp.Identity {
	return idp.Identity{Provider: p, Subject: subject, Address: address, Verified: verified, Name: "Name At Provider"}
}

func appSecret(p idp.Provider) string { return "S3cret~of-" + string(p) }

// on turns providers on, as the operator does.
func (w *world) on(providers ...idp.Provider) {
	w.t.Helper()
	for _, p := range providers {
		in := contracts.ProviderAppSet{ClientID: "client-of-" + string(p), ClientSecret: appSecret(p)}
		if _, err := w.svc.SetProviderApp(context.Background(), string(p), in); err != nil {
			w.t.Fatal(err)
		}
	}
}

// leave starts a trip to a provider, from Settings when who is set, and
// returns its state and the value of the browser's cookie.
func (w *world) leave(who *Authenticated, in contracts.ProviderStart) (state, cookie string) {
	w.t.Helper()
	started, cookie, err := w.svc.StartProvider(context.Background(), who, in)
	if err != nil {
		w.t.Fatalf("starting with %s: %v", in.Provider, err)
	}
	u, err := url.Parse(started.URL)
	if err != nil || u.Host != in.Provider+".test" || started.ExpiresIn != 600 || len(cookie) != 43 || len(u.Query().Get("state")) != 43 {
		w.t.Fatalf("started: %+v, cookie %q", started, cookie)
	}
	return u.Query().Get("state"), cookie
}

// comeBack ends a trip in the browser that holds cookie, with who signed in
// at the provider.
func (w *world) comeBack(state, cookie string, who idp.Identity) (*ProviderDone, error) {
	w.t.Helper()
	done, err := w.svc.FinishProvider(context.Background(), ProviderAnswer{State: state, Code: w.idp.approve(who), Browser: cookie, From: Browser{UserAgent: "provider device"}})
	if done == nil || !strings.HasPrefix(done.Path, "/") || strings.HasPrefix(done.Path, "//") {
		w.t.Fatalf("the browser is sent nowhere in the app: %+v %v", done, err)
	}
	if err != nil && (done.Session != "" || done.Browser != "") {
		w.t.Fatalf("a trip that failed left something: %+v %v", done, err)
	}
	return done, err
}

// through makes the whole trip to sign in.
func (w *world) through(who idp.Identity, invitation string) (*ProviderDone, error) {
	w.t.Helper()
	state, cookie := w.leave(nil, contracts.ProviderStart{Provider: string(who.Provider), Invitation: invitation})
	return w.comeBack(state, cookie, who)
}

// linkFrom makes the whole trip from the Settings of a session.
func (w *world) linkFrom(session *Authenticated, who idp.Identity) (*ProviderDone, error) {
	w.t.Helper()
	state, cookie := w.leave(session, contracts.ProviderStart{Provider: string(who.Provider)})
	return w.comeBack(state, cookie, who)
}

// reason is the reason code a trip ended with, "" when it has none.
func reason(done *ProviderDone) string {
	_, code, _ := strings.Cut(done.Path, "#provider_error=")
	return code
}

// refusedWith fails unless a trip ended on the screen with that reason and
// that error, and signed nobody in.
func (w *world) refusedWith(what string, done *ProviderDone, err, want error, path string) {
	w.t.Helper()
	if !errors.Is(err, want) || done.Path != path || done.Session != "" || done.Browser != "" {
		w.t.Fatalf("%s: %+v %v, want %v at %s", what, done, err, want, path)
	}
}

// providers is what an account's record lists.
func (w *world) providers(accountID string) []providerDoc {
	w.t.Helper()
	doc, err := w.svc.read(context.Background(), accountID)
	if err != nil || doc == nil {
		w.t.Fatalf("the account: %+v %v", doc, err)
	}
	return doc.Providers
}

// accounts is how many accounts there are.
func (w *world) accounts() int {
	w.t.Helper()
	list, err := w.svc.AccountRecords(context.Background(), "", 100)
	if err != nil || list.Unreadable != 0 {
		w.t.Fatalf("the accounts: %+v %v", list, err)
	}
	return len(list.Accounts)
}

func TestProviderSignUpAndSignIn(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	w.on(idp.Google, idp.GitHub)
	ana := person(idp.Google, "g-sub-ana-1081", "Ana@Example.org", true)

	// Whoever the provider vouches for, with no account and no invitation,
	// is told that one is needed. Nothing is made.
	done, err := w.through(ana, "")
	w.refusedWith("no account and no invitation", done, err, contracts.ErrInvitationNeeded, "/sign-in#provider_error=invitation_needed")
	if w.accounts() != 0 || len(w.stored("g-sub-ana-1081")) != 0 {
		t.Fatalf("something was made: %q", w.stored("g-sub-ana-1081"))
	}
	if _, _, err := w.svc.StartProvider(ctx, nil, contracts.ProviderStart{Provider: "google", Invitation: "abcd-2345-wxyz"}); !errors.Is(err, contracts.ErrInvalidInvitation) {
		t.Fatalf("an invitation that is not one: %v", err)
	}

	// With an invitation the account is made, with the provider linked.
	invitation := w.invite()
	state, cookie := w.leave(nil, contracts.ProviderStart{Provider: "google", Invitation: invitation, ReturnTo: "/connections?new=1"})
	done, err = w.comeBack(state, cookie, ana)
	if err != nil || done.Path != "/connections?new=1" || done.Browser != "" {
		t.Fatalf("signing up: %+v %v", done, err)
	}
	who := w.who(done.Session)
	me, err := w.svc.Me(ctx, who)
	if err != nil || me.Account.Address != "ana@example.org" || me.Account.State != contracts.AccountActive || who.Session.Device != "provider device" {
		t.Fatalf("the account made: %+v %v", me, err)
	}
	linked := w.providers(who.AccountID)
	if len(linked) != 1 || linked[0] != (providerDoc{Provider: idp.Google, Subject: "g-sub-ana-1081", LinkedAt: w.now}) {
		t.Fatalf("what the record lists: %+v", linked)
	}
	// The provider was asked with the application's secret and with what the
	// start kept for this trip.
	if w.idp.app != (idp.App{ClientID: "client-of-google", ClientSecret: appSecret(idp.Google)}) || w.idp.started.Verifier != "verifier of "+state || w.idp.started.Nonce != "nonce of "+state {
		t.Fatalf("the provider was asked with %+v %+v", w.idp.app, w.idp.started)
	}
	// The answer worked once, and the invitation too.
	done, err = w.comeBack(state, cookie, ana)
	w.refusedWith("the same state again", done, err, contracts.ErrProviderFailed, "/sign-in#provider_error=provider_failed")
	if _, _, err := w.svc.StartProvider(ctx, nil, contracts.ProviderStart{Provider: "google", Invitation: invitation}); !errors.Is(err, contracts.ErrInvalidInvitation) {
		t.Fatalf("a used invitation: %v", err)
	}

	// Signing in again needs no invitation and asks nothing of the address:
	// the subject leads to the account, whatever the provider now says of it.
	for _, now := range []idp.Identity{ana, person(idp.Google, "g-sub-ana-1081", "moved@example.net", false), person(idp.Google, "g-sub-ana-1081", "", false)} {
		done, err = w.through(now, "")
		if err != nil || done.Path != "/" || w.who(done.Session).AccountID != who.AccountID {
			t.Fatalf("signing in again as %+v: %+v %v", now, done, err)
		}
	}
	if w.accounts() != 1 || len(w.providers(who.AccountID)) != 1 {
		t.Fatalf("%d accounts, %+v", w.accounts(), w.providers(who.AccountID))
	}

	// An account made by mail: the provider that vouches for its address is
	// linked to it, with no invitation (ADR 0017).
	eve, _ := w.signUp("eve@example.org")
	done, err = w.through(person(idp.GitHub, "40071", "eve@example.org", true), "")
	if err != nil || w.who(done.Session).AccountID != eve.Account.ID {
		t.Fatalf("a provider that vouches for the address of an account: %+v %v", done, err)
	}
	if l := w.providers(eve.Account.ID); len(l) != 1 || l[0].Provider != idp.GitHub || l[0].Subject != "40071" {
		t.Fatalf("what the record lists: %+v", l)
	}
	// Someone with an account who gives an invitation keeps it unused.
	spare := w.invite()
	if done, err = w.through(ana, spare); err != nil || w.who(done.Session).AccountID != who.AccountID {
		t.Fatalf("signing in with an invitation: %+v %v", done, err)
	}
	if _, mail := w.start("new@example.org", spare); mail == nil {
		t.Fatal("an invitation was used up by someone who had an account")
	}

	// A trip ends after ten minutes, when the provider says no, and without a code.
	state, cookie = w.leave(nil, contracts.ProviderStart{Provider: "google"})
	w.now = w.now.Add(domain.ProviderLifetime)
	done, err = w.comeBack(state, cookie, ana)
	w.refusedWith("after ten minutes", done, err, contracts.ErrProviderFailed, "/sign-in#provider_error=provider_failed")
	for _, in := range []ProviderAnswer{{Refused: true, Code: w.idp.approve(ana)}, {}, {Refused: true}} {
		in.State, in.Browser = w.leave(nil, contracts.ProviderStart{Provider: "google"})
		done, err = w.svc.FinishProvider(ctx, in)
		w.refusedWith("refused at the provider", done, err, contracts.ErrProviderFailed, "/sign-in#provider_error=provider_failed")
	}

	// An invitation that someone else used while the person was away.
	shared := w.invite()
	state, cookie = w.leave(nil, contracts.ProviderStart{Provider: "google", Invitation: shared})
	attempt, mail := w.start("first@example.org", shared)
	if _, _, err := w.svc.FinishEmail(ctx, contracts.EmailFinish{Attempt: attempt, Code: code(mail)}, Browser{UserAgent: ""}); err != nil {
		t.Fatal(err)
	}
	done, err = w.comeBack(state, cookie, person(idp.Google, "g-sub-late", "late@example.org", true))
	w.refusedWith("an invitation used meanwhile", done, err, contracts.ErrInvalidInvitation, "/sign-in#provider_error=invalid_invitation")
	if len(w.stored("g-sub-late", "late@example.org")) != 0 {
		t.Fatal("something was made without an invitation")
	}

	// A provider that names another one, or an account in a way that cannot
	// be kept, signs nobody in.
	for _, odd := range []idp.Identity{
		{Provider: idp.Google, Subject: "", Address: "odd@example.org", Verified: true},
		{Provider: idp.Google, Subject: "two words", Address: "odd@example.org", Verified: true},
		{Provider: idp.Google, Subject: strings.Repeat("9", 256), Address: "odd@example.org", Verified: true},
	} {
		done, err = w.through(odd, w.invite())
		w.refusedWith("an odd subject", done, err, contracts.ErrProviderFailed, "/sign-in#provider_error=provider_failed")
	}
	state, cookie = w.leave(nil, contracts.ProviderStart{Provider: "google"})
	w.idp.mu.Lock()
	w.idp.who["swapped"] = person(idp.Google, "g-sub-ana-1081", "ana@example.org", true)
	w.idp.mu.Unlock()
	w.idp.err = nil
	done, err = w.svc.FinishProvider(ctx, ProviderAnswer{State: state, Code: "swapped", Browser: cookie})
	if err != nil || done.Session == "" {
		t.Fatalf("a code the provider gave: %+v %v", done, err)
	}
	other := &swapping{fakeProviders: w.idp, as: idp.GitHub}
	w.svc.cfg.Providers = other
	state, cookie = w.leave(nil, contracts.ProviderStart{Provider: "google"})
	done, err = w.comeBack(state, cookie, ana)
	w.refusedWith("an identity of another provider", done, err, contracts.ErrProviderFailed, "/sign-in#provider_error=provider_failed")

	// Nothing the service logged names the person, the provider account,
	// what came back or what was kept.
	w.quiet("ana@", "example.org", "example.net", "g-sub-ana-1081", "40071", "g-sub-late", who.AccountID, eve.Account.ID, state, cookie, done.Session,
		"code-", "verifier", "nonce", appSecret(idp.Google), "client-of-google", "Name At Provider", invitation, shared)
	if !strings.Contains(w.logs.String(), "accounts: provider linked") {
		t.Fatalf("logs:\n%s", w.logs)
	}
}

// swapping is a provider that answers as another one.
type swapping struct {
	*fakeProviders
	as idp.Provider
}

func (s *swapping) Finish(ctx context.Context, p idp.Provider, app idp.App, code string, st idp.Started) (*idp.Identity, error) {
	who, err := s.fakeProviders.Finish(ctx, p, app, code, st)
	if who != nil {
		who.Provider = s.as
	}
	return who, err
}

// An answer of the provider (its code and the state) opened in a browser
// that did not start the trip signs nobody in and links nothing: someone
// could start one for their own provider account and send the answer to
// another person, to sign that person into an account that is not theirs or
// to link their own provider account to it.
func TestProviderAnswerInAnotherBrowser(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	w.on(idp.Google)
	ana, anaCookie := w.signUp("ana@example.org")
	mallory := person(idp.Google, "g-sub-mallory", "ana@example.org", true)

	for name, browser := range map[string]func() string{
		"a browser with no cookie": func() string { return "" },
		"a browser on a trip of its own": func() string {
			_, cookie := w.leave(nil, contracts.ProviderStart{Provider: "google"})
			return cookie
		},
		"a browser that guesses": func() string { return newSecret() },
	} {
		// A sign-in: the address is Ana's and the provider vouches for it,
		// so in the right browser this would sign in to her account.
		state, cookie := w.leave(nil, contracts.ProviderStart{Provider: "google"})
		done, err := w.comeBack(state, browser(), mallory)
		w.refusedWith(name, done, err, contracts.ErrProviderFailed, "/sign-in#provider_error=provider_failed")
		// The trip is over for the browser that started it too.
		done, err = w.comeBack(state, cookie, mallory)
		w.refusedWith(name+", then the right one", done, err, contracts.ErrProviderFailed, "/sign-in#provider_error=provider_failed")

		// A link from Ana's Settings, ended elsewhere.
		state, _ = w.leave(w.who(anaCookie), contracts.ProviderStart{Provider: "google"})
		done, err = w.comeBack(state, browser(), mallory)
		w.refusedWith(name+", linking", done, err, contracts.ErrProviderFailed, "/sign-in#provider_error=provider_failed")
	}
	if len(w.providers(ana.Account.ID)) != 0 || len(w.stored("g-sub-mallory")) != 0 || w.accounts() != 1 {
		t.Fatalf("linked or made by an answer in another browser: %+v %q", w.providers(ana.Account.ID), w.stored("g-sub-mallory"))
	}
	if list, _ := w.svc.Sessions(ctx, w.who(anaCookie)); len(list.Sessions) != 1 {
		t.Fatalf("a session was made: %+v", list)
	}
	// The same answer in the browser that started the trip does sign in:
	// the browser is what was missing.
	state, cookie := w.leave(nil, contracts.ProviderStart{Provider: "google"})
	if done, err := w.comeBack(state, cookie, mallory); err != nil || w.who(done.Session).AccountID != ana.Account.ID {
		t.Fatalf("in the browser that started it: %+v %v", done, err)
	}
	// Only the hash of the cookie's value is kept.
	_, cookie = w.leave(nil, contracts.ProviderStart{Provider: "google"})
	for _, kept := range w.svc.trips {
		if strings.Contains(fmt.Sprintf("%+v", *kept), cookie) {
			t.Fatal("the value of the cookie is kept")
		}
	}
}

// A provider that does not vouch for an address proved who the person is
// there, and nothing of the address: it links to no account and makes none
// until the code of a message says the address is the person's.
func TestProviderAddressNotVouchedFor(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	w.on(idp.Meta, idp.Microsoft)
	ana, anaCookie := w.signUp("ana@example.org")
	written := w.version(ana.Account.ID)
	mallory := person(idp.Meta, "770011", "Ana@Example.org", false)

	// The answer is the same whether the address has an account or not, with
	// an invitation or without, and whatever the provider gave for one.
	var browsers []string
	for _, c := range []struct {
		who        idp.Identity
		invitation string
		offered    string
	}{
		{mallory, "", "ana@example.org"},
		{mallory, w.invite(), "ana@example.org"},
		{person(idp.Meta, "770012", "nobody@example.org", false), "", "nobody@example.org"},
		{person(idp.Meta, "770013", "nobody@example.org", false), w.invite(), "nobody@example.org"},
		{person(idp.Meta, "770014", "", false), "", ""},
		{person(idp.Microsoft, "ms-sub-1", "not an address", false), "", ""},
		// Vouched for, and not an address accounts are found by.
		{person(idp.Microsoft, "ms-sub-2", "aña@example.org", true), w.invite(), ""},
	} {
		done, err := w.through(c.who, c.invitation)
		if err != nil || done.Path != "/sign-in#provider_confirm" || done.Session != "" || len(done.Browser) != 43 {
			t.Fatalf("%+v: %+v %v", c.who, done, err)
		}
		pending, err := w.svc.PendingProvider(ctx, done.Browser)
		if err != nil || *pending != (contracts.ProviderPending{Provider: string(c.who.Provider), Address: c.offered, ReturnTo: "/", ExpiresIn: 1200}) {
			t.Fatalf("%+v waits as %+v %v", c.who, pending, err)
		}
		browsers = append(browsers, done.Browser)
	}
	if w.accounts() != 1 || w.version(ana.Account.ID) != written || len(w.stored("7700", "ms-sub-")) != 0 {
		t.Fatalf("linked or made without the code: %d accounts, stored %q", w.accounts(), w.stored("7700", "ms-sub-"))
	}
	if list, _ := w.svc.Sessions(ctx, w.who(anaCookie)); len(list.Sessions) != 1 {
		t.Fatalf("a session was made: %+v", list)
	}
	// Only the hash of the cookie's value is kept.
	for key := range w.svc.unconfirmed {
		for _, b := range browsers {
			if key == b {
				t.Fatal("the value of the cookie is kept")
			}
		}
	}
	malloryBrowser, withInvitation, nobody := browsers[0], browsers[1], browsers[3]

	// What waits, waits in that browser only.
	for _, other := range []string{"", newSecret(), anaCookie} {
		if _, err := w.svc.PendingProvider(ctx, other); !errors.Is(err, contracts.ErrProviderFailed) {
			t.Fatalf("another browser sees what waits: %v", err)
		}
		if _, err := w.svc.StartProviderConfirm(ctx, other, contracts.ProviderConfirmStart{Address: "ana@example.org"}); !errors.Is(err, contracts.ErrProviderFailed) {
			t.Fatalf("another browser asks for the message: %v", err)
		}
		if _, _, err := w.svc.FinishProviderConfirm(ctx, other, contracts.ProviderConfirm{Attempt: "att_x", Code: "123456"}, Browser{UserAgent: ""}); !errors.Is(err, contracts.ErrProviderFailed) {
			t.Fatalf("another browser confirms: %v", err)
		}
	}

	// The message goes to the address, as a sign-in by mail does: Mallory,
	// who is not Ana, never sees its code, and guessing takes five tries.
	confirm := func(browser, address string) (string, *Mail) {
		t.Helper()
		before := len(w.out.Sent())
		started, err := w.svc.StartProviderConfirm(ctx, browser, contracts.ProviderConfirmStart{Address: address})
		if err != nil || !validID("att", started.Attempt) || started.ExpiresIn != 600 {
			t.Fatalf("asking for the message: %+v %v", started, err)
		}
		w.svc.Wait()
		if sent := w.out.Sent(); len(sent) > before {
			return started.Attempt, &sent[len(sent)-1]
		}
		return started.Attempt, nil
	}
	attempt, mail := confirm(malloryBrowser, " ANA@example.org ")
	if mail == nil || mail.To != "ana@example.org" {
		t.Fatalf("the message: %+v", mail)
	}
	for range domain.EmailCodeTries {
		if _, _, err := w.svc.FinishProviderConfirm(ctx, malloryBrowser, contracts.ProviderConfirm{Attempt: attempt, Code: w.wrongCode(mail)}, Browser{UserAgent: ""}); !errors.Is(err, contracts.ErrInvalidCode) {
			t.Fatalf("a wrong code: %v", err)
		}
	}
	if _, _, err := w.svc.FinishProviderConfirm(ctx, malloryBrowser, contracts.ProviderConfirm{Attempt: attempt, Code: code(mail)}, Browser{UserAgent: ""}); !errors.Is(err, contracts.ErrInvalidCode) {
		t.Fatalf("the right code after five wrong ones: %v", err)
	}
	if len(w.providers(ana.Account.ID)) != 0 || w.version(ana.Account.ID) != written {
		t.Fatal("linked without the code")
	}
	// An address with no account gets no message without an invitation, and
	// the answer does not say so; one that is no address is refused.
	if _, mail := confirm(malloryBrowser, "nobody@example.org"); mail != nil {
		t.Fatal("a message for an address without an account or an invitation")
	}
	if _, err := w.svc.StartProviderConfirm(ctx, malloryBrowser, contracts.ProviderConfirmStart{Address: "nope"}); !errors.Is(err, contracts.ErrInvalidAddress) {
		t.Fatalf("not an address: %v", err)
	}

	// Ana, in her own browser, made the same trip: the code she gets links
	// her Meta account and signs her in. Mail she asked for elsewhere, typed
	// here, proves the address just as well.
	done, err := w.through(person(idp.Meta, "880022", "", false), "")
	if err != nil {
		t.Fatal(err)
	}
	attempt, mail = confirm(done.Browser, "ana@example.org")
	me, cookie, err := w.svc.FinishProviderConfirm(ctx, done.Browser, contracts.ProviderConfirm{Attempt: attempt, Code: code(mail)}, Browser{UserAgent: "ana's phone"})
	if err != nil || me.Account.ID != ana.Account.ID || !me.Session.Current || me.Session.Device != "ana's phone" || w.who(cookie).AccountID != ana.Account.ID {
		t.Fatalf("confirming: %+v %v", me, err)
	}
	if l := w.providers(ana.Account.ID); len(l) != 1 || l[0].Provider != idp.Meta || l[0].Subject != "880022" {
		t.Fatalf("what the record lists: %+v", l)
	}
	// It waited for that, and is over: the code and the browser work once.
	if _, err := w.svc.PendingProvider(ctx, done.Browser); !errors.Is(err, contracts.ErrProviderFailed) {
		t.Fatalf("what was confirmed still waits: %v", err)
	}
	if _, _, err := w.svc.FinishProviderConfirm(ctx, done.Browser, contracts.ProviderConfirm{Attempt: attempt, Code: code(mail)}, Browser{UserAgent: ""}); !errors.Is(err, contracts.ErrProviderFailed) {
		t.Fatalf("confirming twice: %v", err)
	}
	// From now on her Meta account signs in with nothing else asked.
	if done, err = w.through(person(idp.Meta, "880022", "", false), ""); err != nil || w.who(done.Session).AccountID != ana.Account.ID {
		t.Fatalf("signing in after confirming: %+v %v", done, err)
	}

	// With the invitation given at the start, an address with no account
	// gets the message and, with its code, an account with the provider linked.
	attempt, mail = confirm(nobody, "nobody@example.org")
	if mail == nil || mail.To != "nobody@example.org" {
		t.Fatalf("the message of a sign-up: %+v", mail)
	}
	// The link of the message signs in by mail, in whatever browser, and
	// links nothing: a link can be sent on.
	if _, _, err := w.svc.FinishProviderConfirm(ctx, nobody, contracts.ProviderConfirm{Attempt: attempt, Code: linkRE.FindStringSubmatch(mail.Text)[2]}, Browser{UserAgent: ""}); !errors.Is(err, contracts.ErrInvalidCode) {
		t.Fatalf("the token of the link as a code: %v", err)
	}
	me, _, err = w.svc.FinishProviderConfirm(ctx, nobody, contracts.ProviderConfirm{Attempt: attempt, Code: code(mail)}, Browser{UserAgent: ""})
	if err != nil || me.Account.Address != "nobody@example.org" || me.Account.ID == ana.Account.ID {
		t.Fatalf("signing up by confirming: %+v %v", me, err)
	}
	if l := w.providers(me.Account.ID); len(l) != 1 || l[0].Subject != "770013" {
		t.Fatalf("what the new record lists: %+v", l)
	}

	// The provider account that waits in Mallory's browser with an
	// invitation: he confirms an address of his own and gets an account of
	// his own. What the provider said of Ana's address was only offered.
	attempt, mail = confirm(withInvitation, "mallory@example.net")
	me, _, err = w.svc.FinishProviderConfirm(ctx, withInvitation, contracts.ProviderConfirm{Attempt: attempt, Code: code(mail)}, Browser{UserAgent: ""})
	if err != nil || me.Account.Address != "mallory@example.net" || len(w.providers(ana.Account.ID)) != 1 {
		t.Fatalf("confirming another address: %+v %v", me, err)
	}

	// It waits twenty minutes, and not longer.
	w.now = w.now.Add(domain.ProviderConfirmLifetime - time.Second)
	if pending, err := w.svc.PendingProvider(ctx, malloryBrowser); err != nil || pending.ExpiresIn != 1 {
		t.Fatalf("a second before it ends: %+v %v", pending, err)
	}
	w.now = w.now.Add(time.Second)
	if _, err := w.svc.PendingProvider(ctx, malloryBrowser); !errors.Is(err, contracts.ErrProviderFailed) {
		t.Fatalf("after twenty minutes: %v", err)
	}
	w.quiet("ana@", "example.org", "example.net", "7700", "880022", "ms-sub-", ana.Account.ID, malloryBrowser, nobody)
}

// What confirming an address refuses once the address is the person's: the
// refusals name things only its owner is told.
func TestProviderConfirmRefusals(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	w.on(idp.Meta)
	ana, anaCookie := w.signUp("ana@example.org")
	// waits makes the trip for a Meta account and asks for the message.
	waits := func(subject, address string) (browser, attempt string, mail *Mail) {
		t.Helper()
		done, err := w.through(person(idp.Meta, subject, "", false), "")
		if err != nil {
			t.Fatal(err)
		}
		before := len(w.out.Sent())
		started, err := w.svc.StartProviderConfirm(ctx, done.Browser, contracts.ProviderConfirmStart{Address: address})
		if err != nil {
			t.Fatal(err)
		}
		w.svc.Wait()
		if sent := w.out.Sent(); len(sent) > before {
			mail = &sent[len(sent)-1]
		}
		return done.Browser, started.Attempt, mail
	}
	finish := func(browser, attempt string, mail *Mail) error {
		_, cookie, err := w.svc.FinishProviderConfirm(ctx, browser, contracts.ProviderConfirm{Attempt: attempt, Code: code(mail)}, Browser{UserAgent: ""})
		if err != nil && cookie != "" {
			t.Fatal("a refusal signed someone in")
		}
		return err
	}
	// Two browsers confirm the same address for two Meta accounts: the
	// account takes one of a provider.
	b1, a1, m1 := waits("990001", "ana@example.org")
	b2, a2, m2 := waits("990002", "ana@example.org")
	if err := finish(b1, a1, m1); err != nil {
		t.Fatal(err)
	}
	if err := finish(b2, a2, m2); !errors.Is(err, contracts.ErrProviderLinked) {
		t.Fatalf("a second account of one provider: %v", err)
	}
	// One Meta account waits in two browsers, for two addresses: it is
	// linked to the first that confirms.
	eve, _ := w.signUp("eve@example.org")
	w.now = w.now.Add(time.Hour + time.Minute) // an address is sent five messages an hour
	if err := w.svc.UnlinkProvider(ctx, w.who(anaCookie), "meta"); err != nil {
		t.Fatal(err)
	}
	if err := w.svc.UnlinkProvider(ctx, w.who(anaCookie), "meta"); !errors.Is(err, contracts.ErrNotFound) {
		t.Fatalf("unlinking twice: %v", err)
	}
	b1, a1, m1 = waits("990003", "eve@example.org")
	b2, a2, m2 = waits("990003", "ana@example.org")
	if err := finish(b1, a1, m1); err != nil {
		t.Fatal(err)
	}
	if err := finish(b2, a2, m2); !errors.Is(err, contracts.ErrProviderInUse) {
		t.Fatalf("a provider account that is another account's: %v", err)
	}
	if len(w.providers(ana.Account.ID)) != 0 || len(w.providers(eve.Account.ID)) != 1 {
		t.Fatalf("ana %+v, eve %+v", w.providers(ana.Account.ID), w.providers(eve.Account.ID))
	}
	// An account that is closing takes no change, and one that signs in with
	// passkeys only gets no message at all.
	w.now = w.now.Add(time.Hour + time.Minute)
	b1, a1, m1 = waits("990004", "ana@example.org")
	if _, err := w.svc.StartDeletion(ctx, w.who(anaCookie)); err != nil {
		t.Fatal(err)
	}
	if err := finish(b1, a1, m1); !errors.Is(err, contracts.ErrAccountClosing) {
		t.Fatalf("linking to a closing account: %v", err)
	}
	if _, err := w.svc.CancelDeletion(ctx, w.who(anaCookie)); err != nil {
		t.Fatal(err)
	}
	b1, a1, m1 = waits("990004", "ana@example.org")
	w.addPasskey(w.who(anaCookie), webauthntest.New(webauthntest.ES256), "key")
	w.passkeyOnly(w.who(anaCookie))
	if err := finish(b1, a1, m1); !errors.Is(err, contracts.ErrInvalidCode) {
		t.Fatalf("confirming for an account that turned mail off meanwhile: %v", err)
	}
	if _, _, mail := waits("990005", "ana@example.org"); mail != nil {
		t.Fatal("a message for an account that signs in with passkeys only")
	}
	if len(w.providers(ana.Account.ID)) != 0 {
		t.Fatalf("linked: %+v", w.providers(ana.Account.ID))
	}
}

// A provider is linked from Settings to the account of the session that
// started the trip, and to no other: not to the account the provider's
// address belongs to, and not to a second account.
func TestProviderLinkFromSettings(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	w.on(idp.Google, idp.GitHub, idp.Meta)
	ana, anaCookie := w.signUp("ana@example.org")
	eve, eveCookie := w.signUp("eve@example.org")
	anaWho, eveWho := w.who(anaCookie), w.who(eveCookie)

	list, err := w.svc.LinkedProviders(ctx, anaWho)
	if err != nil || list.Providers == nil || len(list.Providers) != 0 {
		t.Fatalf("no providers: %+v %v", list, err)
	}

	// The provider vouches for an address that is Eve's: Ana's session links
	// nothing, to her account or to Eve's.
	done, err := w.linkFrom(anaWho, person(idp.Google, "g-sub-of-eve", "Eve@example.org", true))
	w.refusedWith("an address that is another account's", done, err, contracts.ErrProviderInUse, "/settings#provider_error=provider_in_use")
	if len(w.providers(ana.Account.ID)) != 0 || len(w.providers(eve.Account.ID)) != 0 || len(w.stored("g-sub-of-eve")) != 0 {
		t.Fatalf("linked: ana %+v, eve %+v", w.providers(ana.Account.ID), w.providers(eve.Account.ID))
	}

	// An address nobody has, one that is hers, and one the provider does not
	// vouch for (Eve's, even) all link to the session's account: the address
	// decides nothing here.
	w.now = w.now.Add(time.Minute)
	state, cookie := w.leave(anaWho, contracts.ProviderStart{Provider: "google", ReturnTo: "/settings/sign-in", Invitation: "ignored"})
	done, err = w.comeBack(state, cookie, person(idp.Google, "g-sub-ana", "work@example.net", true))
	if err != nil || done.Path != "/settings/sign-in" || done.Session != "" || done.Browser != "" {
		t.Fatalf("linking: %+v %v", done, err)
	}
	if done, err = w.linkFrom(anaWho, person(idp.GitHub, "5150", "ana@example.org", true)); err != nil || done.Path != "/settings" {
		t.Fatalf("linking with her own address: %+v %v", done, err)
	}
	if done, err = w.linkFrom(anaWho, person(idp.Meta, "6160", "eve@example.org", false)); err != nil {
		t.Fatalf("linking with an address that is not vouched for: %+v %v", done, err)
	}
	list, err = w.svc.LinkedProviders(ctx, anaWho)
	if err != nil || len(list.Providers) != 3 || list.Providers[0] != (contracts.LinkedProvider{Provider: "google", LinkedAt: w.now}) || list.Providers[1].Provider != "github" || list.Providers[2].Provider != "meta" {
		t.Fatalf("the providers of the account: %+v %v", list, err)
	}
	if len(w.providers(eve.Account.ID)) != 0 || w.accounts() != 2 {
		t.Fatalf("eve %+v, %d accounts", w.providers(eve.Account.ID), w.accounts())
	}
	// Each signs in to Ana's account from now on, with no invitation.
	for _, who := range []idp.Identity{person(idp.Google, "g-sub-ana", "", false), person(idp.GitHub, "5150", "eve@example.org", true), person(idp.Meta, "6160", "", false)} {
		if done, err := w.through(who, ""); err != nil || w.who(done.Session).AccountID != ana.Account.ID {
			t.Fatalf("signing in with %s: %+v %v", who.Provider, done, err)
		}
	}

	// A provider account that is Ana's is not linked to Eve's, from her
	// Settings or by the address.
	done, err = w.linkFrom(eveWho, person(idp.Google, "g-sub-ana", "", false))
	w.refusedWith("a provider account of another account", done, err, contracts.ErrProviderInUse, "/settings#provider_error=provider_in_use")
	if len(w.providers(eve.Account.ID)) != 0 {
		t.Fatalf("linked to a second account: %+v", w.providers(eve.Account.ID))
	}
	if done, err := w.through(person(idp.Google, "g-sub-ana", "eve@example.org", true), ""); err != nil || w.who(done.Session).AccountID != ana.Account.ID {
		t.Fatalf("a linked provider account whose address is now another account's: %+v %v", done, err)
	}

	// One of each provider: told at the start, and at the end when two trips
	// were on their way.
	if _, _, err := w.svc.StartProvider(ctx, anaWho, contracts.ProviderStart{Provider: "google"}); !errors.Is(err, contracts.ErrProviderLinked) {
		t.Fatalf("a second account of one provider: %v", err)
	}
	s1, c1 := w.leave(eveWho, contracts.ProviderStart{Provider: "google"})
	s2, c2 := w.leave(eveWho, contracts.ProviderStart{Provider: "google"})
	if done, err := w.comeBack(s1, c1, person(idp.Google, "g-sub-eve-1", "", false)); err != nil {
		t.Fatalf("the first of two trips: %+v %v", done, err)
	}
	done, err = w.comeBack(s2, c2, person(idp.Google, "g-sub-eve-2", "", false))
	w.refusedWith("the second of two trips", done, err, contracts.ErrProviderLinked, "/settings#provider_error=provider_linked")
	// Linking what is linked already changes nothing.
	written := w.version(eve.Account.ID)
	if err := w.svc.UnlinkProvider(ctx, eveWho, "google"); err != nil {
		t.Fatal(err)
	}
	s1, c1 = w.leave(eveWho, contracts.ProviderStart{Provider: "google"})
	s2, c2 = w.leave(eveWho, contracts.ProviderStart{Provider: "google"})
	for _, trip := range [][2]string{{s1, c1}, {s2, c2}} {
		if done, err := w.comeBack(trip[0], trip[1], person(idp.Google, "g-sub-eve-1", "", false)); err != nil || done.Path != "/settings" {
			t.Fatalf("linking the same provider account twice: %+v %v", done, err)
		}
	}
	if l := w.providers(eve.Account.ID); len(l) != 1 || w.version(eve.Account.ID) != written+2 {
		t.Fatalf("eve %+v, written %d times more", l, w.version(eve.Account.ID)-written)
	}

	// The session that started the trip was closed while the person was
	// away, or its account went: nothing is linked.
	_, second := w.signIn("eve@example.org")
	state, cookie = w.leave(w.who(second), contracts.ProviderStart{Provider: "github"})
	if err := w.svc.CloseSession(ctx, eveWho, w.who(second).Session.ID); err != nil {
		t.Fatal(err)
	}
	done, err = w.comeBack(state, cookie, person(idp.GitHub, "7170", "", false))
	w.refusedWith("a session closed meanwhile", done, err, contracts.ErrProviderFailed, "/settings#provider_error=provider_failed")
	// A closing account links nothing: told at the start, and at the end
	// when it was asked meanwhile.
	state, cookie = w.leave(eveWho, contracts.ProviderStart{Provider: "github"})
	if _, err := w.svc.StartDeletion(ctx, eveWho); err != nil {
		t.Fatal(err)
	}
	done, err = w.comeBack(state, cookie, person(idp.GitHub, "7170", "", false))
	w.refusedWith("an account closing meanwhile", done, err, contracts.ErrAccountClosing, "/settings#provider_error=account_closing")
	if _, _, err := w.svc.StartProvider(ctx, eveWho, contracts.ProviderStart{Provider: "github"}); !errors.Is(err, contracts.ErrAccountClosing) {
		t.Fatalf("starting from a closing account: %v", err)
	}
	if err := w.svc.UnlinkProvider(ctx, eveWho, "google"); !errors.Is(err, contracts.ErrAccountClosing) {
		t.Fatalf("unlinking from a closing account: %v", err)
	}
	// It still signs in with the provider it has, to cancel.
	done, err = w.through(person(idp.Google, "g-sub-eve-1", "", false), "")
	if err != nil || w.who(done.Session).AccountID != eve.Account.ID {
		t.Fatalf("signing in to a closing account: %+v %v", done, err)
	}
	if got, err := w.svc.CancelDeletion(ctx, w.who(done.Session)); err != nil || got.State != contracts.AccountActive {
		t.Fatalf("cancelling: %+v %v", got, err)
	}
	if len(w.providers(eve.Account.ID)) != 1 || len(w.stored("7170")) != 0 {
		t.Fatalf("eve %+v, stored %q", w.providers(eve.Account.ID), w.stored("7170"))
	}
	ghost := &Authenticated{AccountID: "acc_" + strings.Repeat("a", 26)}
	if _, _, err := w.svc.StartProvider(ctx, ghost, contracts.ProviderStart{Provider: "google"}); !errors.Is(err, contracts.ErrUnauthorized) {
		t.Fatalf("starting for an account that is not there: %v", err)
	}
	if _, err := w.svc.LinkedProviders(ctx, ghost); !errors.Is(err, contracts.ErrUnauthorized) {
		t.Fatalf("the providers of an account that is not there: %v", err)
	}
	w.quiet("ana@", "example.org", "g-sub-", "5150", "6160", "7170", ana.Account.ID, eve.Account.ID)
}

// "Passkey only" turns providers off for the account, a closing account
// only cancels, and a provider without an application cannot be started.
func TestProviderRulesThatCarryOver(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	w.on(idp.Google)
	ana, anaCookie := w.signUp("ana@example.org")
	anaWho := w.who(anaCookie)
	mine := person(idp.Google, "g-sub-ana", "ana@example.org", true)
	if _, err := w.linkFrom(anaWho, mine); err != nil {
		t.Fatal(err)
	}
	w.addPasskey(anaWho, webauthntest.New(webauthntest.ES256), "key")
	w.passkeyOnly(anaWho)

	// The provider account she linked, and another that vouches for her
	// address, are refused with the reason of a trip that went wrong.
	for _, who := range []idp.Identity{mine, person(idp.Google, "g-sub-other", "ana@example.org", true)} {
		done, err := w.through(who, w.invite())
		w.refusedWith("passkey only", done, err, contracts.ErrProviderFailed, "/sign-in#provider_error=provider_failed")
	}
	if len(w.providers(ana.Account.ID)) != 1 || len(w.stored("g-sub-other")) != 0 {
		t.Fatalf("linked to an account that takes passkeys only: %+v", w.providers(ana.Account.ID))
	}
	if list, _ := w.svc.Sessions(ctx, anaWho); len(list.Sessions) != 1 {
		t.Fatalf("a session was made: %+v", list)
	}
	// Turned off again, the provider she linked signs in.
	if _, err := w.svc.SetPasskeyOnly(ctx, anaWho, contracts.PasskeyOnly{On: false}); err != nil {
		t.Fatal(err)
	}
	if done, err := w.through(mine, ""); err != nil || w.who(done.Session).AccountID != ana.Account.ID {
		t.Fatalf("after passkey only: %+v %v", done, err)
	}

	// A provider that is off, and a name that is no provider.
	for _, name := range []string{"github", "meta", "microsoft", "", "Google", "nope", "google "} {
		for _, who := range []*Authenticated{nil, anaWho} {
			if out, cookie, err := w.svc.StartProvider(ctx, who, contracts.ProviderStart{Provider: name}); !errors.Is(err, contracts.ErrProviderOff) || out != nil || cookie != "" {
				t.Errorf("%q started: %+v %v", name, out, err)
			}
		}
	}
	on, err := w.svc.ProvidersOn(ctx)
	if err != nil || len(on.Providers) != 1 || on.Providers[0] != "google" {
		t.Fatalf("on: %+v %v", on, err)
	}
	// Turned off while the person was away: the trip ends there, and the
	// provider is not asked.
	state, cookie := w.leave(nil, contracts.ProviderStart{Provider: "google"})
	if err := w.svc.RemoveProviderApp(ctx, "google"); err != nil {
		t.Fatal(err)
	}
	w.idp.app = idp.App{}
	done, err := w.comeBack(state, cookie, mine)
	w.refusedWith("turned off meanwhile", done, err, contracts.ErrProviderFailed, "/sign-in#provider_error=provider_failed")
	if w.idp.app != (idp.App{}) {
		t.Fatal("a provider that is off was asked")
	}
	if on, err := w.svc.ProvidersOn(ctx); err != nil || on.Providers == nil || len(on.Providers) != 0 {
		t.Fatalf("on: %+v %v", on, err)
	}
	// What was linked stays listed, for when it is on again.
	if l := w.providers(ana.Account.ID); len(l) != 1 {
		t.Fatalf("unlinked by turning the provider off: %+v", l)
	}
	w.on(idp.Google)
	if done, err := w.through(mine, ""); err != nil || w.who(done.Session).AccountID != ana.Account.ID {
		t.Fatalf("on again: %+v %v", done, err)
	}

	// A service that was given no way to ask the providers has none on,
	// whatever was set.
	w.svc.cfg.Providers = nil
	if on, err := w.svc.ProvidersOn(ctx); err != nil || len(on.Providers) != 0 {
		t.Fatalf("on without a client: %+v %v", on, err)
	}
	if _, _, err := w.svc.StartProvider(ctx, nil, contracts.ProviderStart{Provider: "google"}); !errors.Is(err, contracts.ErrProviderOff) {
		t.Fatalf("started without a client: %v", err)
	}
}

func TestUnlinkProvider(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	w.on(idp.Google, idp.GitHub)
	ana, anaCookie := w.signUp("ana@example.org")
	anaWho := w.who(anaCookie)
	hers := person(idp.GitHub, "5150", "elsewhere@example.net", false)
	for _, who := range []idp.Identity{person(idp.Google, "g-sub-ana", "", false), hers} {
		if _, err := w.linkFrom(anaWho, who); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"meta", "nope", "", "GitHub"} {
		if err := w.svc.UnlinkProvider(ctx, anaWho, name); !errors.Is(err, contracts.ErrNotFound) {
			t.Errorf("unlinking %q: %v", name, err)
		}
	}
	if err := w.svc.UnlinkProvider(ctx, anaWho, "github"); err != nil {
		t.Fatal(err)
	}
	if l := w.providers(ana.Account.ID); len(l) != 1 || l[0].Provider != idp.Google {
		t.Fatalf("after unlinking: %+v", l)
	}
	// Nothing of it stays in the storage, and it signs nobody in: it is a
	// provider account nobody knows, whose address is to be confirmed.
	if left := w.stored("5150"); len(left) != 0 {
		t.Fatalf("left in the storage: %q", left)
	}
	done, err := w.through(hers, "")
	if err != nil || done.Session != "" || done.Path != "/sign-in#provider_confirm" {
		t.Fatalf("signing in with an unlinked provider account: %+v %v", done, err)
	}
	// It can be linked again, and to another account.
	_, eveCookie := w.signUp("eve@example.org")
	if _, err := w.linkFrom(w.who(eveCookie), hers); err != nil {
		t.Fatalf("linking what another account unlinked: %v", err)
	}

	// The only way left to sign in stays. Today an account always has mail
	// or a passkey, so this is reached only by a record that says otherwise.
	alone := func(doc *accountDoc) error {
		doc.PasskeyOnly = true
		return nil
	}
	if _, err := w.svc.change(ctx, ana.Account.ID, alone); err != nil {
		t.Fatal(err)
	}
	if err := w.svc.UnlinkProvider(ctx, anaWho, "google"); !errors.Is(err, contracts.ErrLastWayIn) {
		t.Fatalf("unlinking the only way in: %v", err)
	}
	if l := w.providers(ana.Account.ID); len(l) != 1 {
		t.Fatalf("after a refusal: %+v", l)
	}
	// With a passkey there is another way.
	if _, err := w.svc.change(ctx, ana.Account.ID, func(doc *accountDoc) error {
		doc.Passkeys = []passkeyDoc{{ID: []byte("a key")}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := w.svc.UnlinkProvider(ctx, anaWho, "google"); err != nil {
		t.Fatalf("unlinking with a passkey left: %v", err)
	}
	w.quiet("5150", "g-sub-ana", "example.net", ana.Account.ID)
}

// Whoever writes to the storage cannot link a provider account to an
// account, bring one that was unlinked back, or lead one to another account:
// the pointer proves nothing, and the record is signed and anchored.
func TestProvidersWrittenByHand(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	w.on(idp.Google, idp.GitHub)
	ana, anaCookie := w.signUp("ana@example.org")
	eve, eveCookie := w.signUp("eve@example.org")
	anaWho, eveWho := w.who(anaCookie), w.who(eveCookie)
	eves := person(idp.Google, "g-sub-eve", "eve@example.org", true)
	if _, err := w.linkFrom(eveWho, eves); err != nil {
		t.Fatal(err)
	}
	// notAna fails when a trip ended in Ana's account.
	notAna := func(what string, done *ProviderDone, err error) {
		t.Helper()
		if done.Session != "" {
			if who := w.who(done.Session); who.AccountID == ana.Account.ID {
				t.Fatalf("%s: signed in to an account that never linked it", what)
			}
		}
		if len(w.providers(ana.Account.ID)) != 0 {
			t.Fatalf("%s: linked to the account (%v)", what, err)
		}
	}

	// A pointer for Mallory's provider account that names Ana's account: a
	// copy of the pointer from her address, a copy of Eve's from her
	// provider account, and one written without the key.
	addressPointer, _ := w.mem.GetEntry(ctx, loginPartition("ana@example.org"), loginKey)
	evePointer, _ := w.mem.GetEntry(ctx, providerPartition(idp.Google, "g-sub-eve"), loginKey)
	byHand := []byte(`{"doc":{"account":"` + ana.Account.ID + `"},"mac":"AAAA"}`)
	for name, value := range map[string][]byte{"her address's pointer": addressPointer.Value, "another provider account's pointer": evePointer.Value, "written by hand": byHand} {
		w.mem.SetEntry(store.Entry{Partition: providerPartition(idp.Google, "g-sub-mallory"), Key: loginKey, Value: value, Version: 3})
		done, err := w.through(person(idp.Google, "g-sub-mallory", "mallory@example.net", true), "")
		w.refusedWith(name, done, err, contracts.ErrInvitationNeeded, "/sign-in#provider_error=invitation_needed")
		done, err = w.through(person(idp.Google, "g-sub-mallory", "ana@example.org", false), "")
		if notAna(name, done, err); err != nil || done.Path != "/sign-in#provider_confirm" {
			t.Fatalf("%s: %+v %v", name, done, err)
		}
	}
	// Even a pointer the enclave signed for that very place proves nothing
	// without the record: here with the record changed by hand to list it,
	// which makes the account one that cannot be read, and signs nobody in.
	_ = w.mem.DeleteEntry(ctx, providerPartition(idp.Google, "g-sub-mallory"), loginKey)
	if err := w.svc.point(ctx, providerPartition(idp.Google, "g-sub-mallory"), loginKey, ana.Account.ID, 0); err != nil {
		t.Fatal(err)
	}
	done, err := w.through(person(idp.Google, "g-sub-mallory", "", false), "")
	if notAna("a pointer without the record", done, err); err != nil || done.Session != "" {
		t.Fatalf("a pointer without the record: %+v %v", done, err)
	}
	honest := w.record(w.newest(ana.Account.ID))
	forged := honest
	forged.Value = bytes.Replace(honest.Value, []byte(`"language":"en"`), []byte(`"language":"en","providers":[{"provider":"google","subject":"g-sub-mallory","linked_at":"2026-11-11T09:00:00Z"}]`), 1)
	if bytes.Equal(forged.Value, honest.Value) {
		t.Fatal("the record was not changed")
	}
	w.mem.SetRecord(forged)
	done, err = w.through(person(idp.Google, "g-sub-mallory", "", false), "")
	if err == nil || done.Session != "" || reason(done) != "provider_unavailable" {
		t.Fatalf("a record changed by hand: %+v %v", done, err)
	}
	w.mem.SetRecord(honest)
	notAna("after the record was put right", done, err)

	// Eve's provider account led to Ana's account: by a copy of Ana's
	// pointer, or by hand. It signs in to Eve's, whose record lists it.
	for name, value := range map[string][]byte{"her address's pointer": addressPointer.Value, "written by hand": byHand} {
		w.mem.SetEntry(store.Entry{Partition: providerPartition(idp.Google, "g-sub-eve"), Key: loginKey, Value: value, Version: 7})
		done, err := w.through(eves, "")
		if err != nil || w.who(done.Session).AccountID != eve.Account.ID {
			t.Fatalf("a provider account led elsewhere (%s): %+v %v", name, done, err)
		}
	}

	// Ana links a provider account and unlinks it. Everything the storage
	// held while it was linked is put back: it stays unlinked.
	hers := person(idp.GitHub, "5150", "other@example.net", false)
	if _, err := w.linkFrom(anaWho, hers); err != nil {
		t.Fatal(err)
	}
	if done, err := w.through(hers, ""); err != nil || w.who(done.Session).AccountID != ana.Account.ID {
		t.Fatalf("while linked: %+v %v", done, err)
	}
	while := w.snapshot()
	oldPointer, _ := w.mem.GetEntry(ctx, providerPartition(idp.GitHub, "5150"), loginKey)
	if err := w.svc.UnlinkProvider(ctx, anaWho, "github"); err != nil {
		t.Fatal(err)
	}
	w.putBack(while)
	done, err = w.through(hers, "")
	if notAna("put back after unlinking", done, err); err != nil || done.Path != "/sign-in#provider_confirm" {
		t.Fatalf("put back after unlinking: %+v %v", done, err)
	}
	if list, err := w.svc.LinkedProviders(ctx, anaWho); err != nil || len(list.Providers) != 0 {
		t.Fatalf("listed after being put back: %+v %v", list, err)
	}
	// The same with the earlier copy written over the newest version of the
	// record: the account cannot be read, which is not the same as linked.
	var earlier store.Record
	for _, r := range while.records {
		if strings.HasPrefix(r.Name, "account-"+ana.Account.ID+"@") {
			earlier = r
		}
	}
	newest := w.record(w.newest(ana.Account.ID))
	over := newest
	over.Value = earlier.Value
	w.mem.SetRecord(over)
	done, err = w.through(hers, "")
	if err == nil || done.Session != "" || reason(done) != "provider_unavailable" {
		t.Fatalf("an earlier copy over the newest: %+v %v", done, err)
	}
	w.mem.SetRecord(newest)

	// Eve links the provider account Ana unlinked. The pointer from when it
	// was Ana's is put back: it signs nobody in to Ana's account.
	if _, err := w.linkFrom(eveWho, hers); err != nil {
		t.Fatal(err)
	}
	w.mem.SetEntry(*oldPointer)
	done, err = w.through(person(idp.GitHub, "5150", "eve@example.org", true), "")
	if notAna("a pointer from before", done, err); err != nil || w.who(done.Session).AccountID != eve.Account.ID {
		t.Fatalf("a pointer from before: %+v %v", done, err)
	}
	// And Ana cannot take it back from her Settings while it is Eve's.
	done, err = w.linkFrom(anaWho, hers)
	w.refusedWith("linking what is another account's", done, err, contracts.ErrProviderInUse, "/settings#provider_error=provider_in_use")
	if l := w.providers(eve.Account.ID); len(l) != 2 || len(w.providers(ana.Account.ID)) != 0 {
		t.Fatalf("eve %+v, ana %+v", l, w.providers(ana.Account.ID))
	}
}

// Starting says nothing of who has an account: it asks for no address and
// reads nothing of any account. Every way the callback fails ends with one
// of a few reasons.
func TestProviderReasons(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	w.on(idp.Google, idp.Meta)
	w.signUp("ana@example.org")
	_, eveCookie := w.signUp("eve@example.org")

	counted := &counting{Records: w.svc.cfg.Records, Entries: w.mem}
	w.svc.cfg.Records, w.svc.cfg.Entries = counted, counted
	w.leave(nil, contracts.ProviderStart{Provider: "google", ReturnTo: "/x"})
	if counted.gets != 0 {
		t.Fatalf("starting read the storage %d times", counted.gets)
	}
	w.leave(nil, contracts.ProviderStart{Provider: "google", Invitation: w.invite()})
	if counted.gets != 1 {
		t.Fatalf("starting with an invitation read the storage %d times, not the invitation alone", counted.gets)
	}
	w.svc.cfg.Records, w.svc.cfg.Entries = counted.Records, w.mem

	fixed := map[string]bool{
		"provider_failed": true, "provider_unavailable": true, "invitation_needed": true, "invalid_invitation": true,
		"provider_in_use": true, "provider_linked": true, "account_closing": true, "account_blocked": true,
	}
	if len(providerReasons) != len(fixed) {
		t.Fatalf("%d reasons", len(providerReasons))
	}
	for _, r := range providerReasons {
		if !fixed[r.Code] {
			t.Errorf("a reason that is not one of the fixed ones: %s", r.Code)
		}
	}
	// Whatever the failure, the reason is one of them.
	for err, want := range map[error]string{
		contracts.ErrProviderFailed:                       "provider_failed",
		contracts.ErrUnauthorized:                         "provider_failed",
		contracts.ErrNotFound:                             "provider_failed",
		contracts.ErrInvalidCode:                          "provider_failed",
		contracts.ErrInvalidAddress:                       "provider_failed",
		fmt.Errorf("wrapped: %w", contracts.ErrLastWayIn): "provider_failed",
		contracts.ErrProviderInUse:                        "provider_in_use",
		contracts.ErrAccountClosing:                       "account_closing",
		errors.New("store: down"):                         "provider_unavailable",
		store.ErrLost:                                     "provider_unavailable",
	} {
		if got := providerReason(err); got != want || !fixed[got] {
			t.Errorf("%v: %q, want %q", err, got, want)
		}
	}

	// To a stranger (someone the provider does not vouch for as the owner of
	// an address) every outcome is the same whether the address or the
	// provider account has an account: the screen that asks for an address.
	var seen []string
	for _, who := range []idp.Identity{
		person(idp.Meta, "1", "ana@example.org", false), person(idp.Meta, "2", "nobody@example.org", false),
		person(idp.Google, "3", "ana@example.org", false), person(idp.Google, "4", "", false),
	} {
		done, err := w.through(who, "")
		if err != nil || done.Session != "" {
			t.Fatalf("%+v: %+v %v", who, done, err)
		}
		seen = append(seen, done.Path)
	}
	for _, path := range seen {
		if path != seen[0] {
			t.Fatalf("a stranger is answered differently: %q", seen)
		}
	}
	// The same failure answers the same on the two screens.
	done, err := w.svc.FinishProvider(ctx, ProviderAnswer{State: "unknown", Code: "x", Browser: "y"})
	w.refusedWith("a state nobody started", done, err, contracts.ErrProviderFailed, "/sign-in#provider_error=provider_failed")
	// A trip the service does not know names no provider.
	if done.Provider != "" {
		t.Fatalf("a state nobody started names %q", done.Provider)
	}
	state, cookie := w.leave(w.who(eveCookie), contracts.ProviderStart{Provider: "google"})
	done, err = w.svc.FinishProvider(ctx, ProviderAnswer{State: state, Refused: true, Browser: cookie})
	w.refusedWith("refused at the provider, from Settings", done, err, contracts.ErrProviderFailed, "/settings#provider_error=provider_failed")
	// A trip that failed names the provider it was started with, and one
	// tried again, or in a browser that did not start it, names none.
	if done.Provider != idp.Google {
		t.Fatalf("a trip that failed names %q", done.Provider)
	}
	if done, _ = w.svc.FinishProvider(ctx, ProviderAnswer{State: state, Refused: true, Browser: cookie}); done.Provider != "" {
		t.Fatalf("the same state again names %q", done.Provider)
	}
	state, _ = w.leave(nil, contracts.ProviderStart{Provider: "meta"})
	if done, _ = w.svc.FinishProvider(ctx, ProviderAnswer{State: state, Code: "x", Browser: "another-browser"}); done.Provider != "" {
		t.Fatalf("an answer in another browser names %q", done.Provider)
	}
	state, cookie = w.leave(nil, contracts.ProviderStart{Provider: "meta"})
	if done, _ = w.svc.FinishProvider(ctx, ProviderAnswer{State: state, Refused: true, Browser: cookie}); done.Provider != idp.Meta || done.Path != "/sign-in#provider_error=provider_failed" {
		t.Fatalf("a sign-in that failed: %+v", done)
	}
	// A trip that worked names none: there is nothing to say of it.
	if done, err = w.through(person(idp.Google, "named-1", "ana@example.org", true), ""); err != nil || done.Provider != "" {
		t.Fatalf("a trip that worked: %+v %v", done, err)
	}
}

// Where the browser goes after the callback is a path of the app, whatever
// the start was told.
func TestProviderReturnPath(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	w.on(idp.Google, idp.Meta)
	_, anaCookie := w.signUp("ana@example.org")
	mine := person(idp.Google, "g-sub-ana", "ana@example.org", true)
	leaving := []string{
		"https://evil.example", "https://evil.example/sign-in", "//evil.example", `/\evil.example`, `\\evil.example`, "evil.example",
		"/ok\r\nLocation: https://evil.example", "javascript:alert(1)", " /connections", "/api/v1/auth/provider/callback?state=x", "/" + strings.Repeat("a", 600),
	}
	for _, to := range leaving {
		state, cookie := w.leave(nil, contracts.ProviderStart{Provider: "google", ReturnTo: to})
		if done, err := w.comeBack(state, cookie, mine); err != nil || done.Path != "/" {
			t.Errorf("signing in, to come back to %q: %+v %v", to, done, err)
		}
		// A failure goes to a fixed screen.
		state, cookie = w.leave(nil, contracts.ProviderStart{Provider: "google", ReturnTo: to})
		if done, _ := w.svc.FinishProvider(ctx, ProviderAnswer{State: state, Refused: true, Browser: cookie}); done.Path != "/sign-in#provider_error=provider_failed" {
			t.Errorf("failing, to come back to %q: %+v", to, done)
		}
		// So does a sign-in that waits, and what it says of where to go next.
		state, cookie = w.leave(nil, contracts.ProviderStart{Provider: "meta", ReturnTo: to})
		done, err := w.comeBack(state, cookie, person(idp.Meta, "1", "", false))
		if err != nil || done.Path != "/sign-in#provider_confirm" {
			t.Errorf("waiting, to come back to %q: %+v %v", to, done, err)
		}
		if pending, err := w.svc.PendingProvider(ctx, done.Browser); err != nil || pending.ReturnTo != "/" {
			t.Errorf("what waits, to come back to %q: %+v %v", to, pending, err)
		}
	}
	// From Settings: back to Settings.
	state, cookie := w.leave(w.who(anaCookie), contracts.ProviderStart{Provider: "meta", ReturnTo: "https://evil.example"})
	if done, err := w.comeBack(state, cookie, person(idp.Meta, "2", "", false)); err != nil || done.Path != "/settings" {
		t.Fatalf("linking, to come back elsewhere: %+v %v", done, err)
	}
	// A path of the app is kept, also by what waits.
	state, cookie = w.leave(nil, contracts.ProviderStart{Provider: "meta", ReturnTo: "/connections/new?from=mail#top"})
	done, err := w.comeBack(state, cookie, person(idp.Meta, "3", "", false))
	if err != nil {
		t.Fatal(err)
	}
	if pending, err := w.svc.PendingProvider(ctx, done.Browser); err != nil || pending.ReturnTo != "/connections/new?from=mail#top" {
		t.Fatalf("what waits: %+v %v", pending, err)
	}
}

// The applications' secrets are kept sealed, are never answered or logged,
// and one that was replaced or removed does not come back.
func TestProviderApps(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	const secret, replaced = "GOCSPX-first~Secret_0001", "GOCSPX-second~Secret_0002"
	list, err := w.svc.ProviderApps(ctx)
	if err != nil || list.Providers == nil || len(list.Providers) != 0 {
		t.Fatalf("none set: %+v %v", list, err)
	}
	if len(w.mem.RecordNames()) != 0 {
		t.Fatalf("looking wrote something: %v", w.mem.RecordNames())
	}
	set, err := w.svc.SetProviderApp(ctx, "google", contracts.ProviderAppSet{ClientID: "1234-abc.apps.googleusercontent.com", ClientSecret: secret})
	if err != nil || *set != (contracts.ProviderApp{Provider: "google", ClientID: "1234-abc.apps.googleusercontent.com", SetAt: w.now}) {
		t.Fatalf("set: %+v %v", set, err)
	}
	w.now = w.now.Add(time.Minute)
	if _, err := w.svc.SetProviderApp(ctx, "microsoft", contracts.ProviderAppSet{ClientID: "00000000-aaaa-bbbb-cccc-000000000001", ClientSecret: "ms~Secret.0003"}); err != nil {
		t.Fatal(err)
	}
	list, err = w.svc.ProviderApps(ctx)
	if err != nil || len(list.Providers) != 2 || list.Providers[0] != *set || list.Providers[1].Provider != "microsoft" || !list.Providers[1].SetAt.Equal(w.now) {
		t.Fatalf("the list: %+v %v", list, err)
	}
	if strings.Contains(fmt.Sprintf("%+v", list), "Secret") {
		t.Fatalf("the list holds a secret: %+v", list)
	}
	// The storage holds them sealed: neither a secret nor a client id can be
	// read there, and the record is not one meant to be read.
	if left := w.stored(secret, "ms~Secret", "googleusercontent", "client_secret"); len(left) != 0 {
		t.Fatalf("readable in the storage: %q", left)
	}
	name := ""
	for _, n := range w.mem.RecordNames() {
		if strings.HasPrefix(n, providerAppsRecord+"@") {
			name = n
		}
	}
	if rec := w.record(name); rec.Plain || len(w.mem.RecordNames()) != 1 {
		t.Fatalf("the record of the applications: plain %v, among %v", rec.Plain, w.mem.RecordNames())
	}

	// What cannot be an application, and what is no provider. The refusal
	// never repeats what it was given.
	for _, bad := range []contracts.ProviderAppSet{
		{}, {ClientID: "id"}, {ClientSecret: "Secret-9"}, {ClientID: "two words", ClientSecret: "Secret-9"}, {ClientID: "id", ClientSecret: "Secret-9\n"},
		{ClientID: "id", ClientSecret: " Secret-9"}, {ClientID: "id", ClientSecret: strings.Repeat("S", 513)}, {ClientID: "ñ", ClientSecret: "Secret-9"},
	} {
		_, err := w.svc.SetProviderApp(ctx, "github", bad)
		if !errors.Is(err, contracts.ErrInvalidProviderApp) || strings.Contains(err.Error(), "Secret-9") {
			t.Errorf("%+v: %v", bad, err)
		}
	}
	for _, name := range []string{"", "Google", "nope", "google/x"} {
		if _, err := w.svc.SetProviderApp(ctx, name, contracts.ProviderAppSet{ClientID: "id", ClientSecret: "Secret-9"}); !errors.Is(err, contracts.ErrNotFound) {
			t.Errorf("setting %q: %v", name, err)
		}
		if err := w.svc.RemoveProviderApp(ctx, name); !errors.Is(err, contracts.ErrNotFound) {
			t.Errorf("removing %q: %v", name, err)
		}
	}
	if on, _ := w.svc.ProvidersOn(ctx); len(on.Providers) != 2 || on.Providers[0] != "google" || on.Providers[1] != "microsoft" {
		t.Fatalf("on: %+v", on)
	}

	// The provider is asked with the secret, after a restart too: it is read
	// from the sealed record.
	asked := func(svc *Service) idp.App {
		t.Helper()
		w.idp.app = idp.App{}
		started, cookie, err := svc.StartProvider(ctx, nil, contracts.ProviderStart{Provider: "google"})
		if err != nil {
			return idp.App{}
		}
		u, _ := url.Parse(started.URL)
		_, _ = svc.FinishProvider(ctx, ProviderAnswer{State: u.Query().Get("state"), Code: w.idp.approve(person(idp.Google, "1", "", false)), Browser: cookie})
		return w.idp.app
	}
	if got := asked(w.svc); got.ClientSecret != secret {
		t.Fatalf("asked with %+v", got)
	}
	restarted := w.service(w.svc.cfg.Keys)
	if got := asked(restarted); got.ClientSecret != secret || got.ClientID != "1234-abc.apps.googleusercontent.com" {
		t.Fatalf("after a restart, asked with %+v", got)
	}

	for _, line := range []string{"a provider's application was set", "a provider's application was removed"} {
		if line == "a provider's application was removed" {
			if err := w.svc.RemoveProviderApp(ctx, "microsoft"); err != nil {
				t.Fatal(err)
			}
			w.on(idp.Microsoft)
		}
		if !strings.Contains(w.logs.String(), line) {
			t.Fatalf("the log does not record %q:\n%s", line, w.logs)
		}
	}
	w.quiet(secret, "ms~Secret", "googleusercontent")

	// Replaced: what the storage held before is put back, and the secret
	// from before is never used again.
	before := w.snapshot()
	versions := w.mem.RecordNames()
	if _, err := w.svc.SetProviderApp(ctx, "google", contracts.ProviderAppSet{ClientID: "1234-abc.apps.googleusercontent.com", ClientSecret: replaced}); err != nil {
		t.Fatal(err)
	}
	if got := asked(w.svc); got.ClientSecret != replaced {
		t.Fatalf("after replacing, asked with %+v", got)
	}
	w.putBack(before)
	if got := asked(w.service(w.svc.cfg.Keys)); got.ClientSecret != replaced {
		t.Fatalf("with the storage put back, asked with %+v", got)
	}
	// Removed: the provider is off, and stays off with the storage put back.
	if err := w.svc.RemoveProviderApp(ctx, "google"); err != nil {
		t.Fatal(err)
	}
	written := len(w.mem.RecordNames())
	if err := w.svc.RemoveProviderApp(ctx, "google"); err != nil || len(w.mem.RecordNames()) != written {
		t.Fatalf("removing what is not set: %v", err)
	}
	if got := asked(w.svc); got != (idp.App{}) {
		t.Fatalf("a removed application was used: %+v", got)
	}
	w.putBack(before)
	back := w.service(w.svc.cfg.Keys)
	if got := asked(back); got != (idp.App{}) {
		t.Fatalf("a removed application came back: %+v", got)
	}
	if on, err := back.ProvidersOn(ctx); err != nil || len(on.Providers) != 1 || on.Providers[0] != "microsoft" {
		t.Fatalf("on with the storage put back: %+v %v", on, err)
	}
	// With the earlier copy written over the newest version, or the newest
	// deleted, what was committed is lost: every provider is off, and the
	// earlier copy is never taken for it.
	newest := func() string {
		t.Helper()
		name := ""
		for _, n := range w.mem.RecordNames() {
			if strings.HasPrefix(n, providerAppsRecord+"@") {
				name = n // the names are in order, and the version is part of them
			}
		}
		return name
	}
	earlier := w.record(versions[0]).Value
	for name, tamper := range map[string]func(){
		"an earlier copy over the newest": func() {
			over := w.record(newest())
			over.Value = earlier
			w.mem.SetRecord(over)
		},
		"the newest deleted": func() { _ = w.mem.DeleteRecord(ctx, newest()) },
	} {
		tamper()
		lost := w.service(w.svc.cfg.Keys)
		if got := asked(lost); got != (idp.App{}) {
			t.Fatalf("%s: a provider was asked with %+v", name, got)
		}
		if on, err := lost.ProvidersOn(ctx); err != nil || len(on.Providers) != 0 {
			t.Fatalf("%s: on %+v %v", name, on, err)
		}
		if !strings.Contains(w.logs.String(), "is lost; every provider is off") {
			t.Fatalf("%s: not logged:\n%s", name, w.logs)
		}
		w.logs.Reset()
		// The operator sets one again, and it is on.
		if _, err := lost.SetProviderApp(ctx, "google", contracts.ProviderAppSet{ClientID: "new-id", ClientSecret: replaced}); err != nil {
			t.Fatalf("%s: setting again: %v", name, err)
		}
		if got := asked(w.service(w.svc.cfg.Keys)); got.ClientSecret != replaced || got.ClientID != "new-id" {
			t.Fatalf("%s: set again, asked with %+v", name, got)
		}
	}

	// Under another deployment's root secret the record does not open: an
	// error, and no provider on.
	local, _ := keys.NewLocal()
	other := w.service(local)
	if _, err := other.ProvidersOn(ctx); err == nil || strings.Contains(err.Error(), "Secret") {
		t.Fatalf("another deployment: %v", err)
	}
	if _, _, err := other.StartProvider(ctx, nil, contracts.ProviderStart{Provider: "google"}); err == nil {
		t.Fatal("another deployment started a trip")
	}
	if _, err := other.SetProviderApp(ctx, "google", contracts.ProviderAppSet{ClientID: "id", ClientSecret: "Secret-9"}); err == nil {
		t.Fatal("another deployment wrote over what it cannot open")
	}
	if _, err := other.ProviderApps(ctx); err == nil {
		t.Fatal("another deployment listed what it cannot open")
	}
	w.quiet(secret, replaced, "ms~Secret", "Secret-9", "googleusercontent", "new-id")
}

// Storage, keys or a provider that fail never let someone in, and never
// make an account twice.
func TestProviderFailures(t *testing.T) {
	down := errors.New("down, with acc_" + strings.Repeat("a", 26))
	ctx := context.Background()
	setup := func(t *testing.T) (*world, *failing) {
		w := newWorld(t)
		w.on(idp.Google, idp.Meta)
		f := &failing{Records: w.svc.cfg.Records, Entries: w.mem, Keys: w.svc.cfg.Keys, fail: map[string]error{}}
		w.svc.cfg.Records, w.svc.cfg.Entries, w.svc.cfg.Keys = f, f, f
		return w, f
	}
	nina := person(idp.Google, "g-sub-nina", "nina@example.org", true)

	// Each step of a sign-up through a provider.
	for _, op := range []string{"get entry", "delete entry", "put entry", "put record", "get record", "list entries", "derive", "put " + providerPartition(idp.Google, "g-sub-nina")} {
		w, f := setup(t)
		state, cookie := w.leave(nil, contracts.ProviderStart{Provider: "google", Invitation: w.invite()})
		f.fail[op] = down
		done, err := w.comeBack(state, cookie, nina)
		if !errors.Is(err, down) || done.Session != "" || done.Path != "/sign-in#provider_error=provider_unavailable" {
			t.Fatalf("%s down: %+v %v", op, done, err)
		}
		f.fail = map[string]error{}
		if n := w.accounts(); n > 1 {
			t.Fatalf("%s down: %d accounts", op, n)
		}
		// When it works again the person signs in, to the one account of
		// their address.
		for range 2 {
			done, err = w.through(nina, w.invite())
			if err != nil || w.who(done.Session) == nil {
				t.Fatalf("after %s was down: %+v %v", op, done, err)
			}
		}
		if n := w.accounts(); n != 1 {
			t.Fatalf("after %s was down: %d accounts", op, n)
		}
		if l := w.providers(w.who(done.Session).AccountID); len(l) != 1 || l[0].Subject != "g-sub-nina" {
			t.Fatalf("after %s was down: %+v", op, l)
		}
	}

	// Signing in with a provider account that is linked, linking from
	// Settings, confirming an address and unlinking.
	for _, op := range []string{"get entry", "get record", "put entry", "put record", "list entries", "derive"} {
		w, f := setup(t)
		done, err := w.through(nina, w.invite())
		if err != nil {
			t.Fatal(err)
		}
		who := w.who(done.Session)
		sessions := func() int {
			t.Helper()
			list, err := w.svc.Sessions(ctx, who)
			if err != nil {
				t.Fatal(err)
			}
			return len(list.Sessions)
		}
		state, cookie := w.leave(nil, contracts.ProviderStart{Provider: "google"})
		linkState, linkCookie := w.leave(who, contracts.ProviderStart{Provider: "meta"})
		waiting, err := w.through(person(idp.Meta, "880022", "", false), "")
		if err != nil {
			t.Fatal(err)
		}
		started, err := w.svc.StartProviderConfirm(ctx, waiting.Browser, contracts.ProviderConfirmStart{Address: "nina@example.org"})
		if err != nil {
			t.Fatal(err)
		}
		w.svc.Wait()
		sent := w.out.Sent()
		before := sessions()

		f.fail[op] = down
		done, err = w.comeBack(state, cookie, nina)
		if !errors.Is(err, down) || reason(done) != "provider_unavailable" {
			// A sign-in writes a session and no record.
			if op != "put record" || err != nil {
				t.Fatalf("%s down, signing in: %+v %v", op, done, err)
			}
		}
		done, err = w.comeBack(linkState, linkCookie, person(idp.Meta, "770011", "", false))
		if !errors.Is(err, down) || done.Path != "/settings#provider_error=provider_unavailable" {
			if op != "list entries" || err != nil {
				t.Fatalf("%s down, linking: %+v %v", op, done, err)
			}
		}
		_, session, err := w.svc.FinishProviderConfirm(ctx, waiting.Browser, contracts.ProviderConfirm{Attempt: started.Attempt, Code: code(&sent[len(sent)-1])}, Browser{UserAgent: ""})
		if (err == nil) != (session != "") {
			t.Fatalf("%s down, confirming: a session %v with %v", op, session != "", err)
		}
		if err := w.svc.UnlinkProvider(ctx, who, "google"); !errors.Is(err, down) && op != "list entries" && op != "put entry" {
			t.Fatalf("%s down, unlinking: %v", op, err)
		}
		if _, err := w.svc.LinkedProviders(ctx, who); !errors.Is(err, down) && op == "get record" {
			t.Fatalf("%s down, listing: %v", op, err)
		}
		if _, _, err := w.svc.StartProvider(ctx, who, contracts.ProviderStart{Provider: "microsoft"}); err == nil {
			t.Fatalf("%s down: a provider that is off was started", op)
		}
		if op == "get entry" || op == "derive" {
			if _, _, err := w.svc.StartProvider(ctx, nil, contracts.ProviderStart{Provider: "google", Invitation: "abcd-2345-wxyz"}); !errors.Is(err, down) && op == "get entry" {
				t.Fatalf("%s down, starting with an invitation: %v", op, err)
			}
		}
		f.fail = map[string]error{}
		if w.accounts() != 1 {
			t.Fatalf("%s down: %d accounts", op, w.accounts())
		}
		if op == "get entry" || op == "get record" {
			if now := sessions(); now != before {
				t.Fatalf("%s down: %d sessions more", op, now-before)
			}
		}
	}

	// The provider fails: by not answering, by an answer that is refused,
	// and in a way of its own. None signs in, and what is logged holds
	// nothing of an answer.
	w, _ := setup(t)
	for err, want := range map[error]*contracts.Error{
		fmt.Errorf("%w: no answer from the token endpoint", idp.ErrUnreachable): contracts.ErrProviderUnavailable,
		fmt.Errorf("%w: the nonce is another", idp.ErrInvalid):                  contracts.ErrProviderFailed,
	} {
		state, cookie := w.leave(nil, contracts.ProviderStart{Provider: "google", Invitation: w.invite()})
		w.idp.err = err
		done, got := w.comeBack(state, cookie, nina)
		w.refusedWith(err.Error(), done, got, want, "/sign-in#provider_error="+want.Code)
	}
	state, cookie := w.leave(nil, contracts.ProviderStart{Provider: "google", Invitation: w.invite()})
	w.idp.err = errors.New("boom: client_secret=" + appSecret(idp.Google) + " for nina@example.org")
	done, err := w.comeBack(state, cookie, nina)
	var known *contracts.Error
	if err == nil || errors.As(err, &known) || strings.Contains(err.Error(), "boom") || done.Path != "/sign-in#provider_error=provider_unavailable" {
		t.Fatalf("a failure of the provider's own: %+v %v", done, err)
	}
	w.idp.err = nil
	w.idp.beginErr = errors.New("boom: client_secret=" + appSecret(idp.Google))
	if _, cookie, err := w.svc.StartProvider(ctx, nil, contracts.ProviderStart{Provider: "google"}); err == nil || cookie != "" || strings.Contains(err.Error(), "boom") || len(w.svc.trips) != 0 {
		t.Fatalf("a start the provider's client refused: %v", err)
	}
	if w.accounts() != 0 {
		t.Fatalf("%d accounts", w.accounts())
	}
	logs := w.logs.String()
	for _, line := range []string{"a provider could not be asked who signed in", "a provider's answer was refused", "no answer from the token endpoint", "the nonce is another"} {
		if !strings.Contains(logs, line) {
			t.Fatalf("the log does not record %q:\n%s", line, logs)
		}
	}
	w.quiet("boom", "client_secret", appSecret(idp.Google), "nina@", "g-sub-nina", "code-")

	// The applications: a change that fails changes nothing, and reading
	// that fails is an error, never a provider that is off.
	w, f := setup(t)
	for _, op := range []string{"put record", "derive"} {
		f.fail = map[string]error{op: down}
		if _, err := w.svc.SetProviderApp(ctx, "github", contracts.ProviderAppSet{ClientID: "id", ClientSecret: "Secret-9"}); !errors.Is(err, down) || strings.Contains(err.Error(), "Secret-9") {
			t.Fatalf("%s down, setting: %v", op, err)
		}
		if err := w.svc.RemoveProviderApp(ctx, "google"); !errors.Is(err, down) {
			t.Fatalf("%s down, removing: %v", op, err)
		}
	}
	f.fail = map[string]error{}
	if on, err := w.svc.ProvidersOn(ctx); err != nil || len(on.Providers) != 2 {
		t.Fatalf("after changes that failed: %+v %v", on, err)
	}
	for _, op := range []string{"get record", "derive"} {
		restarted := w.service(f)
		restarted.cfg.Records = f
		f.fail = map[string]error{op: down}
		if _, err := restarted.ProvidersOn(ctx); !errors.Is(err, down) {
			t.Fatalf("%s down, which are on: %v", op, err)
		}
		if _, err := restarted.ProviderApps(ctx); !errors.Is(err, down) {
			t.Fatalf("%s down, the list: %v", op, err)
		}
		if _, _, err := restarted.StartProvider(ctx, nil, contracts.ProviderStart{Provider: "google"}); !errors.Is(err, down) {
			t.Fatalf("%s down, starting: %v", op, err)
		}
		if _, err := restarted.SetProviderApp(ctx, "github", contracts.ProviderAppSet{ClientID: "id", ClientSecret: "Secret-9"}); !errors.Is(err, down) {
			t.Fatalf("%s down, setting: %v", op, err)
		}
		// It is read when it can be.
		f.fail = map[string]error{}
		if on, err := restarted.ProvidersOn(ctx); err != nil || len(on.Providers) != 2 {
			t.Fatalf("after %s was down: %+v %v", op, on, err)
		}
	}
	// Read while a trip is away: the trip fails, and is over.
	restarted := w.service(f)
	restarted.cfg.Records = f
	started, cookie, err := restarted.StartProvider(ctx, nil, contracts.ProviderStart{Provider: "google"})
	if err != nil {
		t.Fatal(err)
	}
	restarted.appsRead = false
	f.fail = map[string]error{"get record": down}
	u, _ := url.Parse(started.URL)
	done, err = restarted.FinishProvider(ctx, ProviderAnswer{State: u.Query().Get("state"), Code: w.idp.approve(nina), Browser: cookie})
	if !errors.Is(err, down) || done.Session != "" || reason(done) != "provider_unavailable" {
		t.Fatalf("the applications could not be read: %+v %v", done, err)
	}
}

// shortKeys gives keys that are too short to seal with.
type shortKeys struct{ Keys }

func (shortKeys) Derive(context.Context, string) ([]byte, error) { return make([]byte, 16), nil }

// Failures in the middle of linking: each leaves nobody signed in and
// nothing linked that was not linked before.
func TestProviderFailuresWhileLinking(t *testing.T) {
	down, ctx := errors.New("down"), context.Background()
	setup := func(t *testing.T) (*world, *failing, *contracts.Me, *Authenticated) {
		w := newWorld(t)
		w.on(idp.Google, idp.Meta)
		ana, cookie := w.signUp("ana@example.org")
		who := w.who(cookie)
		f := &failing{Records: w.svc.cfg.Records, Entries: w.mem, Keys: w.svc.cfg.Keys, fail: map[string]error{}, skip: map[string]int{}}
		w.svc.cfg.Records, w.svc.cfg.Entries, w.svc.cfg.Keys = f, f, f
		return w, f, ana, who
	}
	hers := person(idp.Google, "g-sub-ana", "", false)
	pointer := providerPartition(idp.Google, "g-sub-ana")
	unlinked := func(w *world, f *failing, ana *contracts.Me, what string, done *ProviderDone, err error) {
		t.Helper()
		f.fail, f.skip = map[string]error{}, map[string]int{}
		if !errors.Is(err, down) || done.Session != "" || reason(done) != "provider_unavailable" || len(w.providers(ana.Account.ID)) != 0 {
			t.Fatalf("%s: %+v %v, linked %+v", what, done, err, w.providers(ana.Account.ID))
		}
	}

	// The pointer of the provider account cannot be read.
	w, f, ana, who := setup(t)
	state, cookie := w.leave(who, contracts.ProviderStart{Provider: "google"})
	f.fail["get "+pointer] = down
	done, err := w.comeBack(state, cookie, hers)
	unlinked(w, f, ana, "the pointer cannot be read", done, err)

	// A pointer is there, and the key that says whose it is cannot be had:
	// the third time it is asked for, after the session and the record.
	w, f, ana, who = setup(t)
	w.mem.SetEntry(store.Entry{Partition: pointer, Key: loginKey, Value: []byte(`{}`), Version: 1})
	state, cookie = w.leave(who, contracts.ProviderStart{Provider: "google"})
	f.fail["derive"], f.skip["derive"] = down, 2
	done, err = w.comeBack(state, cookie, hers)
	unlinked(w, f, ana, "the key cannot be had", done, err)

	// The pointer leads to an account that cannot be read: an error, never
	// an account that is not there, whose provider account could be taken.
	w, f, ana, who = setup(t)
	eve, _ := w.signUp("eve@example.org")
	if err := w.svc.point(ctx, pointer, loginKey, eve.Account.ID, 0); err != nil {
		t.Fatal(err)
	}
	state, cookie = w.leave(who, contracts.ProviderStart{Provider: "google"})
	f.fail["get record"], f.skip["get record"] = down, 1
	done, err = w.comeBack(state, cookie, hers)
	unlinked(w, f, ana, "the other account cannot be read", done, err)
	// Read, it does not list the provider account, and the pointer is taken.
	state, cookie = w.leave(who, contracts.ProviderStart{Provider: "google"})
	if done, err := w.comeBack(state, cookie, hers); err != nil || len(w.providers(ana.Account.ID)) != 1 {
		t.Fatalf("over a pointer that leads nowhere: %+v %v", done, err)
	}

	// The address the provider vouches for cannot be looked up: from
	// Settings, and signing in.
	for _, from := range []bool{true, false} {
		w, f, ana, who = setup(t)
		session := who
		if !from {
			session = nil
		}
		state, cookie = w.leave(session, contracts.ProviderStart{Provider: "google"})
		f.fail["get "+loginPartition("ana@example.org")] = down
		done, err = w.comeBack(state, cookie, person(idp.Google, "g-sub-ana", "ana@example.org", true))
		unlinked(w, f, ana, "the address cannot be looked up", done, err)
	}

	// The address is confirmed and the provider linked, and the session
	// cannot be made: nobody is signed in.
	w, f, _, _ = setup(t)
	waiting, err := w.through(person(idp.Meta, "880022", "", false), "")
	if err != nil {
		t.Fatal(err)
	}
	started, err := w.svc.StartProviderConfirm(ctx, waiting.Browser, contracts.ProviderConfirmStart{Address: "ana@example.org"})
	if err != nil {
		t.Fatal(err)
	}
	w.svc.Wait()
	sent := w.out.Sent()
	f.fail["list entries"] = down
	if me, session, err := w.svc.FinishProviderConfirm(ctx, waiting.Browser, contracts.ProviderConfirm{Attempt: started.Attempt, Code: code(&sent[len(sent)-1])}, Browser{UserAgent: ""}); !errors.Is(err, down) || me != nil || session != "" {
		t.Fatalf("confirming with no session to be made: %+v %v", me, err)
	}

	// Unlinking with the key gone after the record was read, and setting an
	// application with a key that cannot seal: nothing changes.
	w, f, ana, who = setup(t)
	if _, err := w.linkFrom(who, hers); err != nil {
		t.Fatal(err)
	}
	f.fail["derive"], f.skip["derive"] = down, 1
	err = w.svc.UnlinkProvider(ctx, who, "google")
	f.fail, f.skip = map[string]error{}, map[string]int{}
	if !errors.Is(err, down) || len(w.providers(ana.Account.ID)) != 1 {
		t.Fatalf("unlinking without the key: %v", err)
	}
	if done, err := w.through(hers, ""); err != nil || w.who(done.Session).AccountID != ana.Account.ID {
		t.Fatalf("after an unlinking that failed: %+v %v", done, err)
	}
	w.svc.cfg.Keys = shortKeys{}
	if _, err := w.svc.SetProviderApp(ctx, "github", contracts.ProviderAppSet{ClientID: "id", ClientSecret: "Secret-9"}); err == nil || strings.Contains(err.Error(), "Secret-9") {
		t.Fatalf("sealing with a short key: %v", err)
	}
	w.svc.cfg.Keys = f
	if on, err := w.svc.ProvidersOn(ctx); err != nil || len(on.Providers) != 2 {
		t.Fatalf("after a change that failed: %+v %v", on, err)
	}
}

// What waits in memory is bounded: the oldest goes, and what ended is
// forgotten.
func TestProvidersAreBounded(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	w.on(idp.Meta)
	first, firstCookie := w.leave(nil, contracts.ProviderStart{Provider: "meta"})
	w.now = w.now.Add(time.Second)
	second, secondCookie := w.leave(nil, contracts.ProviderStart{Provider: "meta"})
	for i := range maxTrips - 2 {
		w.svc.trips[fmt.Sprint("filler ", i)] = &trip{expires: w.now.Add(time.Hour)}
	}
	// One more than is kept: the one that ends first goes, and the new one
	// is kept. Refusing new ones would let anyone keep everybody out.
	third, thirdCookie := w.leave(nil, contracts.ProviderStart{Provider: "meta"})
	if len(w.svc.trips) != maxTrips {
		t.Fatalf("%d trips kept", len(w.svc.trips))
	}
	who := person(idp.Meta, "1", "", false)
	done, err := w.comeBack(first, firstCookie, who)
	w.refusedWith("the trip that was dropped", done, err, contracts.ErrProviderFailed, "/sign-in#provider_error=provider_failed")
	var browsers []string
	for _, trip := range [][2]string{{second, secondCookie}, {third, thirdCookie}} {
		done, err := w.comeBack(trip[0], trip[1], who)
		if err != nil || done.Browser == "" {
			t.Fatalf("a trip that was kept: %+v %v", done, err)
		}
		browsers = append(browsers, done.Browser)
		w.now = w.now.Add(time.Second)
	}
	for i := range maxUnconfirmed - 2 {
		w.svc.unconfirmed[fmt.Sprint("filler ", i)] = &unconfirmed{expires: w.now.Add(time.Hour)}
	}
	state, cookie := w.leave(nil, contracts.ProviderStart{Provider: "meta"})
	if done, err = w.comeBack(state, cookie, who); err != nil || len(w.svc.unconfirmed) != maxUnconfirmed {
		t.Fatalf("one more waiting: %+v %v, %d kept", done, err, len(w.svc.unconfirmed))
	}
	if _, err := w.svc.PendingProvider(ctx, browsers[0]); !errors.Is(err, contracts.ErrProviderFailed) {
		t.Fatalf("the one that was dropped: %v", err)
	}
	for _, kept := range []string{browsers[1], done.Browser} {
		if _, err := w.svc.PendingProvider(ctx, kept); err != nil {
			t.Fatalf("one that was kept: %v", err)
		}
	}
	// What ended is forgotten.
	w.now = w.now.Add(2 * time.Hour)
	w.svc.mu.Lock()
	w.svc.sweep(w.now)
	w.svc.mu.Unlock()
	if len(w.svc.trips) != 0 || len(w.svc.unconfirmed) != 0 {
		t.Fatalf("kept after they ended: %d trips, %d waiting", len(w.svc.trips), len(w.svc.unconfirmed))
	}
}

// An account that is removed takes the pointers of its provider accounts
// with it, and they sign nobody in.
func TestRemovingAnAccountWithProviders(t *testing.T) {
	c := newClosing(t)
	ctx := context.Background()
	c.now = c.day.Add(-48 * time.Hour)
	if _, err := c.svc.CancelDeletion(ctx, c.who); err != nil {
		t.Fatal(err)
	}
	c.on(idp.Google, idp.GitHub)
	hers := person(idp.Google, "g-sub-closing-ana", "", false)
	if _, err := c.linkFrom(c.who, hers); err != nil {
		t.Fatal(err)
	}
	// One whose pointer now leads elsewhere, by hand: it is left alone.
	if _, err := c.linkFrom(c.who, person(idp.GitHub, "5150", "", false)); err != nil {
		t.Fatal(err)
	}
	moved, _ := c.mem.GetEntry(ctx, loginPartition("eve@example.org"), loginKey)
	c.mem.SetEntry(store.Entry{Partition: providerPartition(idp.GitHub, "5150"), Key: loginKey, Value: moved.Value, Version: 9})
	got, err := c.svc.StartDeletion(ctx, c.who)
	if err != nil {
		t.Fatal(err)
	}
	c.now = got.ClosesAt
	if _, err := c.svc.Authenticate(ctx, c.eveCookie); err != nil {
		t.Fatal(err)
	}
	if err := c.svc.RemoveDue(ctx); err != nil {
		t.Fatal(err)
	}
	if left := c.stored("g-sub-closing-ana", c.ana.Account.ID); len(left) != 0 {
		t.Fatalf("left in the storage: %q", left)
	}
	if e, err := c.mem.GetEntry(ctx, providerPartition(idp.GitHub, "5150"), loginKey); err != nil || e.Version != 9 {
		t.Fatalf("a pointer that led elsewhere: %+v %v", e, err)
	}
	done, err := c.through(hers, "")
	if err != nil || done.Session != "" || done.Path != "/sign-in#provider_confirm" {
		t.Fatalf("a provider account of a removed account: %+v %v", done, err)
	}
	c.others()

	// A pointer that cannot be read stops the removal, which starts again.
	c2 := newClosing(t)
	c2.now = c2.day.Add(-48 * time.Hour)
	if _, err := c2.svc.CancelDeletion(ctx, c2.who); err != nil {
		t.Fatal(err)
	}
	c2.on(idp.Google)
	if _, err := c2.linkFrom(c2.who, hers); err != nil {
		t.Fatal(err)
	}
	got, err = c2.svc.StartDeletion(ctx, c2.who)
	if err != nil {
		t.Fatal(err)
	}
	c2.now = got.ClosesAt
	down := errors.New("down")
	f := &failing{Records: c2.svc.cfg.Records, Entries: c2.mem, Keys: c2.svc.cfg.Keys, fail: map[string]error{"get " + providerPartition(idp.Google, "g-sub-closing-ana"): down}}
	c2.svc.cfg.Entries = f
	if err := c2.svc.RemoveDue(ctx); !errors.Is(err, down) {
		t.Fatalf("removing with a pointer that cannot be read: %v", err)
	}
	f.fail = map[string]error{}
	if err := c2.svc.RemoveDue(ctx); err != nil {
		t.Fatal(err)
	}
	if left := c2.stored("g-sub-closing-ana", c2.ana.Account.ID); len(left) != 0 {
		t.Fatalf("left in the storage: %q", left)
	}
}
