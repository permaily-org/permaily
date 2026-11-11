// Package accounts is the service behind accounts and sign-in (ADR 0017):
// invitations, signing in by email, with passkeys and with a provider
// (providers.go), "passkey only" with its recovery codes, and sessions.
//
// What it stores falls under three of the ADR's treatments. An account's
// record is readable and signed: whoever runs the storage reads the contact
// address and the passkeys' public keys, and cannot write either, because
// that would be a way into the account; it is read through a store that
// refuses an earlier copy, so a passkey that was removed or a recovery code
// that was used does not come back. Sessions and invitations are kept by the
// hash of their secret, in entries that are signed too, so one cannot be made
// by hand or moved to another account. A sign-in that was started, by mail or
// with a passkey, is kept nowhere but in this enclave's memory: its count of
// wrong codes cannot be set back by whoever runs the storage, and a restart
// ends it.
//
// An account its person asked to delete is closing for seven days and then
// removed: its record is committed as gone, so nothing put back in the
// storage brings it back (settings.go).
package accounts

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/domain"
	"github.com/permaily-org/permaily/internal/envelope"
	"github.com/permaily-org/permaily/internal/idp"
	"github.com/permaily-org/permaily/internal/store"
	"github.com/permaily-org/permaily/internal/webauthn"
)

// Keys gives the key that signs what is stored readable. keys.Root derives
// it from the root secret, so only an enclave of the deployment has it.
type Keys interface {
	Derive(ctx context.Context, purpose string) ([]byte, error)
}

// signingPurpose names the key that signs accounts, sessions and invitations.
const signingPurpose = "permaily signed values v1"

// Mail is a message the service sends to a person.
type Mail struct {
	To      string
	Subject string
	Text    string
}

// Mailer sends mail. What it returns is never logged: a mail system's errors
// carry addresses.
type Mailer interface {
	Send(ctx context.Context, m Mail) error
}

// Records is where the records of accounts are kept: a store that refuses an
// earlier copy of one, and that removes one for good (store.Anchored).
type Records interface {
	store.Records
	// RemoveRecord makes a record be not there from then on, whatever is put
	// back in the storage, and deletes what is stored of it.
	RemoveRecord(ctx context.Context, name string) error
}

// Config is what a Service needs.
type Config struct {
	// Records holds the account records. It must refuse an earlier copy of
	// one (store.Anchored): an account's ways to sign in must not come back,
	// and neither must an account that was removed.
	Records Records
	Entries store.Entries
	Keys    Keys
	Mailer  Mailer
	// Providers is how Google, GitHub, Meta and Microsoft are asked who signed
	// in (idp.Client). Without it every provider is off.
	Providers Providers
	// AppURL is where the web app is served, without a trailing slash. Its
	// host is who passkeys are made for, and it is the only page whose
	// answers about a passkey are taken.
	AppURL string
	Log    *slog.Logger
	Now    func() time.Time
}

// Limits on what one enclave keeps in memory and starts at once.
const (
	maxAttempts = 10000
	// maxCeremonies is how many passkey ceremonies wait for their answer.
	maxCeremonies = 10000
	maxSending    = 32
	// maxSessions is how many devices an account stays signed in on; the one
	// used longest ago goes when another signs in.
	maxSessions = 50
	// MaxInvitations is how many invitations one request makes.
	MaxInvitations = 100
	// wrongCodesPerDay is how many wrong codes an address takes in a day
	// before its messages carry only the link: guessing a six-digit code
	// stays at a few tries a day whatever the number of sign-ins started.
	wrongCodesPerDay = 10
	sendTimeout      = 30 * time.Second
)

// Service implements accounts and sign-in.
type Service struct {
	cfg Config
	rp  webauthn.RelyingParty

	mu         sync.Mutex
	attempts   map[string]*attempt
	ceremonies map[string]*ceremony
	// trips are the sign-ins with a provider whose answer is awaited, by their
	// state; unconfirmed are the ones that wait for an address to be
	// confirmed, by the hash of their browser's cookie.
	trips       map[string]*trip
	unconfirmed map[string]*unconfirmed
	// starts and wrong count, per address or invitation, the sign-ins started
	// in the last hour and the wrong codes of the last day; recoveries, per
	// address, the wrong recovery codes of the last hour.
	starts     map[string][]time.Time
	wrong      map[string][]time.Time
	recoveries map[string][]time.Time
	swept      time.Time

	// changing is held while an account's record is read, changed and
	// written. The storage takes whichever write comes last, so two changes
	// at once would undo one another: a recovery code would work twice.
	changing sync.Mutex

	// The applications registered at the providers, as their sealed record
	// holds them: read once, and replaced with each change. A map that was
	// handed out is never written to again.
	appsMu   sync.Mutex
	apps     map[idp.Provider]appDoc
	appsRead bool

	sending chan struct{}
	work    sync.WaitGroup
}

