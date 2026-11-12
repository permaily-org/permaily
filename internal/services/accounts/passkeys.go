package accounts

import (
	"bytes"
	"cmp"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"errors"
	"slices"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/domain"
	"github.com/permaily-org/permaily/internal/webauthn"
)

// passkeyDoc is a passkey in its account's record: signed with the rest of
// it, so one written by hand is not a way in. The signature counter is not
// kept (ADR 0017).
type passkeyDoc struct {
	ID contracts.Bytes `json:"id"`
	// PublicKey is the key as the authenticator gave it (a COSE_Key).
	PublicKey contracts.Bytes `json:"public_key"`
	Name      string          `json:"name"`
	CreatedAt time.Time       `json:"created_at"`
	Synced    bool            `json:"synced"`
}

func (p *passkeyDoc) view() contracts.Passkey {
	return contracts.Passkey{ID: p.ID, Name: p.Name, CreatedAt: p.CreatedAt, Synced: p.Synced}
}

// passkey returns where in the record the passkey with that identifier is,
// or -1.
func (d *accountDoc) passkey(id []byte) int {
	return slices.IndexFunc(d.Passkeys, func(p passkeyDoc) bool { return bytes.Equal(p.ID, id) })
}

// ---- ceremonies ----

// ceremonyDoc is a passkey being made or used, between the options sent to
// the browser and its answer. Like a sign-in by mail it is kept sealed in the
// storage, and ended once, through any enclave (pending.go).
type ceremonyDoc struct {
	V         int    `json:"v"`
	Challenge []byte `json:"challenge"`
	// Account is whose passkey is being made; empty for a sign-in, which
	// learns the account from the answer; confirmFor of a session for a
	// ceremony that confirms that session (confirm.go).
	Account string `json:"account,omitempty"`
	pendingTimes
}

// begin starts a ceremony and returns its identifier and its challenge.
func (s *Service) begin(ctx context.Context, account string) (string, []byte, error) {
	now := s.now()
	c := &ceremonyDoc{V: 1, Challenge: make([]byte, 32), Account: account, pendingTimes: pendingTimes{Made: now, Expires: now.Add(domain.PasskeyCeremonyLifetime)}}
	_, _ = rand.Read(c.Challenge) // crypto/rand does not fail
	id := newID("cer")
	if err := s.stash(ctx, pendingPartition(kindCeremony, id), c); err != nil {
		return "", nil, err
	}
	return id, c.Challenge, nil
}

// end returns the challenge of a ceremony that was started for account and
// is still running. The ceremony is over either way: an answer is tried once.
func (s *Service) end(ctx context.Context, id, account string) ([]byte, bool, error) {
	if !validID("cer", id) {
		return nil, false, nil
	}
	var c ceremonyDoc
	e, err := s.unstash(ctx, pendingPartition(kindCeremony, id), &c)
	if err != nil || e == nil {
		return nil, false, err
	}
	ok, err := s.claim(ctx, e, c.Expires)
	if err != nil || !ok || c.Account != account {
		return nil, false, err
	}
	return c.Challenge, true, nil
}

// refused logs why the WebAuthn checks refused an answer. Their errors are
// fixed words that hold nothing of the answer; anything else is not logged.
func (s *Service) refused(what string, err error) {
	if errors.Is(err, webauthn.ErrInvalid) {
		s.cfg.Log.Info(what, "why", err.Error())
	}
}

// ---- making, naming and removing passkeys ----

const (
	publicKey       = "public-key"
	passkeyRequired = "required"
)

// StartPasskey starts making a passkey for the account behind a session, and
// returns what the browser needs to make it.
func (s *Service) StartPasskey(ctx context.Context, who *Authenticated) (*contracts.PasskeyCreation, error) {
	if err := s.confirmed(who); err != nil {
		return nil, err
	}
	doc, err := s.own(ctx, who)
	if err != nil {
		return nil, err
	}
	if doc.State == contracts.AccountClosing {
		// Told now, and not after the person made the passkey.
		return nil, contracts.ErrAccountClosing
	}
	if err := s.agreeing(doc); err != nil {
		return nil, err
	}
	if len(doc.Passkeys) >= domain.MaxPasskeys {
		return nil, contracts.ErrTooManyPasskeys
	}
	id, challenge, err := s.begin(ctx, doc.ID)
	if err != nil {
		return nil, err
	}
	out := &contracts.PasskeyCreation{Ceremony: id, Options: contracts.CreationOptions{
		RP: contracts.RelyingParty{ID: s.rp.ID, Name: "Permaily"},
		// The identifier is what the passkey answers with when it signs in:
		// it names the account, so no address is asked then.
		User:               contracts.PasskeyUser{ID: []byte(doc.ID), Name: doc.Address, DisplayName: cmp.Or(doc.Name, doc.Address)},
		Challenge:          challenge,
		Timeout:            int(domain.PasskeyCeremonyLifetime / time.Millisecond),
		ExcludeCredentials: []contracts.CredentialDescriptor{},
		AuthenticatorSelection: contracts.AuthenticatorSelection{
			ResidentKey: passkeyRequired, UserVerification: passkeyRequired,
		},
		// Which device holds the key does not matter here.
		Attestation: "none",
	}}
	for _, alg := range webauthn.Algorithms {
		out.Options.PubKeyCredParams = append(out.Options.PubKeyCredParams, contracts.CredentialParameter{Type: publicKey, Alg: alg})
	}
	// An authenticator that holds one of these already does not make another.
	for _, p := range doc.Passkeys {
		out.Options.ExcludeCredentials = append(out.Options.ExcludeCredentials, contracts.CredentialDescriptor{Type: publicKey, ID: p.ID})
	}
	return out, nil
}

