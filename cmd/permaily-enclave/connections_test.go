package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/acme"

	"github.com/permaily-org/permaily/internal/oauth"
	"github.com/permaily-org/permaily/internal/router"
	"github.com/permaily-org/permaily/internal/rules"
	"github.com/permaily-org/permaily/internal/services/accounts"
	"github.com/permaily-org/permaily/internal/services/mailboxes"
	"github.com/permaily-org/permaily/internal/services/receipts"
	"github.com/permaily-org/permaily/internal/store"
	"github.com/permaily-org/permaily/pkg/attestation"
)

// countingEntries is the deployment's table, counting the pages of the
// connections' index of accounts that are listed: one a pass of the sweep.
type countingEntries struct {
	*store.Memory
	swept atomic.Int64
}

func (c *countingEntries) ListEntries(ctx context.Context, partition, prefix, after string, limit int) ([]store.Entry, error) {
	if partition == "connections#accounts" {
		c.swept.Add(1)
	}
	return c.Memory.ListEntries(ctx, partition, prefix, after, limit)
}

// connectionsWorld is one enclave's connections composed as the image
// composes them, over a table in memory: accounts, mailboxes and the model
// each with their own Anchored, and the connections with theirs.
type connectionsWorld struct {
	*modelWorld
	table   *countingEntries
	records *store.Anchored
	// chain is the one strict Anchored of the chain of receipts.
	chain *store.Anchored
	parts connectionsParts
}

func newConnectionsWorld(t *testing.T) *connectionsWorld {
	t.Helper()
	w := newModelWorld(t)
	// The chain's records' commits grouped, as the image's (anchorCommits).
	w.commits.Group = receipts.CommitGroup
	table := &countingEntries{Memory: w.mem}
	acc, err := accounts.New(accounts.Config{
		Records: &store.Anchored{Records: w.mem, Commits: w.commits, OnlyNewest: true, Now: w.now}, Entries: w.mem, Keys: w.keys,
		Mailer: w.outbox, AppURL: "https://app.permaily.test", Now: w.now,
	})
	if err != nil {
		t.Fatal(err)
	}
	mb, err := mailboxes.New(mailboxes.Config{
		Records: &store.Anchored{Records: w.mem, Commits: w.commits, OnlyNewest: true, Strict: true, Now: w.now},
		Storage: w.mem, Keys: w.keys, Accounts: acc, Notifier: acc, Domain: "inbox.permaily.test", Now: w.now,
	})
	if err != nil {
		t.Fatal(err)
	}
	acc.SetMailboxes(mb)
	model, _ := w.enclave()
	records := &store.Anchored{Records: w.mem, Commits: w.commits, OnlyNewest: true, Strict: true, Now: w.now}
	chain := &store.Anchored{Records: receipts.ChainTable{Entries: w.mem}, Commits: w.commits, Strict: true, Now: w.now}
	rc, err := composeReceipts(receiptsParts{chain: chain, entries: w.mem, keys: w.keys, accounts: acc, now: w.now})
	if err != nil {
		t.Fatal(err)
	}
	return &connectionsWorld{modelWorld: w, table: table, records: records, chain: chain, parts: connectionsParts{
		oauth: oauthConfig(router.HostsForDomain("permaily.com"), 443), domain: "permaily.com", records: records, entries: table, keys: w.keys,
		vendorHTTP: &http.Client{}, accounts: acc, mailboxes: mb, model: model, receipts: rc, now: w.now,
	}}
}

