package accounts

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"log/slog"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/domain"
	"github.com/permaily-org/permaily/internal/keys"
	"github.com/permaily-org/permaily/internal/store"
)

// world is a service over storage the test can write to by hand, the way
// whoever runs the real storage can.
type world struct {
	t       *testing.T
	mem     *store.Memory
	commits *store.MemoryCommits
	// anchored is the store of the accounts' records, as the enclave sets it
	// up: only the newest version of a record is kept.
	anchored *store.Anchored
	out      *Outbox
	// idp plays the providers (providers_test.go).
	idp *fakeProviders
	// terms is the version of the terms in force for the world's enclaves:
	// a test changes it as a new release would.
	terms string
	logs  *bytes.Buffer
	svc   *Service
	now   time.Time
}

func newWorld(t *testing.T) *world {
	t.Helper()
	w := &world{
		t: t, mem: store.NewMemory(), commits: store.NewMemoryCommits(), out: &Outbox{}, idp: newFakeProviders(), logs: &bytes.Buffer{},
		now: time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC), terms: domain.TermsVersion,
	}
	w.mem.Now = func() time.Time { return w.now }
	local, err := keys.NewLocal()
	if err != nil {
		t.Fatal(err)
	}
	w.svc = w.service(local)
	return w
}

func (w *world) service(k Keys) *Service {
	w.anchored = &store.Anchored{Records: w.mem, Commits: w.commits, OnlyNewest: true, Now: func() time.Time { return w.now }}
	svc, err := New(Config{
		Records: w.anchored,
		Entries: w.mem, Keys: k, Mailer: w.out, Providers: w.idp, AppURL: "https://app.permaily.test", SuperAdmin: superAdmin,
		Log: slog.New(slog.NewTextHandler(w.logs, nil)), Now: func() time.Time { return w.now }, Terms: func() string { return w.terms },
	})
	if err != nil {
		w.t.Fatal(err)
	}
	// A wait for a notice that no test runs out of by accident: the ones
	// about the wait end it themselves (notices_test.go).
	svc.noticeWait = time.Hour
	return svc
}

var (
	codeRE = regexp.MustCompile(`\n    (\d{6})\n`)
	linkRE = regexp.MustCompile(`https://app\.permaily\.test/sign-in#attempt=(att_[a-z2-7]{26})&token=([A-Za-z0-9_-]{43})\n`)
)

// start asks for a sign-in and returns the attempt and the message that was
// sent for it, nil when none was. With an invitation, it is a sign-up from
// the sign-up screen: the person agreed to the terms in force.
func (w *world) start(address, invitation string) (string, *Mail) {
	w.t.Helper()
	before := len(w.out.Sent())
	in := contracts.EmailStart{Address: address, Invitation: invitation}
	if invitation != "" {
		in.Terms = w.terms
	}
	res, err := w.svc.StartEmail(context.Background(), in)
	if err != nil {
		w.t.Fatalf("start %s: %v", address, err)
	}
	if res.ExpiresIn != 600 || !validID("att", res.Attempt) {
		w.t.Fatalf("answer: %+v", res)
	}
	w.svc.Wait()
	sent := w.out.Sent()
	if len(sent) == before {
		return res.Attempt, nil
	}
	return res.Attempt, &sent[len(sent)-1]
}

func code(m *Mail) string {
	if g := codeRE.FindStringSubmatch(m.Text); g != nil {
		return g[1]
	}
	return ""
}

func (w *world) invite() string {
	w.t.Helper()
	inv, err := w.svc.CreateInvitations(context.Background(), 1)
	if err != nil {
		w.t.Fatal(err)
	}
	return inv.Invitations[0]
}

// signUp makes an account for address and returns it with its cookie.
func (w *world) signUp(address string) (*contracts.Me, string) {
	w.t.Helper()
	attempt, mail := w.start(address, w.invite())
	if mail == nil {
		w.t.Fatal("no message for a sign-up with an invitation")
	}
	me, cookie, err := w.svc.FinishEmail(context.Background(), contracts.EmailFinish{Attempt: attempt, Code: code(mail)}, Browser{UserAgent: "test browser"})
	if err != nil {
		w.t.Fatal(err)
	}
	return me, cookie
}

// fresh returns the session behind who as one whose person proved who they
// are a moment ago: the tests of what such a session does start from there.
// It writes nothing; confirm_test.go tests the proof itself, through the
// storage.
func (w *world) fresh(who *Authenticated) *Authenticated {
	if who == nil {
		return nil
	}
	out := *who
	out.Session.ConfirmedUntil = w.now.Add(domain.ConfirmationLifetime)
	return &out
}

// ghost is a session with a fresh proof, of an account that is not there.
func (w *world) ghost() *Authenticated {
	return &Authenticated{AccountID: newID("acc"), Session: contracts.Session{ID: newID("ses"), ConfirmedUntil: w.now.Add(domain.ConfirmationLifetime)}}
}

func (w *world) wrongCode(m *Mail) string {
	if code(m) == "000000" {
		return "000001"
	}
	return "000000"
}

