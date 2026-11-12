package accounts

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/domain"
	"github.com/permaily-org/permaily/internal/idp"
	"github.com/permaily-org/permaily/internal/store"
	"github.com/permaily-org/permaily/internal/webauthn/webauthntest"
)

// device is a value a browser's device cookie can have.
func device(n byte) string { return strings.Repeat(string('a'+n), 43) }

// signInFrom signs a browser in by mail to an account that exists.
func (w *world) signInFrom(address string, from Browser) *contracts.Me {
	w.t.Helper()
	attempt, mail := w.start(address, "")
	if mail == nil {
		w.t.Fatalf("no sign-in message for %s", address)
	}
	me, _, err := w.svc.FinishEmail(context.Background(), contracts.EmailFinish{Attempt: attempt, Code: code(mail)}, from)
	if err != nil {
		w.t.Fatal(err)
	}
	return me
}

// devices returns the keys of the devices an account keeps.
func (w *world) devices(accountID string) []string {
	w.t.Helper()
	var keys []string
	for _, e := range w.mem.AllEntries() {
		if e.Partition == accountPartition(accountID) && strings.HasPrefix(e.Key, devicePrefix) {
			keys = append(keys, e.Key)
		}
	}
	return keys
}

// notices returns the messages sent since there were n that are notices: not
// the ones that carry a code.
func (w *world) notices(n int) []Mail {
	w.t.Helper()
	var out []Mail
	all, _ := w.sent(n)
	for _, m := range all {
		if m.Subject != "Sign in to Permaily" && m.Subject != "Confirm your new address for Permaily" {
			out = append(out, m)
		}
	}
	return out
}

