package smtpd

import (
	"bufio"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net"
	"net/smtp"
	"net/textproto"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/services/mailboxes"
)

// recipients fakes the mailboxes: u@inbox.test and v@inbox.test lead to
// mailboxes, slow@ is over its limits, broken@ cannot be read.
type recipients struct {
	mu        sync.Mutex
	delivered [][]byte
	to        [][]string
	fail      error
	panics    bool
	hold      chan struct{}
}

func (r *recipients) Accept(_ context.Context, rcpt string) (string, error) {
	switch strings.ToLower(strings.Trim(rcpt, "<>")) {
	case "u@inbox.test", "u-alias@inbox.test":
		return "mbx_u", nil
	case "v@inbox.test":
		return "mbx_v", nil
	case "slow@inbox.test":
		return "", mailboxes.ErrSlowDown
	case "broken@inbox.test":
		return "", errors.New("the store failed")
	}
	return "", mailboxes.ErrNoSuchMailbox
}

func (r *recipients) Deliver(ctx context.Context, _ string, ids []string, raw []byte) error {
	r.mu.Lock()
	fail, panics, hold := r.fail, r.panics, r.hold
	r.mu.Unlock()
	if panics {
		panic("deliver")
	}
	if hold != nil {
		select {
		case <-hold:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if fail != nil {
		return fail
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.delivered = append(r.delivered, raw)
	r.to = append(r.to, ids)
	return nil
}

func (r *recipients) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.delivered)
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

// counting counts what the server reads from its connections.
type counting struct {
	net.Listener
	read atomic.Int64
}

func (c *counting) Accept() (net.Conn, error) {
	conn, err := c.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return &countingConn{Conn: conn, n: &c.read}, nil
}

type countingConn struct {
	net.Conn
	n *atomic.Int64
}

func (c *countingConn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	c.n.Add(int64(n))
	return n, err
}

type fixture struct {
	srv  *Server
	addr string
	pool *x509.CertPool
	in   *recipients
	ln   *counting
	logs *strings.Builder
}

func start(t *testing.T, l Limits) *fixture {
	t.Helper()
	cert, pool := serverCert(t)
	in := &recipients{}
	logs := &strings.Builder{}
	var mu sync.Mutex
	log := slog.New(slog.NewTextHandler(writerFunc(func(p []byte) (int, error) {
		mu.Lock()
		defer mu.Unlock()
		return logs.Write(p)
	}), nil))
	srv := New(in, Config{Domain: "inbox.test", TLS: &tls.Config{Certificates: []tls.Certificate{cert}}, Limits: l}, log)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	cl := &counting{Listener: ln}
	go func() { _ = srv.Serve(cl) }()
	t.Cleanup(func() { _ = srv.Close() })
	return &fixture{srv: srv, addr: ln.Addr().String(), pool: pool, in: in, ln: cl, logs: logs}
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

// client is a session driven line by line.
type client struct {
	t    *testing.T
	conn net.Conn
	tp   *textproto.Conn
}

func (f *fixture) open(t *testing.T, secure bool) *client {
	t.Helper()
	conn, err := net.Dial("tcp", f.addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	c := &client{t: t, conn: conn, tp: textproto.NewConn(conn)}
	c.expect(220)
	c.do("EHLO client.test", 250)
	if secure {
		c.do("STARTTLS", 220)
		tc := tls.Client(conn, &tls.Config{ServerName: "inbox.test", RootCAs: f.pool})
		if err := tc.Handshake(); err != nil {
			t.Fatal(err)
		}
		c.conn, c.tp = tc, textproto.NewConn(tc)
		c.do("EHLO client.test", 250)
	}
	return c
}

// send writes a line and reads the answer.
func (c *client) send(line string) (int, string, error) {
	_ = c.conn.SetDeadline(time.Now().Add(10 * time.Second))
	if err := c.tp.PrintfLine("%s", line); err != nil {
		return 0, "", err
	}
	return c.tp.ReadResponse(0)
}

func (c *client) do(line string, want int) string {
	c.t.Helper()
	code, msg, err := c.send(line)
	if code != want {
		c.t.Fatalf("%s: %d %s %v, want %d", line, code, msg, err, want)
	}
	return msg
}

func (c *client) expect(want int) string {
	c.t.Helper()
	_ = c.conn.SetDeadline(time.Now().Add(10 * time.Second))
	code, msg, err := c.tp.ReadResponse(0)
	if code != want {
		c.t.Fatalf("answer %d %s %v, want %d", code, msg, err, want)
	}
	return msg
}

// answer reads the next answer, whatever it is.
func (c *client) answer() (int, string) {
	_ = c.conn.SetDeadline(time.Now().Add(10 * time.Second))
	code, msg, _ := c.tp.ReadResponse(0)
	return code, msg
}

// closed reports whether the server closed the connection, reading what is
// left.
func (c *client) closed() bool {
	_ = c.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	_, err := io.Copy(io.Discard, c.conn)
	return err == nil || errors.Is(err, io.EOF) || strings.Contains(err.Error(), "reset")
}

// data sends a whole message with DATA and reads the answer.
func (c *client) data(body string) (int, string) {
	c.t.Helper()
	c.do("DATA", 354)
	w := c.tp.DotWriter()
	_, _ = io.WriteString(w, body)
	_ = w.Close()
	return c.answer()
}

func eventually(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for range 400 {
		if ok() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s never held", what)
}

func (f *fixture) zero(t *testing.T) {
	t.Helper()
	eventually(t, "the DATA budget back at zero", func() bool { return f.srv.Held() == 0 })
}

const message = "From: a@example.org\r\nTo: u@inbox.test\r\nSubject: Hi\r\n\r\nHello.\r\n"

func TestDeliversOverTLS(t *testing.T) {
	f := start(t, Limits{})
	c := f.open(t, true)
	c.do("MAIL FROM:<a@example.org>", 250)
	c.do("RCPT TO:<u@inbox.test>", 250)
	c.do("RCPT TO:<U-ALIAS@inbox.test>", 250) // the same mailbox: one
	c.do("RCPT TO:<v@inbox.test>", 250)
	if code, msg := c.data(message); code != 250 {
		t.Fatalf("%d %s", code, msg)
	}
	if f.in.count() != 1 || len(f.in.to[0]) != 2 || !strings.Contains(string(f.in.delivered[0]), "Hello.") {
		t.Fatalf("delivered %v to %v", f.in.count(), f.in.to)
	}
	// Another transaction on the same session.
	c.do("MAIL FROM:<a@example.org>", 250)
	c.do("RCPT TO:<u@inbox.test>", 250)
	if code, _ := c.data(message); code != 250 {
		t.Fatal(code)
	}
	c.do("QUIT", 221)
	f.zero(t)
	eventually(t, "the session given back", func() bool { return f.srv.Sessions() == 0 })
}

// net/smtp, as a sender would use it.
func TestNetSMTP(t *testing.T) {
	f := start(t, Limits{})
	c, err := smtp.Dial(f.addr)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.StartTLS(&tls.Config{ServerName: "inbox.test", RootCAs: f.pool}); err != nil {
		t.Fatal(err)
	}
	if err := c.Mail("a@example.org"); err != nil {
		t.Fatal(err)
	}
	if err := c.Rcpt("u@inbox.test"); err != nil {
		t.Fatal(err)
	}
	w, _ := c.Data()
	_, _ = w.Write([]byte(message))
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	_ = c.Quit()
}

// D1: six sessions that send MAIL FROM without SIZE, then NOOP, hold no
// byte of the budget, and each ends at TransactionDeadline.
func TestD1MailFromHoldsNothing(t *testing.T) {
	l := Limits{Transaction: 400 * time.Millisecond}
	f := start(t, l)
	var clients []*client
	began := time.Now()
	for range 6 {
		c := f.open(t, true)
		c.do("MAIL FROM:<a@example.org>", 250)
		c.do("NOOP", 250)
		clients = append(clients, c)
	}
	if f.srv.Held() != 0 {
		t.Fatalf("MAIL FROM took %d bytes", f.srv.Held())
	}
	for _, c := range clients {
		code, msg := c.answer()
		if code != 421 || !strings.HasPrefix(msg, "4.4.2") || !c.closed() {
			t.Fatalf("at the transaction's deadline: %d %s", code, msg)
		}
	}
	if took := time.Since(began); took < l.Transaction {
		t.Fatalf("ended after %v", took)
	}
}

// D2: a declared SIZE over 25 MiB is refused at MAIL FROM; a SIZE of 1 is a
// hint, and 20 MiB of DATA after it is stored.
func TestD2SizeIsAHint(t *testing.T) {
	f := start(t, Limits{})
	c := f.open(t, true)
	if code, msg, _ := c.send(fmt.Sprintf("MAIL FROM:<a@example.org> SIZE=%d", 26<<20)); code != 552 || !strings.HasPrefix(msg, "5.3.4") {
		t.Fatalf("%d %s", code, msg)
	}
	c.do("MAIL FROM:<a@example.org> SIZE=1", 250)
	c.do("RCPT TO:<u@inbox.test>", 250)
	big := message + strings.Repeat(strings.Repeat("x", 998)+"\r\n", 20<<20/1000+1)
	if code, msg := c.data(big); code != 250 {
		t.Fatalf("%d %s", code, msg)
	}
	if f.in.count() != 1 || len(f.in.delivered[0]) < 20<<20 {
		t.Fatal("not stored whole")
	}
	f.zero(t)
}

// D3: DATA that brings a byte just inside DataStall each time holds one step
// and ends at TransactionDeadline with 451 4.4.2, closed.
func TestD3SlowDataEndsAtTheTransactionsDeadline(t *testing.T) {
	l := Limits{Transaction: 900 * time.Millisecond, DataStall: 300 * time.Millisecond}
	f := start(t, l)
	c := f.open(t, true)
	c.do("MAIL FROM:<a@example.org>", 250)
	c.do("RCPT TO:<u@inbox.test>", 250)
	c.do("DATA", 354)
	held := int64(0)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 10 {
			if _, err := c.conn.Write([]byte("x")); err != nil {
				return
			}
			time.Sleep(200 * time.Millisecond)
			if h := f.srv.Held(); h > held {
				held = h
			}
		}
	}()
	code, msg := c.answer()
	<-done
	if code != 451 || !strings.HasPrefix(msg, "4.4.2") || !c.closed() {
		t.Fatalf("%d %s", code, msg)
	}
	if held != DataStep {
		t.Fatalf("held %d", held)
	}
	f.zero(t)
}

// D4: DATA silent for DataStall: 451 4.4.2, closed, bytes given back.
func TestD4SilentData(t *testing.T) {
	f := start(t, Limits{DataStall: 300 * time.Millisecond})
	c := f.open(t, true)
	c.do("MAIL FROM:<a@example.org>", 250)
	c.do("RCPT TO:<u@inbox.test>", 250)
	c.do("DATA", 354)
	_, _ = c.conn.Write([]byte("Subject: x\r\n"))
	code, msg := c.answer()
	if code != 451 || !strings.HasPrefix(msg, "4.4.2") || !c.closed() {
		t.Fatalf("%d %s", code, msg)
	}
	f.zero(t)
	if f.in.count() != 0 {
		t.Fatal("a stopped message was delivered")
	}
}

// D5: the budget held by other sessions, a new DATA waits AdmissionWait for
// its first step: 452 4.3.1, closed, and the rest of its message is not read.
func TestD5NoRoomForData(t *testing.T) {
	l := Limits{DataInFlight: 2 << 10, DataStep: 1 << 10, AdmissionWait: 300 * time.Millisecond, DataStall: 10 * time.Second}
	f := start(t, l)
	for range 2 {
		c := f.open(t, true)
		c.do("MAIL FROM:<a@example.org>", 250)
		c.do("RCPT TO:<u@inbox.test>", 250)
		c.do("DATA", 354)
		_, _ = c.conn.Write([]byte(strings.Repeat("y", 1<<10)))
	}
	eventually(t, "the budget full", func() bool { return f.srv.Held() == 2<<10 })
	c := f.open(t, true)
	c.do("MAIL FROM:<a@example.org>", 250)
	c.do("RCPT TO:<u@inbox.test>", 250)
	c.do("DATA", 354)
	before := f.ln.read.Load()
	go func() {
		_ = c.conn.SetWriteDeadline(time.Now().Add(3 * time.Second))
		_, _ = c.conn.Write([]byte(strings.Repeat("z", 4<<20)))
	}()
	code, msg := c.answer()
	if code != 452 || !strings.HasPrefix(msg, "4.3.1") || !c.closed() {
		t.Fatalf("%d %s", code, msg)
	}
	if read := f.ln.read.Load() - before; read > 256<<10 {
		t.Fatalf("the server read %d bytes of a message it refused", read)
	}
}

// D6: every byte taken is given back, whatever ends the transaction.
func TestD6TheBudgetComesBack(t *testing.T) {
	small := Limits{MaxMessage: 64 << 10, DataStep: 8 << 10, DataStall: 300 * time.Millisecond, Transaction: 2 * time.Second}
	cases := []struct {
		name string
		run  func(t *testing.T, f *fixture, c *client)
	}{
		{"250", func(t *testing.T, _ *fixture, c *client) {
			if code, _ := c.data(message); code != 250 {
				t.Fatal(code)
			}
		}},
		{"451 from Deliver", func(t *testing.T, f *fixture, c *client) {
			f.in.fail = mailboxes.ErrNotAllStored
			if code, msg := c.data(message); code != 451 || !strings.HasPrefix(msg, "4.3.0") {
				t.Fatal(code, msg)
			}
		}},
		{"552 past the largest message", func(t *testing.T, _ *fixture, c *client) {
			c.do("DATA", 354)
			_, _ = c.conn.Write([]byte(lines(100 << 10)))
			if code, msg := c.answer(); code != 552 || !strings.HasPrefix(msg, "5.3.4") || !c.closed() {
				t.Fatal(code, msg)
			}
		}},
		{"RSET during BDAT", func(t *testing.T, _ *fixture, c *client) {
			bdat(t, c, "Subject: x\r\n", false)
			c.do("RSET", 250)
		}},
		{"QUIT during BDAT", func(t *testing.T, _ *fixture, c *client) {
			bdat(t, c, "Subject: x\r\n", false)
			c.do("QUIT", 221)
		}},
		{"a second EHLO during BDAT", func(t *testing.T, _ *fixture, c *client) {
			bdat(t, c, "Subject: x\r\n", false)
			c.do("EHLO again.test", 250)
		}},
		{"a lost connection during BDAT", func(t *testing.T, _ *fixture, c *client) {
			bdat(t, c, "Subject: x\r\n", false)
			_ = c.conn.Close()
		}},
		{"a lost connection during DATA", func(t *testing.T, f *fixture, c *client) {
			c.do("DATA", 354)
			_, _ = c.conn.Write([]byte(lines(20 << 10)))
			eventually(t, "bytes taken", func() bool { return f.srv.Held() > 0 })
			_ = c.conn.Close()
		}},
		{"the stall deadline", func(t *testing.T, _ *fixture, c *client) {
			c.do("DATA", 354)
			_, _ = c.conn.Write([]byte("Subject: x\r\n"))
			if code, _ := c.answer(); code != 451 {
				t.Fatal(code)
			}
		}},
		{"a panic in Deliver", func(t *testing.T, f *fixture, c *client) {
			f.in.panics = true
			if code, _ := c.data(message); code != 421 {
				t.Fatal(code)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := start(t, small)
			c := f.open(t, true)
			c.do("MAIL FROM:<a@example.org>", 250)
			c.do("RCPT TO:<u@inbox.test>", 250)
			tc.run(t, f, c)
			f.zero(t)
		})
	}
}

// lines is about n bytes of text in lines of 78 characters.
func lines(n int) string {
	return strings.Repeat(strings.Repeat("z", 78)+"\r\n", n/80+1)
}

func TestLongLine(t *testing.T) {
	f := start(t, Limits{})
	c := f.open(t, true)
	c.do("MAIL FROM:<a@example.org>", 250)
	c.do("RCPT TO:<u@inbox.test>", 250)
	c.do("DATA", 354)
	_, _ = c.conn.Write([]byte(strings.Repeat("z", 8000)))
	if code, msg := c.answer(); code != 500 || !strings.HasPrefix(msg, "5.4.0") || !c.closed() {
		t.Fatal(code, msg)
	}
	f.zero(t)
}

func bdat(t *testing.T, c *client, chunk string, last bool) (int, string) {
	t.Helper()
	cmd := fmt.Sprintf("BDAT %d", len(chunk))
	if last {
		cmd += " LAST"
	}
	_ = c.conn.SetDeadline(time.Now().Add(10 * time.Second))
	if err := c.tp.PrintfLine("%s", cmd); err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(c.conn, chunk); err != nil {
		t.Fatal(err)
	}
	code, msg := c.answer()
	if !last && code != 250 {
		t.Fatalf("BDAT: %d %s", code, msg)
	}
	return code, msg
}

func TestBDAT(t *testing.T) {
	f := start(t, Limits{})
	c := f.open(t, true)
	c.do("MAIL FROM:<a@example.org>", 250)
	c.do("RCPT TO:<u@inbox.test>", 250)
	bdat(t, c, "Subject: Hi\r\n", false)
	if code, msg := bdat(t, c, "\r\nHello.\r\n", true); code != 250 {
		t.Fatal(code, msg)
	}
	if f.in.count() != 1 || string(f.in.delivered[0]) != "Subject: Hi\r\n\r\nHello.\r\n" {
		t.Fatalf("%q", f.in.delivered)
	}
	f.zero(t)
}

// D7: BDAT chunks spread past the transaction's deadline; a chunk and then
// silence; chunks without LAST and then QUIT.
func TestD7BDATDeadlines(t *testing.T) {
	t.Run("spread past the transaction", func(t *testing.T) {
		f := start(t, Limits{Transaction: 700 * time.Millisecond, DataStall: 400 * time.Millisecond})
		c := f.open(t, true)
		c.do("MAIL FROM:<a@example.org>", 250)
		c.do("RCPT TO:<u@inbox.test>", 250)
		var code int
		var msg string
		for range 10 {
			time.Sleep(250 * time.Millisecond)
			_ = c.conn.SetDeadline(time.Now().Add(5 * time.Second))
			_ = c.tp.PrintfLine("BDAT 3")
			_, _ = io.WriteString(c.conn, "abc")
			if code, msg = c.answer(); code != 250 {
				break
			}
		}
		// Between chunks go-smtp is reading a command: it answers the stall
		// with 421; inside a chunk the session answers 451 4.4.2.
		if code != 421 && code != 451 || !c.closed() {
			t.Fatalf("%d %s", code, msg)
		}
		f.zero(t)
	})
	t.Run("a chunk and then silence", func(t *testing.T) {
		f := start(t, Limits{DataStall: 300 * time.Millisecond})
		c := f.open(t, true)
		c.do("MAIL FROM:<a@example.org>", 250)
		c.do("RCPT TO:<u@inbox.test>", 250)
		_ = c.tp.PrintfLine("BDAT 10")
		_, _ = io.WriteString(c.conn, "abc")
		if code, msg := c.answer(); code != 451 || !strings.HasPrefix(msg, "4.4.2") || !c.closed() {
			t.Fatalf("%d %s", code, msg)
		}
		f.zero(t)
	})
	t.Run("without LAST, then QUIT", func(t *testing.T) {
		f := start(t, Limits{})
		c := f.open(t, true)
		c.do("MAIL FROM:<a@example.org>", 250)
		c.do("RCPT TO:<u@inbox.test>", 250)
		bdat(t, c, "abc", false)
		bdat(t, c, "def", false)
		c.do("QUIT", 221)
		f.zero(t)
		if f.in.count() != 0 {
			t.Fatal("delivered without LAST")
		}
	})
}

// D8: a connection that never sends STARTTLS ends at TLSDeadline; one that
// never sends MAIL FROM after the handshake at IdleDeadline; a session that
// keeps going at SessionDeadline.
func TestD8SessionDeadlines(t *testing.T) {
	t.Run("no STARTTLS", func(t *testing.T) {
		f := start(t, Limits{TLS: 300 * time.Millisecond})
		c := f.open(t, false)
		began := time.Now()
		c.do("RSET", 250) // what a sender does before TLS does not extend it
		if code, msg := c.answer(); code != 421 || !strings.HasPrefix(msg, "4.4.2") || !c.closed() {
			t.Fatalf("%d %s", code, msg)
		}
		if time.Since(began) > 2*time.Second {
			t.Fatal("too late")
		}
	})
	t.Run("no MAIL FROM", func(t *testing.T) {
		f := start(t, Limits{Idle: 300 * time.Millisecond})
		c := f.open(t, true)
		c.do("NOOP", 250)
		if code, msg := c.answer(); code != 421 || !strings.HasPrefix(msg, "4.4.2") || !c.closed() {
			t.Fatalf("%d %s", code, msg)
		}
	})
	t.Run("the whole session", func(t *testing.T) {
		f := start(t, Limits{Idle: 300 * time.Millisecond, Session: 900 * time.Millisecond})
		c := f.open(t, true)
		began := time.Now()
		for time.Since(began) < 5*time.Second {
			code, msg, err := c.send("MAIL FROM:<a@example.org>")
			if code == 421 || err != nil {
				if !strings.HasPrefix(msg, "4.4.2") && err == nil {
					t.Fatalf("%d %s", code, msg)
				}
				break
			}
			if code, _, _ = c.send("RSET"); code == 421 {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if took := time.Since(began); took < 800*time.Millisecond || took > 3*time.Second {
			t.Fatalf("the session ended after %v", took)
		}
	})
}

// D9: the 21st MAIL FROM, the 201st command, the 11th refusal: 421 4.7.0,
// closed; four unknown commands: closed by go-smtp.
func TestD9SessionCaps(t *testing.T) {
	t.Run("transactions", func(t *testing.T) {
		f := start(t, Limits{})
		c := f.open(t, true)
		for range MaxTransactions {
			c.do("MAIL FROM:<a@example.org>", 250)
			c.do("RSET", 250)
		}
		if code, msg, _ := c.send("MAIL FROM:<a@example.org>"); code != 421 || !strings.HasPrefix(msg, "4.7.0") || !c.closed() {
			t.Fatalf("%d %s", code, msg)
		}
	})
	t.Run("commands", func(t *testing.T) {
		f := start(t, Limits{Transactions: 1000})
		c := f.open(t, true) // EHLO twice: two commands
		for n := 2; n+2 < MaxCommands; n += 2 {
			c.do("MAIL FROM:<a@example.org>", 250)
			c.do("RSET", 250)
		}
		c.do("MAIL FROM:<a@example.org>", 250) // the 199th
		c.do("RCPT TO:<u@inbox.test>", 250)    // the 200th
		if code, msg, _ := c.send("RCPT TO:<u@inbox.test>"); code != 421 || !strings.HasPrefix(msg, "4.7.0") || !c.closed() {
			t.Fatalf("%d %s", code, msg)
		}
	})
	t.Run("refusals", func(t *testing.T) {
		f := start(t, Limits{})
		c := f.open(t, true)
		c.do("MAIL FROM:<a@example.org>", 250)
		for range MaxFailedCommands {
			c.do("RCPT TO:<nobody@inbox.test>", 550)
		}
		if code, msg, _ := c.send("RCPT TO:<nobody@inbox.test>"); code != 421 || !strings.HasPrefix(msg, "4.7.0") || !c.closed() {
			t.Fatalf("%d %s", code, msg)
		}
	})
	t.Run("unknown commands", func(t *testing.T) {
		f := start(t, Limits{})
		c := f.open(t, true)
		for range 3 {
			c.do("FROB", 500)
		}
		_, _, _ = c.send("FROB")
		if !c.closed() {
			t.Fatal("not closed")
		}
	})
}

// D10: the 65th session is refused with 421 4.3.2.
func TestD10Sessions(t *testing.T) {
	f := start(t, Limits{})
	var conns []net.Conn
	for range MaxSessions {
		conn, err := net.Dial("tcp", f.addr)
		if err != nil {
			t.Fatal(err)
		}
		conns = append(conns, conn)
		if line, err := bufio.NewReader(conn).ReadString('\n'); err != nil || !strings.HasPrefix(line, "220") {
			t.Fatalf("%q %v", line, err)
		}
	}
	conn, err := net.Dial("tcp", f.addr)
	if err != nil {
		t.Fatal(err)
	}
	line, _ := bufio.NewReader(conn).ReadString('\n')
	if !strings.HasPrefix(line, "421 4.3.2") {
		t.Fatalf("%q", line)
	}
	for _, c := range conns {
		_ = c.Close()
	}
	eventually(t, "every session given back", func() bool { return f.srv.Sessions() == 0 })
}

// D11: every row of "The answers, all of them" that the server answers.
func TestD11Answers(t *testing.T) {
	f := start(t, Limits{MaxMessage: 64 << 10, Transaction: 500 * time.Millisecond})
	t.Run("no TLS", func(t *testing.T) {
		c := f.open(t, false)
		if code, msg, _ := c.send("MAIL FROM:<a@example.org>"); code != 530 || !strings.HasPrefix(msg, "5.7.0") {
			t.Fatal(code, msg)
		}
	})
	t.Run("RCPT TO", func(t *testing.T) {
		c := f.open(t, true)
		c.do("MAIL FROM:<a@example.org>", 250)
		for rcpt, want := range map[string]string{
			"nobody@inbox.test": "550 5.1.1", "slow@inbox.test": "451 4.7.1", "broken@inbox.test": "451 4.3.0",
		} {
			code, msg, _ := c.send("RCPT TO:<" + rcpt + ">")
			if got := fmt.Sprintf("%d %s", code, msg); !strings.HasPrefix(got, want) {
				t.Errorf("%s: %s", rcpt, got)
			}
		}
		for range MaxRecipients {
			c.do("RCPT TO:<u@inbox.test>", 250)
		}
		if code, msg, _ := c.send("RCPT TO:<v@inbox.test>"); code != 452 || !strings.HasPrefix(msg, "4.5.3") {
			t.Fatal(code, msg)
		}
	})
	t.Run("no recipient accepted", func(t *testing.T) {
		c := f.open(t, true)
		c.do("MAIL FROM:<a@example.org>", 250)
		c.do("RCPT TO:<nobody@inbox.test>", 550)
		// go-smtp answers before the session is asked (ADR 0026 names 554).
		if code, _, _ := c.send("DATA"); code != 502 && code != 554 {
			t.Fatal(code)
		}
	})
	t.Run("too large during DATA", func(t *testing.T) {
		c := f.open(t, true)
		c.do("MAIL FROM:<a@example.org>", 250)
		c.do("RCPT TO:<u@inbox.test>", 250)
		code, msg := c.data(lines(70 << 10))
		if code != 552 || !strings.HasPrefix(msg, "5.3.4") || !c.closed() {
			t.Fatal(code, msg)
		}
	})
	t.Run("the transaction past its deadline", func(t *testing.T) {
		c := f.open(t, true)
		c.do("MAIL FROM:<a@example.org>", 250)
		c.do("RCPT TO:<u@inbox.test>", 250)
		c.do("DATA", 354)
		for range 5 {
			_, _ = c.conn.Write([]byte("x"))
			time.Sleep(150 * time.Millisecond)
		}
		if code, msg := c.answer(); code != 451 || !strings.HasPrefix(msg, "4.4.2") || !c.closed() {
			t.Fatal(code, msg)
		}
	})
	t.Run("not stored everywhere, or being stored by another delivery", func(t *testing.T) {
		g := start(t, Limits{})
		g.in.fail = errors.New("the store failed")
		c := g.open(t, true)
		c.do("MAIL FROM:<a@example.org>", 250)
		c.do("RCPT TO:<u@inbox.test>", 250)
		if code, msg := c.data(message); code != 451 || !strings.HasPrefix(msg, "4.3.0") {
			t.Fatal(code, msg)
		}
		// The session goes on after a delivery that failed.
		g.in.mu.Lock()
		g.in.fail = mailboxes.ErrNotAllStored
		g.in.mu.Unlock()
		c.do("MAIL FROM:<a@example.org>", 250)
		c.do("RCPT TO:<u@inbox.test>", 250)
		if code, _ := c.data(message); code != 451 {
			t.Fatal(code)
		}
		if strings.Contains(g.logs.String(), "inbox.test") || strings.Contains(g.logs.String(), "example.org") {
			t.Fatal("an address was logged")
		}
	})
	t.Run("the delivery past its minute", func(t *testing.T) {
		g := start(t, Limits{Ingest: 200 * time.Millisecond})
		g.in.hold = make(chan struct{})
		g.in.fail = nil
		c := g.open(t, true)
		c.do("MAIL FROM:<a@example.org>", 250)
		c.do("RCPT TO:<u@inbox.test>", 250)
		if code, msg := c.data(message); code != 451 || !strings.HasPrefix(msg, "4.3.0") {
			t.Fatal(code, msg)
		}
	})
}

func TestConnDeadlines(t *testing.T) {
	a, b := net.Pipe()
	defer b.Close()
	s := New(&recipients{}, Config{Domain: "inbox.test"}, slog.New(slog.DiscardHandler))
	s.sessions <- struct{}{}
	k := newConn(a, s)
	if err := k.SetDeadline(time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if !earliest(time.Time{}, time.Time{}).IsZero() || earliest(time.Unix(2, 0), time.Unix(1, 0)) != time.Unix(1, 0) {
		t.Fatal("earliest")
	}
	_ = k.Close()
	_ = k.Close() // given back once
	if s.Sessions() != 0 {
		t.Fatal("given back twice or not at all")
	}
}
