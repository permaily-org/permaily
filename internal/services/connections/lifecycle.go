package connections

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/envelope"
	"github.com/permaily-org/permaily/internal/oauth"
	"github.com/permaily-org/permaily/internal/store"
)

// What accounts calls (Lifecycle: an account being deleted, a deletion
// cancelled; finding 6) and the hourly pass (Sweep).

// EndAll implements Lifecycle: the list is fenced (closing, a new
// generation; made with CreateNew when the account has none), and only
// then is every connection and pending one it names ended.
func (s *Service) EndAll(ctx context.Context, account string) error {
	return s.endAll(ctx, account, nil)
}

// endAll is EndAll. With note (RemoveAll's, once the account's removal
// began: no cancellation follows), every connection that ending the list's
// members trims (dropEnded) is noted, with its families, before it is
// removed, so that RemoveAgain finds it (third review of #152, finding 2).
// EndAll keeps no notes: a cancellation may follow it.
func (s *Service) endAll(ctx context.Context, account string, note func(id string) error) error {
	l, err := s.updateList(ctx, account, func(l *ListState) error {
		if !l.Closing {
			l.Closing = true
			l.Generation++
		}
		return nil
	})
	if err != nil {
		return err
	}
	return s.endGeneration(ctx, account, l.Generation, note)
}

// endGeneration ends every member of the list made before generation, and
// writes them ended in the list; what that trims is noted with note
// (endAll).
func (s *Service) endGeneration(ctx context.Context, account string, generation uint64, note func(id string) error) error {
	l, _, err := s.readList(ctx, account)
	if err != nil || l == nil {
		return err
	}
	var ended []string
	for _, m := range l.Members {
		if m.State == contracts.ConnectionEnded || m.Generation >= generation {
			continue
		}
		if err := s.fenceMember(ctx, account, m, contracts.EndedByAccount); err != nil {
			return err
		}
		ended = append(ended, m.ID)
	}
	if len(ended) == 0 {
		return nil
	}
	now := s.now()
	_, err = s.updateList(ctx, account, func(l *ListState) error {
		for _, id := range ended {
			s.dropEnded(ctx, l, l.End(id, now), note)
		}
		return nil
	})
	return err
}

// fenceMember ends one member of a list: a pending one is fenced (its
// record made ended if its maker never made it), an active one ended.
func (s *Service) fenceMember(ctx context.Context, account string, m ListMember, reason contracts.EndReason) error {
	if m.Pending {
		return s.fence(ctx, account, m, reason)
	}
	return s.endConnection(ctx, m.ID, reason, "", true)
}

// Reopen implements Lifecycle: the list is fenced as EndAll fences it, also
// when the deletion's own fence never committed (EndAll failed, or the
// account has no list yet with an approval in flight), whatever of the
// closing generation is still active is ended, and only then is the fence
// lifted, so a cancelled deletion never brings a connection back (finding 4).
func (s *Service) Reopen(ctx context.Context, account string) error {
	if err := s.EndAll(ctx, account); err != nil {
		return err
	}
	_, err := s.updateList(ctx, account, func(l *ListState) error {
		for _, m := range l.Members {
			if m.State != contracts.ConnectionEnded && m.Generation < l.Generation {
				return errBusy // one made meanwhile: ended on the next try
			}
		}
		l.Closing = false
		return nil
	})
	return err
}

