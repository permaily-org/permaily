//go:build dev

package main

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/decide"
	"github.com/permaily-org/permaily/internal/decide/decidetest"
	"github.com/permaily-org/permaily/internal/domain"
	"github.com/permaily-org/permaily/internal/enclave"
	"github.com/permaily-org/permaily/internal/idp"
	"github.com/permaily-org/permaily/internal/idp/idptest"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/keys"
	"github.com/permaily-org/permaily/internal/msgauth"
	"github.com/permaily-org/permaily/internal/nsm"
	"github.com/permaily-org/permaily/internal/nsm/fakensm"
	"github.com/permaily-org/permaily/internal/query"
	"github.com/permaily-org/permaily/internal/router"
	"github.com/permaily-org/permaily/internal/services/accounts"
	"github.com/permaily-org/permaily/internal/services/decisions"
	"github.com/permaily-org/permaily/internal/services/mailboxes"
	"github.com/permaily-org/permaily/internal/store"
	"github.com/permaily-org/permaily/internal/tlsacme"
	"github.com/permaily-org/permaily/pkg/attestation"
	"github.com/permaily-org/permaily/pkg/provenance"
)

type platform struct {
	mode         string
	listener     net.Listener
	smtpListener net.Listener
	attester     nsm.Attester
	certs        *tlsacme.Manager
	// mailboxes is the mailboxes of accounts (ADR 0026), reader reads
	// their stored mail and matcher evaluates formulas over it (ADR 0027);
	// nil without accounts.
	mailboxes *mailboxes.Service
	reader    inbox.Reader
	matcher   inbox.Matcher
	// rootStatus describes the key that holds the root secret, and handoff
	// gives the root secret to the next release; nil without a root secret.
	rootStatus func(context.Context) (*attestation.RootStatus, error)
	handoff    func(context.Context, keys.Handoff) (*keys.HandoffResult, error)
	// sigstore says which trust root releases are checked against, and
	// updateTrust takes a newer one that Sigstore published.
	sigstore    func() *attestation.SigstoreStatus
	updateTrust func(provenance.Update) (*attestation.SigstoreStatus, error)
	// anchors describes the records that cannot be put back to an earlier copy.
	anchors func() *attestation.AnchorStatus
	// accounts is sign-in and accounts (ADR 0017); nil in an image that
	// sends no mail. devRoutes are served by the app's host in dev mode only.
	accounts  *accounts.Service
	devRoutes map[string]http.Handler
	// mail describes who else could get a copy of the mail the enclave
	// sends, as SES answers; nil when it sends none.
	mail func() *attestation.MailStatus
	// model asks the model what depends on meaning (ADR 0028), and holds
	// its key, its spend, its canary and its alarms; nil without mailboxes.
	model *decisions.Service
	// connections is the authorization server's state and the connections
	// it makes (ADR 0030); nil without the model. connectionRecords is the
	// one strict Anchored of its records.
	connections       *composedConnections
	connectionRecords *store.Anchored
	// records are the Anchored of each kind of record, by kind, for the
	// test that no name is read through two (dev mode only).
	records map[string]*store.Anchored
}

// devAhead is how far the clock of dev mode runs ahead of the real one, in
// nanoseconds. The test of the whole service moves it past the days an
// account takes to be deleted.
var devAhead atomic.Int64

func devClock() time.Time { return time.Now().Add(time.Duration(devAhead.Load())) }

// devTerms is the version of the terms in force in dev mode, the compiled one
// until POST /dev/terms?version=<v> changes it: then every account that
// agreed to another is asked to agree again, as after a release that changes
// the terms. The enclave build cannot change it.
var devTerms atomic.Pointer[string]

func devTermsVersion() string {
	if v := devTerms.Load(); v != nil {
		return *v
	}
	return domain.TermsVersion
}

// devProviders plays Google, GitHub, Meta and Microsoft in dev mode: a local
// server that signs anybody in, as idptest.Default says or as the test of the
// whole service tells it.
var devProviders atomic.Pointer[idptest.Server]

