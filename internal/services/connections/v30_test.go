package connections

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/oauth"
	"github.com/permaily-org/permaily/internal/store"
)

// V30. Deletion (finding 6), through either enclave, with a played clock
// and crashes.
func TestV30Deletion(t *testing.T) {
	ctx := context.Background()

	t.Run("EndAll fences the list first, made when there is none", func(t *testing.T) {
		w := newWorld(t)
		s := w.enclave()
		if err := s.EndAll(ctx, accountB); err != nil {
			t.Fatal(err)
		}
		l, _, err := s.readList(ctx, accountB)
		if err != nil || l == nil || !l.Closing || l.Generation != 1 {
			t.Fatal(l, err)
		}
		// A first approval racing it fails.
		w.accts.set(accountB, StandingOpen)
		p := w.person(accountB)
		req := w.start(s, p, w.ask(claudeCode))
		d := req.Draft
		if _, err := s.Decide(ctx, p, req.ID, contracts.AuthorizationDecision{Allow: true, Draft: &d, ModelNotice: notice, Snapshot: &req.Snapshot}); !errors.Is(err, contracts.ErrRequestEnded) {
			t.Fatal(err)
		}
		if err := s.EndAll(ctx, accountB); err != nil { // again: nothing more
			t.Fatal(err)
		}
	})

	for _, pause := range []string{"after the standing read", "after the pending entry", "after the record, before the last list write"} {
		for _, through := range []string{"the same enclave", "the other enclave"} {
			t.Run("an approval paused "+pause+", the fence through "+through, func(t *testing.T) {
				w := newWorld(t)
				s, f := w.hooked()
				other := s
				if through == "the other enclave" {
					other = w.enclave()
				}
				p := w.person(accountA)
				tok, _ := w.connect(s, p, w.ask(otherApp)) // one made before, ended by the fence
				x := w.ask(claudeCode)
				req := w.start(s, p, x)
				fenced := false
				fenceNow := func() {
					if !fenced {
						fenced = true
						w.accts.set(accountA, StandingClosing)
						if err := other.EndAll(ctx, accountA); err != nil {
							t.Error(err)
						}
					}
				}
				switch pause {
				case "after the standing read":
					w.vendors.mu.Lock()
					w.vendors.onClient = fenceNow // after the uncached read, before anything is written
					w.vendors.mu.Unlock()
				case "after the pending entry":
					f.set(func(op, name string) error {
						if op == "create" && strings.HasPrefix(name, RecordConnection) {
							fenceNow()
						}
						return nil
					})
				default:
					writes := 0
					f.set(func(op, name string) error {
						if op == "put" && strings.HasPrefix(name, RecordList) {
							if writes++; writes == 2 {
								fenceNow()
							}
						}
						return nil
					})
				}
				d := req.Draft
				ans, err := s.Decide(ctx, p, req.ID, contracts.AuthorizationDecision{Allow: true, Draft: &d, ModelNotice: notice, Snapshot: &req.Snapshot})
				f.set(nil)
				w.vendors.mu.Lock()
				w.vendors.onClient = nil
				w.vendors.mu.Unlock()
				if err == nil {
					// No code it issued works.
					if _, err := w.exchange(s, x, codeOf(t, ans.Redirect)); err == nil {
						t.Fatal("a code issued past the fence works")
					}
				}
				if err := other.EndAll(ctx, accountA); err != nil {
					t.Fatal(err)
				}
				l, _, _ := s.readList(ctx, accountA)
				for _, m := range l.Members {
					c, _, _ := s.readConn(ctx, m.ID)
					if m.State != contracts.ConnectionEnded || (c != nil && c.State != contracts.ConnectionEnded) {
						t.Fatalf("%+v %+v", m, c)
					}
				}
				for _, e := range []*Service{s, w.enclave()} {
					if _, err := w.check(e, w.ask(otherApp), tok.AccessToken); !errors.Is(err, ErrInvalidToken) {
						t.Fatal(err)
					}
				}
			})
		}
	}

	t.Run("a partial EndAll finished; Reopen ends what is left first", func(t *testing.T) {
		w := newWorld(t)
		s, f := w.hooked()
		p := w.person(accountA)
		t1, _ := w.connect(s, p, w.ask(claudeCode))
		t2, _ := w.connect(s, p, w.ask(otherApp))
		second := w.connectionOf(s, accountA, otherApp).ID
		f.set(func(op, name string) error {
			if op == "put" && name == connName(second) {
				return errCrash
			}
			return nil
		})
		if err := s.EndAll(ctx, accountA); !errors.Is(err, errCrash) {
			t.Fatal(err)
		}
		f.set(nil)
		// Its tokens are refused already: the list is closing.
		if _, err := w.check(w.enclave(), w.ask(otherApp), t2.AccessToken); !errors.Is(err, ErrInvalidToken) {
			t.Fatal(err)
		}
		// Cancelled at once: what of the closing generation is still active
		// ends first, and nothing comes back.
		if err := w.enclave().Reopen(ctx, accountA); err != nil {
			t.Fatal(err)
		}
		l, _, _ := s.readList(ctx, accountA)
		if l.Closing {
			t.Fatal("still fenced")
		}
		for _, m := range l.Members {
			if m.State != contracts.ConnectionEnded {
				t.Fatalf("%+v", m)
			}
		}
		for _, tok := range []string{t1.AccessToken, t2.AccessToken} {
			if _, err := w.enclave().Check(ctx, contracts.ResourceMCP, tok); !errors.Is(err, ErrInvalidToken) {
				t.Fatal(err)
			}
		}
		// The account approves again once open.
		w.connect(s, w.person(accountA), w.ask(claudeCode))
		if err := s.Reopen(ctx, accountA); err != nil { // not closing: nothing
			t.Fatal(err)
		}
		if err := s.Reopen(ctx, accountB); err != nil { // no list: nothing
			t.Fatal(err)
		}
	})

	t.Run("the sweep finishes a partial EndAll", func(t *testing.T) {
		w := newWorld(t)
		s, f := w.hooked()
		p := w.person(accountA)
		w.connect(s, p, w.ask(claudeCode))
		w.connect(s, p, w.ask(otherApp))
		second := w.connectionOf(s, accountA, otherApp).ID
		f.set(func(op, name string) error {
			if op == "put" && name == connName(second) {
				return errCrash
			}
			return nil
		})
		_ = s.EndAll(ctx, accountA)
		f.set(nil)
		if err := w.enclave().Sweep(ctx); err != nil {
			t.Fatal(err)
		}
		l, _, _ := s.readList(ctx, accountA)
		for _, m := range l.Members {
			if m.State != contracts.ConnectionEnded {
				t.Fatalf("%+v", m)
			}
		}
	})
}