// RemoveAll implements Lifecycle: everything stored of the account's
// connections goes with the account (ADR 0030, "Changed while releasing
// step 5"). The list is fenced and ended first, as EndAll does, while it is
// whole; then every connection its newest epoch names is removed with its
// families' records, the account's entry in Sweep's index is deleted, and
// every epoch of the list is removed (removeEpochs). What stays is the
// commit of each removal (store.Anchored.RemoveRecord), which names the
// record and holds nothing of it: no record removed can be read again,
// whatever of it is put back, and none can be made again (CreateNew refuses
// a name with any commit), so the fence outlives the list without its
// history.
//
// It is what accounts calls when an account is removed, and again on every
// pass until the account's removal is done: a call stopped anywhere, a
// removal's commit landed and its values left included, is finished by the
// next, through any enclave. A connection that cannot be read (lost, put
// back, not signed) fails it and keeps the list and the index that find it:
// its families are known only from its record, and a removal that went on
// would leave them where nothing finds them. Each connection, and each of
// its families, is noted before it is removed (noteRemoved), those its
// EndAll trims from the list included, so that RemoveAgain finds their
// names once nothing else does.
func (s *Service) RemoveAll(ctx context.Context, account string) error {
	list := func(e uint64) string { return ListName(account, e) }
	note := func(id string) error { return s.noteRemoved(ctx, account, id) }
	ended := false
	for range RecordTries {
		heads, err := s.epochHeads(ctx, list)
		if err != nil {
			return err
		}
		// A removal began when an epoch after the newest live one is
		// removed (it removes the newest first, after a fence); the epochs
		// before it that are removed are those a rotation closed
		// (pruneList).
		newest, removing := -1, false
		for e, h := range heads {
			if h.live {
				newest = e
			}
		}
		for _, h := range heads[newest+1:] {
			removing = removing || !h.live
		}
		if newest >= 0 {
			var l ListState
			_, found, err := s.readSigned(ctx, list(uint64(newest)), &l)
			if err != nil {
				return err
			}
			if !found {
				continue // removed meanwhile, by another call
			}
			if l.Account != account || l.Epoch != uint64(newest) || l.V != 1 {
				return errDocument
			}
			// A list no removal began on is fenced first. Once one has, its
			// fence held when it began (the newest epoch named what is left).
			if !l.Moved && !removing && !ended {
				if err := s.endAll(ctx, account, note); err != nil {
					return err
				}
				ended = true
				continue
			}
			for _, m := range l.Members {
				if err := s.removeConnStrict(ctx, m.ID, note); err != nil {
					return err
				}
			}
		}
		if err := s.cfg.Entries.DeleteEntry(ctx, partitionAccounts, account); err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
		if err := s.removeEpochs(ctx, list); err != nil {
			return err
		}
		s.mu.Lock()
		delete(s.epochs, "list "+account)
		delete(s.lists, account)
		s.mu.Unlock()
		return nil
	}
	return errBusy
}

// epochHead is what the anchor says of one epoch of a record kept in
// epochs: live, it has a value (readable or not); else it was removed, and
// version is its removal's commit. A removal's commit at version 1 is a
// fence: the name never had a value.
type epochHead struct {
	live    bool
	version uint64
}

// epochHeads walks a record's epochs from 0 by their commits alone,
// removed ones included, up to the first name that has none: an epoch's
// content does not matter, and one removed whose values stayed is found.
func (s *Service) epochHeads(ctx context.Context, name func(uint64) string) ([]epochHead, error) {
	var heads []epochHead
	for e := uint64(0); ; e++ {
		_, at, err := s.cfg.Records.GetRecordAt(ctx, name(e))
		switch {
		case err == nil, errors.Is(err, store.ErrLost):
			heads = append(heads, epochHead{live: true, version: at.Version()})
		case !errors.Is(err, store.ErrNotFound):
			return nil, err
		case at.Version() == 0:
			return heads, nil
		default:
			heads = append(heads, epochHead{version: at.Version()})
		}
	}
}

// endsInFence says the epochs walked end in a fence: the last was never
// made, so no writer can make an epoch after it (an epoch is made only
// after the one before it was written moved, and CreateNew refuses a name
// with a commit). Epochs before it may still be live.
func endsInFence(heads []epochHead) bool {
	return len(heads) > 0 && !heads[len(heads)-1].live && heads[len(heads)-1].version == 1
}

// fenced says the epochs walked end in a fence and none is live: the
// removal is done once what is stored of them is deleted.
func fenced(heads []epochHead) bool {
	if !endsInFence(heads) {
		return false
	}
	for _, h := range heads {
		if h.live {
			return false
		}
	}
	return true
}

// removeEpochs removes every epoch of a record kept in epochs, those whose
// removal committed already included (RemoveRecord deletes what of them is
// still stored), the newest first. The name after the last epoch with a
// value is removed before it, a fence: a writer that wrote that epoch moved
// before its removal, and has yet to make the next one, finds the name
// taken (CreateNew), and a removal never leaves an epoch made after it.
// The epochs are walked again until they end in a fence with nothing live.
// A fence already there is kept, also while an epoch before it is still
// live (a try that failed on it): another is added only after an epoch
// whose removal is not a fence, one a writer made before the fence landed
// on its name, so a retry adds no commit (second review of #152, finding
// 3).
func (s *Service) removeEpochs(ctx context.Context, name func(uint64) string) error {
	heads, err := s.epochHeads(ctx, name)
	if err != nil {
		return err
	}
	for range RecordTries {
		done := fenced(heads)
		n := uint64(len(heads))
		if !endsInFence(heads) {
			n++ // the fence
		}
		for e := n; e > 0; e-- {
			if err := s.cfg.Records.RemoveRecord(ctx, name(e-1)); err != nil {
				return err
			}
		}
		if done {
			return nil
		}
		if heads, err = s.epochHeads(ctx, name); err != nil {
			return err
		}
	}
	return errBusy
}