// A sign-in from a device that had not signed in to the account before is
// told by mail, by whatever way it signed in. The sign-in that makes the
// account is not one, and neither is a device that signed in before.
func TestASignInFromANewDeviceIsTold(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	w.on(idp.Google)
	const chrome = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/142.0.0.0 Safari/537.36"

	// The sign-up: its device is known from then on, and nothing is told.
	attempt, mail := w.start("ana@example.org", w.invite())
	n := w.count()
	me, cookie, err := w.svc.FinishEmail(ctx, contracts.EmailFinish{Attempt: attempt, Code: code(mail)}, Browser{UserAgent: chrome, Device: device(0)})
	if err != nil {
		t.Fatal(err)
	}
	id := me.Account.ID
	if got, _ := w.sent(n); len(got) != 0 {
		t.Fatalf("the sign-up was told as a new device: %+v", got)
	}
	n = w.count()
	w.signInFrom("ana@example.org", Browser{UserAgent: chrome, Device: device(0)})
	if got := w.notices(n); len(got) != 0 {
		t.Fatalf("a device that signed up was told as new: %+v", got)
	}

	// What is kept of a device is a hash made with the account: not the
	// cookie's value, and not the same for another account.
	if keys := w.devices(id); len(keys) != 1 || keys[0] != deviceKey(id, device(0)) || len(keys[0]) != len(devicePrefix)+64 {
		t.Fatalf("the devices: %v", keys)
	}
	if left := w.stored(device(0)); len(left) != 0 {
		t.Fatalf("the value of a device's cookie is stored: %v", left)
	}
	if deviceKey(newID("acc"), device(0)) == deviceKey(id, device(0)) {
		t.Fatal("a device is kept the same for two accounts")
	}

	// Another device, by mail.
	w.now = w.now.Add(90 * time.Minute)
	n = w.count()
	w.signInFrom("ana@example.org", Browser{UserAgent: chrome, Device: device(1)})
	got := w.notices(n)
	if len(got) != 1 || got[0].To != "ana@example.org" || got[0].Subject != subjectNewDevice {
		t.Fatalf("the notice: %+v", got)
	}
	for _, want := range []string{
		"A device that had not signed in to your Permaily account before has just signed in to it.",
		"    When: 11 November 2026, 10:30 UTC\n", "    How: with a code or a link sent by email\n", "    Browser: Chrome on Windows\n",
		"If it was you, there is nothing to do.", "If it was not you, sign in at app.permaily.test, open Settings, sign out the devices you do not know",
		"If you cannot sign in, write to support@permaily.com.",
	} {
		if !strings.Contains(got[0].Text, want) {
			t.Fatalf("the notice lacks %q:\n%s", want, got[0].Text)
		}
	}
	// The list of sessions keeps what the browser said, and the web app names
	// it as the notice does (domain.DeviceName).
	list, err := w.svc.Sessions(ctx, w.who(cookie))
	if err != nil || list.Sessions[0].Device != chrome {
		t.Fatalf("the sessions: %+v %v", list, err)
	}
	// The second time it is not new.
	n = w.count()
	w.signInFrom("ana@example.org", Browser{UserAgent: chrome, Device: device(1)})
	if got := w.notices(n); len(got) != 0 {
		t.Fatalf("a device that signed in before was told as new: %+v", got)
	}

	// With a passkey, with a provider and with a recovery code: each says how.
	key := webauthntest.New(webauthntest.ES256)
	passkey := w.addPasskey(w.who(cookie), key, "laptop")
	google := person(idp.Google, "g-ana", "ana@example.org", true)
	if _, err := w.linkFrom(w.who(cookie), google); err != nil {
		t.Fatal(err)
	}
	how := func(what string, n int, want string) {
		t.Helper()
		var told []Mail
		for _, m := range w.notices(n) {
			if m.Subject == subjectNewDevice {
				told = append(told, m)
			}
		}
		if want == "" {
			if len(told) != 0 {
				t.Fatalf("%s: told as a new device: %+v", what, told)
			}
			return
		}
		if len(told) != 1 || !strings.Contains(told[0].Text, "    How: "+want+"\n") {
			t.Fatalf("%s: %+v", what, told)
		}
	}
	n = w.count()
	if _, _, err := w.svc.FinishPasskeySignIn(ctx, w.assert(key, passkey.ID), Browser{UserAgent: "x", Device: device(2)}); err != nil {
		t.Fatal(err)
	}
	how("a passkey", n, "with a passkey")
	n = w.count()
	if _, _, err := w.svc.FinishPasskeySignIn(ctx, w.assert(key, passkey.ID), Browser{UserAgent: "x", Device: device(2)}); err != nil {
		t.Fatal(err)
	}
	how("the same passkey and device again", n, "")
	provider := func(dev string) {
		t.Helper()
		state, trip := w.leave(nil, contracts.ProviderStart{Provider: "google"})
		done, err := w.svc.FinishProvider(ctx, ProviderAnswer{State: state, Code: w.idp.approve(google), Browser: trip, From: Browser{UserAgent: "x", Device: dev}})
		if err != nil || done.Session == "" {
			t.Fatalf("signing in with the provider: %+v %v", done, err)
		}
	}
	n = w.count()
	provider(device(3))
	how("a provider", n, "with Google")
	n = w.count()
	provider(device(3))
	how("the same provider and device again", n, "")
	codes := w.passkeyOnly(w.who(cookie))
	n = w.count()
	if _, _, err := w.svc.Recover(ctx, contracts.Recovery{Address: "ana@example.org", Code: codes[0]}, Browser{UserAgent: "x", Device: device(4)}); err != nil {
		t.Fatal(err)
	}
	how("a recovery code", n, "with a recovery code")
	n = w.count()
	if _, _, err := w.svc.Recover(ctx, contracts.Recovery{Address: "ana@example.org", Code: codes[1]}, Browser{UserAgent: "x", Device: device(4)}); err != nil {
		t.Fatal(err)
	}
	how("a recovery code from the same device", n, "")
	if _, err := w.svc.SetPasskeyOnly(ctx, w.fresh(w.who(cookie)), contracts.PasskeyOnly{On: false}); err != nil {
		t.Fatal(err)
	}

	// A browser without the cookie, or with something that cannot be one, is
	// a device nobody knows, every time, and nothing is kept of it.
	kept := len(w.devices(id))
	for _, dev := range []string{"", "short", strings.Repeat("a", 42) + "!", strings.Repeat("a", 44)} {
		for range 2 {
			w.now = w.now.Add(21 * time.Minute) // within what an address is sent in an hour
			n = w.count()
			w.signInFrom("ana@example.org", Browser{UserAgent: chrome, Device: dev})
			how("a browser with the cookie "+dev, n, "with a code or a link sent by email")
		}
	}
	if len(w.devices(id)) != kept {
		t.Fatalf("something was kept of a browser without a cookie: %v", w.devices(id))
	}

	// Nothing a browser says of itself is repeated, not even a sentence with
	// no link in it: only a name from fixed words, or that it was not
	// recognised.
	for i, ua := range []string{"", "Visit https://evil.example/now to keep your account", "evil.example says: confirm at once", "write to help@evil.example", "Your account is at risk, call 555 0100"} {
		w.now = w.now.Add(21 * time.Minute)
		n = w.count()
		w.signInFrom("ana@example.org", Browser{UserAgent: ua, Device: device(byte(10 + i))})
		got := w.notices(n)
		if len(got) != 1 || !strings.Contains(got[0].Text, "    Browser: not recognised\n") || strings.Contains(got[0].Text, "evil") || strings.Contains(got[0].Text, "risk") {
			t.Fatalf("user agent %q: %+v", ua, got)
		}
	}
	// A provider signing up makes an account: not a new device.
	n = w.count()
	done, err := w.svc.FinishProvider(ctx, func() ProviderAnswer {
		state, trip := w.leave(nil, contracts.ProviderStart{Provider: "google", Invitation: w.invite(), Terms: w.terms})
		return ProviderAnswer{State: state, Code: w.idp.approve(person(idp.Google, "g-bea", "bea@example.org", true)), Browser: trip, From: Browser{Device: device(20)}}
	}())
	if err != nil || done.Session == "" {
		t.Fatalf("signing up with a provider: %+v %v", done, err)
	}
	if got := w.notices(n); len(got) != 0 {
		t.Fatalf("a sign-up with a provider was told: %+v", got)
	}
	w.quiet("ana@", "example.org", id, device(0), device(1), device(2), "Mozilla", "evil")
}

