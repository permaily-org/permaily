package webhooks

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/permaily-org/permaily/internal/store"
	"github.com/permaily-org/permaily/internal/webhook"
)

// The repair's lease: one entry, sealed, that only shares the work between
// enclaves (as the receipts' audit's, ADR 0037); nothing it holds decides
// anything.
const (
	partitionRepair = "webhooks#repair"
	repairKey       = "lease"
	repairLeaseFor  = 10 * time.Minute
)

type repairLease struct {
	Holder string    `json:"holder"`
	Until  time.Time `json:"until"`
	Done   time.Time `json:"done"`
}

// repair rebuilds the queue from the connections' strict records once every
// webhook.RepairEvery, in one enclave (ADR 0039, "The hourly repair"):
// every webhook Connections.Webhooks lists that still calls, or would but
// for the model or the rules, has its state, its index entries and a due
// entry made again where missing, and is made due now when it wants new
// mail (a lost hint); then every state entry of a connection the listing
// did not name goes, with what it names, once a strict read of that
// connection, made after the listing, finds it no longer calls: the listing
// is no snapshot, and a webhook subscribed after its last page was read
// stays.
func (s *Service) repair(ctx context.Context) {
	seal, _, err := s.keys(ctx)
	if err != nil {
		s.fail("repair", err)
		return
	}
	now := s.now()
	e, err := s.cfg.Entries.GetEntry(ctx, partitionRepair, repairKey)
	var r repairLease
	switch {
	case errors.Is(err, store.ErrNotFound):
		e = &store.Entry{Partition: partitionRepair, Key: repairKey}
	case err != nil:
		s.fail("repair", err)
		return
	default:
		// One that does not open is taken over: it only shares the work.
		_ = openFrom(seal, e, &r)
	}
	if now.Before(r.Done.Add(webhook.RepairEvery)) || now.Before(r.Until) {
		return
	}
	r.Holder, r.Until = s.name, now.Add(repairLeaseFor)
	if err := s.put(ctx, seal, e, r); err != nil {
		s.fail("repair", err)
		return
	}
	if s.rebuildAll(ctx, seal, now) {
		r.Done = now
	}
	r.Until = time.Time{}
	if err := s.put(ctx, seal, e, r); err != nil {
		s.fail("repair", err)
	}
}

// rebuildAll walks the listing, then the state entries; false when the
// listing failed (nothing is dropped then).
func (s *Service) rebuildAll(ctx context.Context, seal []byte, now time.Time) bool {
	named := map[string]bool{}
	for after := ""; ; {
		ids, err := s.cfg.Connections.Webhooks(ctx, after, RepairPage)
		if err != nil {
			s.fail("repair", err)
			return false
		}
		for _, id := range ids {
			named[id] = s.rebuild(ctx, seal, id, now)
		}
		if len(ids) < RepairPage {
			break
		}
		after = ids[len(ids)-1]
	}
	s.sweep(ctx, seal, named, now)
	return true
}

// rebuild makes again what is missing of one listed webhook's entries, and
// says whether they stay.
func (s *Service) rebuild(ctx context.Context, seal []byte, id string, now time.Time) bool {
	sub, err := s.cfg.Connections.Subscription(ctx, id)
	switch {
	case errors.Is(err, ErrNoWebhook):
		return false
	case err != nil:
		// Not known now: kept as it is.
		s.fail("repair", err)
		return true
	case sub.Why == SkipEnded || sub.Why == SkipNoDevice || sub.Why == SkipOff:
		return false
	}
	if sub.Schedule != nil {
		if _, err := webhook.Zone(sub.TimeZone); err != nil {
			s.fail("repair", err)
			return false
		}
	}
	e, err := s.cfg.Entries.GetEntry(ctx, shard(id), id)
	st := &state{}
	switch {
	case errors.Is(err, store.ErrNotFound):
		e = &store.Entry{Partition: shard(id), Key: id}
		err = errNoState
	case err != nil:
		s.fail("repair", err)
		return true
	default:
		err = openFrom(seal, e, st)
	}
	if err != nil {
		// Missing, or not one this service wrote: made again, its cursor
		// where the previous repair found it whole, its coverage boundary
		// the connection's making (which over-covers rather than skips).
		if err := s.build(ctx, seal, e, sub, newState(sub, now.Add(-webhook.RepairEvery-webhook.ArrivalLag)), now); err != nil {
			s.fail("repair", err)
		}
		return true
	}
	r := route{Host: st.Host, Account: st.Account}
	if sub.NewMail {
		for _, m := range sub.Mailboxes {
			if err := s.index(ctx, seal, m, id, r); err != nil {
				s.fail("repair", err)
			}
		}
	}
	// Due now when it wants new mail (a lost hint) or its due minute is
	// gone by (one the workers may no longer list); else where it was.
	due := now
	if !sub.NewMail && st.Due.After(now) {
		due = st.Due
		if _, err := s.cfg.Entries.GetEntry(ctx, minuteKey(due), id); !errors.Is(err, store.ErrNotFound) {
			return true
		}
	}
	if err := s.markDue(ctx, seal, id, r, due); err != nil {
		s.fail("repair", err)
	}
	return true
}

var errNoState = errors.New("webhooks: no state")

// sweep drops every state entry of a connection not named as staying, with
// its index entries and its due entry, once rebuild, reading the
// connection's strict record again now, finds it does not stay (fail
// closed: a webhook subscribed or turned on again since the listing read
// it, or one whose read fails, stays and is rebuilt).
func (s *Service) sweep(ctx context.Context, seal []byte, named map[string]bool, now time.Time) {
	for i := range Shards {
		partition := PartitionState + fmt.Sprintf("%02x", i)
		for after := ""; ; {
			entries, err := s.cfg.Entries.ListEntries(ctx, partition, "", after, RepairPage)
			if err != nil {
				s.fail("repair", err)
				return
			}
			for j := range entries {
				if e := &entries[j]; !named[e.Key] && !s.rebuild(ctx, seal, e.Key, now) {
					st := &state{}
					// One that does not open names nothing: it goes alone.
					_ = openFrom(seal, e, st)
					s.remove(ctx, e, st)
				}
			}
			if len(entries) < RepairPage {
				break
			}
			after = entries[len(entries)-1].Key
		}
	}
}
