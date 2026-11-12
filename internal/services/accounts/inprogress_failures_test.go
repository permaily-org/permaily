package accounts

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/domain"
	"github.com/permaily-org/permaily/internal/envelope"
	"github.com/permaily-org/permaily/internal/idp"
	"github.com/permaily-org/permaily/internal/store"
)

// shortFor gives a key too short to use for one purpose, and the real keys
// for the others.
type shortFor struct {
	Keys
	purpose string
}

func (k shortFor) Derive(ctx context.Context, purpose string) ([]byte, error) {
	if purpose == k.purpose {
		return make([]byte, 16), nil
	}
	return k.Keys.Derive(ctx, purpose)
}

// nth runs before, once, just before the nth read of an entry of a partition.
type nth struct {
	store.Entries
	partition string
	n         int
	before    func()
}

func (c *nth) GetEntry(ctx context.Context, partition, key string) (*store.Entry, error) {
	if partition == c.partition && c.before != nil {
		if c.n--; c.n == 0 {
			f := c.before
			c.before = nil
			f()
		}
	}
	return c.Entries.GetEntry(ctx, partition, key)
}

// failingWorld is a world whose service's storage and keys fail on demand.
func failingWorld(t *testing.T) (*world, *failing) {
	t.Helper()
	w := newWorld(t)
	f := &failing{Records: w.svc.cfg.Records, Entries: w.mem, Keys: w.svc.cfg.Keys, fail: map[string]error{}, skip: map[string]int{}}
	w.svc.cfg.Records, w.svc.cfg.Entries, w.svc.cfg.Keys = f, f, f
	return w, f
}

