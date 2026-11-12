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
// by hand or moved to another account. A sign-in that was started, by mail,
// with a passkey or with a provider, is an entry that is sealed, works once
// and is ended through any enclave that started after it was made; its count
// of wrong answers is in each enclave's memory, where whoever runs the storage
// cannot set it back (pending.go).
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
// earlier copy of one, that writes one only if no other enclave changed it
// since it was read, and that removes one for good (store.Anchored).
type Records interface {
	store.Records
	// GetRecordAt reads a record and says where it was read, and PutRecordAt
	// stores it only if nothing was committed for it since: store.ErrChanged
	// otherwise, with the record as the other enclave left it.
	GetRecordAt(ctx context.Context, name string) (*store.Record, store.Mark, error)
	PutRecordAt(ctx context.Context, r *store.Record, at store.Mark) error
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
	// SuperAdmin is the contact address of the super admin's account, as
	// compiled into the image: whichever account has it is the super admin
	// (admins.go). Empty leaves the admin panel to nobody.
	SuperAdmin string
	Log        *slog.Logger
	Now        func() time.Time
	// Terms says the version of the terms in force: domain.TermsVersion when
	// nil, which is what the enclave runs with. Dev mode and the tests change
	// it, as a new release would.
	Terms func() string
}

// Limits on what one enclave keeps in memory and starts at once.
const (
	// maxRecoveryCounts is how many addresses one enclave counts wrong
	// recovery codes for.
	maxRecoveryCounts = 10000
	maxSending        = 32
	// maxNoticing is how many notices are being sent at once; one more is
	// dropped, and logged as that.
	maxNoticing = 32
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
	// noticeTimeout is the longest a notice by mail holds back the answer to
	// what caused it (notices.go).
	noticeTimeout = 5 * time.Second
)

// Service implements accounts and sign-in.
type Service struct {
	cfg Config
	rp  webauthn.RelyingParty

	mu sync.Mutex
	// marks is what this enclave remembers of the sign-ins in progress it was
	// asked to end, by their partition (pending.go).
	marks map[string]*mark
	// starts and wrong count, per address or invitation, the sign-ins started
	// in the last hour and the wrong codes of the last day; recoveries, per
	// address, the wrong recovery codes of the last hour.
	starts     map[string][]time.Time
	wrong      map[string][]time.Time
	recoveries map[string][]time.Time
	swept      time.Time
	// since is when this enclave started: it counts what the operator does
	// through it from then, and ends no sign-in made before (pending.go).
	// actions is how many of each kind the operator did (operator.go).
	since   time.Time
	actions map[string]int

	// changing is held while an account's record is read, changed and
	// written, and while an account is removed. It is not what keeps two
	// changes from undoing one another: the write is refused when the record
	// changed since it was read, whoever changed it (rewrite). It keeps the
	// requests of this enclave from making each other start over, and the
	// pointers that are written along with a record in one order here.
	changing sync.Mutex

	// The applications registered at the providers, as their sealed record
	// held them when it was read at appsRead (zero for never): read again once
	// that is appsFresh ago. A map that was handed out is never written to
	// again.
	appsMu   sync.Mutex
	apps     map[idp.Provider]appDoc
	appsRead time.Time

	sending chan struct{}
	// noticing bounds the notices being sent, and noticeWait is how long one
	// holds back the answer to what caused it.
	noticing   chan struct{}
	noticeWait time.Duration
	work       sync.WaitGroup
}