// devShared, when a test sets it, is the storage and the keys of every
// platform this process starts in dev mode: two enclaves of one deployment
// over one storage, as the test of a sign-in started through one and ended
// through the other needs.
var devShared atomic.Pointer[devStorage]

type devStorage struct {
	mem     *store.Memory
	commits *store.MemoryCommits
	keys    *keys.Local
	// apps is held while a platform gives the providers their applications:
	// the operator gives them through one enclave at a time, and two
	// platforms writing the one record at once can each lose more times than
	// the service tries again.
	apps sync.Mutex
}

// newDevStorage is empty storage and new keys on the clock of dev mode. The
// clock is set here, before any platform uses the storage: a platform that
// set it on storage it shares would write it while another reads it.
func newDevStorage() (*devStorage, error) {
	local, err := keys.NewLocal()
	if err != nil {
		return nil, err
	}
	mem := store.NewMemory()
	mem.Now = devClock
	return &devStorage{mem: mem, commits: store.NewMemoryCommits(), keys: local}, nil
}

// devConfig is what dev mode alone reads: where the played vendors are.
// Without it dev mode reaches no vendor at all.
type devConfig struct {
	// vendors is the address every vendor's name is dialled at, and
	// vendorsCA the PEM file of the only roots their certificates are
	// checked against.
	vendors, vendorsCA string
}

func devFlags(fs *flag.FlagSet, d *devConfig) {
	fs.StringVar(&d.vendors, "dev-vendors", "", "dev mode: address of the played vendors, which every vendor's name is dialled at; empty reaches no vendor")
	fs.StringVar(&d.vendorsCA, "dev-vendors-ca", "", "dev mode: PEM file of the only roots the played vendors' certificates are checked against")
}

// devVendors, when a test sets it, is the client dev mode fetches vendors'
// documents and key sets with: a played vendor's. It takes the place of
// -dev-vendors.
var devVendors atomic.Pointer[http.Client]

// errNoVendor is every fetch of dev mode with no played vendor.
var errNoVendor = errors.New("dev mode reaches no vendor")

// devVendorHTTP is the client of dev mode's vendors: the test's, else one
// that dials the played vendors' address for every name and checks their
// certificates against their roots alone, over HTTP/2 only, as the image's
// vendor egress client does; with neither, one that dials nothing. No real
// vendor is ever reached from dev mode.
func devVendorHTTP(d devConfig) *http.Client {
	if hc := devVendors.Load(); hc != nil {
		return hc
	}
	if d.vendors == "" {
		return &http.Client{Transport: &http.Transport{
			DialContext: func(context.Context, string, string) (net.Conn, error) { return nil, errNoVendor },
		}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	roots := x509.NewCertPool()
	if pem, err := os.ReadFile(d.vendorsCA); err == nil {
		roots.AppendCertsFromPEM(pem)
	}
	return playedVendorClient(d.vendors, roots)
}

// playedVendorClient is the image's vendor egress client (HTTPS, HTTP/2
// only, TLS 1.2 or later, no redirect, no cookie, no compression, headers
// and time bounded) dialling addr for every name, against roots alone.
func playedVendorClient(addr string, roots *x509.CertPool) *http.Client {
	tr := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "tcp", addr)
		},
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
			RootCAs:    roots,
			NextProtos: []string{"h2"},
			VerifyConnection: func(cs tls.ConnectionState) error {
				if cs.NegotiatedProtocol != "h2" {
					return errors.New("the played vendor does not speak HTTP/2")
				}
				return nil
			},
		},
		Protocols:              new(http.Protocols),
		DisableCompression:     true,
		MaxResponseHeaderBytes: enclave.VendorEgressHeaderMaxBytes,
		TLSHandshakeTimeout:    enclave.VendorEgressTimeout,
	}
	tr.Protocols.SetHTTP2(true)
	return &http.Client{
		Transport:     tr,
		Timeout:       enclave.VendorEgressTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// devPlatforms are the platforms this process started in dev mode, for the
// tests that look at what each composed.
var devPlatforms platformList

type platformList struct {
	mu sync.Mutex
	ps []*platform
}

func (l *platformList) add(p *platform) {
	l.mu.Lock()
	l.ps = append(l.ps, p)
	l.mu.Unlock()
}

func (l *platformList) all() []*platform {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]*platform(nil), l.ps...)
}