// What keeps sign-ins in progress fails: each failure is an error, and none
// lets anyone in or is told as a wrong code that would be counted.
func TestSignInsInProgressFailures(t *testing.T) {
	down, ctx := errors.New("down"), context.Background()

	t.Run("the key that seals them", func(t *testing.T) {
		w, f := failingWorld(t)
		f.fail["derive "+pendingPurpose] = down
		if _, err := w.svc.StartPasskeySignIn(ctx); !errors.Is(err, down) {
			t.Fatalf("starting a ceremony: %v", err)
		}
		delete(f.fail, "derive "+pendingPurpose)
		w.svc.cfg.Keys = shortFor{f, pendingPurpose}
		if _, err := w.svc.StartPasskeySignIn(ctx); err == nil {
			t.Fatal("sealed with a short key")
		}
	})

	t.Run("the storage, when they start", func(t *testing.T) {
		w, f := failingWorld(t)
		w.on(idp.Google, idp.Meta)
		_, cookie := w.signUp("ana@example.org")
		who := w.who(cookie)
		f.fail["put "+pendingKey] = down
		if _, err := w.svc.StartPasskey(ctx, w.fresh(who)); !errors.Is(err, down) {
			t.Fatalf("making a passkey: %v", err)
		}
		f.fail["put "+pendingKey] = down
		if _, err := w.svc.StartConfirmPasskey(ctx, who); !errors.Is(err, contracts.ErrPasskeyNeeded) {
			t.Fatalf("confirming without a passkey: %v", err)
		}
		f.fail["put "+pendingKey] = down
		if _, _, err := w.svc.StartProvider(ctx, nil, contracts.ProviderStart{Provider: "google"}); !errors.Is(err, down) {
			t.Fatalf("a trip: %v", err)
		}
		state, browser := w.leave(nil, contracts.ProviderStart{Provider: "meta"})
		f.fail["put "+pendingKey] = down
		done, err := w.comeBack(state, browser, person(idp.Meta, "77007700770066", "", false))
		if !errors.Is(err, down) || done.Browser != "" {
			t.Fatalf("waiting for an address: %+v %v", done, err)
		}
		// An empty state is no trip.
		done, err = w.svc.FinishProvider(ctx, ProviderAnswer{Code: "x", Browser: browser})
		w.refusedWith("no state", done, err, contracts.ErrProviderFailed, "/sign-in#provider_error=provider_failed")
	})

	t.Run("the storage, when they end", func(t *testing.T) {
		w, f := failingWorld(t)
		w.signUp("ana@example.org")
		// The mark of a sign-in used cannot be read: an error, and not a wrong code.
		attempt, mail := w.start("ana@example.org", "")
		partition := pendingPartition(kindAttempt, attempt)
		f.fail["get "+partition], f.skip["get "+partition] = down, 1
		if _, _, err := w.svc.FinishEmail(ctx, contracts.EmailFinish{Attempt: attempt, Code: code(mail)}, Browser{}); !errors.Is(err, down) {
			t.Fatalf("its mark cannot be read: %v", err)
		}
		delete(f.fail, "get "+partition)
		// It cannot be deleted once used, or once out of tries: it is over all
		// the same, and that is logged.
		f.fail["delete "+partition] = down
		if _, _, err := w.svc.FinishEmail(ctx, contracts.EmailFinish{Attempt: attempt, Code: code(mail)}, Browser{}); err != nil {
			t.Fatalf("a sign-in whose entry cannot be deleted: %v", err)
		}
		attempt, mail = w.start("ana@example.org", "")
		f.fail["delete "+pendingPartition(kindAttempt, attempt)] = down
		for range domain.EmailCodeTries {
			_, _, _ = w.svc.FinishEmail(ctx, contracts.EmailFinish{Attempt: attempt, Code: w.wrongCode(mail)}, Browser{})
		}
		if _, _, err := w.svc.FinishEmail(ctx, contracts.EmailFinish{Attempt: attempt, Code: code(mail)}, Browser{}); !errors.Is(err, contracts.ErrInvalidCode) {
			t.Fatalf("out of tries and not deleted: %v", err)
		}
		for _, line := range []string{"a sign-in that was used could not be deleted", "a sign-in that ran out of tries could not be deleted"} {
			if !strings.Contains(w.logs.String(), line) {
				t.Fatalf("not logged: %q", line)
			}
		}
		// The mark cannot be written: an error.
		attempt, mail = w.start("ana@example.org", "")
		f.fail["put "+usedKey] = down
		if _, _, err := w.svc.FinishEmail(ctx, contracts.EmailFinish{Attempt: attempt, Code: code(mail)}, Browser{}); !errors.Is(err, down) {
			t.Fatalf("its mark cannot be written: %v", err)
		}
		// A ceremony that cannot be read.
		request, err := w.svc.StartPasskeySignIn(ctx)
		if err != nil {
			t.Fatal(err)
		}
		f.fail["get entry"] = down
		if _, _, err := w.svc.FinishPasskeySignIn(ctx, contracts.PasskeySignIn{Ceremony: request.Ceremony}, Browser{}); !errors.Is(err, down) {
			t.Fatalf("a ceremony that cannot be read: %v", err)
		}
		delete(f.fail, "get entry")
		w.now = w.now.Add(time.Hour) // an address is sent five messages an hour
		_, cookie := w.signIn("ana@example.org")
		who := w.who(cookie)
		f.fail["get entry"] = down
		if _, err := w.svc.FinishConfirmPasskey(ctx, who, contracts.PasskeySignIn{Ceremony: request.Ceremony}); !errors.Is(err, down) {
			t.Fatalf("confirming, with a ceremony that cannot be read: %v", err)
		}
	})

	t.Run("an enclave that remembers too many", func(t *testing.T) {
		w := newWorld(t)
		w.signUp("ana@example.org")
		attempt, mail := w.start("ana@example.org", "")
		w.svc.mu.Lock()
		for i := range maxMarks {
			w.svc.marks[pendingPartition(kindAttempt, newID("att"))] = &mark{ends: w.now.Add(time.Duration(i%2) * time.Hour)}
		}
		w.svc.mu.Unlock()
		// Half of them ended: they make room.
		if _, _, err := w.svc.FinishEmail(ctx, contracts.EmailFinish{Attempt: attempt, Code: code(mail)}, Browser{}); err != nil {
			t.Fatalf("with room made: %v", err)
		}
		w.svc.mu.Lock()
		for len(w.svc.marks) < maxMarks {
			w.svc.marks[pendingPartition(kindAttempt, newID("att"))] = &mark{ends: w.now.Add(time.Hour)}
		}
		w.svc.mu.Unlock()
		attempt, mail = w.start("ana@example.org", "")
		if _, _, err := w.svc.FinishEmail(ctx, contracts.EmailFinish{Attempt: attempt, Code: code(mail)}, Browser{}); !errors.Is(err, contracts.ErrInvalidCode) {
			t.Fatalf("with no room: %v", err)
		}
		if !strings.Contains(w.logs.String(), "too many sign-ins in progress remembered") {
			t.Fatalf("not logged:\n%s", w.logs)
		}
	})

	t.Run("a sign-in that waits for its address", func(t *testing.T) {
		w := newWorld(t)
		w.on(idp.Meta)
		w.signUp("ana@example.org")
		one, two := w.enclave(), w.enclave()
		meta := person(idp.Meta, "77007700770077", "", false)
		state, browser := w.leaveFrom(one.svc, nil, contracts.ProviderStart{Provider: "meta"})
		done, err := w.comeBackTo(one.svc, state, browser, meta)
		if err != nil {
			t.Fatal(err)
		}
		confirm := func(e *enclave) contracts.ProviderConfirm {
			t.Helper()
			started, err := e.svc.StartProviderConfirm(ctx, done.Browser, contracts.ProviderConfirmStart{Address: "ana@example.org"})
			if err != nil {
				t.Fatal(err)
			}
			e.svc.Wait()
			sent := w.out.Sent()
			return contracts.ProviderConfirm{Attempt: started.Attempt, Code: code(&sent[len(sent)-1])}
		}
		first, second := confirm(one), confirm(two)
		partition := unconfirmedPartition(done.Browser)
		kept, err := w.mem.GetEntry(ctx, partition, pendingKey)
		if err != nil {
			t.Fatal(err)
		}
		// Ended through both at once, with two codes of the same browser: one does.
		two.entries.partition = partition
		two.entries.beforePut = func() {
			if _, _, err := one.svc.FinishProviderConfirm(ctx, done.Browser, first, Browser{}); err != nil {
				t.Fatalf("the first: %v", err)
			}
		}
		if _, session, err := two.svc.FinishProviderConfirm(ctx, done.Browser, second, Browser{}); !errors.Is(err, contracts.ErrProviderFailed) || session != "" {
			t.Fatalf("the second: %v", err)
		}
		// Put back with its mark deleted: the enclave that ended it remembers.
		w.mem.SetEntry(*kept)
		if err := w.mem.DeleteEntry(ctx, partition, usedKey); err != nil {
			t.Fatal(err)
		}
		if _, err := one.svc.PendingProvider(ctx, done.Browser); !errors.Is(err, contracts.ErrProviderFailed) {
			t.Fatalf("put back where it ended: %v", err)
		}
		if _, err := one.svc.PendingProvider(ctx, ""); !errors.Is(err, contracts.ErrProviderFailed) {
			t.Fatalf("no browser: %v", err)
		}
		// Its mark cannot be written.
		state, browser = w.leave(nil, contracts.ProviderStart{Provider: "meta"})
		if done, err = w.comeBack(state, browser, person(idp.Meta, "77007700770078", "", false)); err != nil {
			t.Fatal(err)
		}
		in := confirm(&enclave{w: w, svc: w.svc})
		f := &failing{Entries: w.mem, fail: map[string]error{"put " + unconfirmedPartition(done.Browser): down}}
		w.svc.cfg.Entries = f
		if _, _, err := w.svc.FinishProviderConfirm(ctx, done.Browser, in, Browser{}); !errors.Is(err, down) {
			t.Fatalf("its mark cannot be written: %v", err)
		}
	})

	t.Run("the storage of the providers' applications", func(t *testing.T) {
		w, f := failingWorld(t)
		f.fail["derive "+sealingPurpose] = down
		if _, err := w.svc.SetProviderApp(ctx, "google", contracts.ProviderAppSet{ClientID: "id", ClientSecret: "Secret-9"}); !errors.Is(err, down) {
			t.Fatalf("setting with no key: %v", err)
		}
	})
}

