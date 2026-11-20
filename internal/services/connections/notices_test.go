package connections

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/oauth"
	"github.com/permaily-org/permaily/internal/services/notices"
	"github.com/permaily-org/permaily/internal/services/notices/noticestest"
	"github.com/permaily-org/permaily/internal/services/receipts"
	"github.com/permaily-org/permaily/internal/store"
)

// The notices about connections (ADR 0041, pull request 3), built with
// noticestest.Fake: each queued after the change it tells of committed,
// under its event's key, and none when the change did not commit.

// counting is a queue that counts what it is asked, by kind, and passes it
// on.
type counting struct {
	notices.Queue
	mu sync.Mutex
	n  map[notices.Kind]int
}

func (c *counting) Enqueue(ctx context.Context, n notices.Notice) error {
	c.mu.Lock()
	if c.n == nil {
		c.n = map[notices.Kind]int{}
	}
	c.n[n.Kind]++
	c.mu.Unlock()
	return c.Queue.Enqueue(ctx, n)
}

func (c *counting) asked(k notices.Kind) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n[k]
}

// noticed gives the enclaves one fake queue on the world's clock, as the
// image gives every enclave the queue over one storage.
func (w *world) noticed(ss ...*Service) (*noticestest.Fake, *counting) {
	q := &noticestest.Fake{Now: w.clock.now}
	c := &counting{Queue: q}
	for _, s := range ss {
		s.cfg.Notices = c
	}
	return q, c
}

// lastConsent is the newest allowed consent receipt written.
func (w *world) lastConsent() receipts.Receipt {
	w.t.Helper()
	all := w.rcpt.of(receipts.KindConsent)
	for i := len(all) - 1; i >= 0; i-- {
		if all[i].Consent.Allowed {
			return all[i]
		}
	}
	w.t.Fatal("no consent receipt")
	return receipts.Receipt{}
}

func queuedOf(q *noticestest.Fake, k notices.Kind) []notices.Notice {
	var out []notices.Notice
	for _, n := range q.Queued() {
		if n.Kind == k {
			out = append(out, n)
		}
	}
	return out
}

func sameNotice(t *testing.T, got, want notices.Notice) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("queued\n%+v\nwant\n%+v", got, want)
	}
	if err := got.Check(); err != nil {
		t.Fatal(err)
	}
}

