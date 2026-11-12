package accounts

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/domain"
	"github.com/permaily-org/permaily/internal/idp"
	"github.com/permaily-org/permaily/internal/store"
)

// askFor asks to change the address of the account behind a session, and
// returns the attempt and the message that was sent, nil when none was.
func (w *world) askFor(svc *Service, who *Authenticated, address string) (string, *Mail) {
	w.t.Helper()
	before := len(w.out.Sent())
	res, err := svc.StartAddressChange(context.Background(), w.fresh(who), contracts.AddressStart{Address: address})
	if err != nil {
		w.t.Fatalf("asking for %s: %v", address, err)
	}
	if res.ExpiresIn != 600 || !validID("att", res.Attempt) {
		w.t.Fatalf("answer: %+v", res)
	}
	to, err := domain.NormalizeAddress(address)
	if err != nil {
		w.t.Fatal(err)
	}
	return res.Attempt, w.mailTo(svc, before, to, "Confirm your new address for Permaily")
}

// moveTo changes the address of the account behind a session.
func (w *world) moveTo(who *Authenticated, address string) (*contracts.Account, error) {
	w.t.Helper()
	attempt, mail := w.askFor(w.svc, who, address)
	if mail == nil {
		w.t.Fatalf("no message to prove %s", address)
	}
	return w.svc.FinishAddressChange(context.Background(), who, contracts.AddressFinish{Attempt: attempt, Code: code(mail)})
}

// found returns the account an address signs in to, "" when it has none.
func (w *world) found(address string) string {
	w.t.Helper()
	doc, _, err := w.enclave().svc.lookup(context.Background(), address)
	if err != nil {
		w.t.Fatalf("looking %s up: %v", address, err)
	}
	if doc == nil {
		return ""
	}
	return doc.ID
}

// onePerAddress fails unless every account is found by its own address, and
// by no other account's: no two accounts have one address, and none is an
// account that cannot be found. It returns the accounts by their address.
func (w *world) onePerAddress() map[string]string {
	w.t.Helper()
	list, err := w.enclave().svc.AccountRecords(context.Background(), "", 100)
	if err != nil || list.Unreadable != 0 {
		w.t.Fatalf("the accounts: %+v %v", list, err)
	}
	by := map[string]string{}
	for _, a := range list.Accounts {
		if other, taken := by[a.Address]; taken {
			w.t.Fatalf("two accounts have %s: %s and %s", a.Address, other, a.ID)
		}
		by[a.Address] = a.ID
		if got := w.found(a.Address); got != a.ID {
			w.t.Fatalf("the account of %s cannot be found by it: its address leads to %q", a.Address, got)
		}
	}
	return by
}

