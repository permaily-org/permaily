package mailboxestest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/services/mailboxes"
)

// Service is httpapi.MailboxesService and accounts.Mailboxes together, as
// one implementation gives both (the fake here, *mailboxes.Service from pull
// request 1). It is spelt out rather than imported: httpapi's own tests
// import this package. fake_test.go checks that it matches both.
type Service interface {
	List(ctx context.Context, c mailboxes.Caller) (*contracts.MailboxList, error)
	Create(ctx context.Context, c mailboxes.Caller, in contracts.MailboxCreate) (*contracts.Mailbox, error)
	Get(ctx context.Context, c mailboxes.Caller, id string) (*contracts.Mailbox, error)
	Update(ctx context.Context, c mailboxes.Caller, id string, in contracts.MailboxUpdate) (*contracts.Mailbox, error)
	Disconnect(ctx context.Context, c mailboxes.Caller, id string) error
	Open(ctx context.Context, c mailboxes.Caller, id string, a mailboxes.Access) (inbox.Box, error)
	RemoveAccount(ctx context.Context, accountID string) error
	Count(ctx context.Context, accountID string) (int, error)
	Total(ctx context.Context) (int, error)
	Records(ctx context.Context, accountID string) (*contracts.MailboxRecordList, error)
	ByAddress(ctx context.Context, address string) (*contracts.MailboxRecordList, error)
	Record(ctx context.Context, id string) (*contracts.MailboxRecord, error)
	Pause(ctx context.Context, id string) (*contracts.MailboxRecord, error)
	Resume(ctx context.Context, id string) (*contracts.MailboxRecord, error)
}

// Subject is one implementation under the contract, fresh for each case,
// with what the contract needs to drive it. The real service of pull request
// 1 is made over store.Memory with its own Anchored, given Accounts and
// Notices from here; MakeLost deletes the newest value of the mailbox's
// record, and MakeForked adds a second commit at one of its versions
// (store.MemoryCommits, pull request 0).
type Subject struct {
	Service  Service
	Accounts *Accounts
	Notices  *Notices
	// SetNow sets the clock the service reads.
	SetNow     func(time.Time)
	MakeLost   func(t *testing.T, id string)
	MakeForked func(t *testing.T, id string)
}

// FakeSubject is Contract's Subject for Fake.
func FakeSubject(*testing.T) *Subject {
	f := New()
	var now time.Time
	f.Now = func() time.Time { return now }
	return &Subject{
		Service: f, Accounts: f.Accounts, Notices: f.Notices,
		SetNow:     func(t time.Time) { now = t },
		MakeLost:   func(_ *testing.T, id string) { f.MakeLost(id) },
		MakeForked: func(_ *testing.T, id string) { f.MakeForked(id) },
	}
}

// contractStart is when every case starts.
var contractStart = time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)

// Contract runs the cases of ADR 0026's seams that every implementation of
// Service must pass, numbered C1 to C16 as the ADR lists them ("The fakes,
// and the contract they share"). Each case gets a new Subject.
func Contract(t *testing.T, newSubject func(t *testing.T) *Subject) {
	for _, c := range contractCases {
		t.Run(c.name, func(t *testing.T) {
			s := newSubject(t)
			s.SetNow(contractStart)
			c.run(t, s)
		})
	}
}

type contractCase struct {
	name string
	run  func(t *testing.T, s *Subject)
}

var ctx = context.Background()

// caller is an account's session, its proof fresh for an hour from start.
func caller(account string) mailboxes.Caller {
	return mailboxes.Caller{Account: account, ConfirmedUntil: contractStart.Add(time.Hour)}
}

