package accounts

import (
	"cmp"
	"context"
	"crypto/sha256"
	"crypto/subtle"
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
// provider and the subject. The trip to the provider is kept sealed in the
// storage (pending.go), ends through any enclave, and ends only in the browser
// that started it, which holds a cookie for that.

// Providers is what the service needs of the providers' protocol
// (idp.Client): where to send the browser, and who came back.
type Providers interface {
	Begin(p idp.Provider, app idp.App, state string) (*idp.Started, error)
	Finish(ctx context.Context, p idp.Provider, app idp.App, code string, s idp.Started) (*idp.Identity, error)
}

// Where the callback sends the browser: screens of the app, and what the app
// reads in the fragment. A fragment never reaches a server or another site.
const (
	homeScreen      = "/"
	signInScreen    = "/sign-in"
	settingsScreen  = "/settings"
	confirmFragment = "#provider_confirm"
	errorFragment   = "#provider_error="
)

// tripDoc is a sign-in with a provider between sending the browser there and
// its coming back, as kept sealed until it ends (pending.go). It is found by
// the hash of its state.
type tripDoc struct {
	V        int          `json:"v"`
	Provider idp.Provider `json:"provider"`
	// Verifier and Nonce are what the provider's answer is checked with.
	Verifier string `json:"verifier"`
	Nonce    string `json:"nonce,omitempty"`
	// Browser is the hash of the value of the cookie the browser was given:
	// the provider's answer ends the trip only where that cookie is.
	Browser []byte `json:"browser"`
	// Invitation is the entry of the invitation a sign-up will use.
	Invitation string `json:"invitation,omitempty"`
	// Account and Session are whose Settings started it, to link a provider
	// to that account and to no other; empty for a sign-in.
	Account string `json:"account,omitempty"`
	Session string `json:"session,omitempty"`
	// ReturnTo is the path of the app the person was going to, or "".
	ReturnTo string `json:"return_to,omitempty"`
	pendingTimes
}

func (t *tripDoc) started() idp.Started { return idp.Started{Verifier: t.Verifier, Nonce: t.Nonce} }

// unconfirmedDoc is a sign-in with a provider that said who the person is
// there and not that an address is theirs: it waits for the code of a
// message, kept sealed and found by the hash of its browser's cookie.
type unconfirmedDoc struct {
	V        int          `json:"v"`
	Provider idp.Provider `json:"provider"`
	Subject  string       `json:"subject"`
	// Address is what the provider gave, to offer in the form. It is never
	// used for anything else.
	Address    string `json:"address,omitempty"`
	Invitation string `json:"invitation,omitempty"`
	ReturnTo   string `json:"return_to,omitempty"`
	pendingTimes
}

// unconfirmedPartition is where the sign-in that waits in a browser is kept.
func unconfirmedPartition(browser string) string {
	return pendingPartition(kindUnconfirmed, secretName(kindUnconfirmed, browser))
}

// tripPartition is where the trip of a state is kept.
func tripPartition(state string) string {
	return pendingPartition(kindTrip, secretName(kindTrip, state))
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

// linkingFor is how long a link that was started holds its provider account
// for its account (accountDoc.Linking). A link takes three steps of a moment
// each; one that stopped halfway holds the provider account from every other
// account for this long, and no longer.
const linkingFor = time.Minute

// linkingDoc is a provider account being linked to the account whose record
// holds it, between the record saying so and the record listing it (link).
type linkingDoc struct {
	Provider idp.Provider `json:"provider"`
	Subject  string       `json:"subject"`
	// Claim names this link, so that its last step is taken only for it.
	Claim string    `json:"claim"`
	At    time.Time `json:"at"`
}

// claims reports whether the record holds a provider account: it lists it,
// or a link of it was started less than linkingFor ago.
func (d *accountDoc) claims(p idp.Provider, subject string, now time.Time) bool {
	l := d.Linking
	return d.signsInWith(p, subject) || (l != nil && l.Provider == p && l.Subject == subject && now.Sub(l.At) < linkingFor)
}

// link makes a provider account sign in to an account. The pointer from the
// provider account is what keeps two accounts from having it, as the pointer
// from an address does, so it goes in three steps, like a change of address:
//
//  1. The record says the provider account is being linked (Linking).
//  2. The pointer is written to lead here, where none is, or where it leads to
//     an account that neither lists the provider account nor is linking it.
//  3. The record lists the provider account, if the pointer still leads here
//     and the link is still this one.
//
// The record says so before the pointer is written: an enclave that finds the
// pointer leading here then finds the record holding the provider account,
// and refuses its own link (ErrProviderInUse). A link that stops halfway holds
// the provider account for linkingFor, and is taken back where it can be.
// Linking what is linked already only writes the pointer again, should it be
// missing. The person is told by mail of a way to sign in that is new, unless
// the account was made by this very sign-in (made).
func (s *Service) link(ctx context.Context, accountID string, p idp.Provider, subject string, made bool) (*accountDoc, error) {
	claim := newSecret()
	var already *accountDoc
	_, err := s.change(ctx, accountID, func(doc *accountDoc) error {
		already = nil
		i := doc.provider(p)
		switch {
		case i >= 0 && doc.Providers[i].Subject != subject:
			return contracts.ErrProviderLinked
		case i >= 0:
			kept := *doc
			already = &kept
			return errUnchanged
		}
		doc.Linking = &linkingDoc{Provider: p, Subject: subject, Claim: claim, At: s.now()}
		return nil
	})
	if errors.Is(err, errUnchanged) {
		return already, s.claimProvider(ctx, accountID, p, subject, false)
	}
	if err != nil {
		return nil, err
	}
	if err := s.claimProvider(ctx, accountID, p, subject, true); err != nil {
		s.unlinking(ctx, accountID, claim, p, subject)
		return nil, err
	}
	partition := providerPartition(p, subject)
	doc, err := s.change(ctx, accountID, func(doc *accountDoc) error {
		l := doc.Linking
		// Half of linkingFor: another enclave, whose clock may be ahead, takes
		// the pointer only once the whole of it is over.
		if l == nil || l.Claim != claim || s.now().Sub(l.At) >= linkingFor/2 {
			return contracts.ErrProviderInUse
		}
		if doc.provider(p) >= 0 {
			return contracts.ErrProviderLinked // another link of this account came first
		}
		here, err := s.pointsTo(ctx, partition, doc.ID)
		if err != nil {
			return err
		}
		if !here {
			return contracts.ErrProviderInUse
		}
		doc.Providers = append(doc.Providers, providerDoc{Provider: p, Subject: subject, LinkedAt: s.now()})
		doc.Linking = nil
		return nil
	})
	if err != nil {
		s.unlinking(ctx, accountID, claim, p, subject)
		return nil, err
	}
	s.cfg.Log.Info("accounts: provider linked", "provider", string(p))
	if !made {
		s.notify(ctx, noticeProviderLinked, providerMail(doc.Address, s.rp.ID, s.now(), p, true))
	}
	return doc, nil
}

// claimProvider writes the pointer from a provider account to an account. It
// refuses when the pointer leads to another account that holds the provider
// account (claims): one provider account signs in to one account. Unless
// again, a pointer that leads here already is left as it is.
func (s *Service) claimProvider(ctx context.Context, id string, p idp.Provider, subject string, again bool) error {
	key, err := s.signingKey(ctx)
	if err != nil {
		return err
	}
	partition := providerPartition(p, subject)
	for range maxRewrites {
		var old *store.Entry
		e, err := s.cfg.Entries.GetEntry(ctx, partition, loginKey)
		switch {
		case errors.Is(err, store.ErrNotFound):
		case err != nil:
			return err
		default:
			old = e
			other := pointed(key, e)
			if other == id && !again {
				return nil
			}
			if other != "" && other != id {
				// An account that cannot be read is never taken for none.
				doc, err := s.read(ctx, other)
				if err != nil {
					return err
				}
				if now := s.now(); doc != nil && !doc.due(now) && doc.claims(p, subject, now) {
					return contracts.ErrProviderInUse
				}
			}
		}
		err = s.repoint(ctx, partition, id, old)
		if !errors.Is(err, store.ErrExists) && !errors.Is(err, store.ErrConflict) {
			return err
		}
		// Someone else wrote it in between: read again who it leads to.
	}
	return errors.New("accounts: the pointer of a provider account keeps changing")
}

// pointsTo reports whether the pointer of a partition leads to an account.
func (s *Service) pointsTo(ctx context.Context, partition, id string) (bool, error) {
	e, err := s.cfg.Entries.GetEntry(ctx, partition, loginKey)
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
	return pointed(key, e) == id, nil
}

// unlinking takes back a link that did not end, if the record still names
// it, and the pointer that may have been written for it.
func (s *Service) unlinking(ctx context.Context, id, claim string, p idp.Provider, subject string) {
	_, err := s.rewrite(ctx, id, func(doc *accountDoc) error {
		if doc.Linking == nil || doc.Linking.Claim != claim {
			return errUnchanged
		}
		doc.Linking = nil
		return nil
	})
	if err == nil || errors.Is(err, errUnchanged) {
		err = s.unclaim(ctx, id, p, subject)
	}
	if err != nil {
		s.cfg.Log.Warn("accounts: a link of a provider that did not end could not be taken back")
	}
}

// unclaim deletes the pointer from a provider account to an account, as it
// was read, when the account's record no longer holds the provider account.
// A pointer that the record holds stays: a link of it may have ended since.
func (s *Service) unclaim(ctx context.Context, id string, p idp.Provider, subject string) error {
	partition := providerPartition(p, subject)
	e, err := s.cfg.Entries.GetEntry(ctx, partition, loginKey)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	key, err := s.signingKey(ctx)
	if err != nil {
		return err
	}
	if pointed(key, e) != id {
		return nil
	}
	doc, err := s.read(ctx, id)
	if err != nil {
		return err
	}
	if doc != nil && doc.claims(p, subject, s.now()) {
		return nil
	}
	// As it was read: a link that writes it again after the record was read
	// writes another value, and that one stays.
	if err := s.cfg.Entries.DeleteEntryAt(ctx, e); err != nil && !errors.Is(err, store.ErrConflict) {
		return err
	}
	return nil
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
	t := &tripDoc{V: 1, Provider: p, ReturnTo: domain.AppPath(in.ReturnTo), pendingTimes: pendingTimes{Made: now, Expires: now.Add(domain.ProviderLifetime)}}
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
		if doc.provider(p) >= 0 {
			return nil, "", contracts.ErrProviderLinked
		}
		t.Account, t.Session = doc.ID, who.Session.ID
	case in.Invitation != "":
		// An invitation is checked at once: it says nothing of any address.
		if t.Invitation, err = s.invitation(ctx, in.Invitation); err != nil {
			return nil, "", err
		}
		if t.Invitation == "" {
			return nil, "", contracts.ErrInvalidInvitation
		}
	}
	state, browser := newSecret(), newSecret()
	started, err := s.cfg.Providers.Begin(p, app, state)
	if err != nil {
		// Not its error: it comes from where the application's secret is used.
		return nil, "", errors.New("accounts: a trip to a provider could not be started")
	}
	sum := sha256.Sum256([]byte(browser))
	t.Verifier, t.Nonce, t.Browser = started.Verifier, started.Nonce, sum[:]
	if err := s.stash(ctx, tripPartition(state), t); err != nil {
		return nil, "", err
	}
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
	t, err := s.arrive(ctx, in.State, in.Browser)
	var done *ProviderDone
	if err == nil {
		if done, err = s.arrived(ctx, t, in); err == nil {
			return done, nil
		}
	}
	screen := signInScreen
	if t != nil && t.Account != "" {
		screen = settingsScreen
	}
	done = &ProviderDone{Path: screen + errorFragment + providerReason(err)}
	if t != nil {
		done.Provider = t.Provider
	}
	return done, err
}

// arrive returns the trip a state belongs to, when it is still running and
// was started by this browser. The trip is over either way: an answer is
// tried once, through whichever enclave it reaches first.
func (s *Service) arrive(ctx context.Context, state, browser string) (*tripDoc, error) {
	if state == "" {
		return nil, nil
	}
	var t tripDoc
	e, err := s.unstash(ctx, tripPartition(state), &t)
	if err != nil || e == nil {
		return nil, err
	}
	// An answer of the provider carried to another person's browser ends
	// here: that browser has no cookie of this trip.
	sum := sha256.Sum256([]byte(browser))
	mine := subtle.ConstantTimeCompare(sum[:], t.Browser) == 1
	ok, err := s.claim(ctx, e, t.Expires)
	switch {
	case err != nil && mine:
		return &t, err // the screen is still the one the browser came from
	case err != nil || !ok || !mine:
		return nil, err
	}
	return &t, nil
}

func (s *Service) arrived(ctx context.Context, t *tripDoc, in ProviderAnswer) (*ProviderDone, error) {
	if t == nil || in.Refused || in.Code == "" {
		return nil, contracts.ErrProviderFailed
	}
	app, on, err := s.app(ctx, t.Provider)
	if err != nil {
		return nil, err
	}
	if !on {
		return nil, contracts.ErrProviderFailed // turned off while the person was away
	}
	who, err := s.cfg.Providers.Finish(ctx, t.Provider, app, in.Code, t.started())
	if err != nil {
		return nil, s.providerError(t.Provider, err)
	}
	if who == nil || who.Provider != t.Provider || !domain.ProviderSubject(who.Subject) {
		s.cfg.Log.Warn("accounts: a provider named an account in a way that cannot be kept", "provider", string(t.Provider))
		return nil, contracts.ErrProviderFailed
	}
	if t.Account != "" {
		return s.linkSignedIn(ctx, t, who)
	}
	doc, err := s.linked(ctx, t.Provider, who.Subject)
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
			return s.confirmLater(ctx, t, who)
		}
		if doc, made, err = s.firstTime(ctx, t, who.Subject, address); err != nil {
			return nil, err
		}
	}
	// The provider said who it is: an account the operator blocked is told
	// so here (enter), and never before.
	_, cookie, err := s.enter(ctx, doc, wayProvider(t.Provider), in.From, made)
	if err != nil {
		return nil, err
	}
	return &ProviderDone{Path: cmp.Or(t.ReturnTo, homeScreen), Session: cookie}, nil
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
func (s *Service) firstTime(ctx context.Context, t *tripDoc, subject, address string) (*accountDoc, bool, error) {
	doc, stale, err := s.lookup(ctx, address)
	made := false
	switch {
	case err != nil:
		return nil, false, err
	case doc == nil && t.Invitation == "":
		return nil, false, contracts.ErrInvitationNeeded
	case doc == nil:
		if doc, made, err = s.signUp(ctx, address, t.Invitation, stale); err != nil {
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
	doc, err = s.link(ctx, doc.ID, t.Provider, subject, made)
	return doc, made, err
}

// linkSignedIn ends a trip that Settings started: the provider account is
// linked to the account of the session that started it, and to no other. An
// address the provider vouches for that is another account's refuses it: the
// two would say different things of whose provider account it is.
func (s *Service) linkSignedIn(ctx context.Context, t *tripDoc, who *idp.Identity) (*ProviderDone, error) {
	open, err := s.sessionOpen(ctx, t.Account, t.Session)
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
		if other != nil && other.ID != t.Account {
			return nil, contracts.ErrProviderInUse
		}
	}
	if _, err := s.link(ctx, t.Account, t.Provider, who.Subject, false); err != nil {
		return nil, err
	}
	return &ProviderDone{Path: cmp.Or(t.ReturnTo, settingsScreen)}, nil
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
	session := s.readSession(key, e, accountID)
	if session == nil {
		return false, nil
	}
	// Signed out since, with every other device, it is no longer open.
	doc, err := s.read(ctx, accountID)
	if err != nil {
		return false, err
	}
	return doc != nil && doc.stands(session), nil
}

// ---- confirming an address ----

// confirmLater keeps a sign-in whose provider did not vouch for an address,
// for the browser that made the trip. Nothing is looked up, linked or made:
// the answer is the same whoever's address the provider gave.
func (s *Service) confirmLater(ctx context.Context, t *tripDoc, who *idp.Identity) (*ProviderDone, error) {
	now, browser := s.now(), newSecret()
	// Offered in the form when it can be an address at all, and never trusted.
	address, _ := domain.NormalizeAddress(who.Address)
	u := &unconfirmedDoc{
		V: 1, Provider: t.Provider, Subject: who.Subject, Address: address, Invitation: t.Invitation,
		ReturnTo: t.ReturnTo, pendingTimes: pendingTimes{Made: now, Expires: now.Add(domain.ProviderConfirmLifetime)},
	}
	if err := s.stash(ctx, unconfirmedPartition(browser), u); err != nil {
		return nil, err
	}
	return &ProviderDone{Path: signInScreen + confirmFragment, Browser: browser}, nil
}

// waitingIn returns the sign-in that waits in a browser for its address, and
// its entry.
func (s *Service) waitingIn(ctx context.Context, browser string) (*unconfirmedDoc, *store.Entry, error) {
	if browser == "" {
		return nil, nil, contracts.ErrProviderFailed
	}
	var u unconfirmedDoc
	e, err := s.unstash(ctx, unconfirmedPartition(browser), &u)
	if err != nil {
		return nil, nil, err
	}
	if e == nil {
		return nil, nil, contracts.ErrProviderFailed
	}
	s.mu.Lock()
	used := s.marks[e.Partition] != nil && s.marks[e.Partition].used
	s.mu.Unlock()
	if used {
		return nil, nil, contracts.ErrProviderFailed // put back after it ended here
	}
	return &u, e, nil
}

// PendingProvider says what waits in a browser: which provider, and the
// address to offer in the form.
func (s *Service) PendingProvider(ctx context.Context, browser string) (*contracts.ProviderPending, error) {
	u, _, err := s.waitingIn(ctx, browser)
	if err != nil {
		return nil, err
	}
	return &contracts.ProviderPending{
		Provider: string(u.Provider), Address: u.Address, ReturnTo: cmp.Or(u.ReturnTo, homeScreen),
		ExpiresIn: int(u.Expires.Sub(s.now()) / time.Second),
	}, nil
}

// StartProviderConfirm sends the message that confirms an address for the
// sign-in that waits in a browser. It is a sign-in by email, with its limits
// and its one answer for every address: a message goes out when the address
// has an account, or when the invitation given at the start lets it make one.
func (s *Service) StartProviderConfirm(ctx context.Context, browser string, in contracts.ProviderConfirmStart) (*contracts.EmailStarted, error) {
	u, _, err := s.waitingIn(ctx, browser)
	if err != nil {
		return nil, err
	}
	address, err := domain.NormalizeAddress(in.Address)
	if err != nil {
		return nil, err
	}
	return s.startEmail(ctx, address, u.Invitation)
}

// FinishProviderConfirm ends the sign-in that waits in a browser with the
// code of the message: the address is the person's, so the provider account
// is linked to its account, or to a new one, and the device is signed in. A
// wrong code leaves it waiting, with the tries a sign-in by email takes.
func (s *Service) FinishProviderConfirm(ctx context.Context, browser string, in contracts.ProviderConfirm, from Browser) (*contracts.Me, string, error) {
	u, e, err := s.waitingIn(ctx, browser)
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
	// It waited for this, and is over whatever comes of linking. Of two
	// enclaves that end it at once, with two codes of the same browser, one
	// does.
	ok, err := s.claim(ctx, e, u.Expires)
	if err != nil {
		return nil, "", err
	}
	if !ok {
		return nil, "", contracts.ErrProviderFailed
	}
	if doc, err = s.link(ctx, doc.ID, u.Provider, u.Subject, made); err != nil {
		return nil, "", err
	}
	return s.enter(ctx, doc, wayProvider(u.Provider), from, made)
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
	subject := ""
	doc, err := s.change(ctx, who.AccountID, func(doc *accountDoc) error {
		i := doc.provider(p)
		if i < 0 {
			return contracts.ErrNotFound
		}
		if doc.waysIn() <= 1 {
			return contracts.ErrLastWayIn
		}
		subject = doc.Providers[i].Subject
		doc.Providers = slices.Delete(doc.Providers, i, i+1)
		return nil
	})
	if err != nil {
		return err
	}
	// The pointer goes once the record no longer lists the provider account,
	// and only if it still does not: a change that was run again, or refused
	// on its second run, deletes no pointer of a link that stayed.
	if err := s.unclaim(ctx, doc.ID, p, subject); err != nil {
		// The record says it is not linked: the pointer leads nowhere.
		s.cfg.Log.Warn("accounts: the pointer of a provider account that was unlinked could not be deleted")
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

// appsFresh is how long an enclave uses the applications it read before it
// reads their record again: an application set or removed through another
// enclave is used here within that time, without a restart.
const appsFresh = time.Minute

// readApps reads the applications' record, and says where it was read.
func (s *Service) readApps(ctx context.Context) (map[idp.Provider]appDoc, store.Mark, error) {
	var doc appsDoc
	rec, at, err := s.cfg.Records.GetRecordAt(ctx, providerAppsRecord)
	switch {
	case errors.Is(err, store.ErrLost):
		// What was committed is gone, and an earlier copy is never taken for
		// it: every provider is off until the operator sets them again.
		s.cfg.Log.Error("accounts: the record of the providers' applications is lost; every provider is off until they are set again")
	case errors.Is(err, store.ErrNotFound):
		// None was ever set.
	case err != nil:
		return nil, at, err
	default:
		key, err := s.sealingKey(ctx)
		if err != nil {
			return nil, at, err
		}
		plain, err := envelope.Open(key, rec.Value, recordAAD(providerAppsRecord))
		// Not the error of the JSON: it could repeat what was sealed.
		if err != nil || json.Unmarshal(plain, &doc) != nil {
			return nil, at, errors.New("accounts: the record of the providers' applications cannot be opened")
		}
	}
	return doc.Apps, at, nil
}

// loadApps returns the applications, with appsMu held: as read less than
// appsFresh ago, or read again.
func (s *Service) loadApps(ctx context.Context) (map[idp.Provider]appDoc, error) {
	if now := s.now(); !s.appsRead.IsZero() && now.Sub(s.appsRead) < appsFresh && !now.Before(s.appsRead) {
		return s.apps, nil
	}
	apps, _, err := s.readApps(ctx)
	if err != nil {
		return nil, err
	}
	s.apps, s.appsRead = apps, s.now()
	return s.apps, nil
}

// changeApps lets edit change the applications and stores them, sealed, as
// the newest version of their record, only if nothing was committed for it
// since it was read: another enclave may have set another provider meanwhile,
// and then the record is read again and edit runs again on what is there.
// edit reports whether it changed any.
func (s *Service) changeApps(ctx context.Context, edit func(map[idp.Provider]appDoc) bool) error {
	s.appsMu.Lock()
	defer s.appsMu.Unlock()
	key, err := s.sealingKey(ctx)
	if err != nil {
		return err
	}
	for range maxRewrites {
		apps, at, err := s.readApps(ctx)
		if err != nil {
			return err
		}
		next := map[idp.Provider]appDoc{}
		maps.Copy(next, apps)
		if !edit(next) {
			s.apps, s.appsRead = apps, s.now()
			return nil
		}
		raw, _ := json.Marshal(appsDoc{V: 1, Apps: next})
		sealed, err := envelope.Seal(key, raw, recordAAD(providerAppsRecord))
		if err != nil {
			return err
		}
		switch err := s.cfg.Records.PutRecordAt(ctx, &store.Record{Name: providerAppsRecord, Value: sealed, UpdatedAt: s.now()}, at); {
		case errors.Is(err, store.ErrChanged):
			continue
		case err != nil:
			return err
		}
		s.apps, s.appsRead = next, s.now()
		return nil
	}
	return errors.New("accounts: the record of the providers' applications keeps changing")
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
	s.logged(ctx, by, ActionProviderAppSet, "", "")
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
		s.logged(ctx, by, ActionProviderAppRemove, "", "")
	}
	return err
}
