package httpapi_test

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/httpapi"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/oauth"
	"github.com/permaily-org/permaily/internal/services/connections"
	"github.com/permaily-org/permaily/internal/services/connections/connectionstest"
)

// A cut while a write of the answer is blocked in the connection, as #135
// found for MCP and fixed in internal/mcpapi (its release.go), mirrored here:
// row 6 may not touch internal/mcpapi (ADR 0030, "Implementation pull
// requests").

// notifiedLease is countedLease, closing finished on its first Finish.
type notifiedLease struct {
	*countedLease
	finished chan struct{}
	once     sync.Once
}

func newNotifiedLease() *notifiedLease {
	return &notifiedLease{countedLease: newLease(), finished: make(chan struct{})}
}

func (l *notifiedLease) Finish(sent bool) {
	l.countedLease.Finish(sent)
	l.once.Do(func() { close(l.finished) })
}

// lockedBuffer is a log's buffer written by a server's goroutines.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

// attachmentAPI is the API whose attachment is data under lease.
func attachmentAPI(data []byte, lease connections.Release, logs io.Writer) http.Handler {
	fake := &connectionstest.Fake{}
	grants(fake, apiGrant)
	fake.AttachmentFunc = func(context.Context, *oauth.Grant, string, int) (*inbox.Attachment, connections.Release, error) {
		return &inbox.Attachment{Data: data}, lease, nil
	}
	return httpapi.VendorAPI(httpapi.ConnectionsConfig{
		Service: fake, OAuth: testOAuth, Challenge: testChallenge, Log: slog.New(slog.NewJSONHandler(logs, nil)),
	})
}

// V45 (#135's finding, for the API): a lease cut while a write of the answer
// is blocked (a peer that reads nothing) sends no byte more on the wire,
// over HTTP/2 as over HTTP/1.1, both with TLS, as the enclave serves them:
// the connection's write is interrupted, not only the stream's, so a DATA
// frame already handed to it never leaves when the peer reads again. The
// lease is finished once, promptly and not sent, and the request is logged
// as cut.
func TestV45ACutDuringABlockedWriteSendsNoByteAfterItOverHTTP2(t *testing.T) {
	for _, h2 := range []bool{true, false} {
		t.Run(map[bool]string{true: "http2", false: "http1"}[h2], func(t *testing.T) {
			data := make([]byte, contracts.VendorAttachmentMax)
			_, _ = rand.Read(data)
			lease := newNotifiedLease()
			var logs lockedBuffer
			api := attachmentAPI(data, lease, &logs)
			handled := make(chan struct{})
			srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(handled)
				api.ServeHTTP(w, r)
			}))
			srv.EnableHTTP2 = h2
			srv.Config.ConnContext = httpapi.ConnContext
			srv.Config.ErrorLog = log.New(io.Discard, "", 0)
			ln := &pauseListener{Listener: srv.Listener, left: 256 << 10, paused: make(chan struct{}), resume: make(chan struct{})}
			srv.Listener = ln
			srv.StartTLS()
			t.Cleanup(srv.Close)
			t.Cleanup(ln.unpause) // first: a failure never leaves a write blocked

			done := make(chan struct{})
			var n int64
			var proto int
			var readErr error
			go func() {
				defer close(done)
				r, _ := http.NewRequest("GET", srv.URL+"/v1/messages/mbx_1.1/attachments/0", nil)
				r.Header.Set("Authorization", "Bearer "+apiToken)
				resp, err := srv.Client().Do(r)
				if err != nil {
					readErr = err
					return
				}
				defer resp.Body.Close()
				proto = resp.ProtoMajor
				n, readErr = io.Copy(io.Discard, resp.Body)
			}()
			select {
			case <-ln.paused:
			case <-time.After(10 * time.Second):
				t.Fatal("the transfer never blocked")
			}
			ln.cut()
			lease.Cut()
			select {
			case <-lease.finished:
			case <-time.After(2 * time.Second):
				t.Error("the lease was not finished after the cut while the peer read nothing")
			}
			ln.unpause() // the peer reads again
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Fatal("the response was not aborted")
			}
			srv.Close()
			select { // an HTTP/2 handler may outlive Close
			case <-handled:
			case <-time.After(10 * time.Second):
				t.Fatal("the handler did not return")
			}
			if after := ln.afterCut(); after != 0 {
				t.Errorf("%d bytes left on the wire after the cut", after)
			}
			if readErr == nil || n >= int64(len(data)) || (proto != 0 && proto != map[bool]int{true: 2, false: 1}[h2]) {
				t.Errorf("HTTP/%d: the client read %d bytes, error %v", proto, n, readErr)
			}
			lease.finishedUnsent(t)
			if s := logs.String(); !strings.Contains(s, `"outcome":"cut"`) || strings.Contains(s, `"outcome":"ok"`) {
				t.Errorf("the request was logged %s", s)
			}
		})
	}
}