// listRemoved says the account's list was removed: its epochs end in the
// fence its removal leaves, and none has a value. Epoch 0 alone does not
// tell, since a rotation removes it while the list lives on (pruneList).
func (s *Service) listRemoved(ctx context.Context, account string) (bool, error) {
	heads, err := s.epochHeads(ctx, func(e uint64) string { return ListName(account, e) })
	if err != nil {
		return false, err
	}
	return fenced(heads), nil
}

// WriteMax bounds every write of a value of the account's connections (a
// list's epoch, a connection, a family's epoch: writeSigned, writeSealed)
// from before the read of the record's commits it rests on, which tells it
// whether the record was removed: the reads of a long history, their pages,
// the credentials, the retries and the staging of the value all fall within
// it, and a write past it stores nothing (store.WriteWithin; third review
// of #152, finding 1). The enclave's AWS client gives up on a request after
// 60 seconds and the SDK tries one at most 4 times, at most 20 seconds
// apart: one operation lasts at most 5 minutes, and a write gets two.
const WriteMax = 10 * time.Minute

// removeAgainMargin is what RemoveAgainAfter leaves past WriteMax: for
// scheduling and for two enclaves' clocks.
const removeAgainMargin = 20 * time.Minute

// RemoveAgainAfter is how long after RemoveAll succeeded accounts calls
// RemoveAgain (second review of #152, finding 2): WriteMax and a margin, 30
// minutes. A write that read the commits of its record before the removal
// committed, and so did not see it, stages its value after it, and a write
// whose answer is lost stays staged; such a value is stored within WriteMax
// of that read, so before RemoveAgain runs, which deletes it
// (TestV65TheLastPassOutlastsAStoreWrite checks the client). A write held
// longer than that, by whoever runs the host or writes the table, is not
// one this bounds: its value lands under a name whose removal committed,
// which no read takes (docs/threat-model.md).
const RemoveAgainAfter = WriteMax + removeAgainMargin

// partitionRemoved is where RemoveAll notes, by account, the connections
// and families it removes (signed, a key each), for RemoveAgain: once they
// are gone, nothing else names them.
const partitionRemoved = "connections#removed#"

// noteRemoved notes id (a connection's or a family's) in the account's
// removed names. One noted already stays as it is.
func (s *Service) noteRemoved(ctx context.Context, account, id string) error {
	partition := partitionRemoved + account
	value, err := envelope.Sign(s.signKey, []byte(strconv.Quote(id)), entryAAD(partition, id))
	if err != nil {
		return err
	}
	err = s.cfg.Entries.PutEntry(ctx, &store.Entry{Partition: partition, Key: id, Value: value, UpdatedAt: s.now()})
	if errors.Is(err, store.ErrExists) {
		return nil
	}
	return err
}

// RemoveAgain implements Lifecycle: RemoveAgainAfter after RemoveAll
// succeeded, it removes once more every name RemoveAll removed: the list's
// epochs and the fence after them (RemoveAll again, which also deletes the
// index entry), and each connection and family it noted, every epoch with
// its fence, so that what a write under way at the removal staged after it
// is deleted (RemoveRecord deletes every stored version of a removed
// record); then the notes go. It is called until it succeeds; once it did,
// nothing of the account's connections is stored but the commits of their
// removal.
func (s *Service) RemoveAgain(ctx context.Context, account string) error {
	if err := s.RemoveAll(ctx, account); err != nil {
		return err
	}
	partition := partitionRemoved + account
	after := ""
	for {
		entries, err := s.cfg.Entries.ListEntries(ctx, partition, "", after, sweepPage)
		if err != nil {
			return err
		}
		for _, e := range entries {
			after = e.Key
			raw, err := envelope.Verified(s.signKey, e.Value, entryAAD(partition, e.Key))
			if err != nil || string(raw) != strconv.Quote(e.Key) {
				continue // not written by this deployment
			}
			switch {
			case strings.HasPrefix(e.Key, "con_"):
				err = s.cfg.Records.RemoveRecord(ctx, connName(e.Key))
			case strings.HasPrefix(e.Key, "fam_"):
				err = s.removeFamily(ctx, e.Key)
			}
			if err != nil {
				return err
			}
		}
		if len(entries) < sweepPage {
			break
		}
	}
	return s.cfg.Entries.DeletePartition(ctx, partition)
}

