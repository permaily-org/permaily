package accounts

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/domain"
	"github.com/permaily-org/permaily/internal/envelope"
	"github.com/permaily-org/permaily/internal/store"
)

// What the operator does to accounts (ADR 0017): block and unblock one, ask
// to delete one and cancel that, sign its devices out, find one by its
// address, and list and revoke the invitations not used yet.
//
// What is not here is on purpose, and nothing here does it by another name:
// the operator changes no contact address, adds and removes no way to sign
// in, does not turn "passkey only" off and signs in as nobody. Each of those
// would be a way into an account. So an account whose person lost their mail,
// their passkeys and their recovery codes cannot be given back by support.
//
// Everything here is counted, by its kind and nothing else, for /status: what
// the operator does through the enclave is public (OperatorActions).

// The kinds of what the operator does through the enclave.
const (
	ActionBlock             = "block"
	ActionUnblock           = "unblock"
	ActionDelete            = "delete"
	ActionDeleteCancel      = "delete_cancel"
	ActionSessionsClose     = "sessions_close"
	ActionInvitationsMade   = "invitations_made"
	ActionInvitationRevoked = "invitation_revoked"
	ActionProviderAppSet    = "provider_app_set"
	ActionProviderAppRemove = "provider_app_removed"
	// ActionAdminMade and ActionAdminRemoved: the super admin made an account
	// an admin, or made it stop being one (admins.go).
	ActionAdminMade    = "admin_made"
	ActionAdminRemoved = "admin_removed"
)

// OperatorActionKinds lists every kind, in the order they are shown.
var OperatorActionKinds = []string{
	ActionBlock, ActionUnblock, ActionDelete, ActionDeleteCancel, ActionSessionsClose,
	ActionInvitationsMade, ActionInvitationRevoked, ActionProviderAppSet, ActionProviderAppRemove,
	ActionAdminMade, ActionAdminRemoved,
}

// acted counts n actions of a kind and logs the kind. Never whose account it
// was: the count and the log are public.
func (s *Service) acted(kind string, n int) {
	if n < 1 {
		return
	}
	s.mu.Lock()
	s.actions[kind] += n
	s.mu.Unlock()
	s.cfg.Log.Info("accounts: the operator acted", "kind", kind, "count", n)
}

