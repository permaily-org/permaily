package tlsacme

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/asn1"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/acme"

	"github.com/permaily-org/permaily/internal/envelope"
	"github.com/permaily-org/permaily/internal/keys"
	"github.com/permaily-org/permaily/internal/store"
)

// pair is two enclaves of one deployment: the same table, the same store of
// commits and the same root secret (one keys.Service), each with its own
// memory, and one CA.
type pair struct {
	t       *testing.T
	mem     *store.Memory
	commits *store.MemoryCommits
	keys    keys.Service
	now     time.Time
	ca      *sharedCA
}

func newPair(t *testing.T) *pair {
	t.Helper()
	p := &pair{t: t, mem: store.NewMemory(), commits: store.NewMemoryCommits(), keys: localKeys(t), now: t0, ca: &sharedCA{life: 90 * 24 * time.Hour}}
	p.mem.Now = func() time.Time { return p.now }
	return p
}

// sealed is what one enclave stores its identity through.
func (p *pair) sealed() *Sealed {
	anchored := &store.Anchored{Records: p.mem, Commits: p.commits, Now: func() time.Time { return p.now }}
	return &Sealed{Records: anchored, Keys: p.keys, Challenges: p.mem, Name: "tls-ca.test", Now: func() time.Time { return p.now }}
}

// enclave starts an enclave: a Manager over the shared storage whose orders
// go to the shared CA. clock, when set, is its own clock.
func (p *pair) enclave(clock *time.Time) *Manager {
	p.t.Helper()
	m, err := New(hosts, "https://ca.test/directory", nil, p.sealed(), quiet)
	if err != nil {
		p.t.Fatal(err)
	}
	if clock == nil {
		clock = &p.now
	}
	m.now = func() time.Time { return *clock }
	m.retry = time.Millisecond
	m.issue = p.ca.issuer(p.t, m)
	if !m.load(context.Background()) {
		p.t.Fatal("load")
	}
	return m
}

// stored is the identity as a new reader finds it.
func (p *pair) stored() *Identity {
	p.t.Helper()
	id, _, err := p.sealed().Read(context.Background())
	if err != nil {
		p.t.Fatal(err)
	}
	return id
}

// sharedCA stands for the CA both enclaves ask: it counts the orders.
type sharedCA struct {
	mu     sync.Mutex
	orders int
	life   time.Duration
	fail   error
	// during runs while an order is open, once.
	during func()
}

func (ca *sharedCA) count() int {
	ca.mu.Lock()
	defer ca.mu.Unlock()
	return ca.orders
}

