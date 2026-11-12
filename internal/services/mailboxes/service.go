package mailboxes

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/envelope"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/keys"
	"github.com/permaily-org/permaily/internal/store"
)

// Keys is what the service needs of the root secret: a random data key per
// mailbox, wrapped under it, and keys derived from it for one purpose
// (keys.Root; keys.Local in dev mode and tests).
type Keys interface {
	keys.Service
	Derive(ctx context.Context, purpose string) ([]byte, error)
}

// Storage is the table and the bucket: entries, a message's parts, the
// descending scan and deleting a message (store.Memory, store.AWS).
type Storage interface {
	store.Entries
	store.MessageParts
	store.Scanner
	ListMessages(ctx context.Context, mailboxID string, limit int, before string) ([]store.Message, error)
	DeleteMessage(ctx context.Context, mailboxID, id string) error
}

// Config is what a Service needs.
type Config struct {
	// Records is the strict anchored records of mailboxes and accounts'
	// lists (store.Anchored with Strict and OnlyNewest), used for those two
	// kinds of record and nothing else: a strict name is never read or
	// written through an Anchored that is not strict.
	Records  Records
	Storage  Storage
	Keys     Keys
	Accounts Accounts
	// Notifier tells a person by mail; nil tells nobody.
	Notifier Notifier
	// Auth checks a message's signatures at delivery; nil checks none.
	Auth inbox.Authenticator
	// Domain is the host mailboxes' addresses are at (inbox.permaily.com).
	Domain string
	Log    *slog.Logger
	// Now is the clock; time.Now when nil.
	Now func() time.Time
}

// Service is the mailboxes of accounts. It implements
// httpapi.MailboxesService, accounts.Mailboxes and smtpd.Recipients.
type Service struct {
	cfg    Config
	writer inbox.Writer
	kick   chan struct{}

	mu sync.Mutex
	// What this enclave counts in its memory (ADR 0026, "Limits"): mail
	// taken into each mailbox, and changes of each mailbox's settings.
	delivered map[string][]delivery
	settings  map[string][]time.Time
}

type delivery struct {
	at    time.Time
	bytes int
}

// New returns a Service.
func New(cfg Config) (*Service, error) {
	if cfg.Records == nil || cfg.Storage == nil || cfg.Keys == nil || cfg.Accounts == nil || cfg.Domain == "" {
		return nil, errors.New("mailboxes: records, storage, keys, accounts and a domain are needed")
	}
	if cfg.Log == nil {
		cfg.Log = slog.New(slog.DiscardHandler)
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Auth == nil {
		cfg.Auth = func(context.Context, []byte) []inbox.AuthResult { return nil }
	}
	cfg.Domain = strings.ToLower(cfg.Domain)
	s := &Service{cfg: cfg, kick: make(chan struct{}, 1), delivered: map[string][]delivery{}, settings: map[string][]time.Time{}}
	s.writer = inbox.Writer{Entries: cfg.Storage, Parts: cfg.Storage, Now: s.now, SeenFor: SeenFor, ClaimStale: ClaimStale}
	return s, nil
}

func (s *Service) now() time.Time { return s.cfg.Now().UTC() }

// The purposes of the keys derived from the root secret.
const (
	signPurpose    = "permaily mailboxes sign v1"
	addressPurpose = "permaily mailbox addresses v1"
)

// The purposes a mailbox's own key seals with.
const (
	privatePurpose = "mailbox-private"
	labelPurpose   = "mailbox-label"
)

// Where the entries of mailboxes are.
const (
	indexPartition      = "mailboxes"
	leavingPartition    = "mailboxes-disconnecting"
	pointerKey          = "mailbox"
	rcptPartitionPrefix = "rcpt#"
)

// maxTries is how many times a change starts over because another enclave
// wrote first (ADR 0026, "State machines").
const maxTries = 4

// Mailbox identifiers are mbx_ and 26 base32 characters (16 random bytes).
var idEncoding = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

func newMailboxID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b) // crypto/rand does not fail
	return "mbx_" + idEncoding.EncodeToString(b)
}

func validMailboxID(id string) bool {
	rest, ok := strings.CutPrefix(id, "mbx_")
	if !ok || len(rest) != 26 {
		return false
	}
	_, err := idEncoding.DecodeString(rest)
	return err == nil
}

// identifiers are taken out of what this package logs: logs leave the
// enclave. Its own errors hold none; a store's might.
var identifiers = regexp.MustCompile(`\b(acc|mbx)_[a-z2-7]{26}|\b[0-9a-f]{32}\b|\b[0-9a-f]{64}\b`)

