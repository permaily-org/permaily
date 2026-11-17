package accounts

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/services/connections"
	"github.com/permaily-org/permaily/internal/services/receipts"
	"github.com/permaily-org/permaily/internal/store"
)

// What accounts does of the connections service (ADR 0030, "Revocation and
// two enclaves", finding 6; the receipts' partition, ADR 0031): an account
// being deleted has its list of connections fenced and every connection
// ended, when the deletion is asked and again on every pass while it is
// closing; a cancelled deletion is done only once Reopen has ended what of
// them was still active; the removal ends them once more, and deletes the
// account's receipts, then again receipts.RemoveAgainAfter later, for a
// receipt whose write began before the removal and ended after it.

type connectionsHolder struct{ c connections.Lifecycle }

// SetConnections gives the service the connections of accounts, once, before
// it serves: the connections service is made with this one as its accounts,
// so Config.Connections cannot hold it when this one is made.
func (s *Service) SetConnections(c connections.Lifecycle) {
	s.connections.Store(&connectionsHolder{c: c})
}

// connectionsOf is what SetConnections gave, else Config.Connections; nil
// for none.
func (s *Service) connectionsOf() connections.Lifecycle {
	if h := s.connections.Load(); h != nil {
		return h.c
	}
	return s.cfg.Connections
}

// fenceConnections fences and ends an account's connections when its
// deletion is asked. A failure is logged by its kind: the deletion stands,
// the account's tokens are refused while it is closing whatever the list
// says (its standing), and the next pass over the closing accounts tries
// again.
func (s *Service) fenceConnections(ctx context.Context, id string) {
	c := s.connectionsOf()
	if c == nil {
		return
	}
	if err := c.EndAll(ctx, id); err != nil {
		s.cfg.Log.Warn("accounts: the connections of a closing account are not all ended yet", "err", scrub(err))
	}
}

// fenceClosing fences again the connections of an account the list of
// closing accounts names under entry, if its record bears it out.
func (s *Service) fenceClosing(ctx context.Context, id, entry string) error {
	doc, err := s.read(ctx, id)
	if err != nil || doc == nil || doc.State != contracts.AccountClosing || closingKey(doc) != entry {
		return err
	}
	return s.connectionsOf().EndAll(ctx, id)
}

// reopenConnections is what a cancellation does first.
func (s *Service) reopenConnections(ctx context.Context, id string) error {
	if c := s.connectionsOf(); c != nil {
		return c.Reopen(ctx, id)
	}
	return nil
}

// receiptsAgainPartition lists the accounts whose receipts are deleted once
// more, by when.
const receiptsAgainPartition = "receipts-again"

// removeReceipts deletes an account's receipts, having first listed it to
// delete them again receipts.RemoveAgainAfter from now.
func (s *Service) removeReceipts(ctx context.Context, id string) error {
	key := timedKey(s.now().Add(receipts.RemoveAgainAfter), id)
	if err := s.point(ctx, receiptsAgainPartition, key, id, 0); err != nil && !errors.Is(err, store.ErrExists) {
		return err
	}
	return s.cfg.Entries.DeletePartition(ctx, receipts.Partition(id))
}

// removeReceiptsAgain deletes the receipts of the accounts whose time has
// come in the list, then their entries. An entry that is not this
// deployment's deletes nothing.
func (s *Service) removeReceiptsAgain(ctx context.Context, key []byte, now time.Time) error {
	after := ""
	for range maxClosingPages {
		entries, err := s.cfg.Entries.ListEntries(ctx, receiptsAgainPartition, "", after, closingPage)
		if err != nil {
			return err
		}
		for i := range entries {
			e := &entries[i]
			after = e.Key
			day, id, ok := parseTimedKey(e.Key)
			if !ok || pointed(key, e) != id {
				continue
			}
			if day.After(now) {
				return nil
			}
			if err := s.cfg.Entries.DeletePartition(ctx, receipts.Partition(id)); err != nil {
				return err
			}
			if err := s.forget(ctx, receiptsAgainPartition, e.Key); err != nil {
				return err
			}
		}
		if len(entries) < closingPage {
			break
		}
	}
	return nil
}

// connectionsAgainPartition lists the removed accounts whose connections
// are removed once more, by when (removeConnections).
const connectionsAgainPartition = "connections-again"

// removeConnectionsAgain removes once more the connections of the accounts
// whose time has come in the list (connections.Lifecycle.RemoveAgain),
// then their entries: a value that a write under way at the removal staged
// after it goes too (second review of #152, finding 2). An entry that is
// not this deployment's removes nothing; one that fails stays for the next
// pass, and the others go on.
func (s *Service) removeConnectionsAgain(ctx context.Context, key []byte, now time.Time) error {
	c := s.connectionsOf()
	if c == nil {
		return nil
	}
	after := ""
	var first error
	for range maxClosingPages {
		entries, err := s.cfg.Entries.ListEntries(ctx, connectionsAgainPartition, "", after, closingPage)
		if err != nil {
			return err
		}
		for i := range entries {
			e := &entries[i]
			after = e.Key
			day, id, ok := parseTimedKey(e.Key)
			if !ok || pointed(key, e) != id {
				continue
			}
			if day.After(now) {
				return first
			}
			err := c.RemoveAgain(ctx, id)
			if err == nil {
				err = s.forget(ctx, connectionsAgainPartition, e.Key)
			}
			if err != nil && first == nil {
				first = err
			}
		}
		if len(entries) < closingPage {
			break
		}
	}
	return first
}

// errNoAccount answers Removing for an account whose record was never made.
var errNoAccount = errors.New("accounts: no such account")

// errNoHistory answers Removing over a store of records that cannot read a
// record's whole history.
var errNoHistory = errors.New("accounts: Removing needs a store that reads a record's whole history")

// historyChecker is a store of records that reads a record's whole
// history (store.Anchored.CheckHistory).
type historyChecker interface {
	CheckHistory(ctx context.Context, name string) error
}

// Removing answers whether an account is removed or being removed, for the
// audit of the receipts' chains (receipts.Accounts; ADR 0037, R3): removed
// when its record was removed by its terminal commit, or says it is being
// removed (Removing, written before the removal deletes anything), or its
// day to close has come. Anything else (a record never made, one lost,
// forked or that does not open, the storage failing) is an error, never
// taken for removed or for live.
//
// The account's record is not strict, and is read and written as before;
// this answer alone rests on its whole history (the review of #170,
// finding 2): a fork at any of its versions is store.ErrForked, whichever
// value a read of its newest commit would choose.
func (s *Service) Removing(ctx context.Context, id string) (bool, error) {
	checker, ok := s.cfg.Records.(historyChecker)
	if !ok {
		return false, errNoHistory
	}
	if err := checker.CheckHistory(ctx, recordName(id)); err != nil {
		return false, fmt.Errorf("accounts: an account's record: %w", err)
	}
	doc, at, err := s.readAt(ctx, id)
	switch {
	case err != nil:
		return false, err
	case doc == nil && at.Version() > 0:
		return true, nil
	case doc == nil:
		return false, errNoAccount
	}
	return doc.due(s.now()), nil
}
