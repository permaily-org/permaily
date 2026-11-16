package connections

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/oauth"
	"github.com/permaily-org/permaily/internal/services/receipts"
	"github.com/permaily-org/permaily/internal/store"
)

// The reproductions of the review of pull request 136, each under the
// number of the test of ADR 0030 it belongs to.

// V59: a started answer ends when its verified authority's freshness ends,
// also while the read that would renew it is blocked (the owner's decisions
// 6 and 14, N2).
func TestV59LeaseEndsWhileTheFreshReadIsBlocked(t *testing.T) {
	ctx := context.Background()
	started := func(t *testing.T) (*world, *Service, *failing, *oauth.Grant, context.Context, chan struct{}, chan struct{}) {
		w := newWorld(t)
		a, f := w.hooked()
		x := w.ask(claudeCode)
		tok := w.connectAll(a, w.person(accountA), x)
		g := w.grant(a, x, tok.AccessToken)
		_, rel, err := a.Search(ctx, g, contracts.VendorSearch{})
		if err != nil {
			t.Fatal(err)
		}
		c, err := rel.Start(ctx)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { rel.Finish(false) })
		entered, unblock := make(chan struct{}), make(chan struct{})
		var once sync.Once
		f.set(func(op, name string) error {
			if op == "get" && name == connName(g.Connection) {
				once.Do(func() { close(entered) })
				<-unblock
			}
			return nil
		})
		t.Cleanup(func() { close(unblock) })
		return w, a, f, g, c, entered, unblock
	}
	ended := func(c context.Context) bool {
		select {
		case <-c.Done():
			return true
		case <-time.After(2 * time.Second):
			return false
		}
	}

	t.Run("its freshness passed with the read blocked", func(t *testing.T) {
		w, _, _, g, c, _, _ := started(t)
		if _, err := w.enclave().Revoke(ctx, w.person(accountA), g.Connection); err != nil {
			t.Fatal(err)
		}
		w.clock.add(oauth.ConnectionFresh + time.Second)
		if !ended(c) {
			t.Fatal("stream remains writable beyond five seconds while the fresh authority read is blocked")
		}
	})

	t.Run("a read begun before its freshness passed and still blocked when it does", func(t *testing.T) {
		w, _, _, g, c, entered, _ := started(t)
		if _, err := w.enclave().Revoke(ctx, w.person(accountA), g.Connection); err != nil {
			t.Fatal(err)
		}
		w.clock.add(oauth.ConnectionFresh - time.Second)
		select {
		case <-entered:
		case <-time.After(2 * time.Second):
			t.Fatal("the watcher did not read the authority again before its freshness passed")
		}
		if c.Err() != nil {
			t.Fatal("ended before its freshness passed")
		}
		w.clock.add(2 * time.Second)
		if !ended(c) {
			t.Fatal("stream remains writable after its freshness passed while the read is blocked")
		}
	})
}

// V59: a read made before the freshness passes and returning in time keeps
// the answer going past the freshness it began with.
func TestV59LeaseReadInTimeKeepsTheAnswer(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	s := w.enclave()
	x := w.ask(claudeCode)
	tok := w.connectAll(s, w.person(accountA), x)
	g := w.grant(s, x, tok.AccessToken)
	_, rel, err := s.Search(ctx, g, contracts.VendorSearch{})
	if err != nil {
		t.Fatal(err)
	}
	defer rel.Finish(true)
	c, err := rel.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	l := rel.(*lease)
	checked := func() time.Time {
		l.mu.Lock()
		defer l.mu.Unlock()
		return l.grant.CheckedAt
	}
	first := checked()
	w.clock.add(oauth.ConnectionFresh - time.Second)
	for i := 0; !checked().After(first); i++ {
		if i == 200 {
			t.Fatal("the lease did not read its authority again before its freshness passed")
		}
		time.Sleep(10 * time.Millisecond)
	}
	w.clock.add(2 * time.Second)
	time.Sleep(3 * leaseTick)
	if c.Err() != nil {
		t.Fatal("an answer whose authority was read again in time ended")
	}
}

// reviewAfterRead runs after a record's read returns, before the service
// uses it: a read delayed after it was made.
type reviewAfterRead struct {
	Records
	mu    sync.Mutex
	after func(string)
}