// attemptDoc is a sign-in by mail that was started and whose message was
// sent, as kept sealed until it ends (pending.go).
type attemptDoc struct {
	V       int    `json:"v"`
	Address string `json:"address"`
	// Account is whose contact address the attempt changes (address.go);
	// empty for a sign-in; confirmFor of a session for an attempt that
	// confirms that session (confirm.go). An attempt works for what it was
	// started for and for nothing else: one of an account signs nobody in,
	// one that confirms changes no address, and a sign-in does neither.
	Account string `json:"account,omitempty"`
	// Invitation is the entry of the invitation the sign-up will use, if the
	// address has no account when the sign-in ends, and Agreed the person's
	// agreement to the terms, given with it (terms.go). Both travel sealed
	// with the attempt, so the enclave that ends it makes the account as the
	// one that started it would have.
	Invitation string   `json:"invitation,omitempty"`
	Agreed     termsDoc `json:"agreed,omitzero"`
	// Code and Token are the hashes of the code and of the link's token; Code
	// is empty when the message carried only the link, and Token when it
	// carried only the code.
	Code  []byte `json:"code,omitempty"`
	Token []byte `json:"token,omitempty"`
	pendingTimes
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
	if cfg.Terms == nil {
		cfg.Terms = func() string { return domain.TermsVersion }
	}
	if cfg.SuperAdmin != "" {
		address, err := domain.NormalizeAddress(cfg.SuperAdmin)
		if err != nil {
			return nil, errors.New("accounts: the super admin's address is not one")
		}
		cfg.SuperAdmin = address
	}
	app, err := url.Parse(cfg.AppURL)
	if err != nil || app.Hostname() == "" {
		return nil, errors.New("accounts: the app's URL has no host")
	}
	return &Service{
		cfg: cfg, since: cfg.Now().UTC(), actions: map[string]int{},
		noticing: make(chan struct{}, maxNoticing), noticeWait: noticeTimeout,
		// The port is part of the origin and not of who the passkeys are for.
		rp:         webauthn.RelyingParty{ID: app.Hostname(), Origins: []string{app.Scheme + "://" + app.Host}},
		marks:      map[string]*mark{},
		starts:     map[string][]time.Time{},
		wrong:      map[string][]time.Time{},
		recoveries: map[string][]time.Time{},
		sending:    make(chan struct{}, maxSending),
	}, nil
}

// Wait returns when the messages being sent are sent.
func (s *Service) Wait() { s.work.Wait() }

func (s *Service) now() time.Time { return s.cfg.Now().UTC() }

// terms is the version of the terms in force.
func (s *Service) terms() string { return s.cfg.Terms() }

// Identifiers are random and say what they name: acc_…, ses_…, att_…, cer_…, inv_….
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
var identifiers = regexp.MustCompile(`\b(acc|ses|att|cer|inv)_[a-z2-7]{26}`)

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
	// ClosesAt is when a closing account is removed, and ClosingBy who asked
	// for that: contracts.ClosingByOperator, or the person (also when empty,
	// as in the records written before the operator could ask).
	ClosesAt  time.Time `json:"closes_at,omitzero"`
	ClosingBy string    `json:"closing_by,omitempty"`
	// BlockedAt is when the operator blocked the account. While it is set
	// nobody signs in to the account and no session of it stands for anything.
	BlockedAt time.Time `json:"blocked_at,omitzero"`
	// MovingTo is the contact address the account is changing to, between the
	// record saying so and the record saying it is done, and MovingClaim names
	// the change that set it (address.go). The pointer from that address is
	// written in between, and leads here from then on.
	MovingTo    string `json:"moving_to,omitempty"`
	MovingClaim string `json:"moving_claim,omitempty"`
	// Admin says the super admin made the account an admin, who and when
	// (admins.go). Nothing else makes one but the super admin's address.
	Admin *adminDoc `json:"admin,omitempty"`
	// Terms is the version of the terms its person agreed to, and when
	// (terms.go); nil for the accounts made before there were terms.
	Terms *termsDoc `json:"terms,omitempty"`
	// Linking is a provider account being linked, until the record lists it
	// (providers.go). It signs nobody in.
	Linking *linkingDoc `json:"linking,omitempty"`
	// Removing says an enclave is removing the account: written before
	// anything of it is destroyed, it is never taken back, so a cancel that
	// comes after it cannot be answered as kept (settings.go).
	Removing bool `json:"removing,omitempty"`
	// SignOuts counts the times every device was signed out, and KeptSession
	// is the one session the last of them kept. A session stands only when it
	// was made at the count the record has, or is the one kept: so a session
	// made through another enclave in the moment the devices are signed out
	// stands for nothing (stands).
	SignOuts    int    `json:"sign_outs,omitempty"`
	KeptSession string `json:"kept_session,omitempty"`
}

