package connections

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/permaily-org/permaily/internal/envelope"
	"github.com/permaily-org/permaily/internal/store"
)

// The account's list over its epochs (list.go's ListState, C2): read from
// the newest epoch this enclave knows, following Moved, making again an
// epoch a crash left unmade; written at the version read, rolling to the
// next epoch at ListEpochVersions.

// partitionAccounts is the service's index of the accounts that have a
// list, for Sweep to find them. It decides nothing: a connection past its
// end is refused whether or not the sweep wrote it so, and EndAll is called
// with the account by accounts; deleting it only delays the cleanup.
const partitionAccounts = "connections#accounts"

func (s *Service) listEpoch(account string) uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.epochs["list "+account]
}

func (s *Service) sawListEpoch(account string, e uint64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if e > s.epochs["list "+account] {
		s.epochs["list "+account] = e
	}
}

// readList is the newest epoch of an account's list, read strictly and
// now, and its mark: nil when the account has none.
func (s *Service) readList(ctx context.Context, account string) (*ListState, store.Mark, error) {
	began := s.now()
	e := s.listEpoch(account)
	for range ListEpochVersions {
		var l ListState
		at, found, err := s.readSigned(ctx, ListName(account, e), &l)
		if err != nil {
			return nil, at, err
		}
		if !found {
			if e == 0 {
				s.cacheList(account, false, began)
				return nil, at, nil
			}
			if err := s.remakeEpoch(ctx, account, e); err != nil && !retry(err) {
				return nil, at, err
			}
			continue
		}
		if l.Account != account || l.Epoch != e || l.V != 1 {
			return nil, at, errDocument
		}
		if l.Moved {
			e++
			s.sawListEpoch(account, e)
			continue
		}
		if l.Current == nil {
			l.Current = map[string]string{}
		}
		s.sawListEpoch(account, e)
		s.cacheList(account, l.Closing, began)
		return &l, at, nil
	}
	return nil, store.Mark{}, errBusy
}

// remakeEpoch makes epoch e of a list again from e-1, written Moved by a
// writer that crashed before it made e.
func (s *Service) remakeEpoch(ctx context.Context, account string, e uint64) error {
	var prev ListState
	_, found, err := s.readSigned(ctx, ListName(account, e-1), &prev)
	if err != nil {
		return err
	}
	if !found || !prev.Moved {
		return store.ErrRolledBack
	}
	next := prev.Next()
	return s.writeSigned(ctx, ListName(account, e), &next, store.Mark{}, true)
}

// writeList writes l at the mark it was read at (made with CreateNew when
// there was none), checked against its bounds, and rolls it to its next
// epoch when this write reaches ListEpochVersions.
func (s *Service) writeList(ctx context.Context, l *ListState, at store.Mark, create bool) error {
	if !l.Fits() {
		return errListFull
	}
	began := s.now()
	name := ListName(l.Account, l.Epoch)
	if create {
		if err := s.writeSigned(ctx, name, l, at, true); err != nil {
			return err
		}
		if l.Epoch == 0 {
			s.indexAccount(ctx, l.Account)
		}
		s.cacheList(l.Account, l.Closing, began)
		return nil
	}
	if l.Rotate(at.Version()) {
		l.Moved = true
		if err := s.writeSigned(ctx, name, l, at, false); err != nil {
			l.Moved = false
			return err
		}
		next := l.Next()
		s.sawListEpoch(l.Account, next.Epoch)
		// A crash here leaves the epoch Moved and the next unmade: the next
		// reader makes it (remakeEpoch).
		if err := s.writeSigned(ctx, ListName(l.Account, next.Epoch), &next, store.Mark{}, true); err != nil && !errors.Is(err, store.ErrExists) {
			return err
		}
		s.cacheList(l.Account, l.Closing, began)
		return nil
	}
	if err := s.writeSigned(ctx, name, l, at, false); err != nil {
		return err
	}
	s.cacheList(l.Account, l.Closing, began)
	return nil
}

// errListFull is a list past its bytes (ListBytesMax).
var errListFull error = &kind{"connections: the account's list is full"}

// updateList reads the account's list, changes it with change and writes
// it, reading again and deciding again on a lost race, at most RecordTries
// times. change is given an empty list when the account has none.
func (s *Service) updateList(ctx context.Context, account string, change func(*ListState) error) (*ListState, error) {
	for range RecordTries {
		l, at, err := s.readList(ctx, account)
		if err != nil {
			return nil, err
		}
		create := l == nil
		if create {
			l = &ListState{V: 1, Account: account, Epoch: s.listEpoch(account), Members: []ListMember{}, Current: map[string]string{}, Decided: []ListDecision{}}
		}
		if err := change(l); err != nil {
			return l, err
		}
		err = s.writeList(ctx, l, at, create)
		if retry(err) {
			continue
		}
		return l, err
	}
	return nil, errBusy
}

// cacheList keeps whether an account's list is closing, as read or written
// by an operation that began at began; one that began earlier never replaces
// it.
func (s *Service) cacheList(account string, closing bool, began time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if old, ok := s.lists[account]; ok && old.read.After(began) {
		return
	}
	if len(s.lists) >= cacheMax {
		clear(s.lists)
	}
	s.lists[account] = cachedList{closing: closing, read: began}
}

// listClosing says the account's list is closing, from a read at most
// ConnectionFresh old unless fresh, and when that read began.
func (s *Service) listClosing(ctx context.Context, account string, fresh bool) (bool, time.Time, error) {
	if !fresh {
		s.mu.Lock()
		c, ok := s.lists[account]
		s.mu.Unlock()
		if ok && s.now().Sub(c.read) < oauthFresh {
			return c.closing, c.read, nil
		}
	}
	began := s.now()
	l, _, err := s.readList(ctx, account)
	if err != nil {
		return false, began, err
	}
	return l != nil && l.Closing, began, nil
}

// indexAccount writes the account in the index Sweep walks.
func (s *Service) indexAccount(ctx context.Context, account string) {
	value, err := envelope.Sign(s.signKey, []byte(strconv.Quote(account)), entryAAD(partitionAccounts, account))
	if err != nil {
		return
	}
	// One that is there already is the same; a failure only delays the
	// sweep of this account.
	_ = s.cfg.Entries.PutEntry(ctx, &store.Entry{Partition: partitionAccounts, Key: account, Value: value, UpdatedAt: s.now()})
}