func (r *reviewAfterRead) GetRecordAt(ctx context.Context, name string) (*store.Record, store.Mark, error) {
	v, m, e := r.Records.GetRecordAt(ctx, name)
	r.mu.Lock()
	after := r.after
	r.mu.Unlock()
	if after != nil {
		after(name)
	}
	return v, m, e
}

// V27: a read that returns late is as old as when it began: it never dates
// the authority it read at its end (finding 2).
func TestV27SlowReadNeverRenewsCachedAuthority(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	b := w.enclave()
	x := w.ask(claudeCode)
	tok := w.connectAll(b, w.person(accountA), x)
	g := w.grant(b, x, tok.AccessToken)
	records := &reviewAfterRead{Records: &store.Anchored{Records: w.mem, Commits: w.commits, Strict: true, Now: w.clock.now}}
	a, err := New(w.config(records))
	if err != nil {
		t.Fatal(err)
	}
	records.after = func(name string) {
		if name == connName(g.Connection) {
			records.after = nil
			if _, e := b.Revoke(ctx, w.person(accountA), g.Connection); e != nil {
				t.Error(e)
			}
			w.clock.add(10 * time.Second)
		}
	}
	_, _ = w.check(a, x, tok.AccessToken)
	stale, err := w.check(a, x, tok.AccessToken)
	if err != nil {
		return
	}
	_, rel, err := a.Search(ctx, stale, contracts.VendorSearch{})
	if err != nil {
		return
	}
	defer rel.Finish(false)
	if _, err = rel.Start(ctx); err == nil {
		t.Fatal("revoked authority was re-dated at the delayed read's end; mail can start 10 seconds after the revocation")
	}
}

// V27: a write that fails leaves what readers see as it was committed
// (finding 3).
func TestV27FailedWriteLeavesTheCacheAsCommitted(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	a, f := w.hooked()
	x := w.ask(claudeCode)
	tok := w.connectAll(a, w.person(accountA), x)
	g := w.grant(a, x, tok.AccessToken)
	w.clock.add(time.Hour)
	p := w.person(accountA)
	before, err := a.Get(ctx, p, g.Connection)
	if err != nil {
		t.Fatal(err)
	}
	f.set(func(op, name string) error {
		if op == "put" && name == connName(g.Connection) {
			return errCrash
		}
		return nil
	})
	_, err = a.Renew(ctx, p, g.Connection, contracts.ConnectionRenewal{Version: before.Version, ModelNotice: notice})
	if !errors.Is(err, errCrash) {
		t.Fatal(err)
	}
	cached, _, err := a.connFresh(ctx, g.Connection)
	if err != nil {
		t.Fatal(err)
	}
	if cached.EndsAt.After(before.EndsAt) {
		t.Fatal("a failed PutRecordAt extended the cached authority through its shared pointer")
	}
}

// V27: renewing while tokens are checked shares no document between them
// (finding 3; run with -race).
func TestV27CachedRecordDoesNotRaceRenew(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	s := w.enclave()
	x := w.ask(claudeCode)
	tok := w.connectAll(s, w.person(accountA), x)
	g := w.grant(s, x, tok.AccessToken)
	p := w.person(accountA)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range 3000 {
			_, _ = w.check(s, x, tok.AccessToken)
		}
	}()
	for range 30 {
		v, err := s.Get(ctx, p, g.Connection)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = s.Renew(ctx, p, g.Connection, contracts.ConnectionRenewal{Version: v.Version, ModelNotice: notice})
	}
	wg.Wait()
}

// V27: the review's positive case: a device unknown to a warm cache is read
// again, and the read refuses it once revoked, its sibling still working.
func TestV27UnknownDeviceReadAgainStillRefusesItsRevocation(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	a, b := w.enclave(), w.enclave()
	x := w.ask(claudeCode)
	old := w.connectAll(a, w.person(accountA), x)
	_ = w.grant(a, x, old.AccessToken)
	y := w.ask(claudeCode)
	req := w.start(b, w.person(accountA), y)
	fresh, err := w.exchange(b, y, w.allow(b, w.person(accountA), req))
	if err != nil {
		t.Fatal(err)
	}
	g := w.grant(b, y, fresh.AccessToken)
	if _, err = b.RevokeDevice(ctx, w.person(accountA), g.Connection, g.Family); err != nil {
		t.Fatal(err)
	}
	if _, err = w.check(a, y, fresh.AccessToken); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("unknown revoked device accepted: %v", err)
	}
	if _, err = w.check(a, x, old.AccessToken); err != nil {
		t.Fatalf("unrevoked sibling refused: %v", err)
	}
}

