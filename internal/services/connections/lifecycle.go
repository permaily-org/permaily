package connections

import (
	"context"
	"errors"
	"fmt"
	"strconv"
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
	return s.endGeneration(ctx, account, l.Generation)
}

// endGeneration ends every member of the list made before generation, and
// writes them ended in the list.
func (s *Service) endGeneration(ctx context.Context, account string, generation uint64) error {
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
			s.dropEnded(ctx, l, l.End(id, now))
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

// Reopen implements Lifecycle: whatever of the closing generation is
// still active is ended first, and only then is the fence lifted, so a
// cancelled deletion never brings a connection back.
func (s *Service) Reopen(ctx context.Context, account string) error {
	l, _, err := s.readList(ctx, account)
	if err != nil || l == nil || !l.Closing {
		return err
	}
	if err := s.endGeneration(ctx, account, l.Generation); err != nil {
		return err
	}
	_, err = s.updateList(ctx, account, func(l *ListState) error {
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
	if err != nil || l == nil {
		return err
	}
	if l.Closing {
		if err := s.endGeneration(ctx, account, l.Generation); err != nil {
			return err
		}
	}
	now := s.now()
	var ended, removed []string
	for _, m := range l.Members {
		switch {
		case m.State == contracts.ConnectionEnded && now.Sub(m.EndedAt) >= EndedKept:
			if err := s.removeConn(ctx, m.ID); err != nil {
				return err
			}
			removed = append(removed, m.ID)
		case m.State == contracts.ConnectionEnded:
		case m.Pending && now.Sub(m.MadeAt) >= pendingFenceAfter:
			if err := s.fence(ctx, account, m, contracts.EndedByTime); err != nil {
				return err
			}
			ended = append(ended, m.ID)
		case !m.Pending:
			d, at, err := s.readConn(ctx, m.ID)
			if err != nil && !refusedRecord(err) {
				return err
			}
			if d == nil || d.State == contracts.ConnectionEnded {
				ended = append(ended, m.ID)
				continue
			}
			if !now.Before(d.EndsAt) {
				if err := s.endConnection(ctx, m.ID, contracts.EndedByTime, "", true); err != nil {
					return err
				}
				ended = append(ended, m.ID)
				continue
			}
			if err := s.tidyConn(ctx, d, at); err != nil {
				return err
			}
		}
	}
	if len(ended) == 0 && len(removed) == 0 {
		return nil
	}
	_, err = s.updateList(ctx, account, func(l *ListState) error {
		for _, id := range ended {
			s.dropEnded(ctx, l, l.End(id, now))
		}
		l.RemoveEnded(removed)
		return nil
	})
	return err
}

// tidyConn ends the devices of an active connection whose codes can no
// longer be exchanged and drops the ended ones past their bounds.
func (s *Service) tidyConn(ctx context.Context, d *connDoc, at store.Mark) error {
	before, _ := jsonSize(d.Devices)
	drop := d.tidy(s.now())
	for _, f := range drop {
		if err := s.removeFamily(ctx, f); err != nil {
			return err
		}
	}
	d.dropDevices(drop)
	if after, _ := jsonSize(d.Devices); after == before {
		return nil
	}
	if err := s.writeConn(ctx, d, at, false); err != nil && !retry(err) {
		return err
	}
	return nil
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
