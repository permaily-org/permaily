package accounts

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emersion/go-msgauth/dkim"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/keys"
	"github.com/permaily-org/permaily/internal/msgauth"
	"github.com/permaily-org/permaily/internal/services/mailboxes"
	"github.com/permaily-org/permaily/internal/store"
)

// Cases O1 to O5 of ADR 0026's fourth pull request against the real
// mailboxes service, as cmd/permaily-enclave composes it: *mailboxes.Service
// over the world's storage, with its own strict Anchored, accounts as its
// Accounts and its Notifier, and accounts given it with SetMailboxes.

const realDomain = "inbox.permaily.test"

// syncBuffer is a log both services may write from their own goroutines.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// realBoxes is what every enclave's mailboxes service shares: the world's
// storage and store of commits, one root of keys and the signatures checked.
type realBoxes struct {
	w    *world
	keys *keys.Local
	logs *syncBuffer
	auth inbox.Authenticator
}

func (w *world) realBoxes() *realBoxes {
	w.t.Helper()
	k, err := keys.NewLocal()
	if err != nil {
		w.t.Fatal(err)
	}
	return &realBoxes{w: w, keys: k, logs: &syncBuffer{}, auth: googleDKIM()}
}

// enclave is one enclave's mailboxes service, made with svc as its accounts
// and its notifier, and given to svc, as the composition does.
func (r *realBoxes) enclave(svc *Service) *mailboxes.Service {
	r.w.t.Helper()
	m, err := mailboxes.New(mailboxes.Config{
		Records: &store.Anchored{Records: r.w.mem, Commits: r.w.commits, OnlyNewest: true, Strict: true, Now: func() time.Time { return r.w.now }},
		Storage: r.w.mem, Keys: r.keys, Accounts: svc, Notifier: svc, Auth: r.auth, Domain: realDomain,
		Log: slog.New(slog.NewJSONHandler(r.logs, &slog.HandlerOptions{Level: slog.LevelDebug})), Now: func() time.Time { return r.w.now },
	})
	if err != nil {
		r.w.t.Fatal(err)
	}
	svc.SetMailboxes(m)
	return m
}

// create makes a mailbox as its person does, through the real service.
func (r *realBoxes) create(m *mailboxes.Service, account, label string) *contracts.Mailbox {
	r.w.t.Helper()
	box, err := m.Create(context.Background(), r.caller(account), contracts.MailboxCreate{Label: label})
	if err != nil {
		r.w.t.Fatalf("create: %v", err)
	}
	return box
}

func (r *realBoxes) caller(account string) mailboxes.Caller {
	return mailboxes.Caller{Account: account, ConfirmedUntil: r.w.now.Add(time.Hour)}
}

// deliver sends raw to address as SMTP does: RCPT TO, then the message.
func deliver(m *mailboxes.Service, raw []byte, address string) error {
	ctx := context.Background()
	id, err := m.Accept(ctx, address)
	if err != nil {
		return err
	}
	return m.Deliver(ctx, "alice@example.org", []string{id}, raw)
}

var googlePub, googleKey, _ = ed25519.GenerateKey(rand.Reader)