// V30: cancelling a deletion whose fence was never written ends the
// connections first; they never work again (finding 4).
func TestV30CancelAfterAFailedFence(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	a, f := w.hooked()
	x := w.ask(claudeCode)
	tok, _ := w.connect(a, w.person(accountA), x)
	w.accts.set(accountA, StandingClosing)
	f.set(func(op, name string) error {
		if op == "put" && strings.HasPrefix(name, RecordList) {
			return errCrash
		}
		return nil
	})
	if err := a.EndAll(ctx, accountA); !errors.Is(err, errCrash) {
		t.Fatal(err)
	}
	f.set(nil)
	b := w.enclave()
	if err := b.Reopen(ctx, accountA); err != nil {
		t.Fatal(err)
	}
	w.accts.set(accountA, StandingOpen)
	if _, err := w.check(w.enclave(), x, tok.AccessToken); err == nil {
		t.Fatal("old token works after a failed EndAll and a successful cancellation")
	}
}

// V21: a proof that expires while the approval's records are written
// commits nothing usable and hands out no code (finding 5).
func TestV21ProofExpiresDuringTheApprovalsWrites(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	s, f := w.hooked()
	p := w.person(accountA)
	p.ConfirmedUntil = w.clock.now().Add(time.Second)
	x := w.ask(claudeCode)
	req := w.start(s, p, x)
	once := false
	f.set(func(op, name string) error {
		if !once && (op == "create" || op == "put") && strings.HasPrefix(name, RecordList) {
			once = true
			w.clock.add(2 * time.Second)
		}
		return nil
	})
	ans, err := s.Decide(ctx, p, req.ID, contracts.AuthorizationDecision{Allow: true, Draft: &req.Draft, Snapshot: &req.Snapshot, ModelNotice: notice})
	if err == nil {
		if _, e := w.exchange(w.enclave(), x, codeOf(t, ans.Redirect)); e != nil {
			t.Fatal(e)
		}
		t.Fatal("approval issued a usable code after the proof expired during the first list write")
	}
	if !errors.Is(err, contracts.ErrConfirmationNeeded) {
		t.Fatal(err)
	}
}

// V25: a renewal whose consent receipt could not be written renews nothing:
// neither its end nor the notice it delivers under (finding 6).
func TestV25RenewalWhoseReceiptFailedRenewsNothing(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	w.notice = "2026-10-01" // approved under the notice before
	s := w.enclave()
	x := w.ask(claudeCode)
	tok := w.connectAll(s, w.person(accountA), x)
	g := w.grant(s, x, tok.AccessToken)
	w.notice = notice
	s = w.enclave()
	w.clock.add(10 * time.Minute)
	p := w.person(accountA)
	before, err := s.Get(ctx, p, g.Connection)
	if err != nil {
		t.Fatal(err)
	}
	if before.Delivery != contracts.DeliveryNoticeChanged {
		t.Fatal(before.Delivery)
	}
	w.rcpt.mu.Lock()
	w.rcpt.fail = errCrash
	w.rcpt.mu.Unlock()
	if _, err = s.Renew(ctx, p, g.Connection, contracts.ConnectionRenewal{Version: before.Version, ModelNotice: notice}); err == nil {
		t.Fatal("wanted the receipt's failure")
	}
	w.rcpt.mu.Lock()
	w.rcpt.fail = nil
	w.rcpt.mu.Unlock()
	after, err := w.enclave().Get(ctx, p, g.Connection)
	if err != nil {
		t.Fatal(err)
	}
	if after.EndsAt.After(before.EndsAt) || after.Version != before.Version {
		t.Fatal("a failed renewal changed the connection's durable end without a consent receipt")
	}
	if after.ModelNotice != before.ModelNotice || after.Delivery != contracts.DeliveryNoticeChanged {
		t.Fatal("a failed renewal made the connection deliver under the new notice")
	}
	if _, _, err := w.enclave().Search(ctx, w.grant(w.enclave(), x, tok.AccessToken), contracts.VendorSearch{}); !errors.Is(err, contracts.ErrModelUnavailable) {
		t.Fatalf("a failed renewal delivers: %v", err)
	}
	// Its receipt written, the renewal is made.
	if _, err = s.Renew(ctx, p, g.Connection, contracts.ConnectionRenewal{Version: before.Version, ModelNotice: notice}); err != nil {
		t.Fatal(err)
	}
	after, err = w.enclave().Get(ctx, p, g.Connection)
	if err != nil || !after.EndsAt.After(before.EndsAt) || after.Delivery != contracts.DeliveryOn {
		t.Fatal(after, err)
	}
}

