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

// Confirming again (ADR 0022). A session alone is not enough for the changes
// that decide who gets into an account: adding or removing a way in, "passkey
// only", recovery codes, the contact address, deleting the account, and what
// an admin changes. The person proves who they are, and the proof lasts
// domain.ConfirmationLifetime. Signing in is a proof; after it, a passkey of
// the account is, and, unless the account is "passkey only", a code sent to
// its contact address.
//
// When the proof was made is kept in the session's own entry, signed with
// the rest of it (sessionDoc.ConfirmedAt): a request that lands on another
// enclave sees it, and whoever writes to the storage cannot make a session
// fresh. A ceremony or an attempt started to confirm lives in this enclave's
// memory, as a sign-in's does, and belongs to the session that started it:
// it signs nobody in, changes no address, and confirms no other session.

// confirmFor is what a ceremony or an attempt that confirms a session is
// started for: no account identifier, and never "", so neither a sign-in nor
// the making of a passkey nor a change of address takes it, and it takes none
// of theirs (end, match).
func confirmFor(who *Authenticated) string {
	return "confirm#" + who.AccountID + "#" + who.Session.ID
}

// fresh reports whether the session's proof of who the person is still holds.
func (s *Service) fresh(who *Authenticated) bool {
	return s.now().Before(who.Session.ConfirmedUntil)
}

// confirmed refuses a change while the session's proof is not fresh.
func (s *Service) confirmed(who *Authenticated) error {
	if !s.fresh(who) {
		return contracts.ErrConfirmationNeeded
	}
	return nil
}

// ConfirmWays says how the person behind a session can confirm it is them:
// with a passkey when the account has one, and by mail unless it signs in
// with passkeys only. A closing account can confirm too.
func (s *Service) ConfirmWays(ctx context.Context, who *Authenticated) (*contracts.ConfirmWays, error) {
	doc, err := s.own(ctx, who)
	if err != nil {
		return nil, err
	}
	out := &contracts.ConfirmWays{Ways: []string{}}
	if len(doc.Passkeys) > 0 {
		out.Ways = append(out.Ways, contracts.ConfirmPasskey)
	}
	if !doc.PasskeyOnly {
		out.Ways = append(out.Ways, contracts.ConfirmEmail)
	}
	if s.fresh(who) {
		out.ConfirmedUntil = who.Session.ConfirmedUntil
	}
	return out, nil
}

// StartConfirmPasskey starts confirming with a passkey: the options name the
// account's own passkeys, and the ceremony belongs to this session.
func (s *Service) StartConfirmPasskey(ctx context.Context, who *Authenticated) (*contracts.PasskeyRequest, error) {
	doc, err := s.own(ctx, who)
	if err != nil {
		return nil, err
	}
	if len(doc.Passkeys) == 0 {
		return nil, contracts.ErrPasskeyNeeded
	}
	id, challenge := s.begin(confirmFor(who))
	out := &contracts.PasskeyRequest{Ceremony: id, Options: contracts.RequestOptions{
		Challenge: challenge, Timeout: int(domain.PasskeyCeremonyLifetime / time.Millisecond),
		RPID: s.rp.ID, UserVerification: passkeyRequired, AllowCredentials: []contracts.CredentialDescriptor{},
	}}
	for _, p := range doc.Passkeys {
		out.Options.AllowCredentials = append(out.Options.AllowCredentials, contracts.CredentialDescriptor{Type: publicKey, ID: p.ID})
	}
	return out, nil
}

// FinishConfirmPasskey ends confirming with a passkey: one of the session's
// own account, answering the ceremony this session started. Every failure is
// the one error of a passkey.
func (s *Service) FinishConfirmPasskey(ctx context.Context, who *Authenticated, in contracts.PasskeySignIn) (*contracts.Me, error) {
	challenge, ok := s.end(in.Ceremony, confirmFor(who))
	if !ok || string(in.Credential.Response.UserHandle) != who.AccountID {
		return nil, contracts.ErrInvalidPasskey
	}
	doc, err := s.own(ctx, who)
	if err != nil {
		return nil, err
	}
	i := doc.passkey(in.Credential.RawID)
	if i < 0 {
		return nil, contracts.ErrInvalidPasskey
	}
	r := in.Credential.Response
	if _, err := s.rp.Asserted(challenge, doc.Passkeys[i].PublicKey, r.ClientDataJSON, r.AuthenticatorData, r.Signature); err != nil {
		s.refused("accounts: a passkey that confirms a session was refused", err)
		return nil, contracts.ErrInvalidPasskey
	}
	return s.confirm(ctx, who, contracts.ConfirmPasskey)
}

