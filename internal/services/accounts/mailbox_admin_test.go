package accounts

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/services/mailboxes"
	"github.com/permaily-org/permaily/internal/services/mailboxes/mailboxestest"
)

// What the operator and the admins see and do about mailboxes (ADR 0026,
// "What the operator and admins see and do"), case O2 of its fourth pull
// request: against mailboxestest.Fake, which keeps the policy of the real
// service. mailboxes_real_test.go runs them again on the real service.

// withMailboxes gives the world's service the fake mailboxes, as the
// composition gives it the real ones.
func (w *world) withMailboxes() *mailboxestest.Fake {
	w.t.Helper()
	f := mailboxestest.New()
	f.Now = func() time.Time { return w.now }
	w.svc.SetMailboxes(f)
	return f
}

// fakeAddress is the address the fake gives a mailbox, which only the
// person's own routes ever show.
func fakeAddress(f *mailboxestest.Fake, account, id string) string {
	list, err := f.List(context.Background(), mailboxes.Caller{Account: account})
	if err != nil {
		panic(err)
	}
	for _, m := range list.Mailboxes {
		if m.ID == id {
			return m.Address
		}
	}
	return ""
}

// noAddressOrLabel fails when anything an admin is answered, or the admin log
// keeps, holds a mailbox's address or its label.
func noAddressOrLabel(t *testing.T, what string, v any, secrets ...string) {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range secrets {
		if s != "" && strings.Contains(strings.ToLower(string(raw)), strings.ToLower(s)) {
			t.Fatalf("%s holds %q: %s", what, s, raw)
		}
	}
	if strings.Contains(string(raw), "@inbox.") {
		t.Fatalf("%s holds a mailbox's address: %s", what, raw)
	}
}

// O2: the operator sees an account's mailboxes as records and finds one by an
// address it knows; it pauses and resumes one, each counted and logged by its
// kind alone. Nothing it is answered holds an address or a label.
func TestTheOperatorPausesAndResumesAMailbox(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	f := w.withMailboxes()
	me, _ := w.signUp("ann@example.org")
	acc := me.Account.ID
	m := f.Add(acc, "Work Gmail")
	address := fakeAddress(f, acc, m.ID)

	list, err := w.svc.MailboxRecords(ctx, acc)
	if err != nil || len(list.Mailboxes) != 1 || list.Mailboxes[0].ID != m.ID || list.Mailboxes[0].Account != acc || list.Mailboxes[0].State != contracts.MailboxWaiting {
		t.Fatalf("records: %+v %v", list, err)
	}
	noAddressOrLabel(t, "the records", list, address, "Work Gmail")

	found, err := w.svc.MailboxRecordByAddress(ctx, strings.ToUpper(address))
	if err != nil || len(found.Mailboxes) != 1 || found.Mailboxes[0].ID != m.ID {
		t.Fatalf("by address: %+v %v", found, err)
	}
	noAddressOrLabel(t, "the record found", found, address, "Work Gmail")

	before := w.logs.Len()
	paused, err := w.svc.PauseMailbox(ctx, m.ID)
	if err != nil || paused.State != contracts.MailboxPaused || paused.PausedAt.IsZero() {
		t.Fatalf("pause: %+v %v", paused, err)
	}
	noAddressOrLabel(t, "the paused record", paused, address, "Work Gmail")
	// Pausing one that is paused changes nothing, and is counted: the
	// enclave did it, as blocking a blocked account is counted.
	if again, err := w.svc.PauseMailbox(ctx, m.ID); err != nil || again.State != contracts.MailboxPaused {
		t.Fatalf("pause again: %+v %v", again, err)
	}
	resumed, err := w.svc.ResumeMailbox(ctx, m.ID)
	if err != nil || resumed.State != contracts.MailboxWaiting || !resumed.PausedAt.IsZero() {
		t.Fatalf("resume: %+v %v", resumed, err)
	}
	_, actions := w.svc.OperatorActions()
	if actions[ActionMailboxPaused] != 2 || actions[ActionMailboxResumed] != 1 {
		t.Fatalf("counted %v", actions)
	}
	// The person is told, once a change, by the mailboxes service (which
	// accounts tells by mail: notices_test.go).
	sent := f.Notices.Sent()
	if len(sent) != 2 || sent[0].Kind != mailboxes.NoticePaused || sent[1].Kind != mailboxes.NoticeResumed || sent[0].Account != acc {
		t.Fatalf("notices: %+v", sent)
	}
	// Logged by kind alone: no identifier, address or label.
	logged := w.logs.String()[before:]
	if !strings.Contains(logged, "kind="+ActionMailboxPaused) || !strings.Contains(logged, "kind="+ActionMailboxResumed) {
		t.Fatalf("not logged by kind:\n%s", logged)
	}
	for _, s := range []string{m.ID, acc, address, "Work Gmail"} {
		if strings.Contains(logged, s) {
			t.Fatalf("the log holds %q:\n%s", s, logged)
		}
	}
	// The operator is not written in the admin log.
	log, err := w.svc.AdminLog(ctx, "", 50)
	if err != nil || len(log.Entries) != 0 {
		t.Fatalf("admin log: %+v %v", log, err)
	}
	// The account says how many mailboxes it has, and the counts the total.
	rec, err := w.svc.AccountRecord(ctx, acc)
	if err != nil || rec.Mailboxes != 1 {
		t.Fatalf("account record: %+v %v", rec, err)
	}
	f.Add(acc, "")
	stats, err := w.svc.Stats(ctx)
	if err != nil || stats.Mailboxes.Total != 2 {
		t.Fatalf("stats: %+v %v", stats, err)
	}
}