func (d *accountDoc) view() contracts.Account {
	return contracts.Account{
		ID: d.ID, State: d.State, Address: d.Address, Name: d.Name, Language: d.Language,
		CreatedAt: d.CreatedAt, ClosesAt: d.ClosesAt, ClosingBy: d.closingBy(), PasskeyOnly: d.PasskeyOnly,
		Terms: d.agreedVersion(),
	}
}

// closingBy says who asked to delete a closing account, "" for one that is not.
func (d *accountDoc) closingBy() string {
	switch {
	case d.State != contracts.AccountClosing:
		return ""
	case d.ClosingBy == contracts.ClosingByOperator:
		return contracts.ClosingByOperator
	}
	return contracts.ClosingByPerson
}

// blocked reports whether the operator blocked the account.
func (d *accountDoc) blocked() bool { return !d.BlockedAt.IsZero() }

// has reports whether address is the account's: its contact address, or the
// one it is changing to, which its person proved to be theirs already.
func (d *accountDoc) has(address string) bool {
	return d.Address == address || (d.MovingTo != "" && d.MovingTo == address)
}

// due reports whether the account's person asked to delete it and the days
// they had to cancel are over: from then on it is nobody's account.
func (d *accountDoc) due(now time.Time) bool {
	return d.Removing || (d.State == contracts.AccountClosing && !now.Before(d.ClosesAt))
}

// signOut signs every device of the account out, but the session keep (""
// for none): from this record on, no session made before it stands.
func (d *accountDoc) signOut(keep string) {
	d.SignOuts++
	d.KeptSession = keep
}

// stands reports whether a session of the account still stands: it was not
// made before the devices were last signed out, or that kept it.
func (d *accountDoc) stands(session *sessionDoc) bool {
	return session.SignOuts == d.SignOuts || (d.KeptSession != "" && d.KeptSession == session.ID)
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
	return s.opened(ctx, id, rec, err)
}

// readAt is read that also says where the record was read, for a change
// that is written only if nothing changed since (rewrite).
func (s *Service) readAt(ctx context.Context, id string) (*accountDoc, store.Mark, error) {
	rec, at, err := s.cfg.Records.GetRecordAt(ctx, recordName(id))
	doc, err := s.opened(ctx, id, rec, err)
	return doc, at, err
}