func TestSignUpAndSignIn(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	invitation := w.invite()
	if !regexp.MustCompile(`^[2-9A-Z]{4}-[2-9A-Z]{4}-[2-9A-Z]{4}$`).MatchString(invitation) {
		t.Fatalf("invitation %q", invitation)
	}

	// Without an invitation an address that has no account gets nothing,
	// and the answer does not say so.
	attempt, mail := w.start("ana@example.org", "")
	if mail != nil {
		t.Fatal("a message for an address without an account or an invitation")
	}
	if _, _, err := w.svc.FinishEmail(ctx, contracts.EmailFinish{Attempt: attempt, Code: "123456"}, Browser{UserAgent: ""}); !errors.Is(err, contracts.ErrInvalidCode) {
		t.Fatalf("finishing a sign-in that sent nothing: %v", err)
	}

	attempt, mail = w.start(" Ana@Example.org ", strings.ToLower(invitation))
	if mail == nil || mail.To != "ana@example.org" || mail.Subject != "Sign in to Permaily" || code(mail) == "" || !linkRE.MatchString(mail.Text) {
		t.Fatalf("message: %+v", mail)
	}
	if strings.Contains(mail.Subject, code(mail)) {
		t.Fatal("the code is in the subject")
	}
	me, cookie, err := w.svc.FinishEmail(ctx, contracts.EmailFinish{Attempt: attempt, Code: code(mail)}, Browser{UserAgent: "Mozilla/5.0 (test)"})
	if err != nil {
		t.Fatal(err)
	}
	if !validID("acc", me.Account.ID) || me.Account.Address != "ana@example.org" || me.Account.State != contracts.AccountActive ||
		me.Account.Language != "en" || !me.Account.CreatedAt.Equal(w.now) {
		t.Fatalf("account: %+v", me.Account)
	}
	if !me.Session.Current || me.Session.Device != "Mozilla/5.0 (test)" || !me.Session.ExpiresAt.Equal(w.now.Add(domain.SessionIdle)) {
		t.Fatalf("session: %+v", me.Session)
	}
	// The code worked once.
	if _, _, err := w.svc.FinishEmail(ctx, contracts.EmailFinish{Attempt: attempt, Code: code(mail)}, Browser{UserAgent: ""}); !errors.Is(err, contracts.ErrInvalidCode) {
		t.Fatalf("the same code again: %v", err)
	}

	who, err := w.svc.Authenticate(ctx, cookie)
	if err != nil || who.AccountID != me.Account.ID || who.Session.ID != me.Session.ID {
		t.Fatalf("authenticate: %+v %v", who, err)
	}
	if got, err := w.svc.Me(ctx, who); err != nil || got.Account != me.Account {
		t.Fatalf("me: %+v %v", got, err)
	}
	if got, err := w.svc.Account(ctx, me.Account.ID); err != nil || *got != me.Account {
		t.Fatalf("account by id: %+v %v", got, err)
	}

	// The invitation was used up: it lets nobody else in.
	if _, err := w.svc.StartEmail(ctx, contracts.EmailStart{Address: "eve@example.org", Invitation: invitation}); !errors.Is(err, contracts.ErrInvalidInvitation) {
		t.Fatalf("a used invitation: %v", err)
	}

	// Signing in again needs no invitation, and leads to the same account.
	attempt, mail = w.start("ANA@example.org", "")
	if mail == nil {
		t.Fatal("no message for an address with an account")
	}
	token := linkRE.FindStringSubmatch(mail.Text)[2]
	again, cookie2, err := w.svc.FinishEmail(ctx, contracts.EmailFinish{Attempt: attempt, Token: token}, Browser{UserAgent: "another device"})
	if err != nil || again.Account != me.Account || again.Session.ID == me.Session.ID || cookie2 == cookie {
		t.Fatalf("second sign-in: %+v %v", again, err)
	}
	if _, _, err := w.svc.FinishEmail(ctx, contracts.EmailFinish{Attempt: attempt, Token: token}, Browser{UserAgent: ""}); !errors.Is(err, contracts.ErrInvalidCode) {
		t.Fatalf("the same link again: %v", err)
	}

	// Nothing the service logged names the person, the code or the cookie.
	for _, secret := range []string{"ana@example.org", "example.org", code(mail), token, cookie, me.Account.ID, invitation} {
		if strings.Contains(w.logs.String(), secret) {
			t.Fatalf("the logs hold %q:\n%s", secret, w.logs)
		}
	}
}

func TestWhatIsStored(t *testing.T) {
	w := newWorld(t)
	invitation := w.invite()
	attempt, mail := w.start("ana@example.org", invitation)
	me, cookie, err := w.svc.FinishEmail(context.Background(), contracts.EmailFinish{Attempt: attempt, Code: code(mail)}, Browser{UserAgent: "test browser"})
	if err != nil {
		t.Fatal(err)
	}
	secret := cookie[strings.LastIndex(cookie, ".")+1:]
	normalized, _ := domain.NormalizeInvitation(invitation)
	readable := false
	for _, name := range w.mem.RecordNames() {
		rec, _ := w.mem.GetRecord(context.Background(), name)
		// The account's record is in plain text, for support to read.
		if strings.HasPrefix(name, "account-"+me.Account.ID+"@") && rec.Plain && bytes.Contains(rec.Value, []byte(`"address":"ana@example.org"`)) {
			readable = true
		}
		for _, hidden := range []string{secret, code(mail), normalized} {
			if bytes.Contains(rec.Value, []byte(hidden)) {
				t.Errorf("record %s holds a secret", name)
			}
		}
	}
	if !readable {
		t.Fatalf("no readable record of the account among %v", w.mem.RecordNames())
	}
	// Sessions are kept by the hash of their secret.
	list, _ := w.mem.ListEntries(context.Background(), accountPartition(me.Account.ID), "", "", 10)
	if len(list) != 1 || bytes.Contains(list[0].Value, []byte(secret)) || !list[0].ExpiresAt.Equal(me.Session.ExpiresAt) {
		t.Fatalf("session entry: %+v", list)
	}
	// The invitation is gone, and was never there under its own name.
	if _, err := w.mem.GetEntry(context.Background(), invitationPartition(normalized), invitationKey); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("the used invitation: %v", err)
	}
	if strings.Contains(invitationPartition(normalized), normalized) {
		t.Fatal("an invitation is stored under its own name")
	}
}

