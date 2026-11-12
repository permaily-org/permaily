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
// pass found nothing, its record is removed and its entry deleted.

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
		return err
	}
	if now.Sub(at) < SweepGrace {
		return nil // a request or a delivery that read the record may still write
	}
	// The last pass, after every write that read the record before its
	// commit has ended.
	if err := s.clean(ctx, id); err != nil {
		return err
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

// forget deletes a disconnect entry as it was read.
func (s *Service) forget(ctx context.Context, e *store.Entry) error {
	if err := s.cfg.Storage.DeleteEntryAt(ctx, e); err != nil && !errors.Is(err, store.ErrConflict) {
		return err
	}
	return nil
}

// clean deletes a mailbox's messages, a page at a time (the raw object, then
// the item), then every other entry of its partition.
func (s *Service) clean(ctx context.Context, id string) error {
	for range sweepPages {
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
	for range sweepPages {
		entries, err := s.cfg.Storage.ListEntries(ctx, partition, "", "", 100)
		if err != nil {
			return err
		}
		for _, e := range entries {
			if err := s.cfg.Storage.DeleteEntry(ctx, partition, e.Key); err != nil && !errors.Is(err, store.ErrNotFound) {
				return err
			}
		}
		if len(entries) < 100 {
			return nil
		}
	}
	return nil
}