func TestChangingTheContactAddress(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	ana, cookie := w.signUp("ana@example.org")
	_, other := w.signIn("ana@example.org")
	who := w.who(cookie)
	listed := w.listed(accountsPartition)

	w.now = w.now.Add(time.Hour)
	attempt, mail := w.askFor(w.svc, who, "  Ana.New@Example.ORG ")
	if mail == nil || mail.To != "ana.new@example.org" || mail.Subject != "Confirm your new address for Permaily" {
		t.Fatalf("the message: %+v", mail)
	}
	// A code and no link: nothing in it leads anywhere, or names the account.
	if code(mail) == "" || strings.Contains(mail.Text, "://") || strings.Contains(mail.Text, "#") || strings.Contains(mail.Text, attempt) || strings.Contains(mail.Text, "ana@example.org") || strings.ContainsAny(mail.Subject, "0123456789@") {
		t.Fatalf("the message: %+v", mail)
	}
	// Until the code is given, nothing changed.
	if doc := w.doc(ana.Account.ID); doc.Address != "ana@example.org" || doc.MovingTo != "" || w.found("ana.new@example.org") != "" {
		t.Fatalf("before the code: %+v", doc)
	}

	w.now = w.now.Add(time.Minute)
	n := w.count()
	got, err := w.svc.FinishAddressChange(ctx, who, contracts.AddressFinish{Attempt: attempt, Code: code(mail)})
	if err != nil || got.Address != "ana.new@example.org" || got.ID != ana.Account.ID || got.State != contracts.AccountActive {
		t.Fatalf("the account after the change: %+v %v", got, err)
	}
	doc := w.doc(ana.Account.ID)
	if doc.Address != "ana.new@example.org" || !doc.AddressVerifiedAt.Equal(w.now) || doc.MovingTo != "" || doc.MovingClaim != "" || !doc.CreatedAt.Equal(ana.Account.CreatedAt) {
		t.Fatalf("the record: %+v", doc)
	}
	// The old address is nowhere in the storage, and leads nowhere; the new
	// one leads to the account, and the list of accounts still does.
	if left := w.stored("ana@example.org"); len(left) != 0 {
		t.Fatalf("the old address is still stored: %v", left)
	}
	if w.found("ana@example.org") != "" || w.found("ana.new@example.org") != ana.Account.ID {
		t.Fatal("the addresses do not lead where they should")
	}
	if now := w.listed(accountsPartition); !reflect.DeepEqual(now, listed) {
		t.Fatalf("the list of accounts: %v, was %v", now, listed)
	}
	w.onePerAddress()
	if list, err := w.svc.AccountRecordByAddress(ctx, "ana.new@example.org"); err != nil || len(list.Accounts) != 1 || !list.Accounts[0].AddressVerifiedAt.Equal(w.now) {
		t.Fatalf("the operator's tool: %+v %v", list, err)
	}

	// Both addresses are told, and neither is told the other.
	told, _ := w.sent(n)
	if len(told) != 2 || told[0].To != "ana@example.org" || told[1].To != "ana.new@example.org" || told[0].Subject != subjectAddress || told[1].Subject != subjectAddress {
		t.Fatalf("the notices: %+v", told)
	}
	if !strings.Contains(told[0].Text, "This address is no longer the account's") || !strings.Contains(told[0].Text, "If it was not you, write to support@permaily.com from this address") || !strings.Contains(told[0].Text, "When: 11 November 2026, 10:01 UTC") {
		t.Fatalf("to the old address: %s", told[0].Text)
	}
	if !strings.Contains(told[1].Text, "This address is now the contact address of your Permaily account") || !strings.Contains(told[1].Text, "support@permaily.com") {
		t.Fatalf("to the new address: %s", told[1].Text)
	}
	for _, m := range told {
		if strings.Contains(m.Text, "example.org") {
			t.Fatalf("a notice names an address: %s", m.Text)
		}
	}

	// Every device stays signed in, and sees the new address.
	for _, c := range []string{cookie, other} {
		if me, err := w.svc.Me(ctx, w.who(c)); err != nil || me.Account.Address != "ana.new@example.org" {
			t.Fatalf("a session after the change: %+v %v", me, err)
		}
	}
	// Mail signs in at the new address, and no longer at the old one.
	if _, mail := w.start("ana@example.org", ""); mail != nil {
		t.Fatalf("a sign-in message for the address that was left: %+v", mail)
	}
	if me, _ := w.signIn("ana.new@example.org"); me.Account.ID != ana.Account.ID {
		t.Fatalf("signed in to %s", me.Account.ID)
	}
	// The old address is free: with an invitation it makes another account.
	if again, _ := w.signUp("ana@example.org"); again.Account.ID == ana.Account.ID {
		t.Fatal("the address that was left leads to the account it left")
	}
	w.onePerAddress()

	// Changing to the address it has already is nothing to do, and back to
	// one it had is a change like another.
	if _, err := w.svc.StartAddressChange(ctx, w.fresh(who), contracts.AddressStart{Address: "ANA.new@example.org"}); !errors.Is(err, contracts.ErrInvalidAddress) {
		t.Fatalf("its own address: %v", err)
	}
	if got, err := w.moveTo(who, "third@example.org"); err != nil || got.Address != "third@example.org" {
		t.Fatalf("a second change: %+v %v", got, err)
	}
	if left := w.stored("ana.new@example.org"); len(left) != 0 {
		t.Fatalf("the second address is still stored: %v", left)
	}
	w.onePerAddress()
	// Asked twice for one address, and both codes given: the second finds the
	// account there already, and tells nobody again.
	w.now = w.now.Add(2 * time.Hour)
	one, oneMail := w.askFor(w.svc, who, "fourth@example.org")
	two, twoMail := w.askFor(w.svc, who, "fourth@example.org")
	if _, err := w.svc.FinishAddressChange(ctx, who, contracts.AddressFinish{Attempt: one, Code: code(oneMail)}); err != nil {
		t.Fatal(err)
	}
	n = w.count()
	verified := w.doc(ana.Account.ID).AddressVerifiedAt
	w.now = w.now.Add(time.Minute)
	if got, err := w.svc.FinishAddressChange(ctx, who, contracts.AddressFinish{Attempt: two, Code: code(twoMail)}); err != nil || got.Address != "fourth@example.org" {
		t.Fatalf("the same change a second time: %+v %v", got, err)
	}
	if got, _ := w.sent(n); len(got) != 0 || !w.doc(ana.Account.ID).AddressVerifiedAt.Equal(verified) {
		t.Fatalf("a change to the address it has told %+v", got)
	}
	w.onePerAddress()
	if one, two := NewDevice(), NewDevice(); !domain.DeviceToken(one) || one == two {
		t.Fatal("a new device's cookie")
	}
	w.quiet("ana@", "ana.new@", "third@", "fourth@", "example.org", ana.Account.ID, attempt)
}

