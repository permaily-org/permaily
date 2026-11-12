package accounts

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/domain"
	"github.com/permaily-org/permaily/internal/store"
	"github.com/permaily-org/permaily/internal/webauthn/webauthntest"
)

// Two enclaves of one deployment run at once: two services, each with its own
// memory, over the same storage and the same store of commits. These tests
// put what one does between what the other reads and what it then writes.

// between is the records of accounts as one enclave has them, where a test
// says what happens after the enclave read a record and before it writes it.
type between struct {
	*store.Anchored
	// meanwhile runs before the next write or removal of a record, once
	// unless always is set.
	meanwhile func()
	always    bool
	writes    int
}

func (b *between) run() {
	b.writes++
	if f := b.meanwhile; f != nil {
		if !b.always {
			b.meanwhile = nil
		}
		f()
	}
}

func (b *between) PutRecord(ctx context.Context, r *store.Record, create bool) error {
	b.run()
	return b.Anchored.PutRecord(ctx, r, create)
}

func (b *between) PutRecordAt(ctx context.Context, r *store.Record, at store.Mark) error {
	b.run()
	return b.Anchored.PutRecordAt(ctx, r, at)
}

// watched is the entries as one enclave has them, where a test says what
// happens after the enclave read an entry of a partition, or before it
// deletes one.
type watched struct {
	store.Entries
	partition string
	afterGet  func()
	onDelete  func()
	// beforePut runs before the enclave writes an entry of the partition,
	// once: after everything it read to decide what to write.
	beforePut func()
}

func (w *watched) PutEntry(ctx context.Context, e *store.Entry) error {
	if f := w.beforePut; f != nil && e.Partition == w.partition {
		w.beforePut = nil
		f()
	}
	return w.Entries.PutEntry(ctx, e)
}

func (w *watched) GetEntry(ctx context.Context, partition, key string) (*store.Entry, error) {
	e, err := w.Entries.GetEntry(ctx, partition, key)
	if f := w.afterGet; f != nil && partition == w.partition {
		w.afterGet = nil
		f()
	}
	return e, err
}

func (w *watched) deleting(partition string) {
	if f := w.onDelete; f != nil && partition == w.partition {
		w.onDelete = nil
		f()
	}
}

func (w *watched) DeleteEntry(ctx context.Context, partition, key string) error {
	w.deleting(partition)
	return w.Entries.DeleteEntry(ctx, partition, key)
}

func (w *watched) DeleteEntryAt(ctx context.Context, e *store.Entry) error {
	w.deleting(e.Partition)
	return w.Entries.DeleteEntryAt(ctx, e)
}

// enclave is one more service over the world's storage.
type enclave struct {
	w       *world
	svc     *Service
	records *between
	entries *watched
}

func (w *world) enclave() *enclave {
	w.t.Helper()
	e := &enclave{
		w:       w,
		records: &between{Anchored: &store.Anchored{Records: w.mem, Commits: w.commits, OnlyNewest: true, Now: func() time.Time { return w.now }}},
		entries: &watched{Entries: w.mem},
	}
	var err error
	e.svc, err = New(Config{
		Records: e.records, Entries: e.entries, Keys: w.svc.cfg.Keys, Mailer: w.out, Providers: w.idp,
		AppURL: "https://app.permaily.test", SuperAdmin: superAdmin, Log: slog.New(slog.NewTextHandler(w.logs, nil)), Now: func() time.Time { return w.now },
	})
	if err != nil {
		w.t.Fatal(err)
	}
	return e
}

// passkey makes a passkey through this enclave: the ceremony is in its memory.
func (e *enclave) passkey(who *Authenticated, name string) (*contracts.Passkey, error) {
	e.w.t.Helper()
	ctx := context.Background()
	started, err := e.svc.StartPasskey(ctx, e.w.fresh(who))
	if err != nil {
		return nil, err
	}
	in := contracts.PasskeyFinish{Ceremony: started.Ceremony, Name: name}
	auth := webauthntest.New(webauthntest.ES256)
	id, clientData, attestation := auth.Create(started.Options.RP.ID, testOrigin, started.Options.Challenge, started.Options.User.ID)
	in.Credential.RawID, in.Credential.Response.ClientDataJSON, in.Credential.Response.AttestationObject = id, clientData, attestation
	return e.svc.FinishPasskey(ctx, who, in)
}

