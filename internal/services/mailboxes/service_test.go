package mailboxes_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/emersion/go-msgauth/dkim"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/envelope"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/keys"
	"github.com/permaily-org/permaily/internal/msgauth"
	"github.com/permaily-org/permaily/internal/services/mailboxes"
	"github.com/permaily-org/permaily/internal/store"
)

func (h *hooked) GetRecordAt(ctx context.Context, name string) (*store.Record, store.Mark, error) {
	if err := h.hook("get", name); err != nil {
		return nil, store.Mark{}, err
	}
	return h.Anchored.GetRecordAt(ctx, name)
}

var googlePub, googleKey, _ = ed25519.GenerateKey(rand.Reader)

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

// M15: two confirmations signed by Google arrive through A and B at once for
// a waiting mailbox, with the mark on: one verified_at, one notice.
func TestM15TwoVerifiedConfirmationsAtOnce(t *testing.T) {
	inbox.VerifyGmail = true
	t.Cleanup(func() { inbox.VerifyGmail = false })
	w := newWorld(t)
	w.auth = googleDKIM()
	a, b := w.enclave(), w.enclave()
	me := caller("acc_me", w.clock)
	m := a.mustCreate(t, me)
	var errB error
	var once onlyOnce
	a.records.set(func(op, name string) error {
		if op == "put" && isMailbox(name) {
			once.Do(func() { errB = b.deliver(t, googleSigned(t, m.Address, "second@gmail.com"), m.Address) })
		}
		return nil
	})
	if err := a.deliver(t, googleSigned(t, m.Address, "first@gmail.com"), m.Address); err != nil {
		t.Fatal(err)
	}
	if errB != nil {
		t.Fatal(errB)
	}
	if n := count(w.kinds(), mailboxes.NoticeForwarding); n != 1 {
		t.Fatalf("%d notices", n)
	}
	got, _ := a.Get(ctx, me, m.ID)
	if got.Source == nil || got.Source.Address != "second@gmail.com" || got.State != contracts.MailboxWaiting || len(got.Confirmations) != 2 || !got.Confirmations[0].Verified {
		t.Fatalf("%+v", got)
	}
	if r, _ := a.Record(ctx, m.ID); !r.Verified {
		t.Fatal("the operator does not see it verified")
	}
	// A genuine one for another address is shown without its code.
	w.clock.add(time.Second)
	other := googleSigned(t, "someone@elsewhere.example", "x@gmail.com")
	if err := a.deliver(t, other, m.Address); err != nil {
		t.Fatal(err)
	}
	got, _ = a.Get(ctx, me, m.ID)
	if c := got.Confirmations[0]; !c.ForAnotherAddress || c.Code != "" || c.Verified {
		t.Fatalf("%+v", c)
	}
	// Mail that is not a confirmation starts the mailbox receiving; a
	// confirmation after it changes nothing.
	if err := a.deliver(t, message("first mail"), m.Address); err != nil {
		t.Fatal(err)
	}
	if err := b.deliver(t, googleSigned(t, m.Address, "third@gmail.com"), m.Address); err != nil {
		t.Fatal(err)
	}
	got, _ = b.Get(ctx, me, m.ID)
	if got.State != contracts.MailboxReceiving || got.Source.Address != "second@gmail.com" {
		t.Fatalf("%+v", got)
	}
}

// The log leaves the enclave: after the main flows it holds no address,
// subject, sender, content, token, or account, mailbox or message
// identifier.
func TestTheLogHoldsNothingOfAnyone(t *testing.T) {
	w := newWorld(t)
	a, b := w.enclave(), w.enclave()
	me := caller("acc_kpq2yvnbsthk7o5xdq3icxfaxe", w.clock)
	m := a.mustCreate(t, me)
	n := b.mustCreate(t, me)
	_ = a.deliver(t, message("Secret subject <script>"), m.Address, n.Address)
	_ = a.deliver(t, message("Another"), "nobody@"+domain)
	w.setInterrupt(func(_ context.Context, step store.Step, _, _ string) error {
		if step == store.StepItem {
			return fmt.Errorf("the store failed for msg#%s", m.ID)
		}
		return nil
	})
	_ = b.deliver(t, message("Fails"), m.Address)
	w.setInterrupt(nil)
	_, _ = a.Pause(ctx, n.ID)
	_ = a.Disconnect(ctx, me, m.ID)
	w.clock.add(mailboxes.SweepGrace)
	sweepAll(t, b)
	_ = a.RemoveAccount(ctx, me.Account)
	msgs := w.messages(n.ID)
	logs := w.logs.String()
	if logs == "" {
		t.Fatal("nothing was logged")
	}
	secrets := []string{m.Address, n.Address, m.ID, n.ID, me.Account, "alice", "example.org", "Secret", "Hello", "gmail", "script"}
	for _, msg := range msgs {
		secrets = append(secrets, msg.ID)
	}
	for _, s := range secrets {
		if strings.Contains(logs, s) {
			t.Errorf("the log holds %q:\n%s", s, logs)
		}
	}
}