// errUnpruned is a pass that could not remove what rotations left of the
// earlier epochs of an account's list.
var errUnpruned error = &kind{"connections: an earlier epoch of the account's list is still to remove"}

// pendingFenceAfter is how old a pending member must be before the sweep
// fences it: its maker is then long done, or crashed.
const pendingFenceAfter = oauth.RequestTTL

// sweepPage is how many accounts of the index one pass reads at once.
const sweepPage = 100

// Sweep is the hourly pass (ADR 0030, "Revocation and two enclaves"): for
// every account the service's index names, it walks the account's list
// (the strict record, never an index of connections): a closing list's
// connections ended (EndAll finished), connections past their end written
// ended, pending ones whose maker is gone fenced, ended ones removed with
// their families' records EndedKept after they ended; then the dead records
// of assertions are removed through their sealed list.
func (s *Service) Sweep(ctx context.Context) error {
	var first error
	failed := 0
	after := ""
	for {
		entries, err := s.cfg.Entries.ListEntries(ctx, partitionAccounts, "", after, sweepPage)
		if err != nil {
			return err
		}
		for _, e := range entries {
			after = e.Key
			raw, err := envelope.Verified(s.signKey, e.Value, entryAAD(partitionAccounts, e.Key))
			if err != nil || string(raw) != strconv.Quote(e.Key) {
				continue // not written by this deployment
			}
			if err := s.sweepAccount(ctx, e.Key); err != nil {
				if failed++; first == nil {
					first = err
				}
			}
		}
		if len(entries) < sweepPage {
			break
		}
	}
	if err := s.sweepAssertions(ctx); err != nil && first == nil {
		failed++
		first = err
	}
	if failed > 0 {
		return fmt.Errorf("connections: %d parts of the sweep failed, the first because: %w", failed, first)
	}
	return nil
}

// sweepAccount is one account's part of the pass.
func (s *Service) sweepAccount(ctx context.Context, account string) error {
	l, _, err := s.readList(ctx, account)
	if l == nil {
		// An entry of a removed list: written by a write of the list that
		// committed before the removal and wrote the index after it, or put
		// back by whoever writes the table. It goes on this pass; a pass
		// that cannot tell fails, and the next one tries again.
		removed, rerr := s.listRemoved(ctx, account)
		if rerr != nil {
			return rerr
		}
		if !removed {
			return err
		}
		if err := s.cfg.Entries.DeleteEntry(ctx, partitionAccounts, account); err != nil && !errors.Is(err, store.ErrNotFound) {
			return err
		}
		s.mu.Lock()
		delete(s.epochs, "list "+account)
		delete(s.lists, account)
		s.mu.Unlock()
		return nil
	}
	if err != nil {
		return err
	}
	// What rotations left of the earlier epochs: removed and the mark
	// cleared (Pruning), or removed once more RemoveAgainAfter after
	// (RemoveAgainDue), by a write of the list (writeListOnce); a pass that
	// could not leaves it to the next and fails.
	if l.Pruning || l.RemoveAgainDue(s.now()) {
		n, err := s.updateList(ctx, account, func(*ListState) error { return nil })
		if err != nil {
			return err
		}
		if n.Pruning || n.RemoveAgainDue(s.now()) {
			return errUnpruned
		}
	}
	if l.Closing {
		if err := s.endGeneration(ctx, account, l.Generation, nil); err != nil {
			return err
		}
	}
	now := s.now()
	var ended, removed []string
	// A closing list's connection that cannot be read stays in it, and
	// fails the pass: the account's removal must find its families
	// (removeConnStrict; second review of #152, finding 1).
	var kept error
	for _, m := range l.Members {
		switch {
		case m.State == contracts.ConnectionEnded && now.Sub(m.EndedAt) >= EndedKept:
			remove := s.removeConn
			if l.Closing {
				remove = func(ctx context.Context, id string) error { return s.removeConnStrict(ctx, id, nil) }
			}
			if err := remove(ctx, m.ID); err != nil {
				if !l.Closing || !refusedRecord(err) {
					return err
				}
				if kept == nil {
					kept = err
				}
				continue
			}
			removed = append(removed, m.ID)
		case m.State == contracts.ConnectionEnded:
		case m.Pending && now.Sub(m.MadeAt) >= pendingFenceAfter:
			if err := s.fence(ctx, account, m, contracts.EndedByTime); err != nil {
				return err
			}
			ended = append(ended, m.ID)
		case !m.Pending:
			gone, err := s.sweepConn(ctx, m.ID, now, l.Closing)
			if err != nil {
				return err
			}
			if gone {
				ended = append(ended, m.ID)
			}
		}
	}
	if len(ended) == 0 && len(removed) == 0 {
		return kept
	}
	_, err = s.updateList(ctx, account, func(l *ListState) error {
		for _, id := range ended {
			s.dropEnded(ctx, l, l.End(id, now), nil)
		}
		l.RemoveEnded(removed)
		return nil
	})
	if err != nil {
		return err
	}
	return kept
}

