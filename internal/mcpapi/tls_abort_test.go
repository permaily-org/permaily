package mcpapi_test

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/sha256"
	"crypto/tls"
	"encoding/binary"
	"encoding/hex"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/mcpapi"
	"github.com/permaily-org/permaily/internal/services/connections/connectionstest"
)

// keyUpdateConn pauses a wire write, with the same unblock-on-close and
// unblock-on-expired-deadline semantics as net.Conn. It never holds TLS
// locks itself; any such locks belong to the unmodified Go TLS server.
type keyUpdateConn struct {
	net.Conn
	armed       atomic.Bool
	paused      chan struct{}
	resume      chan struct{}
	interrupted chan struct{}
	once        sync.Once
}

func (c *keyUpdateConn) Write(p []byte) (int, error) {
	if c.armed.CompareAndSwap(true, false) {
		close(c.paused)
		select {
		case <-c.resume:
		case <-c.interrupted:
			return 0, os.ErrDeadlineExceeded
		}
	}
	select {
	case <-c.interrupted:
		return 0, os.ErrDeadlineExceeded
	default:
	}
	return c.Conn.Write(p)
}
func (c *keyUpdateConn) Close() error {
	c.once.Do(func() { close(c.interrupted) })
	return c.Conn.Close()
}
func (c *keyUpdateConn) SetWriteDeadline(d time.Time) error {
	if !d.IsZero() && !d.After(time.Now()) {
		c.once.Do(func() { close(c.interrupted) })
	}
	return c.Conn.SetWriteDeadline(d)
}
func (c *keyUpdateConn) SetDeadline(d time.Time) error {
	if e := c.SetWriteDeadline(d); e != nil {
		return e
	}
	return c.SetReadDeadline(d)
}

type keyUpdateListener struct {
	net.Listener
	accepted chan *keyUpdateConn
}

func (l *keyUpdateListener) Accept() (net.Conn, error) {
	c, e := l.Listener.Accept()
	if e != nil {
		return nil, e
	}
	p := &keyUpdateConn{Conn: c, paused: make(chan struct{}), resume: make(chan struct{}), interrupted: make(chan struct{})}
	l.accepted <- p
	return p, nil
}

type countTLSRecords struct {
	net.Conn
	seq   uint64
	count bool
}

func (c *countTLSRecords) Write(p []byte) (int, error) {
	if c.count {
		for b := p; len(b) >= 5; {
			n := int(binary.BigEndian.Uint16(b[3:5])) + 5
			if n > len(b) {
				panic("split record in probe")
			}
			if b[0] == 23 {
				c.seq++
			}
			b = b[n:]
		}
	}
	return c.Conn.Write(p)
}
func reviewTLS13Expand(t *testing.T, secret []byte, label string, n int) []byte {
	t.Helper()
	label = "tls13 " + label
	info := []byte{byte(n >> 8), byte(n), byte(len(label))}
	info = append(info, []byte(label)...)
	info = append(info, 0)
	out, e := hkdf.Expand(sha256.New, secret, string(info), n)
	if e != nil {
		t.Fatal(e)
	}
	return out
}

// betweenWrites holds the handler before a write of the answer reaches the
// connection, until the connection is interrupted (its socket's deadline
// passed, or closed): a handler between two chunks, with no write of TLS's
// in flight.
type betweenWrites struct {
	http.ResponseWriter
	at          chan struct{}
	once        sync.Once
	interrupted <-chan struct{}
}

func (w *betweenWrites) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.at) })
	<-w.interrupted
	return w.ResponseWriter.Write(p)
}

