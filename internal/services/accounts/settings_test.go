package accounts

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/domain"
	"github.com/permaily-org/permaily/internal/store"
	"github.com/permaily-org/permaily/internal/webauthn/webauthntest"
)

// signIn signs a device in to an account that exists, by mail.
func (w *world) signIn(address string) (*contracts.Me, string) {
	w.t.Helper()
	attempt, mail := w.start(address, "")
	if mail == nil {
		w.t.Fatalf("no sign-in message for %s", address)
	}
	me, cookie, err := w.svc.FinishEmail(context.Background(), contracts.EmailFinish{Attempt: attempt, Code: code(mail)}, "another device")
	if err != nil {
		w.t.Fatal(err)
	}
	return me, cookie
}

// stored returns what the storage holds that names any of the words: the
// records, by name and by value, and the entries, the expired ones too. It
// is what whoever runs the storage could still read of an account.
func (w *world) stored(words ...string) []string {
	w.t.Helper()
	has := func(texts ...string) bool {
		for _, text := range texts {
			for _, word := range words {
				if strings.Contains(text, word) {
					return true
				}
			}
		}
		return false
	}
	var out []string
	for _, name := range w.mem.RecordNames() {
		if rec := w.record(name); has(name, string(rec.Value)) {
			out = append(out, "record "+name)
		}
	}
	for _, e := range w.mem.AllEntries() {
		if has(e.Partition, e.Key, string(e.Value)) {
			out = append(out, "entry "+e.Partition+" "+e.Key)
		}
	}
	return out
}

// snapshot is everything the storage holds, to put back later.
type snapshot struct {
	records []store.Record
	entries []store.Entry
}

func (w *world) snapshot() snapshot {
	w.t.Helper()
	var s snapshot
	for _, name := range w.mem.RecordNames() {
		s.records = append(s.records, w.record(name))
	}
	s.entries = w.mem.AllEntries()
	return s
}

// putBack writes a snapshot over what is stored, as whoever runs the storage
// can with a copy they kept.
func (w *world) putBack(s snapshot) {
	for _, r := range s.records {
		w.mem.SetRecord(r)
	}
	for _, e := range s.entries {
		w.mem.SetEntry(e)
	}
}

// version is how many times an account's record was written.
func (w *world) version(accountID string) uint64 {
	w.t.Helper()
	head, _, err := w.commits.Head(context.Background(), recordName(accountID))
	if err != nil || head == nil {
		w.t.Fatalf("the commits of the account: %+v %v", head, err)
	}
	return head.Version
}

// listed returns the keys of a list's entries.
func (w *world) listed(partition string) []string {
	w.t.Helper()
	var keys []string
	for _, e := range w.mem.AllEntries() {
		if e.Partition == partition {
			keys = append(keys, e.Key)
		}
	}
	return keys
}

// quiet fails when the logs hold any of the words.
func (w *world) quiet(words ...string) {
	w.t.Helper()
	for _, word := range words {
		if word != "" && strings.Contains(w.logs.String(), word) {
			w.t.Fatalf("the logs hold %q:\n%s", word, w.logs)
		}
	}
}

func text(s string) *string { return &s }

func TestAccountName(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	me, cookie := w.signUp("ana@example.org")
	who := w.who(cookie)
	if me.Account.Name != "" || !me.Account.ClosesAt.IsZero() {
		t.Fatalf("a new account: %+v", me.Account)
	}

	got, err := w.svc.UpdateAccount(ctx, who, contracts.AccountUpdate{Name: text("  Ana Pérez ")})
	if err != nil || got.Name != "Ana Pérez" || got.ID != me.Account.ID || got.Address != "ana@example.org" || got.State != contracts.AccountActive {
		t.Fatalf("named: %+v %v", got, err)
	}
	if seen, err := w.svc.Me(ctx, who); err != nil || seen.Account != *got {
		t.Fatalf("me: %+v %v", seen, err)
	}
	// The same name, or none said, writes nothing.
	written := w.version(me.Account.ID)
	for _, in := range []contracts.AccountUpdate{{Name: text("Ana Pérez")}, {Name: text(" Ana Pérez")}, {}} {
		if got, err := w.svc.UpdateAccount(ctx, who, in); err != nil || got.Name != "Ana Pérez" || w.version(me.Account.ID) != written {
			t.Fatalf("%+v: %+v %v, version %d", in, got, err, w.version(me.Account.ID))
		}
	}
	// One line that can be shown, of eighty characters at most.
	for _, bad := range []string{"two\nlines", strings.Repeat("a", 81), "bell\a", "\xff\xfe", "right\u202eto left"} {
		_, err := w.svc.UpdateAccount(ctx, who, contracts.AccountUpdate{Name: &bad})
		if !errors.Is(err, contracts.ErrInvalidAccountName) || !errors.Is(err, contracts.ErrInvalidName) || w.version(me.Account.ID) != written {
			t.Errorf("%q: %v", bad, err)
		}
	}
	long := strings.Repeat("ñ", 80)
	if got, err := w.svc.UpdateAccount(ctx, who, contracts.AccountUpdate{Name: &long}); err != nil || got.Name != long {
		t.Fatalf("eighty characters: %+v %v", got, err)
	}
	// The name before is not kept anywhere: only the newest version of the
	// record is stored.
	if left := w.stored("Pérez"); len(left) != 0 {
		t.Fatalf("a name that was changed is still stored: %q", left)
	}
	// A person takes their name away the way they gave it.
	if got, err := w.svc.UpdateAccount(ctx, who, contracts.AccountUpdate{Name: text(" ")}); err != nil || got.Name != "" {
		t.Fatalf("no name: %+v %v", got, err)
	}
	if left := w.stored("ñ"); len(left) != 0 {
		t.Fatalf("a name that was taken away is still stored: %q", left)
	}

	ghost := &Authenticated{AccountID: "acc_" + strings.Repeat("a", 26)}
	if _, err := w.svc.UpdateAccount(ctx, ghost, contracts.AccountUpdate{Name: text("x")}); !errors.Is(err, contracts.ErrUnauthorized) {
		t.Fatalf("naming an account that is not there: %v", err)
	}
	w.quiet("Ana", "Pérez", "ñ", me.Account.ID, "example.org")
}

