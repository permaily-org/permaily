package httpapi_test

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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/httpapi"
)

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

// countTLSRecords counts the application data records the client sends, for
// the next record's nonce.
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
				panic("a record split across writes")
			}
			if b[0] == 23 {
				c.seq++
			}
			b = b[n:]
		}
	}
	return c.Conn.Write(p)
}

func tls13Expand(t *testing.T, secret []byte, label string, n int) []byte {
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

// V45 (#135's 8d34b32, for the API, over HTTP/1.1 since the second review):
// a peer that asks the server for a TLS KeyUpdate and reads nothing holds
// TLS's output lock with the server's reply blocked on the socket. Between
// two chunks no write of TLS is in flight, so tls.Conn.Close sends its
// close_notify and waits for that lock: a cut that only closed the
// connection would wait as long as the peer reads nothing. The cut sets the
// socket's write deadline first: the lease is finished promptly.
func TestV45ATLSKeyUpdateCannotHoldACut(t *testing.T) {
	lease := newNotifiedLease()
	handlerDone := make(chan struct{})
	h := attachmentAPI(make([]byte, contracts.VendorAttachmentMax), lease, io.Discard)
	ln := &keyUpdateListener{accepted: make(chan *keyUpdateConn, 1)}
	conns := make(chan *keyUpdateConn, 1)
	at := make(chan struct{})
	s := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(handlerDone)
		c := <-conns
		h.ServeHTTP(&betweenWrites{ResponseWriter: w, at: at, interrupted: c.interrupted}, r)
	}))
	s.Config.ConnContext = httpapi.ConnContext
	s.Config.ErrorLog = log.New(io.Discard, "", 0)
	ln.Listener = s.Listener
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
		t.Fatal("the probe needs AES-128-GCM with SHA-256")
	}
	cr.count = true
	c := <-ln.accepted
	conns <- c
	var resume sync.Once
	unpause := func() { resume.Do(func() { close(c.resume) }) }
	t.Cleanup(unpause)
	if _, e := io.WriteString(tc, "GET /v1/messages/mbx_1.1/attachments/0 HTTP/1.1\r\nHost: "+apiHost+"\r\nAuthorization: Bearer "+apiToken+"\r\n\r\n"); e != nil {
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
		t.Fatal("no client traffic secret")
	}
	key := tls13Expand(t, secret, "key", 16)
	iv := tls13Expand(t, secret, "iv", 12)
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
	// TLS 1.3 KeyUpdate (request_update=1), sealed under the client's
	// current traffic secret and next record number: only this test
	// client's own key is used; neither the server nor any library is
	// altered. HTTP/1.1's background read takes it while the handler runs.
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
		t.Fatal("the server did not answer the KeyUpdate")
	}
	lease.Cut()
	select {
	case <-lease.finished:
	case <-time.After(3 * time.Second):
		t.Error("the lease still held three seconds after the cut: the KeyUpdate's write holds tls.Conn.Close")
	}
	unpause()
	select {
	case <-lease.finished:
	case <-time.After(5 * time.Second):
		t.Fatal("the lease was not finished after the peer read again")
	}
	select {
	case <-handlerDone:
	case <-time.After(5 * time.Second):
		t.Fatal("the handler did not return")
	}
	lease.finishedUnsent(t)
}
