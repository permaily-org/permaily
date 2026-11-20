package accounts

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/domain"
	"github.com/permaily-org/permaily/internal/keys"
	"github.com/permaily-org/permaily/internal/oauth"
	"github.com/permaily-org/permaily/internal/services/accountrules/accountrulestest"
	"github.com/permaily-org/permaily/internal/services/connections"
	"github.com/permaily-org/permaily/internal/store"
)

// Removing an account removes its connections (ADR 0030, "Changed while
// releasing step 5"): every epoch of its list, its entry in the index the
// sweep walks, and every connection's and token family's record go with
// the account, not 30 days later and not never; what stays is the commit
// of each removal, which names a record and holds nothing of it, and which
// keeps the old tokens dead and the list from being made again, whatever
// of the removed records is put back.

// realEnclave is one enclave of the real connections service over the
// world's store, its records anchored as the composition anchors them.
func realEnclave(t *testing.T, w *world, k connections.Keys) *connections.Service {
	t.Helper()
	clock := func() time.Time { return w.now }
	s, err := connections.New(connections.Config{
		OAuth:   oauth.Config{Issuer: "https://api.permaily.test", AppOrigin: "https://app.permaily.test", APIResource: "https://api.permaily.test/v1", MCPResource: "https://mcp.permaily.test/mcp"},
		Records: &store.Anchored{Records: w.mem, Commits: w.commits, Strict: true, OnlyNewest: true, Now: clock},
		Entries: w.mem, Keys: k, Vendors: realVendors{}, Accounts: w.svc, Mailboxes: realMailboxes{},
		Receipts: realReceipts{}, Source: &accountrulestest.Fake{}, ModelNotice: w.notice, Now: clock,
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// realConnect approves realClient for p through s and exchanges its code.
func realConnect(t *testing.T, s *connections.Service, p connections.Person) *connections.TokenAnswer {
	t.Helper()
	ctx := context.Background()
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	verifier := base64.RawURLEncoding.EncodeToString(b)
	sum := sha256.Sum256([]byte(verifier))
	q := url.Values{"client_id": {realClient}, "redirect_uri": {"http://localhost/callback"}, "response_type": {"code"},
		"code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])}, "code_challenge_method": {"S256"}, "resource": {"https://mcp.permaily.test/mcp"}}
	started, err := s.Start(ctx, p, q.Encode())
	if err != nil || started.Request == nil {
		t.Fatal(started, err)
	}
	req := started.Request
	ans, err := s.Decide(ctx, p, req.ID, contracts.AuthorizationDecision{Allow: true, Draft: &req.Draft, Snapshot: &req.Snapshot, ModelNotice: req.ModelNotice})
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(ans.Redirect)
	tok, err := s.Token(ctx, url.Values{"grant_type": {"authorization_code"}, "code": {u.Query().Get("code")}, "code_verifier": {verifier},
		"redirect_uri": {"http://localhost/callback"}, "client_id": {realClient}, "resource": {"https://mcp.permaily.test/mcp"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Check(ctx, contracts.ResourceMCP, tok.AccessToken); err != nil {
		t.Fatal(err)
	}
	return tok
}

// connectionRecords are the names of everything stored of connections:
// lists, connections and token families, every version.
func connectionRecords(w *world) []string {
	var out []string
	for _, name := range w.mem.RecordNames() {
		if strings.HasPrefix(name, connections.RecordList) || strings.HasPrefix(name, connections.RecordConnection) || strings.HasPrefix(name, connections.RecordFamily) {
			out = append(out, name)
		}
	}
	return out
}

// indexPartition is where the connections service lists the accounts its
// sweep walks.
const indexPartition = "connections#accounts"

func TestRemovingAnAccountLeavesNothingOfItsConnections(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	k, err := keys.NewLocal()
	if err != nil {
		t.Fatal(err)
	}
	conns := realEnclave(t, w, k)
	w.svc.SetConnections(conns)
	me, cookie := w.signUp("ana@example.org")
	id := me.Account.ID
	who := w.who(cookie)
	p := func() connections.Person {
		return connections.Person{Account: id, Session: "ses_test", ConfirmedUntil: w.now.Add(10 * time.Minute)}
	}

	// A connection the person revoked, then another: one ended, one
	// active, each with a token family.
	first := realConnect(t, conns, p())
	list, err := conns.List(ctx, p())
	if err != nil || len(list.Connections) != 1 {
		t.Fatal(list, err)
	}
	if _, err := conns.Revoke(ctx, p(), list.Connections[0].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := conns.Check(ctx, contracts.ResourceMCP, first.AccessToken); !errors.Is(err, connections.ErrInvalidToken) {
		t.Fatalf("the revoked connection still works: %v", err)
	}
	second := realConnect(t, conns, p())

	// A deletion asked and cancelled: what was connected ends, and a new
	// connection works afterwards.
	if _, err := w.svc.StartDeletion(ctx, w.fresh(who)); err != nil {
		t.Fatal(err)
	}
	if got, err := w.svc.CancelDeletion(ctx, who); err != nil || got.State != contracts.AccountActive {
		t.Fatal(got, err)
	}
	w.now = w.now.Add(oauth.ConnectionFresh)
	if _, err := conns.Check(ctx, contracts.ResourceMCP, second.AccessToken); !errors.Is(err, connections.ErrInvalidToken) {
		t.Fatalf("a connection made before a cancelled deletion works: %v", err)
	}
	third := realConnect(t, conns, p())

	// Everything stored of them, as whoever runs the storage could keep it.
	kept := map[string]store.Record{}
	for _, name := range connectionRecords(w) {
		r, err := w.mem.GetRecord(ctx, name)
		if err != nil {
			t.Fatal(err)
		}
		kept[name] = *r
	}
	if len(kept) == 0 {
		t.Fatal("nothing of the connections is stored")
	}
	index, err := w.mem.GetEntry(ctx, indexPartition, id)
	if err != nil {
		t.Fatal("the account is not in the sweep's index:", err)
	}

	// The deletion asked again, and the account removed when it is due.
	if _, err := w.svc.StartDeletion(ctx, w.fresh(w.who(cookie))); err != nil {
		t.Fatal(err)
	}
	w.now = w.now.Add(domain.AccountClosing)
	if err := w.svc.RemoveDue(ctx); err != nil {
		t.Fatal(err)
	}
	if left := connectionRecords(w); len(left) != 0 {
		t.Fatalf("the account was removed and these records of its connections stay: %v", left)
	}
	if _, err := w.mem.GetEntry(ctx, indexPartition, id); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("the account was removed and its entry in the sweep's index stays: %v", err)
	}

	// The hourly pass, past the 30 days an ended connection is kept, finds
	// nothing to do and makes nothing again.
	w.now = w.now.Add(connections.EndedKept + time.Hour)
	if err := realEnclave(t, w, k).Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if left := connectionRecords(w); len(left) != 0 {
		t.Fatalf("the sweep left or made records: %v", left)
	}

	// Whoever runs the storage puts back everything that was removed.
	for _, r := range kept {
		if err := w.mem.PutRecord(ctx, &r, false); err != nil {
			t.Fatal(err)
		}
	}
	w.mem.SetEntry(*index)
	fresh := realEnclave(t, w, k)
	for _, tok := range []*connections.TokenAnswer{first, second, third} {
		if _, err := fresh.Check(ctx, contracts.ResourceMCP, tok.AccessToken); !errors.Is(err, connections.ErrInvalidToken) {
			t.Fatalf("a token of a removed account works once its records are put back: %v", err)
		}
		_, err := fresh.Token(ctx, url.Values{"grant_type": {"refresh_token"}, "refresh_token": {tok.RefreshToken}, "client_id": {realClient}, "resource": {"https://mcp.permaily.test/mcp"}})
		var oe *connections.OAuthError
		if !errors.As(err, &oe) || oe.Code != oauth.ErrInvalidGrant {
			t.Fatalf("a refresh token of a removed account is not refused once its records are put back: %v", err)
		}
	}
	if err := fresh.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	// The list cannot be made again, by its fence or by an approval.
	if err := fresh.EndAll(ctx, id); err == nil {
		t.Fatal("the removed account's list was made again")
	}
}