func scrub(err error) string { return identifiers.ReplaceAllString(err.Error(), "…") }

func recordName(id string) string    { return "mailbox-" + id }
func listName(account string) string { return "mailboxes-" + account }

func keyContext(id string) map[string]string {
	return map[string]string{"purpose": "mailbox", "mailbox": id}
}

func (s *Service) signingKey(ctx context.Context) ([]byte, error) {
	k, err := s.cfg.Keys.Derive(ctx, signPurpose)
	if err != nil {
		return nil, fmt.Errorf("mailboxes: signing key: %w", err)
	}
	return k, nil
}

// rcptOf is the HMAC of an address's local part, hex: what the pointer is
// named by and the record and the list keep.
func (s *Service) rcptOf(ctx context.Context, local string) (string, error) {
	k, err := s.cfg.Keys.Derive(ctx, addressPurpose)
	if err != nil {
		return "", fmt.Errorf("mailboxes: address key: %w", err)
	}
	mac := hmac.New(sha256.New, k)
	mac.Write([]byte(local))
	return hex.EncodeToString(mac.Sum(nil)), nil
}

// marshal is the JSON of a value of this package's own types, which always
// have one (no channel, function or number that JSON cannot hold).
func marshal(v any) []byte {
	b, _ := json.Marshal(v)
	return b
}

// ---- the mailbox's record ----

// The states of a mailbox's record as stored.
const (
	stateActive        = "active"
	stateDisconnecting = "disconnecting"
	stateCancelled     = "cancelled"
)

// Who disconnected a mailbox.
const (
	byPerson         = "person"
	byAccountRemoved = "account_removed"
)

// mailboxDoc is a mailbox's record: readable and signed, with a part sealed
// under its own key.
type mailboxDoc struct {
	V              int         `json:"v"`
	ID             string      `json:"id"`
	Account        string      `json:"account"`
	State          string      `json:"state"`
	Rcpt           string      `json:"rcpt,omitempty"`
	CreatedAt      time.Time   `json:"created_at,omitzero"`
	ReceivingSince time.Time   `json:"receiving_since,omitzero"`
	VerifiedAt     time.Time   `json:"verified_at,omitzero"`
	PausedAt       time.Time   `json:"paused_at,omitzero"`
	StateChanges   []time.Time `json:"state_changes,omitempty"`
	DisconnectedAt time.Time   `json:"disconnected_at,omitzero"`
	DisconnectedBy string      `json:"disconnected_by,omitempty"`
	CancelledAt    time.Time   `json:"cancelled_at,omitzero"`
	// Sealed is privateDoc, sealed under the mailbox's key.
	Sealed []byte `json:"sealed,omitempty"`
}

// privateDoc is what nobody outside the enclave may read of a mailbox: its
// address and the Gmail account Google confirmed.
type privateDoc struct {
	Address string     `json:"address"`
	Source  *sourceDoc `json:"source,omitempty"`
}

type sourceDoc struct {
	Address    string    `json:"address"`
	VerifiedAt time.Time `json:"verified_at"`
}

// recState is what reading a mailbox's record found.
type recState int

const (
	// stNone: no commit of the record (a reservation, or never made), or it
	// was removed.
	stNone recState = iota
	stActive
	stDisconnecting
	stCancelled
	stLost
	stForked
)

func (r recState) leaving() bool    { return r == stDisconnecting || r == stCancelled }
func (r recState) unreadable() bool { return r == stLost || r == stForked }

// loaded is a mailbox's record as read.
type loaded struct {
	id    string
	state recState
	doc   *mailboxDoc
	rec   *store.Record
	at    store.Mark
}

// load reads a mailbox's record. A record that is lost, forked or not there
// is a state, not an error; an error is anything else (the store going
// back, a value that does not verify).
func (s *Service) load(ctx context.Context, id string) (*loaded, error) {
	l := &loaded{id: id}
	rec, at, err := s.cfg.Records.GetRecordAt(ctx, recordName(id))
	l.at = at
	switch {
	case errors.Is(err, store.ErrForked):
		l.state = stForked
		return l, nil
	case errors.Is(err, store.ErrLost):
		l.state = stLost
		return l, nil
	case errors.Is(err, store.ErrNotFound):
		return l, nil
	case err != nil:
		return nil, fmt.Errorf("mailboxes: a mailbox's record: %w", err)
	}
	key, err := s.signingKey(ctx)
	if err != nil {
		return nil, err
	}
	raw, err := envelope.Verified(key, rec.Value, envelope.AAD("record", recordName(id)))
	if err != nil {
		return nil, errors.New("mailboxes: a mailbox's record does not verify")
	}
	var doc mailboxDoc
	if err := json.Unmarshal(raw, &doc); err != nil || doc.ID != id {
		return nil, errors.New("mailboxes: a mailbox's record is not one")
	}
	l.doc, l.rec = &doc, rec
	switch doc.State {
	case stateActive:
		l.state = stActive
	case stateDisconnecting:
		l.state = stDisconnecting
	case stateCancelled:
		l.state = stCancelled
	default:
		return nil, errors.New("mailboxes: a mailbox's record in a state it cannot be in")
	}
	return l, nil
}

