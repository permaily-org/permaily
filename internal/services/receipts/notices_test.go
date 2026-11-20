package receipts

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/services/notices"
	"github.com/permaily-org/permaily/internal/services/notices/noticestest"
	"github.com/permaily-org/permaily/internal/store"
)

// E34: an incident's entry naming the account, from a walk, an export, an
// append or the audit, queues receipts_incident once a day per account; an
// entry that fails to commit queues nothing (ADR 0041, pull request 4).

// noticed is one enclave's chained service over the world's storage that
// queues incidents' notices in q.
func (w *chainWorld) noticed(q notices.Queue) *Service {
	w.t.Helper()
	s, err := NewChained(w.mem, testKeys{secret: "root"}, w.clock.now, w.anchored(), w.accounts, WithNotices(q))
	if err != nil {
		w.t.Fatal(err)
	}
	s.random = w.random.read
	return s
}

// fake is the fake queue on the world's clock.
func (w *chainWorld) fake() *noticestest.Fake {
	return &noticestest.Fake{Now: w.clock.now}
}

// incidentOf is the notice an incident's entry at at queues for account.
func incidentOf(account string, at time.Time) notices.Notice {
	return notices.Notice{Account: account, Kind: notices.KindReceiptsIncident, Key: notices.IncidentKey(account, at), At: at.UTC()}
}

// sameNotices fails unless got is want, in order, times compared as times.
func sameNotices(t *testing.T, got []notices.Notice, want ...notices.Notice) {
	t.Helper()
	ok := len(got) == len(want)
	for i := 0; ok && i < len(got); i++ {
		g, n := got[i], want[i]
		ok = g.Account == n.Account && g.Kind == n.Kind && g.Key == n.Key && g.At.Equal(n.At) && g.Facts.Vendor == "" && g.Facts.By == "" && len(g.Facts.Parts) == 0
	}
	if !ok {
		t.Fatalf("queued %+v, want %+v", got, want)
	}
}

func TestE34AWalkQueuesTheAccountsNotice(t *testing.T) {
	ctx := context.Background()
	w := newChainWorld(t)
	s := w.enclave()
	ids := w.writes(s, account, 3)
	w.writes(s, other, 2)
	w.deleteReceipt(account, ids[1])
	q := w.fake()
	walkAll(t, w.noticed(q), account, 100)
	walkAll(t, w.noticed(q), other, 100)
	if w.incidents(s) != 1 {
		t.Fatalf("%d incidents", w.incidents(s))
	}
	sameNotices(t, q.Queued(), incidentOf(account, start))
	// What the queue was given is a notice of its kind, as Check wants.
	if err := q.Queued()[0].Check(); err != nil {
		t.Fatal(err)
	}
	// Found again by the same enclave: recorded once, nothing more queued.
	n := w.noticed(q)
	for range 2 {
		if _, err := n.Walk(ctx, account, Position{}, 100); err != nil {
			t.Fatal(err)
		}
	}
	if len(q.Queued()) != 1 {
		t.Fatalf("queued %+v", q.Queued())
	}
}

func TestE34AnExportQueuesTheAccountsNotice(t *testing.T) {
	ctx := context.Background()
	w := newChainWorld(t)
	s := w.enclave()
	ids := w.writes(s, account, 3)
	w.deleteReceipt(account, ids[0])
	q := w.fake()
	e := w.noticed(q)
	for before := (Position{}); ; {
		page, err := e.Export(ctx, account, before)
		if err != nil {
			t.Fatal(err)
		}
		if page.Done {
			break
		}
		before = page.Next
	}
	sameNotices(t, q.Queued(), incidentOf(account, start))
}

func TestE34AnAppendQueuesTheAccountsNotice(t *testing.T) {
	w := newChainWorld(t)
	s := w.enclave()
	w.writes(s, account, 3)
	w.deleteLink(account, 3)
	q := w.fake()
	w.write(w.noticed(q), account)
	if w.incidents(s) != 1 {
		t.Fatalf("%d incidents", w.incidents(s))
	}
	sameNotices(t, q.Queued(), incidentOf(account, start))
}

