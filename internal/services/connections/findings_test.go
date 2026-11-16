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
		n, _ := jsonSize(receipts.DeliveredOf(m.Mailbox, o.Disclosed, r.d.Parts))
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

// hookedEntries is the entries of one enclave with a test's hook before a
// write.
type hookedEntries struct {
	store.Entries
	mu     sync.Mutex
	before func(e *store.Entry)
}

func (h *hookedEntries) PutEntry(ctx context.Context, e *store.Entry) error {
	h.mu.Lock()
	f := h.before
	h.mu.Unlock()
	if f != nil {
		f(e)
	}
	return h.Entries.PutEntry(ctx, e)
}

// V21: the proof, or the request, ending during each write of an
// approval's commit (the list's first write, the record, the list's last
// write, the receipt, the code's entry; a device's record): Decide refuses,
// hands out no code, leaves nothing of its own usable, and ends nothing
// that was there before until its end was in time (finding 5).
func TestV21ProofOrRequestEndsAtEachWriteOfTheCommit(t *testing.T) {
	ctx := context.Background()
	type barrier struct {
		name     string
		existing bool // a connection of the client, replaced or added to
		device   bool // the draft adds a device to it
		request  bool // the request ends, not the proof
		at       func(w *world, f *failing, e *hookedEntries, jump func())
		want     error
		oldKept  bool // the connection there before still works
	}
	list := func(n int) func(w *world, f *failing, e *hookedEntries, jump func()) {
		return func(w *world, f *failing, _ *hookedEntries, jump func()) {
			seen := 0
			f.set(func(op, name string) error {
				if (op == "create" || op == "put") && strings.HasPrefix(name, RecordList) {
					if seen++; seen == n {
						jump()
					}
				}
				return nil
			})
		}
	}
	record := func(op string) func(w *world, f *failing, e *hookedEntries, jump func()) {
		return func(w *world, f *failing, _ *hookedEntries, jump func()) {
			once := false
			f.set(func(o, name string) error {
				if !once && o == op && strings.HasPrefix(name, RecordConnection) {
					once = true
					jump()
				}
				return nil
			})
		}
	}
	receipt := func(w *world, _ *failing, _ *hookedEntries, jump func()) {
		w.rcpt.mu.Lock()
		w.rcpt.onWrite = func(r *receipts.Receipt) {
			if r.Kind == receipts.KindConsent {
				jump()
			}
		}
		w.rcpt.mu.Unlock()
	}
	codeEntry := func(w *world, _ *failing, e *hookedEntries, jump func()) {
		e.mu.Lock()
		e.before = func(en *store.Entry) {
			if strings.HasPrefix(en.Partition, PartitionCode) {
				jump()
			}
		}
		e.mu.Unlock()
	}
	for _, b := range []barrier{
		{name: "the list's first write", at: list(1), want: contracts.ErrConfirmationNeeded},
		{name: "the record", at: record("create"), want: contracts.ErrConfirmationNeeded},
		{name: "the list's last write", at: list(2), want: contracts.ErrConfirmationNeeded},
		{name: "the receipt", at: receipt, want: contracts.ErrConfirmationNeeded},
		{name: "the code's entry", at: codeEntry, want: contracts.ErrConfirmationNeeded},
		{name: "the request ends at the record", request: true, at: record("create"), want: contracts.ErrRequestEnded},
		{name: "a replacement's new record", existing: true, at: record("create"), want: contracts.ErrConfirmationNeeded, oldKept: true},
		{name: "a replacement's last list write", existing: true, at: list(2), want: contracts.ErrConfirmationNeeded},
		{name: "a device's list write", existing: true, device: true, at: list(1), want: contracts.ErrConfirmationNeeded, oldKept: true},
		{name: "a device's record", existing: true, device: true, at: record("put"), want: contracts.ErrConfirmationNeeded, oldKept: true},
		{name: "a device's code", existing: true, device: true, at: codeEntry, want: contracts.ErrConfirmationNeeded, oldKept: true},
	} {
		t.Run(b.name, func(t *testing.T) {
			w := newWorld(t)
			f := &failing{Records: &store.Anchored{Records: w.mem, Commits: w.commits, Strict: true, Now: w.clock.now}}
			e := &hookedEntries{Entries: w.mem}
			cfg := w.config(f)
			cfg.Entries = e
			s, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			p := w.person(accountA)
			var old *TokenAnswer
			x := w.ask(claudeCode)
			if b.existing {
				old, _ = w.connect(w.enclave(), w.person(accountA), x)
			}
			y := w.ask(claudeCode)
			if b.existing && !b.device {
				y.resource = apiResource
			}
			if b.request {
				p.ConfirmedUntil = w.clock.now().Add(time.Hour)
			} else {
				p.ConfirmedUntil = w.clock.now().Add(time.Minute)
			}
			req := w.start(s, p, y)
			if b.device != (req.Snapshot.Change == contracts.ChangeAddDevice) {
				t.Fatal(req.Snapshot.Change)
			}
			jumped := false
			b.at(w, f, e, func() {
				if !jumped {
					jumped = true
					if b.request {
						w.clock.add(oauth.RequestTTL)
					} else {
						w.clock.add(2 * time.Minute)
					}
				}
			})
			ans, err := s.Decide(ctx, p, req.ID, contracts.AuthorizationDecision{Allow: true, Draft: &req.Draft, Snapshot: &req.Snapshot, ModelNotice: notice})
			f.set(nil)
			e.mu.Lock()
			e.before = nil
			e.mu.Unlock()
			w.rcpt.mu.Lock()
			w.rcpt.onWrite = nil
			w.rcpt.mu.Unlock()
			if !jumped {
				t.Fatal("the barrier was not reached")
			}
			if err == nil {
				_, e := w.exchange(w.enclave(), y, codeOf(t, ans.Redirect))
				t.Fatalf("a code was handed out after the deadline passed (its exchange: %v)", e)
			}
			if !errors.Is(err, b.want) {
				t.Fatal(err)
			}
			// Nothing this approval made is usable: every device of the
			// client's connections is the old one's, or ended.
			l, _, err := w.enclave().readList(ctx, accountA)
			if err != nil {
				t.Fatal(err)
			}
			for _, m := range l.Members {
				d, _, err := w.enclave().readConn(ctx, m.ID)
				if err != nil || d == nil {
					continue
				}
				for _, dev := range d.Devices {
					if dev.State == contracts.ConnectionActive && dev.Code.Status != oauth.CodeExchanged && d.State == contracts.ConnectionActive {
						t.Fatalf("an unexchanged device of the refused approval is active: %+v", dev)
					}
				}
			}
			if old != nil && b.oldKept {
				if _, err := w.check(w.enclave(), x, old.AccessToken); err != nil {
					t.Fatalf("the connection there before was ended: %v", err)
				}
			}
		})
	}
}