// signedRecord is a mailbox's record as stored, with wrapped as its key.
func (s *Service) signedRecord(ctx context.Context, doc *mailboxDoc, wrapped []byte) (*store.Record, error) {
	key, err := s.signingKey(ctx)
	if err != nil {
		return nil, err
	}
	doc.V = 1
	raw := marshal(doc)
	name := recordName(doc.ID)
	value, err := envelope.Sign(key, raw, envelope.AAD("record", name))
	if err != nil {
		return nil, err
	}
	return &store.Record{Name: name, WrappedKey: wrapped, Value: value, Plain: true, UpdatedAt: s.now()}, nil
}

// putAt writes a changed record at the mark it was read at, with its key.
func (s *Service) putAt(ctx context.Context, l *loaded, doc *mailboxDoc) error {
	rec, err := s.signedRecord(ctx, doc, l.rec.WrappedKey)
	if err != nil {
		return err
	}
	return s.cfg.Records.PutRecordAt(ctx, rec, l.at)
}

// tombstone fences a reservation: the record made cancelled, with no key,
// so the creator's own CreateNew fails and nothing ever makes it.
func (s *Service) tombstone(ctx context.Context, id, account string) error {
	rec, err := s.signedRecord(ctx, &mailboxDoc{ID: id, Account: account, State: stateCancelled, CancelledAt: s.now().Truncate(time.Second)}, nil)
	if err != nil {
		return err
	}
	return s.cfg.Records.CreateNew(ctx, rec)
}

// mailboxKey unwraps the mailbox's data key from the record just read. It is
// never kept beyond the request.
func (s *Service) mailboxKey(ctx context.Context, l *loaded) ([]byte, error) {
	if len(l.rec.WrappedKey) == 0 {
		return nil, errors.New("mailboxes: a mailbox's record without its key")
	}
	k, err := s.cfg.Keys.Unwrap(ctx, l.rec.WrappedKey, keyContext(l.id))
	if err != nil {
		return nil, fmt.Errorf("mailboxes: a mailbox's key: %w", err)
	}
	return k, nil
}

func (s *Service) private(key []byte, l *loaded) (*privateDoc, error) {
	plain, err := envelope.Open(key, l.doc.Sealed, envelope.AAD(privatePurpose, l.id))
	if err != nil {
		return nil, errors.New("mailboxes: a mailbox's sealed part does not open")
	}
	var p privateDoc
	if err := json.Unmarshal(plain, &p); err != nil {
		return nil, errors.New("mailboxes: a mailbox's sealed part is not one")
	}
	return &p, nil
}

func sealPrivate(key []byte, id string, p *privateDoc) ([]byte, error) {
	plain := marshal(p)
	return envelope.Seal(key, plain, envelope.AAD(privatePurpose, id))
}

// ---- an account's list ----

const (
	listOpen   = "open"
	listClosed = "closed"
)

type listDoc struct {
	V         int         `json:"v"`
	Account   string      `json:"account"`
	State     string      `json:"state"`
	Mailboxes []listEntry `json:"mailboxes"`
	Made      []time.Time `json:"made"`
}

type listEntry struct {
	ID      string    `json:"id"`
	Rcpt    string    `json:"rcpt"`
	AddedAt time.Time `json:"added_at"`
}

func (l *listDoc) find(id string) (listEntry, bool) {
	if l == nil {
		return listEntry{}, false
	}
	for _, e := range l.Mailboxes {
		if e.ID == id {
			return e, true
		}
	}
	return listEntry{}, false
}

func (l *listDoc) without(id string) []listEntry {
	out := make([]listEntry, 0, len(l.Mailboxes))
	for _, e := range l.Mailboxes {
		if e.ID != id {
			out = append(out, e)
		}
	}
	return out
}

