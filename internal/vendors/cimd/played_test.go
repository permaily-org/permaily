package cimd

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// pki is a certificate authority the played vendors' certificates chain to,
// and that the resolver under test trusts in place of the image's roots.
type pki struct {
	cert *x509.Certificate
	key  *ecdsa.PrivateKey
	pool *x509.CertPool
}

func newPKI(t testing.TB) *pki {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "played vendors' root"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(cert)
	return &pki{cert: cert, key: key, pool: pool}
}

// leaf is a certificate for names, valid from notBefore to notAfter, signed
// by the authority, or by itself when selfSigned.
func (p *pki) leaf(t testing.TB, names []string, notBefore, notAfter time.Time, selfSigned bool) tls.Certificate {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: names[0]},
		DNSNames:     names,
		NotBefore:    notBefore,
		NotAfter:     notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	parent, signer := p.cert, p.key
	if selfSigned {
		parent, signer = tmpl, key
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, parent, &key.PublicKey, signer)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

func (p *pki) valid(t testing.TB, names ...string) tls.Certificate {
	return p.leaf(t, names, time.Now().Add(-time.Hour), time.Now().Add(time.Hour), false)
}

// seen is one request a played vendor received.
type seen struct {
	Method, Path, Proto string
	Header              http.Header
	ContentLength       int64
}

// vendor is a played vendor: an HTTPS server speaking HTTP/2 with a
// certificate for its names, answering with serve, recording what it saw.
type vendor struct {
	srv   *httptest.Server
	mu    sync.Mutex
	serve func(w http.ResponseWriter, r *http.Request)
	seen  []seen
	// date, when set, is the played clock the answers' Date is read from,
	// so that a response's age is the played one; serve may set another.
	date func() time.Time
}

func newVendor(t testing.TB, ca *pki, serve func(w http.ResponseWriter, r *http.Request), names ...string) *vendor {
	t.Helper()
	v := &vendor{serve: serve}
	v.srv = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		v.mu.Lock()
		v.seen = append(v.seen, seen{Method: r.Method, Path: r.URL.Path, Proto: r.Proto, Header: r.Header.Clone(), ContentLength: r.ContentLength})
		serve, date := v.serve, v.date
		v.mu.Unlock()
		if date != nil {
			w.Header().Set("Date", date().UTC().Format(http.TimeFormat))
		}
		serve(w, r)
	}))
	v.srv.EnableHTTP2 = true
	v.srv.TLS = &tls.Config{Certificates: []tls.Certificate{ca.valid(t, names...)}}
	v.srv.StartTLS()
	t.Cleanup(v.srv.Close)
	return v
}

func (v *vendor) set(serve func(w http.ResponseWriter, r *http.Request)) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.serve = serve
}

func (v *vendor) requests() []seen {
	v.mu.Lock()
	defer v.mu.Unlock()
	return append([]seen(nil), v.seen...)
}

func (v *vendor) count() int { return len(v.requests()) }

// clientTo is an HTTP client that reaches addr whatever the URL's host, as
// the vendor egress reaches a vendor whatever the enclave asked for, and
// that trusts ca in place of the image's roots.
func clientTo(addr string, ca *pki) *http.Client {
	return &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, addr)
		},
		TLSClientConfig: &tls.Config{RootCAs: ca.pool, MinVersion: tls.VersionTLS12},
	}}
}

// clock is a played clock.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func newClock() *clock { return &clock{t: time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)} }

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

// document is a valid client document for id.
func document(id string) []byte {
	return fmt.Appendf(nil, `{"client_id":%q,"client_name":"Played","redirect_uris":["https://app.vendor.dev/callback","http://127.0.0.1/callback"],"token_endpoint_auth_method":"none"}`, id)
}

// answer serves body with the given headers.
func answer(body []byte, header ...string) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, _ *http.Request) {
		for i := 0; i+1 < len(header); i += 2 {
			w.Header().Set(header[i], header[i+1])
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(body)
	}
}

// documents answers document(id) for the URL asked, with the headers given.
func documents(header ...string) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		answer(document("https://"+r.Host+r.URL.Path), header...)(w, r)
	}
}

const deployment = "permaily.com"

// played is a resolver over one played vendor at names, with a clock.
type played struct {
	ca  *pki
	v   *vendor
	clk *clock
	r   *Resolver
}

func newPlayed(t testing.TB, serve func(http.ResponseWriter, *http.Request), names ...string) *played {
	t.Helper()
	if len(names) == 0 {
		names = []string{"app.vendor.dev"}
	}
	ca := newPKI(t)
	v := newVendor(t, ca, serve, names...)
	clk := newClock()
	v.mu.Lock()
	v.date = clk.now
	v.mu.Unlock()
	return &played{ca: ca, v: v, clk: clk, r: New(clientTo(v.srv.Listener.Addr().String(), ca), deployment, clk.now)}
}

// other is a second resolver over the same vendor: the other enclave.
func (p *played) other() *Resolver {
	return New(clientTo(p.v.srv.Listener.Addr().String(), p.ca), deployment, p.clk.now)
}