// Each step of linking a provider account, and of taking a link back, when
// it is overtaken or fails: nothing is linked that was not, and the pointer
// leads where the records say.
func TestLinkingStepsOvertakenOrFailing(t *testing.T) {
	down, ctx := errors.New("down"), context.Background()
	subject := "g-sub-steps"
	partition := providerPartition(idp.Google, subject)
	setup := func(t *testing.T) (*world, *failing, string) {
		t.Helper()
		w, f := failingWorld(t)
		w.on(idp.Google)
		ana, _ := w.signUp("ana@example.org")
		return w, f, ana.Account.ID
	}
	linked := func(w *world, id string) bool {
		t.Helper()
		doc := w.doc(id)
		return doc.signsInWith(idp.Google, subject)
	}

	t.Run("slower than half of linkingFor", func(t *testing.T) {
		w, f, id := setup(t)
		f.Entries = &watched{Entries: w.mem, partition: partition, beforePut: func() { w.now = w.now.Add(linkingFor) }}
		if _, err := w.svc.link(ctx, id, idp.Google, subject, false); !errors.Is(err, contracts.ErrProviderInUse) || linked(w, id) || w.doc(id).Linking != nil {
			t.Fatalf("a link too slow: %v", err)
		}
		if here, err := w.svc.pointsTo(ctx, partition, id); here || err != nil {
			t.Fatalf("the pointer of a link taken back: %v %v", here, err)
		}
	})
	t.Run("another of the same provider listed meanwhile", func(t *testing.T) {
		w, f, id := setup(t)
		f.Entries = &watched{Entries: w.mem, partition: partition, beforePut: func() {
			if _, err := w.svc.rewrite(ctx, id, func(doc *accountDoc) error {
				doc.Providers = append(doc.Providers, providerDoc{Provider: idp.Google, Subject: "g-sub-other", LinkedAt: w.now})
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		}}
		if _, err := w.svc.link(ctx, id, idp.Google, subject, false); !errors.Is(err, contracts.ErrProviderLinked) || linked(w, id) {
			t.Fatalf("a second account of one provider: %v", err)
		}
	})
	for _, c := range []struct {
		name string
		then func(w *world, id string)
	}{
		{"its pointer deleted before the last step", func(w *world, _ string) { _ = w.mem.DeleteEntry(ctx, partition, loginKey) }},
		{"its pointer taken before the last step", func(w *world, _ string) {
			_ = w.mem.DeleteEntry(ctx, partition, loginKey)
			_ = w.svc.point(ctx, partition, loginKey, newID("acc"), 0)
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			w, f, id := setup(t)
			f.Entries = &nth{Entries: w.mem, partition: partition, n: 2, before: func() { c.then(w, id) }}
			if _, err := w.svc.link(ctx, id, idp.Google, subject, false); !errors.Is(err, contracts.ErrProviderInUse) || linked(w, id) {
				t.Fatalf("%s: %v", c.name, err)
			}
		})
	}
	t.Run("the storage", func(t *testing.T) {
		for _, op := range []string{"pointer read again", "last record", "key", "keeps changing"} {
			w, f, id := setup(t)
			switch op {
			case "pointer read again":
				f.fail["get "+partition], f.skip["get "+partition] = down, 1
			case "last record":
				f.fail["put record"], f.skip["put record"] = down, 1
			case "key":
				f.fail["derive "+signingPurpose], f.skip["derive "+signingPurpose] = down, 2
			case "keeps changing":
				f.fail["put "+partition] = store.ErrExists
			}
			_, err := w.svc.link(ctx, id, idp.Google, subject, false)
			f.fail = map[string]error{}
			if err == nil || linked(w, id) {
				t.Fatalf("%s: %v", op, err)
			}
		}
		// The account the pointer leads to cannot be read.
		w, f, id := setup(t)
		bea, _ := w.signUp("bea@example.org")
		if _, err := w.svc.link(ctx, bea.Account.ID, idp.Google, subject, false); err != nil {
			t.Fatal(err)
		}
		f.fail["get record"], f.skip["get record"] = down, 1
		if _, err := w.svc.link(ctx, id, idp.Google, subject, false); !errors.Is(err, down) {
			t.Fatalf("the other account cannot be read: %v", err)
		}
		if !strings.Contains(w.logs.String(), "a link of a provider that did not end could not be taken back") {
			t.Fatalf("not logged:\n%s", w.logs)
		}
		f.fail = map[string]error{}
		if linked(w, id) || !linked(w, bea.Account.ID) {
			t.Fatal("linked to the other account")
		}
	})
	t.Run("taking back and unclaiming", func(t *testing.T) {
		w, f, id := setup(t)
		if _, err := w.svc.link(ctx, id, idp.Google, subject, false); err != nil {
			t.Fatal(err)
		}
		// A claim that is not the record's takes nothing back.
		w.svc.unlinking(ctx, id, "another claim", idp.Google, subject)
		if !linked(w, id) {
			t.Fatal("a link taken back by another claim")
		}
		// The record holds it: its pointer stays.
		if err := w.svc.unclaim(ctx, id, idp.Google, subject); err != nil {
			t.Fatal(err)
		}
		if here, _ := w.svc.pointsTo(ctx, partition, id); !here {
			t.Fatal("the pointer of a link that stayed was deleted")
		}
		for _, op := range []string{"get " + partition, "derive " + signingPurpose, "get record", "delete " + partition} {
			f.fail = map[string]error{op: down}
			if op == "delete "+partition {
				// Unlinked in the record, by hand: the pointer is to go.
				if _, err := w.svc.rewrite(ctx, id, func(doc *accountDoc) error { doc.Providers = nil; return nil }); err != nil {
					t.Fatal(err)
				}
			}
			if err := w.svc.unclaim(ctx, id, idp.Google, subject); !errors.Is(err, down) {
				t.Fatalf("%s down, unclaiming: %v", op, err)
			}
		}
		f.fail = map[string]error{"get " + partition: down}
		if _, err := w.svc.pointsTo(ctx, partition, id); !errors.Is(err, down) {
			t.Fatalf("reading the pointer: %v", err)
		}
		f.fail = map[string]error{"derive " + signingPurpose: down}
		if _, err := w.svc.pointsTo(ctx, partition, id); !errors.Is(err, down) {
			t.Fatalf("the key, reading the pointer: %v", err)
		}
	})
}

// The record of sessions and accounts when keys or storage fail.
func TestSignOutsAndRecordsFailures(t *testing.T) {
	down, ctx := errors.New("down"), context.Background()
	w, _ := failingWorld(t)
	ana, _ := w.signUp("ana@example.org")
	id := ana.Account.ID

	// A record signed for an account that is another.
	key, _ := w.svc.signingKey(ctx)
	raw, _ := json.Marshal(accountDoc{V: 1, ID: newID("acc")})
	value, _ := envelope.Sign(key, raw, recordAAD(recordName(id)))
	if err := w.anchored.PutRecord(ctx, &store.Record{Name: recordName(id), Value: value, Plain: true}, false); err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.read(ctx, id); err == nil {
		t.Fatal("a record of another account was read as this one")
	}
	w, f := failingWorld(t)
	ana, cookie := w.signUp("ana@example.org")
	who := w.who(cookie)
	id = ana.Account.ID

	// The key that signs: storing a record, a pointer, a session, a device,
	// invitations; reading sessions and the pointer of a moving address.
	doc := w.doc(id)
	f.fail["derive "+signingPurpose] = down
	if err := w.svc.makeAccount(ctx, doc); !errors.Is(err, down) {
		t.Fatalf("a record: %v", err)
	}
	if err := w.svc.putSession(ctx, &sessionDoc{Account: id, ID: newID("ses")}, 0); !errors.Is(err, down) {
		t.Fatalf("a session: %v", err)
	}
	if _, err := w.svc.sessions(ctx, id, nil); !errors.Is(err, down) {
		t.Fatalf("listing sessions: %v", err)
	}
	if err := w.svc.claimAddress(ctx, id, "new@example.org"); !errors.Is(err, down) {
		t.Fatalf("claiming an address: %v", err)
	}
	if _, err := w.svc.sessionOpen(ctx, id, who.Session.ID); !errors.Is(err, down) {
		t.Fatalf("whether a session is open: %v", err)
	}
	delete(f.fail, "derive "+signingPurpose)
	f.fail["get record"] = down
	if _, err := w.svc.sessionOpen(ctx, id, who.Session.ID); !errors.Is(err, down) {
		t.Fatalf("whether a session is open, its account unreadable: %v", err)
	}
	if _, err := w.svc.confirm(ctx, who, contracts.ConfirmEmail); !errors.Is(err, down) {
		t.Fatalf("confirming, its account unreadable: %v", err)
	}
	delete(f.fail, "get record")
	w.svc.cfg.Keys = shortFor{f, signingPurpose}
	if err := w.svc.makeAccount(ctx, &accountDoc{V: 1, ID: newID("acc")}); err == nil {
		t.Fatal("a record signed with a short key")
	}
	if err := w.svc.point(ctx, accountsPartition, "x", id, 0); err == nil {
		t.Fatal("a pointer signed with a short key")
	}
	if err := w.svc.putSession(ctx, &sessionDoc{Account: id, ID: newID("ses")}, 0); err == nil {
		t.Fatal("a session signed with a short key")
	}
	if _, err := w.svc.noteDevice(ctx, id, NewDevice()); err == nil {
		t.Fatal("a device signed with a short key")
	}
	if _, err := w.svc.CreateInvitations(ctx, 1); err == nil {
		t.Fatal("an invitation signed with a short key")
	}
	if open, err := w.svc.sessionOpen(ctx, id, who.Session.ID); open || err != nil {
		t.Fatalf("a session that does not verify is open: %v", err)
	}
	w.svc.cfg.Keys = f
	// A session signed out since stands for nothing when it confirms.
	if _, err := w.svc.CloseAccountSessions(ctx, id); err != nil {
		t.Fatal(err)
	}
	if err := w.svc.putSession(ctx, &sessionDoc{V: 1, Account: id, ID: who.Session.ID, CreatedAt: w.now, LastUsedAt: w.now}, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := w.svc.confirm(ctx, who, contracts.ConfirmEmail); !errors.Is(err, contracts.ErrUnauthorized) {
		t.Fatalf("a session signed out since, confirming: %v", err)
	}
	// A record that cannot be stored after the pointers of the list.
	f.fail["put record"] = down
	if _, _, err := w.svc.create(ctx, "new@example.org", nil); !errors.Is(err, down) {
		t.Fatalf("a record that cannot be stored: %v", err)
	}
}

// Removing an account that is due, when its record keeps changing, when the
// key fails, and with a link that stopped halfway.
func TestRemovalSteps(t *testing.T) {
	down, ctx := errors.New("down"), context.Background()
	closing := func(t *testing.T) (*world, *failing, string) {
		t.Helper()
		w, f := failingWorld(t)
		w.on(idp.Google)
		ana, cookie := w.signUp("ana@example.org")
		if _, err := w.svc.StartDeletion(ctx, w.fresh(w.who(cookie))); err != nil {
			t.Fatal(err)
		}
		return w, f, ana.Account.ID
	}
	t.Run("a link that stopped halfway", func(t *testing.T) {
		w, _, id := closing(t)
		partition := providerPartition(idp.Google, "g-sub-half")
		if _, err := w.svc.rewrite(ctx, id, func(doc *accountDoc) error {
			doc.Linking = &linkingDoc{Provider: idp.Google, Subject: "g-sub-half", Claim: "c", At: w.now}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
		if err := w.svc.point(ctx, partition, loginKey, id, 0); err != nil {
			t.Fatal(err)
		}
		w.now = w.now.Add(domain.AccountClosing)
		if err := w.svc.RemoveDue(ctx); err != nil {
			t.Fatal(err)
		}
		if _, err := w.mem.GetEntry(ctx, partition, loginKey); !errors.Is(err, store.ErrNotFound) {
			t.Fatalf("the pointer of a link that stopped halfway: %v", err)
		}
	})
	t.Run("the key", func(t *testing.T) {
		w, f, id := closing(t)
		w.now = w.now.Add(domain.AccountClosing)
		// Reading the record, and writing that it is being removed, have the key.
		f.fail["derive "+signingPurpose], f.skip["derive "+signingPurpose] = down, 2
		if err := w.svc.remove(ctx, id); !errors.Is(err, down) {
			t.Fatalf("removing without the key: %v", err)
		}
		f.fail = map[string]error{}
		if doc := w.doc(id); doc == nil || !doc.Removing {
			t.Fatalf("the record does not say it is being removed: %+v", doc)
		}
	})
	t.Run("a record that keeps changing", func(t *testing.T) {
		w, _, id := closing(t)
		w.now = w.now.Add(domain.AccountClosing)
		one := w.enclave()
		one.records.always = true
		one.records.meanwhile = func() {
			doc := w.doc(id)
			if doc == nil {
				return
			}
			rec, err := w.svc.signed(ctx, doc)
			if err != nil {
				t.Fatal(err)
			}
			if err := w.anchored.PutRecord(ctx, rec, false); err != nil {
				t.Fatal(err)
			}
		}
		if err := one.svc.remove(ctx, id); err == nil || !strings.Contains(err.Error(), "keeps changing") {
			t.Fatalf("removing a record that keeps changing: %v", err)
		}
		if err := one.svc.settle(ctx, id, closingKey(w.doc(id))); err == nil || !strings.Contains(err.Error(), "keeps changing") {
			t.Fatalf("settling a record that keeps changing: %v", err)
		}
	})
}
