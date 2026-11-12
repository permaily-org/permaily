package mailboxes

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/domain"
	"github.com/permaily-org/permaily/internal/store"
)

// What accounts asks (accounts.Mailboxes): the account's removal, and the
// operator's half, which accounts wraps for the operator and the admins.

// RemoveAccount implements accounts.Mailboxes (ADR 0026, "Accounts removed,
// closing and blocked").
func (s *Service) RemoveAccount(ctx context.Context, account string) error {
	// Two enclaves removing one account each take the list's next version in
	// turn; every turn ends what is left, so a few more tries than a change
	// needs are enough to see it through.
	for range maxTries + MaxMailboxes {
		list, at, err := s.loadList(ctx, account)
		if errors.Is(err, store.ErrNotFound) {
			// No list: make it closed, so a first creation in flight loses.
			err := s.createList(ctx, &listDoc{Account: account, State: listClosed})
			if err == nil {
				return nil
			}
			if !errors.Is(err, store.ErrExists) {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		if list.State != listClosed {
			list.State, list.Made = listClosed, nil
			if err := s.putList(ctx, list, at); err != nil && !errors.Is(err, store.ErrChanged) {
				return err
			}
			continue // enumerate the list as it is now
		}
		kept, err := s.endAll(ctx, list)
		if errors.Is(err, store.ErrChanged) {
			continue
		}
		if err != nil {
			return err
		}
		if len(kept) != len(list.Mailboxes) {
			list.Mailboxes = kept
			if err := s.putList(ctx, list, at); errors.Is(err, store.ErrChanged) {
				continue
			} else if err != nil {
				return err
			}
		}
		s.wake()
		return nil
	}
	return errors.New("mailboxes: an account's list keeps changing")
}

// endAll ends every mailbox a closed list names, each in a terminal commit,
// and returns what stays named: the forked ones.
func (s *Service) endAll(ctx context.Context, list *listDoc) ([]listEntry, error) {
	var kept []listEntry
	for _, e := range list.Mailboxes {
		l, err := s.load(ctx, e.ID)
		if err != nil {
			return nil, err
		}
		if l.state == stNone {
			// A reservation, fenced; or a record removed already.
			if err := s.tombstone(ctx, e.ID, list.Account); err != nil && !errors.Is(err, store.ErrExists) {
				return nil, err
			}
			if l, err = s.load(ctx, e.ID); err != nil {
				return nil, err
			}
		}
		for try := 0; l.state == stActive; try++ {
			// Another enclave writing the record first (ending it too) is
			// read again and decided again.
			if _, err := s.disconnect(ctx, l, byAccountRemoved); err != nil && (!errors.Is(err, store.ErrChanged) || try == maxTries) {
				return nil, err
			}
			if l, err = s.load(ctx, e.ID); err != nil {
				return nil, err
			}
		}
		switch l.state {
		case stForked:
			kept = append(kept, e)
			_ = s.unpoint(ctx, e.Rcpt, e.ID)
			continue
		case stLost:
			if err := s.queue(ctx, s.now().Truncate(time.Second), e.ID); err != nil {
				return nil, err
			}
			if err := s.cfg.Records.RemoveRecord(ctx, recordName(e.ID)); err != nil {
				return nil, err
			}
			l.state = stCancelled
		case stNone:
			l.state = stCancelled // removed
		}
		if err := s.finish(ctx, l, e.Rcpt); err != nil {
			return nil, err
		}
	}
	return kept, nil
}

// Count implements accounts.Mailboxes.
func (s *Service) Count(ctx context.Context, account string) (int, error) {
	_, slots, err := s.settled(ctx, account)
	return len(slots), err
}

// Total implements accounts.Mailboxes: the index entries, which prove
// nothing and are only counted.
func (s *Service) Total(ctx context.Context) (int, error) {
	n, after := 0, ""
	for {
		page, err := s.cfg.Storage.ListEntries(ctx, indexPartition, "", after, 1000)
		if err != nil {
			return 0, err
		}
		n += len(page)
		if len(page) < 1000 {
			return n, nil
		}
		after = page[len(page)-1].Key
	}
}

// record is a mailbox as the operator sees it.
func (s *Service) record(ctx context.Context, sl slot, account string) (contracts.MailboxRecord, error) {
	if sl.l.state != stActive {
		return contracts.MailboxRecord{ID: sl.entry.ID, Account: account, State: contracts.MailboxUnreadable, CreatedAt: sl.entry.AddedAt}, nil
	}
	d := sl.l.doc
	set, err := s.readSettings(ctx, d.ID, nil)
	if err != nil {
		return contracts.MailboxRecord{}, err
	}
	return contracts.MailboxRecord{
		ID: d.ID, Account: d.Account, State: stateOf(d), CreatedAt: d.CreatedAt, RetentionDays: set.retentionDays,
		ReceivingSince: d.ReceivingSince, PausedAt: d.PausedAt, Verified: !d.VerifiedAt.IsZero(),
	}, nil
}

// Records implements accounts.Mailboxes.
func (s *Service) Records(ctx context.Context, account string) (*contracts.MailboxRecordList, error) {
	out := &contracts.MailboxRecordList{Mailboxes: []contracts.MailboxRecord{}}
	_, slots, err := s.settled(ctx, account)
	if err != nil {
		return nil, err
	}
	for _, sl := range slots {
		if sl.l.state == stNone {
			continue
		}
		r, err := s.record(ctx, sl, account)
		if err != nil {
			return nil, err
		}
		out.Mailboxes = append(out.Mailboxes, r)
	}
	return out, nil
}

// ByAddress implements accounts.Mailboxes.
func (s *Service) ByAddress(ctx context.Context, address string) (*contracts.MailboxRecordList, error) {
	out := &contracts.MailboxRecordList{Mailboxes: []contracts.MailboxRecord{}}
	l, err := s.byAddress(ctx, address)
	if err != nil || l == nil {
		return out, err
	}
	r, err := s.record(ctx, slot{l: l}, l.doc.Account)
	if err != nil {
		return nil, err
	}
	out.Mailboxes = append(out.Mailboxes, r)
	return out, nil
}

// byAddress follows an address to an active mailbox whose record bears it
// out: nil for anything else.
func (s *Service) byAddress(ctx context.Context, address string) (*loaded, error) {
	local, ok := domain.MailboxLocalPart(address, s.cfg.Domain)
	if !ok {
		return nil, nil
	}
	rcpt, err := s.rcptOf(ctx, local)
	if err != nil {
		return nil, err
	}
	_, id, err := s.pointer(ctx, rcpt)
	if err != nil || id == "" {
		return nil, err
	}
	l, err := s.load(ctx, id)
	if err != nil {
		return nil, err
	}
	if l.state != stActive || l.doc.Rcpt != rcpt {
		return nil, nil
	}
	return l, nil
}

// Record implements accounts.Mailboxes.
func (s *Service) Record(ctx context.Context, id string) (*contracts.MailboxRecord, error) {
	if !validMailboxID(id) {
		return nil, contracts.ErrNotFound
	}
	l, err := s.load(ctx, id)
	if err != nil {
		return nil, err
	}
	switch {
	case l.state == stActive:
		r, err := s.record(ctx, slot{l: l}, l.doc.Account)
		return &r, err
	case l.state.unreadable():
		// Whose it is comes from the index, and when it was added from that
		// account's list, which must name it.
		account, err := s.indexed(ctx, id)
		if err != nil || account == "" {
			return nil, errors.Join(err, contracts.ErrNotFound)
		}
		list, _, err := s.loadList(ctx, account)
		if err != nil {
			return nil, errors.Join(err, contracts.ErrNotFound)
		}
		if e, ok := list.find(id); ok {
			r, _ := s.record(ctx, slot{entry: e, l: l}, account)
			return &r, nil
		}
	}
	return nil, contracts.ErrNotFound
}

// Pause implements accounts.Mailboxes.
func (s *Service) Pause(ctx context.Context, id string) (*contracts.MailboxRecord, error) {
	return s.pause(ctx, id, true)
}

// Resume implements accounts.Mailboxes.
func (s *Service) Resume(ctx context.Context, id string) (*contracts.MailboxRecord, error) {
	return s.pause(ctx, id, false)
}

func (s *Service) pause(ctx context.Context, id string, on bool) (*contracts.MailboxRecord, error) {
	if !validMailboxID(id) {
		return nil, contracts.ErrNotFound
	}
	for range maxTries {
		l, err := s.load(ctx, id)
		if err != nil {
			return nil, err
		}
		if l.state != stActive {
			return nil, contracts.ErrNotFound
		}
		if on == !l.doc.PausedAt.IsZero() {
			r, err := s.record(ctx, slot{l: l}, l.doc.Account)
			return &r, err
		}
		now := s.now()
		doc := *l.doc
		doc.StateChanges = slices.DeleteFunc(slices.Clone(doc.StateChanges), func(t time.Time) bool { return now.Sub(t) >= Period })
		if len(doc.StateChanges) >= MaxStateChangesPerPeriod {
			return nil, contracts.ErrTooManyRequests
		}
		doc.StateChanges = append(doc.StateChanges, now)
		kind := NoticeResumed
		doc.PausedAt = time.Time{}
		if on {
			kind, doc.PausedAt = NoticePaused, now.Truncate(time.Second)
		}
		if err := s.putAt(ctx, l, &doc); errors.Is(err, store.ErrChanged) {
			continue
		} else if err != nil {
			return nil, err
		}
		l.doc = &doc
		r, err := s.record(ctx, slot{l: l}, doc.Account)
		if err != nil {
			return nil, err
		}
		s.notify(ctx, doc.Account, kind, now)
		return &r, nil
	}
	return nil, errors.New("mailboxes: a mailbox keeps changing")
}
