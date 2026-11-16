package accounts

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/keys"
	"github.com/permaily-org/permaily/internal/oauth"
	"github.com/permaily-org/permaily/internal/services/connections"
	"github.com/permaily-org/permaily/internal/services/receipts"
	"github.com/permaily-org/permaily/internal/store"
	"github.com/permaily-org/permaily/internal/vendors"
)

// This service with the real connections service (ADR 0030's pull request
// 3) over one store: what a deletion, its fence failing and its
// cancellation do to a connection made before, through an enclave started
// after (finding 4 of the review of pull request 136).

const realClient = "https://claude.ai/oauth/claude-code-client-metadata"

type realVendors struct{}

func (realVendors) Client(_ context.Context, id string) (*vendors.Client, error) {
	if id != realClient {
		return nil, vendors.ErrClientID
	}
	return &vendors.Client{ID: realClient, Domain: "claude.ai", Document: oauth.ClientDocument{
		ClientID: realClient, ClientName: "Claude Code", RedirectURIs: []string{"http://localhost/callback"}, TokenEndpointAuthMethod: "none",
	}}, nil
}

func (realVendors) Keys(context.Context, *vendors.Client, bool) ([]vendors.Key, error) {
	return nil, vendors.ErrUnreachable
}

type realMailboxes struct{}

func (realMailboxes) Catalogue(context.Context, string) ([]string, error) {
	return []string{"mbx_" + strings.Repeat("b", 26)}, nil
}

func (realMailboxes) OpenForConnection(context.Context, string, string) (inbox.Box, error) {
	return inbox.Box{}, contracts.ErrNotFound
}

type realReceipts struct{}

func (realReceipts) Write(context.Context, string, *receipts.Receipt) error { return nil }

// fencing is the connections' records with a write a test can fail.
type fencing struct {
	connections.Records
	mu   sync.Mutex
	fail func(name string) error
}

func (f *fencing) PutRecordAt(ctx context.Context, r *store.Record, at store.Mark) error {
	f.mu.Lock()
	fail := f.fail
	f.mu.Unlock()
	if fail != nil {
		if err := fail(r.Name); err != nil {
			return err
		}
	}
	return f.Records.PutRecordAt(ctx, r, at)
}

func (f *fencing) set(fail func(name string) error) {
	f.mu.Lock()
	f.fail = fail
	f.mu.Unlock()
}

func TestCancellingAfterAFailedFenceNeverBringsAConnectionBack(t *testing.T) {
	ctx := context.Background()
	w := newWorld(t)
	k, err := keys.NewLocal()
	if err != nil {
		t.Fatal(err)
	}
	clock := func() time.Time { return w.now }
	enclave := func() (*connections.Service, *fencing) {
		f := &fencing{Records: &store.Anchored{Records: w.mem, Commits: w.commits, Strict: true, Now: clock}}
		s, err := connections.New(connections.Config{
			OAuth:   oauth.Config{Issuer: "https://api.permaily.test", AppOrigin: "https://app.permaily.test", APIResource: "https://api.permaily.test/v1", MCPResource: "https://mcp.permaily.test/mcp"},
			Records: f, Entries: w.mem, Keys: k, Vendors: realVendors{}, Accounts: w.svc, Mailboxes: realMailboxes{},
			Receipts: realReceipts{}, ModelNotice: w.notice, Now: clock,
		})
		if err != nil {
			t.Fatal(err)
		}
		return s, f
	}
	conns, records := enclave()
	w.svc.SetConnections(conns)
	me, cookie := w.signUp("ana@example.org")
	id := me.Account.ID
	who := w.who(cookie)

	// A connection, made and exchanged.
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	verifier := base64.RawURLEncoding.EncodeToString(b)
	sum := sha256.Sum256([]byte(verifier))
	q := url.Values{"client_id": {realClient}, "redirect_uri": {"http://localhost/callback"}, "response_type": {"code"},
		"code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])}, "code_challenge_method": {"S256"}, "resource": {"https://mcp.permaily.test/mcp"}}
	p := connections.Person{Account: id, Session: "ses_test", ConfirmedUntil: w.now.Add(10 * time.Minute)}
	started, err := conns.Start(ctx, p, q.Encode())
	if err != nil || started.Request == nil {
		t.Fatal(started, err)
	}
	req := started.Request
	ans, err := conns.Decide(ctx, p, req.ID, contracts.AuthorizationDecision{Allow: true, Draft: &req.Draft, Snapshot: &req.Snapshot, ModelNotice: req.ModelNotice})
	if err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(ans.Redirect)
	tok, err := conns.Token(ctx, url.Values{"grant_type": {"authorization_code"}, "code": {u.Query().Get("code")}, "code_verifier": {verifier},
		"redirect_uri": {"http://localhost/callback"}, "client_id": {realClient}, "resource": {"https://mcp.permaily.test/mcp"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := conns.Check(ctx, contracts.ResourceMCP, tok.AccessToken); err != nil {
		t.Fatal(err)
	}

	// The deletion asked, its fence failing at the list's first write.
	records.set(func(name string) error {
		if strings.HasPrefix(name, connections.RecordList) {
			return errors.New("the storage is down")
		}
		return nil
	})
	if _, err := w.svc.StartDeletion(ctx, w.fresh(who)); err != nil {
		t.Fatal(err)
	}
	records.set(nil)

	// Cancelled, through an enclave started since.
	restarted, _ := enclave()
	w.svc.SetConnections(restarted)
	got, err := w.svc.CancelDeletion(ctx, who)
	if err != nil || got.State != contracts.AccountActive {
		t.Fatal(got, err)
	}
	fresh, _ := enclave()
	if _, err := fresh.Check(ctx, contracts.ResourceMCP, tok.AccessToken); !errors.Is(err, connections.ErrInvalidToken) {
		t.Fatalf("a connection made before the deletion works after its cancellation: %v", err)
	}
	// The enclave that made it, within the freshness of what it read.
	w.now = w.now.Add(oauth.ConnectionFresh)
	if _, err := conns.Check(ctx, contracts.ResourceMCP, tok.AccessToken); !errors.Is(err, connections.ErrInvalidToken) {
		t.Fatalf("the enclave that made it still accepts it: %v", err)
	}
}
