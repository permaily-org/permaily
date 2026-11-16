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
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/mcpapi"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"
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
	return c.Conn.SetReadDeadline(d)
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

func TestV50ATLSKeyUpdateCannotHoldACut(t *testing.T) {
	w := newWorld()
	finished := make(chan struct{})
	handlerDone := make(chan struct{})
	h := w.handler()
	s := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { defer close(handlerDone); h.ServeHTTP(w, r) }))
	s.EnableHTTP2 = true
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
	tc := tls.Client(cr, &tls.Config{InsecureSkipVerify: true, NextProtos: []string{"h2"}, MinVersion: tls.VersionTLS13, MaxVersion: tls.VersionTLS13, KeyLogWriter: &keylog})
	if e := tc.HandshakeContext(context.Background()); e != nil {
		t.Fatal(e)
	}
	if tc.ConnectionState().CipherSuite != tls.TLS_AES_128_GCM_SHA256 {
		t.Fatal("probe needs AES128/SHA256")
	}
	cr.count = true
	c := <-ln.accepted
	var resume sync.Once
	unpause := func() { resume.Do(func() { close(c.resume) }) }
	t.Cleanup(unpause)
	// A real HTTP/2 client can stop DATA with its stream receive window,
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
	r := toolCall(1, "read_message", `{"id":"mbx_A.msg_1"}`).http()
	body, _ := io.ReadAll(r.Body)
	for _, h := range []hpack.HeaderField{{Name: ":method", Value: "POST"}, {Name: ":scheme", Value: "https"}, {Name: ":authority", Value: "mcp.permaily.test"}, {Name: ":path", Value: "/mcp"}} {
		if e := enc.WriteField(h); e != nil {
			t.Fatal(e)
		}
	}
	for k, vs := range r.Header {
		for _, v := range vs {
			if e := enc.WriteField(hpack.HeaderField{Name: strings.ToLower(k), Value: v}); e != nil {
				t.Fatal(e)
			}
		}
	}
	if e := f.WriteHeaders(http2.HeadersFrameParam{StreamID: 1, BlockFragment: headers.Bytes(), EndHeaders: true}); e != nil {
		t.Fatal(e)
	}
	if e := f.WriteData(1, true, body); e != nil {
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
	l := w.Leases()[0]
	l.onFinish = func() { close(finished) }
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
	for i := 0; i < 8; i++ {
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
	start := time.Now()
	l.Cut()
	select {
	case <-finished:
		t.Log("lease finished promptly")
	case <-time.After(6 * time.Second):
		dump := make([]byte, 128<<10)
		n := runtime.Stack(dump, true)
		_ = os.WriteFile("/review/tls-abort-stacks.log", dump[:n], 0600)
		t.Error("lease still held six seconds after cut: TLS KeyUpdate write prevents Conn.Close and the stream deadline is not reached")
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
	t.Logf("cut completed only after %v; Finish=%v", time.Since(start), func() int { n, _ := l.Finishes(); return n }())
}