// V23: asking again some time after the first approval, with the starting
// draft or a deliberate change: the same permission adds a device reading
// what the first reads; choosing new mail only again narrows the device
// to this approval; a replacement keeps the first approval's cutoff
// (finding 11).
func TestV23AskingAgainLaterKeepsOrMovesTheSinceAsChosen(t *testing.T) {
	ctx := context.Background()
	for _, c := range []struct {
		name     string
		resource string
		since    *contracts.MailSince
		change   contracts.ConnectionChange
		reads    bool
	}{
		{name: "the same permission", change: contracts.ChangeAddDevice, reads: true},
		{name: "new mail only chosen again", since: &contracts.MailSince{New: true}, change: contracts.ChangeAddDevice, reads: false},
		{name: "a replacement", resource: apiResource, change: contracts.ChangeReplace, reads: true},
	} {
		t.Run(c.name, func(t *testing.T) {
			w := newWorld(t)
			a, b := w.enclave(), w.enclave()
			x := w.ask(claudeCode)
			first, _ := w.connect(a, w.person(accountA), x)
			w.clock.add(10 * time.Minute)
			w.mbx.add(mail{ID: "zmid", Mailbox: mailboxID(1), Received: w.clock.now(), From: "sender@example.com", Subject: "between approvals"})
			w.clock.add(10 * time.Minute)
			y := w.ask(claudeCode)
			if c.resource != "" {
				y.resource = c.resource
			}
			p := w.person(accountA)
			req := w.start(b, p, y)
			d := req.Draft
			if d.Since.New || !d.Since.At.Equal(start) {
				t.Fatalf("the starting draft's since is not the first approval's: %+v", d.Since)
			}
			snap := req.Snapshot
			if c.since != nil {
				d.Since = *c.since
				pv, err := b.Preview(ctx, p, req.ID, contracts.ConsentPreview{Draft: d, Mailbox: mailboxID(1)})
				if err != nil {
					t.Fatal(err)
				}
				snap = pv.Snapshot
			}
			if snap.Change != c.change {
				t.Fatal(snap.Change)
			}
			second, err := w.exchange(b, y, w.allowDraft(b, p, req, d, snap))
			if err != nil {
				t.Fatal(err)
			}
			if c.change == contracts.ChangeAddDevice {
				_, r, err := a.Read(ctx, w.grant(a, x, first.AccessToken), mailboxID(1)+".zmid")
				if err != nil {
					t.Fatal(err)
				}
				r.Finish(false)
			}
			_, r, err := b.Read(ctx, w.grant(b, y, second.AccessToken), mailboxID(1)+".zmid")
			if err == nil {
				r.Finish(false)
			}
			if c.reads != (err == nil) {
				t.Fatalf("the second device reads mail received between the approvals: %v, want %v", err, c.reads)
			}
		})
	}
}

