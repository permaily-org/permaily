package mailboxes

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/store"
)

// What delivery keeps in each enclave's memory (ADR 0026, "Limits").
const (
	// MaxMessagesPerHour and MaxBytesPerDay bound the mail one enclave takes
	// into one mailbox.
	MaxMessagesPerHour = 600
	MaxBytesPerDay     = 2 << 30
	// ConfirmFor is how long a forwarding confirmation is shown.
	ConfirmFor = 30 * 24 * time.Hour
)

// Accept implements smtpd.Recipients: the mailbox an address leads to, after
// reading its pointer and its record (ADR 0026, "The sequence", 1).
func (s *Service) Accept(ctx context.Context, rcpt string) (string, error) {
	l, err := s.byAddress(ctx, rcpt)
	if err != nil {
		return "", err
	}
	if l == nil || !l.doc.PausedAt.IsZero() {
		return "", ErrNoSuchMailbox
	}
	switch err := s.listed(ctx, l); {
	case errors.Is(err, errNotAccepting):
		return "", ErrNoSuchMailbox
	case err != nil:
		return "", err
	}
	if !s.room(l.id) {
		return "", ErrSlowDown
	}
	return l.id, nil
}

// room reports whether a mailbox takes more mail in this enclave now.
func (s *Service) room(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	recent := slices.DeleteFunc(s.delivered[id], func(d delivery) bool { return now.Sub(d.at) >= 24*time.Hour })
	s.delivered[id] = recent
	if len(recent) == 0 {
		delete(s.delivered, id)
	}
	messages, bytes := 0, 0
	for _, d := range recent {
		if now.Sub(d.at) < time.Hour {
			messages++
		}
		bytes += d.bytes
	}
	return messages < MaxMessagesPerHour && bytes < MaxBytesPerDay
}

func (s *Service) count(id string, n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.delivered[id] = append(s.delivered[id], delivery{at: s.now(), bytes: n})
}

// Deliver implements smtpd.Recipients (ADR 0026, "The sequence", 2): the
// signatures are checked and the message parsed once, then each mailbox is
// tried in turn.
func (s *Service) Deliver(ctx context.Context, from string, ids []string, raw []byte) error {
	auth := s.cfg.Auth(ctx, raw)
	p := inbox.Parse(ctx, raw)
	p.Meta.EnvelopeFrom = from
	for _, a := range auth {
		p.Meta.Auth = append(p.Meta.Auth, contracts.AuthCheck{Method: a.Method, Domain: a.Domain, Result: a.Result})
	}
	all := true
	for _, id := range slicesCompact(ids) {
		if err := s.deliver(ctx, id, from, &p, auth, raw); err != nil {
			all = false
			s.cfg.Log.Warn("mailboxes: a message was not stored in a mailbox", "reason", reason(err))
		}
	}
	if !all {
		return ErrNotAllStored
	}
	s.cfg.Log.Info("mailboxes: a message was stored", "size", len(raw), "mailboxes", len(ids), "attachments", len(p.Meta.Attachments))
	return nil
}

func slicesCompact(ids []string) []string {
	out := slices.Clone(ids)
	slices.Sort(out)
	return slices.Compact(out)
}

// errNotAccepting is a mailbox that stopped taking mail between RCPT TO and
// the end of DATA.
var errNotAccepting = errors.New("mailboxes: the mailbox does not take mail now")

// reason is what is logged of why a message was not stored: a kind, never an
// identifier.
func reason(err error) string {
	switch {
	case errors.Is(err, errNotAccepting):
		return "not_accepting"
	case errors.Is(err, inbox.ErrClaimed):
		return "claimed"
	case errors.Is(err, context.DeadlineExceeded), errors.Is(err, context.Canceled):
		return "deadline"
	}
	return "storage"
}