// finishedUnsent says Finish was called once, unsent.
func (l *notifiedLease) finishedUnsent(t *testing.T) {
	t.Helper()
	l.countedLease.finished(t, false)
}

// V45 (#135's finding, for the API): over HTTP/2 without the connection's
// abort (the server's ConnContext is not httpapi.ConnContext), a leased
// answer is refused before its lease starts: 503, nothing of it,
// Finish(false) once.
func TestV45HTTP2WithoutTheConnectionsAbortReleasesNoMail(t *testing.T) {
	const secret = "the bytes of an attachment"
	lease := newNotifiedLease()
	var logs lockedBuffer
	api := attachmentAPI([]byte(secret), lease, &logs)
	handled := make(chan struct{})
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(handled)
		api.ServeHTTP(w, r)
	}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	t.Cleanup(srv.Close)
	r, _ := http.NewRequest("GET", srv.URL+"/v1/messages/mbx_1.1/attachments/0", nil)
	r.Header.Set("Authorization", "Bearer "+apiToken)
	resp, err := srv.Client().Do(r)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	srv.Close()
	<-handled // its line is written when it returns
	if resp.ProtoMajor != 2 || resp.StatusCode != http.StatusServiceUnavailable || resp.Header.Get("Retry-After") != "5" || strings.Contains(string(body), secret) {
		t.Fatalf("HTTP/%d %d %s", resp.ProtoMajor, resp.StatusCode, body)
	}
	lease.finishedUnsent(t)
	if started, _, _, _ := lease.State(); started {
		t.Fatal("the lease was started")
	}
	if s := logs.String(); !strings.Contains(s, `"outcome":"no_abort"`) {
		t.Errorf("the request was logged %s", s)
	}
}

// lateWriter cuts the lease as the answer is flushed, and records a write
// deadline set after the handler returned.
type lateWriter struct {
	*httptest.ResponseRecorder
	lease     *countedLease
	returned  atomic.Bool
	deadlines atomic.Int32
	late      atomic.Bool
}

func (l *lateWriter) Flush() { l.lease.Cut() }

func (l *lateWriter) SetWriteDeadline(time.Time) error {
	if l.returned.Load() {
		l.late.Store(true)
	}
	l.deadlines.Add(1)
	return nil
}

// V45 (#135's finding, for the API): the abort a cut sets off acts while the
// handler runs and never after it returned, when the writer is no longer
// its own.
func TestV45TheCutNeverActsOnTheWriterAfterTheHandlerReturned(t *testing.T) {
	prev := runtime.GOMAXPROCS(1)
	defer runtime.GOMAXPROCS(prev)
	lease := newLease()
	h := attachmentAPI([]byte("x"), lease, io.Discard)
	lw := &lateWriter{ResponseRecorder: httptest.NewRecorder(), lease: lease}
	r := httptest.NewRequest("GET", "https://"+apiHost+"/v1/messages/mbx_1.1/attachments/0", nil)
	r.Header.Set("Authorization", "Bearer "+apiToken)
	func() {
		defer func() {
			if v := recover(); v != nil {
				if err, _ := v.(error); !errors.Is(err, http.ErrAbortHandler) {
					panic(v)
				}
			}
		}()
		h.ServeHTTP(lw, r)
	}()
	lw.returned.Store(true)
	for end := time.Now().Add(time.Second); lw.deadlines.Load() == 0 && time.Now().Before(end); {
		runtime.Gosched()
	}
	if lw.deadlines.Load() == 0 || lw.late.Load() {
		t.Fatalf("deadlines %d, one after the handler returned: %v", lw.deadlines.Load(), lw.late.Load())
	}
}

// pauseListener's connections write until left bytes have gone, then block
// the next large write as a peer that reads nothing does, until the write
// is interrupted (a write deadline that passed, or Close) or the peer reads
// again (unpause); it counts what reaches the wire after cut.
type pauseListener struct {
	net.Listener
	paused, resume chan struct{}

	mu          sync.Mutex
	left        int
	isCut       bool
	after       int
	pauseOnce   sync.Once
	unpauseOnce sync.Once
}

func (l *pauseListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return &pauseConn{Conn: c, l: l, interrupted: make(chan struct{})}, nil
}

func (l *pauseListener) unpause() { l.unpauseOnce.Do(func() { close(l.resume) }) }

func (l *pauseListener) cut() {
	l.mu.Lock()
	l.isCut = true
	l.mu.Unlock()
}

func (l *pauseListener) afterCut() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.after
}

type pauseConn struct {
	net.Conn
	l           *pauseListener
	interrupted chan struct{}
	stop        sync.Once
}

func (c *pauseConn) interrupt() { c.stop.Do(func() { close(c.interrupted) }) }

