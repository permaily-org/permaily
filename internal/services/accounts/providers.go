package accounts

import (
	"cmp"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/domain"
	"github.com/permaily-org/permaily/internal/envelope"
	"github.com/permaily-org/permaily/internal/idp"
	"github.com/permaily-org/permaily/internal/store"
)

// Signing in with Google, GitHub, Meta and Microsoft (ADR 0017).
//
// A provider says who the person is there (a subject) and, some of them, that
// an address is theirs. A subject leads to an account through a pointer that
// proves nothing: the account's record, signed and anchored, lists the
// provider and the subject. The trip to the provider lives in memory and ends
// only in the browser that started it, which holds a cookie for that.

// Providers is what the service needs of the providers' protocol
// (idp.Client): where to send the browser, and who came back.
type Providers interface {
	Begin(p idp.Provider, app idp.App, state string) (*idp.Started, error)
	Finish(ctx context.Context, p idp.Provider, app idp.App, code string, s idp.Started) (*idp.Identity, error)
}

// How many trips to a provider, and sign-ins that wait for an address, one
// enclave keeps at a time.
const (
	maxTrips       = 10000
	maxUnconfirmed = 10000
)

// Where the callback sends the browser: screens of the app, and what the app
// reads in the fragment. A fragment never reaches a server or another site.
const (
	homeScreen      = "/"
	signInScreen    = "/sign-in"
	settingsScreen  = "/settings"
	confirmFragment = "#provider_confirm"
	errorFragment   = "#provider_error="
)

// waiting is something kept in memory until it ends.
type waiting interface{ ends() time.Time }

// keep puts v in m. When m is full the one that ends first goes: anyone can
// start a sign-in, so refusing new ones would let anyone keep everybody out.
func keep[T waiting](m map[string]T, key string, v T, limit int) {
	if len(m) >= limit {
		oldest := ""
		for other, w := range m {
			if oldest == "" || w.ends().Before(m[oldest].ends()) {
				oldest = other
			}
		}
		delete(m, oldest)
	}
	m[key] = v
}

// ended forgets what ended.
func ended[T waiting](m map[string]T, now time.Time) {
	for key, w := range m {
		if !w.ends().After(now) {
			delete(m, key)
		}
	}
}

// trip is a sign-in with a provider between sending the browser there and
// its coming back.
type trip struct {
	provider idp.Provider
	started  idp.Started
	// browser is the hash of the value of the cookie the browser was given:
	// the provider's answer ends the trip only where that cookie is.
	browser [sha256.Size]byte
	// invitation is the entry of the invitation a sign-up will use, and
	// agreed the agreement to the terms given with it.
	invitation string
	agreed     termsDoc
	// account and session are whose Settings started it, to link a provider
	// to that account and to no other; empty for a sign-in.
	account, session string
	// returnTo is the path of the app the person was going to, or "".
	returnTo string
	expires  time.Time
}

func (t *trip) ends() time.Time { return t.expires }

// unconfirmed is a sign-in with a provider that said who the person is there
// and not that an address is theirs: it waits for the code of a message.
type unconfirmed struct {
	provider idp.Provider
	subject  string
	// address is what the provider gave, to offer in the form. It is never
	// used for anything else.
	address    string
	invitation string
	agreed     termsDoc
	returnTo   string
	expires    time.Time
}

func (u *unconfirmed) ends() time.Time { return u.expires }

// browserKey is what is kept of a cookie's value.
func browserKey(cookie string) string {
	sum := sha256.Sum256([]byte(cookie))
	return hex.EncodeToString(sum[:])
}

// ---- what an account's record says ----

// providerDoc is a provider account in its account's record: signed with the
// rest of it. Nothing the provider said is kept but which account it is
// there (ADR 0017): no address, no name.
type providerDoc struct {
	Provider idp.Provider `json:"provider"`
	Subject  string       `json:"subject"`
	LinkedAt time.Time    `json:"linked_at"`
}

// provider returns where in the record the account of a provider is, or -1.
// An account has one of each at most.
func (d *accountDoc) provider(p idp.Provider) int {
	return slices.IndexFunc(d.Providers, func(l providerDoc) bool { return l.Provider == p })
}