// V56 (ADR 0030, "What /status and permaily-verify show"): /status's
// connections, golden: the issuer and every endpoint on the deployment's
// hosts, the two resources, the grants and the client authentication the
// server's own metadata says, client documents and no registration, the
// lifetimes, codes refused on reuse, the five seconds that bound every
// byte, assertions decided strictly, vendors over HTTP/2, 16 devices,
// nothing leaving while the model cannot be asked, the verified list, the
// default rule by the canonical identifiers of its formulas, receipts
// sealed and chained by strict links with the record of incidents read and
// empty, a first pass of the audit and nothing broken by any kind (ADR
// 0037, L19), decisions strict with 28,800 a mailbox's day,
// and every reason at zero; never a client, an account or a connection.
func TestV56TheConnectionsOnStatus(t *testing.T) {
	w := newConnectionsWorld(t)
	c, err := composeConnections(w.parts)
	if err != nil {
		t.Fatal(err)
	}
	// Receipts as an enclave reports them once it has read the record of
	// incidents and finished a first pass of the audit (ADR 0037, L19).
	if err := c.receipts.auditOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	st := connectionsStatus(c)()
	got, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	def := rules.Default()
	var ids []string
	for _, f := range def.Formulas {
		ids = append(ids, `"`+f.ID+`"`)
	}
	reasons := slices.Clone(connectionsReasons)
	slices.Sort(reasons)
	var zero []string
	for _, r := range reasons {
		zero = append(zero, fmt.Sprintf("%q:0", r))
	}
	kinds := slices.Clone(attestation.ReceiptsBrokenKinds)
	slices.Sort(kinds)
	var brokenKinds []string
	for _, k := range kinds {
		brokenKinds = append(brokenKinds, fmt.Sprintf("%q:0", k))
	}
	brokenZero := strings.Join(brokenKinds, ",")
	want := `{"issuer":"https://api.permaily.com","authorization_endpoint":"https://app.permaily.com/oauth/authorize",` +
		`"token_endpoint":"https://api.permaily.com/oauth/token","revocation_endpoint":"https://api.permaily.com/oauth/revoke",` +
		`"pushed_endpoint":"https://api.permaily.com/oauth/par","resources":["https://api.permaily.com/v1","https://mcp.permaily.com/mcp"],` +
		`"grants":["authorization_code","refresh_token"],"client_auth":["none","private_key_jwt"],"client_documents":true,"dynamic_registration":false,` +
		`"access_token_seconds":3600,"refresh_token_seconds":2592000,"refresh_retry_seconds":30,"code_seconds":60,"code_reuse":"denied",` +
		`"revocation_seconds":5,"connection_default_days":90,"connection_max_days":365,"revocation_covers":"every_byte","assertion_replay":"strict",` +
		`"vendor_transport":"h2","devices_per_connection":16,"model_unavailable":"nothing_leaves",` +
		`"verified":[{"domain":"claude.ai","owner":"Anthropic"},{"domain":"chatgpt.com","owner":"OpenAI"}],` +
		`"rules":[{"id":"default-sign-in-and-security","version":1,"formulas":[` + strings.Join(ids, ",") + `],"on":true}],` +
		`"receipts":{"sealed":true,"chained":true,"chain":"strict","incidents":{"recorded":0,"read":"2026-11-16T12:00:00Z"},` +
		`"broken":{` + brokenZero + `},"audit":{"began":"2026-11-16T12:00:00Z","finished":"2026-11-16T12:00:00Z","catalogue":0,` +
		`"chains":0,"removed":0,"broken":{` + brokenZero + `},"recovered":0,"unlinked":0,"expired":0},"decisions":"strict","decisions_per_day":28800},` +
		`"since":"` + st.Since.Format(time.RFC3339Nano) + `","refused":{` + strings.Join(zero, ",") + `}}`
	if string(got) != want {
		t.Fatalf("/status's connections:\n got %s\nwant %s", got, want)
	}
	// permaily-verify's tests check this very section passes (V56): it is
	// their testdata, which must say what the image says.
	golden, err := os.ReadFile("../permaily-verify/testdata/connections-status.json")
	var compact bytes.Buffer
	if err == nil {
		err = json.Compact(&compact, golden)
	}
	if err != nil || compact.String() != string(got) {
		t.Fatalf("permaily-verify's testdata/connections-status.json is not what the image says (%v): write it as\n%s", err, got)
	}
	if len(def.Formulas) != 3 || st.Since.IsZero() {
		t.Fatalf("the default rule's formulas: %d", len(def.Formulas))
	}
	// The server's own metadata and the status agree: what /status says is
	// what the issuer publishes.
	md := oauth.Metadata(w.parts.oauth)
	if md.TokenEndpoint != st.TokenEndpoint || !slices.Equal(md.GrantTypesSupported, st.Grants) || !slices.Equal(md.TokenEndpointAuthMethodsSupported, st.ClientAuth) {
		t.Fatalf("the metadata %+v and /status %+v", md, st)
	}
	// A reason the service counts appears with its count, beside the rest.
	c.svc.Refused() // the service's own map is a copy
	if st := connectionsStatus(c)(); st.Refused["decisions_full"] != 0 || len(st.Refused) != len(connectionsReasons) {
		t.Fatalf("%v", st.Refused)
	}
}

// V57: the composition refuses what would break what the image promises:
// the connections' records through an Anchored that is not strict, or
// keeps every version; and it needs the vendor egress, accounts, mailboxes
// and the model, never composing connections without one.
func TestV57TheCompositionRefuses(t *testing.T) {
	w := newConnectionsWorld(t)
	for _, records := range []*store.Anchored{
		nil,
		{Records: w.mem, Commits: w.commits, OnlyNewest: true},
		{Records: w.mem, Commits: w.commits, Strict: true},
	} {
		p := w.parts
		p.records = records
		if _, err := composeConnections(p); !errors.Is(err, errConnectionsRecords) {
			t.Errorf("records %+v: %v", records, err)
		}
	}
	for i, drop := range []func(*connectionsParts){
		func(p *connectionsParts) { p.vendorHTTP = nil },
		func(p *connectionsParts) { p.accounts = nil },
		func(p *connectionsParts) { p.mailboxes = nil },
		func(p *connectionsParts) { p.model = nil },
		func(p *connectionsParts) { p.receipts = nil },
	} {
		p := w.parts
		drop(&p)
		if _, err := composeConnections(p); err == nil {
			t.Errorf("part %d missing: composed", i)
		}
	}
}