// E30. Decide: a connection made (ChangeNew) or replacing the client's
// (ChangeReplace) queues connection_approved, Replaced for the second, and
// a device added (ChangeAddDevice) device_added, each keyed by its consent
// receipt, through either enclave; a denial, a commit refused after it was
// written and consent_changed queue nothing.
func TestE30DecideQueuesTheApproval(t *testing.T) {
	ctx := context.Background()

	t.Run("new, a device added and a replacement, through either enclave", func(t *testing.T) {
		w := newWorld(t)
		a, b := w.enclave(), w.enclave()
		q, _ := w.noticed(a, b)
		p := w.person(accountA)

		req := w.start(a, p, w.ask(claudeCode))
		if req.Snapshot.Change != contracts.ChangeNew {
			t.Fatal(req.Snapshot.Change)
		}
		w.allow(a, p, req)
		c := w.connectionOf(a, accountA, claudeCode)
		sameNotice(t, only(t, q, 1), notices.Notice{
			Account: accountA, Kind: notices.KindConnectionApproved, Key: notices.Key(accountA, notices.KindConnectionApproved, w.lastConsent().ID), At: w.clock.now(),
			Facts: notices.Facts{Vendor: "claude.ai", Resource: contracts.ResourceMCP, Parts: c.Parts, EndsAt: c.EndsAt},
		})

		w.clock.add(10 * time.Minute)
		p = w.person(accountA)
		req = w.start(b, p, w.ask(claudeCode))
		if req.Snapshot.Change != contracts.ChangeAddDevice {
			t.Fatal(req.Snapshot.Change)
		}
		w.allow(b, p, req)
		c = w.connectionOf(b, accountA, claudeCode)
		sameNotice(t, only(t, q, 2), notices.Notice{
			Account: accountA, Kind: notices.KindDeviceAdded, Key: notices.Key(accountA, notices.KindDeviceAdded, w.lastConsent().ID), At: w.clock.now(),
			Facts: notices.Facts{Vendor: "claude.ai", Parts: c.Devices[1].Parts, EndsAt: c.EndsAt},
		})

		w.clock.add(10 * time.Minute)
		p = w.person(accountA)
		x := w.ask(claudeCode)
		x.resource = apiResource
		req = w.start(a, p, x)
		if req.Snapshot.Change != contracts.ChangeReplace {
			t.Fatal(req.Snapshot.Change)
		}
		w.allow(a, p, req)
		c = w.connectionOf(a, accountA, claudeCode)
		sameNotice(t, only(t, q, 3), notices.Notice{
			Account: accountA, Kind: notices.KindConnectionApproved, Key: notices.Key(accountA, notices.KindConnectionApproved, w.lastConsent().ID), At: w.clock.now(),
			Facts: notices.Facts{Vendor: "claude.ai", Resource: contracts.ResourceAPI, Parts: c.Parts, EndsAt: c.EndsAt, Replaced: true},
		})
		// Three consent receipts, three keys.
		keys := map[string]bool{}
		for _, n := range q.Queued() {
			keys[n.Key] = true
		}
		if len(keys) != 3 {
			t.Fatal(keys)
		}
	})

	t.Run("a denial", func(t *testing.T) {
		w := newWorld(t)
		s := w.enclave()
		q, c := w.noticed(s)
		p := w.person(accountA)
		req := w.start(s, p, w.ask(claudeCode))
		if _, err := s.Decide(ctx, p, req.ID, contracts.AuthorizationDecision{Allow: false}); err != nil {
			t.Fatal(err)
		}
		if len(q.Queued()) != 0 || c.asked(notices.KindConnectionApproved) != 0 {
			t.Fatal(q.Queued())
		}
	})

	t.Run("a commit refused after it was written", func(t *testing.T) {
		for _, kind := range []string{"new", "a device added"} {
			t.Run(kind, func(t *testing.T) {
				w := newWorld(t)
				s := w.enclave()
				if kind != "new" {
					w.connect(s, w.person(accountA), w.ask(claudeCode))
				}
				q, c := w.noticed(s)
				p := w.person(accountA)
				req := w.start(s, p, w.ask(claudeCode))
				// The proof ends while the consent receipt is written: what the
				// approval made is ended (finding 5), and nothing is told.
				w.rcpt.mu.Lock()
				w.rcpt.onWrite = func(r *receipts.Receipt) {
					if r.Kind == receipts.KindConsent {
						w.clock.add(p.ConfirmedUntil.Sub(w.clock.now()))
					}
				}
				w.rcpt.mu.Unlock()
				d := req.Draft
				_, err := s.Decide(ctx, p, req.ID, contracts.AuthorizationDecision{Allow: true, Draft: &d, ModelNotice: req.ModelNotice, Snapshot: &req.Snapshot})
				if !errors.Is(err, contracts.ErrConfirmationNeeded) {
					t.Fatal(err)
				}
				if len(q.Queued()) != 0 || c.asked(notices.KindConnectionApproved)+c.asked(notices.KindDeviceAdded) != 0 {
					t.Fatal(q.Queued())
				}
			})
		}
	})

	t.Run("consent_changed", func(t *testing.T) {
		w := newWorld(t)
		a, b := w.enclave(), w.enclave()
		q, _ := w.noticed(a, b)
		p := w.person(accountA)
		// A stale snapshot.
		req := w.start(a, p, w.ask(claudeCode))
		stale := req.Snapshot
		stale.DraftID = "drf_other"
		d := req.Draft
		if _, err := a.Decide(ctx, p, req.ID, contracts.AuthorizationDecision{Allow: true, Draft: &d, ModelNotice: notice, Snapshot: &stale}); !errors.Is(err, contracts.ErrConsentChanged) {
			t.Fatal(err)
		}
		// Two screens that showed no connection: the second approved would
		// replace one it never showed.
		x, y := w.ask(claudeCode), w.ask(claudeCode)
		y.resource = apiResource
		r1, r2 := w.start(a, p, x), w.start(b, p, y)
		w.allow(a, p, r1)
		d = r2.Draft
		if _, err := b.Decide(ctx, p, r2.ID, contracts.AuthorizationDecision{Allow: true, Draft: &d, ModelNotice: notice, Snapshot: &r2.Snapshot}); !errors.Is(err, contracts.ErrConsentChanged) {
			t.Fatal(err)
		}
		if got := q.Queued(); len(got) != 1 || got[0].Key != notices.Key(accountA, notices.KindConnectionApproved, w.lastConsent().ID) {
			t.Fatal(got)
		}
	})
}

