package smtpd

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"log/slog"
	"math/big"
	"net"
	"net/smtp"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeInbox struct {
	mu   sync.Mutex
	got  []string
	fail error
}

func (f *fakeInbox) Accepts(rcpt string) bool {
	return strings.EqualFold(strings.Trim(rcpt, "<>"), "u@inbox.test")
}

func (f *fakeInbox) Ingest(_ context.Context, _ string, _ []string, raw []byte) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != nil {
		return "", f.fail
	}
	f.got = append(f.got, string(raw))
	return "id", nil
}

func serverCert(t *testing.T) (tls.Certificate, *x509.CertPool) {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1), DNSNames: []string{"inbox.test"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, _ := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	leaf, _ := x509.ParseCertificate(der)
	pool := x509.NewCertPool()
	pool.AddCert(leaf)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key, Leaf: leaf}, pool
}

func start(t *testing.T, in *fakeInbox, maxBytes int64) (string, *x509.CertPool) {
	t.Helper()
	cert, pool := serverCert(t)
	srv := New(in, Config{Domain: "inbox.test", TLS: &tls.Config{Certificates: []tls.Certificate{cert}}, MaxMessageBytes: maxBytes}, slog.New(slog.DiscardHandler))
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return ln.Addr().String(), pool
}

func dial(t *testing.T, addr string, pool *x509.CertPool) *smtp.Client {
	t.Helper()
	c, err := smtp.Dial(addr)
	if err != nil {
		t.Fatal(err)
	}
	if ok, _ := c.Extension("STARTTLS"); !ok {
		t.Fatal("STARTTLS not offered")
	}
	if err := c.StartTLS(&tls.Config{ServerName: "inbox.test", RootCAs: pool}); err != nil {
		t.Fatal(err)
	}
	return c
}

func send(c *smtp.Client, rcpt, body string) error {
	if err := c.Mail("sender@example.org"); err != nil {
		return err
	}
	if err := c.Rcpt(rcpt); err != nil {
		return err
	}
	w, err := c.Data()
	if err != nil {
		return err
	}
	if _, err := w.Write([]byte(body)); err != nil {
		return err
	}
	return w.Close()
}

func code(err error) int {
	var tp interface{ Error() string }
	if errors.As(err, &tp) {
		var n int
		for _, r := range tp.Error() {
			if r < '0' || r > '9' {
				break
			}
			n = n*10 + int(r-'0')
		}
		return n
	}
	return 0
}

func TestDelivery(t *testing.T) {
	in := &fakeInbox{}
	addr, pool := start(t, in, 1<<20)
	c := dial(t, addr, pool)
	defer c.Close()
	if err := send(c, "U@inbox.test", "Subject: hi\r\n\r\nhello\r\n"); err != nil {
		t.Fatal(err)
	}
	if len(in.got) != 1 || !strings.Contains(in.got[0], "hello") {
		t.Fatalf("stored %q", in.got)
	}
}

// Without STARTTLS the parent instance could read the message, so the enclave
// refuses it before any address or content is sent.
func TestRefusesMailWithoutTLS(t *testing.T) {
	in := &fakeInbox{}
	addr, _ := start(t, in, 1<<20)
	c, err := smtp.Dial(addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Mail("sender@example.org"); code(err) != 530 {
		t.Fatalf("MAIL without TLS: %v, want 530", err)
	}
	if err := send(c, "u@inbox.test", "Subject: hi\r\n\r\nhello\r\n"); err == nil || len(in.got) != 0 {
		t.Fatalf("message accepted without TLS: %v, stored %q", err, in.got)
	}
}

func TestRejections(t *testing.T) {
	in := &fakeInbox{}
	addr, pool := start(t, in, 100)

	c := dial(t, addr, pool)
	_ = c.Mail("sender@example.org")
	if err := c.Rcpt("someone@else.test"); code(err) != 550 {
		t.Fatalf("unknown recipient: %v", err)
	}
	_ = c.Close()

	c = dial(t, addr, pool)
	if err := send(c, "u@inbox.test", strings.Repeat("x", 500)); code(err) != 552 {
		t.Fatalf("too large: %v", err)
	}
	_ = c.Close()

	in.fail = errors.New("store down")
	c = dial(t, addr, pool)
	if err := send(c, "u@inbox.test", "Subject: hi\r\n\r\nhello\r\n"); code(err) != 451 {
		t.Fatalf("storage failure must be temporary: %v", err)
	}
	_ = c.Close()
	if len(in.got) != 0 {
		t.Fatalf("stored %q", in.got)
	}
}