// The devices an account keeps are bounded in time and in number, and are no
// way into anything: whoever writes to the table can cause one notice more,
// or one less, and nothing else.
func TestDevicesAreBoundedAndProveNothing(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	me, _ := w.signUp("ana@example.org")
	bea, _ := w.signUp("bea@example.org")
	id := me.Account.ID
	// enter signs a device in without a message, and says whether it was told.
	enter := func(accountID, address, dev string) bool {
		t.Helper()
		n := w.count()
		doc := w.doc(accountID)
		if _, _, err := w.svc.enter(ctx, doc, wayPasskey, Browser{UserAgent: "x", Device: dev}, false); err != nil {
			t.Fatal(err)
		}
		got, _ := w.sent(n)
		if len(got) > 1 || (len(got) == 1 && (got[0].Subject != subjectNewDevice || got[0].To != address)) {
			t.Fatalf("after a sign-in: %+v", got)
		}
		return len(got) == 1
	}
	if !enter(id, "ana@example.org", device(0)) || enter(id, "ana@example.org", device(0)) {
		t.Fatal("a device is new once")
	}
	// A device is known for a year from when it last signed in.
	w.now = w.now.Add(domain.DeviceLifetime - time.Minute)
	if enter(id, "ana@example.org", device(0)) {
		t.Fatal("a device that signed in within a year was told as new")
	}
	w.now = w.now.Add(domain.DeviceLifetime - time.Minute)
	if enter(id, "ana@example.org", device(0)) {
		t.Fatal("signing in did not keep the device known")
	}
	w.now = w.now.Add(domain.DeviceLifetime)
	if !enter(id, "ana@example.org", device(0)) {
		t.Fatal("a device that last signed in a year ago is still known")
	}
	// Whoever runs the storage setting the entry's own time later changes
	// nothing: the time that counts is the one that was signed.
	e, err := w.mem.GetEntry(ctx, accountPartition(id), deviceKey(id, device(0)))
	if err != nil {
		t.Fatal(err)
	}
	w.now = w.now.Add(domain.DeviceLifetime)
	longer := *e
	longer.ExpiresAt = w.now.Add(10 * domain.DeviceLifetime)
	w.mem.SetEntry(longer)
	if !enter(id, "ana@example.org", device(0)) {
		t.Fatal("a device was kept known by the entry's own time")
	}

	// An account keeps fifty: the one that signed in longest ago goes.
	for i := range domain.MaxDevices {
		w.now = w.now.Add(time.Minute)
		if !enter(id, "ana@example.org", strings.Repeat("d", 41)+string(rune('A'+i/26))+string(rune('a'+i%26))) {
			t.Fatalf("device %d was not new", i)
		}
		// The outbox keeps a hundred: the session list keeps fifty of these.
	}
	if n := len(w.devices(id)); n != domain.MaxDevices {
		t.Fatalf("%d devices kept", n)
	}
	if !enter(id, "ana@example.org", device(0)) {
		t.Fatal("the device that signed in longest ago is still known")
	}
	if n := len(w.devices(id)); n != domain.MaxDevices {
		t.Fatalf("%d devices kept", n)
	}
	if enter(id, "ana@example.org", strings.Repeat("d", 41)+"Bx") {
		t.Fatal("the newest device was forgotten")
	}

	// Written by hand. An entry that was not signed here is no device, and
	// goes when the next one comes; one of another account, copied, neither.
	w.now = w.now.Add(time.Minute)
	theirs := device(5)
	if !enter(bea.Account.ID, "bea@example.org", theirs) {
		t.Fatal("a first device")
	}
	copied, err := w.mem.GetEntry(ctx, accountPartition(bea.Account.ID), deviceKey(bea.Account.ID, theirs))
	if err != nil {
		t.Fatal(err)
	}
	moved := *copied
	moved.Partition, moved.Key = accountPartition(id), deviceKey(id, theirs)
	w.mem.SetEntry(moved)
	w.mem.SetEntry(store.Entry{Partition: accountPartition(id), Key: deviceKey(id, device(6)), Value: []byte(`{"v":1,"account":"` + id + `","seen_at":"2099-01-01T00:00:00Z"}`)})
	if !enter(id, "ana@example.org", theirs) || !enter(id, "ana@example.org", device(6)) {
		t.Fatal("an entry written by hand made a device known")
	}
	// Deleting one sends its person one notice more.
	w.now = w.now.Add(time.Minute)
	if err := w.mem.DeleteEntry(ctx, accountPartition(id), deviceKey(id, device(6))); err != nil {
		t.Fatal(err)
	}
	if !enter(id, "ana@example.org", device(6)) {
		t.Fatal("a device whose entry was deleted is still known")
	}
	// Putting back one that was dropped sends one less. That is all it does:
	// it is no session, and signs nobody in.
	dropped := *e
	w.now = e.UpdatedAt.Add(time.Hour)
	w.mem.SetEntry(dropped)
	if enter(id, "ana@example.org", device(0)) {
		t.Fatal("an entry put back was not taken as the device it is")
	}
	if _, err := w.svc.Authenticate(ctx, id+"."+newID("ses")+"."+device(0)); !errors.Is(err, contracts.ErrUnauthorized) {
		t.Fatalf("a device's cookie as a session: %v", err)
	}
	// They go with the account.
	if _, err := w.svc.StartDeletion(ctx, w.fresh(&Authenticated{AccountID: id})); err != nil {
		t.Fatal(err)
	}
	w.now = w.now.Add(domain.AccountClosing)
	if err := w.svc.RemoveDue(ctx); err != nil {
		t.Fatal(err)
	}
	if left := w.devices(id); len(left) != 0 {
		t.Fatalf("devices of an account that was removed: %v", left)
	}
}