// Asking to delete an account makes it closing for seven days: the other
// devices are signed out, nothing of it changes, the person still signs in,
// and cancelling makes it what it was.
func TestDeletingAnAccount(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	ana, laptop := w.signUp("ana@example.org")
	_, phone := w.signIn("ana@example.org")
	_, eveCookie := w.signUp("eve@example.org")
	who := w.who(laptop)
	auth := webauthntest.New(webauthntest.ES256)
	key := w.addPasskey(who, auth, "key")
	// A passkey the person started to make before asking.
	pending := w.create(who, webauthntest.New(webauthntest.ES256), "late")

	asked := w.now
	got, err := w.svc.StartDeletion(ctx, who)
	if err != nil || got.State != contracts.AccountClosing || !got.ClosesAt.Equal(asked.Add(7*24*time.Hour)) || got.ID != ana.Account.ID {
		t.Fatalf("asked: %+v %v", got, err)
	}
	if me, err := w.svc.Me(ctx, who); err != nil || me.Account != *got {
		t.Fatalf("me while closing: %+v %v", me, err)
	}
	// Every other device is signed out; this one, and other people's, are not.
	if _, err := w.svc.Authenticate(ctx, phone); !errors.Is(err, contracts.ErrUnauthorized) {
		t.Fatalf("another device of a closing account: %v", err)
	}
	for _, cookie := range []string{laptop, eveCookie} {
		if _, err := w.svc.Authenticate(ctx, cookie); err != nil {
			t.Fatalf("a session that should stand: %v", err)
		}
	}
	if list, err := w.svc.Sessions(ctx, who); err != nil || len(list.Sessions) != 1 || !list.Sessions[0].Current {
		t.Fatalf("sessions while closing: %+v %v", list, err)
	}
	if keys := w.listed(closingPartition); len(keys) != 1 || keys[0] != "20261118T090000.000000000Z."+ana.Account.ID {
		t.Fatalf("the list of closing accounts: %q", keys)
	}

	// Nothing changes while it is closing, and nothing is written.
	written := w.version(ana.Account.ID)
	for what, err := range map[string]error{
		"the name":                     second(w.svc.UpdateAccount(ctx, who, contracts.AccountUpdate{Name: text("Ana")})),
		"a change that says nothing":   second(w.svc.UpdateAccount(ctx, who, contracts.AccountUpdate{})),
		"starting a passkey":           second(w.svc.StartPasskey(ctx, who)),
		"a passkey started before":     second(w.svc.FinishPasskey(ctx, who, pending)),
		"renaming a passkey":           second(w.svc.RenamePasskey(ctx, who, key.ID.String(), contracts.PasskeyRename{Name: "renamed"})),
		"removing a passkey":           w.svc.RemovePasskey(ctx, who, key.ID.String()),
		"passkey only":                 second(w.svc.SetPasskeyOnly(ctx, who, contracts.PasskeyOnly{On: true})),
		"passkey only as it is":        second(w.svc.SetPasskeyOnly(ctx, who, contracts.PasskeyOnly{On: false})),
		"recovery codes":               second(w.svc.NewRecoveryCodes(ctx, who)),
		"a passkey that is not there":  w.svc.RemovePasskey(ctx, who, contracts.Bytes("none").String()),
		"renaming one that is not one": second(w.svc.RenamePasskey(ctx, who, contracts.Bytes("none").String(), contracts.PasskeyRename{Name: "x"})),
	} {
		if !errors.Is(err, contracts.ErrAccountClosing) {
			t.Errorf("%s while closing: %v", what, err)
		}
	}
	if list, err := w.svc.Passkeys(ctx, who); err != nil || len(list.Passkeys) != 1 || list.Passkeys[0].Name != "key" || list.PasskeyOnly || w.version(ana.Account.ID) != written {
		t.Fatalf("after the refusals: %+v %v, version %d", list, err, w.version(ana.Account.ID))
	}

	// The person still signs in, by mail and with a passkey, sees that the
	// account is closing, and signs out.
	again, tablet := w.signIn("ana@example.org")
	if again.Account != *got {
		t.Fatalf("signed in while closing: %+v", again.Account)
	}
	withKey, third, err := w.signInWith(auth, key.ID)
	if err != nil || withKey.Account != *got {
		t.Fatalf("signed in with a passkey while closing: %+v %v", withKey, err)
	}
	if err := w.svc.CloseSession(ctx, who, withKey.Session.ID); err != nil {
		t.Fatalf("signing a device out while closing: %v", err)
	}
	if _, err := w.svc.Authenticate(ctx, third); !errors.Is(err, contracts.ErrUnauthorized) {
		t.Fatalf("a device that was signed out: %v", err)
	}

	// Asking again, a day later, keeps the day and signs the others out again.
	w.now = w.now.Add(24 * time.Hour)
	if same, err := w.svc.StartDeletion(ctx, who); err != nil || *same != *got || w.version(ana.Account.ID) != written {
		t.Fatalf("asked again: %+v %v", same, err)
	}
	if _, err := w.svc.Authenticate(ctx, tablet); !errors.Is(err, contracts.ErrUnauthorized) {
		t.Fatalf("a device signed in before asking again: %v", err)
	}
	if keys := w.listed(closingPartition); len(keys) != 1 {
		t.Fatalf("the list of closing accounts after asking again: %q", keys)
	}

	// Cancelling makes the account what it was.
	kept, err := w.svc.CancelDeletion(ctx, who)
	if err != nil || kept.State != contracts.AccountActive || !kept.ClosesAt.IsZero() || kept.ID != ana.Account.ID {
		t.Fatalf("cancelled: %+v %v", kept, err)
	}
	if keys := w.listed(closingPartition); len(keys) != 0 {
		t.Fatalf("the list of closing accounts after cancelling: %q", keys)
	}
	written = w.version(ana.Account.ID)
	if same, err := w.svc.CancelDeletion(ctx, who); err != nil || *same != *kept || w.version(ana.Account.ID) != written {
		t.Fatalf("cancelled again: %+v %v", same, err)
	}
	if named, err := w.svc.UpdateAccount(ctx, who, contracts.AccountUpdate{Name: text("Ana")}); err != nil || named.Name != "Ana" {
		t.Fatalf("a change after cancelling: %+v %v", named, err)
	}
	// The days pass, and the account is still there.
	w.now = asked.Add(8 * 24 * time.Hour)
	if err := w.svc.RemoveDue(ctx); err != nil {
		t.Fatal(err)
	}
	if rec, err := w.svc.AccountRecord(ctx, ana.Account.ID); err != nil || rec.State != contracts.AccountActive || rec.Passkeys != 1 {
		t.Fatalf("an account that was kept, after its day: %+v %v", rec, err)
	}
	if me, _ := w.signIn("ana@example.org"); me.Account.ID != ana.Account.ID || me.Account.State != contracts.AccountActive {
		t.Fatalf("signing in to an account that was kept: %+v", me.Account)
	}

	for _, line := range []string{"accounts: account closing", "accounts: account kept"} {
		if strings.Count(w.logs.String(), line) != 1 {
			t.Errorf("the logs say %q %d times", line, strings.Count(w.logs.String(), line))
		}
	}
	w.quiet(ana.Account.ID, "example.org", "Ana")

	ghost := &Authenticated{AccountID: "acc_" + strings.Repeat("a", 26)}
	for what, err := range map[string]error{
		"deleting":   second(w.svc.StartDeletion(ctx, ghost)),
		"cancelling": second(w.svc.CancelDeletion(ctx, ghost)),
	} {
		if !errors.Is(err, contracts.ErrUnauthorized) {
			t.Errorf("%s an account that is not there: %v", what, err)
		}
	}
}

// An account that signs in with passkeys only and lost them is still let in
// by a recovery code while it is closing: its person may want to cancel.
func TestRecoveryWhileClosing(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	_, cookie := w.signUp("ana@example.org")
	who := w.who(cookie)
	w.addPasskey(who, webauthntest.New(webauthntest.ES256), "key")
	codes := w.passkeyOnly(who)
	if _, err := w.svc.StartDeletion(ctx, who); err != nil {
		t.Fatal(err)
	}
	me, recovered, err := w.recover("ana@example.org", codes[0])
	if err != nil || me.Account.State != contracts.AccountClosing {
		t.Fatalf("a recovery while closing: %+v %v", me, err)
	}
	if _, _, err := w.recover("ana@example.org", codes[0]); !errors.Is(err, contracts.ErrInvalidCode) {
		t.Fatalf("the same code again: %v", err)
	}
	kept, err := w.svc.CancelDeletion(ctx, w.who(recovered))
	if err != nil || kept.State != contracts.AccountActive {
		t.Fatalf("cancelling after a recovery: %+v %v", kept, err)
	}
	if list, err := w.svc.Passkeys(ctx, w.who(recovered)); err != nil || list.RecoveryCodesLeft != domain.RecoveryCodes-1 || !list.PasskeyOnly {
		t.Fatalf("the codes after it: %+v %v", list, err)
	}
}

// closing is a world where Ana asked to delete her account, which has a
// passkey and a session that is still used, and where Eve has an account she
// keeps.
type closing struct {
	*world
	ana, eve          *contracts.Me
	cookie, eveCookie string
	who               *Authenticated
	auth              *webauthntest.Authenticator
	key               *contracts.Passkey
	// day is when Ana's account is removed.
	day time.Time
}

func newClosing(t *testing.T) *closing {
	t.Helper()
	c := &closing{world: newWorld(t), auth: webauthntest.New(webauthntest.ES256)}
	ctx := context.Background()
	c.ana, c.cookie = c.signUp("ana@example.org")
	c.eve, c.eveCookie = c.signUp("eve@example.org")
	c.who = c.world.who(c.cookie)
	c.key = c.addPasskey(c.who, c.auth, "key")
	if _, err := c.svc.UpdateAccount(ctx, c.who, contracts.AccountUpdate{Name: text("Ana Closing")}); err != nil {
		t.Fatal(err)
	}
	got, err := c.svc.StartDeletion(ctx, c.who)
	if err != nil {
		t.Fatal(err)
	}
	c.day = got.ClosesAt
	// A day before, both are still using their sessions: seven days without
	// use would end them by themselves.
	c.now = c.day.Add(-24 * time.Hour)
	for _, cookie := range []string{c.cookie, c.eveCookie} {
		if _, err := c.svc.Authenticate(ctx, cookie); err != nil {
			t.Fatal(err)
		}
	}
	return c
}