// V25: more than eight epochs of real rotations, one rollover crashing
// before it made its next epoch: a cold enclave refreshes the newest token,
// and an old token replayed to another cold enclave reaches reuse
// detection and ends the device (finding 10).
func TestV25ColdEnclavesAcrossManyRealRollovers(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	s, f := w.hooked()
	x := w.ask(claudeCode)
	first := w.connectAll(s, w.person(accountA), x)
	g := w.grant(s, x, first.AccessToken)
	crashAt := familyName(g.Family, 3)
	crashed := false
	f.set(func(op, name string) error {
		if op == "create" && name == crashAt && !crashed {
			crashed = true
			return errCrash
		}
		return nil
	})
	tok := first
	rotations := 9*oauth.FamilyEpochRotations + 3
	for i := 0; i < rotations; {
		w.clock.add(7 * time.Second) // within RefreshesPerFamilyPerMinute
		next, err := w.refresh(s, x, tok.RefreshToken)
		if err != nil {
			if !crashed || i != 3*oauth.FamilyEpochRotations-1 {
				t.Fatalf("refresh %d: %v", i, err)
			}
			// The rollover wrote its epoch Moved and crashed before the next:
			// the retry, through a cold enclave, makes it.
			s = w.enclave()
			continue
		}
		tok = next
		i++
	}
	f.set(nil)
	if !crashed {
		t.Fatal("the rollover did not crash")
	}
	cur, _, err := w.enclave().readFamily(ctx, g.Family, 0)
	if err != nil || cur == nil || cur.Epoch != 9 {
		t.Fatalf("a cold enclave does not reach the newest epoch: %+v %v", cur, err)
	}
	if _, err := w.check(w.enclave(), x, tok.AccessToken); err != nil {
		t.Fatal(err)
	}
	newest, err := w.refresh(w.enclave(), x, tok.RefreshToken)
	if err != nil {
		t.Fatalf("a cold enclave cannot refresh the newest token: %v", err)
	}
	if _, err := w.refresh(w.enclave(), x, first.RefreshToken); oauthCode(err) != oauth.ErrInvalidGrant {
		t.Fatalf("an old token replayed to a cold enclave did not reach reuse detection: %v", err)
	}
	if _, err := w.check(w.enclave(), x, newest.AccessToken); err == nil {
		t.Fatal("the device stays active after its old token was replayed")
	}
}