// FinishPasskey ends making a passkey with the browser's answer, and adds it
// to the account. Every failure of the ceremony is the same error.
func (s *Service) FinishPasskey(ctx context.Context, who *Authenticated, in contracts.PasskeyFinish) (*contracts.Passkey, error) {
	// A name that cannot be kept is asked again without the ceremony ending:
	// it says nothing of the answer.
	name, err := domain.PasskeyName(in.Name)
	if err != nil {
		return nil, err
	}
	challenge, ok, err := s.end(ctx, in.Ceremony, who.AccountID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, contracts.ErrInvalidPasskey
	}
	made, err := s.rp.Registered(challenge, in.Credential.Response.ClientDataJSON, in.Credential.Response.AttestationObject)
	if err != nil {
		s.refused("accounts: a passkey that was made was refused", err)
		return nil, contracts.ErrInvalidPasskey
	}
	if !bytes.Equal(made.ID, in.Credential.RawID) {
		// The browser names another credential than the one it proved.
		return nil, contracts.ErrInvalidPasskey
	}
	p := passkeyDoc{ID: made.ID, PublicKey: made.PublicKey, Name: name, CreatedAt: s.now(), Synced: made.Synced}
	doc, err := s.change(ctx, who.AccountID, func(doc *accountDoc) error {
		if doc.passkey(p.ID) >= 0 {
			return contracts.ErrInvalidPasskey
		}
		if len(doc.Passkeys) >= domain.MaxPasskeys {
			return contracts.ErrTooManyPasskeys
		}
		doc.Passkeys = append(doc.Passkeys, p)
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.cfg.Log.Info("accounts: passkey made")
	s.noticeChange(ctx, doc, noticePasskeyAdded)
	view := p.view()
	return &view, nil
}

// Passkeys lists the passkeys of the account behind a session, the newest
// last, and says how the account signs in.
func (s *Service) Passkeys(ctx context.Context, who *Authenticated) (*contracts.PasskeyList, error) {
	doc, err := s.own(ctx, who)
	if err != nil {
		return nil, err
	}
	out := &contracts.PasskeyList{Passkeys: []contracts.Passkey{}, PasskeyOnly: doc.PasskeyOnly, RecoveryCodesLeft: len(doc.RecoveryCodes)}
	for i := range doc.Passkeys {
		out.Passkeys = append(out.Passkeys, doc.Passkeys[i].view())
	}
	return out, nil
}

// passkeyID reads a passkey's identifier as the list writes it.
func passkeyID(id string) (contracts.Bytes, error) {
	var out contracts.Bytes
	if out.UnmarshalText([]byte(id)) != nil || len(out) == 0 {
		return nil, contracts.ErrNotFound
	}
	return out, nil
}

// RenamePasskey gives one of the account's passkeys another name.
func (s *Service) RenamePasskey(ctx context.Context, who *Authenticated, id string, in contracts.PasskeyRename) (*contracts.Passkey, error) {
	credential, err := passkeyID(id)
	if err != nil {
		return nil, err
	}
	name, err := domain.PasskeyName(in.Name)
	if err != nil {
		return nil, err
	}
	var view contracts.Passkey
	_, err = s.change(ctx, who.AccountID, func(doc *accountDoc) error {
		i := doc.passkey(credential)
		if i < 0 {
			return contracts.ErrNotFound
		}
		doc.Passkeys[i].Name = name
		view = doc.Passkeys[i].view()
		return nil
	})
	if err != nil {
		return nil, err
	}
	return &view, nil
}

// RemovePasskey takes a passkey out of the account: it signs nobody in from
// then on. The last one stays while the account signs in with passkeys only,
// or nothing but recovery codes would lead into it.
func (s *Service) RemovePasskey(ctx context.Context, who *Authenticated, id string) error {
	if err := s.confirmed(who); err != nil {
		return err
	}
	credential, err := passkeyID(id)
	if err != nil {
		return err
	}
	doc, err := s.change(ctx, who.AccountID, func(doc *accountDoc) error {
		i := doc.passkey(credential)
		if i < 0 {
			return contracts.ErrNotFound
		}
		if doc.PasskeyOnly && len(doc.Passkeys) == 1 {
			return contracts.ErrLastPasskey
		}
		doc.Passkeys = slices.Delete(doc.Passkeys, i, i+1)
		return nil
	})
	if err != nil {
		return err
	}
	s.cfg.Log.Info("accounts: passkey removed")
	s.noticeChange(ctx, doc, noticePasskeyRemoved)
	return nil
}

// ---- signing in with a passkey ----

// StartPasskeySignIn starts a sign-in with a passkey. Nothing is asked: the
// passkey says which account it is for, so the answer is the same for
// everybody and tells nobody who has an account.
func (s *Service) StartPasskeySignIn(ctx context.Context) (*contracts.PasskeyRequest, error) {
	id, challenge, err := s.begin(ctx, "")
	if err != nil {
		return nil, err
	}
	return &contracts.PasskeyRequest{Ceremony: id, Options: contracts.RequestOptions{
		Challenge: challenge, Timeout: int(domain.PasskeyCeremonyLifetime / time.Millisecond),
		RPID: s.rp.ID, UserVerification: passkeyRequired,
	}}, nil
}

// FinishPasskeySignIn ends a sign-in with the browser's answer, and signs the
// device in as signing in by mail does. A ceremony that ended, an account or
// a passkey that is not there and an answer that does not check are all the
// same error. An account that cannot be read is not: that is a failure.
func (s *Service) FinishPasskeySignIn(ctx context.Context, in contracts.PasskeySignIn, from Browser) (*contracts.Me, string, error) {
	challenge, ok, err := s.end(ctx, in.Ceremony, "")
	if err != nil {
		return nil, "", err
	}
	accountID := string(in.Credential.Response.UserHandle)
	if !ok || !validID("acc", accountID) {
		return nil, "", contracts.ErrInvalidPasskey
	}
	doc, err := s.account(ctx, accountID)
	if err != nil {
		return nil, "", err
	}
	if doc == nil {
		return nil, "", contracts.ErrInvalidPasskey
	}
	i := doc.passkey(in.Credential.RawID)
	if i < 0 {
		return nil, "", contracts.ErrInvalidPasskey
	}
	r := in.Credential.Response
	if _, err := s.rp.Asserted(challenge, doc.Passkeys[i].PublicKey, r.ClientDataJSON, r.AuthenticatorData, r.Signature); err != nil {
		s.refused("accounts: a sign-in with a passkey was refused", err)
		return nil, "", contracts.ErrInvalidPasskey
	}
	// The passkey proved who it is: an account the operator blocked is told
	// so here (enter), and never before.
	return s.enter(ctx, doc, wayPasskey, from, false)
}

// ---- "passkey only" and recovery codes ----

// recoveryHash is what is kept of a recovery code. The account is part of
// it, so a hash copied into another account's record is of no code.
func recoveryHash(accountID, code string) []byte {
	sum := sha256.Sum256([]byte("permaily recovery code v1\x00" + accountID + "\x00" + code))
	return sum[:]
}

// recoveryCode returns where in the record the hash of a recovery code is,
// or -1.
func (d *accountDoc) recoveryCode(hash []byte) int {
	return slices.IndexFunc(d.RecoveryCodes, func(kept []byte) bool { return subtle.ConstantTimeCompare(kept, hash) == 1 })
}

// newRecoveryCodes puts new recovery codes in doc, in place of the ones it
// had, and returns them as they are shown: this once.
func newRecoveryCodes(doc *accountDoc) []string {
	shown := make([]string, 0, domain.RecoveryCodes)
	doc.RecoveryCodes = nil
	for range domain.RecoveryCodes {
		code := randomString(domain.InvitationAlphabet, domain.RecoveryCodeLength)
		doc.RecoveryCodes = append(doc.RecoveryCodes, recoveryHash(doc.ID, code))
		shown = append(shown, domain.FormatRecoveryCode(code))
	}
	return shown
}

// errUnchanged is how a change that is not one leaves without a write.
var errUnchanged = errors.New("accounts: nothing to change")

// SetPasskeyOnly turns "passkey only" on or off. On, signing in by mail is
// off for the account and recovery codes are made; off, the codes are
// dropped. Asking for what is already so changes nothing: codes that were
// just shown keep working.
func (s *Service) SetPasskeyOnly(ctx context.Context, who *Authenticated, in contracts.PasskeyOnly) (*contracts.PasskeyOnlyState, error) {
	if err := s.confirmed(who); err != nil {
		return nil, err
	}
	out := &contracts.PasskeyOnlyState{PasskeyOnly: in.On, RecoveryCodes: []string{}}
	doc, err := s.change(ctx, who.AccountID, func(doc *accountDoc) error {
		// Codes made on a try that was not written are in no record.
		out.RecoveryCodes = []string{}
		if doc.PasskeyOnly == in.On {
			return errUnchanged
		}
		if doc.PasskeyOnly = in.On; !in.On {
			doc.RecoveryCodes = nil
			return nil
		}
		if len(doc.Passkeys) == 0 {
			return contracts.ErrPasskeyNeeded
		}
		out.RecoveryCodes = newRecoveryCodes(doc)
		return nil
	})
	if errors.Is(err, errUnchanged) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	s.cfg.Log.Info("accounts: passkey only changed", "on", in.On)
	if in.On {
		s.noticeChange(ctx, doc, noticePasskeyOnlyOn)
	} else {
		s.noticeChange(ctx, doc, noticePasskeyOnlyOff)
	}
	return out, nil
}

// NewRecoveryCodes makes ten new recovery codes for an account that signs in
// with passkeys only. The ones it had stop working.
func (s *Service) NewRecoveryCodes(ctx context.Context, who *Authenticated) (*contracts.RecoveryCodes, error) {
	if err := s.confirmed(who); err != nil {
		return nil, err
	}
	out := &contracts.RecoveryCodes{}
	doc, err := s.change(ctx, who.AccountID, func(doc *accountDoc) error {
		if !doc.PasskeyOnly {
			return contracts.ErrPasskeyOnlyOff
		}
		out.RecoveryCodes = newRecoveryCodes(doc)
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.noticeChange(ctx, doc, noticeRecoveryCodesNew)
	return out, nil
}

// recoveryTry reports whether a recovery code may be tried for address now,
// and counts the try when the code is wrong. Only addresses of accounts that
// have recovery codes are counted, so nobody fills the memory with addresses.
func (s *Service) recoveryTry(address string, right bool) bool {
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweep(now)
	list := recent(s.recoveries[address], now, time.Hour)
	if len(list) >= domain.RecoveryTriesPerHour || (len(list) == 0 && len(s.recoveries) >= maxRecoveryCounts) {
		return false
	}
	if !right {
		s.recoveries[address] = append(list, now)
	}
	return true
}

// Recover signs in with a recovery code and the account's address, and takes
// the code out of the account in the same change of its record: it works
// once. Every refusal is the same error, the one of a wrong code, whether
// the address has no account, the account takes no recovery codes, the code
// is wrong or too many wrong ones were tried.
func (s *Service) Recover(ctx context.Context, in contracts.Recovery, from Browser) (*contracts.Me, string, error) {
	address, err := domain.NormalizeAddress(in.Address)
	code, ok := domain.NormalizeRecoveryCode(in.Code)
	if err != nil || !ok {
		return nil, "", contracts.ErrInvalidCode
	}
	doc, _, err := s.lookup(ctx, address)
	if err != nil {
		return nil, "", err
	}
	if doc == nil {
		// The storage is asked as much as for an address that has an
		// account, so how long the answer takes does not tell them apart.
		if _, err := s.account(ctx, newID("acc")); err != nil {
			return nil, "", err
		}
		return nil, "", contracts.ErrInvalidCode
	}
	hash := recoveryHash(doc.ID, code)
	right := doc.recoveryCode(hash) >= 0
	if !doc.PasskeyOnly || !s.recoveryTry(address, right) || !right {
		return nil, "", contracts.ErrInvalidCode
	}
	// Of two uses of one code at the same time, here or in two enclaves, the
	// second finds it gone. A closing account takes the code too: its person
	// signs in to cancel. A blocked one takes none: the code was right, so
	// its person is told, and the code stays for when the account is unblocked.
	doc, err = s.rewrite(ctx, doc.ID, func(doc *accountDoc) error {
		i := doc.recoveryCode(hash)
		if i < 0 || !doc.PasskeyOnly || !doc.has(address) {
			return contracts.ErrInvalidCode
		}
		if doc.blocked() {
			return contracts.ErrAccountBlocked
		}
		doc.RecoveryCodes = slices.Delete(doc.RecoveryCodes, i, i+1)
		return nil
	})
	if errors.Is(err, contracts.ErrAccountBlocked) {
		return nil, "", err
	}
	var known *contracts.Error
	if errors.As(err, &known) {
		// Used a moment ago, or the account went away meanwhile: nothing a
		// caller may tell from a wrong code.
		return nil, "", contracts.ErrInvalidCode
	}
	if err != nil {
		return nil, "", err
	}
	s.cfg.Log.Info("accounts: recovery code used")
	me, cookie, err := s.enter(ctx, doc, wayRecovery, from, false)
	// Told whether or not the device then got its session: the code is gone.
	s.noticeChange(ctx, doc, noticeRecoveryCodeUsed)
	return me, cookie, err
}