// V23 (N5): a replacement commits only over the connection at the version
// the screen showed (finding 7).
func TestV23ReplacementKeepsTheSnapshotShown(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	a, f := w.hooked()
	b := w.enclave()
	p := w.person(accountA)
	x := w.ask(claudeCode)
	_, _ = w.connect(a, p, x)
	y := w.ask(claudeCode)
	y.resource = apiResource
	req := w.start(a, p, y)
	if req.Snapshot.Change != contracts.ChangeReplace {
		t.Fatal(req.Snapshot)
	}
	once := false
	f.set(func(op, name string) error {
		if !once && op == "put" && strings.HasPrefix(name, RecordList) {
			once = true
			c, e := b.Get(ctx, p, req.Snapshot.Connection)
			if e != nil {
				t.Error(e)
				return nil
			}
			if _, e = b.Renew(ctx, p, c.ID, contracts.ConnectionRenewal{Version: c.Version, ModelNotice: notice}); e != nil {
				t.Error(e)
			}
		}
		return nil
	})
	ans, err := a.Decide(ctx, p, req.ID, contracts.AuthorizationDecision{Allow: true, Draft: &req.Draft, Snapshot: &req.Snapshot, ModelNotice: notice})
	if err == nil {
		t.Fatalf("a stale replacement still committed, redirect=%t", ans.Redirect != "")
	}
	if !errors.Is(err, contracts.ErrConsentChanged) {
		t.Fatal(err)
	}
}

// V32: a refusal of a request a valid token attributes has its receipt
// (finding 8).
func TestV32AttributedRefusalsHaveReceipts(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	s := w.enclave()
	x := w.ask(claudeCode)
	tok := w.connectAll(s, w.person(accountA), x)
	g := w.grant(s, x, tok.AccessToken)
	before := len(w.rcpt.of(receipts.KindRefused))
	_, _, err := s.Search(ctx, g, contracts.VendorSearch{Cursor: "bad"})
	if !errors.Is(err, contracts.ErrInvalidCursor) {
		t.Fatal(err)
	}
	if len(w.rcpt.of(receipts.KindRefused)) == before {
		t.Fatal("an attributed invalid_cursor refusal has no receipt")
	}
}

// V32: a page whose receipt would pass its bound by its framing is cut,
// never failed (finding 9).
func TestV32ReceiptBoundaryCutsThePage(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	s := w.enclave()
	x := w.ask(claudeCode)
	tok := w.connectAll(s, w.person(accountA), x)
	g := w.grant(s, x, tok.AccessToken)
	r, err := s.prepare(ctx, g, nil)
	if err != nil {
		t.Fatal(err)
	}
	rc := s.receipt(r, receipts.KindSearch)
	base, _ := jsonSize(rc)
	// 32 legal messages whose receipt parts sum to the budget counted
	// without the lists' framing.
	remaining := receipts.MaxDocumentBytes - base
	var msgs []mail
	for i := range 32 {
		m := mail{ID: fmt.Sprintf("z%03d", i), Mailbox: mailboxID(1), Received: start.Add(-time.Hour), From: "a@example.com"}
		o, _ := w.disc.outcome(m, r.d)
		n, _ := jsonSize(delivered(m.Mailbox, o.Disclosed, r.d.Parts))
		add := (remaining / (32 - i)) - n - 1
		if add < 0 {
			t.Fatal(add)
		}
		m.Subject = strings.Repeat("s", add)
		remaining -= n + add + 1
		msgs = append(msgs, m)
	}
	w.mbx.mu.Lock()
	w.mbx.msgs[mailboxID(1)] = msgs
	w.mbx.msgs[mailboxID(2)] = nil
	w.mbx.mu.Unlock()
	page, rel, err := s.Search(ctx, g, contracts.VendorSearch{Limit: 50})
	if err != nil {
		t.Fatalf("a page at the bound fails instead of being cut: %v", err)
	}
	rel.Finish(false)
	if page.Cursor == "" {
		t.Fatal("expected a cut before the bound")
	}
}

