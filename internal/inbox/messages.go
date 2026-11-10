package inbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/permaily-org/permaily/internal/envelope"
	"github.com/permaily-org/permaily/internal/store"
)

// ErrInvalidRetention is returned for an unsupported retention period.
var ErrInvalidRetention = errors.New("inbox: unsupported retention period")

// ForwardingConfirmation is a Gmail forwarding confirmation code received by
// the mailbox. Verified is true when Google's DKIM signature checked out; the
// mailbox accepts mail from anyone, so an unverified one may be spoofed.
type ForwardingConfirmation struct {
	Code       string    `json:"code"`
	Requester  string    `json:"requester,omitempty"`
	ReceivedAt time.Time `json:"received_at"`
	Verified   bool      `json:"verified"`
}

// Ingest stores one message delivered over SMTP and returns its id.
func (s *Service) Ingest(ctx context.Context, from string, rcpts []string, raw []byte) (string, error) {
	for _, r := range rcpts {
		if !s.Accepts(r) {
			return "", ErrUnknownRecipient
		}
	}
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	mb, st, key, err := s.loadMailbox(ctx)
	if err != nil {
		return "", err
	}
	now := s.now().UTC()
	id := newID(now)

	meta, text := parse(raw)
	meta.EnvelopeFrom = from
	meta.EnvelopeTo = rcpts
	meta.Auth = s.auth(ctx, raw)

	if code, requester, ok := forwardingConfirmation(meta, text); ok {
		meta.ForwardingConfirmation = true
		st.ForwardingConfirmations = append(st.ForwardingConfirmations, ForwardingConfirmation{
			Code: code, Requester: requester, ReceivedAt: now, Verified: dkimPass(meta.Auth, "google.com"),
		})
		if n := len(st.ForwardingConfirmations); n > 10 { // keep the latest ones only
			st.ForwardingConfirmations = st.ForwardingConfirmations[n-10:]
		}
		if err := s.saveMailbox(ctx, mb, st, key); err != nil {
			return "", err
		}
	}

	sealedMeta, err := envelope.Seal(key, mustJSON(meta), s.metaAAD(id))
	if err != nil {
		return "", err
	}
	sealedRaw, err := envelope.Seal(key, raw, s.rawAAD(id))
	if err != nil {
		return "", err
	}
	retention := mb.RetentionDays
	if !store.ValidRetention(retention) {
		retention = store.DefaultRetentionDays
	}
	rec := &store.Message{
		MailboxID:     s.cfg.MailboxID,
		ID:            id,
		ReceivedAt:    now,
		ExpiresAt:     now.AddDate(0, 0, retention),
		Size:          len(raw),
		RetentionDays: retention,
		Meta:          sealedMeta,
	}
	if err := s.store.PutMessage(ctx, rec, sealedRaw); err != nil {
		return "", err
	}
	s.log.Info("inbox: message stored", "size", len(raw), "attachments", len(meta.Attachments))
	return id, nil
}

func dkimPass(results []AuthResult, domain string) bool {
	return slices.ContainsFunc(results, func(r AuthResult) bool {
		return r.Method == "dkim" && r.Result == "pass" && r.Domain == domain
	})
}

// MailboxView is what the owner sees about the mailbox.
type MailboxView struct {
	Address                 string                   `json:"address"`
	RetentionDays           int                      `json:"retention_days"`
	RetentionOptions        []int                    `json:"retention_options"`
	ForwardingConfirmations []ForwardingConfirmation `json:"forwarding_confirmations"`
}

// Mailbox returns the mailbox view.
func (s *Service) Mailbox(ctx context.Context) (MailboxView, error) {
	mb, st, _, err := s.loadMailbox(ctx)
	if err != nil {
		return MailboxView{}, err
	}
	confirmations := st.ForwardingConfirmations
	if confirmations == nil {
		confirmations = []ForwardingConfirmation{}
	}
	return MailboxView{
		Address:                 s.cfg.Address,
		RetentionDays:           mb.RetentionDays,
		RetentionOptions:        store.RetentionDays,
		ForwardingConfirmations: confirmations,
	}, nil
}

// SetRetention changes the retention period for messages received from now on.
func (s *Service) SetRetention(ctx context.Context, days int) error {
	if !store.ValidRetention(days) {
		return fmt.Errorf("%w: %d (use one of %v)", ErrInvalidRetention, days, store.RetentionDays)
	}
	s.stateMu.Lock()
	defer s.stateMu.Unlock()
	mb, st, key, err := s.loadMailbox(ctx)
	if err != nil {
		return err
	}
	mb.RetentionDays = days
	return s.saveMailbox(ctx, mb, st, key)
}

// Summary is a message in a listing.
type Summary struct {
	ID          string    `json:"id"`
	ReceivedAt  time.Time `json:"received_at"`
	ExpiresAt   time.Time `json:"expires_at"`
	Size        int       `json:"size"`
	From        []string  `json:"from,omitempty"`
	Subject     string    `json:"subject,omitempty"`
	Snippet     string    `json:"snippet,omitempty"`
	Attachments int       `json:"attachments"`
}

// Message is one message with its metadata.
type Message struct {
	Summary
	Meta Meta `json:"meta"`
}

func (s *Service) open(key []byte, rec store.Message) (Message, error) {
	plain, err := envelope.Open(key, rec.Meta, s.metaAAD(rec.ID))
	if err != nil {
		return Message{}, err
	}
	var meta Meta
	if err := json.Unmarshal(plain, &meta); err != nil {
		return Message{}, err
	}
	return Message{
		Summary: Summary{
			ID: rec.ID, ReceivedAt: rec.ReceivedAt, ExpiresAt: rec.ExpiresAt, Size: rec.Size,
			From: meta.From, Subject: meta.Subject, Snippet: meta.Snippet, Attachments: len(meta.Attachments),
		},
		Meta: meta,
	}, nil
}

// List returns up to limit messages, newest first, older than before.
func (s *Service) List(ctx context.Context, limit int, before string) ([]Summary, error) {
	key, err := s.dataKey(ctx)
	if err != nil {
		return nil, err
	}
	recs, err := s.store.ListMessages(ctx, s.cfg.MailboxID, limit, before)
	if err != nil {
		return nil, err
	}
	out := make([]Summary, 0, len(recs))
	for _, rec := range recs {
		m, err := s.open(key, rec)
		if err != nil {
			return nil, err
		}
		out = append(out, m.Summary)
	}
	return out, nil
}

// Get returns a message's metadata.
func (s *Service) Get(ctx context.Context, id string) (Message, error) {
	m, _, err := s.get(ctx, id, false)
	return m, err
}

// Raw returns a message's original bytes (RFC 5322).
func (s *Service) Raw(ctx context.Context, id string) ([]byte, error) {
	_, raw, err := s.get(ctx, id, true)
	return raw, err
}

func (s *Service) get(ctx context.Context, id string, wantRaw bool) (Message, []byte, error) {
	key, err := s.dataKey(ctx)
	if err != nil {
		return Message{}, nil, err
	}
	rec, sealedRaw, err := s.store.GetMessage(ctx, s.cfg.MailboxID, id)
	if err != nil {
		return Message{}, nil, err
	}
	m, err := s.open(key, *rec)
	if err != nil {
		return Message{}, nil, err
	}
	if !wantRaw {
		return m, nil, nil
	}
	raw, err := envelope.Open(key, sealedRaw, s.rawAAD(id))
	return m, raw, err
}

// Delete removes a message.
func (s *Service) Delete(ctx context.Context, id string) error {
	return s.store.DeleteMessage(ctx, s.cfg.MailboxID, id)
}