// The audit's findings name two accounts and the catalogue: each account is
// told, the catalogue's finding names nobody to tell.
func TestE34TheAuditQueuesEachAccountsNotice(t *testing.T) {
	ctx := context.Background()
	w := newChainWorld(t)
	s := w.enclave()
	a := w.writes(s, account, 3)
	w.writes(s, other, 3)
	w.writes(s, third, 2)
	w.deleteReceipt(account, a[0])
	w.deleteLink(third, 1)
	for _, e := range w.mem.AllEntries() {
		if e.Partition == CataloguePartition && strings.HasPrefix(e.Key, CatalogueKeyPrefix+"0000000000000002@") {
			if err := w.mem.DeleteEntry(ctx, e.Partition, e.Key); err != nil {
				t.Fatal(err)
			}
		}
	}
	w.clock.set(day(1))
	q := w.fake()
	p := w.audit(w.noticed(q))
	if p.Audit.Broken["receipt_missing"] != 1 || p.Audit.Broken["link_lost"] != 1 || p.Audit.Broken["catalogue_lost"] != 1 {
		t.Fatalf("%+v", p.Audit.Broken)
	}
	want := []notices.Notice{incidentOf(account, day(1)), incidentOf(third, day(1))}
	slices.SortFunc(want, func(x, y notices.Notice) int { return strings.Compare(x.Account, y.Account) })
	sameNotices(t, q.Queued(), want...)
	for _, f := range w.findings() {
		if f.Kind == "catalogue_lost" && f.Account != "" {
			t.Fatalf("%+v", f)
		}
	}
}

// Once a day per account: what is found later the same UTC day, by this
// enclave or another, is recorded and not told again; the next day it is.
func TestE34OnceADayPerAccount(t *testing.T) {
	w := newChainWorld(t)
	s := w.enclave()
	ids := w.writes(s, account, 6)
	q := w.fake()
	w.deleteReceipt(account, ids[0])
	walkAll(t, w.noticed(q), account, 100)
	later := time.Date(start.Year(), start.Month(), start.Day(), 23, 59, 0, 0, time.UTC)
	w.clock.set(later)
	w.deleteReceipt(account, ids[1])
	walkAll(t, w.noticed(q), account, 100)
	if w.incidents(s) != 2 {
		t.Fatalf("%d incidents", w.incidents(s))
	}
	sameNotices(t, q.Queued(), incidentOf(account, start))
	next := later.Add(2 * time.Minute)
	w.clock.set(next)
	w.deleteReceipt(account, ids[2])
	walkAll(t, w.noticed(q), account, 100)
	if w.incidents(s) != 3 {
		t.Fatalf("%d incidents", w.incidents(s))
	}
	sameNotices(t, q.Queued(), incidentOf(account, start), incidentOf(account, next))
}

// failingCommits refuses the commits fail names, and calls after, when
// set, with the name of each commit made.
type failingCommits struct {
	*store.MemoryCommits
	fail  func(name string) bool
	after func(name string)
}

func (f *failingCommits) Append(ctx context.Context, c store.Commit) (*store.Commit, error) {
	if f.fail(c.Name) {
		return nil, errBoom
	}
	got, err := f.MemoryCommits.Append(ctx, c)
	if err == nil && f.after != nil {
		f.after(c.Name)
	}
	return got, err
}

// isIncident says whether name is an entry's of the record of incidents.
func isIncident(name string) bool {
	return strings.HasPrefix(name, strings.TrimSuffix(IncidentName(1), "1"))
}

// An entry whose value is not stored, or whose commit is refused, queues
// nothing and fails what found it; found again once it can commit, it is
// recorded and told once.
func TestE34AnEntryThatFailsToCommitQueuesNothing(t *testing.T) {
	ctx := context.Background()
	for _, how := range []string{"value", "commit"} {
		t.Run(how, func(t *testing.T) {
			w := newChainWorld(t)
			s := w.enclave()
			ids := w.writes(s, account, 3)
			w.deleteReceipt(account, ids[1])
			var refusing sync.Mutex
			refuse := true
			refused := func() bool {
				refusing.Lock()
				defer refusing.Unlock()
				return refuse
			}
			switch how {
			case "value":
				w.mem.Interrupt = func(_ context.Context, step store.Step, partition, _ string) error {
					if step == store.StepEntry && partition == IncidentPartition && refused() {
						return errBoom
					}
					return nil
				}
			case "commit":
				w.commits = &failingCommits{MemoryCommits: w.mc, fail: func(name string) bool {
					return isIncident(name) && refused()
				}}
			}
			q := w.fake()
			n := w.noticed(q)
			if _, err := n.Walk(ctx, account, Position{}, 100); err == nil {
				t.Fatal("a walk whose incident was not recorded passed")
			}
			if got := w.incidents(s); got != 0 || len(q.Queued()) != 0 {
				t.Fatalf("%d incidents, queued %+v", got, q.Queued())
			}
			refusing.Lock()
			refuse = false
			refusing.Unlock()
			walkAll(t, n, account, 100)
			if got := w.incidents(s); got != 1 {
				t.Fatalf("%d incidents", got)
			}
			sameNotices(t, q.Queued(), incidentOf(account, start))
		})
	}
}