// V35. Sweep.
func TestV35Sweep(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	s := w.enclave()
	p := w.person(accountA)
	x := w.ask(otherApp)
	req := w.pushed(s, p, x, "", map[string][]string{"ends_in_days": {"1"}})
	tok, err := w.exchange(s, x, w.allow(s, p, req))
	if err != nil {
		t.Fatal(err)
	}
	id := w.connectionOf(s, accountA, otherApp).ID
	fam := w.connectionOf(s, accountA, otherApp).Devices[0].ID
	other, _ := w.connect(s, p, w.ask(claudeCode))

	// The index deleted by whoever writes the table changes nothing of what
	// is decided: past its end, the connection's tokens are refused.
	saved, _ := w.mem.GetEntry(ctx, partitionAccounts, accountA)
	_ = w.mem.DeleteEntry(ctx, partitionAccounts, accountA)
	w.clock.add(25 * time.Hour)
	if _, err := w.check(w.enclave(), x, tok.AccessToken); !errors.Is(err, ErrInvalidToken) {
		t.Fatal(err)
	}
	if err := s.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	w.mem.SetEntry(*saved)
	// An entry of the index not written by this deployment is skipped.
	w.mem.SetEntry(store.Entry{Partition: partitionAccounts, Key: "acc_forged", Value: []byte(`{"doc":"\"acc_forged\""}`)})
	if err := s.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	c, _, _ := s.readConn(ctx, id)
	if c.State != contracts.ConnectionEnded || c.EndReason != contracts.EndedByTime {
		t.Fatalf("%+v", c)
	}
	l, _, _ := s.readList(ctx, accountA)
	if l.Current[otherApp] != "" {
		t.Fatal("still current")
	}
	// The other connection, not past its end, is untouched.
	if _, err := w.refresh(s, w.ask(claudeCode), other.RefreshToken); err != nil {
		t.Fatal(err)
	}

	// 30 days after it ended, it is removed with its families' records,
	// and its entry with it.
	w.clock.add(EndedKept)
	if err := w.enclave().Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if c, _, err := s.readConn(ctx, id); c != nil || err != nil {
		t.Fatal("kept", c, err)
	}
	if f, _, err := s.readFamily(ctx, fam, 0); f != nil || err != nil {
		t.Fatal("its family kept", err)
	}
	l, _, _ = s.readList(ctx, accountA)
	for _, m := range l.Members {
		if m.ID == id {
			t.Fatal("its entry kept")
		}
	}
	// Never read as an earlier copy: it cannot be made again either.
	if err := s.writeConn(ctx, c0(id), store.Mark{}, true); !errors.Is(err, store.ErrExists) {
		t.Fatal(err)
	}
}