// O2: an identifier that is not an account is not_found before the
// mailboxes service is asked; one that is an account with no mailboxes is an
// empty list; an identifier that is not a mailbox is not_found, as one that is
// leaving.
func TestWhatTheOperatorAsksOfMailboxesThatAreNotThere(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	f := w.withMailboxes()
	me, _ := w.signUp("ann@example.org")
	for _, id := range []string{"", "acc_", "nobody", "acc_aaaaaaaaaaaaaaaaaaaaaaaaaa", me.Account.ID + "x"} {
		if _, err := w.svc.MailboxRecords(ctx, id); !errors.Is(err, contracts.ErrNotFound) {
			t.Errorf("records of %q: %v", id, err)
		}
	}
	if list, err := w.svc.MailboxRecords(ctx, me.Account.ID); err != nil || list.Mailboxes == nil || len(list.Mailboxes) != 0 {
		t.Fatalf("an account with none: %+v %v", list, err)
	}
	m := f.Add(me.Account.ID, "")
	if err := f.Disconnect(ctx, mailboxes.Caller{Account: me.Account.ID, ConfirmedUntil: w.now.Add(time.Minute)}, m.ID); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"", "mbx_", "acc_aaaaaaaaaaaaaaaaaaaaaaaaaa", "mbx_" + strings.Repeat("z", 26), m.ID, m.ID + "/../x"} {
		if _, err := w.svc.PauseMailbox(ctx, id); !errors.Is(err, contracts.ErrNotFound) {
			t.Errorf("pause %q: %v", id, err)
		}
		if _, err := w.svc.ResumeMailbox(ctx, id); !errors.Is(err, contracts.ErrNotFound) {
			t.Errorf("resume %q: %v", id, err)
		}
	}
	_, actions := w.svc.OperatorActions()
	if actions[ActionMailboxPaused] != 0 || actions[ActionMailboxResumed] != 0 {
		t.Fatalf("a refusal was counted: %v", actions)
	}
}

// O2: finding a mailbox by an address answers the same empty list for every
// address that leads to no mailbox, whatever it is, and an error only for a
// request that names no address at all; it is one exact address a request.
func TestFindingAMailboxByAnAddressTellsNothingElse(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	f := w.withMailboxes()
	me, _ := w.signUp("ann@example.org")
	m := f.Add(me.Account.ID, "")
	address := fakeAddress(f, me.Account.ID, m.ID)
	gone := f.Add(me.Account.ID, "")
	goneAddress := fakeAddress(f, me.Account.ID, gone.ID)
	if err := f.Disconnect(ctx, mailboxes.Caller{Account: me.Account.ID, ConfirmedUntil: w.now.Add(time.Minute)}, gone.ID); err != nil {
		t.Fatal(err)
	}
	empty, _ := json.Marshal(contracts.MailboxRecordList{Mailboxes: []contracts.MailboxRecord{}})
	for _, a := range []string{
		"never@inbox.permaily.test", goneAddress, "ann@example.org", "not an address", strings.Repeat("a", 5000) + "@inbox.permaily.test",
		address + "," + goneAddress, "*@inbox.permaily.test", address[:len(address)-1],
	} {
		got, err := w.svc.MailboxRecordByAddress(ctx, a)
		raw, _ := json.Marshal(got)
		if err != nil || string(raw) != string(empty) {
			t.Errorf("%.40q: %s %v", a, raw, err)
		}
	}
	if _, err := w.svc.MailboxRecordByAddress(ctx, "  "); !errors.Is(err, contracts.ErrInvalidAddress) {
		t.Fatalf("no address: %v", err)
	}
}

