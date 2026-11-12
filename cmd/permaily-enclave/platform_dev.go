//go:build dev

package main

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
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
	"github.com/permaily-org/permaily/internal/domain"
	"github.com/permaily-org/permaily/internal/idp"
	"github.com/permaily-org/permaily/internal/idp/idptest"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/keys"
	"github.com/permaily-org/permaily/internal/msgauth"
	"github.com/permaily-org/permaily/internal/nsm"
	"github.com/permaily-org/permaily/internal/nsm/fakensm"
	"github.com/permaily-org/permaily/internal/router"
	"github.com/permaily-org/permaily/internal/services/accounts"
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
	// mailboxes is the mailboxes of accounts (ADR 0026), and reader reads
	// their stored mail; nil without accounts.
	mailboxes *mailboxes.Service
	reader    inbox.Reader
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

// defaultSuperAdmin is the super admin's address in dev mode, which a test or
// a person trying the admin panel signs up with.
const defaultSuperAdmin = "admin@permaily.test"

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
	if p.accounts, err = accounts.New(accounts.Config{
		// As in the enclave: only the newest version of an account's record is kept.
		Records: &store.Anchored{Records: mem, Commits: commits, OnlyNewest: true, Now: devClock}, Entries: mem,
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
		Records: &store.Anchored{Records: mem, Commits: commits, OnlyNewest: true, Strict: true, Now: devClock},
		Storage: mem, Keys: local, Accounts: p.accounts, Domain: hosts.Inbox, Log: log, Now: devClock,
		Auth: msgauth.DKIM(net.DefaultResolver.LookupTXT),
	}); err != nil {
		return nil, err
	}
	p.accounts.SetMailboxes(p.mailboxes)
	p.reader = inbox.NewReader(mem)
	port := cfg.smtpPort
	if port == 25 {
		port = 2525 // unprivileged default for local runs
	}
	if p.smtpListener, err = net.Listen("tcp", fmt.Sprintf(":%d", port)); err != nil {
		return nil, err
	}
	return p, nil
}