// gone checks that nothing of Ana's account is left, in the storage or for
// anyone who tries to use it, and that Eve's is as it was.
func (c *closing) gone() {
	c.t.Helper()
	ctx := context.Background()
	if left := c.stored(c.ana.Account.ID, "ana@example.org", "Ana Closing"); len(left) != 0 {
		c.t.Fatalf("left in the storage: %q", left)
	}
	c.refused()
	if _, err := c.svc.AccountRecord(ctx, c.ana.Account.ID); !errors.Is(err, contracts.ErrNotFound) {
		c.t.Fatalf("the operator's tool on a removed account: %v", err)
	}
	c.others()
}

// refused checks that nothing leads into Ana's account.
func (c *closing) refused() {
	c.t.Helper()
	ctx := context.Background()
	if _, err := c.svc.Authenticate(ctx, c.cookie); !errors.Is(err, contracts.ErrUnauthorized) {
		c.t.Fatalf("a session of a removed account: %v", err)
	}
	if _, err := c.svc.Me(ctx, c.who); !errors.Is(err, contracts.ErrUnauthorized) {
		c.t.Fatalf("a request of a removed account: %v", err)
	}
	if me, cookie, err := c.signInWith(c.auth, c.key.ID); !errors.Is(err, contracts.ErrInvalidPasskey) || me != nil || cookie != "" {
		c.t.Fatalf("a passkey of a removed account: %v", err)
	}
	if _, mail := c.start("ana@example.org", ""); mail != nil {
		c.t.Fatal("a sign-in message for the address of a removed account")
	}
}

// others checks that Eve's account, the only one left, is as it was.
func (c *closing) others() {
	c.t.Helper()
	ctx := context.Background()
	if who, err := c.svc.Authenticate(ctx, c.eveCookie); err != nil || who.AccountID != c.eve.Account.ID {
		c.t.Fatalf("another account's session: %v", err)
	}
	list, err := c.svc.AccountRecords(ctx, "", 10)
	if err != nil || len(list.Accounts) != 1 || list.Accounts[0].ID != c.eve.Account.ID || list.Accounts[0].Sessions != 1 || list.Unreadable != 0 {
		c.t.Fatalf("the accounts that are left: %+v %v", list, err)
	}
}

// again signs Ana's address up again, and checks it got a new account.
func (c *closing) again() *contracts.Me {
	c.t.Helper()
	me, cookie := c.signUp("ana@example.org")
	if me.Account.ID == c.ana.Account.ID || me.Account.State != contracts.AccountActive || me.Account.Name != "" || !me.Account.CreatedAt.Equal(c.now) {
		c.t.Fatalf("the account of an address that signed up again: %+v", me.Account)
	}
	list, err := c.svc.Passkeys(context.Background(), c.world.who(cookie))
	if err != nil || len(list.Passkeys) != 0 {
		c.t.Fatalf("the new account has what the removed one had: %+v %v", list, err)
	}
	return me
}

// When its day comes an account is removed: by the enclave, which goes
// through the list of closing accounts, and at the moment anyone uses it.
func TestRemovingAnAccountThatIsDue(t *testing.T) {
	ctx := context.Background()
	uses := map[string]func(c *closing) error{
		"the enclave goes through the list": func(c *closing) error { return c.svc.RemoveDue(ctx) },
		"its session is used": func(c *closing) error {
			_, err := c.svc.Authenticate(ctx, c.cookie)
			return wantErr(err, contracts.ErrUnauthorized)
		},
		"a request of its session goes on": func(c *closing) error {
			_, err := c.svc.Me(ctx, c.who)
			return wantErr(err, contracts.ErrUnauthorized)
		},
		"a change is asked": func(c *closing) error {
			_, err := c.svc.UpdateAccount(ctx, c.who, contracts.AccountUpdate{Name: text("too late")})
			return wantErr(err, contracts.ErrUnauthorized)
		},
		"cancelling comes too late": func(c *closing) error {
			_, err := c.svc.CancelDeletion(ctx, c.who)
			return wantErr(err, contracts.ErrUnauthorized)
		},
		"deleting is asked again": func(c *closing) error {
			_, err := c.svc.StartDeletion(ctx, c.who)
			return wantErr(err, contracts.ErrUnauthorized)
		},
		"its address asks to sign in": func(c *closing) error {
			if _, mail := c.start("ana@example.org", ""); mail != nil {
				return errors.New("a sign-in message was sent")
			}
			return nil
		},
		"its passkey is used": func(c *closing) error {
			_, _, err := c.signInWith(c.auth, c.key.ID)
			return wantErr(err, contracts.ErrInvalidPasskey)
		},
		"the account is read by its identifier": func(c *closing) error {
			_, err := c.svc.Account(ctx, c.ana.Account.ID)
			return wantErr(err, contracts.ErrNotFound)
		},
	}
	for name, use := range uses {
		t.Run(name, func(t *testing.T) {
			c := newClosing(t)
			// A second before its day nothing happens to it.
			c.now = c.day.Add(-time.Second)
			if err := c.svc.RemoveDue(ctx); err != nil {
				t.Fatal(err)
			}
			if me, err := c.svc.Me(ctx, c.who); err != nil || me.Account.State != contracts.AccountClosing {
				t.Fatalf("a second before its day: %+v %v", me, err)
			}
			if _, err := c.svc.Authenticate(ctx, c.cookie); err != nil {
				t.Fatalf("its session a second before its day: %v", err)
			}
			// Not even its own entry removes it before the record says so.
			if err := c.svc.settle(ctx, c.ana.Account.ID, timedKey(c.day, c.ana.Account.ID)); err != nil || len(c.listed(closingPartition)) != 1 {
				t.Fatalf("its entry acted on before its day: %v", err)
			}
			if rec, err := c.svc.AccountRecord(ctx, c.ana.Account.ID); err != nil || rec.Sessions != 1 {
				t.Fatalf("after its entry was acted on before its day: %+v %v", rec, err)
			}
			// The operator's tool looks at an account that is due, and
			// looking removes nothing.
			c.now = c.day
			if rec, err := c.svc.AccountRecord(ctx, c.ana.Account.ID); err != nil || rec.State != contracts.AccountClosing || !rec.ClosesAt.Equal(c.day) {
				t.Fatalf("the operator's tool on an account that is due: %+v %v", rec, err)
			}
			if list, err := c.svc.AccountRecords(ctx, "", 10); err != nil || len(list.Accounts) != 2 {
				t.Fatalf("the list with an account that is due: %+v %v", list, err)
			}
			if len(c.stored(c.ana.Account.ID)) == 0 {
				t.Fatal("looking at an account removed it")
			}

			if err := use(c); err != nil {
				t.Fatal(err)
			}
			c.gone()
			if n := strings.Count(c.logs.String(), "accounts: account removed"); n != 1 {
				t.Fatalf("the logs say an account was removed %d times", n)
			}
			// Going through the list again finds nothing to do.
			if err := c.svc.RemoveDue(ctx); err != nil {
				t.Fatal(err)
			}
			// The address signs up again, with an invitation, to a new account.
			c.again()
			c.refused2()
			c.quiet(c.ana.Account.ID, c.eve.Account.ID, "example.org", "Ana Closing")
		})
	}
}

// refused2 checks that what led into the removed account still leads nowhere
// once its address has a new one.
func (c *closing) refused2() {
	c.t.Helper()
	ctx := context.Background()
	if _, err := c.svc.Authenticate(ctx, c.cookie); !errors.Is(err, contracts.ErrUnauthorized) {
		c.t.Fatalf("a session of the removed account, after signing up again: %v", err)
	}
	if _, _, err := c.signInWith(c.auth, c.key.ID); !errors.Is(err, contracts.ErrInvalidPasskey) {
		c.t.Fatalf("a passkey of the removed account, after signing up again: %v", err)
	}
}