func (c *pauseConn) Write(p []byte) (int, error) {
	c.l.mu.Lock()
	pause := c.l.left <= 0 && len(p) > 1024
	c.l.left -= len(p)
	c.l.mu.Unlock()
	if pause {
		c.l.pauseOnce.Do(func() { close(c.l.paused) })
		select {
		case <-c.l.resume:
		case <-c.interrupted:
			return 0, os.ErrDeadlineExceeded
		}
	}
	select {
	case <-c.interrupted:
		return 0, os.ErrDeadlineExceeded
	default:
	}
	n, err := c.Conn.Write(p)
	c.l.mu.Lock()
	if c.l.isCut {
		c.l.after += n
	}
	c.l.mu.Unlock()
	return n, err
}

func (c *pauseConn) SetWriteDeadline(t time.Time) error {
	if !t.IsZero() && !t.After(time.Now()) {
		c.interrupt()
	}
	return c.Conn.SetWriteDeadline(t)
}

func (c *pauseConn) SetDeadline(t time.Time) error {
	if err := c.SetWriteDeadline(t); err != nil {
		return err
	}
	return c.SetReadDeadline(t)
}

func (c *pauseConn) Close() error {
	c.interrupt()
	return c.Conn.Close()
}

// keyUpdateConn pauses one wire write once armed, with the same
// unblock-on-close and unblock-on-expired-deadline semantics as net.Conn. It
// never holds TLS's locks itself; any such lock is the unmodified Go TLS
// server's.
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

// V45 (#135's 8d34b32, for the API): a peer that asks the server for a TLS
// KeyUpdate and reads nothing holds TLS's output lock with the server's
// reply blocked on the socket; tls.Conn.Close waits for that lock, so a cut
// that only closed the connection would wait as long as the peer reads
// nothing. The cut sets the socket's write deadline first: the lease is
// finished promptly.
func TestV45ATLSKeyUpdateCannotHoldACut(t *testing.T) {
	lease := newNotifiedLease()
	handlerDone := make(chan struct{})
	data := make([]byte, contracts.VendorAttachmentMax)
	h := attachmentAPI(data, lease, io.Discard)
	s := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { defer close(handlerDone); h.ServeHTTP(w, r) }))
	s.EnableHTTP2 = true
	s.Config.ConnContext = httpapi.ConnContext
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
	tc := tls.Client(cr, &tls.Config{InsecureSkipVerify: true, NextProtos: []string{"h2"}, MinVersion: tls.VersionTLS13, MaxVersion: tls.VersionTLS13, KeyLogWriter: &keylog}) //nolint:gosec // a test's own server
	if e := tc.HandshakeContext(context.Background()); e != nil {
		t.Fatal(e)
	}
	if tc.ConnectionState().CipherSuite != tls.TLS_AES_128_GCM_SHA256 {
		t.Fatal("the probe needs AES-128-GCM with SHA-256")
	}
	cr.count = true
	c := <-ln.accepted
	var resume sync.Once
	unpause := func() { resume.Do(func() { close(c.resume) }) }
	t.Cleanup(unpause)
	// A real HTTP/2 client can stop DATA with its stream's receive window,
	// leaving the TLS writer idle while the leased answer waits to flush.
	if _, e := tc.Write([]byte(http2.ClientPreface)); e != nil {
		t.Fatal(e)
	}
	f := http2.NewFramer(tc, tc)
	if e := f.WriteSettings(http2.Setting{ID: http2.SettingInitialWindowSize, Val: 0}); e != nil {
		t.Fatal(e)
	}
	var headers bytes.Buffer
	enc := hpack.NewEncoder(&headers)
	for _, h := range []hpack.HeaderField{
		{Name: ":method", Value: "GET"}, {Name: ":scheme", Value: "https"}, {Name: ":authority", Value: apiHost},
		{Name: ":path", Value: "/v1/messages/mbx_1.1/attachments/0"}, {Name: "authorization", Value: "Bearer " + apiToken},
	} {
		if e := enc.WriteField(h); e != nil {
			t.Fatal(e)
		}
	}
	if e := f.WriteHeaders(http2.HeadersFrameParam{StreamID: 1, BlockFragment: headers.Bytes(), EndHeaders: true, EndStream: true}); e != nil {
		t.Fatal(e)
	}
	_ = raw.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		frame, e := f.ReadFrame()
		if e != nil {
			t.Fatal(e)
		}
		if sf, ok := frame.(*http2.SettingsFrame); ok && !sf.IsAck() {
			if e := f.WriteSettingsAck(); e != nil {
				t.Fatal(e)
			}
		}
		if hf, ok := frame.(*http2.HeadersFrame); ok && hf.StreamID == 1 {
			break
		}
	}
	_ = raw.SetReadDeadline(time.Time{})
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
	// altered.
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
