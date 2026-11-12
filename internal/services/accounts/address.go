package accounts

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/domain"
	"github.com/permaily-org/permaily/internal/store"
)

// Changing the contact address (ADR 0017). The person, signed in, asks for a
// message at the new address; its code proves the address is theirs, and then
// the account moves to it.
//
// The message is a sign-in message without the link: the same attempts in
// memory, the same tries, the same limits for an address. The attempt belongs
// to the account that started it and to this enclave: it signs nobody in, and
// no sign-in changes an address (match).

// StartAddressChange sends a code to a new contact address for the account
// behind a session. The answer is the same, and the message is sent, whether
// the address is another account's or nobody's: that is told only to whoever
// proved the address is theirs (FinishAddressChange).
func (s *Service) StartAddressChange(ctx context.Context, who *Authenticated, in contracts.AddressStart) (*contracts.EmailStarted, error) {
	doc, err := s.own(ctx, who)
	if err != nil {
		return nil, err
	}
	address, err := domain.NormalizeAddress(in.Address)
	if err != nil {
		return nil, err
	}
	switch {
	case doc.State == contracts.AccountClosing:
		return nil, contracts.ErrAccountClosing
	case doc.Address == address:
		// Nothing to change to. It is the asker's own address: it tells nothing.
		return nil, contracts.ErrInvalidAddress
	case !s.allow(doc.ID, s.now()):
		// Counted for the account, which is the asker's own. What the address
		// was sent already is counted apart, and told to nobody (deliverCode).
		return nil, contracts.ErrTooManyRequests
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
			s.deliverCode(ctx, id, doc.ID, address)
		}()
	default:
		s.cfg.Log.Warn("accounts: too many sign-in messages being sent, one was dropped")
	}
	return &contracts.EmailStarted{Attempt: id, ExpiresIn: int(domain.EmailCodeLifetime / time.Second)}, nil
}

// deliverCode sends the code that proves an address to the account that
// wants it for its own, within what an address is sent in an hour. An address
// that took too many wrong codes today is sent none: there is no link here to
// fall back on, and a six-digit code must not be guessed without limit.
func (s *Service) deliverCode(ctx context.Context, id, accountID, address string) {
	now := s.now()
	if !s.allow(address, now) {
		return
	}
	code := randomString("0123456789", domain.EmailCodeDigits)
	a := &attempt{address: address, account: accountID, code: sha256.Sum256([]byte(code)), withCode: true, expires: now.Add(domain.EmailCodeLifetime)}
	s.mu.Lock()
	if len(s.attempts) >= maxAttempts {
		s.mu.Unlock()
		s.cfg.Log.Warn("accounts: too many sign-ins waiting, one was dropped")
		return
	}
	if s.wrong[address] = recent(s.wrong[address], now, 24*time.Hour); len(s.wrong[address]) >= wrongCodesPerDay {
		s.mu.Unlock()
		return
	}
	s.attempts[id] = a
	s.mu.Unlock()
	if err := s.cfg.Mailer.Send(ctx, addressCodeMail(address, code)); err != nil {
		// Not the error: a mail system's errors carry the address.
		s.cfg.Log.Warn("accounts: a message to prove an address could not be sent")
		s.mu.Lock()
		delete(s.attempts, id)
		s.mu.Unlock()
	}
}

// addressCodeMail writes the message that proves a new contact address. It
// carries a code and no link: the code is typed where the change was asked.
func addressCodeMail(to, code string) Mail {
	minutes := int(domain.EmailCodeLifetime / time.Minute)
	return Mail{To: to, Subject: "Confirm your new address for Permaily", Text: fmt.Sprintf(
		"Your code to make this the contact address of your Permaily account:\n\n    %s\n\nType it in Settings, where you asked for the change. It works once, for %d minutes. If you did not ask for it, ignore this message: nothing changes without the code.\n",
		code, minutes)}
}

// FinishAddressChange changes the contact address of the account behind a
// session, with the code of the message sent to the new one. The attempt must
// be one this account started. An address that is another account's is
// refused here, after the code proved it is the asker's too, and never before.
func (s *Service) FinishAddressChange(ctx context.Context, who *Authenticated, in contracts.AddressFinish) (*contracts.Account, error) {
	doc, err := s.own(ctx, who)
	if err != nil {
		return nil, err
	}
	if doc.State == contracts.AccountClosing {
		return nil, contracts.ErrAccountClosing
	}
	a, err := s.match(contracts.EmailFinish{Attempt: in.Attempt, Code: in.Code}, doc.ID, true)
	if err != nil {
		return nil, err
	}
	moved, from, err := s.move(ctx, doc.ID, a.address)
	if err != nil {
		return nil, err
	}
	if from != "" {
		s.cfg.Log.Info("accounts: contact address changed")
		s.notify(ctx, noticeAddressChanged, addressMails(from, moved.Address, s.now())...)
	}
	view := moved.view()
	return &view, nil
}