// wantErr is nil when err is the error wanted.
func wantErr(err, want error) error {
	if !errors.Is(err, want) {
		return errors.New("got " + fmt.Sprint(err) + ", want " + want.Error())
	}
	return nil
}

// Whoever runs the storage kept copies of everything: the account's record
// in every version, the pointer from its address, its sessions, its entries
// in the lists. Put back after the account was removed, none of it brings
// the account back, and its address signs up again to a new one.
func TestARemovedAccountDoesNotComeBack(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	ana, cookie := w.signUp("ana@example.org")
	_, eveCookie := w.signUp("eve@example.org")
	who := w.who(cookie)
	auth := webauthntest.New(webauthntest.ES256)
	key := w.addPasskey(who, auth, "key")
	copies := []snapshot{w.snapshot()} // in use, before anything was asked
	got, err := w.svc.StartDeletion(ctx, who)
	if err != nil {
		t.Fatal(err)
	}
	w.now = got.ClosesAt.Add(-24 * time.Hour)
	for _, c := range []string{cookie, eveCookie} {
		if _, err := w.svc.Authenticate(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	copies = append(copies, w.snapshot()) // closing, with a session in use
	w.now = got.ClosesAt
	copies = append(copies, w.snapshot()) // due
	if err := w.svc.RemoveDue(ctx); err != nil {
		t.Fatal(err)
	}
	if left := w.stored(ana.Account.ID, "ana@example.org"); len(left) != 0 {
		t.Fatalf("left in the storage: %q", left)
	}

	back := func(t *testing.T, svc *Service) {
		t.Helper()
		if _, err := svc.Authenticate(ctx, cookie); !errors.Is(err, contracts.ErrUnauthorized) {
			t.Fatalf("a session put back: %v", err)
		}
		if _, err := svc.Me(ctx, who); !errors.Is(err, contracts.ErrUnauthorized) {
			t.Fatalf("a request of the account: %v", err)
		}
		if _, err := svc.AccountRecord(ctx, ana.Account.ID); !errors.Is(err, contracts.ErrNotFound) {
			t.Fatalf("the operator's tool: %v", err)
		}
		list, err := svc.AccountRecords(ctx, "", 10)
		if err != nil || len(list.Accounts) != 1 || list.Accounts[0].Address != "eve@example.org" || list.Unreadable != 0 {
			t.Fatalf("the list of accounts: %+v %v", list, err)
		}
		started, err := svc.StartPasskeySignIn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		in := contracts.PasskeySignIn{Ceremony: started.Ceremony}
		in.Credential.RawID = key.ID
		r := &in.Credential.Response
		r.ClientDataJSON, r.AuthenticatorData, r.Signature, r.UserHandle = auth.Get(started.Options.RPID, testOrigin, started.Options.Challenge, key.ID)
		if me, c, err := svc.FinishPasskeySignIn(ctx, in, ""); !errors.Is(err, contracts.ErrInvalidPasskey) || me != nil || c != "" {
			t.Fatalf("a passkey of the account: %v", err)
		}
	}
	for i, snap := range copies {
		w.putBack(snap)
		// The record's copies also where a record from before commits would
		// be, and where the value of the commit that says it is gone would.
		for _, r := range snap.records {
			if strings.HasPrefix(r.Name, recordName(ana.Account.ID)+"@") {
				plain := r
				plain.Name = recordName(ana.Account.ID)
				w.mem.SetRecord(plain)
				last := r
				last.Name = fmt.Sprintf("%s@%020d-%016x", recordName(ana.Account.ID), w.version(ana.Account.ID), 0)
				w.mem.SetRecord(last)
			}
		}
		if len(w.stored(ana.Account.ID)) == 0 {
			t.Fatal("nothing was put back")
		}
		back(t, w.svc)
		if _, mail := w.start("ana@example.org", ""); mail != nil {
			t.Fatalf("copy %d: a sign-in message for the address of a removed account", i)
		}
		// An enclave that just started knows no more than the commits say.
		restarted := w.service(w.svc.cfg.Keys)
		back(t, restarted)
		// Going through the list, with the entry of the closing account put
		// back, removes no one and deletes what was put back under its name.
		if err := restarted.RemoveDue(ctx); err != nil {
			t.Fatal(err)
		}
		back(t, restarted)
		w.now = w.now.Add(2 * time.Hour) // an address is sent five messages an hour
	}
	if _, err := w.svc.Authenticate(ctx, eveCookie); err != nil {
		t.Fatalf("another account: %v", err)
	}

	// The address signs up again over the pointer that was put back, and
	// gets a new account; what was put back still leads nowhere.
	w.putBack(copies[1])
	again, _ := w.signUp("ana@example.org")
	if again.Account.ID == ana.Account.ID || again.Account.State != contracts.AccountActive {
		t.Fatalf("signing up again: %+v", again.Account)
	}
	if _, err := w.svc.Authenticate(ctx, cookie); !errors.Is(err, contracts.ErrUnauthorized) {
		t.Fatalf("a session of the removed account, after signing up again: %v", err)
	}
	if me, _ := w.signIn("ana@example.org"); me.Account.ID != again.Account.ID {
		t.Fatalf("the address leads to %s", me.Account.ID)
	}
	list, err := w.svc.AccountRecords(ctx, "", 10)
	if err != nil || len(list.Accounts) != 2 || list.Accounts[1].ID != again.Account.ID || list.Unreadable != 0 {
		t.Fatalf("the list of accounts: %+v %v", list, err)
	}
}

// The two lists prove nothing. An entry written by hand, or an old one put
// back, shows nothing in the operator's tool and removes nothing; an entry
// that was taken away does not keep an account that is due once it is used.
func TestTheListsProveNothing(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	ana, cookie := w.signUp("ana@example.org")
	w.now = w.now.Add(time.Minute)
	eve, _ := w.signUp("eve@example.org")
	who := w.who(cookie)
	key, err := w.svc.signingKey(ctx)
	if err != nil {
		t.Fatal(err)
	}
	past := w.now.Add(-time.Hour)
	ghost := "acc_" + strings.Repeat("a", 26)
	pointer, err := w.mem.GetEntry(ctx, loginPartition("ana@example.org"), loginKey)
	if err != nil {
		t.Fatal(err)
	}
	own := w.listed(accountsPartition)
	if len(own) != 2 || own[0] != timedKey(ana.Account.CreatedAt, ana.Account.ID) {
		t.Fatalf("the list of accounts: %q", own)
	}
	genuine, err := w.mem.GetEntry(ctx, accountsPartition, own[0])
	if err != nil || pointed(key, genuine) != ana.Account.ID {
		t.Fatalf("an entry of the list: %+v %v", genuine, err)
	}

	// Written by hand into both lists: entries without the enclave's key
	// that name an account, one that is not there, and nothing; Ana's own
	// pointer and her own entry copied to other keys; keys that are not keys.
	var junk []string
	byHand := func(partition string) {
		for k, v := range map[string][]byte{
			timedKey(past, ana.Account.ID):                       []byte(`{"doc":{"account":"` + ana.Account.ID + `"},"mac":"AAAA"}`),
			timedKey(past.Add(time.Second), ana.Account.ID):      pointer.Value,
			timedKey(past.Add(time.Minute), ana.Account.ID):      genuine.Value,
			timedKey(past, eve.Account.ID):                       genuine.Value,
			timedKey(past, ghost):                                []byte(`{"doc":{"account":"` + ghost + `"},"mac":""}`),
			"20261111T080000.000000000Z." + ana.Account.ID + "x": genuine.Value,
			"20261111T080000Z." + ana.Account.ID:                 genuine.Value,
			"yesterday." + ana.Account.ID:                        genuine.Value,
			"00000000":                                           []byte(`not JSON`),
			"zzzz":                                               nil,
		} {
			w.mem.SetEntry(store.Entry{Partition: partition, Key: k, Value: v, Version: 1})
			junk = append(junk, k)
		}
	}
	byHand(accountsPartition)
	byHand(closingPartition)
	before := w.snapshot()

	list, err := w.svc.AccountRecords(ctx, "", 100)
	if err != nil || len(list.Accounts) != 2 || list.Accounts[0].ID != ana.Account.ID || list.Accounts[1].ID != eve.Account.ID || list.Unreadable != 0 || list.Next != "" {
		t.Fatalf("the operator's list with entries written by hand: %+v %v", list, err)
	}
	if err := w.svc.RemoveDue(ctx); err != nil {
		t.Fatal(err)
	}
	after := w.snapshot()
	if len(after.records) != len(before.records) || len(after.entries) != len(before.entries) {
		t.Fatalf("entries written by hand removed something: %d records and %d entries, of %d and %d",
			len(after.records), len(after.entries), len(before.records), len(before.entries))
	}
	if me, err := w.svc.Me(ctx, who); err != nil || me.Account.State != contracts.AccountActive {
		t.Fatalf("an account named by an entry written by hand: %+v %v", me, err)
	}

	// An entry the enclave did write, for a deletion that was cancelled, put
	// back: when its day comes it is dropped, and the account stays.
	first, err := w.svc.StartDeletion(ctx, who)
	if err != nil {
		t.Fatal(err)
	}
	old, err := w.mem.GetEntry(ctx, closingPartition, timedKey(first.ClosesAt, ana.Account.ID))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.CancelDeletion(ctx, who); err != nil {
		t.Fatal(err)
	}
	w.mem.SetEntry(*old)
	w.now = first.ClosesAt.Add(-48 * time.Hour)
	if _, err := w.svc.Authenticate(ctx, cookie); err != nil {
		t.Fatal(err)
	}
	// Asked again two days before that day: the account has another day now.
	second, err := w.svc.StartDeletion(ctx, who)
	if err != nil || !second.ClosesAt.After(first.ClosesAt) {
		t.Fatalf("asked again: %+v %v", second, err)
	}
	w.now = first.ClosesAt.Add(time.Minute)
	if err := w.svc.RemoveDue(ctx); err != nil {
		t.Fatal(err)
	}
	if rec, err := w.svc.AccountRecord(ctx, ana.Account.ID); err != nil || rec.State != contracts.AccountClosing || !rec.ClosesAt.Equal(second.ClosesAt) {
		t.Fatalf("an account whose earlier entry was put back: %+v %v", rec, err)
	}
	if _, err := w.mem.GetEntry(ctx, closingPartition, old.Key); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("the entry of a day the account does not have: %v", err)
	}
	// The same entry for an account that is in use again.
	if _, err := w.svc.CancelDeletion(ctx, who); err != nil {
		t.Fatal(err)
	}
	w.mem.SetEntry(*old)
	if err := w.svc.RemoveDue(ctx); err != nil {
		t.Fatal(err)
	}
	if rec, err := w.svc.AccountRecord(ctx, ana.Account.ID); err != nil || rec.State != contracts.AccountActive {
		t.Fatalf("an account in use whose earlier entry was put back: %+v %v", rec, err)
	}
	if _, err := w.mem.GetEntry(ctx, closingPartition, old.Key); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("the entry of an account that is in use: %v", err)
	}

	// The entry of a closing account taken away: the enclave does not find
	// the account when its day comes, and the first use of it removes it.
	third, err := w.svc.StartDeletion(ctx, who)
	if err != nil {
		t.Fatal(err)
	}
	if err := w.mem.DeleteEntry(ctx, closingPartition, timedKey(third.ClosesAt, ana.Account.ID)); err != nil {
		t.Fatal(err)
	}
	w.now = third.ClosesAt.Add(-time.Hour)
	_, cookie = w.signIn("ana@example.org") // a session that is still good when the day comes
	w.now = third.ClosesAt
	if err := w.svc.RemoveDue(ctx); err != nil {
		t.Fatal(err)
	}
	if len(w.stored("ana@example.org")) == 0 {
		t.Fatal("the account was found without its entry")
	}
	if _, err := w.svc.Authenticate(ctx, cookie); !errors.Is(err, contracts.ErrUnauthorized) {
		t.Fatalf("a session of an account that is due: %v", err)
	}
	for _, k := range junk { // what was written by hand above is the hand's to take away
		_ = w.mem.DeleteEntry(ctx, accountsPartition, k)
		_ = w.mem.DeleteEntry(ctx, closingPartition, k)
	}
	if left := w.stored(ana.Account.ID, "ana@example.org"); len(left) != 0 {
		t.Fatalf("left of an account that was due and used: %q", left)
	}

	// An entry of the list of accounts taken away hides the account from the
	// list, which is all it does: the account is there for who asks for it.
	if err := w.mem.DeleteEntry(ctx, accountsPartition, own[1]); err != nil {
		t.Fatal(err)
	}
	if list, err := w.svc.AccountRecords(ctx, "", 100); err != nil || len(list.Accounts) != 0 {
		t.Fatalf("the list without its entries: %+v %v", list, err)
	}
	if rec, err := w.svc.AccountRecord(ctx, eve.Account.ID); err != nil || rec.Address != "eve@example.org" {
		t.Fatalf("an account without its entry: %+v %v", rec, err)
	}
}

