package accounts

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/services/mailboxes"
	"github.com/permaily-org/permaily/internal/services/mailboxes/mailboxestest"
	"github.com/permaily-org/permaily/internal/store"
)

// The findings of PR #89's code review on what the operator and the admins do
// about mailboxes: each test is the sequence the reviewer described.

var errReadFailed = errors.New("a read failed, as a request to DynamoDB can")

// failSettingsRead makes the read of a mailbox's settings fail once, after
// skip of them went through.
func (w *world) failSettingsRead(id string, skip int32) {
	var seen atomic.Int32
	w.mem.Interrupt = func(_ context.Context, step store.Step, partition, key string) error {
		if step == store.StepRead && partition == inbox.Partition(id) && key == inbox.KeySettings && seen.Add(1) == skip+1 {
			return errReadFailed
		}
		return nil
	}
}

// mailboxEntries is how many entries of the admin log are about mailboxes,
// by kind.
func (w *world) mailboxEntries(boss *adminAccount) map[string]int {
	w.t.Helper()
	log, err := w.as(boss).AdminLog(context.Background(), "", 50)
	if err != nil {
		w.t.Fatal(err)
	}
	out := map[string]int{}
	for _, e := range log.Entries {
		if e.Kind == ActionMailboxPaused || e.Kind == ActionMailboxResumed {
			out[e.Kind]++
		}
	}
	return out
}

// Review 89-2, on the real service: the read of the mailbox's settings fails
// once during a pause, and then during a resume, by the operator and by an
// admin. Whatever the call answers, each transition is counted once, written
// once in the admin log and told once; the retry that finds it done adds
// nothing.
func TestReview89ACommittedPauseIsCountedLoggedAndToldOnce(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	r := w.realBoxes()
	a := r.enclave(w.svc)
	boss := w.account(superAdmin, true)
	carl, _ := w.signUp("carl@example.org")
	box := r.create(a, carl.Account.ID, "Carl's Gmail")
	t.Cleanup(func() { w.mem.Interrupt = nil })

	for _, who := range []struct {
		name   string
		skip   int32 // an admin's change reads the settings once first, for the guard
		pause  func(context.Context, string) (*contracts.MailboxRecord, error)
		resume func(context.Context, string) (*contracts.MailboxRecord, error)
	}{
		{"the operator", 0, w.svc.PauseMailbox, w.svc.ResumeMailbox},
		{"an admin", 1, func(ctx context.Context, id string) (*contracts.MailboxRecord, error) {
			return w.as(boss).PauseMailbox(ctx, id)
		},
			func(ctx context.Context, id string) (*contracts.MailboxRecord, error) {
				return w.as(boss).ResumeMailbox(ctx, id)
			}},
	} {
		for _, step := range []struct {
			kind   string
			notice mailboxes.NoticeKind
			act    func(context.Context, string) (*contracts.MailboxRecord, error)
		}{
			{ActionMailboxPaused, mailboxes.NoticePaused, who.pause},
			{ActionMailboxResumed, mailboxes.NoticeResumed, who.resume},
		} {
			w.now = w.now.Add(time.Minute)
			_, before := w.svc.OperatorActions()
			entries, n := w.mailboxEntries(boss), w.count()
			w.failSettingsRead(box.ID, who.skip)
			_, first := step.act(ctx, box.ID)
			w.mem.Interrupt = nil
			if !errors.Is(first, errReadFailed) {
				t.Fatalf("%s, %s: the read did not fail the call: %v", who.name, step.kind, first)
			}
			w.now = w.now.Add(time.Second)
			if _, err := step.act(ctx, box.ID); err != nil {
				t.Fatalf("%s, %s, again: %v", who.name, step.kind, err)
			}
			_, after := w.svc.OperatorActions()
			if got := after[step.kind] - before[step.kind]; got != 1 {
				t.Fatalf("%s, %s: counted %d times", who.name, step.kind, got)
			}
			if got := w.noticesSince(n); len(got) != 1 || got[0] != step.notice {
				t.Fatalf("%s, %s: told %v", who.name, step.kind, got)
			}
			wantEntries := 0
			if who.skip == 1 {
				wantEntries = 1
			}
			if got := w.mailboxEntries(boss)[step.kind] - entries[step.kind]; got != wantEntries {
				t.Fatalf("%s, %s: %d entries in the admin log", who.name, step.kind, got)
			}
		}
	}
}