// errMoved is a change of an address that another change of the same account
// overtook.
var errMoved = errors.New("accounts: another change of the address came first")

// move makes to the contact address of an account, and returns the account
// and the address it had ("" when to was its address already).
//
// An address leads to its account through a pointer, and the pointer is what
// keeps two accounts from having one address: it is one entry, written only
// where none is or in the place of the one that was read. So the move goes in
// four steps, each of which can be the last one that happens:
//
//  1. The record says the account is moving to the address (MovingTo), with
//     a name for this move. Its old address still leads to it.
//  2. The pointer from the new address is written, to lead here. Both
//     addresses lead to the account now: the record bears both out.
//  3. The record says the new address is the account's, and nothing of the
//     move. The old pointer leads nowhere from this moment.
//  4. The old pointer is deleted, as it was read.
//
// The record names the address before the pointer is written, as a sign-up
// writes its record first: a pointer that leads to a record which does not
// bear it out is taken for one that leads nowhere, and replaced by the next
// sign-up. And the pointer is written again even when it leads here already,
// so that whoever read it as leading nowhere a moment ago does not write over
// it. A failure in between leaves an account that both addresses find, or one
// that only the old one finds: never one that cannot be found, and never two
// accounts that one address finds. Asking for the change again ends it.
func (s *Service) move(ctx context.Context, id, to string) (*accountDoc, string, error) {
	claim := newSecret()
	var already *accountDoc
	_, err := s.change(ctx, id, func(doc *accountDoc) error {
		if already = nil; doc.Address == to {
			already = doc
			return errUnchanged
		}
		doc.MovingTo, doc.MovingClaim = to, claim
		return nil
	})
	if errors.Is(err, errUnchanged) {
		return already, "", nil
	}
	if err != nil {
		return nil, "", err
	}
	if err := s.claimAddress(ctx, id, to); err != nil {
		s.unmove(ctx, id, claim)
		return nil, "", err
	}
	from := ""
	doc, err := s.change(ctx, id, func(doc *accountDoc) error {
		if doc.MovingClaim != claim {
			return errMoved
		}
		from = doc.Address
		doc.Address, doc.AddressVerifiedAt, doc.MovingTo, doc.MovingClaim = to, s.now(), "", ""
		return nil
	})
	if errors.Is(err, errMoved) {
		// The code was used and the address is not this one: asked again.
		return nil, "", contracts.ErrInvalidCode
	}
	if err != nil {
		s.unmove(ctx, id, claim)
		return nil, "", err
	}
	// The account is at its new address. What is left of the old one is a
	// pointer that leads nowhere, which the address's next sign-up replaces.
	key, err := s.signingKey(ctx)
	if err == nil {
		err = s.unpoint(ctx, key, loginPartition(from), id)
	}
	if err != nil {
		s.cfg.Log.Warn("accounts: the pointer of an address that was left could not be deleted")
	}
	return doc, from, nil
}

// claimAddress writes the pointer from an address to an account that is
// moving to it. It is ErrAddressInUse when the address is another account's.
func (s *Service) claimAddress(ctx context.Context, id, address string) error {
	key, err := s.signingKey(ctx)
	if err != nil {
		return err
	}
	partition := loginPartition(address)
	for range maxRewrites {
		version := uint64(0)
		e, err := s.cfg.Entries.GetEntry(ctx, partition, loginKey)
		switch {
		case errors.Is(err, store.ErrNotFound):
		case err != nil:
			return err
		default:
			if other := pointed(key, e); other != "" && other != id {
				// An account that cannot be read is never taken for none.
				doc, err := s.read(ctx, other)
				if err != nil {
					return err
				}
				if doc != nil && !doc.due(s.now()) && doc.has(address) {
					return contracts.ErrAddressInUse
				}
			}
			version = e.Version
		}
		err = s.point(ctx, partition, loginKey, id, version)
		if !errors.Is(err, store.ErrExists) && !errors.Is(err, store.ErrConflict) {
			return err
		}
		// Someone else wrote it in between: read again who it leads to.
	}
	return errors.New("accounts: the pointer of an address keeps changing")
}

// unmove takes back a move that did not end, if the record still names it:
// the account is not moving anywhere, and the pointer that may have been
// written for it leads nowhere. A failure here leaves the account found by
// both addresses, until its address is changed again.
func (s *Service) unmove(ctx context.Context, id, claim string) {
	_, err := s.rewrite(ctx, id, func(doc *accountDoc) error {
		if doc.MovingClaim != claim {
			return errUnchanged
		}
		doc.MovingTo, doc.MovingClaim = "", ""
		return nil
	})
	if err != nil && !errors.Is(err, errUnchanged) {
		s.cfg.Log.Warn("accounts: a change of address that did not end could not be taken back")
	}
}