func TestWrongCodes(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	w.signUp("ana@example.org")

	// Five wrong codes end a sign-in: the right one comes too late.
	attempt, mail := w.start("ana@example.org", "")
	for range domain.EmailCodeTries {
		if _, _, err := w.svc.FinishEmail(ctx, contracts.EmailFinish{Attempt: attempt, Code: w.wrongCode(mail)}, Browser{UserAgent: ""}); !errors.Is(err, contracts.ErrInvalidCode) {
			t.Fatalf("wrong code: %v", err)
		}
	}
	if _, _, err := w.svc.FinishEmail(ctx, contracts.EmailFinish{Attempt: attempt, Code: code(mail)}, Browser{UserAgent: ""}); !errors.Is(err, contracts.ErrInvalidCode) {
		t.Fatalf("the right code after five wrong ones: %v", err)
	}
	// A wrong link counts as a try too, and an empty answer is wrong.
	attempt, mail = w.start("ana@example.org", "")
	for _, in := range []contracts.EmailFinish{{Token: "nope"}, {}, {Code: ""}, {Token: code(mail)}} {
		in.Attempt = attempt
		if _, _, err := w.svc.FinishEmail(ctx, in, Browser{UserAgent: ""}); !errors.Is(err, contracts.ErrInvalidCode) {
			t.Fatalf("%+v: %v", in, err)
		}
	}
	if _, _, err := w.svc.FinishEmail(ctx, contracts.EmailFinish{Attempt: attempt, Code: code(mail)}, Browser{UserAgent: ""}); err != nil {
		t.Fatalf("the right code at the fifth try: %v", err)
	}

	// After ten wrong codes in a day the address's messages carry only the
	// link, so guessing stops while the person can still sign in.
	w.now = w.now.Add(2 * time.Hour)
	attempt, mail = w.start("ana@example.org", "")
	for range domain.EmailCodeTries {
		_, _, _ = w.svc.FinishEmail(ctx, contracts.EmailFinish{Attempt: attempt, Code: w.wrongCode(mail)}, Browser{UserAgent: ""})
	}
	attempt, mail = w.start("ana@example.org", "")
	if mail == nil || code(mail) != "" || !linkRE.MatchString(mail.Text) {
		t.Fatalf("message after ten wrong codes: %+v", mail)
	}
	for _, guess := range []string{"000000", ""} {
		if _, _, err := w.svc.FinishEmail(ctx, contracts.EmailFinish{Attempt: attempt, Code: guess}, Browser{UserAgent: ""}); !errors.Is(err, contracts.ErrInvalidCode) {
			t.Fatalf("a code for a sign-in without one: %v", err)
		}
	}
	if _, _, err := w.svc.FinishEmail(ctx, contracts.EmailFinish{Attempt: attempt, Token: linkRE.FindStringSubmatch(mail.Text)[2]}, Browser{UserAgent: ""}); err != nil {
		t.Fatalf("the link: %v", err)
	}
	// A day later codes are back.
	w.now = w.now.Add(25 * time.Hour)
	if _, mail = w.start("ana@example.org", ""); mail == nil || code(mail) == "" {
		t.Fatalf("message a day later: %+v", mail)
	}
}

func TestSignInEnds(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	w.signUp("ana@example.org")
	attempt, mail := w.start("ana@example.org", "")
	w.now = w.now.Add(domain.EmailCodeLifetime)
	if _, _, err := w.svc.FinishEmail(ctx, contracts.EmailFinish{Attempt: attempt, Code: code(mail)}, Browser{UserAgent: ""}); !errors.Is(err, contracts.ErrInvalidCode) {
		t.Fatalf("a code after ten minutes: %v", err)
	}
	// A sign-in lives in the enclave that started it: another one, or the
	// same after a restart, knows nothing of it.
	attempt, mail = w.start("ana@example.org", "")
	restarted := w.service(w.svc.cfg.Keys)
	if _, _, err := restarted.FinishEmail(ctx, contracts.EmailFinish{Attempt: attempt, Code: code(mail)}, Browser{UserAgent: ""}); !errors.Is(err, contracts.ErrInvalidCode) {
		t.Fatalf("a code after a restart: %v", err)
	}
	if _, _, err := w.svc.FinishEmail(ctx, contracts.EmailFinish{Attempt: "att_unknown", Code: "123456"}, Browser{UserAgent: ""}); !errors.Is(err, contracts.ErrInvalidCode) {
		t.Fatalf("an unknown sign-in: %v", err)
	}
}

func TestLookAtALink(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	w.signUp("ana@example.org")
	attempt, mail := w.start("ana@example.org", "")
	token := linkRE.FindStringSubmatch(mail.Text)[2]

	// The page that opened the link is told whose sign-in it is, as often as
	// it asks, and the link still works afterwards.
	for range 3 {
		got, err := w.svc.LookEmail(ctx, contracts.EmailLook{Attempt: attempt, Token: token})
		if err != nil || got.Address != "ana@example.org" {
			t.Fatalf("looking at a link: %+v, %v", got, err)
		}
	}
	// Nobody is told without the link: not with the code, which is typed
	// where the sign-in was asked for, and not with nothing.
	for name, in := range map[string]contracts.EmailLook{
		"the code as a token":  {Attempt: attempt, Token: code(mail)},
		"no token":             {Attempt: attempt},
		"another sign-in":      {Attempt: "att_unknown", Token: token},
		"a token cut short":    {Attempt: attempt, Token: token[:20]},
		"no sign-in, no token": {},
	} {
		if got, err := w.svc.LookEmail(ctx, in); !errors.Is(err, contracts.ErrInvalidCode) || got != nil {
			t.Errorf("%s: %+v, %v", name, got, err)
		}
	}
	// Two wrong tokens were tried: they count like wrong codes, and three
	// more end the sign-in for the right one too.
	me, _, err := w.svc.FinishEmail(ctx, contracts.EmailFinish{Attempt: attempt, Token: token}, Browser{UserAgent: ""})
	if err != nil || me.Account.Address != "ana@example.org" {
		t.Fatalf("the link after looking at it: %v", err)
	}
	if _, err := w.svc.LookEmail(ctx, contracts.EmailLook{Attempt: attempt, Token: token}); !errors.Is(err, contracts.ErrInvalidCode) {
		t.Fatalf("a link that was used: %v", err)
	}

	attempt, mail = w.start("ana@example.org", "")
	token = linkRE.FindStringSubmatch(mail.Text)[2]
	for range domain.EmailCodeTries {
		_, _ = w.svc.LookEmail(ctx, contracts.EmailLook{Attempt: attempt, Token: "not-the-token"})
	}
	if _, err := w.svc.LookEmail(ctx, contracts.EmailLook{Attempt: attempt, Token: token}); !errors.Is(err, contracts.ErrInvalidCode) {
		t.Fatalf("a link after five wrong tokens: %v", err)
	}

	// A link that ended by time says nothing either.
	attempt, mail = w.start("ana@example.org", "")
	token = linkRE.FindStringSubmatch(mail.Text)[2]
	w.now = w.now.Add(domain.EmailCodeLifetime)
	if _, err := w.svc.LookEmail(ctx, contracts.EmailLook{Attempt: attempt, Token: token}); !errors.Is(err, contracts.ErrInvalidCode) {
		t.Fatalf("a link after ten minutes: %v", err)
	}
}