// signsInWith reports whether the record lists that provider account.
func (d *accountDoc) signsInWith(p idp.Provider, subject string) bool {
	i := d.provider(p)
	return i >= 0 && d.Providers[i].Subject == subject
}

// waysIn counts the ways to sign in to the account: mail unless it signs in
// with passkeys only, each passkey and each provider account.
func (d *accountDoc) waysIn() int {
	n := len(d.Passkeys) + len(d.Providers)
	if !d.PasskeyOnly {
		n++
	}
	return n
}

func providerPartition(p idp.Provider, subject string) string {
	return "login#" + string(p) + "#" + subject
}

// linked returns the account a provider account signs in to, nil when it has
// none: the pointer must lead to an account whose record lists it.
func (s *Service) linked(ctx context.Context, p idp.Provider, subject string) (*accountDoc, error) {
	doc, _, err := s.follow(ctx, providerPartition(p, subject), func(doc *accountDoc) bool { return doc.signsInWith(p, subject) })
	return doc, err
}

// link makes a provider account sign in to an account: the pointer first,
// then the record. A pointer whose record was not written leads nowhere and
// is replaced by the next try. Linking what is linked already only writes the
// pointer again, should it be missing. The person is told by mail of a way
// to sign in that is new, unless the account was made by this very sign-in
// (made).
func (s *Service) link(ctx context.Context, accountID string, p idp.Provider, subject string, made bool) (*accountDoc, error) {
	var kept accountDoc
	// A sign-in links, or the end of a link that Settings started, which was
	// asked of the terms at its start: neither asks again.
	doc, err := s.changeAgreed(ctx, accountID, false, func(doc *accountDoc) error {
		i := doc.provider(p)
		if i >= 0 && doc.Providers[i].Subject != subject {
			return contracts.ErrProviderLinked
		}
		if err := s.pointProvider(ctx, doc, p, subject); err != nil {
			return err
		}
		if i >= 0 {
			kept = *doc
			return errUnchanged
		}
		doc.Providers = append(doc.Providers, providerDoc{Provider: p, Subject: subject, LinkedAt: s.now()})
		return nil
	})
	if errors.Is(err, errUnchanged) {
		return &kept, nil
	}
	if err != nil {
		return nil, err
	}
	s.cfg.Log.Info("accounts: provider linked", "provider", string(p))
	if !made {
		s.notify(ctx, noticeProviderLinked, providerMail(doc.Address, s.rp.ID, s.now(), p, true))
	}
	return doc, nil
}

// pointProvider writes the pointer from a provider account to doc's account,
// with changing held, so no other link of this enclave comes in between. It
// refuses when the pointer leads to another account that lists the provider
// account: one provider account signs in to one account.
func (s *Service) pointProvider(ctx context.Context, doc *accountDoc, p idp.Provider, subject string) error {
	partition, version := providerPartition(p, subject), uint64(0)
	e, err := s.cfg.Entries.GetEntry(ctx, partition, loginKey)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	if err == nil {
		key, err := s.signingKey(ctx)
		if err != nil {
			return err
		}
		id := pointed(key, e)
		if id == doc.ID {
			return nil
		}
		if id != "" {
			// read and not account: removing an account that is due takes
			// changing, which is held here.
			other, err := s.read(ctx, id)
			if err != nil {
				return err
			}
			if other != nil && !other.due(s.now()) && other.signsInWith(p, subject) {
				return contracts.ErrProviderInUse
			}
		}
		version = e.Version
	}
	return s.point(ctx, partition, loginKey, doc.ID, version)
}

// ---- the trip ----