// brokenTable is the table of the accounts' records when nothing can be
// deleted from it.
type brokenTable struct {
	store.RecordTable
	err error
}

func (b brokenTable) DeleteRecord(context.Context, string) error { return b.err }

// brokenCommits is the store of commits when nothing can be added to it.
type brokenCommits struct {
	store.Commits
	err error
}

func (b brokenCommits) Append(context.Context, store.Commit) (*store.Commit, error) {
	return nil, b.err
}

// A removal that fails at any step leaves an account nobody can use, and is
// done again from the start until nothing is left.
func TestRemovalFailures(t *testing.T) {
	ctx := context.Background()
	down := errors.New("down")
	type step struct {
		name string
		// fail makes the step fail, and returns how to make it work again.
		fail func(c *closing, f *failing) (lift func())
		// recordLeft says the failure comes before the record is committed
		// as gone; pointerLeft, before the pointer from the address is deleted.
		recordLeft, pointerLeft bool
		// noSignUp says nobody can sign up while the step fails.
		noSignUp bool
	}
	op := func(name string) func(*closing, *failing) func() {
		return func(_ *closing, f *failing) func() {
			f.fail[name] = down
			return func() { delete(f.fail, name) }
		}
	}
	steps := []step{
		{"the key that signs", op("derive"), true, true, true},
		{"reading the pointer of the address", op("get login#email#ana@example.org"), true, true, true},
		{"deleting the pointer of the address", op("delete login#email#ana@example.org"), true, true, true},
		{"deleting the entry in the list of accounts", op("delete " + accountsPartition), true, false, false},
		{"deleting the sessions", op("delete partition"), true, false, false},
		{"removing the record", op("remove record"), true, false, false},
		{"committing that the record is gone", func(c *closing, _ *failing) func() {
			commits := c.anchored.Commits
			c.anchored.Commits = brokenCommits{commits, down}
			return func() { c.anchored.Commits = commits }
		}, true, false, true}, // an account is made with a commit
		{"deleting the versions of the record", func(c *closing, _ *failing) func() {
			c.anchored.Records = brokenTable{c.mem, down}
			return func() { c.anchored.Records = c.mem }
		}, false, false, false},
		{"deleting the entry in the list of closing accounts", op("delete " + closingPartition), false, false, false},
	}
	for _, s := range steps {
		for _, how := range []string{"found by the enclave", "used"} {
			t.Run(s.name+", "+how, func(t *testing.T) {
				c := newClosing(t)
				f := &failing{Records: c.svc.cfg.Records, Entries: c.mem, Keys: c.svc.cfg.Keys, fail: map[string]error{}}
				c.svc.cfg.Records, c.svc.cfg.Entries, c.svc.cfg.Keys = f, f, f
				c.now = c.day
				lift := s.fail(c, f)

				// The removal fails, and says so.
				if how == "used" {
					if _, err := c.svc.Me(ctx, c.who); !errors.Is(err, down) {
						t.Fatalf("using the account: %v", err)
					}
				} else if err := c.svc.RemoveDue(ctx); !errors.Is(err, down) {
					t.Fatalf("going through the list: %v", err)
				}
				// While it fails, nothing leads into the account: not its
				// session, not a request of it, not its passkey, not its address.
				unusable := func() {
					t.Helper()
					if who, err := c.svc.Authenticate(ctx, c.cookie); err == nil || who != nil {
						t.Fatalf("a session of an account half removed: %+v", who)
					}
					if me, err := c.svc.Me(ctx, c.who); err == nil || me != nil {
						t.Fatalf("a request of an account half removed: %+v", me)
					}
					if _, err := c.svc.UpdateAccount(ctx, c.who, contracts.AccountUpdate{Name: text("x")}); err == nil {
						t.Fatal("a change of an account half removed")
					}
					if _, err := c.svc.CancelDeletion(ctx, c.who); err == nil {
						t.Fatal("an account half removed was kept")
					}
					if me, cookie, err := c.signInWith(c.auth, c.key.ID); err == nil || me != nil || cookie != "" {
						t.Fatalf("a passkey of an account half removed: %+v", me)
					}
					if _, mail := c.start("ana@example.org", ""); mail != nil {
						t.Fatal("a sign-in message for an account half removed")
					}
				}
				unusable()
				unusable() // and trying again changes nothing of that

				// What the failure left: the record, with everything under it,
				// or no record at all. Never a record without its day.
				rec, err := c.svc.AccountRecord(ctx, c.ana.Account.ID)
				switch {
				case s.name == "the key that signs":
					if !errors.Is(err, down) {
						t.Fatalf("the record without the key: %v", err)
					}
				case s.recordLeft:
					if err != nil || rec.State != contracts.AccountClosing || !rec.ClosesAt.Equal(c.day) {
						t.Fatalf("the record after the failure: %+v %v", rec, err)
					}
				case !errors.Is(err, contracts.ErrNotFound):
					t.Fatalf("the record after the failure: %+v %v", rec, err)
				}
				_, err = c.mem.GetEntry(ctx, loginPartition("ana@example.org"), loginKey)
				if s.pointerLeft != (err == nil) {
					t.Fatalf("the pointer of the address after the failure: %v", err)
				}
				// The entry that the enclave finds the account by is still
				// there, whatever else is: it goes last.
				if keys := c.listed(closingPartition); len(keys) != 1 {
					t.Fatalf("the list of closing accounts after the failure: %q", keys)
				}

				// Once the pointer is gone the address can sign up again, with
				// an invitation, even while the rest cannot be removed.
				var again *contracts.Me
				if !s.noSignUp {
					c.now = c.now.Add(2 * time.Hour)
					again = c.again()
					unusableAgain := func() {
						t.Helper()
						if who, err := c.svc.Authenticate(ctx, c.cookie); err == nil || who != nil {
							t.Fatalf("a session of the account half removed, after signing up again: %+v", who)
						}
						if me, cookie, err := c.signInWith(c.auth, c.key.ID); err == nil || me != nil || cookie != "" {
							t.Fatalf("a passkey of the account half removed, after signing up again: %+v", me)
						}
					}
					unusableAgain()
				}

				// The step works again: the enclave finishes, and nothing is left.
				lift()
				if err := c.svc.RemoveDue(ctx); err != nil {
					t.Fatal(err)
				}
				if left := c.stored(c.ana.Account.ID, "Ana Closing"); len(left) != 0 {
					t.Fatalf("left in the storage: %q", left)
				}
				if _, err := c.svc.AccountRecord(ctx, c.ana.Account.ID); !errors.Is(err, contracts.ErrNotFound) {
					t.Fatalf("the record once the removal is done: %v", err)
				}
				if again == nil {
					if left := c.stored("ana@example.org"); len(left) != 0 {
						t.Fatalf("left of the address: %q", left)
					}
					c.refused()
					again = c.again()
				}
				// The new account of the address is whole: its pointer was
				// not taken for the removed account's.
				if me, _ := c.signIn("ana@example.org"); me.Account.ID != again.Account.ID {
					t.Fatalf("the address leads to %s", me.Account.ID)
				}
				c.refused2()
				if _, err := c.svc.Authenticate(ctx, c.eveCookie); err != nil {
					t.Fatalf("another account: %v", err)
				}
				c.quiet(c.ana.Account.ID, "example.org", "Ana Closing")
			})
		}
	}

	// The storage fails where the list is read, and where an account is read.
	c := newClosing(t)
	f := &failing{Records: c.svc.cfg.Records, Entries: c.mem, Keys: c.svc.cfg.Keys, fail: map[string]error{}}
	c.svc.cfg.Records, c.svc.cfg.Entries, c.svc.cfg.Keys = f, f, f
	c.now = c.day
	for _, name := range []string{"list " + closingPartition, "get record"} {
		f.fail[name] = down
		if err := c.svc.RemoveDue(ctx); !errors.Is(err, down) {
			t.Fatalf("%s down: %v", name, err)
		}
		delete(f.fail, name)
	}
	if rec, err := c.svc.AccountRecord(ctx, c.ana.Account.ID); err != nil || rec.State != contracts.AccountClosing {
		t.Fatalf("after failures to read: %+v %v", rec, err)
	}
	// An account that another request removes first is removed once.
	if err := c.svc.remove(ctx, c.ana.Account.ID); err != nil {
		t.Fatal(err)
	}
	if err := c.svc.remove(ctx, c.ana.Account.ID); err != nil {
		t.Fatalf("removing what is removed: %v", err)
	}
	c.gone()
}