// O2: an admin looks without a proof and writes nothing in the log; pausing
// and resuming need the fresh proof, are counted, and are written in the admin
// log with the account and the mailbox, by identifier alone.
func TestAnAdminPausesAndResumesAMailbox(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	f := w.withMailboxes()
	boss := w.account(superAdmin, true)
	ana := w.account("ana@example.org", true)
	if _, err := w.as(boss).MakeAdmin(ctx, ana.me.Account.ID); err != nil {
		t.Fatal(err)
	}
	carl, _ := w.signUp("carl@example.org")
	m := f.Add(carl.Account.ID, "Personal")
	address := fakeAddress(f, carl.Account.ID, m.ID)
	_, before := w.svc.OperatorActions()

	// Looking, with a session whose proof is not fresh.
	w.now = w.now.Add(time.Hour)
	stale, err := w.svc.Admin(ctx, w.who(ana.cookie))
	if err != nil {
		t.Fatal(err)
	}
	if list, err := stale.MailboxRecords(ctx, carl.Account.ID); err != nil || len(list.Mailboxes) != 1 {
		t.Fatalf("looking: %+v %v", list, err)
	}
	if list, err := stale.MailboxRecordByAddress(ctx, address); err != nil || len(list.Mailboxes) != 1 {
		t.Fatalf("finding: %+v %v", list, err)
	}
	if _, err := stale.PauseMailbox(ctx, m.ID); !errors.Is(err, contracts.ErrConfirmationNeeded) {
		t.Fatalf("pause without a fresh proof: %v", err)
	}
	if _, err := stale.ResumeMailbox(ctx, m.ID); !errors.Is(err, contracts.ErrConfirmationNeeded) {
		t.Fatalf("resume without a fresh proof: %v", err)
	}
	if rec, _ := f.Record(ctx, m.ID); !rec.PausedAt.IsZero() {
		t.Fatal("paused without a proof")
	}

	admin := w.as(ana)
	if rec, err := admin.PauseMailbox(ctx, m.ID); err != nil || rec.State != contracts.MailboxPaused {
		t.Fatalf("pause: %+v %v", rec, err)
	}
	w.now = w.now.Add(time.Second) // the log's newest first
	if rec, err := admin.ResumeMailbox(ctx, m.ID); err != nil || rec.State != contracts.MailboxWaiting {
		t.Fatalf("resume: %+v %v", rec, err)
	}
	_, after := w.svc.OperatorActions()
	if after[ActionMailboxPaused]-before[ActionMailboxPaused] != 1 || after[ActionMailboxResumed]-before[ActionMailboxResumed] != 1 {
		t.Fatalf("counted %v, then %v", before, after)
	}
	log, err := admin.AdminLog(ctx, "", 50)
	if err != nil || len(log.Entries) < 2 {
		t.Fatalf("log: %+v %v", log, err)
	}
	for i, kind := range []string{ActionMailboxResumed, ActionMailboxPaused} {
		e := log.Entries[i]
		if e.Kind != kind || e.Admin.ID != ana.me.Account.ID || e.Account == nil || e.Account.ID != carl.Account.ID || e.Mailbox != m.ID {
			t.Fatalf("entry %d: %+v", i, e)
		}
	}
	noAddressOrLabel(t, "the admin log", log, address, "Personal")
}

