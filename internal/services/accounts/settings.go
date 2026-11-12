package accounts

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/domain"
	"github.com/permaily-org/permaily/internal/store"
)

// ---- the name ----

// UpdateAccount changes what a person may change of their account here: its
// name. The contact address changes another way, with a code sent to the new
// one (address.go).
func (s *Service) UpdateAccount(ctx context.Context, who *Authenticated, in contracts.AccountUpdate) (*contracts.Account, error) {
	name := ""
	if in.Name != nil {
		var err error
		if name, err = domain.AccountName(*in.Name); err != nil {
			return nil, err
		}
	}
	var view contracts.Account
	_, err := s.change(ctx, who.AccountID, func(doc *accountDoc) error {
		if in.Name == nil || doc.Name == name {
			view = s.view(doc)
			return errUnchanged
		}
		doc.Name = name
		view = s.view(doc)
		return nil
	})
	if err != nil && !errors.Is(err, errUnchanged) {
		return nil, err
	}
	return &view, nil
}

// ---- the two lists ----

// The accounts are found through two lists: every account, for the operator's
// tool, and the ones that are closing, for the enclave to find those that are
// due. Their entries are pointers, signed where they are stored, and prove
// nothing: what is shown or removed is the signed record an entry names, so
// one written by hand, or an old one put back, lists nothing and removes
// nothing.
const (
	accountsPartition = "accounts"
	closingPartition  = "closing"
	// timedKeyLayout puts the entries of a list in the order of their time,
	// to the nanosecond: every time is as long as every other.
	timedKeyLayout = "20060102T150405.000000000Z"
)

// timedKey is the key of a list's entry: a time, and the account.
func timedKey(at time.Time, accountID string) string {
	return at.UTC().Format(timedKeyLayout) + "." + accountID
}

func parseTimedKey(key string) (time.Time, string, bool) {
	stamp, id, _ := strings.Cut(key, "Z.")
	at, err := time.Parse(timedKeyLayout, stamp+"Z")
	if err != nil || !validID("acc", id) {
		return time.Time{}, "", false
	}
	return at, id, true
}

// listKey is where an account is in the list of accounts: by when it was made.
func listKey(doc *accountDoc) string { return timedKey(doc.CreatedAt, doc.ID) }

// closingKey is where an account is in the list of those that are closing: by
// the day it is removed.
func closingKey(doc *accountDoc) string { return timedKey(doc.ClosesAt, doc.ID) }

// ---- deleting an account ----