// V25: an old refresh token replayed to a cold enclave after more than
// eight epochs of rotations still reaches reuse detection and ends its
// device (finding 10).
func TestV25OldRefreshAcrossManyEpochsEndsTheFamily(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	a := w.enclave()
	x := w.ask(claudeCode)
	tok := w.connectAll(a, w.person(accountA), x)
	g := w.grant(a, x, tok.AccessToken)
	// The terminal states of epochs 0 to 7 that 8*400 rotations reach; the
	// old refresh token has not expired.
	f, mark, err := a.readFamily(ctx, g.Family, 0)
	if err != nil {
		t.Fatal(err)
	}
	for e := range uint64(8) {
		f.Epoch = e
		f.Status = oauth.FamilyMoved
		f.Generation = (e + 1) * oauth.FamilyEpochRotations
		f.From = f.Generation - 1
		f.FromScope = f.Scopes
		f.RotatedAt = w.clock.now()
		if err = a.writeSealed(ctx, familyName(g.Family, e), f, mark, e > 0); err != nil {
			t.Fatal(err)
		}
		mark = store.Mark{}
	}
	f.Epoch = 8
	f.Status = oauth.FamilyActive
	if err = a.writeSealed(ctx, familyName(g.Family, 8), f, store.Mark{}, true); err != nil {
		t.Fatal(err)
	}
	restarted := w.enclave()
	_, err = w.refresh(restarted, x, tok.RefreshToken)
	if oauthCode(err) != oauth.ErrInvalidGrant {
		t.Errorf("the old token did not reach reuse detection: %v", err)
	}
	if _, err = w.check(w.enclave(), x, tok.AccessToken); err == nil {
		t.Fatal("the family stays active after an old refresh replayed at epoch 8")
	}
}

// V23: asking again with the starting draft keeps the original connection's
// new-mail cutoff (finding 11).
func TestV23AskingAgainKeepsTheOriginalSince(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	a, b := w.enclave(), w.enclave()
	x := w.ask(claudeCode)
	first, _ := w.connect(a, w.person(accountA), x)
	w.clock.add(10 * time.Minute)
	w.mbx.add(mail{ID: "zmid", Mailbox: mailboxID(1), Received: w.clock.now(), From: "sender@example.com", Subject: "between approvals"})
	w.clock.add(10 * time.Minute)
	y := w.ask(claudeCode)
	req := w.start(b, w.person(accountA), y)
	second, err := w.exchange(b, y, w.allow(b, w.person(accountA), req))
	if err != nil {
		t.Fatal(err)
	}
	g1 := w.grant(a, x, first.AccessToken)
	g2 := w.grant(b, y, second.AccessToken)
	_, r, err := a.Read(ctx, g1, mailboxID(1)+".zmid")
	if err != nil {
		t.Fatal(err)
	}
	r.Finish(false)
	_, r, err = b.Read(ctx, g2, mailboxID(1)+".zmid")
	if err == nil {
		r.Finish(false)
	}
	if errors.Is(err, contracts.ErrNotFound) {
		t.Fatal("the unchanged default draft moved the new-mail cutoff from the original consent to the second device's approval")
	}
	if err != nil {
		t.Fatal(err)
	}
}

// V27: a read that began before the one cached never replaces it, and a
// fresh read that returns after the freshness it would give has passed
// renews nothing (finding 2).
func TestV27ReadsAreAsOldAsWhenTheyBegan(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	b := w.enclave()
	x := w.ask(claudeCode)
	tok := w.connectAll(b, w.person(accountA), x)
	g := w.grant(b, x, tok.AccessToken)
	records := &reviewAfterRead{Records: &store.Anchored{Records: w.mem, Commits: w.commits, Strict: true, Now: w.clock.now}}
	a, err := New(w.config(records))
	if err != nil {
		t.Fatal(err)
	}

	t.Run("a late read never replaces a newer one", func(t *testing.T) {
		d, at, began, err := a.readConnAt(ctx, g.Connection)
		if err != nil {
			t.Fatal(err)
		}
		a.cacheConn(g.Connection, nil, at, store.ErrRolledBack, began.Add(-time.Second))
		cached, read, err := a.connFresh(ctx, g.Connection)
		if err != nil || cached == nil || !read.Equal(began) || cached.EndsAt != d.EndsAt {
			t.Fatalf("an older read replaced the newer: %v %v", read, err)
		}
	})

	t.Run("a fresh read slower than the freshness renews nothing", func(t *testing.T) {
		grant := *g
		grant.CheckedAt = w.clock.now().Add(-oauth.ConnectionFresh)
		records.mu.Lock()
		records.after = func(name string) {
			if name == connName(g.Connection) {
				w.clock.add(oauth.ConnectionFresh + time.Second)
			}
		}
		records.mu.Unlock()
		defer func() {
			records.mu.Lock()
			records.after = nil
			records.mu.Unlock()
		}()
		checked := grant.CheckedAt
		if err := a.again(ctx, &grant); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("a read older than its freshness when it returned was used: %v", err)
		}
		if !grant.CheckedAt.Equal(checked) {
			t.Fatal("the grant's freshness was extended")
		}
	})
}