// sweepConn is the sweep of a listed connection that is not pending: it
// ends one past its end, tidies a live one's devices and queues its ending
// notice; gone says the connection is ended or removed. The notice is
// queued only from a record still current: when the tidy's write lost to
// another writer (a renewal, a revocation), the record is read and decided
// again, never told from the one that lost (review of #197, finding 1).
// A record that keeps changing is left to the next pass.
func (s *Service) sweepConn(ctx context.Context, id string, now time.Time, closing bool) (gone bool, err error) {
	for range RecordTries {
		d, at, err := s.readConn(ctx, id)
		if err != nil && !refusedRecord(err) {
			return false, err
		}
		if d == nil || d.State == contracts.ConnectionEnded {
			return true, nil
		}
		if !now.Before(d.EndsAt) {
			if err := s.endConnection(ctx, id, contracts.EndedByTime, "", true); err != nil {
				return false, err
			}
			return true, nil
		}
		current, err := s.tidyConn(ctx, d, at)
		if err != nil {
			return false, err
		}
		if !current {
			continue
		}
		// Live, and ending within the week: told once per connection and
		// end (ADR 0041, E32); a closing account is told nothing.
		if !closing {
			s.notifyEnding(ctx, d, now)
		}
		return false, nil
	}
	return false, nil
}

// tidyConn ends the devices of an active connection whose codes can no
// longer be exchanged and drops the ended ones past their bounds. current
// is false when its write lost to another writer: d is no longer the
// connection's record.
func (s *Service) tidyConn(ctx context.Context, d *connDoc, at store.Mark) (current bool, err error) {
	before, _ := jsonSize(d.Devices)
	drop := d.tidy(s.now())
	for _, f := range drop {
		if err := s.removeFamily(ctx, f); err != nil {
			return false, err
		}
	}
	d.dropDevices(drop)
	if after, _ := jsonSize(d.Devices); after == before {
		return true, nil
	}
	if err := s.writeConn(ctx, d, at, false); err != nil {
		if retry(err) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// sweepAssertions removes the records of assertions of the hours before
// last, through the sealed list of those this enclave made (C1). Deleting
// that list leaves dead records in the store, never an assertion accepted
// twice: none of theirs is live.
func (s *Service) sweepAssertions(ctx context.Context) error {
	now := s.now().UTC().Truncate(time.Hour)
	for h := 2; h <= 48; h++ {
		partition := PartitionJTI + assertionHour(now.Add(-time.Duration(h)*time.Hour))
		after := ""
		for {
			entries, err := s.cfg.Entries.ListEntries(ctx, partition, "", after, sweepPage)
			if err != nil {
				return err
			}
			for _, e := range entries {
				after = e.Key
				name, err := envelope.OpenText(s.sealKey, e.Value, entryAAD(partition, e.Key))
				if err != nil || string(name) != e.Key {
					continue
				}
				if err := s.cfg.Records.RemoveRecord(ctx, e.Key); err != nil {
					return err
				}
				if err := s.cfg.Entries.DeleteEntry(ctx, partition, e.Key); err != nil && !errors.Is(err, store.ErrNotFound) {
					return err
				}
			}
			if len(entries) < sweepPage {
				break
			}
		}
	}
	return nil
}

// ---- limits ----

// limits are counters in this enclave's memory, by key and window.
type limits struct {
	mu sync.Mutex
	w  map[string]window
}

type window struct {
	start time.Time
	n     int
}

func newLimits() limits { return limits{w: map[string]window{}} }

// allow counts one use of key and says whether it is within max a window.
func (l *limits) allow(key string, max int, per time.Duration, now time.Time) bool {
	return l.allowN(key, 1, max, per, now)
}

// allowN counts n of key (bytes, for attachments).
func (l *limits) allowN(key string, n, max int, per time.Duration, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.w) >= cacheMax {
		for k, w := range l.w {
			if now.Sub(w.start) >= time.Hour {
				delete(l.w, k)
			}
		}
	}
	w := l.w[key]
	if now.Sub(w.start) >= per || now.Before(w.start) {
		w = window{start: now}
	}
	if w.n+n > max {
		l.w[key] = w
		return false
	}
	w.n += n
	l.w[key] = w
	return true
}