func c0(id string) *connDoc {
	return &connDoc{V: 1, ID: id, State: contracts.ConnectionActive, private: &connPrivate{}}
}

// V35, C1: dead records of assertions are removed through their sealed
// list; with that list deleted, nothing is accepted twice.
func TestV35SweepRemovesDeadAssertionRecords(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	s := w.enclave()
	p := w.person(accountA)
	x := w.ask(chatgpt)
	w.connect(s, p, x)
	var names []string
	for _, e := range w.mem.AllEntries() {
		if strings.HasPrefix(e.Partition, PartitionJTI) {
			names = append(names, e.Key)
		}
	}
	if len(names) == 0 {
		t.Fatal("no record listed")
	}
	j := w.assertion(chatgpt, issuer+oauth.PathRevoke, "kept", w.clock.now().Add(time.Minute))
	revoke := func(s *Service) error {
		return s.RevokeToken(ctx, map[string][]string{"token": {"pma_x"}, "client_id": {chatgpt}, "client_assertion_type": {oauth.AssertionType}, "client_assertion": {j}})
	}
	if err := revoke(s); err != nil {
		t.Fatal(err)
	}
	// Within its hour, the sweep removes nothing.
	if err := s.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if err := revoke(w.enclave()); oauthCode(err) != oauth.ErrInvalidClient {
		t.Fatal(err)
	}
	// Its list deleted: nothing is accepted twice.
	for _, e := range w.mem.AllEntries() {
		if strings.HasPrefix(e.Partition, PartitionJTI) {
			_ = w.mem.DeleteEntry(ctx, e.Partition, e.Key)
		}
	}
	if err := revoke(w.enclave()); oauthCode(err) != oauth.ErrInvalidClient {
		t.Fatal(err)
	}
	// Two hours on, the records it lists are removed.
	j2 := w.assertion(chatgpt, issuer+oauth.PathRevoke, "later", w.clock.now().Add(time.Minute))
	if err := s.RevokeToken(ctx, map[string][]string{"token": {"pma_x"}, "client_id": {chatgpt}, "client_assertion_type": {oauth.AssertionType}, "client_assertion": {j2}}); err != nil {
		t.Fatal(err)
	}
	w.clock.add(3 * time.Hour)
	if err := s.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	listed := 0
	for _, e := range w.mem.AllEntries() {
		if strings.HasPrefix(e.Partition, PartitionJTI) {
			listed++
		}
	}
	if listed != 0 {
		t.Fatalf("%d left", listed)
	}
	if err := revoke(w.enclave()); oauthCode(err) != oauth.ErrInvalidClient {
		t.Fatal("a dead assertion accepted", err)
	}
}