// V32: a search's pages at its receipt's bound, delivered only and mixed
// with held messages: one byte below and at the bound the last message is
// on the page; one above, the page is cut before it and the next page
// starts with it; across the pages every message is given or held once
// (finding 9).
func TestV32ReceiptBoundaryBelowAtAndAbove(t *testing.T) {
	ctx := context.Background()
	for _, mixed := range []bool{false, true} {
		for _, delta := range []int{-1, 0, 1} {
			t.Run(fmt.Sprintf("mixed=%t delta=%d", mixed, delta), func(t *testing.T) {
				w := newWorld(t)
				s := w.enclave()
				x := w.ask(claudeCode)
				tok := w.connectAll(s, w.person(accountA), x)
				g := w.grant(s, x, tok.AccessToken)
				r, err := s.prepare(ctx, g, nil)
				if err != nil {
					t.Fatal(err)
				}
				var msgs []mail
				for i := range 31 {
					msgs = append(msgs, mail{ID: fmt.Sprintf("z%03d", i), Mailbox: mailboxID(1), Received: start.Add(-time.Hour), From: "a@example.com",
						Subject: strings.Repeat("s", 8000), Held: mixed && i%3 == 0})
				}
				last := mail{ID: "y000", Mailbox: mailboxID(1), Received: start.Add(-time.Hour), From: "a@example.com"}
				// The receipt's size as the page builds it, newest first.
				rp, err := receipts.NewPage(s.receipt(r, receipts.KindSearch))
				if err != nil {
					t.Fatal(err)
				}
				for i := len(msgs) - 1; i >= 0; i-- {
					o, _ := w.disc.outcome(msgs[i], r.d)
					if fit, err := rp.Add(mailboxID(1), r.d.Parts, o); !fit || err != nil {
						t.Fatal("the fillers do not fit", err)
					}
				}
				o, _ := w.disc.outcome(last, r.d)
				probe, _ := receipts.NewPage(rp.Receipt())
				if fit, _ := probe.Add(mailboxID(1), r.d.Parts, o); !fit {
					t.Fatal("no room left for the last message")
				}
				last.Subject = strings.Repeat("s", receipts.MaxDocumentBytes+delta-probe.Size())
				msgs = append([]mail{last}, msgs...) // oldest first, as the mailbox keeps them
				w.mbx.mu.Lock()
				w.mbx.msgs[mailboxID(1)] = msgs
				w.mbx.msgs[mailboxID(2)] = nil
				w.mbx.mu.Unlock()

				seen := map[string]int{}
				before := w.rcpt.count()
				cursor, pages := "", 0
				for {
					page, rel, err := s.Search(ctx, g, contracts.VendorSearch{Limit: 50, Cursor: cursor})
					if err != nil {
						t.Fatalf("page %d: %v", pages, err)
					}
					rel.Finish(true)
					pages++
					cursor = page.Cursor
					if cursor == "" {
						break
					}
					if pages > 3 {
						t.Fatal("the pages do not end")
					}
				}
				for _, rc := range w.rcpt.all[before:] {
					if n, err := receipts.Size(&rc); err != nil || n > receipts.MaxDocumentBytes {
						t.Fatalf("a receipt past its bound: %d %v", n, err)
					}
					for _, d := range rc.Delivered {
						seen[d.Message]++
					}
					for _, h := range rc.Held {
						seen[h.Message]++
					}
				}
				for _, m := range msgs {
					if seen[m.ID] != 1 {
						t.Fatalf("%s recorded %d times over %d pages", m.ID, seen[m.ID], pages)
					}
				}
				if want := 1 + max(delta, 0); pages != want {
					t.Fatalf("%d pages, want %d", pages, want)
				}
			})
		}
	}
}