// StartProvider starts a trip to a provider and returns where to send the
// browser and the value of the cookie that ties the trip to it. With who it
// links a provider account to that session's account; without, it signs in.
//
// It asks for no address and looks no account up but the session's own, so
// it tells nobody who has an account.
func (s *Service) StartProvider(ctx context.Context, who *Authenticated, in contracts.ProviderStart) (*contracts.ProviderStarted, string, error) {
	// Linking from Settings adds a way in: it needs a fresh proof.
	if who != nil {
		if err := s.confirmed(who); err != nil {
			return nil, "", err
		}
	}
	p := idp.Provider(in.Provider)
	app, on, err := s.app(ctx, p)
	if err != nil {
		return nil, "", err
	}
	if !on {
		return nil, "", contracts.ErrProviderOff
	}
	now := s.now()
	t := &trip{provider: p, returnTo: domain.AppPath(in.ReturnTo), expires: now.Add(domain.ProviderLifetime)}
	switch {
	case who != nil:
		doc, err := s.own(ctx, who)
		if err != nil {
			return nil, "", err
		}
		// Told now, and not after the person went to the provider and back.
		if doc.State == contracts.AccountClosing {
			return nil, "", contracts.ErrAccountClosing
		}
		if !doc.agreed(s.terms()) {
			return nil, "", contracts.ErrTermsChanged
		}
		if doc.provider(p) >= 0 {
			return nil, "", contracts.ErrProviderLinked
		}
		t.account, t.session = doc.ID, who.Session.ID
	case in.Invitation != "":
		// An invitation is checked at once: it says nothing of any address.
		if t.invitation, err = s.invitation(ctx, in.Invitation); err != nil {
			return nil, "", err
		}
		if t.invitation == "" {
			return nil, "", contracts.ErrInvalidInvitation
		}
		if t.agreed, err = s.agreement(t.invitation, in.Terms); err != nil {
			return nil, "", err
		}
	}
	state, browser := newSecret(), newSecret()
	started, err := s.cfg.Providers.Begin(p, app, state)
	if err != nil {
		// Not its error: it comes from where the application's secret is used.
		return nil, "", errors.New("accounts: a trip to a provider could not be started")
	}
	t.started, t.browser = *started, sha256.Sum256([]byte(browser))
	s.mu.Lock()
	s.sweep(now)
	keep(s.trips, state, t, maxTrips)
	s.mu.Unlock()
	return &contracts.ProviderStarted{URL: started.URL, ExpiresIn: int(domain.ProviderLifetime / time.Second)}, browser, nil
}

// ProviderAnswer is what a browser brings back from a provider.
type ProviderAnswer struct {
	State, Code string
	// Refused says the provider sent an error and no code: the person said
	// no, or the provider did. Its words are not read.
	Refused bool
	// Browser is the value of the cookie the start gave this browser.
	Browser string
	// From is what the browser says of itself, and its device cookie.
	From Browser
}

// ProviderDone is how a trip to a provider ended.
type ProviderDone struct {
	// Path is where in the app the browser goes: where the person was going,
	// the screen that asks for an address to confirm, or the screen that
	// says what went wrong. It is always a path of the app.
	Path string
	// Session is the value of the session's cookie, when the device was
	// signed in.
	Session string
	// Browser is the value of the cookie of a sign-in that waits for an
	// address to be confirmed; without it the trip's cookie is dropped.
	Browser string
	// Provider is the provider of a trip that failed, for the screen to name
	// it. It is the one this browser asked for when it started the trip, and
	// empty when the service knows no such trip any more.
	Provider idp.Provider
}

// providerReasons are the reasons a trip to a provider ends with, in the
// fragment of the address the browser is sent to. None of them tells whether
// an address or a provider account has an account here to someone the
// provider did not vouch for as its owner.
var providerReasons = []*contracts.Error{
	contracts.ErrProviderFailed, contracts.ErrProviderUnavailable, contracts.ErrInvitationNeeded,
	contracts.ErrInvalidInvitation, contracts.ErrProviderInUse, contracts.ErrProviderLinked, contracts.ErrAccountClosing,
	// Said only to whoever the provider vouched for as the account's person.
	contracts.ErrAccountBlocked,
}

// providerReason is the reason code of a failure: one of the fixed set,
// whatever the failure was.
func providerReason(err error) string {
	var known *contracts.Error
	if !errors.As(err, &known) {
		return contracts.ErrProviderUnavailable.Code // the service's own failure
	}
	if slices.ContainsFunc(providerReasons, func(r *contracts.Error) bool { return r.Code == known.Code }) {
		return known.Code
	}
	return contracts.ErrProviderFailed.Code
}