// deliver stores the message once in one mailbox, or says why not.
func (s *Service) deliver(ctx context.Context, id, from string, p *inbox.Parsed, auth []inbox.AuthResult, raw []byte) error {
	// 1. The record again: it may have stopped taking mail since RCPT TO.
	l, err := s.load(ctx, id)
	if err != nil {
		return err
	}
	if l.state != stActive || !l.doc.PausedAt.IsZero() {
		return errNotAccepting
	}
	// And its account's list: open and naming it.
	if err := s.listed(ctx, l); err != nil {
		return err
	}
	// 2. The key and the retention.
	key, err := s.mailboxKey(ctx, l)
	if err != nil {
		return err
	}
	b := inbox.Box{MailboxID: id, Key: key}
	set, err := s.readSettings(ctx, id, nil)
	if err != nil {
		return err
	}
	// 3. The claim.
	seen, err := inbox.SeenKey(key, from, raw)
	if err != nil {
		return err
	}
	claim, err := s.writer.Claim(ctx, b, seen, set.retentionDays)
	if err != nil {
		return err
	}
	if claim.Stored() {
		return nil
	}
	// 4. The parts, under what the claim fixed.
	var confirm *inbox.ConfirmEntry
	verified := false
	if c := p.Confirmation; c != nil {
		priv, err := s.private(key, l)
		if err != nil {
			return err
		}
		var other bool
		verified, other = c.Verdict(auth, priv.Address)
		confirm = &inbox.ConfirmEntry{
			Code: c.Code, Requester: c.Requester, ReceivedAt: claim.Seen.ReceivedAt, Verified: verified,
			ForAnotherAddress: other, Ends: claim.Seen.ReceivedAt.Add(ConfirmFor),
		}
		if other {
			confirm.Code = ""
		}
	}
	if err := s.writer.Store(ctx, b, claim, p, raw, confirm); err != nil {
		return err
	}
	// 5. What the record learns of it.
	switch {
	case confirm != nil && verified:
		if err := s.confirmed(ctx, id, key, confirm.Requester); err != nil {
			return err
		}
	case confirm == nil && l.doc.ReceivingSince.IsZero():
		if err := s.receiving(ctx, id, claim.Seen.ReceivedAt); err != nil {
			return err
		}
	}
	// 6. The mark.
	if err := s.writer.Mark(ctx, b, claim); err != nil {
		return err
	}
	s.count(id, len(raw))
	return nil
}

// listed reads the account's list of an active mailbox and checks that it is
// open and names the mailbox: a list that is not there, lost, forked or
// closed (the account was removed, though a write of the record failed) is
// errNotAccepting, and so is one that does not name it. Any other failure of
// the read is an error (a temporary failure). A blocked account and one in
// its seven days of closing keep an open list and still receive (ADR 0026,
// "Accounts removed, closing and blocked"). It is one more strict read per
// recipient, at RCPT TO and again before storing.
func (s *Service) listed(ctx context.Context, l *loaded) error {
	list, _, err := s.loadList(ctx, l.doc.Account)
	switch {
	case errors.Is(err, store.ErrNotFound), errors.Is(err, errListLost), errors.Is(err, store.ErrForked):
		return errNotAccepting
	case err != nil:
		return err
	}
	if list.State != listOpen {
		return errNotAccepting
	}
	if _, ok := list.find(l.id); !ok {
		return errNotAccepting
	}
	return nil
}

// receiving sets when the mailbox started receiving, once: when another
// enclave set it first, the earlier time stays.
func (s *Service) receiving(ctx context.Context, id string, at time.Time) error {
	for range maxTries {
		l, err := s.load(ctx, id)
		if err != nil {
			return err
		}
		if l.state != stActive {
			return errNotAccepting
		}
		if !l.doc.ReceivingSince.IsZero() {
			return nil
		}
		doc := *l.doc
		doc.ReceivingSince = at.Truncate(time.Second)
		if err := s.putAt(ctx, l, &doc); !errors.Is(err, store.ErrChanged) {
			return err
		}
	}
	return errors.New("mailboxes: a mailbox keeps changing")
}

// confirmed writes the Gmail account Google confirmed into a waiting
// mailbox's record, once, and tells its person.
func (s *Service) confirmed(ctx context.Context, id string, key []byte, requester string) error {
	for range maxTries {
		l, err := s.load(ctx, id)
		if err != nil {
			return err
		}
		if l.state != stActive {
			return errNotAccepting
		}
		if !l.doc.ReceivingSince.IsZero() || !l.doc.VerifiedAt.IsZero() {
			return nil // receiving already, or confirmed by an earlier one
		}
		priv, err := s.private(key, l)
		if err != nil {
			return err
		}
		now := s.now().Truncate(time.Second)
		priv.Source = &sourceDoc{Address: requester, VerifiedAt: now}
		doc := *l.doc
		doc.VerifiedAt = now
		if doc.Sealed, err = sealPrivate(key, id, priv); err != nil {
			return err
		}
		err = s.putAt(ctx, l, &doc)
		if errors.Is(err, store.ErrChanged) {
			continue
		}
		if err == nil {
			s.notify(ctx, doc.Account, NoticeForwarding, now)
		}
		return err
	}
	return errors.New("mailboxes: a mailbox keeps changing")
}