func create(t *testing.T, s *Subject, c mailboxes.Caller) *contracts.Mailbox {
	t.Helper()
	m, err := s.Service.Create(ctx, c, contracts.MailboxCreate{Label: "Work"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	return m
}

// is fails the test unless err is want: errors.Is for a contract error, and
// the very value for a pair that shares a code (ErrMailboxForked).
func is(t *testing.T, what string, err, want error) {
	t.Helper()
	switch {
	case want == nil && err != nil:
		t.Errorf("%s: %v, want nothing", what, err)
	case want != nil && !errors.Is(err, want):
		t.Errorf("%s: %v, want %v", what, err, want)
	case errors.Is(want, contracts.ErrMailboxUnreadable) && err != want: //nolint:errorlint // the words tell the two apart
		t.Errorf("%s: %v, want exactly %v", what, err, want)
	}
}

func count(t *testing.T, s *Subject, account string) int {
	t.Helper()
	n, err := s.Service.Count(ctx, account)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func listed(t *testing.T, s *Subject, c mailboxes.Caller) []contracts.Mailbox {
	t.Helper()
	l, err := s.Service.List(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	return l.Mailboxes
}

func kinds(s *Subject) []mailboxes.NoticeKind {
	var out []mailboxes.NoticeKind
	for _, n := range s.Notices.Sent() {
		out = append(out, n.Kind)
	}
	return out
}

var contractCases = []contractCase{
	{"C1 another account's mailbox is not_found", func(t *testing.T, s *Subject) {
		me, other := caller("acc_me"), caller("acc_other")
		m := create(t, s, me)
		_, err := s.Service.Get(ctx, other, m.ID)
		is(t, "get", err, contracts.ErrNotFound)
		_, err = s.Service.Update(ctx, other, m.ID, contracts.MailboxUpdate{})
		is(t, "update", err, contracts.ErrNotFound)
		is(t, "disconnect", s.Service.Disconnect(ctx, other, m.ID), contracts.ErrNotFound)
		_, err = s.Service.Open(ctx, other, m.ID, mailboxes.AccessListMessages)
		is(t, "open", err, contracts.ErrNotFound)
		// Owner before standing and before the fresh proof.
		s.Accounts.Set(other.Account, mailboxes.Standing{Closing: true})
		_, err = s.Service.Update(ctx, other, m.ID, contracts.MailboxUpdate{})
		is(t, "update while closing", err, contracts.ErrNotFound)
		is(t, "disconnect without a proof", s.Service.Disconnect(ctx, mailboxes.Caller{Account: other.Account}, m.ID), contracts.ErrNotFound)
	}},
	{"C2 what each standing may open", func(t *testing.T, s *Subject) {
		me := caller("acc_me")
		m := create(t, s, me)
		for _, c := range []struct {
			standing mailboxes.Standing
			access   mailboxes.Access
			want     error
		}{
			{mailboxes.Standing{TermsAgreed: true}, mailboxes.AccessDeleteMessages, nil},
			{mailboxes.Standing{TermsAgreed: true}, mailboxes.AccessSearch, nil},
			{mailboxes.Standing{Closing: true, TermsAgreed: true}, mailboxes.AccessListMessages, nil},
			{mailboxes.Standing{Closing: true, TermsAgreed: true}, mailboxes.AccessSearch, nil},
			{mailboxes.Standing{Closing: true, TermsAgreed: true}, mailboxes.AccessReadMessage, nil},
			{mailboxes.Standing{Closing: true, TermsAgreed: true}, mailboxes.AccessDownload, nil},
			{mailboxes.Standing{Closing: true, TermsAgreed: true}, mailboxes.AccessDeleteMessages, contracts.ErrAccountClosing},
			{mailboxes.Standing{}, mailboxes.AccessListMessages, nil},
			{mailboxes.Standing{}, mailboxes.AccessDownload, nil},
			{mailboxes.Standing{}, mailboxes.AccessSearch, contracts.ErrTermsChanged},
			{mailboxes.Standing{}, mailboxes.AccessReadMessage, contracts.ErrTermsChanged},
			{mailboxes.Standing{}, mailboxes.AccessDeleteMessages, contracts.ErrTermsChanged},
			{mailboxes.Standing{Closing: true}, mailboxes.AccessDeleteMessages, contracts.ErrAccountClosing},
			{mailboxes.Standing{Closing: true}, mailboxes.AccessSearch, contracts.ErrTermsChanged},
			{mailboxes.Standing{Closing: true}, mailboxes.AccessDownload, nil},
		} {
			s.Accounts.Set(me.Account, c.standing)
			b, err := s.Service.Open(ctx, me, m.ID, c.access)
			is(t, "open", err, c.want)
			if err == nil && (b.MailboxID != m.ID || len(b.Key) == 0) {
				t.Errorf("opened %+v", b)
			}
		}
	}},
	{"C3 making and changing need an open, agreed account", func(t *testing.T, s *Subject) {
		me := caller("acc_me")
		m := create(t, s, me)
		label := "Home"
		for _, c := range []struct {
			standing mailboxes.Standing
			want     error
		}{
			{mailboxes.Standing{Closing: true, TermsAgreed: true}, contracts.ErrAccountClosing},
			{mailboxes.Standing{}, contracts.ErrTermsChanged},
			{mailboxes.Standing{Closing: true}, contracts.ErrAccountClosing},
		} {
			s.Accounts.Set(me.Account, c.standing)
			_, err := s.Service.Create(ctx, me, contracts.MailboxCreate{})
			is(t, "create", err, c.want)
			_, err = s.Service.Update(ctx, me, m.ID, contracts.MailboxUpdate{Label: &label})
			is(t, "update", err, c.want)
			// Standing before the request's own input.
			bad := string(make([]byte, 65))
			_, err = s.Service.Create(ctx, me, contracts.MailboxCreate{Label: bad})
			is(t, "create with a bad label", err, c.want)
		}
	}},
	{"C4 a blocked account is unauthorized before anything else", func(t *testing.T, s *Subject) {
		me, other := caller("acc_me"), caller("acc_other")
		m := create(t, s, other)
		s.Accounts.Block(me.Account)
		_, err := s.Service.List(ctx, me)
		is(t, "list", err, contracts.ErrUnauthorized)
		_, err = s.Service.Create(ctx, me, contracts.MailboxCreate{})
		is(t, "create", err, contracts.ErrUnauthorized)
		_, err = s.Service.Get(ctx, me, m.ID)
		is(t, "get another's", err, contracts.ErrUnauthorized)
		is(t, "disconnect another's", s.Service.Disconnect(ctx, me, m.ID), contracts.ErrUnauthorized)
		_, err = s.Service.Open(ctx, me, m.ID, mailboxes.AccessDownload)
		is(t, "open another's", err, contracts.ErrUnauthorized)
	}},
	{"C5 five mailboxes, and a slot freed by disconnecting", func(t *testing.T, s *Subject) {
		me := caller("acc_me")
		var first *contracts.Mailbox
		for i := range mailboxes.MaxMailboxes {
			m := create(t, s, me)
			if i == 0 {
				first = m
			}
		}
		_, err := s.Service.Create(ctx, me, contracts.MailboxCreate{})
		is(t, "a sixth", err, contracts.ErrTooManyMailboxes)
		_, err = s.Service.Create(ctx, me, contracts.MailboxCreate{Label: string(make([]byte, 65))})
		is(t, "a sixth with a bad label", err, contracts.ErrInvalidLabel)
		is(t, "disconnect", s.Service.Disconnect(ctx, me, first.ID), nil)
		create(t, s, me)
		if n := count(t, s, me.Account); n != mailboxes.MaxMailboxes {
			t.Errorf("count %d", n)
		}
	}},
	{"C6 ten made in thirty days", func(t *testing.T, s *Subject) {
		me := caller("acc_me")
		for range mailboxes.MaxMadePerPeriod {
			m := create(t, s, me)
			is(t, "disconnect", s.Service.Disconnect(ctx, me, m.ID), nil)
		}
		_, err := s.Service.Create(ctx, me, contracts.MailboxCreate{})
		is(t, "an eleventh", err, contracts.ErrTooManyRequests)
		later := contractStart.Add(mailboxes.Period)
		s.SetNow(later)
		_, err = s.Service.Create(ctx, mailboxes.Caller{Account: me.Account, ConfirmedUntil: later.Add(time.Hour)}, contracts.MailboxCreate{})
		is(t, "thirty days later", err, nil)
	}},
	{"C7 the budget is checked before the five", func(t *testing.T, s *Subject) {
		me := caller("acc_me")
		for i := range mailboxes.MaxMadePerPeriod {
			m := create(t, s, me)
			if i%2 == 0 {
				is(t, "disconnect", s.Service.Disconnect(ctx, me, m.ID), nil)
			}
		}
		_, err := s.Service.Create(ctx, me, contracts.MailboxCreate{})
		is(t, "both apply", err, contracts.ErrTooManyRequests)
	}},
	{"C8 disconnecting needs a fresh proof, and changes nothing without one", func(t *testing.T, s *Subject) {
		me := caller("acc_me")
		m := create(t, s, me)
		is(t, "stale", s.Service.Disconnect(ctx, mailboxes.Caller{Account: me.Account}, m.ID), contracts.ErrConfirmationNeeded)
		if len(listed(t, s, me)) != 1 {
			t.Error("a refused disconnect took the mailbox away")
		}
		// Every standing may leave.
		s.Accounts.Set(me.Account, mailboxes.Standing{Closing: true})
		is(t, "closing and not agreed", s.Service.Disconnect(ctx, me, m.ID), nil)
	}},
	{"C9 disconnecting twice answers the same, and tells once", func(t *testing.T, s *Subject) {
		me := caller("acc_me")
		m := create(t, s, me)
		is(t, "first", s.Service.Disconnect(ctx, me, m.ID), nil)
		is(t, "second", s.Service.Disconnect(ctx, me, m.ID), nil)
		is(t, "second without a proof", s.Service.Disconnect(ctx, mailboxes.Caller{Account: me.Account}, m.ID), nil)
		_, err := s.Service.Get(ctx, me, m.ID)
		is(t, "get", err, contracts.ErrNotFound)
		_, err = s.Service.Record(ctx, m.ID)
		is(t, "record", err, contracts.ErrNotFound)
		_, err = s.Service.Pause(ctx, m.ID)
		is(t, "pause", err, contracts.ErrNotFound)
		if len(listed(t, s, me)) != 0 || count(t, s, me.Account) != 0 {
			t.Error("a disconnected mailbox is listed or counted")
		}
		if got := kinds(s); len(got) != 2 || got[1] != mailboxes.NoticeDisconnected {
			t.Errorf("notices %v", got)
		}
	}},
	{"C10 a stranger cannot tell a disconnected mailbox from one never made", func(t *testing.T, s *Subject) {
		me, other := caller("acc_me"), caller("acc_other")
		m := create(t, s, me)
		is(t, "disconnect", s.Service.Disconnect(ctx, me, m.ID), nil)
		is(t, "a stranger, disconnected", s.Service.Disconnect(ctx, other, m.ID), contracts.ErrNotFound)
		is(t, "a stranger, never made", s.Service.Disconnect(ctx, other, "mbx_aaaaaaaaaaaaaaaaaaaaaaaaaa"), contracts.ErrNotFound)
		is(t, "the owner, never made", s.Service.Disconnect(ctx, me, "mbx_aaaaaaaaaaaaaaaaaaaaaaaaaa"), contracts.ErrNotFound)
	}},
	{"C11 a lost record is listed, counted, opened by nobody, and disconnected for good", func(t *testing.T, s *Subject) {
		me := caller("acc_me")
		m := create(t, s, me)
		s.MakeLost(t, m.ID)
		l := listed(t, s, me)
		if len(l) != 1 || l[0].State != contracts.MailboxUnreadable || l[0].Address != "" || l[0].ID != m.ID {
			t.Fatalf("listed %+v", l)
		}
		if count(t, s, me.Account) != 1 {
			t.Error("not counted")
		}
		_, err := s.Service.Open(ctx, me, m.ID, mailboxes.AccessDownload)
		is(t, "open", err, contracts.ErrMailboxUnreadable)
		_, err = s.Service.Update(ctx, me, m.ID, contracts.MailboxUpdate{})
		is(t, "update", err, contracts.ErrMailboxUnreadable)
		is(t, "disconnect", s.Service.Disconnect(ctx, me, m.ID), nil)
		if len(listed(t, s, me)) != 0 || count(t, s, me.Account) != 0 {
			t.Error("the slot is still held")
		}
		is(t, "again", s.Service.Disconnect(ctx, me, m.ID), nil)
	}},
	{"C12 a forked record cannot be disconnected and keeps its slot", func(t *testing.T, s *Subject) {
		me := caller("acc_me")
		m := create(t, s, me)
		s.MakeForked(t, m.ID)
		is(t, "disconnect", s.Service.Disconnect(ctx, me, m.ID), contracts.ErrMailboxForked)
		l := listed(t, s, me)
		if len(l) != 1 || l[0].State != contracts.MailboxUnreadable || count(t, s, me.Account) != 1 {
			t.Fatalf("listed %+v", l)
		}
		_, err := s.Service.Open(ctx, me, m.ID, mailboxes.AccessDownload)
		is(t, "open", err, contracts.ErrMailboxUnreadable)
		_, err = s.Service.Pause(ctx, m.ID)
		is(t, "pause", err, contracts.ErrNotFound)
		is(t, "remove the account", s.Service.RemoveAccount(ctx, me.Account), nil)
		r, err := s.Service.Records(ctx, me.Account)
		if err != nil || len(r.Mailboxes) != 1 || r.Mailboxes[0].State != contracts.MailboxUnreadable {
			t.Errorf("after the account's removal: %+v %v", r, err)
		}
		if got := kinds(s); len(got) != 1 {
			t.Errorf("notices %v", got)
		}
	}},
	{"C13 removing an account leaves a closed list, even when it had none", func(t *testing.T, s *Subject) {
		me, never := caller("acc_me"), caller("acc_never")
		create(t, s, me)
		is(t, "remove", s.Service.RemoveAccount(ctx, me.Account), nil)
		is(t, "remove again", s.Service.RemoveAccount(ctx, me.Account), nil)
		is(t, "remove one that never had a list", s.Service.RemoveAccount(ctx, never.Account), nil)
		_, err := s.Service.Create(ctx, me, contracts.MailboxCreate{})
		is(t, "create after", err, contracts.ErrAccountClosing)
		_, err = s.Service.Create(ctx, never, contracts.MailboxCreate{})
		is(t, "a first create after", err, contracts.ErrAccountClosing)
		r, err := s.Service.Records(ctx, me.Account)
		if err != nil || len(r.Mailboxes) != 0 || count(t, s, me.Account) != 0 {
			t.Errorf("after removal %+v %v", r, err)
		}
		if got := kinds(s); len(got) != 1 || got[0] != mailboxes.NoticeAdded {
			t.Errorf("notices %v", got)
		}
	}},
	{"C14 the operator's view", func(t *testing.T, s *Subject) {
		me, other := caller("acc_me"), caller("acc_other")
		m := create(t, s, me)
		gone := create(t, s, other)
		is(t, "disconnect", s.Service.Disconnect(ctx, other, gone.ID), nil)
		r, err := s.Service.Records(ctx, "acc_unknown")
		if err != nil || r == nil || len(r.Mailboxes) != 0 {
			t.Errorf("an unknown account: %+v %v", r, err)
		}
		if n, err := s.Service.Total(ctx); err != nil || n != 1 {
			t.Errorf("total %d %v", n, err)
		}
		rec, err := s.Service.Record(ctx, m.ID)
		if err != nil || rec.Account != me.Account || rec.State != contracts.MailboxWaiting {
			t.Errorf("record %+v %v", rec, err)
		}
		found, err := s.Service.ByAddress(ctx, m.Address)
		if err != nil || len(found.Mailboxes) != 1 || found.Mailboxes[0].ID != m.ID {
			t.Errorf("by address %+v %v", found, err)
		}
		none, err := s.Service.ByAddress(ctx, "nobody@inbox.permaily.com")
		if err != nil || len(none.Mailboxes) != 0 {
			t.Errorf("by an unknown address %+v %v", none, err)
		}
	}},
	{"C15 pausing: ten changes in thirty days, a notice for each, mail still readable", func(t *testing.T, s *Subject) {
		me := caller("acc_me")
		m := create(t, s, me)
		for i := range mailboxes.MaxStateChangesPerPeriod {
			var err error
			if i%2 == 0 {
				_, err = s.Service.Pause(ctx, m.ID)
			} else {
				_, err = s.Service.Resume(ctx, m.ID)
			}
			is(t, "change", err, nil)
		}
		r, err := s.Service.Resume(ctx, m.ID)
		if err != nil || r.State == contracts.MailboxPaused {
			t.Errorf("resuming one that runs: %+v %v", r, err)
		}
		_, err = s.Service.Pause(ctx, m.ID)
		is(t, "an eleventh", err, contracts.ErrTooManyRequests)
		if got := kinds(s); len(got) != 1+mailboxes.MaxStateChangesPerPeriod {
			t.Errorf("notices %v", got)
		}
		s.SetNow(contractStart.Add(mailboxes.Period))
		r, err = s.Service.Pause(ctx, m.ID)
		if err != nil || r.State != contracts.MailboxPaused {
			t.Fatalf("thirty days later: %+v %v", r, err)
		}
		late := mailboxes.Caller{Account: me.Account, ConfirmedUntil: contractStart.Add(mailboxes.Period + time.Hour)}
		_, err = s.Service.Open(ctx, late, m.ID, mailboxes.AccessReadMessage)
		is(t, "open a paused one", err, nil)
	}},
	{"C16 changing the settings", func(t *testing.T, s *Subject) {
		me := caller("acc_me")
		m := create(t, s, me)
		long, label, days, bad := string(make([]byte, 65)), "Home", 30, 8
		_, err := s.Service.Update(ctx, me, m.ID, contracts.MailboxUpdate{Label: &long})
		is(t, "a long label", err, contracts.ErrInvalidLabel)
		_, err = s.Service.Update(ctx, me, m.ID, contracts.MailboxUpdate{RetentionDays: &bad})
		is(t, "a retention not offered", err, contracts.ErrInvalidRetention)
		got, err := s.Service.Update(ctx, me, m.ID, contracts.MailboxUpdate{Label: &label, RetentionDays: &days})
		if err != nil || got.Label != label || got.RetentionDays != days {
			t.Fatalf("%+v %v", got, err)
		}
		again, err := s.Service.Get(ctx, me, m.ID)
		if err != nil || again.Label != label || again.RetentionDays != days {
			t.Errorf("%+v %v", again, err)
		}
	}},
}