// FinishProvider ends a trip to a provider with what the browser brought
// back. It always says where the browser goes next. With an error that is
// the screen that shows the reason, and nobody was signed in and nothing was
// linked; an error that is not a contracts.Error is the service's own
// failure, for the caller to log.
func (s *Service) FinishProvider(ctx context.Context, in ProviderAnswer) (*ProviderDone, error) {
	t := s.arrive(in.State, in.Browser)
	done, err := s.arrived(ctx, t, in)
	if err == nil {
		return done, nil
	}
	screen := signInScreen
	if t != nil && t.account != "" {
		screen = settingsScreen
	}
	done = &ProviderDone{Path: screen + errorFragment + providerReason(err)}
	if t != nil {
		done.Provider = t.provider
	}
	return done, err
}

// arrive returns the trip a state belongs to, when it is still running and
// was started by this browser. The trip is over either way: an answer is
// tried once.
func (s *Service) arrive(state, browser string) *trip {
	now, sum := s.now(), sha256.Sum256([]byte(browser))
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.trips[state]
	delete(s.trips, state)
	// An answer of the provider carried to another person's browser ends
	// here: that browser has no cookie of this trip.
	if t == nil || !t.expires.After(now) || subtle.ConstantTimeCompare(sum[:], t.browser[:]) != 1 {
		return nil
	}
	return t
}

func (s *Service) arrived(ctx context.Context, t *trip, in ProviderAnswer) (*ProviderDone, error) {
	if t == nil || in.Refused || in.Code == "" {
		return nil, contracts.ErrProviderFailed
	}
	app, on, err := s.app(ctx, t.provider)
	if err != nil {
		return nil, err
	}
	if !on {
		return nil, contracts.ErrProviderFailed // turned off while the person was away
	}
	who, err := s.cfg.Providers.Finish(ctx, t.provider, app, in.Code, t.started)
	if err != nil {
		return nil, s.providerError(t.provider, err)
	}
	if who == nil || who.Provider != t.provider || !domain.ProviderSubject(who.Subject) {
		s.cfg.Log.Warn("accounts: a provider named an account in a way that cannot be kept", "provider", string(t.provider))
		return nil, contracts.ErrProviderFailed
	}
	if t.account != "" {
		return s.linkSignedIn(ctx, t, who)
	}
	doc, err := s.linked(ctx, t.provider, who.Subject)
	made := false
	switch {
	case err != nil:
		return nil, err
	case doc != nil && doc.PasskeyOnly:
		// The account turned providers off, as it did mail. Nothing says so.
		return nil, contracts.ErrProviderFailed
	case doc == nil:
		address, ok := vouched(who)
		if !ok {
			return s.confirmLater(t, who), nil
		}
		if doc, made, err = s.firstTime(ctx, t, who.Subject, address); err != nil {
			return nil, err
		}
	}
	// The provider said who it is: an account the operator blocked is told
	// so here (enter), and never before.
	_, cookie, err := s.enter(ctx, doc, wayProvider(t.provider), in.From, made)
	if err != nil {
		return nil, err
	}
	return &ProviderDone{Path: cmp.Or(t.returnTo, homeScreen), Session: cookie}, nil
}

// providerError turns a failure of the providers' protocol into the reason a
// trip ends with, and logs why. The errors of idp are words that hold nothing
// of what a provider answered; anything else is not logged.
func (s *Service) providerError(p idp.Provider, err error) error {
	switch {
	case errors.Is(err, idp.ErrUnreachable):
		s.cfg.Log.Warn("accounts: a provider could not be asked who signed in", "provider", string(p), "why", err.Error())
		return contracts.ErrProviderUnavailable
	case errors.Is(err, idp.ErrInvalid):
		s.cfg.Log.Info("accounts: a provider's answer was refused", "provider", string(p), "why", err.Error())
		return contracts.ErrProviderFailed
	}
	return errors.New("accounts: a provider could not be asked who signed in")
}

