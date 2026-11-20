package send

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"io"
	"math/big"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"

	"github.com/permaily-org/permaily/internal/webhook"
)

// host is the played vendor's host, with its certificate.
const host = "trip-app.com"

// pki makes a certificate authority and a leaf it signed for names.
func pki(t *testing.T, names ...string) (*x509.CertPool, tls.Certificate) {
	t.Helper()
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	ca := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "played roots"},
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	caCert, _ := x509.ParseCertificate(caDER)
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leaf := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: names[0]}, DNSNames: names,
		NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, leaf, caCert, &key.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(caCert)
	return pool, tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}
}

// tracked is a connection a test's dialer gave, which says whether the
// sender closed it.
type tracked struct {
	net.Conn
	closed atomic.Bool
}

func (c *tracked) Close() error {
	c.closed.Store(true)
	return c.Conn.Close()
}

// dialer is a sender's dial that records the hosts asked for and the
// connections it gave.
type dialer struct {
	connect func(ctx context.Context) (net.Conn, error)

	mu    sync.Mutex
	hosts []string
	conns []*tracked
}

func (d *dialer) dial(ctx context.Context, h string) (net.Conn, error) {
	d.mu.Lock()
	d.hosts = append(d.hosts, h)
	d.mu.Unlock()
	c, err := d.connect(ctx)
	if err != nil {
		return nil, err
	}
	tc := &tracked{Conn: c}
	d.mu.Lock()
	d.conns = append(d.conns, tc)
	d.mu.Unlock()
	return tc, nil
}

// allClosed fails the test when a connection is left open after a call.
func (d *dialer) allClosed(t *testing.T) {
	t.Helper()
	d.mu.Lock()
	defer d.mu.Unlock()
	for i, c := range d.conns {
		if !c.closed.Load() {
			t.Errorf("connection %d left open after the call", i)
		}
	}
}

func (d *dialer) dials() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.hosts)
}

// tcpDialer dials addr, whatever host is asked for.
func tcpDialer(addr string) *dialer {
	return &dialer{connect: func(ctx context.Context) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", addr)
	}}
}

// tcpServe serves every connection to a listener of the loopback with
// serve (TCP buffers what a pipe would not, so both ends may write at once,
// as in a failed handshake), and dials it whatever the host.
func tcpServe(t *testing.T, serve func(net.Conn)) *dialer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { defer c.Close(); serve(c) }()
		}
	}()
	return tcpDialer(ln.Addr().String())
}

// pipeDialer gives the sender one end of a pipe and serves the other end,
// each dial in its own goroutine; wait waits for every serve to return.
type pipes struct {
	*dialer
	wg sync.WaitGroup
}

func pipeDialer(serve func(net.Conn)) *pipes {
	p := &pipes{}
	p.dialer = &dialer{connect: func(context.Context) (net.Conn, error) {
		client, server := net.Pipe()
		p.wg.Add(1)
		go func() {
			defer p.wg.Done()
			defer server.Close()
			serve(server)
		}()
		return client, nil
	}}
	return p
}

// tlsServer is the receiver's TLS over raw: the played vendor's
// certificate, ALPN h2 alone, at most version (0 for TLS 1.3).
func tlsServer(raw net.Conn, cert tls.Certificate, version uint16) *tls.Conn {
	return tls.Server(raw, &tls.Config{Certificates: []tls.Certificate{cert}, NextProtos: []string{"h2"}, MaxVersion: version})
}

// clientStart reads the client's preface and SETTINGS, the first record the
// sender writes, and answers what it read.
func clientStart(srv *tls.Conn) ([]http2.Setting, error) {
	preface := make([]byte, len(http2.ClientPreface))
	if _, err := io.ReadFull(srv, preface); err != nil {
		return nil, err
	}
	if string(preface) != http2.ClientPreface {
		return nil, io.ErrUnexpectedEOF
	}
	f, err := http2.NewFramer(nil, srv).ReadFrame()
	if err != nil {
		return nil, err
	}
	sf, ok := f.(*http2.SettingsFrame)
	if !ok {
		return nil, io.ErrUnexpectedEOF
	}
	var s []http2.Setting
	_ = sf.ForeachSetting(func(x http2.Setting) error { s = append(s, x); return nil })
	return s, nil
}

