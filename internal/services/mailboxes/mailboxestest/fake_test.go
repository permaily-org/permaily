package mailboxestest

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/httpapi"
	"github.com/permaily-org/permaily/internal/services/accounts"
	"github.com/permaily-org/permaily/internal/services/mailboxes"
)

// The fakes are what the pull requests built at the same time code against:
// they must fit the interfaces exactly.
var (
	_ httpapi.MailboxesService = (*Fake)(nil)
	_ accounts.Mailboxes       = (*Fake)(nil)
	_ mailboxes.Accounts       = (*Accounts)(nil)
	_ mailboxes.Notifier       = (*Notices)(nil)
)

// The table of mailboxes.Access, and what a change needs, as the fake keeps it.
func TestTheFakeKeepsThePolicy(t *testing.T) {
	ctx := context.Background()
	f := New()
	at := time.Date(2026, 11, 12, 10, 0, 0, 0, time.UTC)
	f.Now = func() time.Time { return at }
	me := mailboxes.Caller{Account: "acc_me", ConfirmedUntil: at.Add(time.Minute)}
	m, err := f.Create(ctx, me, contracts.MailboxCreate{Label: "Work"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Get(ctx, mailboxes.Caller{Account: "acc_other"}, m.ID); !errors.Is(err, contracts.ErrNotFound) {
		t.Fatalf("another account's mailbox: %v", err)
	}
	cases := []struct {
		standing mailboxes.Standing
		access   mailboxes.Access
		want     error
	}{
		{mailboxes.Standing{TermsAgreed: true}, mailboxes.AccessDeleteMessages, nil},
		{mailboxes.Standing{Closing: true, TermsAgreed: true}, mailboxes.AccessSearch, nil},
		{mailboxes.Standing{Closing: true, TermsAgreed: true}, mailboxes.AccessDeleteMessages, contracts.ErrAccountClosing},
		{mailboxes.Standing{}, mailboxes.AccessListMessages, nil},
		{mailboxes.Standing{}, mailboxes.AccessDownload, nil},
		{mailboxes.Standing{}, mailboxes.AccessReadMessage, contracts.ErrTermsChanged},
		{mailboxes.Standing{Closing: true}, mailboxes.AccessDeleteMessages, contracts.ErrAccountClosing},
		{mailboxes.Standing{Closing: true}, mailboxes.AccessSearch, contracts.ErrTermsChanged},
	}
	for _, c := range cases {
		f.Accounts.Set(me.Account, c.standing)
		if _, err := f.Open(ctx, me, m.ID, c.access); !errors.Is(err, c.want) {
			t.Errorf("%+v, access %d: got %v, want %v", c.standing, c.access, err, c.want)
		}
	}
	f.Accounts.Set(me.Account, mailboxes.Standing{Closing: true})
	if _, err := f.Create(ctx, me, contracts.MailboxCreate{}); !errors.Is(err, contracts.ErrAccountClosing) {
		t.Errorf("made while closing: %v", err)
	}
	if err := f.Disconnect(ctx, mailboxes.Caller{Account: me.Account}, m.ID); !errors.Is(err, contracts.ErrConfirmationNeeded) {
		t.Errorf("disconnected without a fresh proof: %v", err)
	}
	if err := f.Disconnect(ctx, me, m.ID); err != nil {
		t.Errorf("disconnecting while closing and not agreed: %v", err)
	}
	f.Accounts.Block(me.Account)
	if _, err := f.List(ctx, me); !errors.Is(err, contracts.ErrUnauthorized) {
		t.Errorf("a blocked account lists: %v", err)
	}
	if sent := f.Notices.Sent(); len(sent) != 2 || sent[0].Kind != mailboxes.NoticeAdded || sent[1].Kind != mailboxes.NoticeDisconnected {
		t.Errorf("notices %+v", sent)
	}
}

func TestTheFakesLimits(t *testing.T) {
	ctx := context.Background()
	f := New()
	me := mailboxes.Caller{Account: "acc_me"}
	for range mailboxes.MaxMailboxes {
		if _, err := f.Create(ctx, me, contracts.MailboxCreate{}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.Create(ctx, me, contracts.MailboxCreate{}); !errors.Is(err, contracts.ErrTooManyMailboxes) {
		t.Fatalf("a sixth: %v", err)
	}
	list, _ := f.List(ctx, me)
	id := list.Mailboxes[0].ID
	for i := range mailboxes.MaxStateChangesPerPeriod {
		var err error
		if i%2 == 0 {
			_, err = f.Pause(ctx, id)
		} else {
			_, err = f.Resume(ctx, id)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.Pause(ctx, id); !errors.Is(err, contracts.ErrTooManyRequests) {
		t.Fatalf("an eleventh change: %v", err)
	}
	f.MakeUnreadable(id)
	if r, err := f.Record(ctx, id); err != nil || r.State != contracts.MailboxUnreadable || r.RetentionDays != 0 {
		t.Fatalf("%+v %v", r, err)
	}
	if _, err := f.Open(ctx, me, id, mailboxes.AccessListMessages); !errors.Is(err, contracts.ErrMailboxUnreadable) {
		t.Fatalf("opened: %v", err)
	}
}