// vouched returns the address a provider vouches for as the person's, in the
// form accounts are found by. An address it does not vouch for, or one that
// cannot be an account's, is none.
func vouched(who *idp.Identity) (string, bool) {
	address, err := domain.NormalizeAddress(who.Address)
	return address, who.Verified && err == nil
}

// firstTime is a provider account that signs in to no account yet, with an
// address the provider vouches for: it is linked to the account that has the
// address, or to a new one made with the invitation given at the start.
//
// It reports whether the account was made here.
func (s *Service) firstTime(ctx context.Context, t *trip, subject, address string) (*accountDoc, bool, error) {
	doc, stale, err := s.lookup(ctx, address)
	made := false
	switch {
	case err != nil:
		return nil, false, err
	case doc == nil && t.invitation == "":
		return nil, false, contracts.ErrInvitationNeeded
	case doc == nil:
		if doc, made, err = s.signUp(ctx, address, t.invitation, t.agreed, stale); err != nil {
			return nil, false, err
		}
	case doc.PasskeyOnly:
		return nil, false, contracts.ErrProviderFailed
	case doc.blocked():
		// The provider vouched for the address, which is the account's:
		// nothing is linked to an account that lets nobody in.
		return nil, false, contracts.ErrAccountBlocked
	}
	// An account that was just made and is not linked here, because the
	// storage failed, is the address's all the same: the next sign-in links it.
	doc, err = s.link(ctx, doc.ID, t.provider, subject, made)
	return doc, made, err
}

// linkSignedIn ends a trip that Settings started: the provider account is
// linked to the account of the session that started it, and to no other. An
// address the provider vouches for that is another account's refuses it: the
// two would say different things of whose provider account it is.
func (s *Service) linkSignedIn(ctx context.Context, t *trip, who *idp.Identity) (*ProviderDone, error) {
	open, err := s.sessionOpen(ctx, t.account, t.session)
	if err != nil {
		return nil, err
	}
	if !open {
		return nil, contracts.ErrProviderFailed // signed out while the person was away
	}
	if address, ok := vouched(who); ok {
		other, _, err := s.lookup(ctx, address)
		if err != nil {
			return nil, err
		}
		if other != nil && other.ID != t.account {
			return nil, contracts.ErrProviderInUse
		}
	}
	if _, err := s.link(ctx, t.account, t.provider, who.Subject, false); err != nil {
		return nil, err
	}
	return &ProviderDone{Path: cmp.Or(t.returnTo, settingsScreen)}, nil
}

// sessionOpen reports whether a session of an account is still running.
func (s *Service) sessionOpen(ctx context.Context, accountID, id string) (bool, error) {
	e, err := s.cfg.Entries.GetEntry(ctx, accountPartition(accountID), sessionPrefix+id)
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	key, err := s.signingKey(ctx)
	if err != nil {
		return false, err
	}
	return s.readSession(key, e, accountID) != nil, nil
}

// ---- confirming an address ----

// confirmLater keeps a sign-in whose provider did not vouch for an address,
// for the browser that made the trip. Nothing is looked up, linked or made:
// the answer is the same whoever's address the provider gave.
func (s *Service) confirmLater(t *trip, who *idp.Identity) *ProviderDone {
	now, browser := s.now(), newSecret()
	// Offered in the form when it can be an address at all, and never trusted.
	address, _ := domain.NormalizeAddress(who.Address)
	s.mu.Lock()
	s.sweep(now)
	keep(s.unconfirmed, browserKey(browser), &unconfirmed{
		provider: t.provider, subject: who.Subject, address: address, invitation: t.invitation, agreed: t.agreed,
		returnTo: t.returnTo, expires: now.Add(domain.ProviderConfirmLifetime),
	}, maxUnconfirmed)
	s.mu.Unlock()
	return &ProviderDone{Path: signInScreen + confirmFragment, Browser: browser}
}

