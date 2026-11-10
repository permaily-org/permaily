package tlsacme

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"log/slog"
	"math/big"
	"net/http"
	"os"
	"testing"
	"time"

	"golang.org/x/crypto/acme"

	"github.com/permaily-org/permaily/internal/keys"
	"github.com/permaily-org/permaily/internal/store"
)

var (
	t0    = time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	hosts = []string{"app.permaily.test", "inbox.permaily.test"}
	quiet = slog.New(slog.DiscardHandler)
)

func newSealed(t *testing.T, records store.Records, k keys.Service) *Sealed {
	t.Helper()
	return &Sealed{Records: records, Keys: k, Name: "tls-ca.test", Now: func() time.Time { return t0 }}
}

func localKeys(t *testing.T) keys.Service {
	t.Helper()
	k, err := keys.NewLocal()
	if err != nil {
		t.Fatal(err)
	}
	return k
}

// certFor issues a certificate for names with key, valid from notBefore for life.
func certFor(t *testing.T, key *ecdsa.PrivateKey, names []string, notBefore time.Time, life time.Duration) []byte {
	t.Helper()
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()), DNSNames: names,
		NotBefore: notBefore, NotAfter: notBefore.Add(life),
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return der
}

func TestIdentityEncoding(t *testing.T) {
	id, err := NewIdentity(t0.Add(500 * time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	id.AccountURI, id.Chain = "https://ca.test/acct/1", [][]byte{{1, 2}, {3}}
	b, err := id.marshal()
	if err != nil {
		t.Fatal(err)
	}
	got, err := unmarshalIdentity(b)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Key.Equal(id.Key) || !got.AccountKey.Equal(id.AccountKey) || got.Key.Equal(id.AccountKey) {
		t.Fatal("keys changed in the round trip")
	}
	if !got.CreatedAt.Equal(t0) || got.AccountURI != id.AccountURI || len(got.Chain) != 2 || !bytes.Equal(got.Chain[0], []byte{1, 2}) {
		t.Fatalf("identity %+v", got)
	}

	_, edKey, _ := ed25519.GenerateKey(rand.Reader)
	edDER, _ := x509.MarshalPKCS8PrivateKey(edKey)
	var good identityJSON
	_ = json.Unmarshal(b, &good)
	encode := func(change func(*identityJSON)) []byte {
		j := good
		change(&j)
		out, _ := json.Marshal(j)
		return out
	}
	for name, bad := range map[string][]byte{
		"not json":            []byte("nope"),
		"future version":      encode(func(j *identityJSON) { j.Version = 2 }),
		"broken key":          encode(func(j *identityJSON) { j.Key = []byte{1, 2, 3} }),
		"broken account key":  encode(func(j *identityJSON) { j.AccountKey = nil }),
		"key of another type": encode(func(j *identityJSON) { j.Key = edDER }),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := unmarshalIdentity(bad); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}

func TestSealedKeepsTheIdentity(t *testing.T) {
	ctx := context.Background()
	mem, k := store.NewMemory(), localKeys(t)

	first := newSealed(t, mem, k)
	if err := first.Save(ctx, &Identity{}); err == nil {
		t.Fatal("Save before Load must fail")
	}
	id, err := first.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !id.CreatedAt.Equal(t0) || len(id.Chain) != 0 {
		t.Fatalf("new identity %+v", id)
	}

	// What leaves the enclave must not contain the private keys.
	rec, err := mem.GetRecord(ctx, "tls-ca.test")
	if err != nil {
		t.Fatal(err)
	}
	keyDER, _ := x509.MarshalPKCS8PrivateKey(id.Key)
	keyRaw, _ := id.Key.Bytes()
	accountRaw, _ := id.AccountKey.Bytes()
	if bytes.Contains(rec.Value, keyDER) || bytes.Contains(rec.Value, keyRaw) || bytes.Contains(rec.Value, accountRaw) {
		t.Fatal("the stored record holds a private key in clear")
	}

	// After a restart (a new Sealed over the same store and KMS) the keys are the same.
	id.AccountURI, id.Chain = "https://ca.test/acct/1", [][]byte{{9}}
	if err := first.Save(ctx, id); err != nil {
		t.Fatal(err)
	}
	again, err := newSealed(t, mem, k).Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !again.Key.Equal(id.Key) || !again.AccountKey.Equal(id.AccountKey) || again.AccountURI != id.AccountURI || !bytes.Equal(again.Chain[0], []byte{9}) || !again.CreatedAt.Equal(t0) {
		t.Fatal("identity changed across a restart")
	}
}

// failingStore fails every write with an error that is not "already exists".
type failingStore struct {
	store.Records
	put error
}

func (f failingStore) PutRecord(context.Context, *store.Record, bool) error { return f.put }

// failingKeys cannot create data keys.
type failingKeys struct{ keys.Service }

func (failingKeys) NewDataKey(context.Context, map[string]string) (keys.DataKey, error) {
	return keys.DataKey{}, errors.New("no data key")
}

// A failure while creating the identity gives no identity: the
// caller waits and tries again instead of serving with keys that were not stored.
func TestSealedFailuresGiveNoIdentity(t *testing.T) {
	ctx := context.Background()
	down := errors.New("dynamodb unreachable")

	// Creating: the store refuses the record, or there is no data key.
	if _, err := newSealed(t, failingStore{Records: store.NewMemory(), put: down}, localKeys(t)).Load(ctx); !errors.Is(err, down) {
		t.Fatalf("store failure on creation: %v", err)
	}
	if _, err := newSealed(t, store.NewMemory(), failingKeys{}).Load(ctx); err == nil {
		t.Fatal("created an identity without a data key")
	}

}

func TestSealedRejects(t *testing.T) {
	ctx := context.Background()
	fresh := func(t *testing.T) (*store.Memory, keys.Service, *store.Record) {
		mem, k := store.NewMemory(), localKeys(t)
		if _, err := newSealed(t, mem, k).Load(ctx); err != nil {
			t.Fatal(err)
		}
		rec, _ := mem.GetRecord(ctx, "tls-ca.test")
		return mem, k, rec
	}

	t.Run("another KMS key", func(t *testing.T) {
		mem, _, _ := fresh(t)
		if _, err := newSealed(t, mem, localKeys(t)).Load(ctx); err == nil {
			t.Fatal("opened with another key")
		}
	})
	t.Run("tampered record", func(t *testing.T) {
		mem, k, rec := fresh(t)
		rec.Value[len(rec.Value)-1] ^= 1
		_ = mem.PutRecord(ctx, rec, false)
		if _, err := newSealed(t, mem, k).Load(ctx); err == nil {
			t.Fatal("opened a tampered record")
		}
	})
	t.Run("record moved to another name", func(t *testing.T) {
		// The host cannot hand a staging identity to a production image, or the reverse.
		mem, k, rec := fresh(t)
		rec.Name = "tls-other-ca.test"
		_ = mem.PutRecord(ctx, rec, true)
		other := newSealed(t, mem, k)
		other.Name = "tls-other-ca.test"
		if _, err := other.Load(ctx); err == nil {
			t.Fatal("opened a record sealed for another name")
		}
	})
	t.Run("sealed garbage", func(t *testing.T) {
		mem, k, _ := fresh(t)
		s := newSealed(t, mem, k)
		if _, err := s.Load(ctx); err != nil {
			t.Fatal(err)
		}
		s.Now = nil // also covers the default clock
		if err := s.put(ctx, &Identity{}, false); err == nil {
			t.Fatal("stored an identity without keys")
		}
	})
}

// racing makes the first read miss, as when two enclaves start together and
// the other one creates the record in between.
type racing struct {
	*store.Memory
	misses int
	fail   error
}

func (r *racing) GetRecord(ctx context.Context, name string) (*store.Record, error) {
	if r.fail != nil {
		return nil, r.fail
	}
	if r.misses > 0 {
		r.misses--
		return nil, store.ErrNotFound
	}
	return r.Memory.GetRecord(ctx, name)
}

func TestSealedConcurrentCreation(t *testing.T) {
	ctx := context.Background()
	mem, k := store.NewMemory(), localKeys(t)
	winner, err := newSealed(t, mem, k).Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	loser, err := newSealed(t, &racing{Memory: mem, misses: 1}, k).Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !loser.Key.Equal(winner.Key) {
		t.Fatal("two enclaves ended up with different keys")
	}
	if _, err := newSealed(t, &racing{Memory: mem, misses: 5}, k).Load(ctx); err == nil {
		t.Fatal("no error when the record never becomes readable")
	}
	if _, err := newSealed(t, &racing{Memory: mem, fail: errors.New("dynamodb down")}, k).Load(ctx); err == nil {
		t.Fatal("a storage failure must not create a new identity")
	}
}

// issuer stands in for the CA: it certifies the manager's key for its hosts.
type issuer struct {
	t     *testing.T
	m     *Manager
	calls int
	fail  error
	life  time.Duration
	names []string
	key   *ecdsa.PrivateKey // nil: the manager's own key
}

func (i *issuer) issue(context.Context) error {
	i.calls++
	if i.fail != nil {
		return i.fail
	}
	names, key := i.names, i.key
	if names == nil {
		names = i.m.hosts
	}
	if key == nil {
		key = i.m.ident.Key
	}
	i.m.mu.Lock()
	defer i.m.mu.Unlock()
	i.m.ident.Chain = [][]byte{certFor(i.t, key, names, i.m.now().Add(-time.Minute), i.life)}
	i.m.ident.AccountURI = "https://ca.test/acct/7"
	return nil
}

func newManager(t *testing.T, persist Persistence, now *time.Time) (*Manager, *issuer) {
	t.Helper()
	m, err := New(hosts, "https://ca.test/directory", nil, persist, quiet)
	if err != nil {
		t.Fatal(err)
	}
	m.now = func() time.Time { return *now }
	m.retry = time.Millisecond
	ca := &issuer{t: t, m: m, life: 90 * 24 * time.Hour}
	m.issue = ca.issue
	return m, ca
}

func TestManagerInMemory(t *testing.T) {
	if _, err := New(nil, "https://ca.test/directory", nil, nil, quiet); err == nil {
		t.Fatal("no hosts accepted")
	}
	now := t0
	m, ca := newManager(t, nil, &now)
	if m.Key() == nil || len(m.UserData()) != 32 || m.Ready() {
		t.Fatal("an in-memory manager has its key at once and no certificate yet")
	}
	if st := m.Status(); st == nil || st.Sealed || st.ACMEAccount != "" || len(st.SPKISHA256) != 64 {
		t.Fatalf("status %+v", st)
	}
	if _, err := m.GetCertificate(&tls.ClientHelloInfo{ServerName: hosts[0]}); err == nil {
		t.Fatal("served a certificate before having one")
	}
	if !m.load(context.Background()) {
		t.Fatal("load")
	}
	if _, err := m.ensure(context.Background()); err != nil || ca.calls != 1 || !m.Ready() {
		t.Fatalf("ensure: %v, calls %d", err, ca.calls)
	}
	cert, err := m.GetCertificate(&tls.ClientHelloInfo{ServerName: hosts[0]})
	if err != nil || cert.Leaf.VerifyHostname(hosts[1]) != nil {
		t.Fatalf("certificate: %v", err)
	}
	if st := m.Status(); st.ACMEAccount != "https://ca.test/acct/7" {
		t.Fatalf("status %+v", st)
	}
	cfg := m.TLSConfig("h2")
	if cfg.MinVersion != tls.VersionTLS12 || len(cfg.NextProtos) != 2 || cfg.NextProtos[1] != acme.ALPNProto {
		t.Fatalf("TLS config %+v", cfg.NextProtos)
	}

	// ACME challenges are answered only for a name with a pending challenge.
	challenge := &tls.Certificate{}
	m.mu.Lock()
	m.challenges[hosts[0]] = challenge
	m.mu.Unlock()
	alpn := &tls.ClientHelloInfo{ServerName: hosts[0], SupportedProtos: []string{acme.ALPNProto}}
	if got, err := m.GetCertificate(alpn); err != nil || got != challenge {
		t.Fatalf("challenge certificate: %v", err)
	}
	m.clearChallenges()
	if _, err := m.GetCertificate(alpn); err == nil {
		t.Fatal("answered a challenge nobody asked for")
	}
	custom := &tls.Certificate{}
	m.SetCertificate(custom)
	if got, _ := m.GetCertificate(&tls.ClientHelloInfo{}); got != custom {
		t.Fatal("SetCertificate")
	}
}

func TestManagerSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	mem, k := store.NewMemory(), localKeys(t)
	now := t0

	first, ca := newManager(t, newSealed(t, mem, k), &now)
	if first.Key() != nil || first.UserData() != nil || first.Status() != nil {
		t.Fatal("a manager with sealed storage has no key before loading it")
	}
	if !first.load(ctx) {
		t.Fatal("load")
	}
	renewAt, err := first.ensure(ctx)
	if err != nil || ca.calls != 1 {
		t.Fatalf("first certificate: %v, calls %d", err, ca.calls)
	}
	if want := now.Add(-time.Minute).Add(60 * 24 * time.Hour); !renewAt.Equal(want) {
		t.Fatalf("renew at %s, want two thirds of the lifetime (%s)", renewAt, want)
	}
	if st := first.Status(); !st.Sealed || !st.KeyCreatedAt.Equal(t0) {
		t.Fatalf("status %+v", st)
	}
	// Asking again changes nothing while the certificate is good.
	if _, err := first.ensure(ctx); err != nil || ca.calls != 1 {
		t.Fatalf("second ensure: %v, calls %d", err, ca.calls)
	}

	// A restart, a month later: same key, same certificate, no request to the CA.
	now = t0.Add(30 * 24 * time.Hour)
	second, ca2 := newManager(t, newSealed(t, mem, k), &now)
	if !second.load(ctx) {
		t.Fatal("load")
	}
	if _, err := second.ensure(ctx); err != nil {
		t.Fatal(err)
	}
	if ca2.calls != 0 {
		t.Fatal("a restart asked the CA for another certificate")
	}
	if !bytes.Equal(second.UserData(), first.UserData()) {
		t.Fatal("the TLS key changed across a restart")
	}
	a, _ := first.GetCertificate(&tls.ClientHelloInfo{})
	b, _ := second.GetCertificate(&tls.ClientHelloInfo{})
	if !a.Leaf.Equal(b.Leaf) || second.Status().ACMEAccount != "https://ca.test/acct/7" {
		t.Fatal("certificate or account changed across a restart")
	}

	// Past two thirds of its lifetime it is renewed, with the same key, and saved.
	now = t0.Add(70 * 24 * time.Hour)
	if _, err := second.ensure(ctx); err != nil || ca2.calls != 1 {
		t.Fatalf("renewal: %v, calls %d", err, ca2.calls)
	}
	third, ca3 := newManager(t, newSealed(t, mem, k), &now)
	third.load(ctx)
	if _, err := third.ensure(ctx); err != nil || ca3.calls != 0 || !bytes.Equal(third.UserData(), first.UserData()) {
		t.Fatalf("after renewal: %v, calls %d", err, ca3.calls)
	}
}

func TestManagerRenewalFailureKeepsServing(t *testing.T) {
	ctx := context.Background()
	now := t0
	m, ca := newManager(t, newSealed(t, store.NewMemory(), localKeys(t)), &now)
	m.load(ctx)
	if _, err := m.ensure(ctx); err != nil {
		t.Fatal(err)
	}
	old, _ := m.GetCertificate(&tls.ClientHelloInfo{})

	now = t0.Add(70 * 24 * time.Hour)
	ca.fail = errors.New("CA unreachable")
	if _, err := m.ensure(ctx); err == nil {
		t.Fatal("renewal failure not reported")
	}
	if got, err := m.GetCertificate(&tls.ClientHelloInfo{}); err != nil || !got.Leaf.Equal(old.Leaf) {
		t.Fatal("the valid certificate stopped being served when renewal failed")
	}
}

func TestManagerRejectsUnusableCertificates(t *testing.T) {
	ctx := context.Background()
	other, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	cases := []struct {
		name  string
		setup func(ca *issuer)
	}{
		{"for another key", func(ca *issuer) { ca.key = other }},
		{"missing a host", func(ca *issuer) { ca.names = hosts[:1] }},
		{"already expired", func(ca *issuer) { ca.life = time.Second }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			now := t0
			m, ca := newManager(t, nil, &now)
			c.setup(ca)
			if _, err := m.ensure(ctx); err == nil || m.Ready() {
				t.Fatalf("installed a certificate %s: %v", c.name, err)
			}
		})
	}
	t.Run("garbage in the stored chain", func(t *testing.T) {
		now := t0
		m, ca := newManager(t, nil, &now)
		m.ident.Chain = [][]byte{{1, 2, 3}}
		if _, err := m.ensure(ctx); err != nil || ca.calls != 1 {
			t.Fatalf("did not replace a broken chain: %v, calls %d", err, ca.calls)
		}
	})
}

// flaky fails to load a few times, like KMS or DynamoDB being unreachable at boot.
type flaky struct {
	Persistence
	failures int
	saveErr  error
}

func (f *flaky) Load(ctx context.Context) (*Identity, error) {
	if f.failures > 0 {
		f.failures--
		return nil, errors.New("kms unreachable")
	}
	return f.Persistence.Load(ctx)
}

func (f *flaky) Save(ctx context.Context, id *Identity) error {
	if f.saveErr != nil {
		return f.saveErr
	}
	return f.Persistence.Save(ctx, id)
}

func TestManagerWaitsForItsIdentity(t *testing.T) {
	now := t0
	mem, k := store.NewMemory(), localKeys(t)
	m, _ := newManager(t, &flaky{Persistence: newSealed(t, mem, k), failures: 3}, &now)
	if !m.load(context.Background()) || m.Key() == nil {
		t.Fatal("did not retry until the identity loaded")
	}

	// While storage is down the enclave waits; it never makes up another key.
	down, _ := newManager(t, &flaky{Persistence: newSealed(t, mem, k), failures: 1 << 30}, &now)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if down.load(ctx) || down.Key() != nil || down.UserData() != nil {
		t.Fatal("got a key without storage")
	}
	down.Run(ctx) // returns at once: the context is done and there is no identity
}

func TestManagerRunAndSaveFailure(t *testing.T) {
	now := t0
	f := &flaky{Persistence: newSealed(t, store.NewMemory(), localKeys(t)), saveErr: errors.New("dynamodb down")}
	m, ca := newManager(t, f, &now)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { m.Run(ctx); close(done) }()
	deadline := time.Now().Add(5 * time.Second)
	for !m.Ready() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	<-done
	// A failed save does not stop the enclave from serving the certificate it got.
	if !m.Ready() || ca.calls != 1 {
		t.Fatalf("ready %v, calls %d", m.Ready(), ca.calls)
	}
}

// TestObtainAgainstACME runs the real ACME flow against a test CA (Pebble with
// PEBBLE_VA_ALWAYS_VALID=1, which accepts every challenge). Set
// PERMAILY_TEST_ACME to its directory URL; CI runs Pebble as a service.
func TestObtainAgainstACME(t *testing.T) {
	directory := os.Getenv("PERMAILY_TEST_ACME")
	if directory == "" {
		t.Skip("PERMAILY_TEST_ACME not set")
	}
	// Pebble serves its API with a certificate from its own test CA.
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}, Timeout: 30 * time.Second}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	mem, k := store.NewMemory(), localKeys(t)
	start := func() *Manager {
		m, err := New(hosts, directory, client, &Sealed{Records: mem, Keys: k, Name: "tls-pebble"}, quiet)
		if err != nil {
			t.Fatal(err)
		}
		if !m.load(ctx) {
			t.Fatal("load")
		}
		return m
	}

	first := start()
	if _, err := first.ensure(ctx); err != nil {
		t.Fatal(err)
	}
	cert, err := first.GetCertificate(&tls.ClientHelloInfo{ServerName: hosts[0]})
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range hosts {
		if err := cert.Leaf.VerifyHostname(h); err != nil {
			t.Fatal(err)
		}
	}
	if !cert.Leaf.PublicKey.(*ecdsa.PublicKey).Equal(first.Key().Public()) {
		t.Fatal("the certificate is not for the manager's key")
	}
	account := first.Status().ACMEAccount
	if account == "" {
		t.Fatal("no ACME account URL after issuance")
	}
	first.mu.RLock()
	pending := len(first.challenges)
	first.mu.RUnlock()
	if pending != 0 {
		t.Fatalf("%d challenge certificates left behind", pending)
	}

	// After a restart the stored certificate is used without asking the CA...
	second := start()
	second.issue = func(context.Context) error { return errors.New("asked the CA again") }
	if _, err := second.ensure(ctx); err != nil {
		t.Fatal(err)
	}
	// ...and a renewal comes from the same account with the same key.
	second.issue = second.obtain
	second.now = func() time.Time { return cert.Leaf.NotAfter.Add(-time.Hour) }
	if _, err := second.ensure(ctx); err != nil {
		t.Fatal(err)
	}
	renewed, _ := second.GetCertificate(&tls.ClientHelloInfo{})
	if renewed.Leaf.Equal(cert.Leaf) || second.Status().ACMEAccount != account || !bytes.Equal(second.UserData(), first.UserData()) {
		t.Fatal("renewal did not produce a new certificate from the same account and key")
	}
}