func TestAddressChangeRefusals(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	ana, cookie := w.signUp("ana@example.org")
	bea, beaCookie := w.signUp("bea@example.org")
	who, beaWho := w.who(cookie), w.who(beaCookie)
	start := func(address string) error {
		_, err := w.svc.StartAddressChange(ctx, w.fresh(who), contracts.AddressStart{Address: address})
		w.svc.Wait()
		return err
	}
	finish := func(who *Authenticated, attempt, code string) error {
		_, err := w.svc.FinishAddressChange(ctx, who, contracts.AddressFinish{Attempt: attempt, Code: code})
		return err
	}
	untouched := w.ways(ana.Account.ID)

	// What cannot be an address, and the account's own.
	n := w.count()
	for _, address := range []string{"", "new", "new@", "new@example", "Ana <new@example.org>", "a@b.org, c@d.org", "ana@example.org", " ANA@example.org "} {
		if err := start(address); !errors.Is(err, contracts.ErrInvalidAddress) {
			t.Errorf("%q: %v", address, err)
		}
	}
	if got, _ := w.sent(n); len(got) != 0 {
		t.Fatalf("a refused address was sent %+v", got)
	}
	// No session, no change.
	ghost := w.ghost()
	if _, err := w.svc.StartAddressChange(ctx, w.fresh(ghost), contracts.AddressStart{Address: "new@example.org"}); !errors.Is(err, contracts.ErrUnauthorized) {
		t.Fatalf("starting without an account: %v", err)
	}
	if err := finish(ghost, newID("att"), "123456"); !errors.Is(err, contracts.ErrUnauthorized) {
		t.Fatalf("ending without an account: %v", err)
	}

	// A wrong code, five times: then the right one is too late.
	attempt, mail := w.askFor(w.svc, who, "new@example.org")
	for i := range domain.EmailCodeTries {
		if err := finish(who, attempt, w.wrongCode(mail)); !errors.Is(err, contracts.ErrInvalidCode) {
			t.Fatalf("wrong code %d: %v", i, err)
		}
	}
	if err := finish(who, attempt, code(mail)); !errors.Is(err, contracts.ErrInvalidCode) {
		t.Fatalf("the right code after five wrong ones: %v", err)
	}
	// One that ended, one that was used, one that never was, no code at all.
	attempt, mail = w.askFor(w.svc, who, "new@example.org")
	w.now = w.now.Add(domain.EmailCodeLifetime)
	if err := finish(who, attempt, code(mail)); !errors.Is(err, contracts.ErrInvalidCode) {
		t.Fatalf("a code that ended: %v", err)
	}
	for _, in := range [][2]string{{newID("att"), "123456"}, {"", ""}, {attempt, ""}, {"att_x", code(mail)}} {
		if err := finish(who, in[0], in[1]); !errors.Is(err, contracts.ErrInvalidCode) {
			t.Errorf("%v: %v", in, err)
		}
	}
	// The attempt of another account: it is not this one's, and it is not
	// spent by the try.
	theirs, theirMail := w.askFor(w.svc, beaWho, "bea.new@example.org")
	if err := finish(who, theirs, code(theirMail)); !errors.Is(err, contracts.ErrInvalidCode) {
		t.Fatalf("another account's attempt: %v", err)
	}
	if got := w.ways(ana.Account.ID); !reflect.DeepEqual(got, untouched) {
		t.Fatalf("the refusals changed the account: %+v", got)
	}
	if err := finish(beaWho, theirs, code(theirMail)); err != nil {
		t.Fatalf("an attempt after another account tried it: %v", err)
	}
	if err := finish(beaWho, theirs, code(theirMail)); !errors.Is(err, contracts.ErrInvalidCode) {
		t.Fatalf("an attempt used twice: %v", err)
	}

	// An address that is another account's. Asking answers as for any
	// address, and sends the message; only the code's holder is told.
	w.now = w.now.Add(2 * time.Hour)
	attempt, mail = w.askFor(w.svc, who, "bea.new@example.org")
	if mail == nil || mail.To != "bea.new@example.org" {
		t.Fatalf("no message for an address that is taken: %+v", mail)
	}
	if err := finish(who, attempt, w.wrongCode(mail)); !errors.Is(err, contracts.ErrInvalidCode) {
		t.Fatalf("a wrong code for an address that is taken: %v", err)
	}
	n = w.count()
	if err := finish(who, attempt, code(mail)); !errors.Is(err, contracts.ErrAddressInUse) {
		t.Fatalf("an address that is taken: %v", err)
	}
	if got, _ := w.sent(n); len(got) != 0 {
		t.Fatalf("a change that was refused told %+v", got)
	}
	if got := w.ways(ana.Account.ID); !reflect.DeepEqual(got, untouched) {
		t.Fatalf("a change that was refused left something: %+v", got)
	}
	if w.found("bea.new@example.org") != bea.Account.ID || w.found("ana@example.org") != ana.Account.ID {
		t.Fatal("the addresses lead elsewhere")
	}
	w.onePerAddress()

	// A closing account changes nothing, and is told before the message.
	if _, err := w.svc.StartDeletion(ctx, w.fresh(who)); err != nil {
		t.Fatal(err)
	}
	if err := start("new@example.org"); !errors.Is(err, contracts.ErrAccountClosing) {
		t.Fatalf("starting while closing: %v", err)
	}
	if err := finish(who, attempt, "123456"); !errors.Is(err, contracts.ErrAccountClosing) {
		t.Fatalf("ending while closing: %v", err)
	}
	if _, err := w.svc.CancelDeletion(ctx, who); err != nil {
		t.Fatal(err)
	}
	// Asked while active, and deleting is asked through another enclave while
	// the change is being written: refused, and nothing of it is left.
	w.now = w.now.Add(2 * time.Hour)
	attempt, mail = w.askFor(w.svc, who, "new@example.org")
	records := &between{Anchored: w.anchored}
	w.svc.cfg.Records = records
	records.meanwhile = func() {
		if _, err := w.enclave().svc.StartDeletion(ctx, w.fresh(who)); err != nil {
			t.Error(err)
		}
	}
	if err := finish(who, attempt, code(mail)); !errors.Is(err, contracts.ErrAccountClosing) {
		t.Fatalf("a change that deleting overtook: %v", err)
	}
	if doc := w.doc(ana.Account.ID); doc.Address != "ana@example.org" || doc.MovingTo != "" || doc.State != contracts.AccountClosing {
		t.Fatalf("after a change that deleting overtook: %+v", doc)
	}
	// And between the pointer and the end of the change: taken back.
	if _, err := w.svc.CancelDeletion(ctx, who); err != nil {
		t.Fatal(err)
	}
	w.now = w.now.Add(2 * time.Hour)
	attempt, mail = w.askFor(w.svc, who, "new@example.org")
	writes := 0
	records.always, records.meanwhile = true, func() {
		if writes++; writes == 2 {
			if _, err := w.enclave().svc.StartDeletion(ctx, w.fresh(who)); err != nil {
				t.Error(err)
			}
		}
	}
	if err := finish(who, attempt, code(mail)); !errors.Is(err, contracts.ErrAccountClosing) {
		t.Fatalf("a change that deleting overtook at its end: %v", err)
	}
	records.meanwhile = nil
	if doc := w.doc(ana.Account.ID); doc.Address != "ana@example.org" || doc.MovingTo != "" || w.found("new@example.org") != "" {
		t.Fatalf("after a change that deleting overtook at its end: %+v", doc)
	}
	w.onePerAddress()
	w.quiet("ana@", "bea@", "new@", "example.org", ana.Account.ID, bea.Account.ID)
}