// waitingIn returns the sign-in that waits in a browser for its address.
func (s *Service) waitingIn(browser string) (*unconfirmed, error) {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	u := s.unconfirmed[browserKey(browser)]
	if u == nil || !u.expires.After(now) {
		return nil, contracts.ErrProviderFailed
	}
	found := *u
	return &found, nil
}

// PendingProvider says what waits in a browser: which provider, and the
// address to offer in the form.
func (s *Service) PendingProvider(_ context.Context, browser string) (*contracts.ProviderPending, error) {
	u, err := s.waitingIn(browser)
	if err != nil {
		return nil, err
	}
	return &contracts.ProviderPending{
		Provider: string(u.provider), Address: u.address, ReturnTo: cmp.Or(u.returnTo, homeScreen),
		ExpiresIn: int(u.expires.Sub(s.now()) / time.Second),
	}, nil
}

// StartProviderConfirm sends the message that confirms an address for the
// sign-in that waits in a browser. It is a sign-in by email, with its limits
// and its one answer for every address: a message goes out when the address
// has an account, or when the invitation given at the start lets it make one.
func (s *Service) StartProviderConfirm(ctx context.Context, browser string, in contracts.ProviderConfirmStart) (*contracts.EmailStarted, error) {
	u, err := s.waitingIn(browser)
	if err != nil {
		return nil, err
	}
	address, err := domain.NormalizeAddress(in.Address)
	if err != nil {
		return nil, err
	}
	return s.startEmail(ctx, address, u.invitation, u.agreed)
}

// FinishProviderConfirm ends the sign-in that waits in a browser with the
// code of the message: the address is the person's, so the provider account
// is linked to its account, or to a new one, and the device is signed in. A
// wrong code leaves it waiting, with the tries a sign-in by email takes.
func (s *Service) FinishProviderConfirm(ctx context.Context, browser string, in contracts.ProviderConfirm, from Browser) (*contracts.Me, string, error) {
	u, err := s.waitingIn(browser)
	if err != nil {
		return nil, "", err
	}
	// The code, typed where the trip was made. The link of the same message
	// signs in by mail and links nothing.
	// An account the operator blocked is refused there, with the code that
	// proved the address: nothing is linked to it.
	doc, made, err := s.proved(ctx, contracts.EmailFinish{Attempt: in.Attempt, Code: in.Code})
	if err != nil {
		return nil, "", err
	}
	// It waited for this, and is over whatever comes of linking.
	s.mu.Lock()
	delete(s.unconfirmed, browserKey(browser))
	s.mu.Unlock()
	if doc, err = s.link(ctx, doc.ID, u.provider, u.subject, made); err != nil {
		return nil, "", err
	}
	return s.enter(ctx, doc, wayProvider(u.provider), from, made)
}

// ---- settings ----

// LinkedProviders lists the provider accounts that sign in to the account
// behind a session, the oldest first.
func (s *Service) LinkedProviders(ctx context.Context, who *Authenticated) (*contracts.LinkedProviders, error) {
	doc, err := s.own(ctx, who)
	if err != nil {
		return nil, err
	}
	out := &contracts.LinkedProviders{Providers: []contracts.LinkedProvider{}}
	for _, l := range doc.Providers {
		out.Providers = append(out.Providers, contracts.LinkedProvider{Provider: string(l.Provider), LinkedAt: l.LinkedAt})
	}
	return out, nil
}

// UnlinkProvider takes a provider account out of the account: it signs
// nobody in from then on. It is refused for the only way left to sign in.
func (s *Service) UnlinkProvider(ctx context.Context, who *Authenticated, provider string) error {
	if err := s.confirmed(who); err != nil {
		return err
	}
	p := idp.Provider(provider)
	doc, err := s.change(ctx, who.AccountID, func(doc *accountDoc) error {
		i := doc.provider(p)
		if i < 0 {
			return contracts.ErrNotFound
		}
		if doc.waysIn() <= 1 {
			return contracts.ErrLastWayIn
		}
		// The pointer goes while changing is held, so it is not one that a
		// link made a moment later. If the record is then not written, the
		// provider account is still listed and its next sign-in, with an
		// address that is the account's, writes the pointer again.
		key, err := s.signingKey(ctx)
		if err != nil {
			return err
		}
		if err := s.unpoint(ctx, key, providerPartition(p, doc.Providers[i].Subject), doc.ID); err != nil {
			return err
		}
		doc.Providers = slices.Delete(doc.Providers, i, i+1)
		return nil
	})
	if err != nil {
		return err
	}
	s.cfg.Log.Info("accounts: provider unlinked", "provider", provider)
	s.notify(ctx, noticeProviderUnlinked, providerMail(doc.Address, s.rp.ID, s.now(), p, false))
	return nil
}