// O2, the guard: a mailbox of an admin's account is acted on by the super
// admin alone, and no other admin acts on the super admin's; each is told so
// before being asked to confirm, and nothing changes. The super admin acts on
// any, its own included.
func TestOnlyTheSuperAdminActsOnAnAdminsMailbox(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	f := w.withMailboxes()
	boss := w.account(superAdmin, true)
	ana := w.account("ana@example.org", true)
	bea := w.account("bea@example.org", true)
	for _, a := range []*adminAccount{ana, bea} {
		if _, err := w.as(boss).MakeAdmin(ctx, a.me.Account.ID); err != nil {
			t.Fatal(err)
		}
	}
	bossBox := f.Add(boss.me.Account.ID, "")
	anaBox := f.Add(ana.me.Account.ID, "")
	beaBox := f.Add(bea.me.Account.ID, "")

	w.now = w.now.Add(time.Hour) // no proof is fresh any more
	stale, err := w.svc.Admin(ctx, w.who(ana.cookie))
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{bossBox.ID, anaBox.ID, beaBox.ID} {
		for name, act := range map[string]func(context.Context, string) (*contracts.MailboxRecord, error){"pause": stale.PauseMailbox, "resume": stale.ResumeMailbox} {
			// Told before being asked to confirm: confirming would be for nothing.
			if _, err := act(ctx, id); !errors.Is(err, contracts.ErrSuperAdminOnly) {
				t.Errorf("%s %s without a proof: %v", name, id, err)
			}
		}
		if _, err := w.as(ana).PauseMailbox(ctx, id); !errors.Is(err, contracts.ErrSuperAdminOnly) {
			t.Errorf("an admin paused an admin's mailbox: %v", err)
		}
		if rec, _ := f.Record(ctx, id); !rec.PausedAt.IsZero() {
			t.Fatalf("%s was paused", id)
		}
	}
	for _, id := range []string{bossBox.ID, anaBox.ID, beaBox.ID} {
		if rec, err := w.as(boss).PauseMailbox(ctx, id); err != nil || rec.State != contracts.MailboxPaused {
			t.Fatalf("the super admin pauses %s: %+v %v", id, rec, err)
		}
	}
	// What was refused is not counted, nor written.
	_, actions := w.svc.OperatorActions()
	if actions[ActionMailboxPaused] != 3 {
		t.Fatalf("counted %v", actions)
	}
	log, err := w.as(boss).AdminLog(ctx, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range log.Entries {
		if e.Kind == ActionMailboxPaused {
			n++
			if e.Admin.ID != boss.me.Account.ID {
				t.Fatalf("an entry by %s", e.Admin.ID)
			}
		}
	}
	if n != 3 {
		t.Fatalf("%d entries of pauses", n)
	}
}

// O2: past ten pauses and resumes of one mailbox in thirty days, the eleventh
// is too_many_requests, for the operator and an admin alike, and is neither
// counted nor written.
func TestPausingAMailboxIsLimited(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	f := w.withMailboxes()
	boss := w.account(superAdmin, true)
	carl, _ := w.signUp("carl@example.org")
	m := f.Add(carl.Account.ID, "")
	for i := range mailboxes.MaxStateChangesPerPeriod {
		act := w.svc.PauseMailbox
		if i%2 == 1 {
			act = w.svc.ResumeMailbox
		}
		if _, err := act(ctx, m.ID); err != nil {
			t.Fatalf("change %d: %v", i, err)
		}
	}
	_, before := w.svc.OperatorActions()
	if _, err := w.svc.PauseMailbox(ctx, m.ID); !errors.Is(err, contracts.ErrTooManyRequests) {
		t.Fatalf("the operator's eleventh: %v", err)
	}
	if _, err := w.as(boss).PauseMailbox(ctx, m.ID); !errors.Is(err, contracts.ErrTooManyRequests) {
		t.Fatalf("an admin's eleventh: %v", err)
	}
	_, after := w.svc.OperatorActions()
	if after[ActionMailboxPaused] != before[ActionMailboxPaused] {
		t.Fatalf("a refusal was counted: %v, then %v", before, after)
	}
	log, _ := w.as(boss).AdminLog(ctx, "", 100)
	for _, e := range log.Entries {
		if e.Mailbox != "" {
			t.Fatalf("a refusal was written: %+v", e)
		}
	}
}

// Without mailboxes (an image whose accounts have none yet), an account has
// none, the counts say none, and no mailbox is there to act on.
func TestAccountsWithoutMailboxes(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	me, _ := w.signUp("ann@example.org")
	if rec, err := w.svc.AccountRecord(ctx, me.Account.ID); err != nil || rec.Mailboxes != 0 {
		t.Fatalf("record: %+v %v", rec, err)
	}
	if list, err := w.svc.MailboxRecords(ctx, me.Account.ID); err != nil || list.Mailboxes == nil || len(list.Mailboxes) != 0 {
		t.Fatalf("records: %+v %v", list, err)
	}
	if list, err := w.svc.MailboxRecordByAddress(ctx, "x@inbox.permaily.test"); err != nil || len(list.Mailboxes) != 0 {
		t.Fatalf("by address: %+v %v", list, err)
	}
	if _, err := w.svc.PauseMailbox(ctx, "mbx_aaaaaaaaaaaaaaaaaaaaaaaaaa"); !errors.Is(err, contracts.ErrNotFound) {
		t.Fatalf("pause: %v", err)
	}
	if stats, err := w.svc.Stats(ctx); err != nil || stats.Mailboxes.Total != 0 {
		t.Fatalf("stats: %+v %v", stats, err)
	}
}

// brokenMailboxes is a mailboxes service whose every call fails, with an
// error that names an address, an account and a mailbox, as a storage error
// might.
type brokenMailboxes struct{ *mailboxestest.Fake }

var errMailboxesBroken = errors.New("mailboxes down: secret@inbox.permaily.test mbx_aaaaaaaaaaaaaaaaaaaaaaaaaa")

func (brokenMailboxes) Count(context.Context, string) (int, error) { return 0, errMailboxesBroken }
func (brokenMailboxes) Total(context.Context) (int, error)         { return 0, errMailboxesBroken }
func (brokenMailboxes) Records(context.Context, string) (*contracts.MailboxRecordList, error) {
	return nil, errMailboxesBroken
}
func (brokenMailboxes) ByAddress(context.Context, string) (*contracts.MailboxRecordList, error) {
	return nil, errMailboxesBroken
}
func (brokenMailboxes) Record(context.Context, string) (*contracts.MailboxRecord, error) {
	return nil, errMailboxesBroken
}
func (brokenMailboxes) Pause(context.Context, string) (*contracts.MailboxRecord, error) {
	return nil, errMailboxesBroken
}

// When the mailboxes service fails, what was asked fails with its error
// (which the adapter logs, redacted) and nothing is counted or written; the
// counts say they are not complete, and their failure is logged through the
// one redactor.
func TestWhenTheMailboxesServiceFails(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	w.svc.SetMailboxes(brokenMailboxes{mailboxestest.New()})
	boss := w.account(superAdmin, true)
	me, _ := w.signUp("ann@example.org")
	before := w.logs.Len()
	if _, err := w.svc.AccountRecord(ctx, me.Account.ID); !errors.Is(err, errMailboxesBroken) {
		t.Fatalf("record: %v", err)
	}
	if _, err := w.svc.MailboxRecords(ctx, me.Account.ID); !errors.Is(err, errMailboxesBroken) {
		t.Fatalf("records: %v", err)
	}
	if _, err := w.svc.MailboxRecordByAddress(ctx, "x@inbox.permaily.test"); !errors.Is(err, errMailboxesBroken) {
		t.Fatalf("by address: %v", err)
	}
	if _, err := w.svc.PauseMailbox(ctx, "mbx_aaaaaaaaaaaaaaaaaaaaaaaaaa"); !errors.Is(err, errMailboxesBroken) {
		t.Fatalf("pause: %v", err)
	}
	if _, err := w.as(boss).PauseMailbox(ctx, "mbx_aaaaaaaaaaaaaaaaaaaaaaaaaa"); !errors.Is(err, errMailboxesBroken) {
		t.Fatalf("an admin's pause: %v", err)
	}
	stats, err := w.svc.Stats(ctx)
	if err != nil || stats.Complete || stats.Mailboxes.Total != 0 {
		t.Fatalf("stats: %+v %v", stats, err)
	}
	logged := w.logs.String()[before:]
	if strings.Contains(logged, "inbox.permaily") || strings.Contains(logged, "secret") || strings.Contains(logged, "mbx_a") {
		t.Fatalf("logged what the error says:\n%s", logged)
	}
	if !strings.Contains(logged, "mailboxes could not be counted") || !strings.Contains(logged, "mbx_…") {
		t.Fatalf("the counts' failure is not logged:\n%s", logged)
	}
	_, actions := w.svc.OperatorActions()
	if actions[ActionMailboxPaused] != 0 {
		t.Fatalf("counted %v", actions)
	}
}