// attempt is a sign-in that was started and whose message was sent.
type attempt struct {
	address string
	// invitation is the entry of the invitation the sign-up will use, if the
	// address has no account when the sign-in ends.
	invitation string
	// code is empty when the message carried only the link.
	code, token [sha256.Size]byte
	withCode    bool
	expires     time.Time
	tries       int
}

// New returns a Service.
func New(cfg Config) (*Service, error) {
	if cfg.Records == nil || cfg.Entries == nil || cfg.Keys == nil || cfg.Mailer == nil || cfg.AppURL == "" {
		return nil, errors.New("accounts: records, entries, keys, a mailer and the app's URL are needed")
	}
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	app, err := url.Parse(cfg.AppURL)
	if err != nil || app.Hostname() == "" {
		return nil, errors.New("accounts: the app's URL has no host")
	}
	return &Service{
		cfg: cfg,
		// The port is part of the origin and not of who the passkeys are for.
		rp:         webauthn.RelyingParty{ID: app.Hostname(), Origins: []string{app.Scheme + "://" + app.Host}},
		attempts:   map[string]*attempt{},
		ceremonies: map[string]*ceremony{},
		trips:      map[string]*trip{}, unconfirmed: map[string]*unconfirmed{},
		starts:     map[string][]time.Time{},
		wrong:      map[string][]time.Time{},
		recoveries: map[string][]time.Time{},
		sending:    make(chan struct{}, maxSending),
	}, nil
}

// Wait returns when the messages being sent are sent.
func (s *Service) Wait() { s.work.Wait() }

func (s *Service) now() time.Time { return s.cfg.Now().UTC() }

// Identifiers are random and say what they name: acc_…, ses_…, att_…, cer_….
var idEncoding = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

const idLength = 26 // 16 bytes in base32

func newID(kind string) string {
	b := make([]byte, 16)
	_, _ = rand.Read(b) // crypto/rand does not fail
	return kind + "_" + idEncoding.EncodeToString(b)
}

func validID(kind, id string) bool {
	rest, ok := strings.CutPrefix(id, kind+"_")
	if !ok || len(rest) != idLength {
		return false
	}
	_, err := idEncoding.DecodeString(rest)
	return err == nil
}