func (ca *sharedCA) issuer(t *testing.T, m *Manager) func(context.Context) error {
	return func(context.Context) error {
		ca.mu.Lock()
		ca.orders++
		fail, during := ca.fail, ca.during
		ca.during = nil
		ca.mu.Unlock()
		if during != nil {
			during()
		}
		if fail != nil {
			return fail
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		// Like Let's Encrypt, it dates a certificate an hour back.
		m.ident.Chain = [][]byte{certFor(t, m.ident.Key, hosts, m.now().Add(-time.Hour), ca.life)}
		m.ident.AccountURI = "https://ca.test/acct/7"
		return nil
	}
}

func leafOf(t *testing.T, m *Manager) *x509.Certificate {
	t.Helper()
	c, err := m.GetCertificate(&tls.ClientHelloInfo{ServerName: hosts[0]})
	if err != nil {
		t.Fatal(err)
	}
	return c.Leaf
}

func TestTwoEnclavesStartingTogetherOrderOneCertificate(t *testing.T) {
	ctx := context.Background()
	// Whichever way the two interleave, one orders.
	for i := range 25 {
		p := newPair(t)
		var a, b *Manager
		var wg sync.WaitGroup
		wg.Go(func() { a = p.enclave(nil) })
		wg.Go(func() { b = p.enclave(nil) })
		wg.Wait()
		if !bytes.Equal(a.UserData(), b.UserData()) {
			t.Fatal("two enclaves started together hold different keys")
		}
		wg.Go(func() { _, _ = a.ensure(ctx) })
		wg.Go(func() { _, _ = b.ensure(ctx) })
		wg.Wait()
		if n := p.ca.count(); n != 1 {
			t.Fatalf("run %d: %d orders, want 1", i, n)
		}
		// The one that waited serves what the other stored on its next look.
		p.now = p.now.Add(waitPoll)
		for _, m := range []*Manager{a, b} {
			if _, err := m.ensure(ctx); err != nil {
				t.Fatal(err)
			}
		}
		if p.ca.count() != 1 || !a.Ready() || !b.Ready() || !leafOf(t, a).Equal(leafOf(t, b)) {
			t.Fatalf("run %d: orders %d, ready %v %v", i, p.ca.count(), a.Ready(), b.Ready())
		}
		if a.Status().ACMEAccount != b.Status().ACMEAccount || a.Status().ACMEAccount == "" {
			t.Fatal("the two enclaves report different ACME accounts")
		}
		if id := p.stored(); id.Order != nil || len(id.Chain) == 0 {
			t.Fatalf("run %d: stored order %+v, chain %d", i, id.Order, len(id.Chain))
		}
	}
}

func TestAnEnclaveStartingWhileTheOtherOrdersWaitsAndAdopts(t *testing.T) {
	ctx := context.Background()
	p := newPair(t)
	a := p.enclave(nil)
	var b *Manager
	var waited time.Time
	p.ca.during = func() {
		// B starts while A's order is open.
		b = p.enclave(nil)
		next, err := b.ensure(ctx)
		if err != nil || b.Ready() {
			t.Errorf("B while A orders: %v, ready %v", err, b.Ready())
		}
		waited = next
	}
	if _, err := a.ensure(ctx); err != nil {
		t.Fatal(err)
	}
	if p.ca.count() != 1 {
		t.Fatalf("%d orders: B ordered while A was ordering", p.ca.count())
	}
	if d := waited.Sub(p.now); d <= 0 || d > waitPoll {
		t.Fatalf("B looks again in %s, want within %s", d, waitPoll)
	}
	if _, err := b.ensure(ctx); err != nil {
		t.Fatal(err)
	}
	if p.ca.count() != 1 || !b.Ready() || !leafOf(t, a).Equal(leafOf(t, b)) {
		t.Fatalf("B did not adopt A's certificate: orders %d", p.ca.count())
	}

	// A third enclave started later takes the stored certificate at once.
	c := p.enclave(nil)
	if _, err := c.ensure(ctx); err != nil || !c.Ready() || p.ca.count() != 1 {
		t.Fatalf("a later enclave: %v, orders %d", err, p.ca.count())
	}
}

func TestOneRenewsAndTheOtherServesTheNewCertificateWithoutARestart(t *testing.T) {
	ctx := context.Background()
	p := newPair(t)
	a := p.enclave(nil)
	// B's clock is two minutes behind A's: its own certificate is not due yet
	// when A renews.
	behind := p.now
	b := p.enclave(&behind)
	if _, err := a.ensure(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := b.ensure(ctx); err != nil {
		t.Fatal(err)
	}
	first := leafOf(t, b)
	renewAt := first.NotBefore.Add(first.NotAfter.Sub(first.NotBefore) * 2 / 3)

	p.now, behind = renewAt.Add(time.Minute), renewAt.Add(-time.Minute)
	if _, err := a.ensure(ctx); err != nil {
		t.Fatal(err)
	}
	if p.ca.count() != 2 || leafOf(t, a).Equal(first) {
		t.Fatalf("A did not renew: orders %d", p.ca.count())
	}
	next, err := b.ensure(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !leafOf(t, b).Equal(leafOf(t, a)) {
		t.Fatal("B does not serve the certificate A stored")
	}
	if p.ca.count() != 2 {
		t.Fatalf("B ordered too: %d orders", p.ca.count())
	}
	if d := next.Sub(behind); d > refreshEvery {
		t.Fatalf("B looks again in %s, want within %s", d, refreshEvery)
	}
	// Once B's own clock says the old one was due, it still orders nothing.
	behind = p.now.Add(time.Hour)
	if _, err := b.ensure(ctx); err != nil || p.ca.count() != 2 {
		t.Fatalf("B later: %v, orders %d", err, p.ca.count())
	}
}

func TestARunningEnclaveLearnsOfTheCertificateTheOtherGot(t *testing.T) {
	ctx := context.Background()
	p := newPair(t)
	a, b := p.enclave(nil), p.enclave(nil)
	if _, err := a.ensure(ctx); err != nil {
		t.Fatal(err)
	}
	// B, which was waiting, asks nothing and serves A's certificate.
	if next, err := b.ensure(ctx); err != nil || !b.Ready() || p.ca.count() != 1 || next.Sub(p.now) > refreshEvery {
		t.Fatalf("B: %v, ready %v, orders %d, next in %s", err, b.Ready(), p.ca.count(), next.Sub(p.now))
	}
}

func TestAnOrderLeftByAnEnclaveThatStoppedIsTakenOverWhenItsClaimEnds(t *testing.T) {
	ctx := context.Background()
	p := newPair(t)
	a, b := p.enclave(nil), p.enclave(nil)
	p.ca.fail = errors.New("CA unreachable")
	if _, err := a.ensure(ctx); err == nil {
		t.Fatal("a failed order is not reported")
	}
	claim := p.stored().Order
	if claim == nil || claim.By != a.holder || !claim.Until.Equal(p.now.Add(orderLease+firstBackoff)) {
		t.Fatalf("claim %+v: it must cover one order and the wait before the next", claim)
	}
	// A stops. Until its claim ends B asks nothing, whatever it is told.
	p.ca.fail = nil
	for _, at := range []time.Duration{0, orderLease, orderLease + firstBackoff - time.Second} {
		p.now = t0.Add(at)
		next, err := b.ensure(ctx)
		if err != nil || b.Ready() || p.ca.count() != 1 {
			t.Fatalf("at %s: %v, ready %v, orders %d", at, err, b.Ready(), p.ca.count())
		}
		if next.After(claim.Until) {
			t.Fatalf("at %s B looks again at %s, after the claim ends (%s)", at, next, claim.Until)
		}
	}
	p.now = claim.Until
	if _, err := b.ensure(ctx); err != nil || !b.Ready() || p.ca.count() != 2 {
		t.Fatalf("after the claim: %v, ready %v, orders %d", err, b.Ready(), p.ca.count())
	}
	if p.stored().Order != nil {
		t.Fatal("the claim stays after the certificate was stored")
	}
}

// Two enclaves whose orders fail order no more often than one: the one that
// holds the claim keeps it over the wait before its next try.
func TestTwoEnclavesOrderNoMoreOftenThanOneWhenTheCAFails(t *testing.T) {
	ctx := context.Background()
	p := newPair(t)
	a, b := p.enclave(nil), p.enclave(nil)
	p.ca.fail = errors.New("CA unreachable")
	// Each wakes every minute; A follows its backoff after a failure.
	nextA := p.now
	for range 6 * 60 {
		if !p.now.Before(nextA) {
			if _, err := a.ensure(ctx); err != nil {
				nextA = p.now.Add(a.backoff)
				a.backoff = min(a.backoff*2, maxBackoff)
			}
		}
		_, _ = b.ensure(ctx)
		p.now = p.now.Add(time.Minute)
	}
	// Alone, A tries at 0, 1, 3, 7, 15, 31, 63, 123, 183, 243, 303 minutes.
	if n := p.ca.count(); n != 11 {
		t.Fatalf("%d orders in six hours, want the 11 one enclave makes", n)
	}
}

func TestAStoredIdentityWithAnotherKeyIsNotAdopted(t *testing.T) {
	ctx := context.Background()
	p := newPair(t)
	a := p.enclave(nil)
	if _, err := a.ensure(ctx); err != nil {
		t.Fatal(err)
	}
	served := leafOf(t, a)
	// The identity is lost (its stored versions deleted) and made again by an
	// enclave that starts: another key.
	names, _ := p.mem.ListRecords(ctx, "tls-ca.test@", 100)
	for _, n := range names {
		_ = p.mem.DeleteRecord(ctx, n)
	}
	b := p.enclave(nil)
	if bytes.Equal(a.UserData(), b.UserData()) {
		t.Fatal("the identity was not made again")
	}
	if _, err := a.ensure(ctx); !errors.Is(err, errOtherKey) {
		t.Fatalf("A with another key stored: %v", err)
	}
	if !leafOf(t, a).Equal(served) || p.ca.count() != 1 {
		t.Fatalf("A stopped serving its certificate, or ordered: orders %d", p.ca.count())
	}
}

// caCheck connects to an enclave as the CA does for TLS-ALPN-01 (RFC 8737):
// ALPN acme-tls/1 and the name in SNI, and accepts only a certificate for the
// name whose critical acmeIdentifier extension holds the SHA-256 of the key
// authorization.
func caCheck(m *Manager, host, keyAuth string) error {
	client, server := net.Pipe()
	defer client.Close()
	go func() {
		defer server.Close()
		_ = tls.Server(server, m.TLSConfig()).Handshake()
	}()
	conn := tls.Client(client, &tls.Config{ServerName: host, NextProtos: []string{acme.ALPNProto}, InsecureSkipVerify: true, MinVersion: tls.VersionTLS12}) //nolint:gosec // the challenge certificate is self-signed, as RFC 8737 says
	if err := conn.Handshake(); err != nil {
		return err
	}
	st := conn.ConnectionState()
	if st.NegotiatedProtocol != acme.ALPNProto || len(st.PeerCertificates) != 1 {
		return fmt.Errorf("protocol %q, %d certificates", st.NegotiatedProtocol, len(st.PeerCertificates))
	}
	leaf := st.PeerCertificates[0]
	if len(leaf.DNSNames) != 1 || leaf.DNSNames[0] != host {
		return fmt.Errorf("certificate for %v", leaf.DNSNames)
	}
	want := sha256.Sum256([]byte(keyAuth))
	for _, e := range leaf.Extensions {
		if e.Id.Equal(asn1.ObjectIdentifier{1, 3, 6, 1, 5, 5, 7, 1, 31}) {
			var got []byte
			if _, err := asn1.Unmarshal(e.Value, &got); err != nil || !e.Critical || !bytes.Equal(got, want[:]) {
				return errors.New("the acmeIdentifier does not hold the key authorization")
			}
			return nil
		}
	}
	return errors.New("no acmeIdentifier")
}

// challengeFor makes the challenge certificate an order of m opens for host,
// and the key authorization the CA expects.
func challengeFor(t *testing.T, m *Manager, token, host string) (*tls.Certificate, string) {
	t.Helper()
	cert, err := m.client.TLSALPN01ChallengeCert(token, host)
	if err != nil {
		t.Fatal(err)
	}
	thumb, err := acme.JWKThumbprint(m.ident.AccountKey.Public())
	if err != nil {
		t.Fatal(err)
	}
	return &cert, token + "." + thumb
}

func TestTheCAsCheckReachesTheEnclaveThatDidNotOrder(t *testing.T) {
	ctx := context.Background()
	p := newPair(t)
	a, b := p.enclave(nil), p.enclave(nil)
	cert, keyAuth := challengeFor(t, a, "token-1", hosts[1])

	if err := caCheck(b, hosts[1], keyAuth); err == nil {
		t.Fatal("B answered a challenge nobody opened")
	}
	if err := a.publish(ctx, hosts[1], cert); err != nil {
		t.Fatal(err)
	}
	p.now = p.now.Add(challengeRecheck) // B read before; it reads again a second later
	for _, m := range []*Manager{a, b} {
		if err := caCheck(m, hosts[1], keyAuth); err != nil {
			t.Fatalf("the CA's check failed: %v", err)
		}
	}
	// Only for the name the order is for, and only for names of ours.
	if err := caCheck(b, hosts[0], keyAuth); err == nil {
		t.Fatal("B answered for another name")
	}
	if err := caCheck(b, "other.test", keyAuth); err == nil {
		t.Fatal("B answered for a name that is not its own")
	}

	// Once the order ends, neither answers it.
	a.clearChallenges()
	p.now = p.now.Add(challengeRecheck)
	for _, m := range []*Manager{a, b} {
		if err := caCheck(m, hosts[1], keyAuth); err == nil {
			t.Fatal("a challenge is answered after its order ended")
		}
	}
	if es, _ := p.mem.ListEntries(ctx, "acme-challenge#tls-ca.test", "", "", 10); len(es) != 0 {
		t.Fatalf("%d challenges left in the storage", len(es))
	}
}

// What whoever reads the table finds of a challenge, and what they can do
// with the entry: neither the certificate nor its key, and nothing they write
// makes an enclave answer with what it should not.
func TestAChallengeInTheTableIsSealedAndBound(t *testing.T) {
	ctx := context.Background()
	p := newPair(t)
	a, b := p.enclave(nil), p.enclave(nil)
	cert, keyAuth := challengeFor(t, a, "token-2", hosts[0])
	if err := a.publish(ctx, hosts[0], cert); err != nil {
		t.Fatal(err)
	}
	e, err := p.mem.GetEntry(ctx, "acme-challenge#tls-ca.test", hosts[0])
	if err != nil {
		t.Fatal(err)
	}
	keyDER, _ := x509.MarshalPKCS8PrivateKey(cert.PrivateKey)
	var ce challengeEntry
	if err := json.Unmarshal(e.Value, &ce); err != nil {
		t.Fatal(err)
	}
	for _, v := range [][]byte{e.Value, ce.Sealed} {
		if bytes.Contains(v, cert.Certificate[0]) || bytes.Contains(v, keyDER) || bytes.Contains(v, []byte(keyAuth)) {
			t.Fatal("the stored challenge holds its certificate, its key or the key authorization in the clear")
		}
	}
	if !e.ExpiresAt.Equal(p.now.Add(orderLease)) {
		t.Fatalf("the entry ends at %s, want with the order", e.ExpiresAt)
	}

	// Moved to another name, it is refused.
	moved := *e
	moved.Key = hosts[1]
	p.mem.SetEntry(moved)
	if _, err := b.persist.GetChallenge(ctx, hosts[1]); err == nil {
		t.Fatal("a challenge moved to another name was opened")
	}
	// Kept past its order by hand, it is refused: the end is sealed in it.
	kept := *e
	kept.ExpiresAt = p.now.Add(24 * time.Hour)
	p.mem.SetEntry(kept)
	p.now = p.now.Add(orderLease)
	if _, err := b.persist.GetChallenge(ctx, hosts[0]); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("a challenge kept past its order: %v", err)
	}
	// Garbage is refused.
	for _, v := range []string{"nope", `{"sealed":"AAAA"}`} {
		p.mem.SetEntry(store.Entry{Partition: e.Partition, Key: hosts[0], Value: []byte(v)})
		if _, err := b.persist.GetChallenge(ctx, hosts[0]); err == nil {
			t.Fatalf("opened %q", v)
		}
	}
}

func TestChallengesNeedTheIdentityAndAStore(t *testing.T) {
	ctx := context.Background()
	s := &Sealed{Records: store.NewMemory(), Keys: localKeys(t), Name: "x"}
	if err := s.PutChallenge(ctx, hosts[0], &tls.Certificate{}, t0); err == nil {
		t.Fatal("a challenge stored before the identity was loaded")
	}
	if _, err := s.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetChallenge(ctx, hosts[0]); err == nil {
		t.Fatal("a challenge read without a store for them")
	}
	if err := s.DeleteChallenge(ctx, hosts[0]); err != nil {
		t.Fatal(err)
	}
}

// countOrders counts the new orders an ACME client sends.
type countOrders struct {
	next     http.RoundTripper
	newOrder string
	mu       sync.Mutex
	n        int
}

func (c *countOrders) RoundTrip(r *http.Request) (*http.Response, error) {
	if r.Method == http.MethodPost && r.URL.String() == c.newOrder {
		c.mu.Lock()
		c.n++
		c.mu.Unlock()
	}
	return c.next.RoundTrip(r)
}

// acmePair is two enclaves over one storage against a real ACME server.
func acmePair(t *testing.T, directory string) (*countOrders, func() *Manager) {
	t.Helper()
	base := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}} //nolint:gosec // Pebble's own test CA
	dir, err := (&acme.Client{DirectoryURL: directory, HTTPClient: &http.Client{Transport: base}}).Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	counter := &countOrders{next: base, newOrder: dir.OrderURL}
	client := &http.Client{Transport: counter, Timeout: 30 * time.Second}
	mem, commits, k := store.NewMemory(), store.NewMemoryCommits(), localKeys(t)
	start := func() *Manager {
		s := &Sealed{Records: &store.Anchored{Records: mem, Commits: commits}, Keys: k, Challenges: mem, Name: "tls-pebble-pair"}
		m, err := New(hosts, directory, client, s, quiet)
		if err != nil {
			t.Fatal(err)
		}
		if !m.load(context.Background()) {
			t.Fatal("load")
		}
		return m
	}
	return counter, start
}