// ---- the applications' secrets ----

// The operator registers an application at each provider and gives its
// client id and secret to the enclave. They are kept in one record, sealed
// with a key that comes from the root secret, and read through the store
// that refuses an earlier copy: a secret that was replaced or removed does
// not come back. A provider without an application is off.
const (
	providerAppsRecord = "provider-apps"
	// sealingPurpose names the key that seals the applications' secrets.
	sealingPurpose = "permaily provider secrets v1"
)

type appDoc struct {
	ClientID     string    `json:"client_id"`
	ClientSecret string    `json:"client_secret"`
	SetAt        time.Time `json:"set_at"`
}

type appsDoc struct {
	V    int                     `json:"v"`
	Apps map[idp.Provider]appDoc `json:"apps"`
}

func (s *Service) sealingKey(ctx context.Context) ([]byte, error) {
	key, err := s.cfg.Keys.Derive(ctx, sealingPurpose)
	if err != nil {
		return nil, fmt.Errorf("accounts: sealing key: %w", err)
	}
	return key, nil
}

// loadApps returns the applications, reading their record the first time,
// with appsMu held.
func (s *Service) loadApps(ctx context.Context) (map[idp.Provider]appDoc, error) {
	if s.appsRead {
		return s.apps, nil
	}
	var doc appsDoc
	rec, err := s.cfg.Records.GetRecord(ctx, providerAppsRecord)
	switch {
	case errors.Is(err, store.ErrLost):
		// What was committed is gone, and an earlier copy is never taken for
		// it: every provider is off until the operator sets them again.
		s.cfg.Log.Error("accounts: the record of the providers' applications is lost; every provider is off until they are set again")
	case errors.Is(err, store.ErrNotFound):
		// None was ever set.
	case err != nil:
		return nil, err
	default:
		key, err := s.sealingKey(ctx)
		if err != nil {
			return nil, err
		}
		plain, err := envelope.Open(key, rec.Value, recordAAD(providerAppsRecord))
		// Not the error of the JSON: it could repeat what was sealed.
		if err != nil || json.Unmarshal(plain, &doc) != nil {
			return nil, errors.New("accounts: the record of the providers' applications cannot be opened")
		}
	}
	s.apps, s.appsRead = doc.Apps, true
	return s.apps, nil
}

// changeApps lets edit change the applications and stores them, sealed, as
// the newest version of their record. edit reports whether it changed any.
func (s *Service) changeApps(ctx context.Context, edit func(map[idp.Provider]appDoc) bool) error {
	s.appsMu.Lock()
	defer s.appsMu.Unlock()
	apps, err := s.loadApps(ctx)
	if err != nil {
		return err
	}
	next := map[idp.Provider]appDoc{}
	maps.Copy(next, apps)
	if !edit(next) {
		return nil
	}
	key, err := s.sealingKey(ctx)
	if err != nil {
		return err
	}
	raw, _ := json.Marshal(appsDoc{V: 1, Apps: next})
	sealed, err := envelope.Seal(key, raw, recordAAD(providerAppsRecord))
	if err != nil {
		return err
	}
	if err := s.cfg.Records.PutRecord(ctx, &store.Record{Name: providerAppsRecord, Value: sealed, UpdatedAt: s.now()}, false); err != nil {
		return err
	}
	s.apps, s.appsRead = next, true
	return nil
}