// serverSettings writes the server's SETTINGS.
func serverSettings(w io.Writer, s ...http2.Setting) error {
	return http2.NewFramer(w, nil).WriteSettings(s...)
}

// ackFrame is the client's SETTINGS acknowledgement as it is written.
var ackFrame = []byte{0, 0, 0, byte(http2.FrameSettings), byte(http2.FlagSettingsAck), 0, 0, 0, 0}

// readAll reads what the receiver's TLS opens until the connection ends.
func readAll(srv *tls.Conn) []byte {
	var got bytes.Buffer
	buf := make([]byte, 64<<10)
	for {
		n, err := srv.Read(buf)
		got.Write(buf[:n])
		if err != nil {
			return got.Bytes()
		}
	}
}

// answerFrames is a HEADERS frame on the call's stream holding fields.
func answerFrames(fields ...hpack.HeaderField) []byte {
	var block, out bytes.Buffer
	enc := hpack.NewEncoder(&block)
	for _, f := range fields {
		_ = enc.WriteField(f)
	}
	_ = http2.NewFramer(&out, nil).WriteHeaders(http2.HeadersFrameParam{StreamID: 1, BlockFragment: block.Bytes(), EndHeaders: true, EndStream: true})
	return out.Bytes()
}

func status(code string) []byte { return answerFrames(hpack.HeaderField{Name: ":status", Value: code}) }

// aNotice is a body of a notice's size: what the sender carries is opaque to
// it.
func aNotice(size int) []byte {
	b := []byte(`{"v":1,"pad":"` + strings.Repeat("p", size) + `"}`)
	return b[len(b)-size:]
}

// aCall is a call of the played vendor's URL, written by five seconds from
// now.
func aCall(body []byte) webhook.Call {
	return webhook.Call{
		URL: "https://" + host + "/hooks/permaily", ID: "ntc_0123456789abcdefghijklmnop", Timestamp: "1795161787",
		Signature: "v2a," + strings.Repeat("A", 86) + "==", Body: body, WriteBy: time.Now().Add(5 * time.Second),
	}
}

// largestNotice is the largest call the design allows: a URL of
// webhook.URLBytesMax, a body of webhook.NoticeBytesMax, two signatures
// (a rotation).
func largestNotice() webhook.Call {
	c := aCall(aNotice(webhook.NoticeBytesMax))
	prefix := "https://" + host + "/"
	c.URL = prefix + strings.Repeat("h", webhook.URLBytesMax-len(prefix))
	sig := "v2a," + strings.Repeat("B", 86) + "=="
	c.Signature = sig + " " + sig
	return c
}

// framedAt is a call whose framed request is exactly size bytes: its
// signatures padded with a character HPACK's Huffman code does not shorten,
// so each one adds a byte.
func framedAt(t *testing.T, size int) webhook.Call {
	t.Helper()
	c := aCall(aNotice(webhook.NoticeBytesMax))
	base := c.Signature
	c.Signature = base + " v2a,"
	guess := size - framedLen(c)
	for pad := guess - 8; pad <= guess+8; pad++ {
		c.Signature = base + " v2a," + strings.Repeat("X", max(pad, 1))
		if framedLen(c) == size {
			return c
		}
	}
	t.Fatalf("no call frames to %d bytes", size)
	return c
}

// framedLen is what c frames to, whatever the bound.
func framedLen(c webhook.Call) int {
	var block bytes.Buffer
	enc := hpack.NewEncoder(&block)
	enc.SetMaxDynamicTableSizeLimit(0)
	for _, f := range []hpack.HeaderField{
		{Name: ":method", Value: "POST"}, {Name: ":scheme", Value: "https"}, {Name: ":authority", Value: host},
		{Name: ":path", Value: "/hooks/permaily"}, {Name: "content-type", Value: "application/json"},
		{Name: "content-length", Value: "2048"}, {Name: "webhook-id", Value: c.ID},
		{Name: "webhook-timestamp", Value: c.Timestamp}, {Name: "webhook-signature", Value: c.Signature},
	} {
		_ = enc.WriteField(f)
	}
	return 9 + block.Len() + 9 + len(c.Body)
}

// frameOf is a frame's header, read from b.
func frameOf(b []byte) (typ http2.FrameType, flags http2.Flags, length int) {
	return http2.FrameType(b[3]), http2.Flags(b[4]), int(binary.BigEndian.Uint32(append([]byte{0}, b[:3]...)))
}