// The limits of signing in by mail hold for the code of a new address too.
func TestAddressChangeLimits(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	_, cookie := w.signUp("ana@example.org")
	_, beaCookie := w.signUp("bea@example.org")
	who, beaWho := w.who(cookie), w.who(beaCookie)

	// An account asks five times an hour.
	for i := range domain.EmailStartsPerHour {
		if _, mail := w.askFor(w.svc, who, "new"+string(rune('a'+i))+"@example.org"); mail == nil {
			t.Fatalf("request %d sent nothing", i)
		}
	}
	n := w.count()
	if _, err := w.svc.StartAddressChange(ctx, w.fresh(who), contracts.AddressStart{Address: "one.more@example.org"}); !errors.Is(err, contracts.ErrTooManyRequests) {
		t.Fatalf("a sixth request in an hour: %v", err)
	}
	if got, _ := w.sent(n); len(got) != 0 {
		t.Fatalf("a request over the limit sent %+v", got)
	}
	w.now = w.now.Add(time.Hour + time.Second)
	if _, mail := w.askFor(w.svc, who, "one.more@example.org"); mail == nil {
		t.Fatal("after an hour: nothing sent")
	}

	// An address is sent five messages an hour, of whatever kind, and what it
	// was sent already is told to nobody: the answer is the same.
	for range domain.EmailStartsPerHour {
		w.start("bea@example.org", "")
	}
	if _, mail := w.askFor(w.svc, who, "bea@example.org"); mail != nil {
		t.Fatalf("a sixth message in an hour to one address: %+v", mail)
	}
	w.now = w.now.Add(2 * time.Hour)

	// An address that took ten wrong codes today is sent no code: here there
	// is no link to fall back on, and codes must not be guessed without end.
	for range wrongCodesPerDay / domain.EmailCodeTries {
		attempt, mail := w.askFor(w.svc, beaWho, "target@example.org")
		for range domain.EmailCodeTries {
			if _, err := w.svc.FinishAddressChange(ctx, beaWho, contracts.AddressFinish{Attempt: attempt, Code: w.wrongCode(mail)}); !errors.Is(err, contracts.ErrInvalidCode) {
				t.Fatal(err)
			}
		}
	}
	if _, mail := w.askFor(w.svc, beaWho, "target@example.org"); mail != nil {
		t.Fatalf("a code for an address that took ten wrong ones today: %+v", mail)
	}
	if _, mail := w.askFor(w.svc, who, "target@example.org"); mail != nil {
		t.Fatalf("another account gets a code for it: %+v", mail)
	}
	w.now = w.now.Add(24*time.Hour + time.Second)
	if _, mail := w.askFor(w.svc, who, "target@example.org"); mail == nil {
		t.Fatal("a day later: nothing sent")
	}

	// The mail system is down: the answer is the same, and no code works.
	w.now = w.now.Add(2 * time.Hour)
	w.out.Fail = true
	unsent, mail := w.askFor(w.svc, who, "down@example.org")
	w.out.Fail = false
	if mail != nil {
		t.Fatalf("a message with the mail system down: %+v", mail)
	}
	if _, err := w.svc.FinishAddressChange(ctx, who, contracts.AddressFinish{Attempt: unsent, Code: "123456"}); !errors.Is(err, contracts.ErrInvalidCode) {
		t.Fatalf("a code that was never sent: %v", err)
	}
	if !strings.Contains(w.logs.String(), "a message to prove an address could not be sent") {
		t.Fatalf("logs:\n%s", w.logs)
	}

	// Too much at once: the message is dropped, the answer is the same.
	for range maxSending {
		w.svc.sending <- struct{}{}
	}
	if _, mail := w.askFor(w.svc, beaWho, "busy@example.org"); mail != nil {
		t.Fatalf("a message with too many being sent: %+v", mail)
	}
	for range maxSending {
		<-w.svc.sending
	}
	w.svc.cfg.Entries = &failing{Entries: w.mem, fail: map[string]error{"put " + pendingKey: errors.New("down")}}
	if _, mail := w.askFor(w.svc, beaWho, "full@example.org"); mail != nil {
		t.Fatalf("a message for an attempt that was not kept: %+v", mail)
	}
	w.svc.cfg.Entries = w.mem
	w.quiet("ana@", "bea@", "example.org", unsent)
}

