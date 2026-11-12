package accounts

import (
	"context"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/domain/mail"
	"github.com/permaily-org/permaily/internal/services/mailboxes"
)

// The notices about mailboxes (ADR 0026, "Notices by mail"), case O1 of its
// fourth pull request: accounts is the mailboxes service's Notifier, and
// tells the account's contact address with blocks, like every other notice.
var _ mailboxes.Notifier = (*Service)(nil)

// The subjects of the notices about mailboxes, each fixed words.
var mailboxSubjects = map[mailboxes.NoticeKind]string{
	mailboxes.NoticeAdded:        "A mailbox was added to your Permaily account",
	mailboxes.NoticeForwarding:   "Gmail asked to forward to your Permaily mailbox",
	mailboxes.NoticeDisconnected: "A mailbox was disconnected from your Permaily account",
	mailboxes.NoticePaused:       "Permaily paused one of your mailboxes",
	mailboxes.NoticeResumed:      "Permaily resumed one of your mailboxes",
}

// O1: each kind is one message to the account's contact address, its fixed
// subject, its blocks as ADR 0026 writes them, and when it happened.
func TestEveryMailboxNoticeIsSentOnceToTheContactAddress(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	me, _ := w.signUp("ann@example.org")
	at := time.Date(2026, 11, 12, 9, 30, 0, 0, time.UTC)
	for kind, subject := range mailboxSubjects {
		n := w.count()
		w.svc.MailboxNotice(ctx, me.Account.ID, kind, at)
		m := w.one(n, "ann@example.org", subject)
		if !strings.Contains(m.Text, "When: 12 November 2026, 09:30 UTC") {
			t.Errorf("%s: no time:\n%s", kind, m.Text)
		}
		if m.Text != mail.Text(m.Blocks) || m.HTML == "" || m.HTML != mail.HTML(m.Subject, m.Blocks) {
			t.Errorf("%s: not written from its blocks", kind)
		}
		if m.Blocks[0].Kind != mail.KindLead || m.Blocks[len(m.Blocks)-1].Kind != mail.KindNote {
			t.Errorf("%s: blocks %+v", kind, m.Blocks)
		}
	}
}

// O1: a notice about a mailbox names no mailbox, no address but support's,
// no label, no code, no link and no identifier: the facts are times.
func TestAMailboxNoticeNamesNoMailbox(t *testing.T) {
	addressRE := regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(\.[A-Za-z0-9-]+)*`)
	for _, s := range mailboxSamples() {
		for _, part := range []string{s.mail.Subject, s.mail.Text} {
			for _, a := range addressRE.FindAllString(part, -1) {
				if a != supportAddress {
					t.Errorf("%s names %s", s.name, a)
				}
			}
			for _, bad := range []string{"http", "://", "mbx_", "acc_", "inbox.permaily", "#"} {
				if strings.Contains(part, bad) {
					t.Errorf("%s holds %q", s.name, bad)
				}
			}
			if regexp.MustCompile(`\d{6}`).MatchString(strings.ReplaceAll(part, "2026", "")) {
				t.Errorf("%s holds what looks like a code", s.name)
			}
		}
		for _, b := range s.mail.Blocks {
			if b.Kind == mail.KindAction || b.Kind == mail.KindCode || b.URL != "" {
				t.Errorf("%s has a %s block", s.name, b.Kind)
			}
			for _, f := range b.Facts {
				if !f.Machine || f.Label != "When" {
					t.Errorf("%s has a fact that is not when: %+v", s.name, f)
				}
			}
		}
	}
}

// O1: a notice never fails what caused it and holds it back no longer than
// any notice: with the mail system down it is logged by its kind alone; for
// an account that is gone, or a kind it does not know, nothing is sent and
// nothing of the account is logged.
func TestAMailboxNoticeNeverFails(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	me, _ := w.signUp("ann@example.org")
	before, n := w.logs.Len(), w.count()

	w.out.Fail = true
	w.svc.MailboxNotice(ctx, me.Account.ID, mailboxes.NoticePaused, w.now)
	w.svc.Wait()
	w.out.Fail = false
	w.svc.MailboxNotice(ctx, "acc_aaaaaaaaaaaaaaaaaaaaaaaaaa", mailboxes.NoticeAdded, w.now)
	w.svc.MailboxNotice(ctx, "not an account", mailboxes.NoticeAdded, w.now)
	w.svc.MailboxNotice(ctx, me.Account.ID, "mailbox_renamed", w.now)
	if got, _ := w.sent(n); len(got) != 0 {
		t.Fatalf("sent %+v", got)
	}
	logged := w.logs.String()[before:]
	for _, want := range []string{"kind=mailbox_paused", "kind=mailbox_added", "kind=mailbox_renamed"} {
		if !strings.Contains(logged, want) {
			t.Errorf("not logged: %s\n%s", want, logged)
		}
	}
	for _, s := range []string{me.Account.ID, "ann@example.org", "acc_aaaa"} {
		if strings.Contains(logged, s) {
			t.Fatalf("logged %q:\n%s", s, logged)
		}
	}

	// A mail system that does not answer holds the notice back for the wait
	// of any notice, and no longer.
	w.svc.noticeWait = 50 * time.Millisecond
	hung := &stuck{released: make(chan struct{}, 1)}
	w.svc.cfg.Mailer = hung
	start := time.Now()
	w.svc.MailboxNotice(ctx, me.Account.ID, mailboxes.NoticeResumed, w.now)
	if waited := time.Since(start); waited > 5*time.Second {
		t.Fatalf("held for %s", waited)
	}
	<-hung.released
	w.svc.Wait()
}

// mailboxSamples are the samples of the notices about mailboxes.
func mailboxSamples() []sampleMail {
	var out []sampleMail
	for _, s := range sampleMails() {
		if strings.HasPrefix(s.name, "mailbox-") || s.name == "forwarding-asked" {
			out = append(out, s)
		}
	}
	return out
}

// The notices about mailboxes are among the samples: their golden texts, the
// checks of their HTML (internal/domain/mail) and their preview come from
// there.
func TestTheMailboxNoticesAreSamples(t *testing.T) {
	got := map[string]bool{}
	for _, s := range mailboxSamples() {
		got[s.name] = true
	}
	for _, name := range []string{"mailbox-added", "forwarding-asked", "mailbox-disconnected", "mailbox-paused", "mailbox-resumed"} {
		if !got[name] {
			t.Errorf("no sample %s", name)
		}
	}
	if len(got) != 5 {
		t.Errorf("samples %v", got)
	}
}