// Asking to delete, cancelling and naming fail whole or not at all, and what
// a failure leaves in the lists is dropped.
func TestSettingsFailures(t *testing.T) {
	ctx := context.Background()
	down := errors.New("down")
	setup := func(t *testing.T) (*world, *failing, *contracts.Me, *Authenticated, string) {
		t.Helper()
		w := newWorld(t)
		f := &failing{Records: w.svc.cfg.Records, Entries: w.mem, Keys: w.svc.cfg.Keys, fail: map[string]error{}}
		w.svc.cfg.Records, w.svc.cfg.Entries, w.svc.cfg.Keys = f, f, f
		me, cookie := w.signUp("ana@example.org")
		_, phone := w.signIn("ana@example.org")
		return w, f, me, w.who(cookie), phone
	}

	// Asking, before the record says so: the account is in use, and what
	// was written to the list is dropped when its day comes.
	for _, name := range []string{"get record", "derive", "put entry", "put record"} {
		w, f, me, who, phone := setup(t)
		asked := w.now
		f.fail[name] = down
		if _, err := w.svc.StartDeletion(ctx, who); !errors.Is(err, down) {
			t.Fatalf("%s down: asking: %v", name, err)
		}
		if _, err := w.svc.UpdateAccount(ctx, who, contracts.AccountUpdate{Name: text("Ana")}); !errors.Is(err, down) && name != "put entry" {
			t.Fatalf("%s down: naming: %v", name, err)
		}
		delete(f.fail, name)
		if rec, err := w.svc.AccountRecord(ctx, me.Account.ID); err != nil || rec.State != contracts.AccountActive || rec.Sessions != 2 || (rec.Name != "" && name != "put entry") {
			t.Fatalf("%s down: the account afterwards: %+v %v", name, rec, err)
		}
		if _, err := w.svc.Authenticate(ctx, phone); err != nil {
			t.Fatalf("%s down: another device: %v", name, err)
		}
		if keys := w.listed(closingPartition); (len(keys) == 1) != (name == "put record") {
			t.Fatalf("%s down: the list of closing accounts: %q", name, keys)
		}
		w.now = asked.Add(domain.AccountClosing)
		if err := w.svc.RemoveDue(ctx); err != nil {
			t.Fatal(err)
		}
		if rec, err := w.svc.AccountRecord(ctx, me.Account.ID); err != nil || rec.State != contracts.AccountActive || len(w.listed(closingPartition)) != 0 {
			t.Fatalf("%s down: the account after the day it asked for: %+v %v, list %q", name, rec, err, w.listed(closingPartition))
		}
	}
	// Asked again in the same second, over the entry a failure left.
	w, f, me, who, phone := setup(t)
	f.fail["put record"] = down
	if _, err := w.svc.StartDeletion(ctx, who); !errors.Is(err, down) {
		t.Fatal(err)
	}
	delete(f.fail, "put record")
	// The record says closing and the other devices cannot be signed out:
	// asking again does it.
	for _, name := range []string{"list entries", "delete " + accountPartition(me.Account.ID)} {
		f.fail[name] = down
		if _, err := w.svc.StartDeletion(ctx, who); !errors.Is(err, down) {
			t.Fatalf("%s down: asking: %v", name, err)
		}
		delete(f.fail, name)
		if rec, err := w.svc.AccountRecord(ctx, me.Account.ID); err != nil || rec.State != contracts.AccountClosing || rec.Sessions != 2 {
			t.Fatalf("%s down: the account afterwards: %+v %v", name, rec, err)
		}
	}
	got, err := w.svc.StartDeletion(ctx, who)
	if err != nil || got.State != contracts.AccountClosing || len(w.listed(closingPartition)) != 1 {
		t.Fatalf("asked again: %+v %v", got, err)
	}
	if _, err := w.svc.Authenticate(ctx, phone); !errors.Is(err, contracts.ErrUnauthorized) {
		t.Fatalf("another device after asking again: %v", err)
	}
	if n := strings.Count(w.logs.String(), "accounts: account closing"); n != 1 {
		t.Fatalf("the logs say an account is closing %d times", n)
	}

	// Cancelling: the record is not written, and the account is still closing.
	for _, name := range []string{"get record", "derive", "put record"} {
		f.fail[name] = down
		if _, err := w.svc.CancelDeletion(ctx, who); !errors.Is(err, down) {
			t.Fatalf("%s down: cancelling: %v", name, err)
		}
		delete(f.fail, name)
		if rec, err := w.svc.AccountRecord(ctx, me.Account.ID); err != nil || rec.State != contracts.AccountClosing {
			t.Fatalf("%s down: the account afterwards: %+v %v", name, rec, err)
		}
	}
	// The record is written and the entry cannot be deleted: the account is
	// kept, and the entry is dropped when its day comes.
	f.fail["delete "+closingPartition] = down
	kept, err := w.svc.CancelDeletion(ctx, who)
	if err != nil || kept.State != contracts.AccountActive || len(w.listed(closingPartition)) != 1 {
		t.Fatalf("cancelling with the list down: %+v %v", kept, err)
	}
	if !strings.Contains(w.logs.String(), "an entry of the list of closing accounts could not be deleted") {
		t.Fatalf("logs:\n%s", w.logs)
	}
	w.now = got.ClosesAt
	if err := w.svc.RemoveDue(ctx); !errors.Is(err, down) {
		t.Fatalf("dropping the entry with the list down: %v", err)
	}
	delete(f.fail, "delete "+closingPartition)
	if err := w.svc.RemoveDue(ctx); err != nil {
		t.Fatal(err)
	}
	if rec, err := w.svc.AccountRecord(ctx, me.Account.ID); err != nil || rec.State != contracts.AccountActive || len(w.listed(closingPartition)) != 0 {
		t.Fatalf("the account that was kept, after the day: %+v %v", rec, err)
	}
	w.quiet(me.Account.ID, "example.org")
}