// secrets is everything in a test that a person typed, or that would let
// someone in: none of it belongs in a notice.
type secrets []string

// A notice holds no code, no link and no token: only fixed words, a time,
// how a sign-in was made and the name of a browser, never what it said of
// itself. Its subject is fixed words. Every kind is sent here.
func TestNoticesHoldNoCodeLinkOrToken(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	w.on(idp.Google)
	var hidden secrets
	const ua = "Mozilla/5.0 (X11; Linux x86_64; rv:109.0) Gecko/20100101 Firefox/115.0"

	invitation := w.invite()
	attempt, mail := w.start("ana@example.org", invitation)
	me, cookie, err := w.svc.FinishEmail(ctx, contracts.EmailFinish{Attempt: attempt, Code: code(mail)}, Browser{UserAgent: ua, Device: device(0)})
	if err != nil {
		t.Fatal(err)
	}
	who := w.who(cookie)
	id := me.Account.ID
	hidden = append(hidden, invitation, attempt, code(mail), cookie, device(0), id, me.Session.ID)
	n := w.count()
	if _, err := w.svc.UpdateAccount(ctx, who, contracts.AccountUpdate{Name: text("Ana https://evil.example")}); err != nil {
		t.Fatal(err)
	}

	// A new device.
	w.signInFrom("ana@example.org", Browser{UserAgent: ua, Device: device(1)})
	// A passkey added, and one removed. The name a person gives it is theirs
	// to choose, so it is not repeated.
	key, spare := webauthntest.New(webauthntest.ES256), webauthntest.New(webauthntest.ES256)
	passkey := w.addPasskey(who, key, "see https://evil.example/passkey")
	extra := w.addPasskey(who, spare, "spare evil.example")
	if err := w.svc.RemovePasskey(ctx, w.fresh(who), extra.ID.String()); err != nil {
		t.Fatal(err)
	}
	hidden = append(hidden, passkey.ID.String(), extra.ID.String(), "evil")
	// A provider linked, and unlinked.
	if _, err := w.linkFrom(who, person(idp.Google, "subject-at-google", "ana@example.org", true)); err != nil {
		t.Fatal(err)
	}
	if err := w.svc.UnlinkProvider(ctx, w.fresh(who), "google"); err != nil {
		t.Fatal(err)
	}
	hidden = append(hidden, "subject-at-google", "Name At Provider")
	// "Passkey only" on, new recovery codes, one used, and off.
	codes := w.passkeyOnly(who)
	fresh, err := w.svc.NewRecoveryCodes(ctx, w.fresh(who))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.svc.Recover(ctx, contracts.Recovery{Address: "ana@example.org", Code: fresh.RecoveryCodes[0]}, Browser{UserAgent: ua, Device: device(2)}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.SetPasskeyOnly(ctx, w.fresh(who), contracts.PasskeyOnly{On: false}); err != nil {
		t.Fatal(err)
	}
	hidden = append(hidden, codes...)
	hidden = append(hidden, fresh.RecoveryCodes...)
	// Deleting asked and cancelled, by the person and by the operator.
	if _, err := w.svc.StartDeletion(ctx, w.fresh(who)); err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.CancelDeletion(ctx, who); err != nil {
		t.Fatal(err)
	}
	w.operator(w.svc.DeleteAccount, id)
	w.operator(w.svc.KeepAccount, id)
	// The contact address changed.
	w.now = w.now.Add(2 * time.Hour)
	changing, proof := w.askFor(w.svc, who, "ana.new@example.org")
	if _, err := w.svc.FinishAddressChange(ctx, who, contracts.AddressFinish{Attempt: changing, Code: code(proof)}); err != nil {
		t.Fatal(err)
	}
	hidden = append(hidden, changing, code(proof))
	// Blocked, asked to delete while blocked, and unblocked.
	w.operator(w.svc.BlockAccount, id)
	w.operator(w.svc.DeleteAccount, id)
	w.operator(w.svc.UnblockAccount, id)

	got := w.notices(n)
	subjects := map[string]int{}
	for _, m := range got {
		subjects[m.Subject]++
	}
	want := map[string]int{
		subjectNewDevice: 2, // by mail, and with the recovery code
		// Two passkeys added and one removed, a provider linked and unlinked,
		// "passkey only" on and off, recovery codes made anew, and one used.
		subjectChange:   9,
		subjectDeleting: 3, subjectKept: 2, subjectAddress: 2, subjectBlocked: 1, subjectUnblocked: 1,
	}
	for subject, n := range want {
		if subjects[subject] != n {
			t.Errorf("%d notices %q, want %d", subjects[subject], subject, n)
		}
	}
	if len(subjects) != len(want) {
		t.Fatalf("the subjects: %v", subjects)
	}
	// Every kind of change in how the account is signed into was told.
	for kind, what := range signInChanges {
		found := false
		for _, m := range got {
			found = found || strings.HasPrefix(m.Text, what)
		}
		if !found {
			t.Errorf("no notice of %s", kind)
		}
	}
	for _, what := range []string{"Signing in with Google was added to your Permaily account.", "Signing in with Google was removed from your Permaily account."} {
		found := false
		for _, m := range got {
			found = found || strings.HasPrefix(m.Text, what)
		}
		if !found {
			t.Errorf("no notice that says %q", what)
		}
	}

	var (
		link   = regexp.MustCompile(`(?i)://|https?:|www\.|[#?&=]|token|attempt`)
		digits = regexp.MustCompile(`\d{5,}`)
		ident  = regexp.MustCompile(`\b(acc|ses|att|cer|inv)_`)
		// A secret is long and has no spaces: nothing like one is here.
		long = regexp.MustCompile(`[A-Za-z0-9_-]{25,}`)
		// The only hosts and addresses a notice names are the app's and support's.
		names = regexp.MustCompile(`[a-z0-9-]+(\.[a-z0-9-]+)*\.[a-z]{2,}`)
		code  = regexp.MustCompile(`\b[2-9A-Z]{4}-[2-9A-Z]{4}\b`)
		// The line of a new device's notice that names the browser.
		browserLine = regexp.MustCompile(`(?m)^    Browser: (.*)\n`)
	)
	for _, m := range got {
		if m.To != "ana@example.org" && m.To != "ana.new@example.org" {
			t.Errorf("a notice to %s", m.To)
		}
		// The subject is fixed words: no digit, no address, nothing else.
		if strings.ContainsAny(m.Subject, "0123456789@:/#.") {
			t.Errorf("the subject %q", m.Subject)
		}
		text := m.Subject + "\n" + m.Text
		// The browser is named, and nothing it said of itself is repeated:
		// every line is fixed words, and all of them are checked below.
		if said := browserLine.FindStringSubmatch(text); said != nil && said[1] != "Firefox on Linux" {
			t.Errorf("a notice names the browser %q", said[1])
		}
		for what, re := range map[string]*regexp.Regexp{"a link": link, "a code": digits, "an identifier": ident, "a token": long, "a recovery code or an invitation": code} {
			if found := re.FindString(text); found != "" {
				t.Errorf("a notice holds %s (%q):\n%s", what, found, text)
			}
		}
		for _, name := range names.FindAllString(text, -1) {
			if name != "app.permaily.test" && name != "permaily.com" {
				t.Errorf("a notice names %q:\n%s", name, text)
			}
		}
		if strings.Count(text, "@") != strings.Count(text, "support@permaily.com") {
			t.Errorf("a notice names an address:\n%s", text)
		}
		for _, secret := range hidden {
			if strings.Contains(text, secret) {
				t.Errorf("a notice holds %q:\n%s", secret, text)
			}
		}
		// Plain text, in lines a mail program shows as they are.
		if strings.ContainsAny(text, "<>\r\t") || !strings.HasSuffix(m.Text, "\n") || strings.Contains(m.Text, "\n\n\n") {
			t.Errorf("a notice is not plain text:\n%q", m.Text)
		}
	}
	// And the two messages that do carry a code carry it in their text alone.
	for _, m := range w.out.Sent() {
		if (m.Subject == "Sign in to Permaily" || m.Subject == "Confirm your new address for Permaily") && strings.ContainsAny(m.Subject, "0123456789@:/#") {
			t.Errorf("the subject %q", m.Subject)
		}
	}
}

// stuck is a mail system that takes a message and never answers, until it is
// told the wait is over.
type stuck struct {
	mu       sync.Mutex
	taken    int
	released chan struct{}
}

func (s *stuck) Send(ctx context.Context, _ Mail) error {
	s.mu.Lock()
	s.taken++
	s.mu.Unlock()
	<-ctx.Done()
	s.released <- struct{}{}
	return ctx.Err()
}

// ignoring is a mail system that takes a message and does not answer until
// the test frees it, whatever its context says.
type ignoring struct {
	took, free chan struct{}
}

func (i *ignoring) Send(context.Context, Mail) error {
	close(i.took)
	<-i.free
	return nil
}

// A notice that cannot be sent fails nothing: what caused it is done, and
// answered. One that hangs holds the answer for a short time and no longer.
// What is logged is that a notice of a kind was not sent, and no more.
func TestANoticeThatFailsDoesNotFailTheAction(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	w.on(idp.Google)
	me, cookie := w.signUp("ana@example.org")
	who, id := w.who(cookie), me.Account.ID
	key, spare := webauthntest.New(webauthntest.ES256), webauthntest.New(webauthntest.ES256)

	// Every action that tells its person, with the mail system down for the
	// notice: each is done.
	down := func(what string, act func() error) {
		t.Helper()
		w.out.Fail = true
		err := act()
		w.out.Fail = false
		if err != nil {
			t.Fatalf("%s with the mail system down: %v", what, err)
		}
	}
	var passkey, extra *contracts.Passkey
	down("adding a passkey", func() (err error) {
		in := w.create(who, key, "laptop")
		passkey, err = w.svc.FinishPasskey(ctx, who, in)
		return err
	})
	down("adding another", func() (err error) {
		in := w.create(who, spare, "spare")
		extra, err = w.svc.FinishPasskey(ctx, who, in)
		return err
	})
	down("removing a passkey", func() error { return w.svc.RemovePasskey(ctx, w.fresh(who), extra.ID.String()) })
	attempt, mail := w.start("ana@example.org", "")
	down("a sign-in from a new device", func() error {
		_, _, err := w.svc.FinishEmail(ctx, contracts.EmailFinish{Attempt: attempt, Code: code(mail)}, Browser{Device: device(1)})
		return err
	})
	down("a sign-in with a passkey from a new device", func() error {
		_, _, err := w.svc.FinishPasskeySignIn(ctx, w.assert(key, passkey.ID), Browser{Device: device(2)})
		return err
	})
	state, trip := w.leave(who, contracts.ProviderStart{Provider: "google"})
	down("linking a provider", func() error {
		_, err := w.comeBack(state, trip, person(idp.Google, "g-ana", "ana@example.org", true))
		return err
	})
	down("unlinking a provider", func() error { return w.svc.UnlinkProvider(ctx, w.fresh(who), "google") })
	var codes []string
	down("turning passkey only on", func() error {
		out, err := w.svc.SetPasskeyOnly(ctx, w.fresh(who), contracts.PasskeyOnly{On: true})
		if err == nil {
			codes = out.RecoveryCodes
		}
		return err
	})
	down("new recovery codes", func() error {
		out, err := w.svc.NewRecoveryCodes(ctx, w.fresh(who))
		if err == nil {
			codes = out.RecoveryCodes
		}
		return err
	})
	down("a recovery code", func() error {
		got, c, err := w.svc.Recover(ctx, contracts.Recovery{Address: "ana@example.org", Code: codes[0]}, Browser{Device: device(3)})
		if err == nil && (got == nil || c == "") {
			t.Fatal("a recovery that signed nobody in")
		}
		return err
	})
	down("turning passkey only off", func() error {
		_, err := w.svc.SetPasskeyOnly(ctx, w.fresh(who), contracts.PasskeyOnly{On: false})
		return err
	})
	down("asking to delete", func() error { _, err := w.svc.StartDeletion(ctx, w.fresh(who)); return err })
	down("cancelling", func() error { _, err := w.svc.CancelDeletion(ctx, who); return err })
	down("the operator asking to delete", func() error { _, err := w.svc.DeleteAccount(ctx, id); return err })
	down("the operator cancelling", func() error { _, err := w.svc.KeepAccount(ctx, id); return err })
	changing, proof := w.askFor(w.svc, who, "ana.new@example.org")
	down("changing the address", func() error {
		got, err := w.svc.FinishAddressChange(ctx, who, contracts.AddressFinish{Attempt: changing, Code: code(proof)})
		if err == nil && got.Address != "ana.new@example.org" {
			t.Fatalf("the address: %+v", got)
		}
		return err
	})
	down("blocking", func() error { _, err := w.svc.BlockAccount(ctx, id); return err })
	down("unblocking", func() error { _, err := w.svc.UnblockAccount(ctx, id); return err })

	// Each was done.
	doc := w.doc(id)
	if doc.Address != "ana.new@example.org" || len(doc.Passkeys) != 1 || doc.PasskeyOnly || len(doc.Providers) != 0 || doc.blocked() || doc.State != contracts.AccountActive {
		t.Fatalf("after all of it: %+v", doc)
	}
	// And each is logged by its kind: nothing of the mail system's own error,
	// which names the address.
	logs := w.logs.String()
	for _, kind := range []string{
		noticeNewDevice, noticePasskeyAdded, noticePasskeyRemoved, noticeProviderLinked, noticeProviderUnlinked, noticePasskeyOnlyOn, noticePasskeyOnlyOff,
		noticeRecoveryCodesNew, noticeRecoveryCodeUsed, noticeAddressChanged, noticeDeletionAsked, noticeDeletionCancelled, noticeBlocked, noticeUnblocked,
	} {
		if !strings.Contains(logs, `msg="accounts: a notice could not be sent" kind=`+kind+"\n") {
			t.Errorf("no log line for a notice of kind %s", kind)
		}
	}
	line := regexp.MustCompile(`msg="accounts: a notice could not be sent" kind=[a-z_]+$`)
	for _, l := range strings.Split(strings.TrimSpace(logs), "\n") {
		if strings.Contains(l, "notice") && !line.MatchString(l) {
			t.Errorf("a log line with more than the kind: %s", l)
		}
	}
	w.quiet("ana@", "ana.new@", "example.org", id, "outbox", "told to fail")

	// A mail system that never answers holds the action for the wait, and no
	// longer: then the sending is given up. A wait that is over already, so
	// that nothing here depends on how fast the machine is.
	hang := &stuck{released: make(chan struct{}, 4)}
	w.svc.cfg.Mailer, w.svc.noticeWait = hang, 0
	if _, err := w.svc.BlockAccount(ctx, id); err != nil {
		t.Fatalf("blocking with a mail system that hangs: %v", err)
	}
	if !w.doc(id).blocked() {
		t.Fatal("not blocked")
	}
	<-hang.released
	w.svc.Wait()
	// And one that does not even end when told to: the action is answered
	// all the same, while it still holds the message.
	deaf := &ignoring{took: make(chan struct{}), free: make(chan struct{})}
	w.svc.cfg.Mailer = deaf
	if _, err := w.svc.UnblockAccount(ctx, id); err != nil {
		t.Fatalf("unblocking with a mail system that never answers: %v", err)
	}
	<-deaf.took
	close(deaf.free)
	w.svc.Wait()
	logs = w.logs.String()
	for _, kind := range []string{noticeBlocked, noticeUnblocked} {
		if !strings.Contains(logs, `msg="accounts: a notice was not sent in time" kind=`+kind+"\n") {
			t.Fatalf("no line for a notice of kind %s not sent in time:\n%s", kind, logs)
		}
	}

	// The request that caused a notice may be over before it is sent: it is
	// sent all the same.
	w.svc.cfg.Mailer, w.svc.noticeWait = w.out, time.Hour
	if _, err := w.svc.BlockAccount(ctx, id); err != nil {
		t.Fatal(err)
	}
	gone, cancel := context.WithCancel(ctx)
	cancel()
	n := w.count()
	if _, err := w.svc.UnblockAccount(gone, id); err != nil {
		t.Fatal(err)
	}
	w.one(n, "ana.new@example.org", subjectUnblocked)

	// Too many notices at once: one more is dropped, and what caused it is done.
	for range maxNoticing {
		w.svc.noticing <- struct{}{}
	}
	n = w.count()
	if _, err := w.svc.BlockAccount(ctx, id); err != nil {
		t.Fatal(err)
	}
	if got, _ := w.sent(n); len(got) != 0 || !w.doc(id).blocked() {
		t.Fatalf("with too many notices being sent: %+v", got)
	}
	if !strings.Contains(w.logs.String(), `msg="accounts: too many notices being sent, one was dropped" kind=blocked`) {
		t.Fatalf("logs:\n%s", w.logs)
	}
	for range maxNoticing {
		<-w.svc.noticing
	}
}

// Keeping a device fails when the storage does: the sign-in does not, and
// the person is told once more rather than once less.
func TestDeviceFailures(t *testing.T) {
	ctx := context.Background()
	var down error
	setup := func(t *testing.T) (*world, *failing, *accountDoc) {
		w := newWorld(t)
		me, _ := w.signUp("ana@example.org")
		f := &failing{Records: w.svc.cfg.Records, Entries: w.mem, Keys: w.svc.cfg.Keys, fail: map[string]error{}, skip: map[string]int{}}
		w.svc.cfg.Records, w.svc.cfg.Entries, w.svc.cfg.Keys = f, f, f
		// As a store's error is: it names what it was about.
		down = errors.New("store: entries of account#" + me.Account.ID + ": down")
		return w, f, w.doc(me.Account.ID)
	}
	enter := func(t *testing.T, w *world, doc *accountDoc, dev string) int {
		t.Helper()
		n := w.count()
		me, cookie, err := w.svc.enter(ctx, doc, wayMail, Browser{Device: dev}, false)
		if err != nil || me == nil || cookie == "" {
			t.Fatalf("a sign-in while a device could not be kept: %+v %v", me, err)
		}
		got, _ := w.sent(n)
		return len(got)
	}
	t.Run("a device that is known cannot be read", func(t *testing.T) {
		w, f, doc := setup(t)
		if enter(t, w, doc, device(0)) != 1 {
			t.Fatal("a first sign-in was not told")
		}
		f.fail["get "+accountPartition(doc.ID)] = down
		if enter(t, w, doc, device(0)) != 1 {
			t.Fatal("a device that could not be read was taken for a known one")
		}
		if !strings.Contains(w.logs.String(), `msg="accounts: a device could not be kept" err="store: entries of account#acc_…: down"`) {
			t.Fatalf("logs:\n%s", w.logs)
		}
		w.quiet("ana@", doc.ID, device(0))
	})
	t.Run("the devices cannot be listed", func(t *testing.T) {
		w, f, doc := setup(t)
		// The first listing is of the sessions.
		f.fail["list "+accountPartition(doc.ID)], f.skip["list "+accountPartition(doc.ID)] = down, 1
		if enter(t, w, doc, device(0)) != 1 || len(w.devices(doc.ID)) != 0 {
			t.Fatal("a device that could not be kept was not told, or was kept")
		}
	})
	t.Run("a device cannot be written", func(t *testing.T) {
		w, f, doc := setup(t)
		f.fail["put "+deviceKey(doc.ID, device(0))] = down
		if enter(t, w, doc, device(0)) != 1 {
			t.Fatal("not told")
		}
		// It was not kept, so the next time it is new again.
		if enter(t, w, doc, device(0)) != 1 {
			t.Fatal("a device that was not kept is known")
		}
		// Known, and not written this time: known all the same.
		f.fail["put "+deviceKey(doc.ID, device(0))] = down
		if enter(t, w, doc, device(0)) != 0 {
			t.Fatal("a known device whose entry could not be written again was told as new")
		}
	})
	t.Run("an old device cannot be dropped", func(t *testing.T) {
		w, f, doc := setup(t)
		w.mem.SetEntry(store.Entry{Partition: accountPartition(doc.ID), Key: devicePrefix + "by-hand", Value: []byte("{}")})
		f.fail["delete "+accountPartition(doc.ID)] = down
		if enter(t, w, doc, device(0)) != 1 || len(w.devices(doc.ID)) != 1 {
			t.Fatal("a device was kept past what could not be dropped")
		}
	})
	t.Run("the key cannot be had", func(t *testing.T) {
		w, f, doc := setup(t)
		// The session is signed first, and then the device.
		f.fail["derive"], f.skip["derive"] = down, 2
		if enter(t, w, doc, device(0)) != 1 {
			t.Fatal("not told")
		}
		if !strings.Contains(w.logs.String(), "a device could not be kept") {
			t.Fatalf("logs:\n%s", w.logs)
		}
	})
	t.Run("two sign-ins of one device at once", func(t *testing.T) {
		w, f, doc := setup(t)
		// Another sign-in of the same device writes its entry in between.
		f.fail["put "+deviceKey(doc.ID, device(0))] = store.ErrExists
		if enter(t, w, doc, device(0)) != 1 {
			t.Fatal("not told")
		}
		if strings.Contains(w.logs.String(), "a device could not be kept") {
			t.Fatalf("an entry that another sign-in wrote was logged as a failure:\n%s", w.logs)
		}
	})
}

// How a notice's sending ended is what is logged, whenever the end of the
// wait is seen: the sending ends the wait once it is over, so an answer that
// is there when the wait ends is the answer. Before, notify chose between
// the two at random, and logged a notice that could not be sent as one not
// sent in time on a busy machine.
func TestANoticeIsLoggedAsItEnded(t *testing.T) {
	over := make(chan struct{})
	close(over)
	for _, how := range []sending{noticeSent, noticeFailed, noticeLate} {
		for range 200 {
			done := make(chan sending, 1)
			done <- how
			if got := settle(done, over); got != how {
				t.Fatalf("a sending that ended as %d was taken as %d", how, got)
			}
		}
	}
	// The wait over, and no answer yet: not in time.
	if got := settle(make(chan sending), over); got != noticeLate {
		t.Fatalf("no answer when the wait ended: %d", got)
	}
}