// opened is the account in a record that was read, as read says.
func (s *Service) opened(ctx context.Context, id string, rec *store.Record, err error) (*accountDoc, error) {
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

// signed is an account's record as it is stored.
func (s *Service) signed(ctx context.Context, doc *accountDoc) (*store.Record, error) {
	key, err := s.signingKey(ctx)
	if err != nil {
		return nil, err
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	name := recordName(doc.ID)
	value, err := envelope.Sign(key, raw, recordAAD(name))
	if err != nil {
		return nil, err
	}
	return &store.Record{Name: name, Value: value, Plain: true, UpdatedAt: s.now()}, nil
}

// makeAccount stores the record of a new account: one that is there already
// is store.ErrExists.
func (s *Service) makeAccount(ctx context.Context, doc *accountDoc) error {
	rec, err := s.signed(ctx, doc)
	if err != nil {
		return err
	}
	return s.cfg.Records.PutRecord(ctx, rec, true)
}

// putAccountAt stores an account's record that was read at a mark, if it did
// not change since: store.ErrChanged when it did, or was removed.
func (s *Service) putAccountAt(ctx context.Context, doc *accountDoc, at store.Mark) error {
	rec, err := s.signed(ctx, doc)
	if err != nil {
		return err
	}
	return s.cfg.Records.PutRecordAt(ctx, rec, at)
}

// own returns the account behind a session. A session of an account that
// the operator blocked stands for nothing, like one of no account.
func (s *Service) own(ctx context.Context, who *Authenticated) (*accountDoc, error) {
	doc, err := s.account(ctx, who.AccountID)
	if err != nil {
		return nil, err
	}
	if doc == nil || doc.blocked() {
		return nil, contracts.ErrUnauthorized
	}
	return doc, nil
}

// change is rewrite for what a person changes of their own account. A
// blocked account takes none of it, as if its session were not there, a
// closing one refuses everything but cancelling, and a recovery code used to
// sign in, and one whose person did not agree to the terms in force refuses
// everything until they do (terms.go).
func (s *Service) change(ctx context.Context, id string, edit func(*accountDoc) error) (*accountDoc, error) {
	return s.changeAgreed(ctx, id, true, edit)
}

// changeAgreed is change, asking for the agreement to the terms or not: not
// for agreeing itself, nor for what a sign-in writes (link).
func (s *Service) changeAgreed(ctx context.Context, id string, terms bool, edit func(*accountDoc) error) (*accountDoc, error) {
	return s.rewrite(ctx, id, func(doc *accountDoc) error {
		if doc.blocked() {
			return contracts.ErrUnauthorized
		}
		if doc.State == contracts.AccountClosing {
			return contracts.ErrAccountClosing
		}
		if terms && !doc.agreed(s.terms()) {
			return contracts.ErrTermsChanged
		}
		return edit(doc)
	})
}

// maxRewrites is how many times a change of an account's record is tried
// when other enclaves keep changing the record under it.
const maxRewrites = 4

// rewrite reads an account's record, lets edit change it and stores it as the
// newest version. When edit fails nothing is stored. An account that is not
// there, or is due and so removed here, is ErrUnauthorized: its session
// stands for nothing.
//
// The record is stored only if nothing was committed for it since it was
// read (store.ErrChanged): another enclave of the deployment may have changed
// or removed it in between. Then it is read again and edit runs again, on
// what is there now, so no change undoes another and none brings a removed
// account back. So edit can run more than once: it decides everything from
// the record it is given, sets what it hands to its caller on every run, and
// whatever else it writes it can write again. After maxRewrites tries the
// change is given up, as an error.
func (s *Service) rewrite(ctx context.Context, id string, edit func(*accountDoc) error) (*accountDoc, error) {
	s.changing.Lock()
	defer s.changing.Unlock()
	for range maxRewrites {
		doc, at, err := s.readAt(ctx, id)
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
		switch err := s.putAccountAt(ctx, doc, at); {
		case err == nil:
			return doc, nil
		case !errors.Is(err, store.ErrChanged):
			return nil, err
		}
	}
	return nil, errors.New("accounts: an account's record keeps changing")
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
	// Write makes each write of an entry a value of its own, so that one that
	// is deleted as it was read (DeleteEntryAt) is that write and no later
	// one: versions start over when an entry is deleted and made again.
	Write string `json:"write,omitempty"`
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
	raw, _ := json.Marshal(loginDoc{Account: accountID, Write: newWrite()})
	if e.Value, err = envelope.Sign(signing, raw, entryAAD(partition, key)); err != nil {
		return err
	}
	return s.cfg.Entries.PutEntry(ctx, e)
}

// newWrite names one write of an entry (loginDoc.Write).
func newWrite() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// repoint writes the pointer of a partition to an account in the place of
// the one that was read (nil for none): that one is deleted as it was read,
// and the new one is written where none is. Writing at the version that was
// read would not do: a version repeats once an entry is deleted and made
// again, and the write would land on the pointer another account just made.
// store.ErrConflict or store.ErrExists says another writer came in between.
func (s *Service) repoint(ctx context.Context, partition, accountID string, old *store.Entry) error {
	if old != nil {
		if err := s.cfg.Entries.DeleteEntryAt(ctx, old); err != nil {
			return err
		}
	}
	return s.point(ctx, partition, loginKey, accountID, 0)
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
	return s.follow(ctx, loginPartition(address), func(doc *accountDoc) bool { return doc.has(address) })
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

// create makes the account of address, which just proved to be the person's,
// with the agreement to the terms they gave. stale is the pointer that led
// nowhere, if there was one. It reports whether the account it returns was
// made here: not when another sign-up of the same address ended first.
func (s *Service) create(ctx context.Context, address string, agreed termsDoc, stale *store.Entry) (*accountDoc, bool, error) {
	now := s.now()
	doc := &accountDoc{
		V: 1, ID: newID("acc"), State: contracts.AccountActive, Address: address,
		Language: "en", CreatedAt: now, AddressVerifiedAt: now, Terms: &agreed,
	}
	// The entry in the list of accounts goes in before the record: one without
	// a record lists nothing, where an account without one would never be shown.
	if err := s.point(ctx, accountsPartition, listKey(doc), doc.ID, 0); err != nil {
		return nil, false, err
	}
	if err := s.makeAccount(ctx, doc); err != nil {
		_ = s.forget(ctx, accountsPartition, listKey(doc))
		return nil, false, err
	}
	err := s.repoint(ctx, loginPartition(address), doc.ID, stale)
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
			return nil, false, err
		}
		if winner == nil {
			return nil, false, errors.New("accounts: the pointer of an address keeps changing")
		}
		return winner, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	s.cfg.Log.Info("accounts: account created")
	return doc, true, nil
}

// ---- invitations ----

type invitationDoc struct {
	V int `json:"v"`
	// ID names the invitation for the operator (operator.go). It is random:
	// the code cannot be had from it. The invitations made before there was a
	// list have none.
	ID        string    `json:"id,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

func invitationPartition(code string) string {
	sum := sha256.Sum256([]byte("permaily invitation v1\x00" + code))
	return "invitation#" + hex.EncodeToString(sum[:])
}

const invitationKey = "invitation"

// CreateInvitations makes single-use sign-up codes. They are returned once:
// only their hash is kept. Each is also listed for the operator under an
// identifier of its own, which says nothing of the code (operator.go).
func (s *Service) CreateInvitations(ctx context.Context, count int) (*contracts.Invitations, error) {
	return s.createInvitations(ctx, nil, count)
}

func (s *Service) createInvitations(ctx context.Context, by *acting, count int) (*contracts.Invitations, error) {
	if err := by.changing(); err != nil {
		return nil, err
	}
	if count < 1 || count > MaxInvitations {
		return nil, &contracts.Error{Code: "invalid_count", Message: fmt.Sprintf("count must be 1-%d", MaxInvitations)}
	}
	key, err := s.signingKey(ctx)
	if err != nil {
		return nil, err
	}
	now := s.now()
	out := &contracts.Invitations{ExpiresAt: now.Add(domain.InvitationLifetime)}
	// The ones made before a failure are made all the same.
	defer func() {
		s.acted(ActionInvitationsMade, len(out.Invitations))
		for _, id := range out.IDs {
			s.logged(ctx, by, logDoc{Kind: ActionInvitationsMade, Invitation: id})
		}
	}()
	for range count {
		code, id := randomString(domain.InvitationAlphabet, domain.InvitationLength), newID("inv")
		partition := invitationPartition(code)
		// The entry of the list goes in first: one without an invitation lists
		// nothing, where an invitation without one could never be revoked.
		listed, _ := json.Marshal(invitedDoc{V: 1, ID: id, Invitation: partition, CreatedAt: now})
		raw, _ := json.Marshal(invitationDoc{V: 1, ID: id, CreatedAt: now})
		for _, e := range []*store.Entry{
			{Partition: invitationsPartition, Key: id, Value: listed},
			{Partition: partition, Key: invitationKey, Value: raw},
		} {
			e.ExpiresAt, e.UpdatedAt = out.ExpiresAt, now
			if e.Value, err = envelope.Sign(key, e.Value, entryAAD(e.Partition, e.Key)); err != nil {
				return nil, err
			}
			if err := s.cfg.Entries.PutEntry(ctx, e); err != nil {
				return nil, err
			}
		}
		out.Invitations, out.IDs = append(out.Invitations, domain.FormatInvitation(code)), append(out.IDs, id)
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
	s.sweepMarks(now)
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
	// An invitation makes an account, which takes the agreement to the terms
	// in force: asked of the request, and so of no address.
	agreed, err := s.agreement(invitation, in.Terms)
	if err != nil {
		return nil, err
	}
	return s.startEmail(ctx, address, invitation, agreed)
}

// startEmail starts a sign-in by email for an address as accounts are found
// by, with the entry of an invitation that was checked, if there is one, and
// the agreement to the terms given with it.
func (s *Service) startEmail(ctx context.Context, address, invitation string, agreed termsDoc) (*contracts.EmailStarted, error) {
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
			s.deliver(ctx, id, address, invitation, agreed)
		}()
	default:
		s.cfg.Log.Warn("accounts: too many sign-in messages being sent, one was dropped")
	}
	return &contracts.EmailStarted{Attempt: id, ExpiresIn: int(domain.EmailCodeLifetime / time.Second)}, nil
}

// deliver sends the message of a sign-in, if one is due.
func (s *Service) deliver(ctx context.Context, id, address, invitation string, agreed termsDoc) {
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
	code, token := "", newSecret()
	a := &attemptDoc{V: 1, Address: address, Invitation: invitation, Agreed: agreed, pendingTimes: pendingTimes{Made: now, Expires: now.Add(domain.EmailCodeLifetime)}}
	sum := sha256.Sum256([]byte(token))
	a.Token = sum[:]
	s.mu.Lock()
	if s.wrong[address] = recent(s.wrong[address], now, 24*time.Hour); len(s.wrong[address]) < wrongCodesPerDay {
		code = randomString("0123456789", domain.EmailCodeDigits)
		sum := sha256.Sum256([]byte(code))
		a.Code = sum[:]
	}
	s.mu.Unlock()
	s.send(ctx, id, a, signInMail(address, s.cfg.AppURL, id, code, token), "accounts: a sign-in message could not be sent")
}

// send keeps an attempt and sends its message. One whose message could not be
// sent is dropped: nobody has its code.
func (s *Service) send(ctx context.Context, id string, a *attemptDoc, m Mail, failed string) {
	partition := pendingPartition(kindAttempt, id)
	if err := s.stash(ctx, partition, a); err != nil {
		s.cfg.Log.Error("accounts: a sign-in in progress could not be kept", "err", scrub(err))
		return
	}
	if err := s.cfg.Mailer.Send(ctx, m); err != nil {
		// Not the error: a mail system's errors carry the address.
		s.cfg.Log.Warn(failed)
		_ = s.forget(ctx, partition, pendingKey)
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
func (s *Service) take(ctx context.Context, in contracts.EmailFinish) (*attemptDoc, error) {
	return s.match(ctx, in, "", true)
}

// match finds the attempt that the code or the token of in belongs to, among
// the sign-ins (account "") or among the changes of that account's address.
// With use it ends it: an attempt works once, through whichever enclave ends
// it first. Without, it is only looked at, and a wrong token counts against it
// all the same. The wrong answers are counted in this enclave's memory, by the
// attempt, for as long as it could be ended (pending.go).
func (s *Service) match(ctx context.Context, in contracts.EmailFinish, account string, use bool) (*attemptDoc, error) {
	if !validID("att", in.Attempt) {
		return nil, contracts.ErrInvalidCode
	}
	var a attemptDoc
	e, err := s.unstash(ctx, pendingPartition(kindAttempt, in.Attempt), &a)
	if err != nil {
		return nil, err
	}
	if e == nil || a.Account != account {
		// Not there, or started for something else or for another account:
		// here it is no attempt at all, and nothing of it changes.
		return nil, contracts.ErrInvalidCode
	}
	ok, byCode := false, false
	switch {
	case in.Token != "":
		sum := sha256.Sum256([]byte(in.Token))
		ok = len(a.Token) > 0 && subtle.ConstantTimeCompare(sum[:], a.Token) == 1
	case len(a.Code) > 0:
		sum := sha256.Sum256([]byte(in.Code))
		ok, byCode = subtle.ConstantTimeCompare(sum[:], a.Code) == 1, true
	}
	now := s.now()
	s.mu.Lock()
	m := s.marked(e.Partition, a.Expires, now)
	switch {
	case m == nil || m.used || m.tries >= domain.EmailCodeTries:
		s.mu.Unlock()
		return nil, contracts.ErrInvalidCode
	case !ok:
		if byCode {
			s.wrong[a.Address] = append(recent(s.wrong[a.Address], now, 24*time.Hour), now)
		}
		m.tries++
		spent := m.tries >= domain.EmailCodeTries
		s.mu.Unlock()
		if spent {
			// Out of tries here: it goes from the storage too, as it was read.
			if _, err := s.drop(ctx, e); err != nil {
				s.cfg.Log.Warn("accounts: a sign-in that ran out of tries could not be deleted", "err", scrub(err))
			}
		}
		return nil, contracts.ErrInvalidCode
	case use:
		m.used = true // it works once
	}
	s.mu.Unlock()
	if use {
		used, err := s.use(ctx, e, a.Expires)
		if err != nil {
			return nil, err
		}
		if !used {
			return nil, contracts.ErrInvalidCode // another enclave ended it first
		}
	}
	return &a, nil
}

// FinishEmail ends a sign-in by email: it signs the device in, to the
// address's account or, with an invitation, to a new one. It returns the
// account and the value of the session's cookie.
func (s *Service) FinishEmail(ctx context.Context, in contracts.EmailFinish, from Browser) (*contracts.Me, string, error) {
	doc, made, err := s.proved(ctx, in)
	if err != nil {
		return nil, "", err
	}
	return s.enter(ctx, doc, wayMail, from, made)
}

// proved ends a sign-in by email with the code or the token of its message,
// and returns the account of the address that so proved to be the person's:
// the one it has or, with an invitation, a new one, and whether it was made
// here. An account the operator blocked is ErrAccountBlocked: said here, to
// whoever proved the address, and never before.
func (s *Service) proved(ctx context.Context, in contracts.EmailFinish) (*accountDoc, bool, error) {
	a, err := s.take(ctx, in)
	if err != nil {
		return nil, false, err
	}
	doc, stale, err := s.lookup(ctx, a.Address)
	switch {
	case err != nil:
		return nil, false, err
	case doc != nil && doc.PasskeyOnly:
		// "Passkey only" was turned on after the message was sent.
		return nil, false, contracts.ErrInvalidCode
	case doc != nil && doc.blocked():
		return nil, false, contracts.ErrAccountBlocked
	case doc != nil:
		return doc, false, nil
	case a.Invitation == "":
		return nil, false, contracts.ErrInvalidCode
	}
	return s.signUp(ctx, a.Address, a.Invitation, a.Agreed, stale)
}

// signUp makes the account of an address that proved to be the person's and
// has none, with the agreement to the terms given at the start. The terms may
// have changed since (a new release): then the person agreed to a text that
// is not in force, and nothing is made. The invitation is used up here, by
// whoever deletes it first.
func (s *Service) signUp(ctx context.Context, address, invitation string, agreed termsDoc, stale *store.Entry) (*accountDoc, bool, error) {
	if agreed.Version != s.terms() {
		return nil, false, contracts.ErrTermsNeeded
	}
	if err := s.cfg.Entries.DeleteEntry(ctx, invitation, invitationKey); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, false, contracts.ErrInvalidInvitation
		}
		return nil, false, err
	}
	return s.create(ctx, address, agreed, stale)
}

// LookEmail says which address the link of a message would sign in, and ends
// nothing. Whoever holds the link can sign in with it, so it tells them
// nothing they could not have. The page asks the person before it signs in:
// the link may come from someone else, who started a sign-in for their own
// account to have this person act inside it.
func (s *Service) LookEmail(ctx context.Context, in contracts.EmailLook) (*contracts.EmailPending, error) {
	if in.Token == "" {
		// A code is typed where the sign-in was asked for; only a link travels.
		return nil, contracts.ErrInvalidCode
	}
	a, err := s.match(ctx, contracts.EmailFinish{Attempt: in.Attempt, Token: in.Token}, "", false)
	if err != nil {
		return nil, err
	}
	return &contracts.EmailPending{Address: a.Address}, nil
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
	// ConfirmedAt is when the person last proved who they are with this
	// session: when it was made, by signing in, or when they confirmed it is
	// them again (confirm.go). It is signed with the rest, so whoever writes to
	// the storage cannot make a session fresh, and it is in the storage, so a
	// request that lands on another enclave sees it.
	ConfirmedAt time.Time `json:"confirmed_at,omitzero"`
	// SignOuts is how many times every device of the account had been signed
	// out when the session was made: it stands while the record says the
	// same (accountDoc.stands).
	SignOuts int `json:"sign_outs,omitempty"`
}

func (d *sessionDoc) expiry() time.Time { return domain.SessionExpiry(d.CreatedAt, d.LastUsedAt) }

// view is the session as its person sees it at now. Only the session that
// asks says until when its proof is fresh, and only while it is.
func (d *sessionDoc) view(current string, now time.Time) contracts.Session {
	out := contracts.Session{
		ID: d.ID, CreatedAt: d.CreatedAt, LastUsedAt: d.LastUsedAt, ExpiresAt: d.expiry(),
		Device: d.Device, Current: d.ID == current,
	}
	if until := domain.ConfirmedUntil(d.ConfirmedAt); out.Current && now.Before(until) {
		out.ConfirmedUntil = until
	}
	return out
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

func (s *Service) newSession(ctx context.Context, account *accountDoc, userAgent string) (*contracts.Session, string, error) {
	accountID := account.ID
	// One device more than an account keeps: the one used longest ago goes.
	// More than one goes when there are more than it keeps: sign-ins that
	// ended at the same time, here or in another enclave, each saw room for
	// theirs.
	list, err := s.sessions(ctx, accountID, nil)
	if err != nil {
		return nil, "", err
	}
	slices.SortFunc(list, func(a, b sessionDoc) int { return a.LastUsedAt.Compare(b.LastUsedAt) })
	for i := 0; i <= len(list)-maxSessions; i++ {
		if err := s.forget(ctx, accountPartition(accountID), sessionPrefix+list[i].ID); err != nil {
			return nil, "", err
		}
	}
	now, secret := s.now(), newSecret()
	sum := sha256.Sum256([]byte(secret))
	doc := &sessionDoc{
		V: 1, Account: accountID, ID: newID("ses"), TokenSHA256: sum[:],
		CreatedAt: now, LastUsedAt: now, Device: domain.Device(userAgent),
		// Signing in is a proof of who the person is.
		ConfirmedAt: now,
		// As the record was read when the person proved who they are: if every
		// device was signed out since, this one was too.
		SignOuts: account.SignOuts,
	}
	if err := s.putSession(ctx, doc, 0); err != nil {
		return nil, "", err
	}
	view := doc.view(doc.ID, now)
	return &view, accountID + "." + doc.ID + "." + secret, nil
}

// Authenticated is the session a cookie stands for.
type Authenticated struct {
	AccountID string
	Session   contracts.Session
	// signOuts is the session's own count of the times every device was
	// signed out (sessionDoc.SignOuts).
	signOuts int
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
	// put back in the storage; and one made before every device was signed
	// out stands for nothing, also when it was written after.
	account, err := s.own(ctx, &Authenticated{AccountID: accountID})
	if err != nil {
		return nil, err
	}
	if !account.stands(doc) {
		return nil, contracts.ErrUnauthorized
	}
	if now := s.now(); now.Sub(doc.LastUsedAt) >= domain.SessionTouch {
		doc.LastUsedAt = now
		// Another request of the same session may have written it first.
		if err := s.putSession(ctx, doc, e.Version); err != nil && !errors.Is(err, store.ErrConflict) {
			return nil, err
		}
	}
	return &Authenticated{AccountID: accountID, Session: doc.view(id, s.now()), signOuts: doc.SignOuts}, nil
}

// sessions lists the sessions of an account that are still running; with
// the account's record, only the ones that stand.
func (s *Service) sessions(ctx context.Context, accountID string, account *accountDoc) ([]sessionDoc, error) {
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
		if doc := s.readSession(key, &entries[i], accountID); doc != nil && (account == nil || account.stands(doc)) {
			out = append(out, *doc)
		}
	}
	return out, nil
}

// Sessions lists the devices signed in to the account, the one last used first.
func (s *Service) Sessions(ctx context.Context, who *Authenticated) (*contracts.SessionList, error) {
	account, err := s.own(ctx, who)
	if err != nil {
		return nil, err
	}
	list, err := s.sessions(ctx, who.AccountID, account)
	if err != nil {
		return nil, err
	}
	slices.SortFunc(list, func(a, b sessionDoc) int { return b.LastUsedAt.Compare(a.LastUsedAt) })
	out, now := &contracts.SessionList{Sessions: []contracts.Session{}}, s.now()
	for i := range list {
		out.Sessions = append(out.Sessions, list[i].view(who.Session.ID, now))
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
	return &contracts.Me{Account: s.view(doc), Session: who.Session, Terms: s.terms()}, nil
}

// view is the account as its person sees it, with what it is in the admin
// panel, worked out from the record each time (admins.go).
func (s *Service) view(doc *accountDoc) contracts.Account {
	out := doc.view()
	out.Admin = s.role(doc)
	return out
}