// The operator's tool shows what the signed records say, a page at a time,
// the oldest account first.
func TestTheOperatorsTool(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	var made []*contracts.Me
	var cookies []string
	for i, address := range []string{"ana@example.org", "bea@example.org", "cho@example.org", "dan@example.org", "eve@example.org"} {
		w.now = w.now.Add(time.Duration(i+1) * time.Minute)
		me, cookie := w.signUp(address)
		made, cookies = append(made, me), append(cookies, cookie)
	}
	ana := w.who(cookies[0])
	w.addPasskey(ana, webauthntest.New(webauthntest.ES256), "one")
	w.addPasskey(ana, webauthntest.New(webauthntest.ES256), "two")
	w.passkeyOnly(ana)
	if _, err := w.svc.UpdateAccount(ctx, w.who(cookies[1]), contracts.AccountUpdate{Name: text("Bea")}); err != nil {
		t.Fatal(err)
	}
	w.signIn("bea@example.org")
	closing, err := w.svc.StartDeletion(ctx, w.who(cookies[2]))
	if err != nil {
		t.Fatal(err)
	}

	rec, err := w.svc.AccountRecord(ctx, made[0].Account.ID)
	want := contracts.AccountRecord{
		ID: made[0].Account.ID, State: contracts.AccountActive, Address: "ana@example.org", Language: "en",
		CreatedAt: made[0].Account.CreatedAt, AddressVerifiedAt: made[0].Account.CreatedAt,
		Passkeys: 2, PasskeyOnly: true, Sessions: 1,
	}
	if err != nil || *rec != want {
		t.Fatalf("an account: %+v %v, want %+v", rec, err, want)
	}
	for _, id := range []string{"", "acc_x", "acc_" + strings.Repeat("a", 26), "../" + made[0].Account.ID, made[0].Session.ID} {
		if _, err := w.svc.AccountRecord(ctx, id); !errors.Is(err, contracts.ErrNotFound) {
			t.Errorf("account %q: %v", id, err)
		}
	}

	// Two at a time, in the order they were made.
	var seen []contracts.AccountRecord
	after, pages := "", 0
	for {
		page, err := w.svc.AccountRecords(ctx, after, 2)
		if err != nil || len(page.Accounts) > 2 || page.Unreadable != 0 {
			t.Fatalf("a page: %+v %v", page, err)
		}
		seen, pages = append(seen, page.Accounts...), pages+1
		if after = page.Next; after == "" {
			break
		}
	}
	if len(seen) != 5 || pages != 3 {
		t.Fatalf("%d accounts in %d pages", len(seen), pages)
	}
	for i, r := range seen {
		if r.ID != made[i].Account.ID || r.Address != made[i].Account.Address || !r.CreatedAt.Equal(made[i].Account.CreatedAt) {
			t.Fatalf("account %d of the list: %+v", i, r)
		}
	}
	if seen[0] != want || seen[1].Name != "Bea" || seen[1].Sessions != 2 || seen[1].Passkeys != 0 || seen[1].PasskeyOnly {
		t.Fatalf("the list: %+v", seen[:2])
	}
	if seen[2].State != contracts.AccountClosing || !seen[2].ClosesAt.Equal(closing.ClosesAt) || !seen[3].ClosesAt.IsZero() {
		t.Fatalf("a closing account in the list: %+v", seen[2])
	}
	// A page that ends exactly where the list does says there may be more,
	// and the page after it is empty.
	page, err := w.svc.AccountRecords(ctx, "", 5)
	if err != nil || len(page.Accounts) != 5 || page.Next == "" {
		t.Fatalf("a full page: %+v %v", page, err)
	}
	if last, err := w.svc.AccountRecords(ctx, page.Next, 5); err != nil || len(last.Accounts) != 0 || last.Next != "" || last.Accounts == nil {
		t.Fatalf("the page after the last: %+v %v", last, err)
	}
	if all, err := w.svc.AccountRecords(ctx, "", domain.MaxAccountsPage); err != nil || len(all.Accounts) != 5 || all.Next != "" {
		t.Fatalf("the largest page: %+v %v", all, err)
	}
	for _, limit := range []int{0, -1, domain.MaxAccountsPage + 1} {
		if _, err := w.svc.AccountRecords(ctx, "", limit); !errors.Is(err, contracts.ErrInvalidLimit) {
			t.Errorf("limit %d: %v", limit, err)
		}
	}
	for _, after := range []string{"x", made[0].Account.ID, "20261111T090000.000000000Z", "20261111T090000.000000000Z.acc_x", "20261111T090000Z." + made[0].Account.ID, "2026-11-11T09:00:00Z." + made[0].Account.ID} {
		if _, err := w.svc.AccountRecords(ctx, after, 2); !errors.Is(err, contracts.ErrInvalidCursor) {
			t.Errorf("after %q: %v", after, err)
		}
	}

	// What is shown is what the enclave signed, not what the table says. A
	// record changed by hand, to another name or to no deletion, is not
	// shown as changed: it is an account that cannot be read, and counted.
	for i, change := range [][2]string{{`"name":"Bea"`, `"name":"Eve's friend"`}, {`"state":"closing"`, `"state":"active"`}} {
		name := w.newest(made[i+1].Account.ID)
		forged := w.record(name)
		forged.Value = []byte(strings.Replace(string(forged.Value), change[0], change[1], 1))
		if string(forged.Value) == string(w.record(name).Value) {
			t.Fatalf("the record was not changed: %s", forged.Value)
		}
		w.mem.SetRecord(forged)
	}
	page, err = w.svc.AccountRecords(ctx, "", 10)
	if err != nil || len(page.Accounts) != 3 || page.Unreadable != 2 || page.Accounts[0].ID != made[0].Account.ID || page.Accounts[1].ID != made[3].Account.ID {
		t.Fatalf("the list with records changed by hand: %+v %v", page, err)
	}
	if rec, err := w.svc.AccountRecord(ctx, made[1].Account.ID); err == nil || errors.Is(err, contracts.ErrNotFound) || rec != nil {
		t.Fatalf("a record changed by hand: %+v %v", rec, err)
	}
	// A record deleted by hand is not an account that is not there either.
	_ = w.mem.DeleteRecord(ctx, w.newest(made[3].Account.ID))
	if page, err = w.svc.AccountRecords(ctx, "", 10); err != nil || len(page.Accounts) != 2 || page.Unreadable != 3 {
		t.Fatalf("the list with a record deleted by hand: %+v %v", page, err)
	}
	if _, err := w.svc.AccountRecord(ctx, made[3].Account.ID); !errors.Is(err, store.ErrLost) {
		t.Fatalf("a record deleted by hand: %v", err)
	}
	if !strings.Contains(w.logs.String(), "records of accounts could not be read for the operator's tool") {
		t.Fatalf("logs:\n%s", w.logs)
	}

	// The storage fails: an error, or accounts counted as not read. Never
	// fewer accounts than there are.
	down := errors.New("down")
	f := &failing{Records: w.svc.cfg.Records, Entries: w.mem, Keys: w.svc.cfg.Keys, fail: map[string]error{}}
	w.svc.cfg.Records, w.svc.cfg.Entries, w.svc.cfg.Keys = f, f, f
	for _, name := range []string{"derive", "list " + accountsPartition} {
		f.fail[name] = down
		if _, err := w.svc.AccountRecords(ctx, "", 10); !errors.Is(err, down) {
			t.Errorf("%s down: the list: %v", name, err)
		}
		delete(f.fail, name)
	}
	for _, name := range []string{"get record", "list " + accountPartition(made[0].Account.ID)} {
		f.fail[name] = down
		if _, err := w.svc.AccountRecord(ctx, made[0].Account.ID); !errors.Is(err, down) {
			t.Errorf("%s down: an account: %v", name, err)
		}
		page, err := w.svc.AccountRecords(ctx, "", 10)
		if err != nil || len(page.Accounts)+page.Unreadable != 5 || page.Unreadable < 4 {
			t.Errorf("%s down: the list: %+v %v", name, page, err)
		}
		delete(f.fail, name)
	}
	var words []string
	for _, me := range made {
		words = append(words, me.Account.ID)
	}
	w.quiet(append(words, "example.org", "Bea", "friend")...)
}