func (w *betweenWrites) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// V50 (#135's 8d34b32, over HTTP/1.1 since #131's second review): a peer
// that asks the server for a TLS KeyUpdate and reads nothing holds TLS's
// output lock with the server's reply blocked on the socket. Between two
// chunks no write of TLS is in flight, so tls.Conn.Close sends its
// close_notify and waits for that lock: a cut that only closed the
// connection would wait as long as the peer reads nothing. The cut sets the
// socket's write deadline first: the lease is finished promptly.
func TestV50ATLSKeyUpdateCannotHoldACut(t *testing.T) {
	w := newWorld()
	finished := make(chan struct{})
	w.leaseFor = func() *countingLease {
		return &countingLease{Lease: &connectionstest.Lease{}, onFinish: func() { close(finished) }}
	}
	handlerDone := make(chan struct{})
	h := w.handler()
	conns := make(chan *keyUpdateConn, 1)
	at := make(chan struct{})
	s := httptest.NewUnstartedServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		defer close(handlerDone)
		c := <-conns
		h.ServeHTTP(&betweenWrites{ResponseWriter: rw, at: at, interrupted: c.interrupted}, r)
	}))
	s.Config.ConnContext = mcpapi.ConnContext
	s.Config.ErrorLog = log.New(io.Discard, "", 0)
	ln := &keyUpdateListener{Listener: s.Listener, accepted: make(chan *keyUpdateConn, 1)}
	s.Listener = ln
	s.StartTLS()
	t.Cleanup(s.Close)
	raw, e := net.Dial("tcp", s.Listener.Addr().String())
	if e != nil {
		t.Fatal(e)
	}
	defer raw.Close()
	cr := &countTLSRecords{Conn: raw}
	var keylog bytes.Buffer
	tc := tls.Client(cr, &tls.Config{InsecureSkipVerify: true, NextProtos: []string{"http/1.1"}, MinVersion: tls.VersionTLS13, MaxVersion: tls.VersionTLS13, KeyLogWriter: &keylog}) //nolint:gosec // a test's own server
	if e := tc.HandshakeContext(context.Background()); e != nil {
		t.Fatal(e)
	}
	if tc.ConnectionState().CipherSuite != tls.TLS_AES_128_GCM_SHA256 {
		t.Fatal("probe needs AES128/SHA256")
	}
	cr.count = true
	c := <-ln.accepted
	conns <- c
	var resume sync.Once
	unpause := func() { resume.Do(func() { close(c.resume) }) }
	t.Cleanup(unpause)
	r := toolCall(1, "read_message", `{"id":"mbx_A.msg_1"}`).http()
	r.URL.Host = s.Listener.Addr().String()
	r.RequestURI = ""
	if e := r.Write(tc); e != nil {
		t.Fatal(e)
	}
	select {
	case <-at: // the lease started; the handler is between two writes
	case <-time.After(5 * time.Second):
		t.Fatal("the handler never wrote")
	}
	var secret []byte
	for _, line := range strings.Split(keylog.String(), "\n") {
		p := strings.Fields(line)
		if len(p) == 3 && p[0] == "CLIENT_TRAFFIC_SECRET_0" {
			secret, e = hex.DecodeString(p[2])
			if e != nil {
				t.Fatal(e)
			}
		}
	}
	if len(secret) != 32 {
		t.Fatal("missing test client's traffic secret")
	}
	key := reviewTLS13Expand(t, secret, "key", 16)
	iv := reviewTLS13Expand(t, secret, "iv", 12)
	for i := range 8 {
		iv[len(iv)-1-i] ^= byte(cr.seq >> (8 * i))
	}
	block, e := aes.NewCipher(key)
	if e != nil {
		t.Fatal(e)
	}
	aead, e := cipher.NewGCM(block)
	if e != nil {
		t.Fatal(e)
	}
	// TLS 1.3 KeyUpdate(request_update=1), encrypted under the current client
	// traffic secret and next record number. Only this test client's own key
	// is used; neither side of the server nor any library source is altered.
	// HTTP/1.1's background read takes it while the handler runs.
	msg := []byte{24, 0, 0, 1, 1, 22}
	hdr := []byte{23, 3, 3, 0, byte(len(msg) + aead.Overhead())}
	record := append(hdr, aead.Seal(nil, iv, msg, hdr)...)
	c.armed.Store(true)
	if _, e := raw.Write(record); e != nil {
		t.Fatal(e)
	}
	select {
	case <-c.paused:
	case <-time.After(5 * time.Second):
		t.Fatal("server did not reply to KeyUpdate")
	}
	l := w.Leases()[0]
	l.Cut()
	select {
	case <-finished:
	case <-time.After(3 * time.Second):
		t.Error("lease still held three seconds after cut: the KeyUpdate's write holds tls.Conn.Close")
	}
	unpause()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("lease did not finish after peer resumed")
	}
	select {
	case <-handlerDone:
	case <-time.After(5 * time.Second):
		t.Fatal("handler did not finish")
	}
	if n, args := l.Finishes(); n != 1 || args[0] {
		t.Fatalf("finished %d %v", n, args)
	}
}