func newSecret() string {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// randomString returns n characters of alphabet, each as likely as the others.
func randomString(alphabet string, n int) string {
	out := make([]byte, n)
	for i := range out {
		k, _ := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		out[i] = alphabet[k.Int64()]
	}
	return string(out)
}

// identifiers are taken out of the errors this package logs: logs leave the
// enclave, and a store's error names the record it was about.
var identifiers = regexp.MustCompile(`\b(acc|ses|att|cer)_[a-z2-7]{26}`)

func scrub(err error) string { return identifiers.ReplaceAllString(err.Error(), "${1}_…") }

func (s *Service) signingKey(ctx context.Context) ([]byte, error) {
	key, err := s.cfg.Keys.Derive(ctx, signingPurpose)
	if err != nil {
		return nil, fmt.Errorf("accounts: signing key: %w", err)
	}
	return key, nil
}

// ---- accounts ----

// accountDoc is an account's record as stored: readable and signed.
type accountDoc struct {
	V         int                    `json:"v"`
	ID        string                 `json:"id"`
	State     contracts.AccountState `json:"state"`
	Address   string                 `json:"address"`
	Name      string                 `json:"name"`
	Language  string                 `json:"language"`
	CreatedAt time.Time              `json:"created_at"`
	// AddressVerifiedAt is when the person last proved the address is theirs.
	AddressVerifiedAt time.Time `json:"address_verified_at"`
	// Passkeys sign in to the account. One that is not here signs nobody in.
	Passkeys []passkeyDoc `json:"passkeys,omitempty"`
	// PasskeyOnly turns signing in by mail off.
	PasskeyOnly bool `json:"passkey_only,omitempty"`
	// RecoveryCodes are the hashes of the recovery codes not used yet.
	RecoveryCodes [][]byte `json:"recovery_codes,omitempty"`
	// Providers are the provider accounts that sign in to the account. One
	// that is not here signs nobody in, wherever its pointer leads.
	Providers []providerDoc `json:"providers,omitempty"`
	// ClosesAt is when a closing account is removed.
	ClosesAt time.Time `json:"closes_at,omitzero"`
}

func (d *accountDoc) view() contracts.Account {
	return contracts.Account{
		ID: d.ID, State: d.State, Address: d.Address, Name: d.Name, Language: d.Language,
		CreatedAt: d.CreatedAt, ClosesAt: d.ClosesAt,
	}
}

// due reports whether the account's person asked to delete it and the days
// they had to cancel are over: from then on it is nobody's account.
func (d *accountDoc) due(now time.Time) bool {
	return d.State == contracts.AccountClosing && !now.Before(d.ClosesAt)
}

func recordName(accountID string) string { return "account-" + accountID }

func recordAAD(name string) []byte { return envelope.AAD("record", name) }

func entryAAD(partition, key string) []byte { return envelope.AAD("entry", partition, key) }

// account returns an account that can be used: nil when there is none, and
// an error when there is one that cannot be trusted (read). An account that
// is due is removed here, the moment anyone tries to use it, so it does not
// live on when the list that the enclave finds it by was written to.
func (s *Service) account(ctx context.Context, id string) (*accountDoc, error) {
	doc, err := s.read(ctx, id)
	if err != nil || doc == nil || !doc.due(s.now()) {
		return doc, err
	}
	if err := s.remove(ctx, id); err != nil {
		return nil, err
	}
	return nil, nil
}

// read reads an account's record. It returns nil when there is no such
// account, and an error when there is one that cannot be trusted: a record
// that does not verify, or whose newest version is gone. Such an account is
// never taken for a missing one, which a sign-up could then replace.
func (s *Service) read(ctx context.Context, id string) (*accountDoc, error) {
	rec, err := s.cfg.Records.GetRecord(ctx, recordName(id))
	switch {
	case errors.Is(err, store.ErrLost):
		return nil, fmt.Errorf("accounts: an account's record: %w", err)
	case errors.Is(err, store.ErrNotFound):
		return nil, nil
	case err != nil:
		return nil, err
	}
	key, err := s.signingKey(ctx)
	if err != nil {
		return nil, err
	}
	raw, err := envelope.Verified(key, rec.Value, recordAAD(recordName(id)))
	if err != nil {
		return nil, fmt.Errorf("accounts: an account's record: %w", err)
	}
	var doc accountDoc
	if err := json.Unmarshal(raw, &doc); err != nil || doc.ID != id {
		return nil, errors.New("accounts: an account's record is not one")
	}
	return &doc, nil
}

func (s *Service) putAccount(ctx context.Context, doc *accountDoc, create bool) error {
	key, err := s.signingKey(ctx)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	name := recordName(doc.ID)
	value, err := envelope.Sign(key, raw, recordAAD(name))
	if err != nil {
		return err
	}
	return s.cfg.Records.PutRecord(ctx, &store.Record{Name: name, Value: value, Plain: true, UpdatedAt: s.now()}, create)
}

// own returns the account behind a session.
func (s *Service) own(ctx context.Context, who *Authenticated) (*accountDoc, error) {
	doc, err := s.account(ctx, who.AccountID)
	if err != nil {
		return nil, err
	}
	if doc == nil {
		return nil, contracts.ErrUnauthorized
	}
	return doc, nil
}

// change is rewrite for everything a closing account refuses: all but
// cancelling, and a recovery code used to sign in.
func (s *Service) change(ctx context.Context, id string, edit func(*accountDoc) error) (*accountDoc, error) {
	return s.rewrite(ctx, id, func(doc *accountDoc) error {
		if doc.State == contracts.AccountClosing {
			return contracts.ErrAccountClosing
		}
		return edit(doc)
	})
}

// rewrite reads an account's record, lets edit change it and stores it as the
// newest version. When edit fails nothing is stored. An account that is not
// there, or is due and so removed here, is ErrUnauthorized: its session
// stands for nothing.
func (s *Service) rewrite(ctx context.Context, id string, edit func(*accountDoc) error) (*accountDoc, error) {
	s.changing.Lock()
	defer s.changing.Unlock()
	doc, err := s.read(ctx, id)
	if err != nil {
		return nil, err
	}
	if doc != nil && doc.due(s.now()) {
		if err := s.removeLocked(ctx, doc); err != nil {
			return nil, err
		}
		doc = nil
	}
	if doc == nil {
		return nil, contracts.ErrUnauthorized
	}
	if err := edit(doc); err != nil {
		return nil, err
	}
	if err := s.putAccount(ctx, doc, false); err != nil {
		return nil, err
	}
	return doc, nil
}

// Account returns an account by its id.
func (s *Service) Account(ctx context.Context, id string) (*contracts.Account, error) {
	if !validID("acc", id) {
		return nil, contracts.ErrNotFound
	}
	doc, err := s.account(ctx, id)
	if err != nil {
		return nil, err
	}
	if doc == nil {
		return nil, contracts.ErrNotFound
	}
	a := doc.view()
	return &a, nil
}

// An address leads to its account through an entry. The entry is only a
// pointer: the account's record says which address is its own, so a pointer
// written by hand leads nowhere. The entries of the two lists of accounts
// (settings.go) are pointers too.
type loginDoc struct {
	Account string `json:"account"`
}

// pointed returns the account a pointer names, or "" when the entry is not
// one this deployment wrote where it is.
func pointed(key []byte, e *store.Entry) string {
	var login loginDoc
	raw, err := envelope.Verified(key, e.Value, entryAAD(e.Partition, e.Key))
	if err != nil || json.Unmarshal(raw, &login) != nil || !validID("acc", login.Account) {
		return ""
	}
	return login.Account
}

// point writes the pointer to an account at an entry, in the place of the
// version given (0 for a new one).
func (s *Service) point(ctx context.Context, partition, key, accountID string, version uint64) error {
	signing, err := s.signingKey(ctx)
	if err != nil {
		return err
	}
	e := &store.Entry{Partition: partition, Key: key, Version: version, UpdatedAt: s.now()}
	raw, _ := json.Marshal(loginDoc{Account: accountID})
	if e.Value, err = envelope.Sign(signing, raw, entryAAD(partition, key)); err != nil {
		return err
	}
	return s.cfg.Entries.PutEntry(ctx, e)
}

// forget deletes an entry that may be gone already.
func (s *Service) forget(ctx context.Context, partition, key string) error {
	if err := s.cfg.Entries.DeleteEntry(ctx, partition, key); err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	return nil
}

func loginPartition(address string) string { return "login#email#" + address }

const loginKey = "account"

// lookup returns the account that address signs in to, nil when it has none,
// and the pointer that was found, which a sign-up replaces when it leads
// nowhere.
func (s *Service) lookup(ctx context.Context, address string) (*accountDoc, *store.Entry, error) {
	return s.follow(ctx, loginPartition(address), func(doc *accountDoc) bool { return doc.Address == address })
}

// follow returns the account a pointer leads to, when the account's own
// record bears the pointer out (owns): nil when it does not or there is no
// pointer, and the pointer that was found.
func (s *Service) follow(ctx context.Context, partition string, owns func(*accountDoc) bool) (*accountDoc, *store.Entry, error) {
	e, err := s.cfg.Entries.GetEntry(ctx, partition, loginKey)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	key, err := s.signingKey(ctx)
	if err != nil {
		return nil, nil, err
	}
	id := pointed(key, e)
	if id == "" {
		return nil, e, nil
	}
	doc, err := s.account(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	if doc == nil || !owns(doc) {
		return nil, e, nil
	}
	return doc, e, nil
}

// create makes the account of address, which just proved to be the person's.
// stale is the pointer that led nowhere, if there was one.
func (s *Service) create(ctx context.Context, address string, stale *store.Entry) (*accountDoc, error) {
	now := s.now()
	doc := &accountDoc{
		V: 1, ID: newID("acc"), State: contracts.AccountActive, Address: address,
		Language: "en", CreatedAt: now, AddressVerifiedAt: now,
	}
	// The entry in the list of accounts goes in before the record: one without
	// a record lists nothing, where an account without one would never be shown.
	if err := s.point(ctx, accountsPartition, listKey(doc), doc.ID, 0); err != nil {
		return nil, err
	}
	if err := s.putAccount(ctx, doc, true); err != nil {
		_ = s.forget(ctx, accountsPartition, listKey(doc))
		return nil, err
	}
	version := uint64(0)
	if stale != nil {
		version = stale.Version
	}
	err := s.point(ctx, loginPartition(address), loginKey, doc.ID, version)
	if err != nil {
		// No address leads to the record just written, and none will: it is
		// removed, as far as that works now.
		_ = s.forget(ctx, accountsPartition, listKey(doc))
		_ = s.cfg.Records.RemoveRecord(ctx, recordName(doc.ID))
	}
	if errors.Is(err, store.ErrExists) || errors.Is(err, store.ErrConflict) {
		// Another sign-up of the same address ended first: that account is
		// the address's.
		winner, _, err := s.lookup(ctx, address)
		if err != nil {
			return nil, err
		}
		if winner == nil {
			return nil, errors.New("accounts: the pointer of an address keeps changing")
		}
		return winner, nil
	}
	if err != nil {
		return nil, err
	}
	s.cfg.Log.Info("accounts: account created")
	return doc, nil
}

// ---- invitations ----

type invitationDoc struct {
	V         int       `json:"v"`
	CreatedAt time.Time `json:"created_at"`
}

func invitationPartition(code string) string {
	sum := sha256.Sum256([]byte("permaily invitation v1\x00" + code))
	return "invitation#" + hex.EncodeToString(sum[:])
}

const invitationKey = "invitation"

// CreateInvitations makes single-use sign-up codes. They are returned once:
// only their hash is kept.
func (s *Service) CreateInvitations(ctx context.Context, count int) (*contracts.Invitations, error) {
	if count < 1 || count > MaxInvitations {
		return nil, &contracts.Error{Code: "invalid_count", Message: fmt.Sprintf("count must be 1-%d", MaxInvitations)}
	}
	key, err := s.signingKey(ctx)
	if err != nil {
		return nil, err
	}
	now := s.now()
	out := &contracts.Invitations{ExpiresAt: now.Add(domain.InvitationLifetime)}
	raw, _ := json.Marshal(invitationDoc{V: 1, CreatedAt: now})
	for range count {
		code := randomString(domain.InvitationAlphabet, domain.InvitationLength)
		e := &store.Entry{Partition: invitationPartition(code), Key: invitationKey, ExpiresAt: out.ExpiresAt, UpdatedAt: now}
		if e.Value, err = envelope.Sign(key, raw, entryAAD(e.Partition, e.Key)); err != nil {
			return nil, err
		}
		if err := s.cfg.Entries.PutEntry(ctx, e); err != nil {
			return nil, err
		}
		out.Invitations = append(out.Invitations, domain.FormatInvitation(code))
	}
	return out, nil
}

// invitation returns the entry of an invitation that can still be used, or
// "" when code is not one.
func (s *Service) invitation(ctx context.Context, code string) (string, error) {
	code, ok := domain.NormalizeInvitation(code)
	if !ok {
		return "", nil
	}
	partition := invitationPartition(code)
	e, err := s.cfg.Entries.GetEntry(ctx, partition, invitationKey)
	if errors.Is(err, store.ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	key, err := s.signingKey(ctx)
	if err != nil {
		return "", err
	}
	var doc invitationDoc
	raw, err := envelope.Verified(key, e.Value, entryAAD(partition, invitationKey))
	// The time comes from what was signed: the entry's own expiry can be
	// written by hand.
	if err != nil || json.Unmarshal(raw, &doc) != nil || !doc.CreatedAt.Add(domain.InvitationLifetime).After(s.now()) {
		return "", nil
	}
	return partition, nil
}

// ---- signing in by email ----

// recent drops from list the times older than window, and returns the rest.
func recent(list []time.Time, now time.Time, window time.Duration) []time.Time {
	return slices.DeleteFunc(list, func(t time.Time) bool { return !t.After(now.Add(-window)) })
}

// sweep forgets what ended. Called with the lock held.
func (s *Service) sweep(now time.Time) {
	if now.Sub(s.swept) < time.Minute {
		return
	}
	s.swept = now
	for id, a := range s.attempts {
		if !a.expires.After(now) {
			delete(s.attempts, id)
		}
	}
	for id, c := range s.ceremonies {
		if !c.expires.After(now) {
			delete(s.ceremonies, id)
		}
	}
	ended(s.trips, now)
	ended(s.unconfirmed, now)
	for _, counted := range []struct {
		m      map[string][]time.Time
		window time.Duration
	}{{s.starts, time.Hour}, {s.wrong, 24 * time.Hour}, {s.recoveries, time.Hour}} {
		for k, list := range counted.m {
			if list = recent(list, now, counted.window); len(list) == 0 {
				delete(counted.m, k)
			} else {
				counted.m[k] = list
			}
		}
	}
}

// allow counts one more sign-in started for subject, and reports whether it
// is within what an hour takes.
func (s *Service) allow(subject string, now time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweep(now)
	list := recent(s.starts[subject], now, time.Hour)
	if len(list) >= domain.EmailStartsPerHour {
		s.starts[subject] = list
		return false
	}
	s.starts[subject] = append(list, now)
	return true
}

// StartEmail starts a sign-in by email. The answer is the same whether the
// address has an account or not, and comes before anything is looked up, so
// neither what it says nor how long it takes tells which addresses have one.
// A message is sent when the address has an account, or when the invitation
// lets it make one.
func (s *Service) StartEmail(ctx context.Context, in contracts.EmailStart) (*contracts.EmailStarted, error) {
	address, err := domain.NormalizeAddress(in.Address)
	if err != nil {
		return nil, err
	}
	invitation := ""
	if in.Invitation != "" {
		// An invitation is checked at once: it says nothing of any address.
		if invitation, err = s.invitation(ctx, in.Invitation); err != nil {
			return nil, err
		}
		if invitation == "" {
			return nil, contracts.ErrInvalidInvitation
		}
	}
	return s.startEmail(ctx, address, invitation)
}

// startEmail starts a sign-in by email for an address as accounts are found
// by, with the entry of an invitation that was checked, if there is one.
func (s *Service) startEmail(ctx context.Context, address, invitation string) (*contracts.EmailStarted, error) {
	if invitation != "" && !s.allow(invitation, s.now()) {
		return nil, contracts.ErrInvalidInvitation
	}
	id := newID("att")
	select {
	case s.sending <- struct{}{}:
		s.work.Add(1)
		go func() {
			defer func() { <-s.sending; s.work.Done() }()
			// The request may end before the message is sent.
			ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), sendTimeout)
			defer cancel()
			s.deliver(ctx, id, address, invitation)
		}()
	default:
		s.cfg.Log.Warn("accounts: too many sign-in messages being sent, one was dropped")
	}
	return &contracts.EmailStarted{Attempt: id, ExpiresIn: int(domain.EmailCodeLifetime / time.Second)}, nil
}

// deliver sends the message of a sign-in, if one is due.
func (s *Service) deliver(ctx context.Context, id, address, invitation string) {
	doc, _, err := s.lookup(ctx, address)
	if err != nil {
		s.cfg.Log.Error("accounts: looking an address up", "err", scrub(err))
		return
	}
	if doc == nil && invitation == "" {
		return // no account, and nothing that lets the address make one
	}
	if doc != nil && doc.PasskeyOnly {
		return // the account turned signing in by mail off
	}
	now := s.now()
	if !s.allow(address, now) {
		return
	}
	a := &attempt{address: address, invitation: invitation, expires: now.Add(domain.EmailCodeLifetime)}
	code, token := "", newSecret()
	a.token = sha256.Sum256([]byte(token))

	s.mu.Lock()
	if len(s.attempts) >= maxAttempts {
		s.mu.Unlock()
		s.cfg.Log.Warn("accounts: too many sign-ins waiting, one was dropped")
		return
	}
	if s.wrong[address] = recent(s.wrong[address], now, 24*time.Hour); len(s.wrong[address]) < wrongCodesPerDay {
		code = randomString("0123456789", domain.EmailCodeDigits)
		a.code, a.withCode = sha256.Sum256([]byte(code)), true
	}
	s.attempts[id] = a
	s.mu.Unlock()

	if err := s.cfg.Mailer.Send(ctx, signInMail(address, s.cfg.AppURL, id, code, token)); err != nil {
		// Not the error: a mail system's errors carry the address.
		s.cfg.Log.Warn("accounts: a sign-in message could not be sent")
		s.mu.Lock()
		delete(s.attempts, id)
		s.mu.Unlock()
	}
}

// signInMail writes the one message a sign-in sends. code is empty when the
// address took too many wrong codes today: then only the link works.
func signInMail(to, appURL, attempt, code, token string) Mail {
	link := fmt.Sprintf("%s/sign-in#attempt=%s&token=%s", appURL, attempt, token)
	minutes := int(domain.EmailCodeLifetime / time.Minute)
	var b strings.Builder
	if code != "" {
		fmt.Fprintf(&b, "Your code to sign in to Permaily:\n\n    %s\n\nOr open this link on the device you want to sign in on:\n\n", code)
	} else {
		b.WriteString("Open this link on the device you want to sign in to Permaily on:\n\n")
	}
	fmt.Fprintf(&b, "    %s\n\nIt works once, for %d minutes. If you did not ask for it, ignore this message: nobody signs in without it.\n", link, minutes)
	return Mail{To: to, Subject: "Sign in to Permaily", Text: b.String()}
}

// take ends a sign-in with the code or the token of its message, and returns
// what it was started for. Every failure is the same error.
func (s *Service) take(in contracts.EmailFinish) (*attempt, error) {
	return s.match(in, true)
}

// match finds the sign-in that the code or the token of in belongs to. With
// use it ends it: a sign-in works once. Without, it is only looked at, and a
// wrong token counts against it all the same.
func (s *Service) match(in contracts.EmailFinish, use bool) (*attempt, error) {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.attempts[in.Attempt]
	if a == nil || !a.expires.After(now) {
		delete(s.attempts, in.Attempt)
		return nil, contracts.ErrInvalidCode
	}
	ok := false
	switch {
	case in.Token != "":
		sum := sha256.Sum256([]byte(in.Token))
		ok = subtle.ConstantTimeCompare(sum[:], a.token[:]) == 1
	case a.withCode:
		sum := sha256.Sum256([]byte(in.Code))
		ok = subtle.ConstantTimeCompare(sum[:], a.code[:]) == 1
		if !ok {
			s.wrong[a.address] = append(recent(s.wrong[a.address], now, 24*time.Hour), now)
		}
	}
	if !ok {
		if a.tries++; a.tries >= domain.EmailCodeTries {
			delete(s.attempts, in.Attempt)
		}
		return nil, contracts.ErrInvalidCode
	}
	if use {
		delete(s.attempts, in.Attempt) // it works once
	}
	found := *a // a copy: the one kept may change once the lock is let go
	return &found, nil
}

// FinishEmail ends a sign-in by email: it signs the device in, to the
// address's account or, with an invitation, to a new one. It returns the
// account and the value of the session's cookie.
func (s *Service) FinishEmail(ctx context.Context, in contracts.EmailFinish, userAgent string) (*contracts.Me, string, error) {
	doc, err := s.proved(ctx, in)
	if err != nil {
		return nil, "", err
	}
	session, cookie, err := s.newSession(ctx, doc.ID, userAgent)
	if err != nil {
		return nil, "", err
	}
	return &contracts.Me{Account: doc.view(), Session: *session}, cookie, nil
}

// proved ends a sign-in by email with the code or the token of its message,
// and returns the account of the address that so proved to be the person's:
// the one it has or, with an invitation, a new one.
func (s *Service) proved(ctx context.Context, in contracts.EmailFinish) (*accountDoc, error) {
	a, err := s.take(in)
	if err != nil {
		return nil, err
	}
	doc, stale, err := s.lookup(ctx, a.address)
	switch {
	case err != nil:
		return nil, err
	case doc != nil && doc.PasskeyOnly:
		// "Passkey only" was turned on after the message was sent.
		return nil, contracts.ErrInvalidCode
	case doc != nil:
		return doc, nil
	case a.invitation == "":
		return nil, contracts.ErrInvalidCode
	}
	return s.signUp(ctx, a.address, a.invitation, stale)
}

// signUp makes the account of an address that proved to be the person's and
// has none. The invitation is used up here, by whoever deletes it first.
func (s *Service) signUp(ctx context.Context, address, invitation string, stale *store.Entry) (*accountDoc, error) {
	if err := s.cfg.Entries.DeleteEntry(ctx, invitation, invitationKey); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, contracts.ErrInvalidInvitation
		}
		return nil, err
	}
	return s.create(ctx, address, stale)
}