// A sign-up that does not end leaves no account behind it: not one the
// operator's tool would show, and not one nobody could ever remove.
func TestASignUpThatFailsLeavesNothing(t *testing.T) {
	ctx := context.Background()
	down := errors.New("down")
	for _, name := range []string{"put " + loginKey, "put record", "derive"} {
		w := newWorld(t)
		f := &failing{Records: w.svc.cfg.Records, Entries: w.mem, Keys: w.svc.cfg.Keys, fail: map[string]error{}}
		w.svc.cfg.Records, w.svc.cfg.Entries, w.svc.cfg.Keys = f, f, f
		attempt, mail := w.start("ana@example.org", w.invite())
		f.fail[name] = down
		if _, _, err := w.svc.FinishEmail(ctx, contracts.EmailFinish{Attempt: attempt, Code: code(mail)}, ""); !errors.Is(err, down) {
			t.Fatalf("%s down: %v", name, err)
		}
		delete(f.fail, name)
		if left := w.stored("ana@example.org", "acc_"); len(left) != 0 {
			t.Fatalf("%s down: left in the storage: %q", name, left)
		}
		if list, err := w.svc.AccountRecords(ctx, "", 10); err != nil || len(list.Accounts) != 0 || list.Unreadable != 0 {
			t.Fatalf("%s down: the list of accounts: %+v %v", name, list, err)
		}
		// The address signs up, with another invitation.
		w.now = w.now.Add(2 * time.Hour)
		if me, _ := w.signUp("ana@example.org"); me.Account.Address != "ana@example.org" {
			t.Fatalf("%s down: signing up afterwards: %+v", name, me)
		}
		if list, err := w.svc.AccountRecords(ctx, "", 10); err != nil || len(list.Accounts) != 1 {
			t.Fatalf("%s down: the list of accounts afterwards: %+v %v", name, list, err)
		}
	}
}

// signalling is storage whose sessions cannot be deleted, and that tells
// each time it was asked to.
type signalling struct {
	store.Entries
	err   error
	asked chan struct{}
}

func (s *signalling) DeletePartition(context.Context, string) error {
	select {
	case s.asked <- struct{}{}:
	default:
	}
	return s.err
}

// The enclave goes through the list by itself, every so often, and what it
// logs when that fails names no account.
func TestRun(t *testing.T) {
	c := newClosing(t)
	c.now = c.day
	id := c.ana.Account.ID
	stuck := &signalling{Entries: c.mem, err: fmt.Errorf("store: delete of account#%s: down", id), asked: make(chan struct{}, 1)}
	c.svc.cfg.Entries = stuck
	ctx, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		c.svc.Run(ctx, time.Millisecond)
	}()
	wait := func() {
		t.Helper()
		select {
		case <-stuck.asked:
		case <-time.After(10 * time.Second):
			t.Fatal("the enclave did not go through the list")
		}
	}
	// Twice: the first failure is logged by the time it tries again.
	wait()
	wait()
	stop()
	<-done
	if logs := c.logs.String(); !strings.Contains(logs, "accounts: removing the accounts that are due") || !strings.Contains(logs, "account#acc_…: down") {
		t.Fatalf("logs:\n%s", logs)
	}
	c.quiet(id, "example.org", "Ana Closing")
	if len(c.stored(id)) == 0 {
		t.Fatal("removed although the sessions could not be deleted")
	}

	// With the storage working, it removes the account without being asked.
	c.svc.cfg.Entries = c.mem
	ctx, stop = context.WithCancel(context.Background())
	done = make(chan struct{})
	go func() {
		defer close(done)
		c.svc.Run(ctx, time.Millisecond)
	}()
	deadline := time.Now().Add(10 * time.Second)
	for len(c.mem.RecordNames()) != 1 {
		if time.Now().After(deadline) {
			t.Fatal("the enclave did not remove the account that was due")
		}
		time.Sleep(time.Millisecond)
	}
	stop()
	<-done
	c.gone()
}

// A failure to look an address up is logged without the account it was about.
func TestLookupFailuresNameNoAccount(t *testing.T) {
	w := newWorld(t)
	me, _ := w.signUp("ana@example.org")
	f := &failing{Records: w.svc.cfg.Records, Entries: w.mem, Keys: w.svc.cfg.Keys, fail: map[string]error{}}
	w.svc.cfg.Records = f
	f.fail["get record"] = fmt.Errorf("store: commits of %s: down", recordName(me.Account.ID))
	if _, mail := w.start("ana@example.org", ""); mail != nil {
		t.Fatal("a message was sent")
	}
	if !strings.Contains(w.logs.String(), "commits of account-acc_…: down") {
		t.Fatalf("logs:\n%s", w.logs)
	}
	w.quiet(me.Account.ID, "example.org")
}