// googleSigned is Gmail's forwarding confirmation for to, signed as Google
// signs it, by a key the test publishes (googleDKIM).
func googleSigned(t *testing.T, to, requester string) []byte {
	t.Helper()
	raw := "From: Gmail Team <forwarding-noreply@google.com>\r\nTo: " + to + "\r\n" +
		"Subject: (#123456789) Gmail Forwarding Confirmation - Receive Mail from " + requester + "\r\n\r\n" +
		requester + " has requested to automatically forward mail to your email address " + to + ".\r\nConfirmation code: 123456789\r\n"
	var out bytes.Buffer
	if err := dkim.Sign(&out, strings.NewReader(raw), &dkim.SignOptions{Domain: "google.com", Selector: "test", Signer: googleKey, HeaderKeys: []string{"From", "To", "Subject"}}); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func googleDKIM() inbox.Authenticator {
	return msgauth.DKIM(func(_ context.Context, name string) ([]string, error) {
		if name != "test._domainkey.google.com" {
			return nil, errors.New("no such record")
		}
		return []string{"v=DKIM1; k=ed25519; p=" + base64.StdEncoding.EncodeToString(googlePub)}, nil
	})
}

// secretsOf is what of a mailbox only its person may know: its address, its
// local part and its label.
func secretsOf(m *contracts.Mailbox) []string {
	return []string{m.Address, strings.Split(m.Address, "@")[0], m.Label}
}

// holdsNone fails when text holds any of secrets, in any case.
func holdsNone(t *testing.T, what, text string, secrets ...string) {
	t.Helper()
	for _, s := range secrets {
		if s != "" && strings.Contains(strings.ToLower(text), strings.ToLower(s)) {
			t.Fatalf("%s holds %q:\n%s", what, s, text)
		}
	}
}

func asJSON(t *testing.T, v any) string {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// noticesSince is the notices about mailboxes sent since there were n
// messages, by their kind.
func (w *world) noticesSince(n int) []mailboxes.NoticeKind {
	w.t.Helper()
	sent, _ := w.sent(n)
	var kinds []mailboxes.NoticeKind
	for _, m := range sent {
		for kind, subject := range mailboxSubjects {
			if m.Subject == subject {
				kinds = append(kinds, kind)
			}
		}
	}
	return kinds
}

// O1 on the real service: each of the five notices is sent once, by the
// lifecycle that causes it, to the account's contact address, and names
// nothing of the mailbox. "Forwarding asked" is sent when a confirmation
// Google signed for exactly this mailbox is verified. While inbox.VerifyGmail
// is off, as it is in every release until a staging rehearsal turns it on,
// no confirmation is verified, so the real service sends no "forwarding
// asked" at all: the confirmation is stored, unverified, and shown to the
// person in their mailbox. With the mark on (here only in the test), it is
// sent once, for the first verified confirmation.
func TestTheRealMailboxesSendEachNoticeOnce(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	r := w.realBoxes()
	a := r.enclave(w.svc)
	me, _ := w.signUp("ann@example.org")
	acc := me.Account.ID
	only := func(n int, kind mailboxes.NoticeKind) {
		t.Helper()
		if got := w.noticesSince(n); len(got) != 1 || got[0] != kind {
			t.Fatalf("want one %s, got %v", kind, got)
		}
		w.one(n, "ann@example.org", mailboxSubjects[kind])
	}
	none := func(n int, what string) {
		t.Helper()
		if got := w.noticesSince(n); len(got) != 0 {
			t.Fatalf("%s: notices %v", what, got)
		}
	}

	n := w.count()
	m := r.create(a, acc, "Work Gmail")
	only(n, mailboxes.NoticeAdded)

	// Forwarding asked, with the mark off: stored, not verified, no notice.
	if inbox.VerifyGmail {
		t.Fatal("the mark is on in this build")
	}
	n = w.count()
	if err := deliver(a, googleSigned(t, m.Address, "someone@gmail.com"), m.Address); err != nil {
		t.Fatal(err)
	}
	none(n, "a confirmation with the mark off")
	got, err := a.Get(ctx, r.caller(acc), m.ID)
	if err != nil || got.State != contracts.MailboxWaiting || got.Source != nil || len(got.Confirmations) != 1 || got.Confirmations[0].Verified {
		t.Fatalf("with the mark off: %+v %v", got, err)
	}
	if rec, err := w.svc.MailboxRecords(ctx, acc); err != nil || rec.Mailboxes[0].Verified {
		t.Fatalf("the operator sees it verified: %+v %v", rec, err)
	}

	// With the mark on: one notice for the first verified confirmation, none
	// for the next.
	inbox.VerifyGmail = true
	t.Cleanup(func() { inbox.VerifyGmail = false })
	w.now = w.now.Add(time.Second)
	n = w.count()
	if err := deliver(a, googleSigned(t, m.Address, "first@gmail.com"), m.Address); err != nil {
		t.Fatal(err)
	}
	only(n, mailboxes.NoticeForwarding)
	w.now = w.now.Add(time.Second)
	n = w.count()
	if err := deliver(a, googleSigned(t, m.Address, "second@gmail.com"), m.Address); err != nil {
		t.Fatal(err)
	}
	none(n, "a second verified confirmation")
	inbox.VerifyGmail = false

	// Paused and resumed by the operator: one each; doing it again tells
	// nobody, since nothing changed.
	n = w.count()
	if _, err := w.svc.PauseMailbox(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	only(n, mailboxes.NoticePaused)
	n = w.count()
	if _, err := w.svc.PauseMailbox(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	none(n, "pausing a paused mailbox")
	n = w.count()
	if _, err := w.svc.ResumeMailbox(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	only(n, mailboxes.NoticeResumed)
	n = w.count()
	if _, err := w.svc.ResumeMailbox(ctx, m.ID); err != nil {
		t.Fatal(err)
	}
	none(n, "resuming a mailbox that is not paused")

	// Disconnected by its person: one; a second disconnect tells nobody.
	n = w.count()
	if err := a.Disconnect(ctx, r.caller(acc), m.ID); err != nil {
		t.Fatal(err)
	}
	only(n, mailboxes.NoticeDisconnected)
	n = w.count()
	if err := a.Disconnect(ctx, r.caller(acc), m.ID); err != nil {
		t.Fatal(err)
	}
	none(n, "a second disconnect")

	// Every notice went to the contact address and names nothing of the
	// mailbox; nor does any log line.
	sent, _ := w.sent(0)
	kinds := map[mailboxes.NoticeKind]int{}
	for _, mail := range sent {
		for kind, subject := range mailboxSubjects {
			if mail.Subject == subject {
				kinds[kind]++
				if mail.To != "ann@example.org" {
					t.Fatalf("%s went to %s", kind, mail.To)
				}
				holdsNone(t, string(kind), mail.Subject+mail.Text+mail.HTML, append(secretsOf(m), m.ID, acc)...)
			}
		}
	}
	if len(kinds) != 5 {
		t.Fatalf("notices by kind: %v", kinds)
	}
	for kind, k := range kinds {
		if k != 1 {
			t.Fatalf("%d of %s", k, kind)
		}
	}
	holdsNone(t, "the logs", w.logs.String()+r.logs.String(), append(secretsOf(m), m.ID, acc, "first@gmail.com", "someone@gmail.com")...)
}

// O1: an account removed ends its mailboxes in the real service without a
// notice per mailbox: the person was told their account was removed.
func TestARemovedAccountsMailboxesAreNotNoticedOneByOne(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	r := w.realBoxes()
	a := r.enclave(w.svc)
	me, _ := w.signUp("ann@example.org")
	m := r.create(a, me.Account.ID, "Work")
	if _, err := w.svc.DeleteAccount(ctx, me.Account.ID); err != nil {
		t.Fatal(err)
	}
	w.now = w.now.Add(8 * 24 * time.Hour)
	n := w.count()
	if err := w.svc.RemoveDue(ctx); err != nil {
		t.Fatal(err)
	}
	if got := w.noticesSince(n); len(got) != 0 {
		t.Fatalf("notices %v", got)
	}
	if _, err := a.Accept(ctx, m.Address); !errors.Is(err, mailboxes.ErrNoSuchMailbox) {
		t.Fatalf("the mailbox of a removed account: %v", err)
	}
}

// O2 on the real service: the operator pauses and resumes a mailbox, and an
// admin does with a fresh proof and the guard of ADR 0022, told before the
// proof. A second enclave, with its own accounts and mailboxes services over
// the same storage, sees each change: its RCPT TO refuses the paused mailbox
// and its operator sees it paused.
func TestPausingARealMailboxIsSeenByEveryEnclave(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	r := w.realBoxes()
	a := r.enclave(w.svc)
	other := w.enclave()
	b := r.enclave(other.svc)
	boss := w.account(superAdmin, true)
	ana := w.account("ana@example.org", true)
	if _, err := w.as(boss).MakeAdmin(ctx, ana.me.Account.ID); err != nil {
		t.Fatal(err)
	}
	carl, _ := w.signUp("carl@example.org")
	carlBox := r.create(a, carl.Account.ID, "Carl's Gmail")
	anaBox := r.create(b, ana.me.Account.ID, "Ana's Gmail")
	bossBox := r.create(a, boss.me.Account.ID, "Boss's Gmail")
	secrets := append(append(append(secretsOf(carlBox), secretsOf(anaBox)...), secretsOf(bossBox)...), "@inbox.")
	stateOn := func(svc *Service, account, id string) contracts.MailboxState {
		t.Helper()
		list, err := svc.MailboxRecords(ctx, account)
		if err != nil {
			t.Fatal(err)
		}
		holdsNone(t, "the records", asJSON(t, list), secrets...)
		for _, m := range list.Mailboxes {
			if m.ID == id {
				return m.State
			}
		}
		t.Fatalf("%s is not listed", id)
		return ""
	}
	accepted := func(m *mailboxes.Service, address string) bool {
		t.Helper()
		_, err := m.Accept(ctx, address)
		if err != nil && !errors.Is(err, mailboxes.ErrNoSuchMailbox) {
			t.Fatal(err)
		}
		return err == nil
	}

	// The operator, through the first enclave.
	rec, err := w.svc.PauseMailbox(ctx, carlBox.ID)
	if err != nil || rec.State != contracts.MailboxPaused {
		t.Fatalf("pause: %+v %v", rec, err)
	}
	holdsNone(t, "the answer", asJSON(t, rec), secrets...)
	if accepted(a, carlBox.Address) || accepted(b, carlBox.Address) {
		t.Fatal("a paused mailbox is accepted at RCPT TO")
	}
	if s := stateOn(other.svc, carl.Account.ID, carlBox.ID); s != contracts.MailboxPaused {
		t.Fatalf("the other enclave sees %s", s)
	}
	// Ana's receives; a message accepted for the boss's before it is paused
	// is not stored after it.
	if err := deliver(a, []byte("From: x@example.org\r\nSubject: hi\r\n\r\nhi\r\n"), anaBox.Address); err != nil {
		t.Fatal(err)
	}
	id, err := b.Accept(ctx, bossBox.Address)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.as(boss).PauseMailbox(ctx, bossBox.ID); err != nil {
		t.Fatal(err)
	}
	if err := b.Deliver(ctx, "x@example.org", []string{id}, []byte("Subject: late\r\n\r\nlate\r\n")); !errors.Is(err, mailboxes.ErrNotAllStored) {
		t.Fatalf("a delivery to a mailbox paused since RCPT TO: %v", err)
	}
	if msgs, _ := w.mem.ListMessages(ctx, bossBox.ID, 10, ""); len(msgs) != 0 {
		t.Fatalf("stored %d messages in a paused mailbox", len(msgs))
	}

	// An admin whose proof is not fresh is asked for one; told first when
	// the mailbox is an admin's.
	w.now = w.now.Add(time.Hour)
	stale, err := other.svc.Admin(ctx, w.who(ana.cookie))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := stale.ResumeMailbox(ctx, carlBox.ID); !errors.Is(err, contracts.ErrConfirmationNeeded) {
		t.Fatalf("without a fresh proof: %v", err)
	}
	for _, box := range []*contracts.Mailbox{anaBox, bossBox} {
		if _, err := stale.PauseMailbox(ctx, box.ID); !errors.Is(err, contracts.ErrSuperAdminOnly) {
			t.Fatalf("an admin's mailbox, without a proof: %v", err)
		}
		if _, err := w.as(ana).PauseMailbox(ctx, box.ID); !errors.Is(err, contracts.ErrSuperAdminOnly) {
			t.Fatalf("an admin's mailbox: %v", err)
		}
	}
	if s := stateOn(w.svc, ana.me.Account.ID, anaBox.ID); s != contracts.MailboxReceiving {
		t.Fatalf("an admin paused an admin's mailbox: %s", s)
	}

	// The admin, with a fresh proof, through the second enclave.
	fresh, err := other.svc.Admin(ctx, w.fresh(w.who(ana.cookie)))
	if err != nil {
		t.Fatal(err)
	}
	if rec, err := fresh.ResumeMailbox(ctx, carlBox.ID); err != nil || rec.State != contracts.MailboxWaiting {
		t.Fatalf("resume: %+v %v", rec, err)
	}
	if !accepted(a, carlBox.Address) || !accepted(b, carlBox.Address) {
		t.Fatal("a resumed mailbox is refused")
	}
	if s := stateOn(w.svc, carl.Account.ID, carlBox.ID); s != contracts.MailboxWaiting {
		t.Fatalf("the first enclave sees %s", s)
	}
	// The super admin acts on an admin's.
	if rec, err := w.as(boss).PauseMailbox(ctx, anaBox.ID); err != nil || rec.State != contracts.MailboxPaused {
		t.Fatalf("the super admin: %+v %v", rec, err)
	}

	// Counted on each enclave by kind; the admin log names the account and
	// the mailbox, by identifier.
	_, first := w.svc.OperatorActions()
	_, second := other.svc.OperatorActions()
	if first[ActionMailboxPaused] != 3 || first[ActionMailboxResumed] != 0 || second[ActionMailboxResumed] != 1 || second[ActionMailboxPaused] != 0 {
		t.Fatalf("counted %v and %v", first, second)
	}
	log, err := w.as(boss).AdminLog(ctx, "", 50)
	if err != nil {
		t.Fatal(err)
	}
	holdsNone(t, "the admin log", asJSON(t, log), secrets...)
	var mailboxEntries int
	for _, e := range log.Entries {
		if e.Kind == ActionMailboxPaused || e.Kind == ActionMailboxResumed {
			mailboxEntries++
			if e.Mailbox == "" || e.Account == nil {
				t.Fatalf("entry %+v", e)
			}
		}
	}
	if mailboxEntries != 3 {
		t.Fatalf("%d entries about mailboxes: %+v", mailboxEntries, log.Entries)
	}
	holdsNone(t, "the logs", w.logs.String()+r.logs.String(), append(secrets, carlBox.ID, anaBox.ID, bossBox.ID, carl.Account.ID)...)
}

// O2 on the real service: ten pauses and resumes of one mailbox in thirty
// days; the eleventh is too_many_requests, counted nowhere and told nobody,
// and is allowed again when the first leaves the thirty days.
func TestARealMailboxIsPausedTenTimesInThirtyDays(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	r := w.realBoxes()
	a := r.enclave(w.svc)
	me, _ := w.signUp("ann@example.org")
	m := r.create(a, me.Account.ID, "")
	start := w.now
	for i := range mailboxes.MaxStateChangesPerPeriod {
		act := w.svc.PauseMailbox
		if i%2 == 1 {
			act = w.svc.ResumeMailbox
		}
		if _, err := act(ctx, m.ID); err != nil {
			t.Fatalf("change %d: %v", i+1, err)
		}
		w.now = w.now.Add(time.Minute)
	}
	_, before := w.svc.OperatorActions()
	n := w.count()
	if _, err := w.svc.PauseMailbox(ctx, m.ID); !errors.Is(err, contracts.ErrTooManyRequests) {
		t.Fatalf("the eleventh: %v", err)
	}
	_, after := w.svc.OperatorActions()
	if after[ActionMailboxPaused] != before[ActionMailboxPaused] {
		t.Fatalf("a refusal was counted: %v", after)
	}
	if got := w.noticesSince(n); len(got) != 0 {
		t.Fatalf("a refusal was told: %v", got)
	}
	w.now = start.Add(mailboxes.Period + time.Second)
	if rec, err := w.svc.PauseMailbox(ctx, m.ID); err != nil || rec.State != contracts.MailboxPaused {
		t.Fatalf("thirty days later: %+v %v", rec, err)
	}
}

// O2 and O5 on the real service: a mailbox whose record is lost, and one
// whose record is forked, are shown as ADR 0026 says ("An unreadable
// mailbox"): its identifier, its account (from the signed index) and when it
// was added, and that it cannot be read; nothing else. Neither can be paused
// or resumed, by the operator or an admin; neither is found by its address;
// both refuse mail and are counted.
func TestARealMailboxThatCannotBeReadIsShownAsUnreadable(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	r := w.realBoxes()
	a := r.enclave(w.svc)
	boss := w.account(superAdmin, true)
	me, _ := w.signUp("ann@example.org")
	acc := me.Account.ID
	lost := r.create(a, acc, "Lost one")
	w.now = w.now.Add(time.Minute)
	forked := r.create(a, acc, "Forked one")
	added := map[string]time.Time{lost.ID: lost.CreatedAt, forked.ID: forked.CreatedAt}
	for _, n := range w.mem.RecordNames() {
		if strings.HasPrefix(n, "mailbox-"+lost.ID+"@") {
			if err := w.mem.DeleteRecord(ctx, n); err != nil {
				t.Fatal(err)
			}
		}
	}
	h, err := w.commits.History(ctx, "mailbox-"+forked.ID)
	if err != nil || h.Head == nil {
		t.Fatalf("history: %v", err)
	}
	var digest [sha256.Size]byte
	_, _ = rand.Read(digest[:])
	w.commits.Fork("mailbox-"+forked.ID, h.Head.Version, digest)
	// A fresh enclave reads both for the first time.
	a = r.enclave(w.svc)

	list, err := w.svc.MailboxRecords(ctx, acc)
	if err != nil || len(list.Mailboxes) != 2 {
		t.Fatalf("records: %+v %v", list, err)
	}
	for _, m := range list.Mailboxes {
		want := contracts.MailboxRecord{ID: m.ID, Account: acc, State: contracts.MailboxUnreadable, CreatedAt: added[m.ID]}
		if added[m.ID].IsZero() || !m.CreatedAt.Equal(want.CreatedAt) || asJSON(t, m) != asJSON(t, want) {
			t.Fatalf("an unreadable mailbox is shown as %s", asJSON(t, m))
		}
	}
	for _, m := range []*contracts.Mailbox{lost, forked} {
		rec, err := a.Record(ctx, m.ID)
		if err != nil || rec.State != contracts.MailboxUnreadable || rec.Account != acc {
			t.Fatalf("its record: %+v %v", rec, err)
		}
		if _, err := w.svc.PauseMailbox(ctx, m.ID); !errors.Is(err, contracts.ErrNotFound) {
			t.Fatalf("the operator pauses an unreadable mailbox: %v", err)
		}
		if _, err := w.as(boss).ResumeMailbox(ctx, m.ID); !errors.Is(err, contracts.ErrNotFound) {
			t.Fatalf("an admin resumes an unreadable mailbox: %v", err)
		}
		if found, err := w.svc.MailboxRecordByAddress(ctx, m.Address); err != nil || len(found.Mailboxes) != 0 {
			t.Fatalf("found by its address: %+v %v", found, err)
		}
		if _, err := a.Accept(ctx, m.Address); !errors.Is(err, mailboxes.ErrNoSuchMailbox) {
			t.Fatalf("RCPT TO: %v", err)
		}
	}
	if rec, err := w.svc.AccountRecord(ctx, acc); err != nil || rec.Mailboxes != 2 {
		t.Fatalf("the account counts %+v %v", rec, err)
	}
	_, actions := w.svc.OperatorActions()
	if actions[ActionMailboxPaused] != 0 || actions[ActionMailboxResumed] != 0 {
		t.Fatalf("counted %v", actions)
	}
	holdsNone(t, "the records", asJSON(t, list), append(secretsOf(lost), secretsOf(forked)...)...)
}

// O5 on the real service: across a mailbox's life, nothing the operator or
// an admin is answered, nothing in the admin log, the counts or the account's
// record, no notice and no log line holds the mailbox's address or label.
func TestNothingTheOperatorSeesOfARealMailboxNamesIt(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	r := w.realBoxes()
	a := r.enclave(w.svc)
	boss := w.account(superAdmin, true)
	me, _ := w.signUp("ann@example.org")
	acc := me.Account.ID
	m := r.create(a, acc, "Very Private Label")
	secrets := secretsOf(m)
	if err := deliver(a, []byte("From: Bob <bob@example.org>\r\nSubject: Hello there\r\n\r\nBody text\r\n"), m.Address); err != nil {
		t.Fatal(err)
	}
	var answers []any
	keep := func(v any, err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		answers = append(answers, v)
	}
	keep(w.svc.MailboxRecords(ctx, acc))
	keep(w.svc.MailboxRecordByAddress(ctx, m.Address))
	keep(w.svc.PauseMailbox(ctx, m.ID))
	keep(w.svc.ResumeMailbox(ctx, m.ID))
	admin := w.as(boss)
	keep(admin.MailboxRecords(ctx, acc))
	keep(admin.MailboxRecordByAddress(ctx, strings.ToUpper(m.Address)))
	keep(admin.PauseMailbox(ctx, m.ID))
	w.now = w.now.Add(time.Second)
	keep(admin.ResumeMailbox(ctx, m.ID))
	keep(w.svc.AccountRecord(ctx, acc))
	keep(w.svc.AccountRecords(ctx, "", 50))
	keep(w.svc.Stats(ctx))
	keep(admin.AdminLog(ctx, "", 50))
	since, actions := w.svc.OperatorActions()
	answers = append(answers, since, actions)
	for i, v := range answers {
		holdsNone(t, fmt.Sprintf("answer %d", i), asJSON(t, v), append(secrets, "bob@example.org", "Hello there", "Body text")...)
	}
	if stats, _ := w.svc.Stats(ctx); stats.Mailboxes.Total != 1 {
		t.Fatalf("stats %+v", stats)
	}
	sent, _ := w.sent(0)
	for _, mail := range sent {
		holdsNone(t, "a message "+mail.Subject, mail.Subject+mail.Text+mail.HTML, secrets...)
	}
	holdsNone(t, "the logs", w.logs.String()+r.logs.String(), append(secrets, m.ID, acc, "bob@example.org", "Hello there", "Body text")...)
}