// LookEmail says which address the link of a message would sign in, and ends
// nothing. Whoever holds the link can sign in with it, so it tells them
// nothing they could not have. The page asks the person before it signs in:
// the link may come from someone else, who started a sign-in for their own
// account to have this person act inside it.
func (s *Service) LookEmail(_ context.Context, in contracts.EmailLook) (*contracts.EmailPending, error) {
	if in.Token == "" {
		// A code is typed where the sign-in was asked for; only a link travels.
		return nil, contracts.ErrInvalidCode
	}
	a, err := s.match(contracts.EmailFinish{Attempt: in.Attempt, Token: in.Token}, false)
	if err != nil {
		return nil, err
	}
	return &contracts.EmailPending{Address: a.address}, nil
}

// ---- sessions ----

// sessionDoc is a session as stored: signed, and with the hash of its secret
// only.
type sessionDoc struct {
	V           int       `json:"v"`
	Account     string    `json:"account"`
	ID          string    `json:"id"`
	TokenSHA256 []byte    `json:"token_sha256"`
	CreatedAt   time.Time `json:"created_at"`
	LastUsedAt  time.Time `json:"last_used_at"`
	Device      string    `json:"device"`
}

func (d *sessionDoc) expiry() time.Time { return domain.SessionExpiry(d.CreatedAt, d.LastUsedAt) }

