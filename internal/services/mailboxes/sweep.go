package mailboxes

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/store"
)

// The sweep (ADR 0026, "Disconnecting"): every enclave goes through the
// disconnect entries, the oldest first, and deletes what is left of each
// mailbox; once SweepGrace has passed since it was disconnected, and a last
// pass left nothing, its record is removed and its entry deleted.

// What one sweep does at most.
const (
	sweepEntries = 100
	// sweepPages bounds the pages of messages, and of other entries, one
	// pass deletes of one mailbox; the next sweep goes on.
	sweepPages = 50
	// stuckFor is how long a disconnect entry of a mailbox that is still
	// active stays: the disconnect failed before its commit.
	stuckFor = time.Hour
)

// Run sweeps every SweepInterval, and soon after a disconnect, until ctx
// ends.
func (s *Service) Run(ctx context.Context) {
	t := time.NewTicker(SweepInterval)
	defer t.Stop()
	for {
		if err := s.Sweep(ctx); err != nil && ctx.Err() == nil {
			s.cfg.Log.Error("mailboxes: sweeping disconnected mailboxes", "err", scrub(err))
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-s.kick:
		}
	}
}

// Sweep goes once through the disconnect entries. Every deletion can be done
// twice, by two enclaves at once.
func (s *Service) Sweep(ctx context.Context) error {
	list, err := s.cfg.Storage.ListEntries(ctx, leavingPartition, "", "", sweepEntries)
	if err != nil {
		return err
	}
	var errs []error
	for i := range list {
		if err := s.sweepOne(ctx, &list[i]); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

func (s *Service) sweepOne(ctx context.Context, e *store.Entry) error {
	stamp, id, ok := strings.Cut(e.Key, "#")
	at, err := time.Parse(time.RFC3339, stamp)
	var d leavingDoc
	if !ok || err != nil || !validMailboxID(id) {
		return nil // not one this deployment writes
	}
	if ok, err := s.readSigned(ctx, e, &d); err != nil || !ok || d.Mailbox != id {
		return err
	}
	l, err := s.load(ctx, id)
	if err != nil {
		return err
	}
	now := s.now()
	switch l.state {
	case stActive:
		if now.Sub(at) >= stuckFor {
			return s.forget(ctx, e)
		}
		return nil
	case stForked:
		// Nothing of it is deleted while it is forked: nobody opens its mail,
		// which expires with its retention.
		return s.forget(ctx, e)
	case stLost:
		// A disconnect entry is written only when the person or the account's
		// removal disconnects it: the record goes, as it needs no value.
		if err := s.cfg.Records.RemoveRecord(ctx, recordName(id)); err != nil {
			return err
		}
	case stDisconnecting:
		at = l.doc.DisconnectedAt
		if err := s.leave(ctx, l, l.doc.Account, l.doc.Rcpt); err != nil {
			return err
		}
	}
	if err := s.clean(ctx, id); err != nil {
		return quiet(err)
	}
	if now.Sub(at) < SweepGrace {
		return nil // a request or a delivery that read the record may still write
	}
	// The last pass, after every write that read the record before its
	// commit has ended: the rest goes only after a pass that left the
	// partition empty. One whose budget ran out leaves the work entry for the
	// next sweep.
	if err := s.clean(ctx, id); err != nil {
		return quiet(err)
	}
	if l.state == stDisconnecting || l.state == stCancelled {
		if err := s.cfg.Records.RemoveRecord(ctx, recordName(id)); err != nil {
			return err
		}
	}
	if err := s.unindex(ctx, id); err != nil {
		return err
	}
	return s.forget(ctx, e)
}

// errNotSwept is a pass whose budget ran out before the partition was empty:
// the work entry stays, and the next sweep goes on. It is not a failure.
var errNotSwept = errors.New("mailboxes: the sweep's budget for a mailbox ran out")

// quiet is a sweep's error as Sweep reports it: none for a budget that ran
// out.
func quiet(err error) error {
	if errors.Is(err, errNotSwept) {
		return nil
	}
	return err
}

// forget deletes a disconnect entry as it was read.
func (s *Service) forget(ctx context.Context, e *store.Entry) error {
	if err := s.cfg.Storage.DeleteEntryAt(ctx, e); err != nil && !errors.Is(err, store.ErrConflict) {
		return err
	}
	return nil
}

// partitionPrefixes are the keys of a mailbox's partition that are entries
// (inbox.Writer, the settings). Its messages (msg#) are deleted only through
// the message path, the raw object first: on AWS a listing of entries without
// a prefix reads the message rows too, and deleting those as entries would
// leave their raw objects.
var partitionPrefixes = []string{inbox.KeySearch, inbox.KeyConfirm, inbox.KeySeen, inbox.KeySettings}

// clean deletes a mailbox's messages, a page at a time (the raw object, then
// the item), then its entries, kind by kind, within sweepPages pages in all:
// nil once a page of each came back short, which leaves the partition empty;
// errNotSwept when the budget ran out first.
func (s *Service) clean(ctx context.Context, id string) error {
	pages := 0
	for {
		if pages == sweepPages {
			return errNotSwept
		}
		pages++
		msgs, err := s.cfg.Storage.ListMessages(ctx, id, 100, "")
		if err != nil {
			return err
		}
		for _, m := range msgs {
			if err := s.cfg.Storage.DeleteMessage(ctx, id, m.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
				return err
			}
		}
		if len(msgs) < 100 {
			break
		}
	}
	partition := inbox.Partition(id)
	for _, prefix := range partitionPrefixes {
		for {
			if pages == sweepPages {
				return errNotSwept
			}
			pages++
			entries, err := s.cfg.Storage.ListEntries(ctx, partition, prefix, "", 100)
			if err != nil {
				return err
			}
			for _, e := range entries {
				if err := s.cfg.Storage.DeleteEntry(ctx, partition, e.Key); err != nil && !errors.Is(err, store.ErrNotFound) {
					return err
				}
			}
			if len(entries) < 100 {
				break
			}
		}
	}
	return nil
}