// V57: the hourly pass runs once when the enclave starts and then on its
// ticker, an hour, and stops with the enclave.
func TestV57TheHourlyPassRunsOnItsTicker(t *testing.T) {
	w := newConnectionsWorld(t)
	c, err := composeConnections(w.parts)
	if err != nil {
		t.Fatal(err)
	}
	if sweepEvery != time.Hour {
		t.Fatalf("the pass runs every %s", sweepEvery)
	}
	ctx, cancel := context.WithCancel(context.Background())
	tick := make(chan time.Time)
	runConnections(ctx, c.svc, slog.New(slog.DiscardHandler), tick, refusalClock{})
	waitFor := func(n int64) {
		t.Helper()
		for i := 0; w.table.swept.Load() < n; i++ {
			if i == 500 {
				t.Fatalf("%d passes, want %d", w.table.swept.Load(), n)
			}
			time.Sleep(2 * time.Millisecond)
		}
	}
	waitFor(1)
	tick <- time.Now()
	tick <- time.Now()
	waitFor(3)
	cancel()
	time.Sleep(20 * time.Millisecond)
	select {
	case tick <- time.Now():
		t.Fatal("the pass ran after the enclave stopped")
	default:
	}
	if n := w.table.swept.Load(); n != 3 {
		t.Fatalf("%d passes", n)
	}
}

// The api and mcp hosts negotiate http/1.1 with a client that offers h2
// (ADR 0030, "Changed after the second review of pull request 6"), the
// app's host h2, and ACME's challenges stay answerable on every host.
func TestTheAPIAndMCPHostsOfferOnlyHTTP1(t *testing.T) {
	hosts := router.HostsForDomain("permaily.test")
	cert := selfSigned(t, hosts.All()...)
	base := &tls.Config{MinVersion: tls.VersionTLS12, NextProtos: []string{"h2", "http/1.1", acme.ALPNProto}, Certificates: []tls.Certificate{cert}}
	srv := serverTLS(base, hosts)
	if !slices.Equal(base.NextProtos, []string{"h2", "http/1.1", acme.ALPNProto}) || base.GetConfigForClient != nil {
		t.Fatal("the base configuration was changed")
	}
	for _, c := range []struct {
		name, want string
	}{
		{hosts.API, "http/1.1"}, {hosts.MCP, "http/1.1"}, {strings.ToUpper(hosts.API) + ".", "http/1.1"},
		{hosts.App, "h2"}, {hosts.Inbox, "h2"}, {"", "h2"},
	} {
		if got := negotiate(t, srv, c.name, "h2", "http/1.1"); got != c.want {
			t.Errorf("%q: %q, want %q", c.name, got, c.want)
		}
	}
	for _, name := range []string{hosts.API, hosts.MCP, hosts.App} {
		cfg, err := srv.GetConfigForClient(&tls.ClientHelloInfo{ServerName: name})
		if err != nil {
			t.Fatal(err)
		}
		if cfg == nil {
			cfg = srv
		}
		if !slices.Contains(cfg.NextProtos, acme.ALPNProto) {
			t.Errorf("%s: ACME's challenges are not offered", name)
		}
	}
}

func negotiate(t *testing.T, srv *tls.Config, name string, protos ...string) string {
	t.Helper()
	ln, err := tls.Listen("tcp", "127.0.0.1:0", srv)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if c, err := ln.Accept(); err == nil {
			_ = c.(*tls.Conn).Handshake()
			_ = c.Close()
		}
	}()
	c, err := tls.Dial("tcp", ln.Addr().String(), &tls.Config{ServerName: name, NextProtos: protos, InsecureSkipVerify: true}) //nolint:gosec // a test's own certificate
	if err != nil {
		t.Fatal(err)
	}
	proto := c.ConnectionState().NegotiatedProtocol
	_ = c.Close()
	wg.Wait()
	return proto
}

func selfSigned(t *testing.T, names ...string) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: names[0]}, DNSNames: names,
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// The authorization server's addresses are the image's hosts, with the
// port clients name when it is not 443.
func TestTheAuthorizationServersAddresses(t *testing.T) {
	hosts := router.HostsForDomain("permaily.com")
	if c := oauthConfig(hosts, 443); c != (oauth.Config{Issuer: "https://api.permaily.com", AppOrigin: "https://app.permaily.com", APIResource: "https://api.permaily.com/v1", MCPResource: "https://mcp.permaily.com/mcp"}) {
		t.Fatalf("%+v", c)
	}
	if c := oauthConfig(hosts, 8443); c.Issuer != "https://api.permaily.com:8443" || c.MCPResource != "https://mcp.permaily.com:8443/mcp" {
		t.Fatalf("%+v", c)
	}
	if p := (config{port: 4443}).oauthPort(); p != 4443 {
		t.Fatal(p)
	}
	if p := (config{port: 4443, clientPort: 8443}).oauthPort(); p != 8443 {
		t.Fatal(p)
	}
}