func (d *sessionDoc) view(current string) contracts.Session {
	return contracts.Session{
		ID: d.ID, CreatedAt: d.CreatedAt, LastUsedAt: d.LastUsedAt, ExpiresAt: d.expiry(),
		Device: d.Device, Current: d.ID == current,
	}
}

func accountPartition(accountID string) string { return "account#" + accountID }

const sessionPrefix = "session#"

func (s *Service) putSession(ctx context.Context, doc *sessionDoc, version uint64) error {
	key, err := s.signingKey(ctx)
	if err != nil {
		return err
	}
	e := &store.Entry{
		Partition: accountPartition(doc.Account), Key: sessionPrefix + doc.ID,
		Version: version, ExpiresAt: doc.expiry(), UpdatedAt: s.now(),
	}
	raw, _ := json.Marshal(doc)
	if e.Value, err = envelope.Sign(key, raw, entryAAD(e.Partition, e.Key)); err != nil {
		return err
	}
	return s.cfg.Entries.PutEntry(ctx, e)
}

// readSession returns the session in an entry, or nil when the entry is not
// a session of that account that is still running.
func (s *Service) readSession(key []byte, e *store.Entry, accountID string) *sessionDoc {
	var doc sessionDoc
	raw, err := envelope.Verified(key, e.Value, entryAAD(e.Partition, e.Key))
	if err != nil || json.Unmarshal(raw, &doc) != nil {
		return nil
	}
	// The end comes from what was signed, not from the entry's own expiry.
	if doc.Account != accountID || sessionPrefix+doc.ID != e.Key || !doc.expiry().After(s.now()) {
		return nil
	}
	return &doc
}