// TestTwoEnclavesAgainstACME starts two enclaves together against Pebble:
// one order, one account, one certificate.
func TestTwoEnclavesAgainstACME(t *testing.T) {
	directory := os.Getenv("PERMAILY_TEST_ACME")
	if directory == "" {
		t.Skip("PERMAILY_TEST_ACME not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	orders, start := acmePair(t, directory)
	a, b := start(), start()
	var wg sync.WaitGroup
	wg.Go(func() { _, _ = a.ensure(ctx) })
	wg.Go(func() { _, _ = b.ensure(ctx) })
	wg.Wait()
	for _, m := range []*Manager{a, b} {
		if _, err := m.ensure(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if orders.n != 1 || !a.Ready() || !b.Ready() || !leafOf(t, a).Equal(leafOf(t, b)) {
		t.Fatalf("orders %d, ready %v %v", orders.n, a.Ready(), b.Ready())
	}
	if a.Status().ACMEAccount == "" || a.Status().ACMEAccount != b.Status().ACMEAccount {
		t.Fatal("not one ACME account")
	}
}

// TestTheCAsCheckReachesTheOtherEnclaveAgainstACME runs the order of one
// enclave against a Pebble that really validates (no PEBBLE_VA_ALWAYS_VALID),
// with every one of the CA's connections sent to the other enclave, the way a
// load balancer in front of two can. Pebble must resolve the hosts to this
// machine (pebble-challtestsrv -defaultIPv4 127.0.0.1) and connect to
// PERMAILY_TEST_ACME_TLSALPN (its tlsPort, 127.0.0.1:5001 by default).
func TestTheCAsCheckReachesTheOtherEnclaveAgainstACME(t *testing.T) {
	directory, addr := os.Getenv("PERMAILY_TEST_ACME_VALIDATING"), os.Getenv("PERMAILY_TEST_ACME_TLSALPN")
	if directory == "" {
		t.Skip("PERMAILY_TEST_ACME_VALIDATING not set")
	}
	if addr == "" {
		addr = "127.0.0.1:5001"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	orders, start := acmePair(t, directory)
	a, b := start(), start()
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	var reached sync.WaitGroup
	reached.Add(1)
	var once sync.Once
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				if tls.Server(conn, b.TLSConfig()).HandshakeContext(ctx) == nil {
					once.Do(reached.Done)
				}
			}()
		}
	}()
	if _, err := a.ensure(ctx); err != nil {
		t.Fatalf("the order of A, validated through B: %v", err)
	}
	reached.Wait()
	if _, err := b.ensure(ctx); err != nil || !b.Ready() || orders.n != 1 || !leafOf(t, a).Equal(leafOf(t, b)) {
		t.Fatalf("B: %v, orders %d", err, orders.n)
	}
}

// An order that takes longer than its claim: the other enclave orders too,
// and both certificates are for the one key, so either may be served.
func TestAnOrderThatOutlivesItsClaim(t *testing.T) {
	ctx := context.Background()
	p := newPair(t)
	a, b := p.enclave(nil), p.enclave(nil)
	p.ca.during = func() {
		p.now = p.now.Add(orderLease + firstBackoff)
		if _, err := b.ensure(ctx); err != nil || !b.Ready() {
			t.Errorf("B after A's claim ended: %v", err)
		}
		p.now = p.now.Add(time.Hour) // A's certificate lasts longer than B's
	}
	if _, err := a.ensure(ctx); err != nil {
		t.Fatal(err)
	}
	if p.ca.count() != 2 {
		t.Fatalf("%d orders", p.ca.count())
	}
	stored := p.stored()
	if stored.Order != nil || !bytes.Equal(stored.Chain[0], leafOf(t, a).Raw) {
		t.Fatal("A's later certificate is not the one stored")
	}
	if _, err := b.ensure(ctx); err != nil || !leafOf(t, b).Equal(leafOf(t, a)) {
		t.Fatalf("B does not take the later certificate: %v", err)
	}

	// The other way round: what B stored lasts longer, and A takes it.
	p.now = p.now.Add(61 * 24 * time.Hour)
	p.ca.during = func() {
		p.now = p.now.Add(orderLease + firstBackoff)
		p.ca.during = nil
		if _, err := b.ensure(ctx); err != nil {
			t.Error(err)
		}
		p.now = p.now.Add(-time.Hour)
	}
	if _, err := a.ensure(ctx); err != nil {
		t.Fatal(err)
	}
	if !leafOf(t, a).Equal(leafOf(t, b)) || !bytes.Equal(p.stored().Chain[0], leafOf(t, b).Raw) {
		t.Fatal("A did not take B's later certificate")
	}
}

// broken is a Persistence whose storage fails as told.
type broken struct {
	Persistence
	read, write, put, get, del error
}

func (f *broken) Read(ctx context.Context) (*Identity, Version, error) {
	if f.read != nil {
		return nil, Version{}, f.read
	}
	return f.Persistence.Read(ctx)
}

func (f *broken) Write(ctx context.Context, id *Identity, v Version) error {
	if f.write != nil {
		return f.write
	}
	return f.Persistence.Write(ctx, id, v)
}

func (f *broken) PutChallenge(ctx context.Context, host string, c *tls.Certificate, until time.Time) error {
	if f.put != nil {
		return f.put
	}
	return f.Persistence.PutChallenge(ctx, host, c, until)
}

func (f *broken) GetChallenge(ctx context.Context, host string) (*tls.Certificate, error) {
	if f.get != nil {
		return nil, f.get
	}
	return f.Persistence.GetChallenge(ctx, host)
}

func (f *broken) DeleteChallenge(ctx context.Context, host string) error {
	if f.del != nil {
		return f.del
	}
	return f.Persistence.DeleteChallenge(ctx, host)
}

func TestAStorageThatFailsOrdersNothingAndKeepsServing(t *testing.T) {
	ctx := context.Background()
	down := errors.New("dynamodb down")
	p := newPair(t)
	a := p.enclave(nil)
	f := &broken{Persistence: a.persist}
	a.persist = f

	// Without a claim there is no order.
	f.write = down
	if _, err := a.ensure(ctx); !errors.Is(err, down) || p.ca.count() != 0 {
		t.Fatalf("claim refused: %v, orders %d", err, p.ca.count())
	}
	f.write = nil
	if _, err := a.ensure(ctx); err != nil || !a.Ready() {
		t.Fatal(err)
	}
	served := leafOf(t, a)
	// While the storage cannot be read, the certificate in memory is served.
	f.read = down
	if _, err := a.ensure(ctx); !errors.Is(err, down) || !leafOf(t, a).Equal(served) {
		t.Fatalf("read refused: %v", err)
	}
	f.read = nil

	// The certificate got but not stored is served all the same.
	p.now = p.now.Add(61 * 24 * time.Hour)
	f.write = nil
	p.ca.during = func() { f.write = down }
	if _, err := a.ensure(ctx); err != nil || leafOf(t, a).Equal(served) {
		t.Fatalf("not stored: %v", err)
	}
	// And when the storage cannot be read after a write was refused.
	p.now = p.now.Add(61 * 24 * time.Hour)
	f.write = nil
	p.ca.during = func() {
		f.write, f.read = ErrChanged, down
	}
	before := leafOf(t, a)
	if _, err := a.ensure(ctx); err != nil || leafOf(t, a).Equal(before) {
		t.Fatalf("not read back: %v", err)
	}
	f.write, f.read = nil, nil

	// An identity that keeps changing under the claim gives up.
	f.write = ErrChanged
	p.now = p.now.Add(61 * 24 * time.Hour)
	if _, err := a.ensure(ctx); err == nil {
		t.Fatal("no error when the claim never holds")
	}
}

func TestChallengeStorageFailures(t *testing.T) {
	ctx := context.Background()
	down := errors.New("dynamodb down")
	p := newPair(t)
	a, b := p.enclave(nil), p.enclave(nil)
	cert, keyAuth := challengeFor(t, a, "token-3", hosts[0])

	fa := &broken{Persistence: a.persist, put: down}
	a.persist = fa
	if err := a.publish(ctx, hosts[0], cert); !errors.Is(err, down) {
		t.Fatalf("a challenge that could not be stored: %v", err)
	}
	fa.put = nil
	// Published twice (an order tried again) the entry is written over.
	for range 2 {
		if err := a.publish(ctx, hosts[0], cert); err != nil {
			t.Fatal(err)
		}
	}
	fb := &broken{Persistence: b.persist, get: down}
	b.persist = fb
	if err := caCheck(b, hosts[0], keyAuth); err == nil {
		t.Fatal("answered without reading the challenge")
	}
	fb.get = nil
	p.now = p.now.Add(challengeRecheck)
	if err := caCheck(b, hosts[0], keyAuth); err != nil {
		t.Fatal(err)
	}
	// A challenge that cannot be removed ends with its order.
	fa.del = down
	a.clearChallenges()
	if _, err := p.mem.GetEntry(ctx, "acme-challenge#tls-ca.test", hosts[0]); err != nil {
		t.Fatal("the challenge went although removing it failed")
	}
	p.now = p.now.Add(orderLease)
	if err := caCheck(b, hosts[0], keyAuth); err == nil {
		t.Fatal("a challenge is answered after its order's time")
	}
}

func TestRunBacksOffWhenTheStorageFails(t *testing.T) {
	p := newPair(t)
	a := p.enclave(nil)
	ctx, cancel := context.WithCancel(context.Background())
	// The first read fails and ends the run; Run doubles the wait.
	a.persist = cancelOnRead{Persistence: &broken{Persistence: p.sealed(), read: errors.New("down")}, cancel: cancel}
	a.Run(ctx)
	if a.backoff != 2*firstBackoff {
		t.Fatalf("backoff %s", a.backoff)
	}
}

type cancelOnRead struct {
	Persistence
	cancel func()
}

func (c cancelOnRead) Read(ctx context.Context) (*Identity, Version, error) {
	c.cancel()
	return c.Persistence.Read(ctx)
}

func TestAManagerInMemoryRenewsAlone(t *testing.T) {
	ctx := context.Background()
	now := t0
	m, ca := newManager(t, nil, &now)
	if _, err := m.ensure(ctx); err != nil || ca.calls != 1 {
		t.Fatal(err)
	}
	if m.currentLeaf() == nil {
		t.Fatal("no leaf")
	}
	now = now.Add(70 * 24 * time.Hour)
	if _, err := m.ensure(ctx); err != nil || ca.calls != 2 {
		t.Fatalf("renewal in memory: %v, calls %d", err, ca.calls)
	}
	if (&Manager{}).currentLeaf() != nil {
		t.Fatal("a leaf without a certificate")
	}
}

func TestSealedReadRefuses(t *testing.T) {
	ctx := context.Background()
	p := newPair(t)
	s := p.sealed()
	if _, _, err := s.Read(ctx); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("read before any identity: %v", err)
	}
	if _, err := s.Load(ctx); err != nil {
		t.Fatal(err)
	}
	// A stored version that was changed by hand is lost, never opened.
	names, _ := p.mem.ListRecords(ctx, "tls-ca.test@", 10)
	rec, _ := p.mem.GetRecord(ctx, names[0])
	rec.Value[len(rec.Value)-1] ^= 1
	p.mem.SetRecord(*rec)
	if _, _, err := p.sealed().Read(ctx); err == nil {
		t.Fatal("read a changed identity")
	}
	// Plain records sealed under another data key do not open.
	mem := store.NewMemory()
	plain := &Sealed{Records: mem, Keys: p.keys, Name: "x"}
	if _, err := plain.Load(ctx); err != nil {
		t.Fatal(err)
	}
	other := &Sealed{Records: mem, Keys: localKeys(t), Name: "x"}
	if _, _, err := other.Read(ctx); err == nil {
		t.Fatal("opened with another root secret")
	}
	r, _ := mem.GetRecord(ctx, "x")
	r.Value = r.Value[:4]
	mem.SetRecord(*r)
	if _, _, err := plain.Read(ctx); err == nil {
		t.Fatal("opened a cut record")
	}
}

func TestAStoredChallengeWithoutAUsableKeyIsRefused(t *testing.T) {
	ctx := context.Background()
	p := newPair(t)
	a := p.enclave(nil)
	s := a.persist.(*Sealed)
	for name, c := range map[string]challengeJSON{
		"garbage key":    {Chain: [][]byte{{1}}, Key: []byte{1, 2}, Until: p.now.Add(time.Hour)},
		"no certificate": {Key: mustPKCS8(t, a), Until: p.now.Add(time.Hour)},
	} {
		plain, _ := json.Marshal(c)
		sealed, err := envelopeSeal(s, plain, hosts[0])
		if err != nil {
			t.Fatal(err)
		}
		value, _ := json.Marshal(challengeEntry{Sealed: sealed})
		p.mem.SetEntry(store.Entry{Partition: s.challengePartition(), Key: hosts[0], Value: value})
		if _, err := s.GetChallenge(ctx, hosts[0]); err == nil {
			t.Fatalf("%s: opened", name)
		}
	}
	sealed, _ := envelopeSeal(s, []byte("not json"), hosts[0])
	value, _ := json.Marshal(challengeEntry{Sealed: sealed})
	p.mem.SetEntry(store.Entry{Partition: s.challengePartition(), Key: hosts[0], Value: value})
	if _, err := s.GetChallenge(ctx, hosts[0]); err == nil {
		t.Fatal("opened a challenge that is not JSON")
	}
	// A key that cannot be written is refused before anything is stored.
	if err := s.PutChallenge(ctx, hosts[0], &tls.Certificate{PrivateKey: struct{}{}}, p.now); err == nil {
		t.Fatal("stored a challenge without a key")
	}
}

func mustPKCS8(t *testing.T, m *Manager) []byte {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(m.ident.Key)
	if err != nil {
		t.Fatal(err)
	}
	return der
}

func envelopeSeal(s *Sealed, plain []byte, host string) ([]byte, error) {
	key, err := s.challengeKey()
	if err != nil {
		return nil, err
	}
	return envelope.Seal(key, plain, s.challengeAAD(host))
}