// OperatorActions says since when this enclave counts what the operator does
// through it, and how many of each kind it did: every kind, also the ones
// that never happened. The count is in memory: a restart starts it again.
func (s *Service) OperatorActions() (time.Time, map[string]int) {
	out := map[string]int{}
	for _, kind := range OperatorActionKinds {
		out[kind] = 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	maps.Copy(out, s.actions)
	return s.since, out
}

// operate is rewrite for the operator and the admins: the account is named
// by its identifier, one that is not there is ErrNotFound, and an edit that
// finds nothing to change (errUnchanged) hands back the account as it is. It
// reports whether the record was written. guard, unless it is nil, refuses
// an account the one who acts may not act on, before edit runs.
func (s *Service) operate(ctx context.Context, id string, guard, edit func(*accountDoc) error) (*accountDoc, bool, error) {
	if !validID("acc", id) {
		return nil, false, contracts.ErrNotFound
	}
	var same accountDoc
	doc, err := s.rewrite(ctx, id, func(doc *accountDoc) error {
		same = *doc
		if guard != nil {
			if err := guard(doc); err != nil {
				return err
			}
		}
		return edit(doc)
	})
	switch {
	case errors.Is(err, errUnchanged):
		return &same, false, nil
	case errors.Is(err, contracts.ErrUnauthorized):
		return nil, false, contracts.ErrNotFound
	case err != nil:
		return nil, false, err
	}
	return doc, true, nil
}

// BlockAccount blocks an account: from now nobody signs in to it, by any way,
// and every device is signed out. Its record says so with the time. Blocking
// is apart from deleting: a blocked account can be closing, and stays blocked
// until it is unblocked or removed.
func (s *Service) BlockAccount(ctx context.Context, id string) (*contracts.AccountRecord, error) {
	return s.block(ctx, nil, id)
}

func (s *Service) block(ctx context.Context, by *acting, id string) (*contracts.AccountRecord, error) {
	if err := by.changing(); err != nil {
		return nil, err
	}
	at := s.now().Truncate(time.Second)
	doc, changed, err := s.operate(ctx, id, by.guard(s, ActionBlock), func(doc *accountDoc) error {
		if doc.blocked() {
			return errUnchanged
		}
		doc.BlockedAt = at
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.acted(ActionBlock, 1)
	s.logged(ctx, by, ActionBlock, doc.ID, "")
	// Also when it was blocked already: an earlier try may have failed here.
	// A session written after this stands for nothing all the same: every
	// request reads the record.
	if err := s.closeSessions(ctx, id, ""); err != nil {
		return nil, err
	}
	if changed {
		s.notify(ctx, noticeBlocked, blockedMail(doc.Address, s.rp.ID, at, true))
	}
	return s.record(ctx, doc)
}

// UnblockAccount lets the account's person sign in again.
func (s *Service) UnblockAccount(ctx context.Context, id string) (*contracts.AccountRecord, error) {
	return s.unblock(ctx, nil, id)
}

func (s *Service) unblock(ctx context.Context, by *acting, id string) (*contracts.AccountRecord, error) {
	if err := by.changing(); err != nil {
		return nil, err
	}
	doc, changed, err := s.operate(ctx, id, by.guard(s, ActionUnblock), func(doc *accountDoc) error {
		if !doc.blocked() {
			return errUnchanged
		}
		doc.BlockedAt = time.Time{}
		return nil
	})
	if err != nil {
		return nil, err
	}
	s.acted(ActionUnblock, 1)
	s.logged(ctx, by, ActionUnblock, doc.ID, "")
	if changed {
		s.notify(ctx, noticeUnblocked, blockedMail(doc.Address, s.rp.ID, s.now(), false))
	}
	return s.record(ctx, doc)
}

// DeleteAccount is the operator asking to delete an account: the same seven
// days, and the same removal, as when its person asks. The record says the
// operator asked (an admin asks as the operator). No device is signed out:
// the person sees the account closing and can cancel, unless the account is
// blocked. One that is closing already keeps its day and who asked.
func (s *Service) DeleteAccount(ctx context.Context, id string) (*contracts.AccountRecord, error) {
	return s.deleteAccount(ctx, nil, id)
}

func (s *Service) deleteAccount(ctx context.Context, by *acting, id string) (*contracts.AccountRecord, error) {
	if err := by.changing(); err != nil {
		return nil, err
	}
	doc, changed, err := s.operate(ctx, id, by.guard(s, ActionDelete), s.closing(ctx, contracts.ClosingByOperator))
	if err != nil {
		return nil, err
	}
	s.acted(ActionDelete, 1)
	s.logged(ctx, by, ActionDelete, doc.ID, "")
	if changed {
		s.cfg.Log.Info("accounts: account closing")
		s.notify(ctx, noticeDeletionAsked, deletionAskedMail(doc, s.rp.ID, s.now()))
	}
	return s.record(ctx, doc)
}

// KeepAccount is the operator cancelling the deletion of an account that the
// operator asked for: the account is active again. A deletion its person
// asked for is theirs to cancel (ErrNotCancellable), and nothing changes.
func (s *Service) KeepAccount(ctx context.Context, id string) (*contracts.AccountRecord, error) {
	return s.keepAccount(ctx, nil, id)
}

func (s *Service) keepAccount(ctx context.Context, by *acting, id string) (*contracts.AccountRecord, error) {
	if err := by.changing(); err != nil {
		return nil, err
	}
	if !validID("acc", id) {
		return nil, contracts.ErrNotFound
	}
	doc, _, err := s.keep(ctx, id, contracts.ClosingByOperator, by.guard(s, ActionDeleteCancel))
	if errors.Is(err, contracts.ErrUnauthorized) {
		return nil, contracts.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	s.acted(ActionDeleteCancel, 1)
	s.logged(ctx, by, ActionDeleteCancel, doc.ID, "")
	return s.record(ctx, doc)
}

// CloseAccountSessions signs every device out of an account.
func (s *Service) CloseAccountSessions(ctx context.Context, id string) (*contracts.AccountRecord, error) {
	return s.closeAccountSessions(ctx, nil, id)
}

func (s *Service) closeAccountSessions(ctx context.Context, by *acting, id string) (*contracts.AccountRecord, error) {
	if err := by.changing(); err != nil {
		return nil, err
	}
	doc, _, err := s.operate(ctx, id, by.guard(s, ActionSessionsClose), func(*accountDoc) error { return errUnchanged })
	if err != nil {
		return nil, err
	}
	s.acted(ActionSessionsClose, 1)
	s.logged(ctx, by, ActionSessionsClose, doc.ID, "")
	if err := s.closeSessions(ctx, id, ""); err != nil {
		return nil, err
	}
	return s.record(ctx, doc)
}

// AccountRecordByAddress returns the account of exactly that address, read as
// a sign-in reads it, as a list of none or one. Like the rest of the
// operator's looking, it removes nothing: an account that is due is shown.
func (s *Service) AccountRecordByAddress(ctx context.Context, address string) (*contracts.AccountList, error) {
	address, err := domain.NormalizeAddress(address)
	if err != nil {
		return nil, err
	}
	out := &contracts.AccountList{Accounts: []contracts.AccountRecord{}}
	id, err := s.pointer(ctx, address)
	if err != nil {
		return nil, err
	}
	if id == "" {
		return out, nil
	}
	doc, err := s.read(ctx, id)
	if err == nil && (doc == nil || !doc.has(address)) {
		return out, nil // the pointer leads nowhere
	}
	var rec *contracts.AccountRecord
	if err == nil {
		rec, err = s.record(ctx, doc)
	}
	if err != nil {
		// There, and nothing can be said of it: never taken for none.
		out.Unreadable = 1
		s.cfg.Log.Error("accounts: records of accounts could not be read for the operator's tool", "count", 1)
		return out, nil
	}
	out.Accounts = append(out.Accounts, *rec)
	return out, nil
}

// find returns the account of an address as a sign-in finds it, and only
// looks: an account that is due is returned and not removed. nil when the
// address has none, or its pointer leads nowhere; an error when the account
// is there and cannot be read.
func (s *Service) find(ctx context.Context, address string) (*accountDoc, error) {
	id, err := s.pointer(ctx, address)
	if err != nil || id == "" {
		return nil, err
	}
	doc, err := s.read(ctx, id)
	if err != nil {
		return nil, err
	}
	if doc == nil || !doc.has(address) {
		return nil, nil // the pointer leads nowhere
	}
	return doc, nil
}

// pointer returns the account the pointer of an address names, "" when there
// is none that this deployment wrote there.
func (s *Service) pointer(ctx context.Context, address string) (string, error) {
	e, err := s.cfg.Entries.GetEntry(ctx, loginPartition(address), loginKey)
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
	return pointed(key, e), nil
}

// ---- invitations ----

// The invitations are listed for the operator under identifiers of their own.
// An entry of the list is a pointer, signed where it is stored, to where the
// invitation is kept (by the hash of its code). It proves nothing: what is
// shown is the invitation it leads to, if that is still there and not ended.
const invitationsPartition = "invitations"

// invitedDoc is an entry of the list of invitations.
type invitedDoc struct {
	V  int    `json:"v"`
	ID string `json:"id"`
	// Invitation is the partition the invitation is kept in.
	Invitation string    `json:"invitation"`
	CreatedAt  time.Time `json:"created_at"`
}

// listed returns what an entry of the list of invitations says, or nil when
// the entry is not one this deployment wrote where it is.
func listed(key []byte, e *store.Entry) *invitedDoc {
	var doc invitedDoc
	raw, err := envelope.Verified(key, e.Value, entryAAD(e.Partition, e.Key))
	if err != nil || json.Unmarshal(raw, &doc) != nil || doc.ID != e.Key || !validID("inv", doc.ID) || !strings.HasPrefix(doc.Invitation, "invitation#") {
		return nil
	}
	return &doc
}

const invitationsPage = 100

// Invitations lists the invitations that were not used yet, the oldest first:
// when each was made, when it ends, and its identifier. Never a code: only
// the hash of one is kept. An invitation that was used was deleted when it
// was used, so it is not listed, and neither is one that was revoked or
// ended. The invitations made before there was a list are not in it.
func (s *Service) Invitations(ctx context.Context) (*contracts.InvitationList, error) {
	key, err := s.signingKey(ctx)
	if err != nil {
		return nil, err
	}
	out := &contracts.InvitationList{Invitations: []contracts.Invitation{}}
	now, after := s.now(), ""
	for read := 0; ; read += invitationsPage {
		entries, err := s.cfg.Entries.ListEntries(ctx, invitationsPartition, "", after, invitationsPage)
		if err != nil {
			return nil, err
		}
		if read >= domain.MaxInvitationsListed {
			out.Truncated = len(entries) > 0
			break
		}
		for i := range entries {
			e := &entries[i]
			after = e.Key
			doc := listed(key, e)
			if doc == nil {
				continue
			}
			inv, err := s.cfg.Entries.GetEntry(ctx, doc.Invitation, invitationKey)
			if errors.Is(err, store.ErrNotFound) {
				// Used, or revoked: the entry of the list is nothing now.
				_ = s.cfg.Entries.DeleteEntryAt(ctx, e)
				continue
			}
			if err != nil {
				return nil, err
			}
			var kept invitationDoc
			raw, err := envelope.Verified(key, inv.Value, entryAAD(doc.Invitation, invitationKey))
			// The times come from what was signed, as when one is used.
			if err != nil || json.Unmarshal(raw, &kept) != nil || kept.ID != doc.ID || !kept.CreatedAt.Add(domain.InvitationLifetime).After(now) {
				continue
			}
			out.Invitations = append(out.Invitations, contracts.Invitation{
				ID: doc.ID, CreatedAt: kept.CreatedAt, ExpiresAt: kept.CreatedAt.Add(domain.InvitationLifetime),
			})
		}
		if len(entries) < invitationsPage {
			break
		}
	}
	slices.SortFunc(out.Invitations, func(a, b contracts.Invitation) int {
		return cmp.Or(a.CreatedAt.Compare(b.CreatedAt), cmp.Compare(a.ID, b.ID))
	})
	return out, nil
}

// RevokeInvitation makes an invitation that was not used yet be of no use:
// it is deleted, as using it deletes it. A sign-up that was started with it
// and has not ended finds it gone. One that was used, or revoked, or never
// made, is ErrNotFound.
func (s *Service) RevokeInvitation(ctx context.Context, id string) error {
	return s.revokeInvitation(ctx, nil, id)
}

func (s *Service) revokeInvitation(ctx context.Context, by *acting, id string) error {
	if err := by.changing(); err != nil {
		return err
	}
	if !validID("inv", id) {
		return contracts.ErrNotFound
	}
	e, err := s.cfg.Entries.GetEntry(ctx, invitationsPartition, id)
	if errors.Is(err, store.ErrNotFound) {
		return contracts.ErrNotFound
	}
	if err != nil {
		return err
	}
	key, err := s.signingKey(ctx)
	if err != nil {
		return err
	}
	doc := listed(key, e)
	if doc == nil {
		return contracts.ErrNotFound
	}
	// Of a sign-up that uses it and this, only one deletes it.
	err = s.cfg.Entries.DeleteEntry(ctx, doc.Invitation, invitationKey)
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	revoked := err == nil
	if revoked {
		s.acted(ActionInvitationRevoked, 1)
		s.logged(ctx, by, ActionInvitationRevoked, "", id)
	}
	if err := s.forget(ctx, invitationsPartition, id); err != nil {
		return err
	}
	if !revoked {
		return contracts.ErrNotFound
	}
	return nil
}