func (s *Service) newSession(ctx context.Context, accountID, userAgent string) (*contracts.Session, string, error) {
	// One device more than an account keeps: the one used longest ago goes.
	if list, err := s.sessions(ctx, accountID); err != nil {
		return nil, "", err
	} else if len(list) >= maxSessions {
		oldest := slices.MinFunc(list, func(a, b sessionDoc) int { return a.LastUsedAt.Compare(b.LastUsedAt) })
		if err := s.cfg.Entries.DeleteEntry(ctx, accountPartition(accountID), sessionPrefix+oldest.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
			return nil, "", err
		}
	}
	now, secret := s.now(), newSecret()
	sum := sha256.Sum256([]byte(secret))
	doc := &sessionDoc{
		V: 1, Account: accountID, ID: newID("ses"), TokenSHA256: sum[:],
		CreatedAt: now, LastUsedAt: now, Device: domain.Device(userAgent),
	}
	if err := s.putSession(ctx, doc, 0); err != nil {
		return nil, "", err
	}
	view := doc.view(doc.ID)
	return &view, accountID + "." + doc.ID + "." + secret, nil
}

// Authenticated is the session a cookie stands for.
type Authenticated struct {
	AccountID string
	Session   contracts.Session
}

// Authenticate returns the session of a cookie's value, or ErrUnauthorized.
func (s *Service) Authenticate(ctx context.Context, cookie string) (*Authenticated, error) {
	parts := strings.Split(cookie, ".")
	if len(parts) != 3 || !validID("acc", parts[0]) || !validID("ses", parts[1]) {
		return nil, contracts.ErrUnauthorized
	}
	accountID, id, secret := parts[0], parts[1], parts[2]
	e, err := s.cfg.Entries.GetEntry(ctx, accountPartition(accountID), sessionPrefix+id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, contracts.ErrUnauthorized
	}
	if err != nil {
		return nil, err
	}
	key, err := s.signingKey(ctx)
	if err != nil {
		return nil, err
	}
	doc := s.readSession(key, e, accountID)
	sum := sha256.Sum256([]byte(secret))
	if doc == nil || subtle.ConstantTimeCompare(sum[:], doc.TokenSHA256) != 1 {
		return nil, contracts.ErrUnauthorized
	}
	// A session is only as good as its account: one that was removed, or that
	// is due and is removed here, signs nobody in, whatever entries of it are
	// put back in the storage.
	if _, err := s.own(ctx, &Authenticated{AccountID: accountID}); err != nil {
		return nil, err
	}
	if now := s.now(); now.Sub(doc.LastUsedAt) >= domain.SessionTouch {
		doc.LastUsedAt = now
		// Another request of the same session may have written it first.
		if err := s.putSession(ctx, doc, e.Version); err != nil && !errors.Is(err, store.ErrConflict) {
			return nil, err
		}
	}
	return &Authenticated{AccountID: accountID, Session: doc.view(id)}, nil
}