func TestNew(t *testing.T) {
	if _, err := mailboxes.New(mailboxes.Config{}); err == nil {
		t.Fatal("made without what it needs")
	}
	w := newWorld(t)
	s, err := mailboxes.New(mailboxes.Config{Records: &store.Anchored{Records: w.mem, Commits: w.commits, Strict: true}, Storage: w.mem, Keys: w.keys, Accounts: w.accounts, Domain: "INBOX.example"})
	if err != nil {
		t.Fatal(err)
	}
	m, err := s.Create(ctx, caller("acc_me", w.clock), contracts.MailboxCreate{})
	if err != nil || !strings.HasSuffix(m.Address, "@inbox.example") {
		t.Fatalf("%+v %v", m, err)
	}
	// Mail is checked by no authenticator, and stored all the same.
	id, err := s.Accept(ctx, strings.ToUpper(m.Address))
	if err != nil || s.Deliver(ctx, "a@example.org", []string{id, id}, message("x")) != nil {
		t.Fatal(err)
	}
	// Run sweeps until its context ends.
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { s.Run(runCtx); close(done) }()
	cancel()
	<-done
}

func TestStandingAndAccess(t *testing.T) {
	w := newWorld(t)
	a := w.enclave()
	if _, err := a.List(ctx, mailboxes.Caller{}); !errors.Is(err, contracts.ErrUnauthorized) {
		t.Fatal(err)
	}
	me := caller("acc_me", w.clock)
	m := a.mustCreate(t, me)
	if _, err := a.Open(ctx, me, m.ID, mailboxes.Access(99)); !errors.Is(err, contracts.ErrNotFound) {
		t.Fatal(err)
	}
	for _, id := range []string{"", "mbx_", "mbx_" + strings.Repeat("1", 26), "acc_" + m.ID[4:]} {
		if _, err := a.Get(ctx, me, id); !errors.Is(err, contracts.ErrNotFound) {
			t.Errorf("%q: %v", id, err)
		}
		if err := a.Disconnect(ctx, me, id); !errors.Is(err, contracts.ErrNotFound) {
			t.Errorf("%q: %v", id, err)
		}
		if _, err := a.Record(ctx, id); !errors.Is(err, contracts.ErrNotFound) {
			t.Errorf("%q: %v", id, err)
		}
		if _, err := a.Pause(ctx, id); !errors.Is(err, contracts.ErrNotFound) {
			t.Errorf("%q: %v", id, err)
		}
	}
	// Ten changes of the settings a minute in one enclave.
	label := "x"
	for range 10 {
		if _, err := a.Update(ctx, me, m.ID, contracts.MailboxUpdate{Label: &label}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := a.Update(ctx, me, m.ID, contracts.MailboxUpdate{Label: &label}); !errors.Is(err, contracts.ErrTooManyRequests) {
		t.Fatal(err)
	}
	w.clock.add(time.Minute)
	if _, err := a.Update(ctx, caller("acc_me", w.clock), m.ID, contracts.MailboxUpdate{}); err != nil {
		t.Fatal(err)
	}
	empty := ""
	if got, err := a.Update(ctx, caller("acc_me", w.clock), m.ID, contracts.MailboxUpdate{Label: &empty}); err != nil || got.Label != "" {
		t.Fatal(got, err)
	}
	// Addresses that lead nowhere.
	for _, addr := range []string{"nobody@" + domain, m.Address + ".other", "x+tag@" + domain, "not an address"} {
		if _, err := a.Accept(ctx, addr); !errors.Is(err, mailboxes.ErrNoSuchMailbox) {
			t.Errorf("%q: %v", addr, err)
		}
		if l, err := a.ByAddress(ctx, addr); err != nil || len(l.Mailboxes) != 0 {
			t.Errorf("%q: %v", addr, err)
		}
	}
}

// The limits of one enclave on the mail one mailbox takes.
func TestMailLimits(t *testing.T) {
	w := newWorld(t)
	a := w.enclave()
	m := a.mustCreate(t, caller("acc_me", w.clock))
	for i := range mailboxes.MaxMessagesPerHour {
		if err := a.deliver(t, message(fmt.Sprint(i)), m.Address); err != nil {
			t.Fatal(i, err)
		}
	}
	if _, err := a.Accept(ctx, m.Address); !errors.Is(err, mailboxes.ErrSlowDown) {
		t.Fatal(err)
	}
	w.clock.add(time.Hour)
	if _, err := a.Accept(ctx, m.Address); err != nil {
		t.Fatal(err)
	}
	w.clock.add(24 * time.Hour)
	if _, err := a.Accept(ctx, m.Address); err != nil {
		t.Fatal(err)
	}
}

// A reservation can be given up: a pointer that is another mailbox's, and a
// reservation the person disconnects.
func TestReservations(t *testing.T) {
	w := newWorld(t)
	a := w.enclave()
	me := caller("acc_me", w.clock)
	taken := &takenPointers{Memory: w.mem}
	s, err := mailboxes.New(mailboxes.Config{Records: a.records, Storage: taken, Keys: w.keys, Accounts: w.accounts, Domain: domain, Now: w.clock.now})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Create(ctx, me, contracts.MailboxCreate{}); err == nil {
		t.Fatal("made with another mailbox's address")
	}
	if n, _ := s.Count(ctx, me.Account); n != 0 {
		t.Fatal("the reservation stays")
	}
	// A reservation stopped before its record, disconnected by its person.
	_, goOn, done := reserveAndStop(t, a, me)
	if w.pointers() != 1 {
		t.Fatal("no pointer for the reservation")
	}
	b := w.enclave()
	if n, _ := b.Count(ctx, me.Account); n != 1 {
		t.Fatal("a young reservation does not count")
	}
	pending := reservedID(t, w, me.Account)
	if err := b.Disconnect(ctx, mailboxes.Caller{Account: me.Account}, pending); !errors.Is(err, contracts.ErrConfirmationNeeded) {
		t.Fatal(err)
	}
	if err := b.Disconnect(ctx, me, pending); err != nil {
		t.Fatal(err)
	}
	close(goOn)
	if err := <-done; err == nil {
		t.Fatal("made after it was given up")
	}
	if n, _ := b.Count(ctx, me.Account); n != 0 || w.pointers() != 0 {
		t.Fatal("left something")
	}
}

// reservedID is the identifier an account's list holds without a record.
func reservedID(t *testing.T, w *world, account string) string {
	t.Helper()
	for _, n := range w.mem.RecordNames() {
		if !strings.HasPrefix(n, "mailboxes-"+account+"@") {
			continue
		}
		r, _ := w.mem.GetRecord(ctx, n)
		i := bytes.Index(r.Value, []byte(`"id":"`))
		if i >= 0 {
			return string(r.Value[i+6 : i+6+30])
		}
	}
	t.Fatal("no reservation")
	return ""
}

// takenPointers says every address is another mailbox's already.
type takenPointers struct{ *store.Memory }

func (p *takenPointers) PutEntry(ctx context.Context, e *store.Entry) error {
	if strings.HasPrefix(e.Partition, "rcpt#") {
		return store.ErrExists
	}
	return p.Memory.PutEntry(ctx, e)
}

// What a removal ends: a reservation, a lost record and one gone already.
func TestRemoveAccountEndsEverything(t *testing.T) {
	w := newWorld(t)
	a, b := w.enclave(), w.enclave()
	me := caller("acc_me", w.clock)
	lost, gone := a.mustCreate(t, me), a.mustCreate(t, me)
	w.lose(lost.ID)
	// gone: removed, its identifier still in the list (a disconnect that
	// stopped after the removal).
	a.records.set(func(op, name string) error {
		if op == "put" && isList(name) {
			return errStopped
		}
		return nil
	})
	w.lose(gone.ID)
	if err := a.Disconnect(ctx, me, gone.ID); !errors.Is(err, errStopped) {
		t.Fatal(err)
	}
	a.records.set(nil)
	_, goOn, done := reserveAndStop(t, a, me)
	if err := b.RemoveAccount(ctx, me.Account); err != nil {
		t.Fatal(err)
	}
	close(goOn)
	if err := <-done; err == nil {
		t.Fatal("a mailbox was made for an account that is being removed")
	}
	if r, _ := b.Records(ctx, me.Account); len(r.Mailboxes) != 0 {
		t.Fatalf("%+v", r)
	}
	w.clock.add(mailboxes.SweepGrace)
	sweepAll(t, b)
	if n, _ := b.Total(ctx); n != 0 {
		t.Fatal(n)
	}
}

// What the sweep does with each record a disconnect entry names.
func TestSweepCases(t *testing.T) {
	w := newWorld(t)
	a := w.enclave()
	me := caller("acc_me", w.clock)
	active, forked, lost := a.mustCreate(t, me), a.mustCreate(t, me), a.mustCreate(t, me)
	key := func(id string) string { return w.clock.now().Format(time.RFC3339) + "#" + id }
	// Entries written by the service: a disconnect that stopped before its
	// commit (active), a forked record, a lost one.
	for _, id := range []string{active.ID, forked.ID, lost.ID} {
		a.records.set(func(op, name string) error {
			if op == "put" && isMailbox(name) {
				return errStopped
			}
			return nil
		})
		_ = a.Disconnect(ctx, me, id)
	}
	a.records.set(nil)
	w.fork(forked.ID)
	w.lose(lost.ID)
	// And junk nobody signed.
	w.mem.SetEntry(store.Entry{Partition: "mailboxes-disconnecting", Key: key("mbx_" + strings.Repeat("a", 26)), Value: []byte("junk")})
	w.mem.SetEntry(store.Entry{Partition: "mailboxes-disconnecting", Key: "not a time#x", Value: []byte("junk")})
	sweepAll(t, a)
	left := func() int {
		l, _ := w.mem.ListEntries(ctx, "mailboxes-disconnecting", "", "", 100)
		return len(l)
	}
	if left() != 4 { // the active one waits an hour, the lost one SweepGrace, the junk stays
		t.Fatalf("%d left", left())
	}
	if !w.history("mailbox-" + lost.ID).Head.Gone() {
		t.Fatal("the lost record stays")
	}
	w.clock.add(time.Hour)
	sweepAll(t, a)
	if left() != 2 {
		t.Fatalf("%d left", left())
	}
	if _, err := a.Accept(ctx, active.Address); err != nil {
		t.Fatal("a disconnect that never committed took the mailbox away")
	}
}

// Every failure of the keys, the records or the store along the main flows
// is an error, never a panic or an answer that says what did not happen.
func TestEveryFailureIsAnError(t *testing.T) {
	flow := func(w *world, e *enclave) {
		me := caller("acc_me", w.clock)
		m, _ := e.Create(ctx, me, contracts.MailboxCreate{Label: "L"})
		n, _ := e.Create(ctx, me, contracts.MailboxCreate{})
		_, _ = e.List(ctx, me)
		if m == nil || n == nil {
			return
		}
		days := 30
		_, _ = e.Update(ctx, me, m.ID, contracts.MailboxUpdate{RetentionDays: &days})
		_, _ = e.Get(ctx, me, m.ID)
		_, _ = e.Open(ctx, me, m.ID, mailboxes.AccessReadMessage)
		_ = e.deliver(w.t, message("one"), m.Address, n.Address)
		_ = e.deliver(w.t, []byte("From: forwarding-noreply@google.com\r\nTo: "+m.Address+"\r\nSubject: hi\r\n\r\nConfirmation code: 123456789\r\n"), m.Address)
		_, _ = e.Get(ctx, me, m.ID)
		_, _ = e.Pause(ctx, n.ID)
		_, _ = e.Resume(ctx, n.ID)
		_, _ = e.Record(ctx, n.ID)
		_, _ = e.Records(ctx, me.Account)
		_, _ = e.ByAddress(ctx, n.Address)
		_, _ = e.Total(ctx)
		_ = e.Disconnect(ctx, me, m.ID)
		w.lose(n.ID)
		_, _ = e.Record(ctx, n.ID)
		_ = e.Sweep(ctx)
		w.clock.add(mailboxes.SweepGrace)
		_ = e.Sweep(ctx)
		_ = e.RemoveAccount(ctx, me.Account)
		_ = e.RemoveAccount(ctx, "acc_none")
		_, _ = e.Count(ctx, me.Account)
	}
	type source struct {
		name string
		arm  func(w *world, e *enclave, fail func() bool)
	}
	sources := []source{
		{"store", func(w *world, _ *enclave, fail func() bool) {
			w.setInterrupt(func(context.Context, store.Step, string, string) error {
				if fail() {
					return errStopped
				}
				return nil
			})
		}},
		{"records", func(_ *world, e *enclave, fail func() bool) {
			e.records.set(func(string, string) error {
				if fail() {
					return errStopped
				}
				return nil
			})
		}},
	}
	for _, src := range sources {
		for k := int64(1); ; k++ {
			w := newWorld(t)
			e := w.enclave()
			var calls atomic.Int64
			src.arm(w, e, func() bool { return calls.Add(1) == k })
			flow(w, e)
			if calls.Load() < k {
				break // the flow ran without the failure: every call was tried
			}
		}
	}
	// The keys: failing, or giving a key of the wrong size.
	for _, short := range []bool{false, true} {
		for k := int64(1); ; k++ {
			if !keysFlow(t, flow, short, k) {
				break
			}
		}
	}
}

func keysFlow(t *testing.T, flow func(*world, *enclave), short bool, k int64) bool {
	{
		w := newWorld(t)
		var calls atomic.Int64
		fk := &failingKeys{Local: w.keys, short: short, fail: func() bool { return calls.Add(1) == k }}
		records := &hooked{Anchored: &store.Anchored{Records: w.mem, Commits: w.commits, OnlyNewest: true, Strict: true, Now: w.clock.now}}
		s, err := mailboxes.New(mailboxes.Config{Records: records, Storage: w.mem, Keys: fk, Accounts: w.accounts, Domain: domain, Now: w.clock.now, Log: slog.New(slog.DiscardHandler)})
		if err != nil {
			t.Fatal(err)
		}
		flow(w, &enclave{Service: s, records: records})
		return calls.Load() >= k
	}
}

// failingKeys fails one call, or gives a key too short for anything.
type failingKeys struct {
	*keys.Local
	short bool
	fail  func() bool
}

func (f *failingKeys) Derive(ctx context.Context, purpose string) ([]byte, error) {
	if f.fail() {
		if f.short {
			return make([]byte, 16), nil
		}
		return nil, errStopped
	}
	return f.Local.Derive(ctx, purpose)
}

func (f *failingKeys) NewDataKey(ctx context.Context, c map[string]string) (keys.DataKey, error) {
	if f.fail() {
		if f.short {
			dk, err := f.Local.NewDataKey(ctx, c)
			dk.Plaintext = dk.Plaintext[:16]
			return dk, err
		}
		return keys.DataKey{}, errStopped
	}
	return f.Local.NewDataKey(ctx, c)
}

func (f *failingKeys) Unwrap(ctx context.Context, wrapped []byte, c map[string]string) ([]byte, error) {
	if f.fail() {
		if f.short {
			return make([]byte, 16), nil
		}
		return nil, errStopped
	}
	return f.Local.Unwrap(ctx, wrapped, c)
}

// Values that are not this deployment's: a list or a record that does not
// verify is an error, never an empty account.
func TestValuesThatDoNotVerify(t *testing.T) {
	w := newWorld(t)
	a := w.enclave()
	me := caller("acc_me", w.clock)
	m := a.mustCreate(t, me)
	for _, n := range w.mem.RecordNames() {
		if strings.Contains(n, "@") {
			r, _ := w.mem.GetRecord(ctx, n)
			r.Value = append([]byte(nil), r.Value...)
			r.Value[len(r.Value)-3] ^= 1
			w.mem.SetRecord(*r)
		}
	}
	if _, err := a.List(ctx, me); err == nil {
		t.Fatal("a list whose value is not what was committed was taken for none")
	}
	if _, err := a.Create(ctx, me, contracts.MailboxCreate{}); err == nil {
		t.Fatal("made over a lost list")
	}
	if _, err := a.Accept(ctx, m.Address); !errors.Is(err, mailboxes.ErrNoSuchMailbox) {
		t.Fatalf("a record that is not what was committed: %v", err)
	}
	// An index entry and a settings entry that are not signed are ignored.
	w.mem.SetEntry(store.Entry{Partition: "mailboxes", Key: "mbx_x", Value: []byte("junk")})
	w.mem.SetEntry(store.Entry{Partition: inbox.Partition(m.ID), Key: inbox.KeySettings, Value: []byte("junk"), Version: 9})
	if n, err := a.Total(ctx); err != nil || n != 2 {
		t.Fatal(n, err)
	}
}

// Total counts past a page.
func TestTotalPages(t *testing.T) {
	w := newWorld(t)
	a := w.enclave()
	for i := range 1001 {
		w.mem.SetEntry(store.Entry{Partition: "mailboxes", Key: fmt.Sprintf("mbx_%04d", i), Value: []byte("{}")})
	}
	if n, err := a.Total(ctx); err != nil || n != 1001 {
		t.Fatal(n, err)
	}
}

// Disconnecting what is not the caller's to disconnect, in every state.
func TestDisconnectStrangers(t *testing.T) {
	w := newWorld(t)
	a := w.enclave()
	me, other := caller("acc_me", w.clock), caller("acc_other", w.clock)
	lost, forked, gone := a.mustCreate(t, me), a.mustCreate(t, me), a.mustCreate(t, me)
	w.lose(lost.ID)
	w.fork(forked.ID)
	for _, id := range []string{lost.ID, forked.ID} {
		if err := a.Disconnect(ctx, other, id); !errors.Is(err, contracts.ErrNotFound) {
			t.Fatal(err)
		}
		if err := a.Disconnect(ctx, mailboxes.Caller{Account: me.Account}, id); !errors.Is(err, contracts.ErrConfirmationNeeded) {
			t.Fatal(err)
		}
		if _, err := a.Get(ctx, other, id); !errors.Is(err, contracts.ErrNotFound) {
			t.Fatal(err)
		}
		if _, err := a.Get(ctx, me, id); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.Disconnect(ctx, me, gone.ID); err != nil {
		t.Fatal(err)
	}
	w.clock.add(mailboxes.SweepGrace)
	sweepAll(t, a)
	// Removed and swept: nobody can tell it from one never made.
	if err := a.Disconnect(ctx, me, gone.ID); !errors.Is(err, contracts.ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := a.Record(ctx, lost.ID); err != nil {
		t.Fatal("a lost record of a listed mailbox", err)
	}
}

// Run logs a sweep that failed, with nothing of whom it was about.
func TestRunLogsAFailedSweep(t *testing.T) {
	w := newWorld(t)
	a := w.enclave()
	w.setInterrupt(func(_ context.Context, _ store.Step, partition, _ string) error {
		if partition == "mailboxes-disconnecting" {
			return errors.New("down for mbx_aaaaaaaaaaaaaaaaaaaaaaaaaa msg#0123456789abcdef0123456789abcdef")
		}
		return nil
	})
	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { a.Run(runCtx); close(done) }()
	for range 200 {
		if strings.Contains(w.logs.String(), "sweeping") {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done
	logs := w.logs.String()
	if !strings.Contains(logs, "sweeping") || strings.Contains(logs, "mbx_a") || strings.Contains(logs, "0123456789abcdef0123456789abcdef") {
		t.Fatal(logs)
	}
}

// commit writes a value as a record's next version through a strict
// Anchored of its own: committed, so only what it says can refuse it.
func (w *world) commit(name string, value []byte, wrapped []byte) {
	w.t.Helper()
	a := &store.Anchored{Records: w.mem, Commits: w.commits, OnlyNewest: true, Strict: true, Now: w.clock.now}
	_, at, _ := a.GetRecordAt(ctx, name)
	if err := a.PutRecordAt(ctx, &store.Record{Name: name, Value: value, WrappedKey: wrapped, Plain: true}, at); err != nil {
		w.t.Fatal(err)
	}
}

func (w *world) signed(name string, doc string) []byte {
	w.t.Helper()
	k, _ := w.keys.Derive(ctx, "permaily mailboxes sign v1")
	v, err := envelope.Sign(k, []byte(doc), envelope.AAD("record", name))
	if err != nil {
		w.t.Fatal(err)
	}
	return v
}

// Committed values that are not a mailbox's record or an account's list:
// errors, or a mailbox that cannot be opened, never one taken for another.
func TestCommittedValuesThatAreNotOnes(t *testing.T) {
	w := newWorld(t)
	a := w.enclave()
	me := caller("acc_me", w.clock)
	m := a.mustCreate(t, me)
	name := "mailbox-" + m.ID
	rec, _ := a.records.GetRecord(ctx, name)
	for what, value := range map[string][]byte{
		"not signed":    []byte(`{"doc":{},"mac":"AAAA"}`),
		"another id":    w.signed(name, `{"id":"mbx_aaaaaaaaaaaaaaaaaaaaaaaaaa","state":"active"}`),
		"another state": w.signed(name, `{"id":"`+m.ID+`","state":"open"}`),
		"no key":        w.signed(name, `{"id":"`+m.ID+`","account":"acc_me","state":"active"}`),
		"a sealed part": w.signed(name, `{"id":"`+m.ID+`","account":"acc_me","state":"active","sealed":"AAAA"}`),
	} {
		wrapped := rec.WrappedKey
		if what == "no key" {
			wrapped = nil
		}
		w.commit(name, value, wrapped)
		if _, err := a.Get(ctx, me, m.ID); err == nil {
			t.Errorf("%s: opened", what)
		}
	}
	// The sealed part opens and is not one.
	key, _ := w.keys.Unwrap(ctx, rec.WrappedKey, map[string]string{"purpose": "mailbox", "mailbox": m.ID})
	junk, _ := envelope.Seal(key, []byte("junk"), envelope.AAD("mailbox-private", m.ID))
	w.commit(name, w.signed(name, `{"id":"`+m.ID+`","account":"acc_me","state":"active","sealed":"`+base64.StdEncoding.EncodeToString(junk)+`"}`), rec.WrappedKey)
	if _, err := a.Get(ctx, me, m.ID); err == nil {
		t.Error("a sealed part that is not one")
	}
	list := "mailboxes-acc_me"
	for what, value := range map[string][]byte{
		"not signed":      []byte(`{"doc":{},"mac":"AAAA"}`),
		"another account": w.signed(list, `{"account":"acc_other","state":"open"}`),
	} {
		w.commit(list, value, nil)
		if _, err := a.List(ctx, me); err == nil {
			t.Errorf("list %s: read", what)
		}
	}
}

// alwaysChanged is the records when other enclaves keep writing first.
func TestEverythingKeepsChanging(t *testing.T) {
	w := newWorld(t)
	a := w.enclave()
	me := caller("acc_me", w.clock)
	m, n := a.mustCreate(t, me), a.mustCreate(t, me)
	label := "x"
	w.setInterrupt(func(_ context.Context, step store.Step, _, key string) error {
		if step == store.StepEntry && key == inbox.KeySettings {
			return store.ErrConflict
		}
		return nil
	})
	if _, err := a.Update(ctx, me, m.ID, contracts.MailboxUpdate{Label: &label}); err == nil {
		t.Fatal("settings")
	}
	w.setInterrupt(nil)
	a.records.set(func(op, name string) error {
		if op == "put" {
			return store.ErrChanged
		}
		return nil
	})
	if _, err := a.Create(ctx, me, contracts.MailboxCreate{}); err == nil {
		t.Fatal("create")
	}
	if _, err := a.Pause(ctx, m.ID); err == nil {
		t.Fatal("pause")
	}
	if err := a.Disconnect(ctx, me, m.ID); err == nil {
		t.Fatal("disconnect")
	}
	if err := a.deliver(t, message("first"), n.Address); err == nil {
		t.Fatal("receiving")
	}
	if err := a.RemoveAccount(ctx, me.Account); err == nil {
		t.Fatal("remove")
	}
	a.records.set(nil)
	inbox.VerifyGmail = true
	defer func() { inbox.VerifyGmail = false }()
	w2 := newWorld(t)
	w2.auth = googleDKIM()
	b := w2.enclave()
	o := b.mustCreate(t, me)
	b.records.set(func(op, name string) error {
		if op == "put" {
			return store.ErrChanged
		}
		return nil
	})
	if err := b.deliver(t, googleSigned(t, o.Address, "x@gmail.com"), o.Address); err == nil {
		t.Fatal("confirmed")
	}
	// A disconnect of the mailbox while its confirmation is stored.
	b.records.set(func(op, name string) error {
		if op == "put" && isMailbox(name) {
			b.records.set(nil)
			if err := b.Disconnect(ctx, me, o.ID); err != nil {
				t.Error(err)
			}
			return store.ErrChanged
		}
		return nil
	})
	if err := b.deliver(t, googleSigned(t, o.Address, "y@gmail.com"), o.Address); err == nil {
		t.Fatal("confirmed into a disconnected mailbox")
	}
}

// A reservation disconnected by its person while its record is made, and one
// whose record was removed with its identifier left in the list.
func TestDisconnectingAReservationRaces(t *testing.T) {
	w := newWorld(t)
	a, b := w.enclave(), w.enclave()
	me := caller("acc_me", w.clock)
	_, goOn, done := reserveAndStop(t, a, me)
	pending := reservedID(t, w, me.Account)
	b.records.set(func(op, name string) error {
		if op == "create" && isMailbox(name) {
			b.records.set(nil)
			close(goOn)
			if err := <-done; err != nil {
				t.Error(err)
			}
		}
		return nil
	})
	if err := b.Disconnect(ctx, me, pending); err != nil {
		t.Fatal(err)
	}
	if n, _ := b.Count(ctx, me.Account); n != 0 {
		t.Fatal("still counted")
	}
	// Removed, its identifier left in the list, fenced eleven minutes on.
	m := a.mustCreate(t, me)
	w.lose(m.ID)
	a.records.set(func(op, name string) error {
		if op == "put" && isList(name) {
			return errStopped
		}
		return nil
	})
	_ = a.Disconnect(ctx, me, m.ID)
	a.records.set(nil)
	if err := b.Disconnect(ctx, me, m.ID); err != nil {
		t.Fatal(err)
	}
	n := a.mustCreate(t, me)
	w.lose(n.ID)
	a.records.set(func(op, name string) error {
		if op == "put" && isList(name) {
			return errStopped
		}
		return nil
	})
	_ = a.Disconnect(ctx, me, n.ID)
	a.records.set(nil)
	w.clock.add(mailboxes.ReservationAge)
	if l := listed(t, b, caller("acc_me", w.clock)); len(l) != 0 {
		t.Fatalf("%+v", l)
	}
	// A reservation whose record was forked before its tombstone.
	_, goOn, done = reserveAndStop(t, a, me)
	pending = reservedID(t, w, me.Account)
	close(goOn)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	w.fork(pending)
	w.clock.add(mailboxes.ReservationAge)
	if l := listed(t, b, caller("acc_me", w.clock)); len(l) != 1 || l[0].State != contracts.MailboxUnreadable {
		t.Fatalf("%+v", l)
	}
}

// Whoever reads the table and the bucket finds no address, label, sender,
// subject, text or Gmail account, only identifiers, sizes and times.
func TestTheStoreNeverHoldsPlaintext(t *testing.T) {
	inbox.VerifyGmail = true
	t.Cleanup(func() { inbox.VerifyGmail = false })
	w := newWorld(t)
	w.auth = googleDKIM()
	a := w.enclave()
	me := caller("acc_me", w.clock)
	m, err := a.Create(ctx, me, contracts.MailboxCreate{Label: "Private label"})
	if err != nil {
		t.Fatal(err)
	}
	if err := a.deliver(t, googleSigned(t, m.Address, "requester@gmail.com"), m.Address); err != nil {
		t.Fatal(err)
	}
	if err := a.deliver(t, message("Confidential subject"), m.Address); err != nil {
		t.Fatal(err)
	}
	var stored []string
	for _, e := range w.mem.AllEntries() {
		stored = append(stored, e.Partition, e.Key, string(e.Value))
	}
	for _, n := range w.mem.RecordNames() {
		r, _ := w.mem.GetRecord(ctx, n)
		stored = append(stored, n, string(r.Value))
	}
	for _, msg := range w.messages(m.ID) {
		raw, _ := w.mem.ReadRaw(ctx, m.ID, msg.ID, 1<<20)
		stored = append(stored, string(msg.Meta), string(raw))
	}
	all := strings.Join(stored, "\n")
	local := strings.Split(m.Address, "@")[0]
	for _, secret := range []string{local, "Private label", "requester", "alice", "Confidential", "Hello, this is", "123456789"} {
		if strings.Contains(all, secret) {
			t.Errorf("the store holds %q", secret)
		}
	}
}
