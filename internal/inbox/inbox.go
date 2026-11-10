// Package inbox receives messages for a mailbox, encrypts them with the
// mailbox's data key and stores them, and serves them back to the mailbox
// owner. Everything here runs inside the enclave; the store only ever sees
// sealed bytes plus ids, sizes and timestamps.
package inbox

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/permaily-org/permaily/internal/envelope"
	"github.com/permaily-org/permaily/internal/keys"
	"github.com/permaily-org/permaily/internal/store"
)

// ErrUnknownRecipient is returned for a recipient that is not this mailbox.
var ErrUnknownRecipient = errors.New("inbox: unknown recipient")

// MaxMessageBytes is the largest message accepted (SMTP SIZE).
const MaxMessageBytes = 25 << 20

// AuthResult is the outcome of one sender authentication check.
type AuthResult struct {
	Method string `json:"method"` // "dkim"
	Domain string `json:"domain"`
	Result string `json:"result"` // "pass" or "fail: <reason>"
}

// Authenticator checks a raw message's signatures (DKIM).
type Authenticator func(ctx context.Context, raw []byte) []AuthResult

// Config describes the single mailbox this enclave serves (F1).
type Config struct {
	MailboxID string
	Address   string
}

// Service is the mailbox. Create it with New and call Ensure once at startup.
type Service struct {
	cfg   Config
	store store.Store
	keys  keys.Service
	auth  Authenticator
	now   func() time.Time
	log   *slog.Logger

	mu  sync.Mutex
	key []byte // unwrapped data key, cached for the enclave lifetime

	stateMu sync.Mutex // serializes read-modify-write of the mailbox record
}

// New returns a Service. auth may be nil (no signature checks).
func New(cfg Config, s store.Store, k keys.Service, auth Authenticator, log *slog.Logger) *Service {
	if auth == nil {
		auth = func(context.Context, []byte) []AuthResult { return nil }
	}
	return &Service{cfg: cfg, store: s, keys: k, auth: auth, now: time.Now, log: log}
}

// SetClock overrides the clock (tests).
func (s *Service) SetClock(now func() time.Time) { s.now = now }

// Address is the mailbox's email address.
func (s *Service) Address() string { return s.cfg.Address }

// Accepts reports whether rcpt is this mailbox (case-insensitive, as mail
// systems treat the domain and, in practice, the local part).
func (s *Service) Accepts(rcpt string) bool {
	return strings.EqualFold(strings.Trim(rcpt, "<> "), s.cfg.Address)
}

func (s *Service) keyContext() map[string]string {
	return map[string]string{"purpose": "mailbox", "mailbox": s.cfg.MailboxID}
}

// Ensure creates the mailbox (and its data key) if it does not exist, and
// loads the data key.
func (s *Service) Ensure(ctx context.Context) error {
	_, err := s.dataKey(ctx)
	return err
}

func (s *Service) dataKey(ctx context.Context) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.key != nil {
		return s.key, nil
	}
	mb, err := s.store.GetMailbox(ctx, s.cfg.MailboxID)
	switch {
	case errors.Is(err, store.ErrNotFound):
		dk, err := s.keys.NewDataKey(ctx, s.keyContext())
		if err != nil {
			return nil, err
		}
		state, err := envelope.Seal(dk.Plaintext, mustJSON(mailboxState{}), s.stateAAD())
		if err != nil {
			return nil, err
		}
		mb = &store.Mailbox{ID: s.cfg.MailboxID, WrappedKey: dk.Wrapped, RetentionDays: store.DefaultRetentionDays, State: state, UpdatedAt: s.now()}
		if err := s.store.PutMailbox(ctx, mb); err != nil {
			return nil, err
		}
		s.log.Info("inbox: mailbox created")
		s.key = dk.Plaintext
	case err != nil:
		return nil, err
	default:
		k, err := s.keys.Unwrap(ctx, mb.WrappedKey, s.keyContext())
		if err != nil {
			return nil, err
		}
		s.key = k
	}
	return s.key, nil
}

func (s *Service) stateAAD() []byte { return envelope.AAD("mailbox-state", s.cfg.MailboxID) }
func (s *Service) metaAAD(id string) []byte {
	return envelope.AAD("message-meta", s.cfg.MailboxID, id)
}
func (s *Service) rawAAD(id string) []byte { return envelope.AAD("message-raw", s.cfg.MailboxID, id) }

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

// newID returns a time-ordered message id (32 hex characters).
func newID(t time.Time) string {
	r := make([]byte, 8)
	_, _ = rand.Read(r)
	return fmt.Sprintf("%016x", uint64(t.UnixNano())) + hex.EncodeToString(r) //nolint:gosec // time is after 1970
}

type mailboxState struct {
	ForwardingConfirmations []ForwardingConfirmation `json:"forwarding_confirmations"`
}

func (s *Service) loadMailbox(ctx context.Context) (*store.Mailbox, mailboxState, []byte, error) {
	key, err := s.dataKey(ctx)
	if err != nil {
		return nil, mailboxState{}, nil, err
	}
	mb, err := s.store.GetMailbox(ctx, s.cfg.MailboxID)
	if err != nil {
		return nil, mailboxState{}, nil, err
	}
	var st mailboxState
	plain, err := envelope.Open(key, mb.State, s.stateAAD())
	if err != nil {
		return nil, mailboxState{}, nil, err
	}
	if err := json.Unmarshal(plain, &st); err != nil {
		return nil, mailboxState{}, nil, err
	}
	return mb, st, key, nil
}

func (s *Service) saveMailbox(ctx context.Context, mb *store.Mailbox, st mailboxState, key []byte) error {
	sealed, err := envelope.Seal(key, mustJSON(st), s.stateAAD())
	if err != nil {
		return err
	}
	mb.State = sealed
	mb.UpdatedAt = s.now()
	return s.store.PutMailbox(ctx, mb)
}