func (s *Service) sessions(ctx context.Context, accountID string) ([]sessionDoc, error) {
	entries, err := s.cfg.Entries.ListEntries(ctx, accountPartition(accountID), sessionPrefix, "", 2*maxSessions)
	if err != nil {
		return nil, err
	}
	key, err := s.signingKey(ctx)
	if err != nil {
		return nil, err
	}
	var out []sessionDoc
	for i := range entries {
		if doc := s.readSession(key, &entries[i], accountID); doc != nil {
			out = append(out, *doc)
		}
	}
	return out, nil
}

// Sessions lists the devices signed in to the account, the one last used first.
func (s *Service) Sessions(ctx context.Context, who *Authenticated) (*contracts.SessionList, error) {
	list, err := s.sessions(ctx, who.AccountID)
	if err != nil {
		return nil, err
	}
	slices.SortFunc(list, func(a, b sessionDoc) int { return b.LastUsedAt.Compare(a.LastUsedAt) })
	out := &contracts.SessionList{Sessions: []contracts.Session{}}
	for i := range list {
		out.Sessions = append(out.Sessions, list[i].view(who.Session.ID))
	}
	return out, nil
}

// CloseSession signs a device out of the account: its own, or another.
func (s *Service) CloseSession(ctx context.Context, who *Authenticated, id string) error {
	if !validID("ses", id) {
		return contracts.ErrNotFound
	}
	err := s.cfg.Entries.DeleteEntry(ctx, accountPartition(who.AccountID), sessionPrefix+id)
	if errors.Is(err, store.ErrNotFound) {
		return contracts.ErrNotFound
	}
	return err
}

// Me returns the account behind a session.
func (s *Service) Me(ctx context.Context, who *Authenticated) (*contracts.Me, error) {
	doc, err := s.own(ctx, who)
	if err != nil {
		return nil, err
	}
	return &contracts.Me{Account: doc.view(), Session: who.Session}, nil
}