// V36. Two enclaves for every flow.
func TestV36TwoEnclaves(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	a, b := w.enclave(), w.enclave()
	p := w.person(accountA)
	x := w.ask(claudeCode)
	// Started through A, decided through B; its code made through A
	// exchanged through B.
	req := w.start(a, p, x)
	code := w.allow(b, p, req)
	tok, err := w.exchange(b, x, code)
	if err != nil {
		t.Fatal(err)
	}
	// A device added through B to a connection made through A.
	y := w.ask(claudeCode)
	tok2, err := w.exchange(a, y, w.allow(b, p, w.start(b, p, y)))
	if err != nil {
		t.Fatal(err)
	}
	if n := w.connectionOf(a, accountA, claudeCode).activeDevices(); n != 2 {
		t.Fatal(n)
	}
	w.grant(a, x, tok.AccessToken)
	w.grant(b, y, tok2.AccessToken)
	// Revoked through B, made through A.
	c := w.connectionOf(a, accountA, claudeCode)
	if _, err := b.Revoke(ctx, p, c.ID); err != nil {
		t.Fatal(err)
	}
	w.clock.add(oauth.ConnectionFresh)
	for _, s := range []*Service{a, b} {
		for _, tk := range []string{tok.AccessToken, tok2.AccessToken} {
			if _, err := s.Check(ctx, contracts.ResourceMCP, tk); !errors.Is(err, ErrInvalidToken) {
				t.Fatal(err)
			}
		}
	}
	// The person's list through either.
	for _, s := range []*Service{a, b} {
		l, err := s.List(ctx, p)
		if err != nil || len(l.Connections) != 1 || l.Connections[0].State != contracts.ConnectionEnded || len(l.Connections[0].Devices) != 2 {
			t.Fatal(l, err)
		}
	}
	if l, err := a.List(ctx, w.person(accountB)); err != nil || len(l.Connections) != 0 {
		t.Fatal(l, err)
	}
	if _, err := a.Get(ctx, w.person(accountB), c.ID); !errors.Is(err, contracts.ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := a.Get(ctx, p, "nothing"); !errors.Is(err, contracts.ErrNotFound) {
		t.Fatal(err)
	}
}

// V64. The account's list (C2), with an honest store counting versions.
func TestV64TheAccountsList(t *testing.T) {
	ctx := context.Background()

	t.Run("more than 10,000 writes over its life", func(t *testing.T) {
		w := newWorld(t)
		s := w.enclave()
		for i := range 10001 {
			if i%ListDecidedMax == 0 {
				w.clock.add(oauth.RequestTTL)
			}
			if _, err := s.updateList(ctx, accountA, func(l *ListState) error { return l.Decide(fmt.Sprint("r", i), w.clock.now()) }); err != nil {
				t.Fatal(i, err)
			}
		}
		l, _, err := w.enclave().readList(ctx, accountA)
		if err != nil || l.Epoch != 10001/(ListEpochVersions-1) {
			t.Fatal(l.Epoch, err)
		}
	})

	t.Run("its rollover with a crash between moved and the next epoch", func(t *testing.T) {
		w := newWorld(t)
		s, f := w.hooked()
		p := w.person(accountA)
		w.connect(s, p, w.ask(claudeCode))
		if _, err := s.updateList(ctx, accountA, func(l *ListState) error { return l.Decide("a-decision", w.clock.now()) }); err != nil {
			t.Fatal(err)
		}
		if err := s.EndAll(ctx, accountA); err != nil {
			t.Fatal(err)
		}
		l, at, _ := s.readList(ctx, accountA)
		// The epoch near its end.
		for at.Version() < ListEpochVersions-1 {
			if _, err := s.updateList(ctx, accountA, func(*ListState) error { return nil }); err != nil {
				t.Fatal(err)
			}
			l, at, _ = s.readList(ctx, accountA)
		}
		f.set(func(op, name string) error {
			if op == "create" && name == ListName(accountA, 1) {
				return errCrash
			}
			return nil
		})
		if err := s.writeList(ctx, l, at, false); !errors.Is(err, errCrash) {
			t.Fatal(err)
		}
		f.set(nil)
		// A reader after a restart follows Moved and makes the next epoch.
		r := w.enclave()
		n, _, err := r.readList(ctx, accountA)
		if err != nil || n.Epoch != 1 || !n.Closing || n.Generation != 1 || len(n.Members) != 1 || len(n.Decided) != 2 {
			t.Fatalf("%+v %v", n, err)
		}
		// The same carried: the current connection of each client.
		if _, err := r.updateList(ctx, accountA, func(l *ListState) error { l.Closing = false; return nil }); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("64 active, the 65th too_many_connections, a replacement not counted twice", func(t *testing.T) {
		w := newWorld(t)
		s := w.enclave()
		p := w.person(accountA)
		var clients []string
		for i := range ListActiveMax {
			if i > 0 && i%(ListDecidedMax/2) == 0 {
				w.clock.add(oauth.RequestTTL) // the list keeps 64 decisions of 15 minutes
				p = w.person(accountA)
			}
			id := fmt.Sprintf("https://app%d.example.org/client.json", i)
			w.vendors.clients[id] = w.vendors.clients[otherApp]
			c := *w.vendors.clients[otherApp]
			c.ID, c.Document.ClientID = id, id
			w.vendors.clients[id] = &c
			clients = append(clients, id)
			x := w.ask(otherApp)
			x.client = id
			w.allow(s, p, w.start(s, p, x))
		}
		x := w.ask(claudeCode)
		req := w.start(s, p, x)
		d := req.Draft
		if _, err := s.Decide(ctx, p, req.ID, contracts.AuthorizationDecision{Allow: true, Draft: &d, ModelNotice: notice, Snapshot: &req.Snapshot}); !errors.Is(err, contracts.ErrTooManyConnections) {
			t.Fatal(err)
		}
		// A replacement of one of them.
		y := w.ask(otherApp)
		y.client, y.resource = clients[0], apiResource
		req = w.start(s, p, y)
		if req.Change != contracts.ChangeReplace {
			t.Fatal(req.Change)
		}
		w.allow(s, p, req)

		// Ended past 64: the oldest ended one's record removed before its
		// entry; an active one never.
		l, _, _ := s.readList(ctx, accountA)
		var oldest string
		for _, m := range l.Members {
			if m.State == contracts.ConnectionEnded {
				oldest = m.ID
			}
		}
		for i := range ListActiveMax {
			w.clock.add(time.Second)
			c := w.connectionOf(s, accountA, clients[i])
			if _, err := s.Revoke(ctx, p, c.ID); err != nil {
				t.Fatal(err)
			}
		}
		l, _, _ = s.readList(ctx, accountA)
		ended := 0
		for _, m := range l.Members {
			if m.ID == oldest {
				t.Fatal("the oldest ended one kept")
			}
			if m.State == contracts.ConnectionEnded {
				ended++
			}
		}
		if ended != ListEndedMax {
			t.Fatal(ended)
		}
		if c, _, _ := s.readConn(ctx, oldest); c != nil {
			t.Fatal("its record kept")
		}
		w.grant(s, y, mustToken(t, w, s, y))
	})
}

// mustToken connects y's client again and answers its access token.
func mustToken(t *testing.T, w *world, s *Service, y *ask) string {
	t.Helper()
	tok, _ := w.connect(s, w.person(accountA), y)
	return tok.AccessToken
}