func TestStartLimits(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	w.signUp("ana@example.org") // the first message of the hour
	for i := 1; i < domain.EmailStartsPerHour; i++ {
		if _, mail := w.start("ana@example.org", ""); mail == nil {
			t.Fatalf("message %d of the hour was not sent", i+1)
		}
	}
	// The sixth is not sent, and the answer is the same.
	if _, mail := w.start("ana@example.org", ""); mail != nil {
		t.Fatal("a sixth message in an hour")
	}
	w.now = w.now.Add(time.Hour + time.Minute)
	if _, mail := w.start("ana@example.org", ""); mail == nil {
		t.Fatal("no message an hour later")
	}

	// One invitation does not send mail to address after address.
	invitation := w.invite()
	for i := range domain.EmailStartsPerHour {
		if _, mail := w.start(string(rune('a'+i))+"@example.org", invitation); mail == nil {
			t.Fatalf("sign-up %d with the invitation sent nothing", i+1)
		}
	}
	if _, err := w.svc.StartEmail(ctx, contracts.EmailStart{Address: "z@example.org", Invitation: invitation, Terms: w.terms}); !errors.Is(err, contracts.ErrInvalidInvitation) {
		t.Fatalf("a sixth address with one invitation: %v", err)
	}

	for _, bad := range []contracts.EmailStart{{Address: "not an address"}, {Address: "ana@example.org", Invitation: "nope"}, {Address: "ana@example.org", Invitation: "abcd-2345-wxyz"}} {
		if _, err := w.svc.StartEmail(ctx, bad); err == nil {
			t.Errorf("%+v was accepted", bad)
		}
	}
	if _, err := w.svc.StartEmail(ctx, contracts.EmailStart{Address: "nope"}); !errors.Is(err, contracts.ErrInvalidAddress) {
		t.Fatalf("a bad address: %v", err)
	}
}

func TestMailSystemDown(t *testing.T) {
	w := newWorld(t)
	w.signUp("ana@example.org")
	w.out.Fail = true
	attempt, mail := w.start("ana@example.org", "")
	if mail != nil || len(w.svc.attempts) != 0 {
		t.Fatalf("a sign-in is kept for a message that was not sent (%d)", len(w.svc.attempts))
	}
	if _, _, err := w.svc.FinishEmail(context.Background(), contracts.EmailFinish{Attempt: attempt, Code: "123456"}, Browser{UserAgent: ""}); !errors.Is(err, contracts.ErrInvalidCode) {
		t.Fatal(err)
	}
	// What the mail system answered is not logged: it names the address.
	if !strings.Contains(w.logs.String(), "could not be sent") || strings.Contains(w.logs.String(), "ana@") {
		t.Fatalf("logs:\n%s", w.logs)
	}
}