// app returns the application of a provider, and whether the provider is on.
func (s *Service) app(ctx context.Context, p idp.Provider) (idp.App, bool, error) {
	if s.cfg.Providers == nil {
		return idp.App{}, false, nil
	}
	s.appsMu.Lock()
	defer s.appsMu.Unlock()
	apps, err := s.loadApps(ctx)
	a, on := apps[p]
	return idp.App{ClientID: a.ClientID, ClientSecret: a.ClientSecret}, on, err
}

// ProvidersOn says which providers a person can sign in with.
func (s *Service) ProvidersOn(ctx context.Context) (*contracts.ProvidersOn, error) {
	out := &contracts.ProvidersOn{Providers: []string{}}
	for _, p := range idp.Providers {
		_, on, err := s.app(ctx, p)
		if err != nil {
			return nil, err
		}
		if on {
			out.Providers = append(out.Providers, string(p))
		}
	}
	return out, nil
}

// ProviderApps lists the providers that have an application set, for the
// operator: the client id, never the secret.
func (s *Service) ProviderApps(ctx context.Context) (*contracts.ProviderApps, error) {
	s.appsMu.Lock()
	defer s.appsMu.Unlock()
	apps, err := s.loadApps(ctx)
	if err != nil {
		return nil, err
	}
	out := &contracts.ProviderApps{Providers: []contracts.ProviderApp{}}
	for _, p := range idp.Providers {
		if a, set := apps[p]; set {
			out.Providers = append(out.Providers, contracts.ProviderApp{Provider: string(p), ClientID: a.ClientID, SetAt: a.SetAt})
		}
	}
	return out, nil
}

// SetProviderApp gives the enclave the application of a provider, in place
// of the one it had. The provider is on from then.
func (s *Service) SetProviderApp(ctx context.Context, provider string, in contracts.ProviderAppSet) (*contracts.ProviderApp, error) {
	return s.setProviderApp(ctx, nil, provider, in)
}

func (s *Service) setProviderApp(ctx context.Context, by *acting, provider string, in contracts.ProviderAppSet) (*contracts.ProviderApp, error) {
	if err := by.superOnly(); err != nil {
		return nil, err
	}
	p := idp.Provider(provider)
	if !slices.Contains(idp.Providers, p) {
		return nil, contracts.ErrNotFound
	}
	if !domain.ProviderCredential(in.ClientID) || !domain.ProviderCredential(in.ClientSecret) {
		return nil, contracts.ErrInvalidProviderApp
	}
	set := appDoc{ClientID: in.ClientID, ClientSecret: in.ClientSecret, SetAt: s.now().Truncate(time.Second)}
	err := s.changeApps(ctx, func(apps map[idp.Provider]appDoc) bool {
		apps[p] = set
		return true
	})
	if err != nil {
		return nil, err
	}
	s.cfg.Log.Info("accounts: a provider's application was set", "provider", provider)
	s.acted(ActionProviderAppSet, 1)
	s.logged(ctx, by, logDoc{Kind: ActionProviderAppSet, Provider: provider})
	return &contracts.ProviderApp{Provider: provider, ClientID: set.ClientID, SetAt: set.SetAt}, nil
}

// RemoveProviderApp takes the application of a provider away: the provider
// is off, for signing in too. The provider accounts that are linked stay
// linked, for when it is on again.
func (s *Service) RemoveProviderApp(ctx context.Context, provider string) error {
	return s.removeProviderApp(ctx, nil, provider)
}

func (s *Service) removeProviderApp(ctx context.Context, by *acting, provider string) error {
	if err := by.superOnly(); err != nil {
		return err
	}
	p := idp.Provider(provider)
	if !slices.Contains(idp.Providers, p) {
		return contracts.ErrNotFound
	}
	removed := false
	err := s.changeApps(ctx, func(apps map[idp.Provider]appDoc) bool {
		_, removed = apps[p]
		delete(apps, p)
		return removed
	})
	if err == nil && removed {
		s.cfg.Log.Info("accounts: a provider's application was removed", "provider", provider)
		s.acted(ActionProviderAppRemove, 1)
		s.logged(ctx, by, logDoc{Kind: ActionProviderAppRemove, Provider: provider})
	}
	return err
}