// StartDeletion is the person asking to delete their account. It is closing
// from now and removed in seven days; until then they can sign in and cancel,
// and nothing else of it changes. Every other device is signed out. Asking
// again keeps the day, and who asked first, and signs the others out again.
func (s *Service) StartDeletion(ctx context.Context, who *Authenticated) (*contracts.Account, error) {
	if err := s.confirmed(who); err != nil {
		return nil, err
	}
	changed := false
	edit := s.closing(ctx, contracts.ClosingByPerson)
	doc, err := s.rewrite(ctx, who.AccountID, func(doc *accountDoc) error {
		if doc.blocked() || !doc.stands(&sessionDoc{ID: who.Session.ID, SignOuts: who.signOuts}) {
			return contracts.ErrUnauthorized // its session stands for nothing
		}
		err := edit(doc)
		if changed = err == nil; err != nil && !errors.Is(err, errUnchanged) {
			return err
		}
		// In the same change: a device that signs in through another enclave
		// meanwhile was signed in before it, and is signed out with the rest.
		doc.signOut(who.Session.ID)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if changed {
		s.cfg.Log.Info("accounts: account closing")
	}
	// The record says they stand for nothing; their entries go too.
	err = s.closeOthers(ctx, who)
	if changed {
		s.notify(ctx, noticeDeletionAsked, deletionAskedMail(doc, s.rp.ID, s.now()))
	}
	if err != nil {
		return nil, err
	}
	view := s.view(doc)
	return &view, nil
}

// closing returns the edit that makes an account closing, asked for by its
// person or by the operator: removed in seven days, counted from now. One
// that is closing already stays as it is (errUnchanged).
func (s *Service) closing(ctx context.Context, by string) func(*accountDoc) error {
	// The day is set once: the edit may run more than once.
	closes := s.now().Add(domain.AccountClosing).Truncate(time.Second)
	return func(doc *accountDoc) error {
		if doc.State == contracts.AccountClosing {
			return errUnchanged
		}
		doc.State, doc.ClosesAt, doc.ClosingBy = contracts.AccountClosing, closes, by
		// The entry goes in before the record: one the record does not bear
		// out is dropped when it is found, where a closing account without
		// one would be found only when someone uses it.
		return s.listClosing(ctx, doc)
	}
}

// listClosing writes the entry of an account in the list of closing accounts.
// One that is there already is written again, so that it is another entry:
// it may be of a deletion that was just cancelled, in this enclave or in
// another, whose entry is about to be deleted as it was read then
// (CancelDeletion), and this account would be closing and not in the list.
func (s *Service) listClosing(ctx context.Context, doc *accountDoc) error {
	key := closingKey(doc)
	for range maxRewrites {
		err := s.point(ctx, closingPartition, key, doc.ID, 0)
		if !errors.Is(err, store.ErrExists) {
			return err
		}
		e, err := s.cfg.Entries.GetEntry(ctx, closingPartition, key)
		if errors.Is(err, store.ErrNotFound) {
			continue // deleted meanwhile
		}
		if err != nil {
			return err
		}
		if err := s.point(ctx, closingPartition, key, doc.ID, e.Version); !errors.Is(err, store.ErrConflict) {
			return err
		}
	}
	return errors.New("accounts: an entry of the list of closing accounts keeps changing")
}

// closeOthers signs out every device of the account but the one that asks.
func (s *Service) closeOthers(ctx context.Context, who *Authenticated) error {
	return s.closeSessions(ctx, who.AccountID, who.Session.ID)
}

// maxSessionPages bounds one pass over the sessions of an account, which
// keeps maxSessions of them.
const maxSessionPages = 10

// closeSessions signs out every device of an account but the one whose
// session is keep ("" for none).
func (s *Service) closeSessions(ctx context.Context, accountID, keep string) error {
	after := ""
	for range maxSessionPages {
		entries, err := s.cfg.Entries.ListEntries(ctx, accountPartition(accountID), sessionPrefix, after, 2*maxSessions)
		if err != nil {
			return err
		}
		for i := range entries {
			if after = entries[i].Key; after == sessionPrefix+keep {
				continue
			}
			if err := s.forget(ctx, entries[i].Partition, entries[i].Key); err != nil {
				return err
			}
		}
		if len(entries) < 2*maxSessions {
			break
		}
	}
	return nil
}

// CancelDeletion keeps an account that was closing: it is active again. One
// that is not closing stays as it is. The person can cancel a deletion the
// operator asked for too, unless the account is blocked: then they are not
// signed in at all.
func (s *Service) CancelDeletion(ctx context.Context, who *Authenticated) (*contracts.Account, error) {
	doc, _, err := s.keep(ctx, who.AccountID, contracts.ClosingByPerson, nil)
	if err != nil {
		return nil, err
	}
	view := s.view(doc)
	return &view, nil
}

// keep makes a closing account active again, for its person or for the
// operator (by), and reports whether it was closing. The operator, and an
// admin, cancel only a deletion the operator asked for: one the person asked
// for is theirs to cancel (ErrNotCancellable). guard, when there is one,
// refuses an account the one who acts may not act on.
func (s *Service) keep(ctx context.Context, id, by string, guard func(*accountDoc) error) (*accountDoc, bool, error) {
	var same accountDoc
	var entry *store.Entry
	doc, err := s.rewrite(ctx, id, func(doc *accountDoc) error {
		if by == contracts.ClosingByPerson && doc.blocked() {
			return contracts.ErrUnauthorized // its session stands for nothing
		}
		if guard != nil {
			if err := guard(doc); err != nil {
				return err
			}
		}
		if doc.State != contracts.AccountClosing {
			same = *doc
			return errUnchanged
		}
		if by == contracts.ClosingByOperator && doc.closingBy() != contracts.ClosingByOperator {
			return contracts.ErrNotCancellable
		}
		// The entry is read before the record says the account is kept, and
		// deleted below only as it was read: once the record says so, the
		// person can ask to delete again, and the entry that is there then is
		// of that (listClosing).
		entry = nil
		if e, err := s.cfg.Entries.GetEntry(ctx, closingPartition, closingKey(doc)); err == nil {
			entry = e
		} else if !errors.Is(err, store.ErrNotFound) {
			return err
		}
		doc.State, doc.ClosesAt, doc.ClosingBy = contracts.AccountActive, time.Time{}, ""
		return nil
	})
	if errors.Is(err, errUnchanged) {
		return &same, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	// The record says it is kept, so the entry is nothing now. If it stays,
	// it is dropped when the enclave next goes through the list.
	if entry != nil {
		if err := s.cfg.Entries.DeleteEntryAt(ctx, entry); err != nil && !errors.Is(err, store.ErrConflict) {
			s.cfg.Log.Warn("accounts: an entry of the list of closing accounts could not be deleted")
		}
	}
	s.cfg.Log.Info("accounts: account kept")
	s.notify(ctx, noticeDeletionCancelled, deletionCancelledMail(doc.Address, s.rp.ID, s.now(), by))
	return doc, true, nil
}

// ---- removing an account ----

// remove removes an account that is due.
func (s *Service) remove(ctx context.Context, id string) error {
	s.changing.Lock()
	defer s.changing.Unlock()
	for range maxRewrites {
		doc, at, err := s.readAt(ctx, id)
		if err != nil || doc == nil || !doc.due(s.now()) {
			return err // nil when another request removed it first
		}
		if err := s.removeAt(ctx, doc, at); !errors.Is(err, store.ErrChanged) {
			return err
		}
	}
	return errors.New("accounts: an account's record keeps changing")
}

// removeAt removes an account that is due, read at a mark, with changing
// held. The record first says it is being removed (Removing), at the mark it
// was read at: a cancel that another enclave wrote since makes that write
// fail with store.ErrChanged, and the caller reads again and decides again.
// Once it says so nothing takes it back, so a cancel that comes after it is
// not answered as kept: it finds the account due, and removes it too.
func (s *Service) removeAt(ctx context.Context, doc *accountDoc, at store.Mark) error {
	if !doc.Removing {
		doc.Removing = true
		if err := s.putAccountAt(ctx, doc, at); err != nil {
			return err
		}
	}
	return s.removeLocked(ctx, doc)
}

// removeLocked removes an account that is due, with changing held. It goes
// in this order: the pointers from its address and from its provider
// accounts, its entry in the list of accounts, its sessions, its record, its
// entry in the list of closing accounts.
//
// The record goes after everything that is found through it: it says the
// address and the times the other entries are stored under, and while it is
// there it says the account is due, so nothing uses the account and every use
// and every pass over the list tries the removal again from the start. The
// record itself goes with a commit (store.Anchored.RemoveRecord): from that
// moment the account is not there, whatever of it is still stored or is put
// back. The entry in the list of closing accounts goes last, so the enclave
// keeps finding the account until nothing of it is left.
func (s *Service) removeLocked(ctx context.Context, doc *accountDoc) error {
	key, err := s.signingKey(ctx)
	if err != nil {
		return err
	}
	// The pointer is deleted only if it still leads here: the address may
	// have signed up again after an earlier try got this far. The pointers
	// from the provider accounts go the same way, and the one of a link that
	// stopped halfway.
	if err := s.unpoint(ctx, key, loginPartition(doc.Address), doc.ID); err != nil {
		return err
	}
	// And the one from an address it was moving to, if a change of address
	// stopped halfway (address.go).
	if doc.MovingTo != "" {
		if err := s.unpoint(ctx, key, loginPartition(doc.MovingTo), doc.ID); err != nil {
			return err
		}
	}
	for _, p := range doc.Providers {
		if err := s.unpoint(ctx, key, providerPartition(p.Provider, p.Subject), doc.ID); err != nil {
			return err
		}
	}
	if l := doc.Linking; l != nil {
		if err := s.unpoint(ctx, key, providerPartition(l.Provider, l.Subject), doc.ID); err != nil {
			return err
		}
	}
	if err := s.forget(ctx, accountsPartition, listKey(doc)); err != nil {
		return err
	}
	if err := s.forget(ctx, adminsPartition, doc.ID); err != nil {
		return err
	}
	if err := s.clear(ctx, doc.ID, closingKey(doc)); err != nil {
		return err
	}
	s.cfg.Log.Info("accounts: account removed")
	return nil
}

// unpoint deletes the pointer of a partition if it leads to the account. One
// that leads elsewhere is another account's now, or nobody's.
func (s *Service) unpoint(ctx context.Context, key []byte, partition, accountID string) error {
	pointer, err := s.cfg.Entries.GetEntry(ctx, partition, loginKey)
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil || pointed(key, pointer) != accountID {
		return err
	}
	// Only the pointer that was read: another enclave may have ended this
	// removal meanwhile, and the address may have an account again, whose
	// pointer is where this one was.
	if err := s.cfg.Entries.DeleteEntryAt(ctx, pointer); err != nil && !errors.Is(err, store.ErrConflict) {
		return err
	}
	return nil
}

// clear removes what is stored under an account's identifier: its sessions
// and its record, and then the entry of the list of closing accounts that
// led here. It is also what is left to do for an account whose record is
// gone already, when an earlier removal failed after the commit.
func (s *Service) clear(ctx context.Context, id, closing string) error {
	if err := s.cfg.Entries.DeletePartition(ctx, accountPartition(id)); err != nil {
		return err
	}
	if err := s.cfg.Records.RemoveRecord(ctx, recordName(id)); err != nil {
		return err
	}
	return s.forget(ctx, closingPartition, closing)
}

// RemoveEvery is how often an enclave looks for accounts that are due. One
// that is used is removed at once in any case.
const RemoveEvery = 10 * time.Minute

// What one pass over the list of closing accounts reads.
const (
	closingPage     = 100
	maxClosingPages = 100
)

// Run removes the accounts that are due, every so often, until ctx ends.
func (s *Service) Run(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if err := s.RemoveDue(ctx); err != nil && ctx.Err() == nil {
			s.cfg.Log.Error("accounts: removing the accounts that are due", "err", scrub(err))
		}
	}
}

// RemoveDue goes through the list of closing accounts, which is in the order
// of their days, and removes the ones whose day has come. Each entry is
// checked against the record it names: an entry does nothing by itself.
func (s *Service) RemoveDue(ctx context.Context) error {
	key, err := s.signingKey(ctx)
	if err != nil {
		return err
	}
	now, after, failed := s.now(), "", 0
	var first error
pages:
	for range maxClosingPages {
		entries, err := s.cfg.Entries.ListEntries(ctx, closingPartition, "", after, closingPage)
		if err != nil {
			return err
		}
		for i := range entries {
			e := &entries[i]
			after = e.Key
			day, id, ok := parseTimedKey(e.Key)
			if !ok || pointed(key, e) != id {
				continue // not written by this deployment, or not here
			}
			if day.After(now) {
				break pages // nor is any after it
			}
			if err := s.settle(ctx, id, e.Key); err != nil {
				if failed++; first == nil {
					first = err
				}
			}
		}
		if len(entries) < closingPage {
			break
		}
	}
	if failed > 0 {
		return fmt.Errorf("accounts: %d accounts that are due were not removed, the first because: %w", failed, first)
	}
	return nil
}

// settle does what an entry of the list of closing accounts whose day has
// come asks for, as far as the record of its account bears it out.
func (s *Service) settle(ctx context.Context, id, entry string) error {
	s.changing.Lock()
	defer s.changing.Unlock()
	for range maxRewrites {
		doc, at, err := s.readAt(ctx, id)
		switch {
		case err != nil:
			return err
		case doc == nil:
			// Removed already. A removal that failed after the record was
			// committed as gone left the rest for now.
			return s.clear(ctx, id, entry)
		case doc.State != contracts.AccountClosing || closingKey(doc) != entry:
			// The person cancelled, or asked again later: the entry is of no day.
			return s.forget(ctx, closingPartition, entry)
		case !doc.due(s.now()):
			return nil
		}
		if err := s.removeAt(ctx, doc, at); !errors.Is(err, store.ErrChanged) {
			return err
		}
	}
	return errors.New("accounts: an account's record keeps changing")
}

// ---- the operator's tool ----

// record is an account as the operator's tool shows it.
func (s *Service) record(ctx context.Context, doc *accountDoc) (*contracts.AccountRecord, error) {
	sessions, err := s.sessions(ctx, doc.ID, doc)
	if err != nil {
		return nil, err
	}
	// Which providers, and never which account at each: the subject stays in
	// the record.
	providers := []string{}
	for _, p := range doc.Providers {
		providers = append(providers, string(p.Provider))
	}
	return &contracts.AccountRecord{
		ID: doc.ID, State: doc.State, Address: doc.Address, Name: doc.Name, Language: doc.Language,
		CreatedAt: doc.CreatedAt, AddressVerifiedAt: doc.AddressVerifiedAt, ClosesAt: doc.ClosesAt,
		ClosingBy: doc.closingBy(), BlockedAt: doc.BlockedAt,
		Passkeys: len(doc.Passkeys), PasskeyOnly: doc.PasskeyOnly, Providers: providers, Sessions: len(sessions),
		Admin: s.role(doc), Terms: doc.agreedTo(),
	}, nil
}

// AccountRecord returns one account for the operator's tool, from its signed
// record. It only looks: an account that is due is shown as it is, and
// removed by the enclave, not by being looked at.
func (s *Service) AccountRecord(ctx context.Context, id string) (*contracts.AccountRecord, error) {
	if !validID("acc", id) {
		return nil, contracts.ErrNotFound
	}
	doc, err := s.read(ctx, id)
	if err != nil {
		return nil, err
	}
	if doc == nil {
		return nil, contracts.ErrNotFound
	}
	return s.record(ctx, doc)
}

// AccountRecords returns a page of the accounts for the operator's tool, the
// oldest first, starting after the place a page before ended at ("" for the
// first). Each is read from its signed record: an entry of the list that
// names no account shows nothing, and one whose account cannot be read is
// counted, never taken for an account that is not there.
func (s *Service) AccountRecords(ctx context.Context, after string, limit int) (*contracts.AccountList, error) {
	if limit < 1 || limit > domain.MaxAccountsPage {
		return nil, contracts.ErrInvalidLimit
	}
	if _, _, ok := parseTimedKey(after); after != "" && !ok {
		return nil, contracts.ErrInvalidCursor
	}
	key, err := s.signingKey(ctx)
	if err != nil {
		return nil, err
	}
	entries, err := s.cfg.Entries.ListEntries(ctx, accountsPartition, "", after, limit)
	if err != nil {
		return nil, err
	}
	out := &contracts.AccountList{Accounts: []contracts.AccountRecord{}}
	for i := range entries {
		_, id, ok := parseTimedKey(entries[i].Key)
		if !ok || pointed(key, &entries[i]) != id {
			continue
		}
		doc, err := s.read(ctx, id)
		if err == nil && doc == nil {
			continue // removed since, or never made
		}
		var rec *contracts.AccountRecord
		if err == nil {
			rec, err = s.record(ctx, doc)
		}
		if err != nil {
			out.Unreadable++
			continue
		}
		out.Accounts = append(out.Accounts, *rec)
	}
	if len(entries) == limit {
		out.Next = entries[len(entries)-1].Key
	}
	if out.Unreadable > 0 {
		s.cfg.Log.Error("accounts: records of accounts could not be read for the operator's tool", "count", out.Unreadable)
	}
	return out, nil
}