// only is the newest of the n notices queued, failing unless there are n.
func only(t *testing.T, q *noticestest.Fake, n int) notices.Notice {
	t.Helper()
	got := q.Queued()
	if len(got) != n {
		t.Fatalf("%d queued, want %d: %+v", len(got), n, got)
	}
	return got[n-1]
}

// E31. Renew queues connection_renewed with the new end, keyed by its
// consent receipt; a renewal whose write loses, fails or whose answer is
// lost queues nothing.
func TestE31RenewQueuesTheRenewal(t *testing.T) {
	t.Run("renewed", func(t *testing.T) {
		r := newRenewal(t)
		q, _ := r.noticed(r.s)
		decided := r.clock.now()
		if _, err := r.renew(r.person(accountA)); err != nil {
			t.Fatal(err)
		}
		var receipt string
		for _, rc := range r.rcpt.of(receipts.KindConsent) {
			if rc.Consent.Renewal {
				receipt = rc.ID
			}
		}
		sameNotice(t, only(t, q, 1), notices.Notice{
			Account: accountA, Kind: notices.KindConnectionRenewed, Key: notices.Key(accountA, notices.KindConnectionRenewed, receipt), At: decided,
			Facts: notices.Facts{Vendor: "claude.ai", EndsAt: endsAt(decided, r.days)},
		})
		if r.observe().end != endsAt(decided, r.days) {
			t.Fatal("the end told is not the end written")
		}
	})

	for _, mode := range []string{"loses", "fails", "answer lost"} {
		t.Run("a write that "+mode, func(t *testing.T) {
			r := newRenewal(t)
			q, c := r.noticed(r.s)
			r.f.set(func(op, name string) error {
				if op != "put" || name != connName(r.id) {
					return nil
				}
				switch mode {
				case "loses":
					return store.ErrChanged
				case "fails":
					return errDown
				}
				return nil
			})
			r.f.setAfter(func(_ context.Context, name string) error {
				if mode == "answer lost" && name == connName(r.id) {
					return context.DeadlineExceeded
				}
				return nil
			})
			if _, err := r.renew(r.person(accountA)); err == nil {
				t.Fatal("renewed")
			}
			r.f.set(nil)
			r.f.setAfter(nil)
			if r.renewals() != 1 {
				t.Fatal("no receipt: the barrier was not reached")
			}
			if len(q.Queued()) != 0 || c.asked(notices.KindConnectionRenewed) != 0 {
				t.Fatal(q.Queued())
			}
		})
	}
}