// defaultSuperAdmin is the super admin's address in dev mode, which a test or
// a person trying the admin panel signs up with.
const defaultSuperAdmin = "admin@permaily.test"

// devModelClient, when a test sets it, is the egress of the model's real
// client (openai.New) in dev mode: a played provider over HTTP/2
// (decidetest.Provider), so dev mode composes the model as the image does.
// Without it, dev mode asks decidetest's fake, which never reaches OpenAI.
var devModelClient atomic.Pointer[http.Client]

// devCanary makes tab answer the canary compiled into the image as it was
// measured, so the canary of dev mode finds nothing moved.
func devCanary(tab *decidetest.Table) *decidetest.Table {
	if c, err := decide.LoadCanary(); err == nil {
		for _, m := range c.Messages {
			for _, a := range m.Atoms {
				tab.SetFor(m.Subject, m.Body, a.Atom(), a.P)
			}
		}
	}
	return tab
}

// devRemoveEvery is how often dev mode looks for accounts that are due: its
// storage is in memory, so looking costs nothing.
const devRemoveEvery = time.Second

// logOutput is the terminal in dev mode.
func logOutput(config) io.Writer { return os.Stdout }

// newPlatform listens on TCP (HTTPS on -port, SMTP on -smtp-port, default
// 2525 in dev), uses a self-signed certificate (valid for every host plus
// localhost), a fake attester, local keys and an in-memory store. DKIM uses
// the system resolver. Accounts are on, mail to people is kept in memory, and
// the four providers are a local server that asks nobody for a password.
func newPlatform(ctx context.Context, cfg config, hosts router.Hosts, log *slog.Logger) (*platform, error) {
	att, err := fakensm.New()
	if err != nil {
		return nil, err
	}
	certs, err := tlsacme.New(hosts.All(), cfg.acmeDirectory, nil, nil, log)
	if err != nil {
		return nil, err
	}
	names := append(hosts.All(), "localhost")
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: hosts.App},
		DNSNames:     names,
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, certs.Key().Public(), certs.Key())
	if err != nil {
		return nil, err
	}
	leaf, _ := x509.ParseCertificate(der)
	certs.SetCertificate(&tls.Certificate{Certificate: [][]byte{der}, PrivateKey: certs.Key(), Leaf: leaf})
	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", cfg.port))
	if err != nil {
		return nil, err
	}
	storage := devShared.Load()
	if storage == nil {
		if storage, err = newDevStorage(); err != nil {
			return nil, err
		}
	}
	mem, commits, local := storage.mem, storage.commits, storage.keys
	p := &platform{mode: "dev", listener: ln, attester: att, certs: certs}
	// Accounts, with the messages kept instead of sent: GET /dev/outbox on
	// the app's host shows them, sign-in codes included.
	appURL := "https://" + hosts.App
	if cfg.port != 443 {
		appURL = fmt.Sprintf("%s:%d", appURL, cfg.port)
	}
	outbox := &accounts.Outbox{}
	providers := idptest.New()
	providers.Now = devClock
	devProviders.Store(providers)
	go func() {
		<-ctx.Done()
		providers.Close()
	}()
	// Each kind of record has an Anchored of its own, as in the enclave.
	accountRecords := &store.Anchored{Records: mem, Commits: commits, OnlyNewest: true, Now: devClock}
	mailboxRecords := &store.Anchored{Records: mem, Commits: commits, OnlyNewest: true, Strict: true, Now: devClock}
	modelRecords := &store.Anchored{Records: mem, Commits: commits, OnlyNewest: true, Strict: true, Now: devClock}
	if p.accounts, err = accounts.New(accounts.Config{
		// As in the enclave: only the newest version of an account's record is kept.
		Records: accountRecords, Entries: mem,
		Keys: local, Mailer: outbox, AppURL: appURL, SuperAdmin: cfg.superAdmin, Log: log, Now: devClock, Terms: devTermsVersion,
		Providers: &idp.Client{HTTP: &http.Client{}, RedirectURL: appURL + providerCallback, Now: devClock, Endpoints: providers.Endpoints()},
	}); err != nil {
		return nil, err
	}
	// Every provider is on from the start, with the application the local
	// server knows: in the enclave the operator gives each (scripts/provider.sh).
	storage.apps.Lock()
	defer storage.apps.Unlock()
	for _, provider := range idp.Providers {
		app := providers.App(provider)
		if _, err := p.accounts.SetProviderApp(ctx, string(provider), contracts.ProviderAppSet{ClientID: app.ClientID, ClientSecret: app.ClientSecret}); err != nil {
			return nil, err
		}
	}
	go p.accounts.Run(ctx, devRemoveEvery)
	p.devRoutes = map[string]http.Handler{"/dev/outbox": http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(outbox.Sent())
	}), "/dev/terms": http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			if v := r.URL.Query().Get("version"); v != "" {
				devTerms.Store(&v)
			} else {
				devTerms.Store(nil)
			}
		}
		_, _ = io.WriteString(w, devTermsVersion()+"\n")
	})}
	// The mailboxes of accounts, with their own strict records, and SMTP on
	// -smtp-port (2525 by default in dev mode).
	if p.mailboxes, err = mailboxes.New(mailboxes.Config{
		Records: mailboxRecords,
		Storage: mem, Keys: local, Accounts: p.accounts, Notifier: p.accounts, Domain: hosts.Inbox, Log: log, Now: devClock,
		Auth: msgauth.DKIM(net.DefaultResolver.LookupTXT),
	}); err != nil {
		return nil, err
	}
	p.accounts.SetMailboxes(p.mailboxes)
	p.reader = inbox.NewReader(mem)
	p.matcher = inbox.NewMatcher(mem, query.Eval)
	// The model, as in the enclave: its records strict in one Anchored of
	// their own, its alarms in the outbox. No key is set: the operator gives
	// one (PUT /v1/admin/model-key) and the model is off until then.
	model := modelParts{
		cfg: cfg.model, records: modelRecords,
		entries: mem, keys: local, mailer: outbox, superAdmin: cfg.superAdmin, log: log, now: devClock,
	}
	if hc := devModelClient.Load(); hc != nil {
		model.egress = hc
	} else {
		tab := devCanary(decidetest.NewTable())
		model.newModel = func(key decide.Credential) decide.Model { return decidetest.NewFake(tab, key) }
	}
	if p.model, err = composeModel(model); err != nil {
		return nil, err
	}
	// Connections, as in the enclave: their records strict in one Anchored
	// of their own, and vendors played, never a real one (devVendorHTTP).
	p.connectionRecords = &store.Anchored{Records: mem, Commits: commits, OnlyNewest: true, Strict: true, Now: devClock}
	if p.connections, err = composeConnections(connectionsParts{
		oauth: oauthConfig(hosts, cfg.oauthPort()), domain: cfg.domain, records: p.connectionRecords, entries: mem, keys: local,
		vendorHTTP: devVendorHTTP(cfg.dev), accounts: p.accounts, mailboxes: p.mailboxes, model: p.model, now: devClock,
	}); err != nil {
		return nil, err
	}
	p.records = map[string]*store.Anchored{"accounts": accountRecords, "mailboxes": mailboxRecords, "model": modelRecords, "connections": p.connectionRecords}
	devPlatforms.add(p)
	port := cfg.smtpPort
	if port == 25 {
		port = 2525 // unprivileged default for local runs
	}
	if p.smtpListener, err = net.Listen("tcp", fmt.Sprintf(":%d", port)); err != nil {
		return nil, err
	}
	return p, nil
}