// StartConfirmEmail sends a code to the account's contact address, to confirm
// this session. An account that signs in with passkeys only confirms with one
// (ErrPasskeyRequired). The message carries a code and no link: the code is
// typed where it was asked for.
func (s *Service) StartConfirmEmail(ctx context.Context, who *Authenticated) (*contracts.EmailStarted, error) {
	doc, err := s.own(ctx, who)
	if err != nil {
		return nil, err
	}
	if doc.PasskeyOnly {
		return nil, contracts.ErrPasskeyRequired
	}
	now := s.now()
	// Counted for the account, which is the asker's own, and the address's
	// messages with the sign-in messages. An address that took too many wrong
	// codes today gets no code: there is no link to fall back on. It is the
	// person's own address, so they are told.
	s.mu.Lock()
	s.sweep(now)
	tooWrong := len(recent(s.wrong[doc.Address], now, 24*time.Hour)) >= wrongCodesPerDay
	s.mu.Unlock()
	if tooWrong || !s.allow("confirm#"+doc.ID, now) {
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
			s.deliverConfirm(ctx, id, confirmFor(who), doc.Address)
		}()
	default:
		s.cfg.Log.Warn("accounts: too many sign-in messages being sent, one was dropped")
	}
	return &contracts.EmailStarted{Attempt: id, ExpiresIn: int(domain.EmailCodeLifetime / time.Second)}, nil
}

// deliverConfirm sends the code that confirms a session, within what an
// address is sent in an hour.
func (s *Service) deliverConfirm(ctx context.Context, id, owner, address string) {
	now := s.now()
	if !s.allow(address, now) {
		return
	}
	code := randomString("0123456789", domain.EmailCodeDigits)
	a := &attempt{address: address, account: owner, code: sha256.Sum256([]byte(code)), withCode: true, expires: now.Add(domain.EmailCodeLifetime)}
	s.mu.Lock()
	if len(s.attempts) >= maxAttempts {
		s.mu.Unlock()
		s.cfg.Log.Warn("accounts: too many sign-ins waiting, one was dropped")
		return
	}
	s.attempts[id] = a
	s.mu.Unlock()
	if err := s.cfg.Mailer.Send(ctx, confirmMail(address, s.rp.ID, code)); err != nil {
		// Not the error: a mail system's errors carry the address.
		s.cfg.Log.Warn("accounts: a message to confirm a session could not be sent")
		s.mu.Lock()
		delete(s.attempts, id)
		s.mu.Unlock()
	}
}

// confirmMail writes the message with the code that confirms a session.
func confirmMail(to, app, code string) Mail {
	minutes := int(domain.EmailCodeLifetime / time.Minute)
	return Mail{To: to, Subject: "Confirm it is you on Permaily", Text: fmt.Sprintf(
		"Your code to confirm it is you on Permaily:\n\n    %s\n\nType it where you were asked for it. It works once, for %d minutes.\n\nIf you did not ask for it, someone may be signed in to your account: sign in at %s, open Settings and sign out the devices you do not know. Nothing changes without the code.\n",
		code, minutes, app)}
}

// FinishConfirmEmail confirms the session with the code of its message. The
// attempt must be one this session started.
func (s *Service) FinishConfirmEmail(ctx context.Context, who *Authenticated, in contracts.ConfirmFinish) (*contracts.Me, error) {
	if _, err := s.own(ctx, who); err != nil {
		return nil, err
	}
	if _, err := s.match(contracts.EmailFinish{Attempt: in.Attempt, Code: in.Code}, confirmFor(who), true); err != nil {
		return nil, err
	}
	return s.confirm(ctx, who, contracts.ConfirmEmail)
}

// confirm writes in the session's entry that its person proved who they are
// now, and returns the account and the session. The entry is written at the
// version read: another request of the session may touch it meanwhile.
func (s *Service) confirm(ctx context.Context, who *Authenticated, how string) (*contracts.Me, error) {
	key, err := s.signingKey(ctx)
	if err != nil {
		return nil, err
	}
	for range maxRewrites {
		e, err := s.cfg.Entries.GetEntry(ctx, accountPartition(who.AccountID), sessionPrefix+who.Session.ID)
		if errors.Is(err, store.ErrNotFound) {
			return nil, contracts.ErrUnauthorized // signed out meanwhile
		}
		if err != nil {
			return nil, err
		}
		doc := s.readSession(key, e, who.AccountID)
		if doc == nil {
			return nil, contracts.ErrUnauthorized
		}
		now := s.now()
		doc.ConfirmedAt, doc.LastUsedAt = now, now
		switch err := s.putSession(ctx, doc, e.Version); {
		case errors.Is(err, store.ErrConflict):
			continue
		case err != nil:
			return nil, err
		}
		s.cfg.Log.Info("accounts: a session was confirmed", "way", how)
		session := doc.view(doc.ID, now)
		return s.Me(ctx, &Authenticated{AccountID: who.AccountID, Session: session})
	}
	return nil, errors.New("accounts: a session keeps changing")
}