// The attempt of a change of address belongs to the account that started it,
// and works for nothing else: it signs nobody in. And a sign-in changes no
// address.
func TestAnAddressAttemptSignsNobodyIn(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	w.on(idp.Meta)
	ana, cookie := w.signUp("ana@example.org")
	who := w.who(cookie)
	sessions := func() int {
		t.Helper()
		n := 0
		for _, e := range w.mem.AllEntries() {
			if strings.HasPrefix(e.Key, sessionPrefix) {
				n++
			}
		}
		return n
	}
	// The attempt and the code of a change to an address with no account, and
	// of one to an address that has one.
	bea, _ := w.signUp("bea@example.org")
	before, accounts := sessions(), w.accounts()
	for _, address := range []string{"new@example.org", "bea@example.org"} {
		attempt, mail := w.askFor(w.svc, who, address)
		in := contracts.EmailFinish{Attempt: attempt, Code: code(mail)}
		// As a sign-in by mail, by its code and as if it were a link.
		if me, c, err := w.svc.FinishEmail(ctx, in, Browser{}); !errors.Is(err, contracts.ErrInvalidCode) || me != nil || c != "" {
			t.Fatalf("%s: an address attempt as a sign-in: %+v %v", address, me, err)
		}
		if _, _, err := w.svc.FinishEmail(ctx, contracts.EmailFinish{Attempt: attempt, Token: in.Code}, Browser{}); !errors.Is(err, contracts.ErrInvalidCode) {
			t.Fatalf("%s: an address attempt as a link: %v", address, err)
		}
		if _, err := w.svc.LookEmail(ctx, contracts.EmailLook{Attempt: attempt, Token: in.Code}); !errors.Is(err, contracts.ErrInvalidCode) {
			t.Fatalf("%s: an address attempt looked at as a link: %v", address, err)
		}
		// As the code that confirms an address for a provider.
		done, err := w.through(person(idp.Meta, "fb-"+address, address, false), w.invite())
		if err != nil || done.Browser == "" {
			t.Fatalf("a provider that does not vouch: %+v %v", done, err)
		}
		if me, _, err := w.svc.FinishProviderConfirm(ctx, done.Browser, contracts.ProviderConfirm{Attempt: attempt, Code: in.Code}, Browser{}); !errors.Is(err, contracts.ErrInvalidCode) || me != nil {
			t.Fatalf("%s: an address attempt to confirm a provider: %+v %v", address, me, err)
		}
		// Nobody was signed in, no account was made and nothing was linked.
		if sessions() != before || w.accounts() != accounts || len(w.providers(ana.Account.ID))+len(w.providers(bea.Account.ID)) != 0 {
			t.Fatalf("%s: a session, an account or a link was made", address)
		}
		// And the attempt is as it was: those tries were not tries of it.
		_, err = w.svc.FinishAddressChange(ctx, who, contracts.AddressFinish{Attempt: attempt, Code: in.Code})
		if address == "new@example.org" && err != nil {
			t.Fatalf("the attempt after being tried as a sign-in: %v", err)
		}
		if address == "bea@example.org" && !errors.Is(err, contracts.ErrAddressInUse) {
			t.Fatalf("the attempt after being tried as a sign-in: %v", err)
		}
	}
	if sessions() != before {
		t.Fatal("changing an address made a session")
	}

	// A sign-in changes no address: not one of the account's own address,
	// not one of another address, with or without an invitation.
	address := w.doc(ana.Account.ID).Address
	for _, in := range [][2]string{{address, ""}, {"other@example.org", w.invite()}, {"bea@example.org", ""}} {
		attempt, mail := w.start(in[0], in[1])
		if mail == nil {
			t.Fatalf("no sign-in message for %s", in[0])
		}
		token := linkRE.FindStringSubmatch(mail.Text)[2]
		for _, c := range []string{code(mail), token} {
			if got, err := w.svc.FinishAddressChange(ctx, who, contracts.AddressFinish{Attempt: attempt, Code: c}); !errors.Is(err, contracts.ErrInvalidCode) || got != nil {
				t.Fatalf("a sign-in for %s as a change of address: %+v %v", in[0], got, err)
			}
		}
		if doc := w.doc(ana.Account.ID); doc.Address != address || doc.MovingTo != "" {
			t.Fatalf("a sign-in changed an address: %+v", doc)
		}
		// The sign-in is as it was.
		if _, _, err := w.svc.FinishEmail(ctx, contracts.EmailFinish{Attempt: attempt, Code: code(mail)}, Browser{}); err != nil {
			t.Fatalf("the sign-in for %s after being tried as a change: %v", in[0], err)
		}
	}
	// An attempt ends through another enclave, for the account that started it.
	other := w.enclave()
	attempt, mail := w.askFor(w.svc, who, "elsewhere@example.org")
	if got, err := other.svc.FinishAddressChange(ctx, who, contracts.AddressFinish{Attempt: attempt, Code: code(mail)}); err != nil || got.Address != "elsewhere@example.org" {
		t.Fatalf("an attempt ended in another enclave: %+v %v", got, err)
	}
	w.onePerAddress()
}