func TestInvitations(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	for _, n := range []int{0, -1, MaxInvitations + 1} {
		if _, err := w.svc.CreateInvitations(ctx, n); err == nil {
			t.Errorf("%d invitations were made", n)
		}
	}
	inv, err := w.svc.CreateInvitations(ctx, 3)
	if err != nil || len(inv.Invitations) != 3 || !inv.ExpiresAt.Equal(w.now.Add(domain.InvitationLifetime)) || inv.Invitations[0] == inv.Invitations[1] {
		t.Fatalf("invitations: %+v %v", inv, err)
	}

	// Two people with the same invitation: only one gets the account.
	first, mail1 := w.start("ana@example.org", inv.Invitations[0])
	second, mail2 := w.start("eve@example.org", inv.Invitations[0])
	if _, _, err := w.svc.FinishEmail(ctx, contracts.EmailFinish{Attempt: first, Code: code(mail1)}, Browser{UserAgent: ""}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.svc.FinishEmail(ctx, contracts.EmailFinish{Attempt: second, Code: code(mail2)}, Browser{UserAgent: ""}); !errors.Is(err, contracts.ErrInvalidInvitation) {
		t.Fatalf("the second use of an invitation: %v", err)
	}
	// Someone with an account who types an invitation keeps it unused.
	attempt, mail := w.start("ana@example.org", inv.Invitations[1])
	if _, _, err := w.svc.FinishEmail(ctx, contracts.EmailFinish{Attempt: attempt, Code: code(mail)}, Browser{UserAgent: ""}); err != nil {
		t.Fatal(err)
	}
	if _, mail := w.start("eve@example.org", inv.Invitations[1]); mail == nil {
		t.Fatal("an invitation was used up by someone who had an account")
	}

	// An invitation ends after thirty days, whatever its entry says: the
	// entry's own expiry can be written by hand.
	normalized, _ := domain.NormalizeInvitation(inv.Invitations[2])
	e, _ := w.mem.GetEntry(ctx, invitationPartition(normalized), invitationKey)
	e.ExpiresAt = time.Time{}
	w.mem.SetEntry(*e)
	w.now = w.now.Add(domain.InvitationLifetime)
	if _, err := w.svc.StartEmail(ctx, contracts.EmailStart{Address: "eve@example.org", Invitation: inv.Invitations[2]}); !errors.Is(err, contracts.ErrInvalidInvitation) {
		t.Fatalf("an invitation after thirty days: %v", err)
	}
}

func TestSessions(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	me, cookie := w.signUp("ana@example.org")
	start, day := w.now, 24*time.Hour

	// Each use, at most once an hour, gives seven more days.
	w.now = start.Add(30 * time.Minute)
	if who, _ := w.svc.Authenticate(ctx, cookie); who == nil || !who.Session.LastUsedAt.Equal(start) {
		t.Fatalf("used half an hour later: %+v", who)
	}
	w.now = start.Add(6 * day)
	who, err := w.svc.Authenticate(ctx, cookie)
	if err != nil || !who.Session.LastUsedAt.Equal(w.now) || !who.Session.ExpiresAt.Equal(w.now.Add(domain.SessionIdle)) {
		t.Fatalf("used six days later: %+v %v", who, err)
	}
	// Seven days without use end it.
	w.now = start.Add(13*day + time.Second)
	if _, err := w.svc.Authenticate(ctx, cookie); !errors.Is(err, contracts.ErrUnauthorized) {
		t.Fatalf("seven days without use: %v", err)
	}

	// Used every day, it still ends at thirty.
	w.now = start.Add(14 * day)
	attempt, mail := w.start("ana@example.org", "")
	_, cookie, err = w.svc.FinishEmail(ctx, contracts.EmailFinish{Attempt: attempt, Code: code(mail)}, Browser{UserAgent: "laptop"})
	if err != nil {
		t.Fatal(err)
	}
	made := w.now
	for d := 1; d < 30; d++ {
		w.now = made.Add(time.Duration(d) * day)
		if _, err := w.svc.Authenticate(ctx, cookie); err != nil {
			t.Fatalf("day %d: %v", d, err)
		}
	}
	w.now = made.Add(30 * day)
	if _, err := w.svc.Authenticate(ctx, cookie); !errors.Is(err, contracts.ErrUnauthorized) {
		t.Fatalf("thirty days: %v", err)
	}

	// Listed, the one last used first, and closed one by one.
	attempt, mail = w.start("ana@example.org", "")
	_, phone, _ := w.svc.FinishEmail(ctx, contracts.EmailFinish{Attempt: attempt, Code: code(mail)}, Browser{UserAgent: "phone"})
	w.now = w.now.Add(2 * time.Hour)
	attempt, mail = w.start("ana@example.org", "")
	_, laptop, _ := w.svc.FinishEmail(ctx, contracts.EmailFinish{Attempt: attempt, Code: code(mail)}, Browser{UserAgent: "laptop"})
	who, _ = w.svc.Authenticate(ctx, laptop)
	list, err := w.svc.Sessions(ctx, who)
	if err != nil || len(list.Sessions) != 2 || list.Sessions[0].Device != "laptop" || !list.Sessions[0].Current || list.Sessions[1].Device != "phone" || list.Sessions[1].Current {
		t.Fatalf("sessions: %+v %v", list, err)
	}
	if err := w.svc.CloseSession(ctx, who, list.Sessions[1].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.Authenticate(ctx, phone); !errors.Is(err, contracts.ErrUnauthorized) {
		t.Fatalf("a closed session: %v", err)
	}
	if err := w.svc.CloseSession(ctx, who, list.Sessions[1].ID); !errors.Is(err, contracts.ErrNotFound) {
		t.Fatalf("closing it again: %v", err)
	}
	if err := w.svc.CloseSession(ctx, who, "../"+list.Sessions[0].ID); !errors.Is(err, contracts.ErrNotFound) {
		t.Fatalf("closing something that is not a session: %v", err)
	}
	// Nobody closes a session of another account.
	other, otherCookie := w.signUp("eve@example.org")
	eve, _ := w.svc.Authenticate(ctx, otherCookie)
	if err := w.svc.CloseSession(ctx, eve, who.Session.ID); !errors.Is(err, contracts.ErrNotFound) {
		t.Fatalf("closing another account's session: %v", err)
	}
	if _, err := w.svc.Authenticate(ctx, laptop); err != nil || other.Account.ID == me.Account.ID {
		t.Fatalf("the session someone else tried to close: %v", err)
	}

	for _, bad := range []string{"", "a.b.c", laptop + ".x", strings.Replace(laptop, "acc_", "ACC_", 1), laptop[:len(laptop)-1] + "!", me.Account.ID + "." + who.Session.ID + "."} {
		if _, err := w.svc.Authenticate(ctx, bad); !errors.Is(err, contracts.ErrUnauthorized) {
			t.Errorf("cookie %q: %v", bad, err)
		}
	}
}

func TestSessionsAreBounded(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	me, first := w.signUp("ana@example.org")
	for range maxSessions {
		w.now = w.now.Add(time.Minute)
		if _, _, err := w.svc.newSession(ctx, me.Account.ID, "device"); err != nil {
			t.Fatal(err)
		}
	}
	list, _ := w.svc.sessions(ctx, me.Account.ID)
	if len(list) != maxSessions {
		t.Fatalf("%d sessions kept", len(list))
	}
	if _, err := w.svc.Authenticate(ctx, first); !errors.Is(err, contracts.ErrUnauthorized) {
		t.Fatalf("the session used longest ago: %v", err)
	}
}

// Whoever runs the storage reads the table and writes to it. None of what
// they can write lets them into an account.
func TestStorageWrittenByHand(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	ana, anaCookie := w.signUp("ana@example.org")
	eve, eveCookie := w.signUp("eve@example.org")
	anaSession, _ := w.svc.Authenticate(ctx, anaCookie)
	eveSession, _ := w.svc.Authenticate(ctx, eveCookie)
	eveSecret := eveCookie[strings.LastIndex(eveCookie, ".")+1:]

	// Eve's session copied into Ana's account, under its name or Ana's.
	entry, _ := w.mem.GetEntry(ctx, accountPartition(eve.Account.ID), sessionPrefix+eveSession.Session.ID)
	for _, key := range []string{sessionPrefix + eveSession.Session.ID, sessionPrefix + anaSession.Session.ID} {
		moved := *entry
		moved.Partition, moved.Key = accountPartition(ana.Account.ID), key
		w.mem.SetEntry(moved)
		id := strings.TrimPrefix(key, sessionPrefix)
		if _, err := w.svc.Authenticate(ctx, ana.Account.ID+"."+id+"."+eveSecret); !errors.Is(err, contracts.ErrUnauthorized) {
			t.Fatalf("a session moved to another account as %s: %v", key, err)
		}
	}
	// A session written without the enclave's key, for a secret its writer
	// knows: the empty one.
	empty := sha256.Sum256(nil)
	forged := *entry
	forged.Partition, forged.Key = accountPartition(ana.Account.ID), sessionPrefix+"ses_"+strings.Repeat("a", 26)
	forged.Value = []byte(`{"doc":{"v":1,"account":"` + ana.Account.ID + `","id":"ses_` + strings.Repeat("a", 26) + `","token_sha256":"` + base64.StdEncoding.EncodeToString(empty[:]) + `","created_at":"2026-11-11T09:00:00Z","last_used_at":"2026-11-11T09:00:00Z"},"mac":"AAAA"}`)
	w.mem.SetEntry(forged)
	if _, err := w.svc.Authenticate(ctx, ana.Account.ID+".ses_"+strings.Repeat("a", 26)+"."); !errors.Is(err, contracts.ErrUnauthorized) {
		t.Fatalf("a session written by hand: %v", err)
	}
	if list, _ := w.svc.Sessions(ctx, anaSession); len(list.Sessions) != 0 {
		// Ana's own entry was overwritten above; nothing written by hand is listed.
		t.Fatalf("sessions written by hand are listed: %+v", list)
	}
	// A session's life is the signed one: an entry made to never expire ends anyway.
	live, _ := w.mem.GetEntry(ctx, accountPartition(eve.Account.ID), sessionPrefix+eveSession.Session.ID)
	live.ExpiresAt = time.Time{}
	w.mem.SetEntry(*live)
	w.now = w.now.Add(domain.SessionIdle)
	if _, err := w.svc.Authenticate(ctx, eveCookie); !errors.Is(err, contracts.ErrUnauthorized) {
		t.Fatalf("a session whose entry was made to last: %v", err)
	}

	// Eve's address pointed at Ana's account: with Ana's own pointer copied,
	// or with one written by hand. Eve's sign-in sends nothing, and never
	// leads into Ana's account.
	pointer, _ := w.mem.GetEntry(ctx, loginPartition("ana@example.org"), loginKey)
	for _, value := range [][]byte{pointer.Value, []byte(`{"doc":{"account":"` + ana.Account.ID + `"},"mac":"AAAA"}`)} {
		w.mem.SetEntry(store.Entry{Partition: loginPartition("eve@example.org"), Key: loginKey, Value: value, Version: 7})
		if _, mail := w.start("eve@example.org", ""); mail != nil {
			t.Fatal("a pointer written by hand sent a sign-in message")
		}
	}
	// With an invitation Eve signs up again, into an account of her own.
	w.now = w.now.Add(2 * time.Hour)
	again, _ := w.signUp("eve@example.org")
	if again.Account.ID == ana.Account.ID || again.Account.ID == eve.Account.ID || again.Account.Address != "eve@example.org" {
		t.Fatalf("signing up over a pointer written by hand: %+v", again.Account)
	}

	// An account's record written by hand, with another address: the account
	// is not read at all, and no sign-in is sent for either address.
	var name string
	for _, n := range w.mem.RecordNames() {
		if strings.HasPrefix(n, "account-"+ana.Account.ID+"@") {
			name = n
		}
	}
	rec, _ := w.mem.GetRecord(ctx, name)
	changed := *rec
	changed.Value = bytes.Replace(rec.Value, []byte("ana@example.org"), []byte("eve@example.net"), 1)
	w.mem.SetRecord(changed)
	if _, err := w.svc.Account(ctx, ana.Account.ID); err == nil || errors.Is(err, contracts.ErrNotFound) {
		t.Fatalf("a record changed by hand: %v", err)
	}
	w.mem.SetEntry(store.Entry{Partition: loginPartition("eve@example.net"), Key: loginKey, Value: pointer.Value, Version: 1})
	for _, address := range []string{"ana@example.org", "eve@example.net"} {
		if _, mail := w.start(address, ""); mail != nil {
			t.Fatalf("a record changed by hand sent a sign-in message to %s", address)
		}
	}
	// Not even with an invitation is the address given a new account: the
	// one it has cannot be trusted, which is not the same as having none.
	if _, mail := w.start("ana@example.org", w.invite()); mail != nil {
		t.Fatal("an address whose account cannot be read was let to sign up again")
	}
	// The same when the record is deleted: its commit says there was one.
	_ = w.mem.DeleteRecord(ctx, name)
	if _, err := w.svc.Account(ctx, ana.Account.ID); !errors.Is(err, store.ErrLost) {
		t.Fatalf("a deleted record: %v", err)
	}
	if _, mail := w.start("ana@example.org", w.invite()); mail != nil {
		t.Fatal("an address whose account was deleted by hand was let to sign up again")
	}
	if _, err := w.svc.Me(ctx, anaSession); err == nil {
		t.Fatal("a session of an account that cannot be read")
	}
}

func TestAnotherDeployment(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	me, cookie := w.signUp("ana@example.org")
	// The same storage under an enclave with another root secret: nothing
	// verifies, and nothing is taken for missing.
	local, _ := keys.NewLocal()
	other := w.service(local)
	if _, err := other.Authenticate(ctx, cookie); !errors.Is(err, contracts.ErrUnauthorized) {
		t.Fatalf("a session of another deployment: %v", err)
	}
	if _, err := other.Account(ctx, me.Account.ID); err == nil || errors.Is(err, contracts.ErrNotFound) {
		t.Fatalf("an account of another deployment: %v", err)
	}
}

func TestMissing(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	if _, err := New(Config{}); err == nil {
		t.Fatal("a service without anything")
	}
	if svc, err := New(Config{Records: w.svc.cfg.Records, Entries: w.mem, Keys: w.svc.cfg.Keys, Mailer: w.out, AppURL: "https://app"}); err != nil || svc.now().IsZero() {
		t.Fatalf("defaults: %v", err)
	}
	for _, id := range []string{"", "acc_x", "acc_" + strings.Repeat("a", 26), "ses_" + strings.Repeat("a", 26), "acc_" + strings.Repeat("1", 26)} {
		if _, err := w.svc.Account(ctx, id); !errors.Is(err, contracts.ErrNotFound) {
			t.Errorf("account %q: %v", id, err)
		}
	}
	// A session whose account is not there.
	ghost := &Authenticated{AccountID: "acc_" + strings.Repeat("a", 26)}
	if _, err := w.svc.Me(ctx, ghost); !errors.Is(err, contracts.ErrUnauthorized) {
		t.Fatalf("me without an account: %v", err)
	}
}

// failing is storage and keys that fail on demand.
type failing struct {
	Records
	store.Entries
	Keys
	fail map[string]error
	// skip is how many times an operation still works before it fails.
	skip map[string]int
}

func (f *failing) err(op string) error {
	if f.skip[op] > 0 {
		f.skip[op]--
		return nil
	}
	return f.fail[op]
}

func (f *failing) GetRecord(ctx context.Context, name string) (*store.Record, error) {
	if err := f.err("get record"); err != nil {
		return nil, err
	}
	return f.Records.GetRecord(ctx, name)
}

func (f *failing) PutRecord(ctx context.Context, r *store.Record, create bool) error {
	if err := f.err("put record"); err != nil {
		return err
	}
	return f.Records.PutRecord(ctx, r, create)
}

func (f *failing) GetRecordAt(ctx context.Context, name string) (*store.Record, store.Mark, error) {
	if err := f.err("get record"); err != nil {
		return nil, store.Mark{}, err
	}
	return f.Records.GetRecordAt(ctx, name)
}

func (f *failing) PutRecordAt(ctx context.Context, r *store.Record, at store.Mark) error {
	if err := f.err("put record"); err != nil {
		return err
	}
	return f.Records.PutRecordAt(ctx, r, at)
}

func (f *failing) GetEntry(ctx context.Context, partition, key string) (*store.Entry, error) {
	if err := f.err("get entry"); err != nil {
		return nil, err
	}
	if err := f.err("get " + partition); err != nil {
		return nil, err
	}
	return f.Entries.GetEntry(ctx, partition, key)
}

func (f *failing) PutEntry(ctx context.Context, e *store.Entry) error {
	if err := f.err("put entry"); err != nil {
		return err
	}
	if err := f.err("put " + e.Key); err != nil {
		delete(f.fail, "put "+e.Key)
		return err
	}
	if err := f.err("put " + e.Partition); err != nil {
		return err
	}
	return f.Entries.PutEntry(ctx, e)
}

func (f *failing) DeleteEntry(ctx context.Context, partition, key string) error {
	if err := f.err("delete entry"); err != nil {
		return err
	}
	if err := f.err("delete " + partition); err != nil {
		return err
	}
	return f.Entries.DeleteEntry(ctx, partition, key)
}

func (f *failing) DeleteEntryAt(ctx context.Context, e *store.Entry) error {
	if err := f.err("delete entry"); err != nil {
		return err
	}
	if err := f.err("delete " + e.Partition); err != nil {
		return err
	}
	return f.Entries.DeleteEntryAt(ctx, e)
}

func (f *failing) ListEntries(ctx context.Context, partition, prefix, after string, limit int) ([]store.Entry, error) {
	if err := f.err("list entries"); err != nil {
		return nil, err
	}
	if err := f.err("list " + partition); err != nil {
		return nil, err
	}
	return f.Entries.ListEntries(ctx, partition, prefix, after, limit)
}

func (f *failing) DeletePartition(ctx context.Context, partition string) error {
	if err := f.err("delete partition"); err != nil {
		return err
	}
	return f.Entries.DeletePartition(ctx, partition)
}

func (f *failing) RemoveRecord(ctx context.Context, name string) error {
	if err := f.err("remove record"); err != nil {
		return err
	}
	return f.Records.RemoveRecord(ctx, name)
}

func (f *failing) Derive(ctx context.Context, purpose string) ([]byte, error) {
	if err := f.err("derive"); err != nil {
		return nil, err
	}
	return f.Keys.Derive(ctx, purpose)
}

// Storage or keys that fail are an error, never a reason to let someone in
// or to make an account again.
func TestFailures(t *testing.T) {
	down := errors.New("down")
	setup := func(t *testing.T) (*world, *failing) {
		w := newWorld(t)
		f := &failing{Records: w.svc.cfg.Records, Entries: w.mem, Keys: w.svc.cfg.Keys, fail: map[string]error{}}
		w.svc.cfg.Records, w.svc.cfg.Entries, w.svc.cfg.Keys = f, f, f
		return w, f
	}
	ctx := context.Background()

	for _, op := range []string{"get record", "get entry", "derive"} {
		w, f := setup(t)
		me, cookie := w.signUp("ana@example.org")
		who, _ := w.svc.Authenticate(ctx, cookie)
		invitation := w.invite()
		f.fail[op] = down
		if _, mail := w.start("ana@example.org", ""); mail != nil {
			t.Errorf("%s down: a sign-in message was sent", op)
		}
		if _, err := w.svc.Me(ctx, who); !errors.Is(err, down) && op != "get entry" {
			t.Errorf("%s down: me: %v", op, err)
		}
		if _, err := w.svc.Account(ctx, me.Account.ID); !errors.Is(err, down) && op != "get entry" {
			t.Errorf("%s down: account: %v", op, err)
		}
		if op != "get record" {
			if _, err := w.svc.Authenticate(ctx, cookie); !errors.Is(err, down) {
				t.Errorf("%s down: authenticate: %v", op, err)
			}
			if _, err := w.svc.StartEmail(ctx, contracts.EmailStart{Address: "eve@example.org", Invitation: invitation}); !errors.Is(err, down) {
				t.Errorf("%s down: start with an invitation: %v", op, err)
			}
		}
		if op == "derive" {
			if _, err := w.svc.CreateInvitations(ctx, 1); !errors.Is(err, down) {
				t.Errorf("derive down: invitations: %v", err)
			}
			if _, err := w.svc.Sessions(ctx, who); !errors.Is(err, down) {
				t.Errorf("derive down: sessions: %v", err)
			}
		}
	}

	// Each step of ending a sign-in.
	for _, op := range []string{"get entry", "get record", "delete entry", "put record", "put entry", "list entries", "derive"} {
		w, f := setup(t)
		attempt, mail := w.start("ana@example.org", w.invite())
		f.fail[op] = down
		if _, _, err := w.svc.FinishEmail(ctx, contracts.EmailFinish{Attempt: attempt, Code: code(mail)}, Browser{UserAgent: ""}); !errors.Is(err, down) && op != "get record" {
			t.Errorf("%s down: finish: %v", op, err)
		}
		if op == "put entry" {
			if _, err := w.svc.CreateInvitations(ctx, 1); !errors.Is(err, down) {
				t.Errorf("put entry down: invitations: %v", err)
			}
		}
	}
	w, f := setup(t)
	_, cookie := w.signUp("ana@example.org")
	who, _ := w.svc.Authenticate(ctx, cookie)
	f.fail["list entries"] = down
	if _, err := w.svc.Sessions(ctx, who); !errors.Is(err, down) {
		t.Errorf("list down: sessions: %v", err)
	}
	f.fail = map[string]error{"delete entry": down}
	if err := w.svc.CloseSession(ctx, who, who.Session.ID); !errors.Is(err, down) {
		t.Errorf("delete down: close: %v", err)
	}
	// Writing the time of last use fails: the request fails, unless another
	// request of the session wrote it first.
	w.now = w.now.Add(2 * time.Hour)
	f.fail = map[string]error{"put entry": down}
	if _, err := w.svc.Authenticate(ctx, cookie); !errors.Is(err, down) {
		t.Errorf("put down: authenticate: %v", err)
	}
	f.fail = map[string]error{"put entry": store.ErrConflict}
	if _, err := w.svc.Authenticate(ctx, cookie); err != nil {
		t.Errorf("another request wrote first: %v", err)
	}
}

// Two sign-ups of one address that end at the same time make one account.
func TestSignUpRace(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	f := &failing{Records: w.svc.cfg.Records, Entries: w.mem, Keys: w.svc.cfg.Keys, fail: map[string]error{}}
	w.svc.cfg.Entries = f
	first, mail1 := w.start("ana@example.org", w.invite())
	second, mail2 := w.start("ana@example.org", w.invite())

	// The second finds no account, and by the time it writes the pointer
	// the first has written its own.
	racing := &racer{failing: f, w: w, first: contracts.EmailFinish{Attempt: first, Code: code(mail1)}}
	w.svc.cfg.Entries = racing
	loser, _, err := w.svc.FinishEmail(ctx, contracts.EmailFinish{Attempt: second, Code: code(mail2)}, Browser{UserAgent: ""})
	if err != nil || racing.winner == nil || loser.Account.ID != racing.winner.Account.ID {
		t.Fatalf("the sign-up that ended second: %+v, the first %+v, %v", loser, racing.winner, err)
	}

	// A pointer that keeps failing to be written leaves an error, not a
	// session on an account nothing leads to.
	w.svc.cfg.Entries = f
	attempt, mail := w.start("eve@example.org", w.invite())
	f.fail["put "+loginKey] = store.ErrExists
	if _, _, err := w.svc.FinishEmail(ctx, contracts.EmailFinish{Attempt: attempt, Code: code(mail)}, Browser{UserAgent: ""}); err == nil {
		t.Fatal("signed in to an account no address leads to")
	}
}

// racer ends another sign-up of the same address just before the pointer of
// this one is written.
type racer struct {
	*failing
	w      *world
	first  contracts.EmailFinish
	winner *contracts.Me
}

func (r *racer) PutEntry(ctx context.Context, e *store.Entry) error {
	if e.Key == loginKey && r.winner == nil {
		r.w.svc.cfg.Entries = r.failing
		me, _, err := r.w.svc.FinishEmail(ctx, r.first, Browser{UserAgent: ""})
		if err != nil {
			r.w.t.Fatal(err)
		}
		r.winner = me
	}
	return r.failing.PutEntry(ctx, e)
}

func TestTooMuchAtOnce(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	w.signUp("ana@example.org")
	// Every slot for sending is taken: the answer is the same, nothing is sent.
	for range maxSending {
		w.svc.sending <- struct{}{}
	}
	if _, mail := w.start("ana@example.org", ""); mail != nil {
		t.Fatal("a message beyond what is sent at once")
	}
	for range maxSending {
		<-w.svc.sending
	}
	// As many sign-ins waiting as the enclave keeps: one more is dropped.
	for i := range maxAttempts {
		w.svc.attempts[string(rune(i))] = &attempt{expires: w.now.Add(time.Hour)}
	}
	if _, mail := w.start("ana@example.org", ""); mail != nil {
		t.Fatal("a sign-in beyond what is kept")
	}
	// What ended is forgotten.
	w.now = w.now.Add(2 * time.Hour)
	if _, mail := w.start("ana@example.org", ""); mail == nil || len(w.svc.attempts) != 1 {
		t.Fatalf("after the others ended: %d kept", len(w.svc.attempts))
	}
	w.now = w.now.Add(25 * time.Hour)
	w.svc.mu.Lock()
	w.svc.sweep(w.now)
	w.svc.mu.Unlock()
	if len(w.svc.attempts) != 0 || len(w.svc.starts) != 0 || len(w.svc.wrong) != 0 {
		t.Fatalf("kept after a day: %d sign-ins, %d starts, %d wrong", len(w.svc.attempts), len(w.svc.starts), len(w.svc.wrong))
	}
	if _, err := w.svc.StartEmail(ctx, contracts.EmailStart{Address: "ana@example.org"}); err != nil {
		t.Fatal(err)
	}
	w.svc.Wait()
}