// loadList reads an account's list: store.ErrNotFound when it has none.
func (s *Service) loadList(ctx context.Context, account string) (*listDoc, store.Mark, error) {
	name := listName(account)
	rec, at, err := s.cfg.Records.GetRecordAt(ctx, name)
	switch {
	case errors.Is(err, store.ErrLost):
		// Never taken for no list (which ErrLost would pass for): an account
		// whose list is gone is refused, not given an empty one.
		return nil, store.Mark{}, errors.New("mailboxes: an account's list is lost")
	case errors.Is(err, store.ErrNotFound):
		return nil, at, store.ErrNotFound
	case err != nil:
		return nil, store.Mark{}, fmt.Errorf("mailboxes: an account's list: %w", err)
	}
	key, err := s.signingKey(ctx)
	if err != nil {
		return nil, store.Mark{}, err
	}
	raw, err := envelope.Verified(key, rec.Value, envelope.AAD("record", name))
	if err != nil {
		return nil, store.Mark{}, errors.New("mailboxes: an account's list does not verify")
	}
	var doc listDoc
	if err := json.Unmarshal(raw, &doc); err != nil || doc.Account != account {
		return nil, store.Mark{}, errors.New("mailboxes: an account's list is not one")
	}
	return &doc, at, nil
}

func (s *Service) signedList(ctx context.Context, doc *listDoc) (*store.Record, error) {
	key, err := s.signingKey(ctx)
	if err != nil {
		return nil, err
	}
	doc.V = 1
	if doc.Mailboxes == nil {
		doc.Mailboxes = []listEntry{}
	}
	if doc.Made == nil {
		doc.Made = []time.Time{}
	}
	raw := marshal(doc)
	name := listName(doc.Account)
	value, err := envelope.Sign(key, raw, envelope.AAD("record", name))
	if err != nil {
		return nil, err
	}
	return &store.Record{Name: name, Value: value, Plain: true, UpdatedAt: s.now()}, nil
}

func (s *Service) putList(ctx context.Context, doc *listDoc, at store.Mark) error {
	rec, err := s.signedList(ctx, doc)
	if err != nil {
		return err
	}
	return s.cfg.Records.PutRecordAt(ctx, rec, at)
}

func (s *Service) createList(ctx context.Context, doc *listDoc) error {
	rec, err := s.signedList(ctx, doc)
	if err != nil {
		return err
	}
	return s.cfg.Records.CreateNew(ctx, rec)
}

// ---- entries ----

func entryAAD(partition, key string) []byte { return envelope.AAD("entry", partition, key) }

// putSigned writes a signed entry at version 0.
func (s *Service) putSigned(ctx context.Context, partition, key string, v any) error {
	sk, err := s.signingKey(ctx)
	if err != nil {
		return err
	}
	raw := marshal(v)
	value, err := envelope.Sign(sk, raw, entryAAD(partition, key))
	if err != nil {
		return err
	}
	return s.cfg.Storage.PutEntry(ctx, &store.Entry{Partition: partition, Key: key, Value: value, UpdatedAt: s.now()})
}

// readSigned reads what a signed entry says into v: false when it is not
// one this deployment wrote where it is.
func (s *Service) readSigned(ctx context.Context, e *store.Entry, v any) (bool, error) {
	sk, err := s.signingKey(ctx)
	if err != nil {
		return false, err
	}
	raw, err := envelope.Verified(sk, e.Value, entryAAD(e.Partition, e.Key))
	if err != nil {
		return false, nil
	}
	return json.Unmarshal(raw, v) == nil, nil
}

// pointerDoc is the pointer from an address: which mailbox, and nothing
// else. Write makes each write of one a value of its own.
type pointerDoc struct {
	Mailbox string `json:"mailbox"`
	Write   string `json:"write"`
}

func (s *Service) putPointer(ctx context.Context, rcpt, id string) error {
	w := make([]byte, 12)
	_, _ = rand.Read(w)
	return s.putSigned(ctx, rcptPartitionPrefix+rcpt, pointerKey, pointerDoc{Mailbox: id, Write: base64.RawURLEncoding.EncodeToString(w)})
}