// Review 89-4: pausing a paused mailbox, or resuming one that is not paused,
// answers the mailbox as it is and is neither counted on /status nor logged,
// by the operator or by an admin: two pauses are one transition, one notice,
// one count and one entry of the admin log.
func TestReview89ANoOpPauseOrResumeIsNeitherCountedNorLogged(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	f := w.withMailboxes()
	boss := w.account(superAdmin, true)
	me, _ := w.signUp("ann@example.org")
	m := f.Add(me.Account.ID, "Work Gmail")
	before := w.logs.Len()
	for i, act := range []func(context.Context, string) (*contracts.MailboxRecord, error){
		w.svc.PauseMailbox, w.svc.PauseMailbox, w.svc.ResumeMailbox, w.svc.ResumeMailbox,
		w.as(boss).PauseMailbox, w.as(boss).PauseMailbox, w.as(boss).ResumeMailbox, w.as(boss).ResumeMailbox,
	} {
		w.now = w.now.Add(time.Minute)
		rec, err := act(ctx, m.ID)
		if err != nil || rec.ID != m.ID || (rec.State == contracts.MailboxPaused) != (i%4 < 2) {
			t.Fatalf("call %d: %+v %v", i, rec, err)
		}
	}
	_, actions := w.svc.OperatorActions()
	if actions[ActionMailboxPaused] != 2 || actions[ActionMailboxResumed] != 2 {
		t.Fatalf("counted %v", actions)
	}
	if got := w.mailboxEntries(boss); got[ActionMailboxPaused] != 1 || got[ActionMailboxResumed] != 1 {
		t.Fatalf("entries in the admin log %v", got)
	}
	if got := len(f.Notices.Sent()); got != 4 {
		t.Fatalf("%d notices", got)
	}
	logged := w.logs.String()[before:]
	if p, r := strings.Count(logged, "kind="+ActionMailboxPaused), strings.Count(logged, "kind="+ActionMailboxResumed); p != 2 || r != 2 {
		t.Fatalf("logged %d pauses and %d resumes:\n%s", p, r, logged)
	}
}

// beforeChange is a mailboxes service where something happens between the
// guard's read of the owner and the pause or the resume.
type beforeChange struct {
	Mailboxes
	before func()
}

func (b beforeChange) Pause(ctx context.Context, id string) (*contracts.MailboxRecord, bool, error) {
	b.before()
	return b.Mailboxes.Pause(ctx, id)
}

func (b beforeChange) Resume(ctx context.Context, id string) (*contracts.MailboxRecord, bool, error) {
	b.before()
	return b.Mailboxes.Resume(ctx, id)
}

// Review 89-1, the accepted interleaving: an admin who is not the super admin
// asks to pause a mailbox; its owner is read as an ordinary account; the super
// admin makes the owner an admin; then the pause commits. It is kept as it
// is, with no fence: the outcome is the one the allowed order "pause, then
// promotion" gives, so the race reaches no state the rules forbid. The guard
// protects an account while it is an admin: from then on, the admin's next
// change of that mailbox is refused.
func TestReview89APauseAdmittedBeforeItsOwnerIsMadeAnAdminCommits_TheAcceptedInterleaving(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	f := mailboxestest.New()
	f.Now = func() time.Time { return w.now }
	boss := w.account(superAdmin, true)
	ana := w.account("ana@example.org", true)
	if _, err := w.as(boss).MakeAdmin(ctx, ana.me.Account.ID); err != nil {
		t.Fatal(err)
	}
	carl := w.account("carl@example.org", true)
	m := f.Add(carl.me.Account.ID, "Carl's Gmail")
	var promoted atomic.Bool
	w.svc.SetMailboxes(beforeChange{Mailboxes: f, before: func() {
		if promoted.CompareAndSwap(false, true) {
			if _, err := w.as(boss).MakeAdmin(ctx, carl.me.Account.ID); err != nil {
				t.Errorf("made an admin: %v", err)
			}
		}
	}})

	rec, err := w.as(ana).PauseMailbox(ctx, m.ID)
	if err != nil || rec.State != contracts.MailboxPaused {
		t.Fatalf("the pause admitted before the promotion: %+v %v", rec, err)
	}
	if w.role(carl) != contracts.AdminRole {
		t.Fatalf("carl is %q", w.role(carl))
	}
	if got := w.mailboxEntries(boss)[ActionMailboxPaused]; got != 1 {
		t.Fatalf("%d entries", got)
	}
	// From the promotion on, the guard holds.
	if _, err := w.as(ana).ResumeMailbox(ctx, m.ID); !errors.Is(err, contracts.ErrSuperAdminOnly) {
		t.Fatalf("resuming an admin's mailbox: %v", err)
	}
	if _, err := w.as(boss).ResumeMailbox(ctx, m.ID); err != nil {
		t.Fatalf("the super admin resumes it: %v", err)
	}
}

// slowRead holds the first read of an account's record for d, as a slow
// request to DynamoDB would, ending sooner when ctx does.
func (w *world) slowRead(d time.Duration) {
	var once atomic.Bool
	w.mem.Interrupt = func(ctx context.Context, step store.Step, _, key string) error {
		if step != store.StepRead || !strings.HasPrefix(key, "record#account-") || !once.CompareAndSwap(false, true) {
			return nil
		}
		select {
		case <-time.After(d):
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// Review 89-6: a notice about a mailbox has one time budget, for the read of
// the account and the sending together. The read takes most of it and the
// mail system never answers: the notice gives up when the budget is spent,
// not a whole budget later.
func TestReview89AMailboxNoticeHasOneTimeBudget(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	me, _ := w.signUp("ann@example.org")
	const budget = 400 * time.Millisecond
	w.svc.noticeWait = budget
	hung := &stuck{released: make(chan struct{}, 1)}
	w.svc.cfg.Mailer = hung
	w.slowRead(budget * 3 / 4)
	t.Cleanup(func() { w.mem.Interrupt = nil })
	start := time.Now()
	w.svc.MailboxNotice(ctx, me.Account.ID, mailboxes.NoticePaused, w.now)
	took := time.Since(start)
	<-hung.released
	w.svc.Wait()
	if took > budget*3/2 {
		t.Fatalf("a notice with a budget of %s held its caller for %s", budget, took)
	}
	if took < budget*3/4 {
		t.Fatalf("the read was not slow: %s", took)
	}
}