// recordingQueue is a queue that answers err and keeps what it was called
// with: the notice, its context's deadline and error, and whether the record
// of incidents' lock was free.
type recordingQueue struct {
	s        *Service
	err      error
	block    bool
	notices  []notices.Notice
	deadline []time.Duration
	ctxErr   []error
	unlocked []bool
}

func (r *recordingQueue) Enqueue(ctx context.Context, n notices.Notice) error {
	d, ok := ctx.Deadline()
	if !ok {
		d = time.Time{}
	}
	r.deadline = append(r.deadline, time.Until(d))
	r.ctxErr = append(r.ctxErr, ctx.Err())
	free := r.s.chain.incidents.write.TryLock()
	if free {
		r.s.chain.incidents.write.Unlock()
	}
	r.unlocked = append(r.unlocked, free)
	r.notices = append(r.notices, n)
	if r.block {
		<-ctx.Done()
		return ctx.Err()
	}
	return r.err
}

// A queue that fails, or holds its caller, fails nothing of the record nor
// of what found it: the walk answers, the incident is recorded, and the
// queue is called outside the record's lock, with EnqueueMax and not the
// caller's context.
func TestE34AQueueThatFailsFailsNothing(t *testing.T) {
	for _, how := range []string{"refused", "invalid", "held"} {
		t.Run(how, func(t *testing.T) {
			w := newChainWorld(t)
			s := w.enclave()
			ids := w.writes(s, account, 3)
			w.deleteReceipt(account, ids[1])
			r := &recordingQueue{}
			switch how {
			case "refused":
				r.err = errBoom
			case "invalid":
				r.err = notices.ErrInvalid
			case "held":
				r.block = true
			}
			n := w.noticed(r)
			r.s = n
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			began := time.Now()
			page, err := n.Walk(ctx, account, Position{}, 100)
			if err != nil || len(page.Links) == 0 {
				t.Fatalf("%+v %v", page, err)
			}
			if how == "held" && time.Since(began) < notices.EnqueueMax/2 {
				t.Fatal("the queue was not given its time")
			}
			if w.incidents(s) != 1 || n.Broken()["receipt_missing"] != 0 {
				t.Fatalf("%d incidents, broken %+v", w.incidents(s), n.Broken())
			}
			if len(r.notices) != 1 || r.notices[0].Key != notices.IncidentKey(account, start) {
				t.Fatalf("%+v", r.notices)
			}
			if !r.unlocked[0] || r.ctxErr[0] != nil || r.deadline[0] <= 0 || r.deadline[0] > notices.EnqueueMax {
				t.Fatalf("unlocked %v, context %v, deadline %v", r.unlocked, r.ctxErr, r.deadline)
			}
		})
	}
	// A caller whose context ends right after the commit still queues.
	w := newChainWorld(t)
	s := w.enclave()
	ids := w.writes(s, account, 3)
	w.deleteReceipt(account, ids[1])
	ctx, cancel := context.WithCancel(context.Background())
	w.commits = &failingCommits{MemoryCommits: w.mc, fail: func(string) bool { return false }, after: func(name string) {
		if isIncident(name) {
			cancel()
		}
	}}
	r := &recordingQueue{}
	n := w.noticed(r)
	r.s = n
	_, _ = n.Walk(ctx, account, Position{}, 100)
	if w.incidents(s) != 1 || len(r.notices) != 1 || r.ctxErr[0] != nil {
		t.Fatalf("%d incidents, queued %+v with %v", w.incidents(s), r.notices, r.ctxErr)
	}
}

// Without WithNotices, nothing is queued and nothing changes.
func TestE34WithoutNotices(t *testing.T) {
	w := newChainWorld(t)
	s := w.enclave()
	ids := w.writes(s, account, 3)
	w.deleteReceipt(account, ids[1])
	walkAll(t, w.enclave(), account, 100)
	if w.incidents(s) != 1 || s.chain.notices != nil {
		t.Fatal("an incident without a queue")
	}
}