// V23 (N5): more races after the screen's last read: a connection that
// appears for a client whose screen showed none, a device added to the one
// shown, and the shown one renewed while the new record is made. Each is
// consent_changed, the connection there before as it was, and what the
// approval made ended.
func TestV23ReplacementRacesAfterTheSnapshot(t *testing.T) {
	ctx := context.Background()
	type race struct {
		name    string
		existed bool
		at      func(op, name string) bool
		do      func(t *testing.T, w *world, b *Service, p Person, shown string)
	}
	atList := func(op, name string) bool { return op == "put" && strings.HasPrefix(name, RecordList) }
	atRecord := func(op, name string) bool { return op == "create" && strings.HasPrefix(name, RecordConnection) }
	for _, r := range []race{
		{name: "a connection appears where none was shown", existed: false, at: func(op, name string) bool {
			return (op == "create" || op == "put") && strings.HasPrefix(name, RecordList)
		}, do: func(t *testing.T, w *world, b *Service, p Person, _ string) {
			_, _ = w.connect(b, p, w.ask(claudeCode))
		}},
		{name: "a device added to the one shown", existed: true, at: atList, do: func(t *testing.T, w *world, b *Service, p Person, _ string) {
			_, _ = w.connect(b, p, w.ask(claudeCode))
		}},
		{name: "the one shown renewed as the new record is made", existed: true, at: atRecord, do: func(t *testing.T, w *world, b *Service, p Person, shown string) {
			c, err := b.Get(ctx, p, shown)
			if err != nil {
				t.Error(err)
				return
			}
			if _, err = b.Renew(ctx, p, shown, contracts.ConnectionRenewal{Version: c.Version, ModelNotice: notice}); err != nil {
				t.Error(err)
			}
		}},
	} {
		t.Run(r.name, func(t *testing.T) {
			w := newWorld(t)
			a, f := w.hooked()
			b := w.enclave()
			p := w.person(accountA)
			p2 := p
			p2.Session = "ses_other"
			var before *TokenAnswer
			x := w.ask(claudeCode)
			if r.existed {
				before, _ = w.connect(b, p2, x)
			}
			y := w.ask(claudeCode)
			y.resource = apiResource
			req := w.start(a, p, y)
			once := false
			f.set(func(op, name string) error {
				if !once && r.at(op, name) {
					once = true
					r.do(t, w, b, p2, req.Snapshot.Connection)
				}
				return nil
			})
			_, err := a.Decide(ctx, p, req.ID, contracts.AuthorizationDecision{Allow: true, Draft: &req.Draft, Snapshot: &req.Snapshot, ModelNotice: notice})
			f.set(nil)
			if !errors.Is(err, contracts.ErrConsentChanged) {
				t.Fatalf("committed over what the screen did not show: %v", err)
			}
			if before != nil {
				if _, err := w.check(w.enclave(), x, before.AccessToken); err != nil {
					t.Fatalf("the connection there before was ended: %v", err)
				}
				if _, err := w.check(a, x, before.AccessToken); err != nil {
					t.Fatalf("the connection there before was ended in the approving enclave: %v", err)
				}
			}
			l, _, err := b.readList(ctx, accountA)
			if err != nil {
				t.Fatal(err)
			}
			for _, m := range l.Members {
				if m.ID != l.Current[claudeCode] && m.State != contracts.ConnectionEnded {
					t.Fatalf("what the refused approval made is not ended: %+v", l.Members)
				}
			}
		})
	}
}