// start starts a sign-in through this enclave, and returns what ends it.
func (e *enclave) start(address, invitation string) contracts.EmailFinish {
	e.w.t.Helper()
	res, err := e.svc.StartEmail(context.Background(), contracts.EmailStart{Address: address, Invitation: invitation})
	if err != nil {
		e.w.t.Fatal(err)
	}
	e.svc.Wait()
	sent := e.w.out.Sent()
	return contracts.EmailFinish{Attempt: res.Attempt, Code: code(&sent[len(sent)-1])}
}

// doc reads an account's record through an enclave that was just started.
func (w *world) doc(id string) *accountDoc {
	w.t.Helper()
	doc, err := w.enclave().svc.read(context.Background(), id)
	if err != nil {
		w.t.Fatal(err)
	}
	return doc
}

// Each of two enclaves changes one account, the second between the first's
// read and its write. Nothing that either was told it did is undone.
func TestTwoEnclavesChangeOneAccount(t *testing.T) {
	ctx := context.Background()
	setup := func(t *testing.T) (w *world, who *Authenticated, one, two *enclave) {
		t.Helper()
		w = newWorld(t)
		_, cookie := w.signUp("ana@example.org")
		return w, w.who(cookie), w.enclave(), w.enclave()
	}

	t.Run("a name by one, a passkey by the other", func(t *testing.T) {
		w, who, one, two := setup(t)
		var made *contracts.Passkey
		one.records.meanwhile = func() {
			var err error
			if made, err = two.passkey(who, "laptop"); err != nil {
				t.Fatal(err)
			}
		}
		account, err := one.svc.UpdateAccount(ctx, who, contracts.AccountUpdate{Name: text("Ana")})
		if err != nil || account.Name != "Ana" {
			t.Fatalf("got %+v, %v", account, err)
		}
		doc := w.doc(who.AccountID)
		if doc.Name != "Ana" || len(doc.Passkeys) != 1 || made == nil || doc.passkey(made.ID) != 0 {
			t.Fatalf("the name is %q and there are %d passkeys", doc.Name, len(doc.Passkeys))
		}
		if one.records.writes != 2 {
			t.Fatalf("the first enclave wrote %d times", one.records.writes)
		}
		// Only the newest version is in the storage.
		if stored := w.stored("account-" + who.AccountID); len(stored) != 1 {
			t.Fatalf("stored %q", stored)
		}
	})

	t.Run("a passkey by one, a name by the other", func(t *testing.T) {
		w, who, one, two := setup(t)
		one.records.meanwhile = func() {
			if _, err := two.svc.UpdateAccount(ctx, who, contracts.AccountUpdate{Name: text("Ana")}); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := one.passkey(who, "laptop"); err != nil {
			t.Fatal(err)
		}
		if doc := w.doc(who.AccountID); doc.Name != "Ana" || len(doc.Passkeys) != 1 {
			t.Fatalf("the name is %q and there are %d passkeys", doc.Name, len(doc.Passkeys))
		}
	})

	t.Run("the last passkey removed by one while the other turns passkey only on", func(t *testing.T) {
		w, who, one, two := setup(t)
		key := w.addPasskey(who, webauthntest.New(webauthntest.ES256), "key")
		var codes []string
		one.records.meanwhile = func() {
			state, err := two.svc.SetPasskeyOnly(ctx, w.fresh(who), contracts.PasskeyOnly{On: true})
			if err != nil {
				t.Fatal(err)
			}
			codes = state.RecoveryCodes
		}
		// Written over the other's change, this would leave an account that
		// signs in with passkeys only and has none.
		if err := one.svc.RemovePasskey(ctx, w.fresh(who), key.ID.String()); !errors.Is(err, contracts.ErrLastPasskey) {
			t.Fatalf("got %v", err)
		}
		doc := w.doc(who.AccountID)
		if !doc.PasskeyOnly || len(doc.Passkeys) != 1 || len(doc.RecoveryCodes) != domain.RecoveryCodes {
			t.Fatalf("passkey only %v, %d passkeys, %d codes", doc.PasskeyOnly, len(doc.Passkeys), len(doc.RecoveryCodes))
		}
		if _, _, err := one.svc.Recover(ctx, contracts.Recovery{Address: "ana@example.org", Code: codes[0]}, Browser{UserAgent: ""}); err != nil {
			t.Fatalf("a code the other enclave showed: %v", err)
		}
	})

	t.Run("one recovery code used through both", func(t *testing.T) {
		w, who, one, two := setup(t)
		w.addPasskey(who, webauthntest.New(webauthntest.ES256), "key")
		codes := w.passkeyOnly(who)
		in := contracts.Recovery{Address: "ana@example.org", Code: codes[0]}
		one.records.meanwhile = func() {
			if _, _, err := two.svc.Recover(ctx, in, Browser{UserAgent: ""}); err != nil {
				t.Fatal(err)
			}
		}
		if _, _, err := one.svc.Recover(ctx, in, Browser{UserAgent: ""}); !errors.Is(err, contracts.ErrInvalidCode) {
			t.Fatalf("a code that was used a moment before: %v", err)
		}
		if doc := w.doc(who.AccountID); len(doc.RecoveryCodes) != domain.RecoveryCodes-1 {
			t.Fatalf("%d codes left", len(doc.RecoveryCodes))
		}
		if list, _ := w.svc.sessions(ctx, who.AccountID, nil); len(list) != 2 {
			t.Fatalf("%d sessions: one code signed in twice", len(list))
		}
		// The use that lost is not a wrong code.
		if len(one.svc.recoveries) != 0 {
			t.Fatalf("counted as wrong: %v", one.svc.recoveries)
		}
	})

	t.Run("passkey only turned on through both", func(t *testing.T) {
		w, who, one, two := setup(t)
		w.addPasskey(who, webauthntest.New(webauthntest.ES256), "key")
		var shown []string
		one.records.meanwhile = func() {
			state, err := two.svc.SetPasskeyOnly(ctx, w.fresh(who), contracts.PasskeyOnly{On: true})
			if err != nil {
				t.Fatal(err)
			}
			shown = state.RecoveryCodes
		}
		state, err := one.svc.SetPasskeyOnly(ctx, w.fresh(who), contracts.PasskeyOnly{On: true})
		// It was on already when it came to it: the codes are the ones the
		// other showed, and none that the record does not hold is shown here.
		if err != nil || !state.PasskeyOnly || len(state.RecoveryCodes) != 0 {
			t.Fatalf("got %+v, %v", state, err)
		}
		for _, code := range shown {
			if _, _, err := w.recover("ana@example.org", code); err != nil {
				t.Fatalf("a code that was shown does not work: %v", err)
			}
		}
	})

	t.Run("new recovery codes through both", func(t *testing.T) {
		w, who, one, two := setup(t)
		w.addPasskey(who, webauthntest.New(webauthntest.ES256), "key")
		w.passkeyOnly(who)
		var theirs *contracts.RecoveryCodes
		one.records.meanwhile = func() {
			var err error
			if theirs, err = two.svc.NewRecoveryCodes(ctx, w.fresh(who)); err != nil {
				t.Fatal(err)
			}
		}
		mine, err := one.svc.NewRecoveryCodes(ctx, w.fresh(who))
		if err != nil {
			t.Fatal(err)
		}
		// The ones asked for last are the ones that work, all of them.
		if _, _, err := w.recover("ana@example.org", theirs.RecoveryCodes[0]); !errors.Is(err, contracts.ErrInvalidCode) {
			t.Fatalf("a code that was replaced: %v", err)
		}
		for _, code := range mine.RecoveryCodes {
			if _, _, err := w.recover("ana@example.org", code); err != nil {
				t.Fatalf("a code that was shown does not work: %v", err)
			}
		}
	})

	t.Run("a name by one while the other starts deleting the account", func(t *testing.T) {
		w, who, one, two := setup(t)
		one.records.meanwhile = func() {
			if _, err := two.svc.StartDeletion(ctx, w.fresh(who)); err != nil {
				t.Fatal(err)
			}
		}
		// Written over the other's change, the account would be active again.
		if _, err := one.svc.UpdateAccount(ctx, who, contracts.AccountUpdate{Name: text("Ana")}); !errors.Is(err, contracts.ErrAccountClosing) {
			t.Fatalf("got %v", err)
		}
		doc := w.doc(who.AccountID)
		if doc.State != contracts.AccountClosing || doc.Name != "" {
			t.Fatalf("state %s, name %q", doc.State, doc.Name)
		}
		if listed := w.listed(closingPartition); len(listed) != 1 || listed[0] != closingKey(doc) {
			t.Fatalf("the list of closing accounts: %q", listed)
		}
	})

	t.Run("deleting started by one while the other changes the name", func(t *testing.T) {
		w, who, one, two := setup(t)
		one.records.meanwhile = func() {
			if _, err := two.svc.UpdateAccount(ctx, who, contracts.AccountUpdate{Name: text("Ana")}); err != nil {
				t.Fatal(err)
			}
		}
		account, err := one.svc.StartDeletion(ctx, w.fresh(who))
		if err != nil || account.State != contracts.AccountClosing || account.Name != "Ana" {
			t.Fatalf("got %+v, %v", account, err)
		}
		doc := w.doc(who.AccountID)
		if doc.State != contracts.AccountClosing || doc.Name != "Ana" || !doc.ClosesAt.Equal(account.ClosesAt) {
			t.Fatalf("state %s, name %q, closes %s", doc.State, doc.Name, doc.ClosesAt)
		}
		// One entry in the list of closing accounts, the one the record bears out.
		if listed := w.listed(closingPartition); len(listed) != 1 || listed[0] != closingKey(doc) {
			t.Fatalf("the list of closing accounts: %q", listed)
		}
	})

	t.Run("deleting started through both", func(t *testing.T) {
		w, who, one, two := setup(t)
		var theirs *contracts.Account
		one.records.meanwhile = func() {
			w.now = w.now.Add(3 * time.Second)
			var err error
			if theirs, err = two.svc.StartDeletion(ctx, w.fresh(who)); err != nil {
				t.Fatal(err)
			}
		}
		mine, err := one.svc.StartDeletion(ctx, w.fresh(who))
		// The day is the one that was written, for both.
		if err != nil || !mine.ClosesAt.Equal(theirs.ClosesAt) {
			t.Fatalf("got %+v and %+v, %v", mine, theirs, err)
		}
		doc := w.doc(who.AccountID)
		if !doc.ClosesAt.Equal(theirs.ClosesAt) {
			t.Fatalf("closes %s, told %s", doc.ClosesAt, theirs.ClosesAt)
		}
		// The entry the first one made is of no day; it goes when its day
		// comes, and the account goes on its own.
		w.now = doc.ClosesAt.Add(-time.Second)
		if err := one.svc.RemoveDue(ctx); err != nil {
			t.Fatal(err)
		}
		if listed := w.listed(closingPartition); len(listed) != 1 || listed[0] != closingKey(doc) {
			t.Fatalf("the list of closing accounts: %q", listed)
		}
		if w.doc(who.AccountID) == nil {
			t.Fatal("removed before its day")
		}
		w.now = doc.ClosesAt
		if err := two.svc.RemoveDue(ctx); err != nil {
			t.Fatal(err)
		}
		if w.doc(who.AccountID) != nil || len(w.stored(who.AccountID, "ana@example.org")) != 0 {
			t.Fatalf("left of the account: %q", w.stored(who.AccountID, "ana@example.org"))
		}
	})
}

// A change that one enclave is making when the other removes the account does
// not bring the account back.
func TestAChangeDoesNotUndoARemoval(t *testing.T) {
	ctx := context.Background()
	for _, name := range []string{"cancelling", "a recovery code"} {
		t.Run(name, func(t *testing.T) {
			w := newWorld(t)
			me, cookie := w.signUp("ana@example.org")
			who := w.who(cookie)
			w.addPasskey(who, webauthntest.New(webauthntest.ES256), "key")
			codes := w.passkeyOnly(who)
			change := func(svc *Service) error {
				if name == "cancelling" {
					_, err := svc.CancelDeletion(ctx, who)
					return err
				}
				_, _, err := svc.Recover(ctx, contracts.Recovery{Address: "ana@example.org", Code: codes[0]}, Browser{UserAgent: ""})
				return err
			}
			one, two := w.enclave(), w.enclave()
			closing, err := one.svc.StartDeletion(ctx, w.fresh(who))
			if err != nil {
				t.Fatal(err)
			}
			// The first enclave read the account a moment before its day; the
			// day comes, and the second removes it.
			w.now = closing.ClosesAt.Add(-time.Second)
			one.records.meanwhile = func() {
				w.now = closing.ClosesAt
				if err := two.svc.RemoveDue(ctx); err != nil {
					t.Fatal(err)
				}
			}
			err = change(one.svc)
			var known *contracts.Error
			if !errors.As(err, &known) || (known != contracts.ErrUnauthorized && known != contracts.ErrInvalidCode) {
				t.Fatalf("a change of an account that was removed meanwhile: %v", err)
			}
			if w.doc(me.Account.ID) != nil {
				t.Fatal("the account is back")
			}
			if left := w.stored(me.Account.ID, "ana@example.org"); len(left) != 0 {
				t.Fatalf("left of the account: %q", left)
			}
			if _, err := w.svc.Authenticate(ctx, cookie); !errors.Is(err, contracts.ErrUnauthorized) {
				t.Fatalf("its session: %v", err)
			}
			// The address signs up again, as a new account.
			again, _ := w.signUp("ana@example.org")
			if again.Account.ID == me.Account.ID {
				t.Fatal("the same account")
			}
		})
	}
}

// Two sign-ups of one address that end at the same time in two enclaves make
// one account, and leave nothing of the other.
func TestTwoEnclavesSignOneAddressUp(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	one, two := w.enclave(), w.enclave()
	first := one.start("ana@example.org", w.invite())
	second := two.start("ana@example.org", w.invite())
	var winner *contracts.Me
	// The first has found no account and is about to write its record.
	one.records.meanwhile = func() {
		var err error
		if winner, _, err = two.svc.FinishEmail(ctx, second, Browser{UserAgent: ""}); err != nil {
			t.Fatal(err)
		}
	}
	loser, _, err := one.svc.FinishEmail(ctx, first, Browser{UserAgent: ""})
	if err != nil || winner == nil || loser.Account.ID != winner.Account.ID {
		t.Fatalf("the sign-up that ended second: %+v, the first %+v, %v", loser, winner, err)
	}
	list, err := w.svc.AccountRecords(ctx, "", 10)
	if err != nil || len(list.Accounts) != 1 || list.Accounts[0].ID != winner.Account.ID || list.Accounts[0].Sessions != 2 {
		t.Fatalf("the accounts: %+v, %v", list, err)
	}
	records := 0
	for _, name := range w.mem.RecordNames() {
		if strings.HasPrefix(name, "account-") {
			records++
		}
	}
	if records != 1 || len(w.listed(accountsPartition)) != 1 {
		t.Fatalf("%d records and %d entries in the list of accounts", records, len(w.listed(accountsPartition)))
	}
}

// A record that another enclave changes every time this one is about to
// write it: the change is given up after a few tries, as an error, and is
// never written over what the other did.
func TestAnAccountThatKeepsChanging(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	me, cookie := w.signUp("ana@example.org")
	who := w.who(cookie)
	one, two := w.enclave(), w.enclave()
	before, theirs := w.version(me.Account.ID), 0
	one.records.always = true
	one.records.meanwhile = func() {
		theirs++
		if _, err := two.svc.UpdateAccount(ctx, who, contracts.AccountUpdate{Name: text(strings.Repeat("b", theirs))}); err != nil {
			t.Fatal(err)
		}
	}
	_, err := one.svc.UpdateAccount(ctx, who, contracts.AccountUpdate{Name: text("Ana")})
	var known *contracts.Error
	if err == nil || errors.As(err, &known) || errors.Is(err, store.ErrChanged) || !strings.Contains(err.Error(), "keeps changing") {
		t.Fatalf("got %v", err)
	}
	if strings.Contains(err.Error(), me.Account.ID) {
		t.Fatalf("the error names the account: %v", err)
	}
	if theirs != maxRewrites || one.records.writes != maxRewrites {
		t.Fatalf("%d tries, %d changes by the other enclave, want %d", one.records.writes, theirs, maxRewrites)
	}
	if doc := w.doc(me.Account.ID); doc.Name != strings.Repeat("b", theirs) {
		t.Fatalf("the name is %q", doc.Name)
	}
	if got := w.version(me.Account.ID); got != before+uint64(theirs) {
		t.Fatalf("the record was written %d times, the other enclave wrote %d", got-before, theirs)
	}
	if stored := w.stored("account-" + me.Account.ID); len(stored) != 1 {
		t.Fatalf("stored %q", stored)
	}
	// Left alone, it goes through.
	one.records.meanwhile = nil
	if account, err := one.svc.UpdateAccount(ctx, who, contracts.AccountUpdate{Name: text("Ana")}); err != nil || account.Name != "Ana" {
		t.Fatalf("got %+v, %v", account, err)
	}
}

// An enclave that is removing an account read the pointer of its address;
// before it deletes it, the other enclave ends the removal and the address
// signs up again. The pointer that is there now is the new account's: it stays.
func TestARemovalLeavesAnotherAccountsPointer(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	me, cookie := w.signUp("ana@example.org")
	one, two := w.enclave(), w.enclave()
	closing, err := one.svc.StartDeletion(ctx, w.fresh(w.who(cookie)))
	if err != nil {
		t.Fatal(err)
	}
	w.now = closing.ClosesAt
	var again *contracts.Me
	one.entries.partition = loginPartition("ana@example.org")
	one.entries.afterGet = func() {
		if err := two.svc.RemoveDue(ctx); err != nil {
			t.Fatal(err)
		}
		if again, _, err = two.svc.FinishEmail(ctx, two.start("ana@example.org", w.invite()), Browser{UserAgent: ""}); err != nil {
			t.Fatal(err)
		}
	}
	if err := one.svc.RemoveDue(ctx); err != nil {
		t.Fatal(err)
	}
	if again == nil || again.Account.ID == me.Account.ID {
		t.Fatalf("the second account: %+v", again)
	}
	if doc, _, err := w.svc.lookup(ctx, "ana@example.org"); err != nil || doc == nil || doc.ID != again.Account.ID {
		t.Fatalf("the address leads to %+v, %v", doc, err)
	}
	if left := w.stored(me.Account.ID); len(left) != 0 {
		t.Fatalf("left of the first account: %q", left)
	}
	if signed, _ := w.signIn("ana@example.org"); signed.Account.ID != again.Account.ID {
		t.Fatalf("signed in to %s", signed.Account.ID)
	}
}

// Deleting is cancelled through one enclave and, before its entry in the
// list of closing accounts is deleted, asked again through the other in the
// same second. The account that is closing stays in the list, or the
// enclave would not find it when its day comes.
func TestDeletingAskedAgainWhileItIsCancelled(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	me, cookie := w.signUp("ana@example.org")
	who := w.who(cookie)
	one, two := w.enclave(), w.enclave()
	if _, err := one.svc.StartDeletion(ctx, w.fresh(who)); err != nil {
		t.Fatal(err)
	}
	one.entries.partition = closingPartition
	one.entries.onDelete = func() {
		if _, err := two.svc.StartDeletion(ctx, w.fresh(who)); err != nil {
			t.Fatal(err)
		}
	}
	if account, err := one.svc.CancelDeletion(ctx, who); err != nil || account.State != contracts.AccountActive {
		t.Fatalf("got %+v, %v", account, err)
	}
	doc := w.doc(me.Account.ID)
	if doc.State != contracts.AccountClosing {
		t.Fatalf("state %s", doc.State)
	}
	if listed := w.listed(closingPartition); len(listed) != 1 || listed[0] != closingKey(doc) {
		t.Fatalf("the list of closing accounts: %q, the record says %s", listed, closingKey(doc))
	}
	if doc.ClosesAt.Sub(w.now) != domain.AccountClosing {
		t.Fatalf("closes %s", doc.ClosesAt)
	}
	w.now = doc.ClosesAt
	if err := one.svc.RemoveDue(ctx); err != nil {
		t.Fatal(err)
	}
	if left := w.stored(me.Account.ID, "ana@example.org"); len(left) != 0 {
		t.Fatalf("left of the account: %q", left)
	}
}

// Two enclaves that each sign a device in when the account has all it keeps
// leave one too many. The next sign-in brings it back to what it keeps.
func TestSessionsOverTheBoundGoBack(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	me, _ := w.signUp("ana@example.org")
	for i := range maxSessions + 2 {
		w.now = w.now.Add(time.Minute)
		doc := &sessionDoc{V: 1, Account: me.Account.ID, ID: newID("ses"), TokenSHA256: []byte{byte(i)}, CreatedAt: w.now, LastUsedAt: w.now}
		if err := w.svc.putSession(ctx, doc, 0); err != nil {
			t.Fatal(err)
		}
	}
	if list, _ := w.svc.sessions(ctx, me.Account.ID, nil); len(list) != maxSessions+3 {
		t.Fatalf("%d sessions", len(list))
	}
	w.now = w.now.Add(time.Minute)
	newest, _, err := w.enclave().svc.newSession(ctx, w.doc(me.Account.ID), "device")
	if err != nil {
		t.Fatal(err)
	}
	list, _ := w.svc.sessions(ctx, me.Account.ID, nil)
	if len(list) != maxSessions {
		t.Fatalf("%d sessions kept", len(list))
	}
	// The ones used longest ago went.
	for _, s := range list {
		if s.ID != newest.ID && s.LastUsedAt.Before(w.now.Add(-time.Duration(maxSessions-1)*time.Minute)) {
			t.Fatalf("a session last used %s stayed", s.LastUsedAt)
		}
	}
}

// Storage and keys that fail while a record is changed are an error, and
// leave the account as it was: never a change half made, and never a reason
// to try again as if another enclave had changed the record.
func TestChangeFailures(t *testing.T) {
	ctx := context.Background()
	down := errors.New("down")
	setup := func(t *testing.T) (*world, *failing, *contracts.Me, *Authenticated) {
		t.Helper()
		w := newWorld(t)
		f := &failing{Records: w.svc.cfg.Records, Entries: w.mem, Keys: w.svc.cfg.Keys, fail: map[string]error{}, skip: map[string]int{}}
		w.svc.cfg.Records, w.svc.cfg.Entries, w.svc.cfg.Keys = f, f, f
		me, cookie := w.signUp("ana@example.org")
		return w, f, me, w.who(cookie)
	}
	active := func(t *testing.T, w *world, id string) {
		t.Helper()
		if doc := w.doc(id); doc.State != contracts.AccountActive || doc.Name != "" || len(w.listed(closingPartition)) > 1 {
			t.Fatalf("the account afterwards: %+v, list %q", doc, w.listed(closingPartition))
		}
	}

	t.Run("the key is gone when the record is signed", func(t *testing.T) {
		w, f, me, who := setup(t)
		before := w.version(me.Account.ID)
		f.skip["derive"], f.fail["derive"] = 1, down // the read still verifies
		if _, err := w.svc.UpdateAccount(ctx, who, contracts.AccountUpdate{Name: text("Ana")}); !errors.Is(err, down) {
			t.Fatalf("got %v", err)
		}
		delete(f.fail, "derive")
		active(t, w, me.Account.ID)
		if w.version(me.Account.ID) != before {
			t.Fatal("the record was written")
		}
		// And when a new account's record is signed: nothing of it is left.
		attempt, mail := w.start("eve@example.org", w.invite())
		f.skip["derive"], f.fail["derive"] = 1, down // the entry in the list of accounts
		if _, _, err := w.svc.FinishEmail(ctx, contracts.EmailFinish{Attempt: attempt, Code: code(mail)}, Browser{UserAgent: ""}); !errors.Is(err, down) {
			t.Fatalf("got %v", err)
		}
		delete(f.fail, "derive")
		if left := w.stored("eve@example.org"); len(left) != 0 || len(w.listed(accountsPartition)) != 1 {
			t.Fatalf("left of a sign-up that failed: %q, accounts %q", left, w.listed(accountsPartition))
		}
	})

	t.Run("the list of closing accounts while deleting is asked", func(t *testing.T) {
		w, f, me, who := setup(t)
		key := timedKey(w.now.Add(domain.AccountClosing), me.Account.ID)
		// An entry of this second was there and is gone when it is read: the
		// entry is made.
		f.fail["put "+key] = store.ErrExists
		if account, err := w.svc.StartDeletion(ctx, w.fresh(who)); err != nil || account.State != contracts.AccountClosing {
			t.Fatalf("got %+v, %v", account, err)
		}
		if listed := w.listed(closingPartition); len(listed) != 1 || listed[0] != key {
			t.Fatalf("the list of closing accounts: %q", listed)
		}

		// It is there and cannot be read.
		w, f, me, who = setup(t)
		key = timedKey(w.now.Add(domain.AccountClosing), me.Account.ID)
		f.fail["put "+key], f.fail["get "+closingPartition] = store.ErrExists, down
		if _, err := w.svc.StartDeletion(ctx, w.fresh(who)); !errors.Is(err, down) {
			t.Fatalf("got %v", err)
		}
		delete(f.fail, "get "+closingPartition)
		active(t, w, me.Account.ID)

		// It is said to be there every time, and never is.
		w, f, me, who = setup(t)
		f.fail["put "+closingPartition] = store.ErrExists
		if _, err := w.svc.StartDeletion(ctx, w.fresh(who)); err == nil || !strings.Contains(err.Error(), "keeps changing") || strings.Contains(err.Error(), me.Account.ID) {
			t.Fatalf("got %v", err)
		}
		active(t, w, me.Account.ID)
	})

	t.Run("the list of closing accounts while deleting is cancelled", func(t *testing.T) {
		w, f, me, who := setup(t)
		if _, err := w.svc.StartDeletion(ctx, w.fresh(who)); err != nil {
			t.Fatal(err)
		}
		f.fail["get "+closingPartition] = down
		if _, err := w.svc.CancelDeletion(ctx, who); !errors.Is(err, down) {
			t.Fatalf("got %v", err)
		}
		if doc := w.doc(me.Account.ID); doc.State != contracts.AccountClosing || len(w.listed(closingPartition)) != 1 {
			t.Fatalf("state %s, list %q", doc.State, w.listed(closingPartition))
		}
		delete(f.fail, "get "+closingPartition)
		// The entry went already: the account is kept all the same.
		if err := w.mem.DeleteEntry(ctx, closingPartition, w.listed(closingPartition)[0]); err != nil {
			t.Fatal(err)
		}
		if account, err := w.svc.CancelDeletion(ctx, who); err != nil || account.State != contracts.AccountActive {
			t.Fatalf("got %+v, %v", account, err)
		}
	})

	t.Run("a session that cannot be closed to make room", func(t *testing.T) {
		w, f, me, _ := setup(t)
		for range maxSessions {
			w.now = w.now.Add(time.Minute)
			if _, _, err := w.svc.newSession(ctx, w.doc(me.Account.ID), "device"); err != nil {
				t.Fatal(err)
			}
		}
		f.fail["delete "+accountPartition(me.Account.ID)] = down
		if _, _, err := w.svc.newSession(ctx, w.doc(me.Account.ID), "device"); !errors.Is(err, down) {
			t.Fatalf("got %v", err)
		}
		if list, _ := w.svc.sessions(ctx, me.Account.ID, nil); len(list) != maxSessions {
			t.Fatalf("%d sessions", len(list))
		}
	})
}

// A session confirmed through one enclave makes a sensitive change through
// the other: the proof is in the session's entry, not in either's memory.
// And the change is refused through both once the proof is old.
func TestASessionConfirmedThroughOneEnclaveChangesThroughTheOther(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	me, cookie := w.signUp("ana@example.org")
	one, other := w.enclave(), w.enclave()
	w.later()
	if _, err := other.svc.StartPasskey(ctx, w.who(cookie)); !errors.Is(err, contracts.ErrConfirmationNeeded) {
		t.Fatalf("before: %v", err)
	}
	// Confirmed by mail through the first: the attempt is in its memory.
	who, err := one.svc.Authenticate(ctx, cookie)
	if err != nil {
		t.Fatal(err)
	}
	started, err := one.svc.StartConfirmEmail(ctx, who)
	if err != nil {
		t.Fatal(err)
	}
	one.svc.Wait()
	sent := w.out.Sent()
	if _, err := one.svc.FinishConfirmEmail(ctx, who, contracts.ConfirmFinish{Attempt: started.Attempt, Code: code(&sent[len(sent)-1])}); err != nil {
		t.Fatal(err)
	}
	// The next request lands on the other.
	who, err = other.svc.Authenticate(ctx, cookie)
	if err != nil || !who.Session.ConfirmedUntil.Equal(w.now.Add(domain.ConfirmationLifetime)) {
		t.Fatalf("the other enclave reads %+v %v", who, err)
	}
	if _, err := other.passkey(who, "made through the other"); err != nil {
		t.Fatalf("a passkey through the other: %v", err)
	}
	if doc := w.doc(me.Account.ID); len(doc.Passkeys) != 1 {
		t.Fatalf("the passkeys: %+v", doc.Passkeys)
	}
	w.later()
	for _, e := range []*enclave{one, other} {
		who, _ := e.svc.Authenticate(ctx, cookie)
		if _, err := e.svc.StartDeletion(ctx, who); !errors.Is(err, contracts.ErrConfirmationNeeded) {
			t.Fatalf("after the proof is old: %v", err)
		}
	}
}

// An admin made through one enclave acts through the other: the role is in
// the account's record, read with it.
func TestAnAdminMadeThroughOneEnclaveActsThroughTheOther(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	boss := w.account(superAdmin, true)
	ana := w.account("ana@example.org", true)
	carl, _ := w.signUp("carl@example.org")
	one, other := w.enclave(), w.enclave()
	bossWho, _ := one.svc.Authenticate(ctx, boss.cookie)
	admin, err := one.svc.Admin(ctx, w.fresh(bossWho))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.MakeAdmin(ctx, ana.me.Account.ID); err != nil {
		t.Fatal(err)
	}
	w.now = w.now.Add(time.Second)
	anaWho, _ := other.svc.Authenticate(ctx, ana.cookie)
	as, err := other.svc.Admin(ctx, w.fresh(anaWho))
	if err != nil {
		t.Fatalf("ana through the other: %v", err)
	}
	if rec, err := as.BlockAccount(ctx, carl.Account.ID); err != nil || rec.BlockedAt.IsZero() {
		t.Fatalf("ana blocks carl through the other: %+v %v", rec, err)
	}
	// Both write to the one log, and each counts what it did.
	log, err := one.svc.AdminLog(ctx, "", 10)
	if err != nil || len(log.Entries) != 2 || log.Entries[0].Kind != ActionBlock || log.Entries[1].Kind != ActionAdminMade {
		t.Fatalf("the log read through the first: %+v %v", log, err)
	}
	_, counted := other.svc.OperatorActions()
	if counted[ActionBlock] != 1 || counted[ActionAdminMade] != 0 {
		t.Fatalf("the other counted %v", counted)
	}
	// Removed through the first, ana is no admin through the other.
	if _, err := admin.RemoveAdmin(ctx, ana.me.Account.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := other.svc.Admin(ctx, w.fresh(anaWho)); !errors.Is(err, contracts.ErrNotFound) {
		t.Fatalf("ana through the other after: %v", err)
	}
}