// pointer reads the pointer of an address: the entry and the mailbox it
// names, "" when there is none or it is not one this deployment wrote.
func (s *Service) pointer(ctx context.Context, rcpt string) (*store.Entry, string, error) {
	e, err := s.cfg.Storage.GetEntry(ctx, rcptPartitionPrefix+rcpt, pointerKey)
	if errors.Is(err, store.ErrNotFound) {
		return nil, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	var p pointerDoc
	ok, err := s.readSigned(ctx, e, &p)
	if err != nil || !ok || !validMailboxID(p.Mailbox) {
		return nil, "", err
	}
	return e, p.Mailbox, nil
}

// unpoint deletes the pointer of an address, as it was read, when it names
// the mailbox: never moved or rewritten.
func (s *Service) unpoint(ctx context.Context, rcpt, id string) error {
	if rcpt == "" {
		return nil
	}
	e, named, err := s.pointer(ctx, rcpt)
	if err != nil || named != id {
		return err
	}
	if err := s.cfg.Storage.DeleteEntryAt(ctx, e); err != nil && !errors.Is(err, store.ErrConflict) {
		return err
	}
	return nil
}

// indexDoc is the entry that counts a mailbox for the operator: whose it is.
// It proves nothing beyond what it was signed with.
type indexDoc struct {
	Account string `json:"account"`
}

func (s *Service) indexed(ctx context.Context, id string) (string, error) {
	e, err := s.cfg.Storage.GetEntry(ctx, indexPartition, id)
	if errors.Is(err, store.ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	var d indexDoc
	if ok, err := s.readSigned(ctx, e, &d); err != nil || !ok {
		return "", err
	}
	return d.Account, nil
}

func (s *Service) unindex(ctx context.Context, id string) error {
	if err := s.cfg.Storage.DeleteEntry(ctx, indexPartition, id); err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	return nil
}

// leavingDoc is a mailbox being disconnected: the sweep goes through these.
// It proves nothing: the record does.
type leavingDoc struct {
	Mailbox string `json:"mailbox"`
}

func leavingKey(at time.Time, id string) string {
	return at.UTC().Format(time.RFC3339) + "#" + id
}

// queue writes the disconnect entry of a mailbox, create-only.
func (s *Service) queue(ctx context.Context, at time.Time, id string) error {
	err := s.putSigned(ctx, leavingPartition, leavingKey(at, id), leavingDoc{Mailbox: id})
	if err != nil && !errors.Is(err, store.ErrExists) {
		return err
	}
	return nil
}

// settingsDoc is a mailbox's settings: the retention and the label, sealed.
type settingsDoc struct {
	V             int    `json:"v"`
	RetentionDays int    `json:"retention_days"`
	Label         []byte `json:"label,omitempty"`
}

type settings struct {
	entry         *store.Entry
	retentionDays int
	label         string
}

// readSettings reads a mailbox's settings: 90 days and no label when there
// are none, or they are not this deployment's. key may be nil when the label
// is not needed.
func (s *Service) readSettings(ctx context.Context, id string, key []byte) (*settings, error) {
	out := &settings{retentionDays: contracts.MailboxRetentionDefault}
	e, err := s.cfg.Storage.GetEntry(ctx, inbox.Partition(id), inbox.KeySettings)
	if errors.Is(err, store.ErrNotFound) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	out.entry = e
	var d settingsDoc
	if ok, err := s.readSigned(ctx, e, &d); err != nil || !ok {
		return out, err
	}
	if store.ValidRetention(d.RetentionDays) {
		out.retentionDays = d.RetentionDays
	}
	if key != nil && len(d.Label) > 0 {
		if label, err := envelope.OpenText(key, d.Label, envelope.AAD(labelPurpose, id)); err == nil {
			out.label = string(label)
		}
	}
	return out, nil
}

// writeSettings writes a mailbox's settings at the version they were read at
// (0 for none).
func (s *Service) writeSettings(ctx context.Context, id string, key []byte, version uint64, retention int, label string) error {
	sk, err := s.signingKey(ctx)
	if err != nil {
		return err
	}
	d := settingsDoc{V: 1, RetentionDays: retention}
	if label != "" {
		if d.Label, err = envelope.SealText(key, []byte(label), envelope.AAD(labelPurpose, id)); err != nil {
			return err
		}
	}
	raw := marshal(d)
	partition := inbox.Partition(id)
	value, err := envelope.Sign(sk, raw, entryAAD(partition, inbox.KeySettings))
	if err != nil {
		return err
	}
	return s.cfg.Storage.PutEntry(ctx, &store.Entry{Partition: partition, Key: inbox.KeySettings, Value: value, Version: version, UpdatedAt: s.now()})
}

// notify tells the account's person, when there is a Notifier.
func (s *Service) notify(ctx context.Context, account string, kind NoticeKind, at time.Time) {
	if s.cfg.Notifier != nil {
		s.cfg.Notifier.MailboxNotice(ctx, account, kind, at)
	}
}

// wake asks the sweep to run soon (Run).
func (s *Service) wake() {
	select {
	case s.kick <- struct{}{}:
	default:
	}
}