// Two enclaves change addresses at the same time, each between what the
// other reads and what it writes. Whatever the order, no two accounts end
// with one address and none ends where it cannot be found.
func TestTwoEnclavesChangingAddressesLeaveOneAccountPerAddress(t *testing.T) {
	ctx := context.Background()
	type scene struct {
		w        *world
		one, two *enclave
		ana, bea *Authenticated
		// finish ends, through an enclave, a change that was asked through it.
		ask func(e *enclave, who *Authenticated, address string) func() error
	}
	setup := func(t *testing.T) *scene {
		w := newWorld(t)
		_, anaCookie := w.signUp("ana@example.org")
		_, beaCookie := w.signUp("bea@example.org")
		s := &scene{w: w, one: w.enclave(), two: w.enclave(), ana: w.who(anaCookie), bea: w.who(beaCookie)}
		s.ask = func(e *enclave, who *Authenticated, address string) func() error {
			attempt, mail := w.askFor(e.svc, who, address)
			if mail == nil {
				t.Fatalf("no message for %s", address)
			}
			return func() error {
				_, err := e.svc.FinishAddressChange(ctx, who, contracts.AddressFinish{Attempt: attempt, Code: code(mail)})
				return err
			}
		}
		return s
	}
	const target = "wanted@example.org"

	t.Run("two accounts want one address, the second while the first reads its pointer", func(t *testing.T) {
		s := setup(t)
		first, second := s.ask(s.one, s.ana, target), s.ask(s.two, s.bea, target)
		s.one.entries.partition = loginPartition(target)
		s.one.entries.afterGet = func() {
			if err := second(); err != nil {
				t.Errorf("the change that came in between: %v", err)
			}
		}
		if err := first(); !errors.Is(err, contracts.ErrAddressInUse) {
			t.Fatalf("the change that lost: %v", err)
		}
		by := s.w.onePerAddress()
		if by[target] != s.bea.AccountID || by["ana@example.org"] != s.ana.AccountID || len(by) != 2 {
			t.Fatalf("the accounts: %v", by)
		}
		if doc := s.w.doc(s.ana.AccountID); doc.MovingTo != "" || doc.MovingClaim != "" {
			t.Fatalf("the account that lost is still moving: %+v", doc)
		}
	})
	t.Run("the second before the first says where it is moving", func(t *testing.T) {
		s := setup(t)
		first, second := s.ask(s.one, s.ana, target), s.ask(s.two, s.bea, target)
		s.one.records.meanwhile = func() {
			if err := second(); err != nil {
				t.Errorf("the change that came in between: %v", err)
			}
		}
		if err := first(); !errors.Is(err, contracts.ErrAddressInUse) {
			t.Fatalf("the change that lost: %v", err)
		}
		if by := s.w.onePerAddress(); by[target] != s.bea.AccountID || by["ana@example.org"] != s.ana.AccountID {
			t.Fatalf("the accounts: %v", by)
		}
	})
	t.Run("the second after the first wrote its pointer and before it ends", func(t *testing.T) {
		s := setup(t)
		first, second := s.ask(s.one, s.ana, target), s.ask(s.two, s.bea, target)
		writes := 0
		s.one.records.always = true
		s.one.records.meanwhile = func() {
			// The second write of the record is the one that ends the move.
			if writes++; writes != 2 {
				return
			}
			// Both of the first's addresses lead to it at this moment.
			if s.w.found(target) != s.ana.AccountID || s.w.found("ana@example.org") != s.ana.AccountID {
				t.Error("an account that is moving is not found by both addresses")
			}
			if err := second(); !errors.Is(err, contracts.ErrAddressInUse) {
				t.Errorf("the change that came in between: %v", err)
			}
		}
		if err := first(); err != nil {
			t.Fatalf("the change that came first: %v", err)
		}
		by := s.w.onePerAddress()
		if by[target] != s.ana.AccountID || by["bea@example.org"] != s.bea.AccountID || s.w.found("ana@example.org") != "" {
			t.Fatalf("the accounts: %v", by)
		}
		if doc := s.w.doc(s.bea.AccountID); doc.MovingTo != "" {
			t.Fatalf("the account that lost is still moving: %+v", doc)
		}
	})
	t.Run("a sign-up of the address while the change reads its pointer", func(t *testing.T) {
		s := setup(t)
		first := s.ask(s.one, s.ana, target)
		signUp := s.two.start(target, s.w.invite())
		var made *contracts.Me
		s.one.entries.partition = loginPartition(target)
		s.one.entries.afterGet = func() {
			var err error
			if made, _, err = s.two.svc.FinishEmail(ctx, signUp, Browser{}); err != nil {
				t.Errorf("the sign-up that came in between: %v", err)
			}
		}
		if err := first(); !errors.Is(err, contracts.ErrAddressInUse) {
			t.Fatalf("the change that lost to a sign-up: %v", err)
		}
		by := s.w.onePerAddress()
		if by[target] != made.Account.ID || by["ana@example.org"] != s.ana.AccountID || len(by) != 3 {
			t.Fatalf("the accounts: %v", by)
		}
	})
	t.Run("a change of address while a sign-up of it reads its pointer", func(t *testing.T) {
		s := setup(t)
		first := s.ask(s.one, s.ana, target)
		signUp := s.two.start(target, s.w.invite())
		s.two.entries.partition = loginPartition(target)
		s.two.entries.afterGet = func() {
			if err := first(); err != nil {
				t.Errorf("the change that came in between: %v", err)
			}
		}
		// The sign-up finds the address taken by then: whoever proved it is
		// signed in to the account that has it, and no second one is made.
		me, _, err := s.two.svc.FinishEmail(ctx, signUp, Browser{})
		if err != nil || me.Account.ID != s.ana.AccountID {
			t.Fatalf("the sign-up that lost: %+v %v", me, err)
		}
		if by := s.w.onePerAddress(); by[target] != s.ana.AccountID || len(by) != 2 {
			t.Fatalf("the accounts: %v", by)
		}
	})
	t.Run("one account changes to two addresses at once", func(t *testing.T) {
		s := setup(t)
		first, second := s.ask(s.one, s.ana, target), s.ask(s.two, s.ana, "other@example.org")
		writes := 0
		s.one.records.always = true
		s.one.records.meanwhile = func() {
			if writes++; writes == 2 {
				if err := second(); err != nil {
					t.Errorf("the change that came in between: %v", err)
				}
			}
		}
		// The change that was overtaken is told to ask again, and changes nothing.
		if err := first(); !errors.Is(err, contracts.ErrInvalidCode) {
			t.Fatalf("the change that was overtaken: %v", err)
		}
		by := s.w.onePerAddress()
		if by["other@example.org"] != s.ana.AccountID || s.w.found(target) != "" || s.w.found("ana@example.org") != "" || len(by) != 2 {
			t.Fatalf("the accounts: %v", by)
		}
		if doc := s.w.doc(s.ana.AccountID); doc.MovingTo != "" {
			t.Fatalf("still moving: %+v", doc)
		}
		// The address it did not get is free, pointer and all.
		if me, _ := s.w.signUp(target); me.Account.ID == s.ana.AccountID {
			t.Fatal("the address it did not get leads to it")
		}
		s.w.onePerAddress()
	})
	t.Run("one account changes to two addresses at once, the first to one that is taken", func(t *testing.T) {
		s := setup(t)
		first, second := s.ask(s.one, s.ana, "bea@example.org"), s.ask(s.two, s.ana, target)
		// Between the record saying where it moves and the pointer being read.
		s.one.entries.partition = loginPartition("bea@example.org")
		s.one.entries.afterGet = func() {
			if err := second(); err != nil {
				t.Errorf("the change that came in between: %v", err)
			}
		}
		if err := first(); !errors.Is(err, contracts.ErrAddressInUse) {
			t.Fatalf("the change to an address that is taken: %v", err)
		}
		// Taking the first back does not take back the second.
		by := s.w.onePerAddress()
		if by[target] != s.ana.AccountID || by["bea@example.org"] != s.bea.AccountID || len(by) != 2 {
			t.Fatalf("the accounts: %v", by)
		}
	})
	t.Run("a pointer left by a change that stopped is not written over while its account comes back for it", func(t *testing.T) {
		s := setup(t)
		// Ana's change to the address stopped after its pointer was written,
		// and was taken back: the pointer is there and leads nowhere.
		if _, err := s.w.svc.change(ctx, s.ana.AccountID, func(doc *accountDoc) error {
			doc.MovingTo, doc.MovingClaim = target, "stopped"
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if err := s.w.svc.claimAddress(ctx, s.ana.AccountID, target); err != nil {
			t.Fatal(err)
		}
		s.w.svc.unmove(ctx, s.ana.AccountID, "stopped")
		if s.w.found(target) != "" {
			t.Fatal("a pointer of a change that was taken back leads somewhere")
		}
		// Bea reads that pointer, and Ana's record, and takes the pointer for
		// one that leads nowhere. Before she writes hers in its place, Ana
		// asks again and ends her change, through the other enclave. Ana finds
		// the pointer leading to her already, and writes it again all the
		// same: that is what makes Bea's write, at the version she read, fail.
		first, second := s.ask(s.one, s.bea, target), s.ask(s.two, s.ana, target)
		s.one.entries.partition = loginPartition(target)
		s.one.entries.beforePut = func() {
			if err := second(); err != nil {
				t.Errorf("the change that came in between: %v", err)
			}
		}
		if err := first(); !errors.Is(err, contracts.ErrAddressInUse) {
			t.Fatalf("the change that lost: %v", err)
		}
		by := s.w.onePerAddress()
		if by[target] != s.ana.AccountID || by["bea@example.org"] != s.bea.AccountID || len(by) != 2 {
			t.Fatalf("the accounts: %v", by)
		}
	})
	t.Run("the pointer keeps being written by others", func(t *testing.T) {
		s := setup(t)
		first := s.ask(s.one, s.ana, target)
		// Every time the change reads the pointer, someone writes it again.
		rewrite := &rewriting{Entries: s.w.mem, partition: loginPartition(target), svc: s.w.svc}
		s.one.svc.cfg.Entries = rewrite
		if err := first(); err == nil || errors.Is(err, contracts.ErrAddressInUse) || !strings.Contains(err.Error(), "keeps changing") {
			t.Fatalf("a pointer that keeps changing: %v", err)
		}
		if rewrite.reads != maxRewrites {
			t.Fatalf("tried %d times", rewrite.reads)
		}
		// Given up: the account is where it was, and moving nowhere.
		if doc := s.w.doc(s.ana.AccountID); doc.Address != "ana@example.org" || doc.MovingTo != "" {
			t.Fatalf("after giving up: %+v", doc)
		}
		if s.w.found("ana@example.org") != s.ana.AccountID {
			t.Fatal("the account cannot be found")
		}
	})
}

// rewriting is entries where the pointer of one address is written again,
// to lead nowhere, each time it is read.
type rewriting struct {
	store.Entries
	partition string
	svc       *Service
	reads     int
}

func (r *rewriting) GetEntry(ctx context.Context, partition, key string) (*store.Entry, error) {
	e, err := r.Entries.GetEntry(ctx, partition, key)
	if partition == r.partition {
		r.reads++
		version := uint64(0)
		if e != nil {
			version = e.Version
		}
		if err := r.svc.point(ctx, partition, key, newID("acc"), version); err != nil {
			return nil, err
		}
	}
	return e, err
}

// A change of address that stops halfway, at any step, leaves an account
// that its old address finds, or both: never one that cannot be found, and
// never an address that finds two. Asking again ends it.
func TestAChangeOfAddressThatStopsHalfway(t *testing.T) {
	down := errors.New("down")
	const target = "new@example.org"
	setup := func(t *testing.T) (*world, *failing, *Authenticated, func() error) {
		w := newWorld(t)
		_, cookie := w.signUp("ana@example.org")
		who := w.who(cookie)
		attempt, mail := w.askFor(w.svc, who, target)
		f := &failing{Records: w.svc.cfg.Records, Entries: w.mem, Keys: w.svc.cfg.Keys, fail: map[string]error{}, skip: map[string]int{}}
		w.svc.cfg.Records, w.svc.cfg.Entries, w.svc.cfg.Keys = f, f, f
		return w, f, who, func() error {
			_, err := w.svc.FinishAddressChange(context.Background(), who, contracts.AddressFinish{Attempt: attempt, Code: code(mail)})
			return err
		}
	}
	again := func(t *testing.T, w *world, f *failing, who *Authenticated) {
		t.Helper()
		f.fail, f.skip = map[string]error{}, map[string]int{}
		w.now = w.now.Add(2 * time.Hour)
		if got, err := w.moveTo(who, target); err != nil || got.Address != target {
			t.Fatalf("asking again: %+v %v", got, err)
		}
		if by := w.onePerAddress(); by[target] != who.AccountID || w.found("ana@example.org") != "" {
			t.Fatalf("after asking again: %v", by)
		}
		if doc := w.doc(who.AccountID); doc.MovingTo != "" || doc.MovingClaim != "" {
			t.Fatalf("still moving: %+v", doc)
		}
	}

	t.Run("the record cannot be read", func(t *testing.T) {
		w, f, who, finish := setup(t)
		f.fail["get record"] = down
		if err := finish(); !errors.Is(err, down) {
			t.Fatalf("%v", err)
		}
		again(t, w, f, who)
	})
	t.Run("the record cannot say where it is moving", func(t *testing.T) {
		w, f, who, finish := setup(t)
		f.fail["put record"] = down
		if err := finish(); !errors.Is(err, down) {
			t.Fatalf("%v", err)
		}
		if by := w.onePerAddress(); by["ana@example.org"] != who.AccountID || w.found(target) != "" {
			t.Fatalf("the accounts: %v", by)
		}
		again(t, w, f, who)
	})
	for name, op := range map[string]string{
		"the pointer cannot be read":                "get " + loginPartition(target),
		"the pointer cannot be written":             "put " + loginPartition(target),
		"the key cannot be had to sign the record":  "derive 2",
		"the key cannot be had to read the pointer": "derive 3",
	} {
		t.Run(name, func(t *testing.T) {
			w, f, who, finish := setup(t)
			if times, counted := strings.CutPrefix(op, "derive "); counted {
				// The key is had to read the record, twice, before it signs it.
				op, f.skip["derive"] = "derive", int(times[0]-'0')
			}
			f.fail[op] = down
			err := finish()
			if !errors.Is(err, down) {
				t.Fatalf("%v", err)
			}
			f.fail = map[string]error{}
			// Taken back, or never begun: the account is at its old address.
			if by := w.onePerAddress(); by["ana@example.org"] != who.AccountID || w.found(target) != "" {
				t.Fatalf("the accounts: %v", by)
			}
			again(t, w, f, who)
		})
	}
	t.Run("the pointer cannot be written, and the move cannot be taken back", func(t *testing.T) {
		w, f, who, finish := setup(t)
		f.fail["put "+loginPartition(target)] = down
		f.fail["put record"], f.skip["put record"] = down, 1
		if err := finish(); !errors.Is(err, down) {
			t.Fatalf("%v", err)
		}
		f.fail, f.skip = map[string]error{}, map[string]int{}
		// The record still says it is moving, and no pointer says so: the
		// address is nobody's, and whoever signs up with it gets their own.
		if doc := w.doc(who.AccountID); doc.MovingTo != target {
			t.Fatalf("the record: %+v", doc)
		}
		if by := w.onePerAddress(); by["ana@example.org"] != who.AccountID || w.found(target) != "" {
			t.Fatalf("the accounts: %v", by)
		}
		if !strings.Contains(w.logs.String(), "a change of address that did not end could not be taken back") {
			t.Fatalf("logs:\n%s", w.logs)
		}
		other, _ := w.signUp(target)
		if by := w.onePerAddress(); by[target] != other.Account.ID || by["ana@example.org"] != who.AccountID {
			t.Fatalf("after a sign-up of the address: %v", by)
		}
		// And the account that was moving there is told it is taken.
		w.now = w.now.Add(2 * time.Hour)
		if _, err := w.moveTo(who, target); !errors.Is(err, contracts.ErrAddressInUse) {
			t.Fatalf("asking again for an address that was taken meanwhile: %v", err)
		}
		if doc := w.doc(who.AccountID); doc.MovingTo != "" {
			t.Fatalf("the record: %+v", doc)
		}
		w.onePerAddress()
	})
	t.Run("the record cannot say it is done", func(t *testing.T) {
		w, f, who, finish := setup(t)
		f.fail["put record"], f.skip["put record"] = down, 1
		if err := finish(); !errors.Is(err, down) {
			t.Fatalf("%v", err)
		}
		f.fail, f.skip = map[string]error{}, map[string]int{}
		// Both addresses find the account, and no other: it stopped between
		// the pointer and the record.
		if w.found("ana@example.org") != who.AccountID || w.found(target) != who.AccountID || len(w.onePerAddress()) != 1 {
			t.Fatal("an account that stopped halfway is not found by both addresses")
		}
		if doc := w.doc(who.AccountID); doc.Address != "ana@example.org" || doc.MovingTo != target {
			t.Fatalf("the record: %+v", doc)
		}
		// Whoever signs in with the new address, which its person proved,
		// reaches the account; nobody signs up with it.
		if me, _ := w.signIn(target); me.Account.ID != who.AccountID {
			t.Fatal("the new address signs in elsewhere")
		}
		// Removed as it is, nothing of either address is left behind: the
		// removal that cannot reach the second pointer is tried again.
		if _, err := w.svc.StartDeletion(context.Background(), w.fresh(who)); err != nil {
			t.Fatal(err)
		}
		w.now = w.now.Add(domain.AccountClosing)
		f.fail["get "+loginPartition(target)] = down
		if err := w.svc.RemoveDue(context.Background()); !errors.Is(err, down) {
			t.Fatalf("a removal that could not reach a pointer: %v", err)
		}
		if w.doc(who.AccountID) == nil {
			t.Fatal("removed before its pointers were")
		}
		f.fail = map[string]error{}
		if err := w.svc.RemoveDue(context.Background()); err != nil {
			t.Fatal(err)
		}
		if left := w.stored("ana@example.org", target, who.AccountID); len(left) != 0 {
			t.Fatalf("left of an account that was moving: %v", left)
		}
	})
	t.Run("the record cannot say it is done, and asking again ends it", func(t *testing.T) {
		w, f, who, finish := setup(t)
		f.fail["put record"], f.skip["put record"] = down, 1
		if err := finish(); !errors.Is(err, down) {
			t.Fatalf("%v", err)
		}
		again(t, w, f, who)
	})
	t.Run("the old pointer cannot be deleted", func(t *testing.T) {
		for _, op := range []string{"get " + loginPartition("ana@example.org"), "delete " + loginPartition("ana@example.org"), "derive 8"} {
			w, f, who, finish := setup(t)
			if times, counted := strings.CutPrefix(op, "derive "); counted {
				// Everything before the old pointer is read has the key.
				op, f.skip["derive"] = "derive", int(times[0]-'0')
			}
			f.fail[op] = down
			// The change is done all the same: what is left leads nowhere.
			if err := finish(); err != nil {
				t.Fatalf("%s: %v", op, err)
			}
			f.fail = map[string]error{}
			if by := w.onePerAddress(); by[target] != who.AccountID || w.found("ana@example.org") != "" {
				t.Fatalf("%s: the accounts: %v", op, by)
			}
			if !strings.Contains(w.logs.String(), "the pointer of an address that was left could not be deleted") {
				t.Fatalf("%s: logs:\n%s", op, w.logs)
			}
			// And the address that was left makes a new account, in its place.
			if other, _ := w.signUp("ana@example.org"); other.Account.ID == who.AccountID {
				t.Fatalf("%s: the address that was left leads back", op)
			}
			w.onePerAddress()
			w.quiet("ana@", "new@", "example.org", who.AccountID)
		}
	})
	t.Run("the account that has the address cannot be read", func(t *testing.T) {
		w, _, who, _ := setup(t)
		bea, _ := w.signUp("bea@example.org")
		w.now = w.now.Add(2 * time.Hour)
		attempt, mail := w.askFor(w.svc, who, "bea@example.org")
		rec := w.record(w.newest(bea.Account.ID))
		rec.Value = []byte(strings.Replace(string(rec.Value), "bea@example.org", "eve@example.org", 1))
		w.mem.SetRecord(rec)
		// Never taken for an address that is free.
		if _, err := w.svc.FinishAddressChange(context.Background(), who, contracts.AddressFinish{Attempt: attempt, Code: code(mail)}); err == nil || errors.Is(err, contracts.ErrAddressInUse) {
			t.Fatalf("an address whose account cannot be read: %v", err)
		}
		if doc := w.doc(who.AccountID); doc.Address != "ana@example.org" || doc.MovingTo != "" {
			t.Fatalf("the record: %+v", doc)
		}
	})
}