// E32. The hourly sweep queues connection_ending once per connection and
// end, over two enclaves and many passes, a week before the end of one
// approved or renewed for more than 14 days; a renewal queues one for the
// new end; one of 14 days or fewer, an ended or revoked one, and a closing
// account's queue nothing. EndingDue's table is the seam's
// (TestE32EndingDue in notices).
func TestE32TheSweepQueuesTheEnding(t *testing.T) {
	ctx := context.Background()

	sweep := func(t *testing.T, ss ...*Service) {
		t.Helper()
		for _, s := range ss {
			if err := s.Sweep(ctx); err != nil {
				t.Fatal(err)
			}
		}
	}

	t.Run("once per end, over two enclaves and many passes, then the renewed end", func(t *testing.T) {
		w := newWorld(t)
		a, b := w.enclave(), w.enclave()
		q, c := w.noticed(a, b)
		w.connect(a, w.person(accountA), w.ask(claudeCode))
		d := w.connectionOf(a, accountA, claudeCode)
		if d.Days != oauth.ConnectionDefaultDays {
			t.Fatal(d.Days)
		}
		w.clock.add(d.EndsAt.Sub(w.clock.now()) - notices.EndingBefore - time.Hour)
		sweep(t, a, b)
		if len(queuedOf(q, notices.KindConnectionEnding)) != 0 {
			t.Fatal("told more than a week before the end")
		}
		w.clock.add(2 * time.Hour)
		told := w.clock.now()
		for range 6 {
			sweep(t, a, b, a)
			w.clock.add(time.Hour)
		}
		got := queuedOf(q, notices.KindConnectionEnding)
		if len(got) != 1 {
			t.Fatal(got)
		}
		sameNotice(t, got[0], notices.Notice{
			Account: accountA, Kind: notices.KindConnectionEnding, Key: notices.EndingKey(accountA, d.ID, d.EndsAt), At: told,
			Facts: notices.Facts{Vendor: "claude.ai", EndsAt: d.EndsAt},
		})
		// Each enclave asked once: what it queued it remembers.
		if n := c.asked(notices.KindConnectionEnding); n != 2 {
			t.Fatalf("asked %d times", n)
		}

		// Renewed: the new end is told a week before it, once.
		p := w.person(accountA)
		v, err := a.Get(ctx, p, d.ID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := a.Renew(ctx, p, d.ID, contracts.ConnectionRenewal{Version: v.Version, ModelNotice: notice}); err != nil {
			t.Fatal(err)
		}
		renewed := w.connectionOf(a, accountA, claudeCode)
		sweep(t, a, b)
		if len(queuedOf(q, notices.KindConnectionEnding)) != 1 {
			t.Fatal("the new end told at once")
		}
		w.clock.add(renewed.EndsAt.Sub(w.clock.now()) - notices.EndingBefore + time.Minute)
		for range 3 {
			sweep(t, b, a)
			w.clock.add(time.Hour)
		}
		got = queuedOf(q, notices.KindConnectionEnding)
		if len(got) != 2 || got[1].Key != notices.EndingKey(accountA, d.ID, renewed.EndsAt) || !got[1].Facts.EndsAt.Equal(renewed.EndsAt) {
			t.Fatal(got)
		}
	})

	t.Run("14 days or fewer, 15", func(t *testing.T) {
		for _, days := range []string{"1", "14", "15"} {
			w := newWorld(t)
			s := w.enclave()
			q, _ := w.noticed(s)
			x := w.ask(claudeCode)
			p := w.person(accountA)
			w.allow(s, p, w.pushed(s, p, x, "", map[string][]string{"ends_in_days": {days}}))
			d := w.connectionOf(s, accountA, claudeCode)
			if strconv.Itoa(d.Days) != days {
				t.Fatal(d.Days)
			}
			w.clock.add(d.EndsAt.Sub(w.clock.now()) - time.Hour)
			sweep(t, s)
			if got := len(queuedOf(q, notices.KindConnectionEnding)); (days == "15") != (got == 1) || got > 1 {
				t.Fatalf("%s days: %d told", days, got)
			}
		}
	})

	t.Run("revoked, a device ended, ended at its end, closing", func(t *testing.T) {
		for _, how := range []string{"revoked", "ended at its end", "closing"} {
			t.Run(how, func(t *testing.T) {
				w := newWorld(t)
				s := w.enclave()
				q, _ := w.noticed(s)
				p := w.person(accountA)
				w.connect(s, p, w.ask(claudeCode))
				d := w.connectionOf(s, accountA, claudeCode)
				switch how {
				case "revoked":
					if _, err := s.Revoke(ctx, p, d.ID); err != nil {
						t.Fatal(err)
					}
					w.clock.add(d.EndsAt.Sub(w.clock.now()) - time.Hour)
				case "ended at its end":
					w.clock.add(d.EndsAt.Sub(w.clock.now()) + time.Second)
				case "closing":
					if err := s.EndAll(ctx, accountA); err != nil {
						t.Fatal(err)
					}
					w.clock.add(d.EndsAt.Sub(w.clock.now()) - time.Hour)
				}
				sweep(t, s)
				if got := q.Queued(); len(queuedOf(q, notices.KindConnectionEnding)) != 0 {
					t.Fatal(got)
				}
			})
		}
	})
}

// E33. A device ended because its code or a refresh token of it was used
// again queues device_ended_reuse once per device, keyed by the connection
// and the device, through either enclave and however often it is reused.
func TestE33ADeviceEndedForReuse(t *testing.T) {
	w := newWorld(t)
	a, b := w.enclave(), w.enclave()
	q, c := w.noticed(a, b)

	// The first device: a refresh token used again after its grace, twice.
	x := w.ask(claudeCode)
	tok, _ := w.connect(a, w.person(accountA), x)
	if _, err := w.refresh(a, x, tok.RefreshToken); err != nil {
		t.Fatal(err)
	}
	w.clock.add(oauth.RefreshRetryGrace + time.Second)
	for _, s := range []*Service{b, a} {
		if _, err := w.refresh(s, x, tok.RefreshToken); oauthCode(err) != oauth.ErrInvalidGrant {
			t.Fatal(err)
		}
	}
	d := w.connectionOf(a, accountA, claudeCode)
	first := d.Devices[0]
	if first.EndReason != contracts.EndedByReuse {
		t.Fatalf("%+v", first)
	}
	got := queuedOf(q, notices.KindDeviceEndedReuse)
	if len(got) != 1 {
		t.Fatal(got)
	}
	sameNotice(t, got[0], notices.Notice{
		Account: accountA, Kind: notices.KindDeviceEndedReuse, Key: notices.Key(accountA, notices.KindDeviceEndedReuse, d.ID, first.ID), At: first.EndedAt,
		Facts: notices.Facts{Vendor: "claude.ai"},
	})

	// The second device: its code exchanged again, then a third time.
	w.clock.add(10 * time.Minute)
	y := w.ask(claudeCode)
	p := w.person(accountA)
	req := w.start(b, p, y)
	if req.Snapshot.Change != contracts.ChangeAddDevice {
		t.Fatal(req.Snapshot.Change)
	}
	code := w.allow(b, p, req)
	if _, err := w.exchange(b, y, code); err != nil {
		t.Fatal(err)
	}
	for _, s := range []*Service{a, b} {
		if _, err := w.exchange(s, y, code); oauthCode(err) != oauth.ErrInvalidGrant {
			t.Fatal(err)
		}
	}
	d = w.connectionOf(a, accountA, claudeCode)
	var second *deviceDoc
	for i := range d.Devices {
		if d.Devices[i].ID != first.ID {
			second = &d.Devices[i]
		}
	}
	if second == nil || second.EndReason != contracts.EndedByReuse {
		t.Fatalf("%+v", d.Devices)
	}
	got = queuedOf(q, notices.KindDeviceEndedReuse)
	if len(got) != 2 || got[1].Key != notices.Key(accountA, notices.KindDeviceEndedReuse, d.ID, second.ID) || !got[1].At.Equal(second.EndedAt) {
		t.Fatal(got)
	}
	// Each device's end was written once, so each was asked once.
	if n := c.asked(notices.KindDeviceEndedReuse); n != 2 {
		t.Fatalf("asked %d times", n)
	}
}

// stuck is a queue that cannot be written and does not answer before its
// context ends.
type stuck struct {
	mu    sync.Mutex
	bound []time.Duration
	kinds []notices.Kind
}

func (s *stuck) Enqueue(ctx context.Context, n notices.Notice) error {
	dl, ok := ctx.Deadline()
	s.mu.Lock()
	if ok {
		s.bound = append(s.bound, time.Until(dl))
	} else {
		s.bound = append(s.bound, -1)
	}
	s.kinds = append(s.kinds, n.Kind)
	s.mu.Unlock()
	<-ctx.Done()
	return ctx.Err()
}

// E36. A queue that cannot be written fails none of the changes it would
// tell of and holds none of them for more than EnqueueMax; it is counted
// and logged by its kind alone, and an ending not queued is asked again on
// the next pass.
func TestE36AQueueThatCannotBeWrittenFailsNothing(t *testing.T) {
	ctx := context.Background()
	if s := newWorld(t).enclave(); s.enqueueMax != notices.EnqueueMax {
		t.Fatal(s.enqueueMax)
	}

	// every plays each event through a and b, and fails unless each change
	// is made.
	every := func(t *testing.T, w *world, a, b *Service) {
		t.Helper()
		p := w.person(accountA)
		x := w.ask(claudeCode)
		tok, _ := w.connect(a, p, x)
		d := w.connectionOf(a, accountA, claudeCode)
		w.clock.add(10 * time.Minute)
		y := w.ask(claudeCode)
		w.connect(b, w.person(accountA), y)
		v, err := a.Get(ctx, w.person(accountA), d.ID)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := a.Renew(ctx, w.person(accountA), d.ID, contracts.ConnectionRenewal{Version: v.Version, ModelNotice: notice}); err != nil {
			t.Fatal(err)
		}
		if _, err := w.refresh(a, x, tok.RefreshToken); err != nil {
			t.Fatal(err)
		}
		w.clock.add(oauth.RefreshRetryGrace + time.Second)
		if _, err := w.refresh(b, x, tok.RefreshToken); oauthCode(err) != oauth.ErrInvalidGrant {
			t.Fatal(err)
		}
		if got := w.connectionOf(a, accountA, claudeCode); got.Devices[0].EndReason != contracts.EndedByReuse || got.EndsAt.Equal(d.EndsAt) || len(got.Devices) != 2 {
			t.Fatalf("%+v", got)
		}
		renewed := w.connectionOf(a, accountA, claudeCode)
		w.clock.add(renewed.EndsAt.Sub(w.clock.now()) - time.Hour)
		for _, s := range []*Service{a, b, a} {
			if err := s.Sweep(ctx); err != nil {
				t.Fatal(err)
			}
		}
	}
	kinds := []notices.Kind{notices.KindConnectionApproved, notices.KindDeviceAdded, notices.KindConnectionRenewed, notices.KindDeviceEndedReuse, notices.KindConnectionEnding}

	t.Run("refused", func(t *testing.T) {
		w := newWorld(t)
		a, b := w.enclave(), w.enclave()
		q, c := w.noticed(a, b)
		q.Fail = errors.New("the table cannot be written")
		var buf bytes.Buffer
		var mu sync.Mutex
		log := slog.New(slog.NewJSONHandler(&lockedWriter{w: &buf, mu: &mu}, nil))
		a.cfg.Log, b.cfg.Log = log, log
		every(t, w, a, b)
		for _, k := range kinds {
			if c.asked(k) == 0 {
				t.Errorf("%s never asked", k)
			}
		}
		// The ending, not queued, is asked again on every pass.
		if n := c.asked(notices.KindConnectionEnding); n != 3 {
			t.Fatalf("an ending asked %d times over three passes", n)
		}
		failed := a.Refused()["notice_not_queued"] + b.Refused()["notice_not_queued"]
		total := 0
		for _, k := range kinds {
			total += c.asked(k)
		}
		if failed != int64(total) {
			t.Fatalf("%d counted, %d asked", failed, total)
		}
		mu.Lock()
		out := buf.String()
		mu.Unlock()
		lines := strings.Split(strings.TrimSpace(out), "\n")
		if len(lines) != total {
			t.Fatalf("%d lines for %d notices:\n%s", len(lines), total, out)
		}
		for _, l := range lines {
			if !strings.Contains(l, `"msg":"connections: a notice could not be queued"`) || !strings.Contains(l, `"kind":"`) {
				t.Fatal(l)
			}
			for _, leak := range []string{"acc_", "con_", "fam_", "rcp_", "claude", "cannot be written", "pmc_", "pmr_", "pma_"} {
				if strings.Contains(l, leak) {
					t.Fatalf("%q in %s", leak, l)
				}
			}
		}
	})

	t.Run("held", func(t *testing.T) {
		w := newWorld(t)
		a, b := w.enclave(), w.enclave()
		q := &stuck{}
		for _, s := range []*Service{a, b} {
			s.cfg.Notices = q
			s.enqueueMax = 50 * time.Millisecond
		}
		every(t, w, a, b)
		q.mu.Lock()
		defer q.mu.Unlock()
		for _, k := range kinds {
			if !slices.Contains(q.kinds, k) {
				t.Errorf("%s never asked", k)
			}
		}
		for i, bound := range q.bound {
			if bound <= 0 || bound > 50*time.Millisecond {
				t.Fatalf("%s held under %v", q.kinds[i], bound)
			}
		}
	})
}

type lockedWriter struct {
	w  *bytes.Buffer
	mu *sync.Mutex
}

func (l *lockedWriter) Write(b []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(b)
}

// E32, the cleanup that lost (review of #197, finding 1). A's sweep reads
// a connection ending within the week with a device whose code was never
// exchanged; before A's write of that device's end, B renews or revokes
// the connection. A's write loses, and A queues no ending for the end it
// read: it reads again and decides on what is there.
func TestE32ASweepWhoseCleanupLostQueuesNoStaleEnding(t *testing.T) {
	for _, how := range []string{"renewed", "revoked"} {
		t.Run(how, func(t *testing.T) {
			ctx := context.Background()
			w := newWorld(t)
			f := &failing{Records: &store.Anchored{Records: w.mem, Commits: w.commits, Strict: true, Now: w.clock.now}}
			a, err := New(w.config(f))
			if err != nil {
				t.Fatal(err)
			}
			b := w.enclave()
			q, _ := w.noticed(a, b)
			p := w.person(accountA)
			w.connect(b, p, w.ask(claudeCode))
			// A second device, its code never exchanged.
			w.allow(b, p, w.start(b, w.person(accountA), w.ask(claudeCode)))
			d := w.connectionOf(b, accountA, claudeCode)
			if len(d.Devices) != 2 {
				t.Fatalf("%d devices", len(d.Devices))
			}
			w.clock.add(d.EndsAt.Sub(w.clock.now()) - notices.EndingBefore + time.Hour)

			raced := false
			f.set(func(op, name string) error {
				if op != "put" || name != connName(d.ID) || raced {
					return nil
				}
				raced = true
				p := w.person(accountA)
				switch how {
				case "renewed":
					v, err := b.Get(ctx, p, d.ID)
					if err != nil {
						return err
					}
					if _, err := b.Renew(ctx, p, d.ID, contracts.ConnectionRenewal{Version: v.Version, ModelNotice: notice}); err != nil {
						return err
					}
				case "revoked":
					if _, err := b.Revoke(ctx, p, d.ID); err != nil {
						return err
					}
				}
				return nil
			})
			if err := a.Sweep(ctx); err != nil {
				t.Fatal(err)
			}
			if !raced {
				t.Fatal("the sweep wrote no cleanup")
			}
			if got := queuedOf(q, notices.KindConnectionEnding); len(got) != 0 {
				t.Fatalf("queued an ending after the connection was %s: %+v", how, got)
			}
		})
	}
}
