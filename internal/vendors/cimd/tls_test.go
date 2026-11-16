package cimd

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"io"
	"log"
	"log/slog"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"

	"github.com/permaily-org/permaily/internal/vendors"
)

// rawPeer is a played vendor that speaks TLS (or not) and does what serve
// says with each connection, counting the bytes of application data it
// read after its handshake.
type rawPeer struct {
	ln       net.Listener
	mu       sync.Mutex
	appBytes int
	conns    int
}

func newRawPeer(t *testing.T, cfg *tls.Config, serve func(c net.Conn, p *rawPeer)) *rawPeer {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &rawPeer{ln: ln}
	var wg sync.WaitGroup
	t.Cleanup(func() { ln.Close(); wg.Wait() })
	wg.Go(func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			p.mu.Lock()
			p.conns++
			p.mu.Unlock()
			wg.Go(func() {
				defer c.Close()
				_ = c.SetDeadline(time.Now().Add(1500 * time.Millisecond))
				if cfg == nil {
					serve(c, p)
					return
				}
				tc := tls.Server(c, cfg)
				if err := tc.Handshake(); err != nil {
					return
				}
				serve(tc, p)
			})
		}
	})
	return p
}

// readAll counts what the peer reads after its handshake.
func readAll(c net.Conn, p *rawPeer) {
	n, _ := io.Copy(io.Discard, c)
	p.mu.Lock()
	p.appBytes += int(n)
	p.mu.Unlock()
}

func (p *rawPeer) read() (int, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.appBytes, p.conns
}

// V18 (ADR 0030): a played vendor whose certificate is for another name,
// self-signed or expired is unreachable, and no request byte after the
// ClientHello reaches it; one that offers only HTTP/1.1, or no protocol,
// is unreachable too, refused at the handshake.
func TestV18TLSFailuresAreUnreachableAndSendNothing(t *testing.T) {
	ca := newPKI(t)
	now := time.Now()
	certs := map[string]tls.Certificate{
		"another name": ca.valid(t, "other.vendor.dev"),
		"self-signed":  ca.leaf(t, []string{"app.vendor.dev"}, now.Add(-time.Hour), now.Add(time.Hour), true),
		"expired":      ca.leaf(t, []string{"app.vendor.dev"}, now.Add(-48*time.Hour), now.Add(-24*time.Hour), false),
		"not yet":      ca.leaf(t, []string{"app.vendor.dev"}, now.Add(24*time.Hour), now.Add(48*time.Hour), false),
	}
	for name, cert := range certs {
		for _, maxVersion := range []uint16{tls.VersionTLS12, tls.VersionTLS13} {
			peer := newRawPeer(t, &tls.Config{Certificates: []tls.Certificate{cert}, NextProtos: []string{"h2"}, MaxVersion: maxVersion}, readAll)
			r := New(clientTo(peer.ln.Addr().String(), ca), deployment, newClock().now)
			_, err := r.Client(context.Background(), clientURL)
			if !errors.Is(err, vendors.ErrUnreachable) || reason(err) != ReasonTransport {
				t.Errorf("%s (TLS %x): %v", name, maxVersion, err)
			}
			time.Sleep(20 * time.Millisecond)
			if n, conns := peer.read(); n != 0 || conns == 0 {
				t.Errorf("%s (TLS %x): %d bytes after the handshake over %d connections", name, maxVersion, n, conns)
			}
		}
	}
	good := ca.valid(t, "app.vendor.dev")
	for name, protos := range map[string][]string{"HTTP/1.1 only": {"http/1.1"}, "no protocol": nil} {
		peer := newRawPeer(t, &tls.Config{Certificates: []tls.Certificate{good}, NextProtos: protos}, readAll)
		r := New(clientTo(peer.ln.Addr().String(), ca), deployment, newClock().now)
		if _, err := r.Client(context.Background(), clientURL); !errors.Is(err, vendors.ErrUnreachable) {
			t.Errorf("%s: %v", name, err)
		}
		time.Sleep(20 * time.Millisecond)
		if n, conns := peer.read(); n != 0 || conns == 0 {
			t.Errorf("%s: %d bytes after the handshake over %d connections", name, n, conns)
		}
	}
	// TLS 1.1 is refused.
	peer := newRawPeer(t, &tls.Config{Certificates: []tls.Certificate{good}, NextProtos: []string{"h2"}, MaxVersion: tls.VersionTLS11}, readAll)
	r := New(clientTo(peer.ln.Addr().String(), ca), deployment, newClock().now)
	if _, err := r.Client(context.Background(), clientURL); !errors.Is(err, vendors.ErrUnreachable) {
		t.Errorf("TLS 1.1: %v", err)
	}
}

// secret is what a hostile vendor writes, which no log may hold.
const secret = "SECRET-ana.garcia@vendor-bytes"

// h2Answer speaks just enough HTTP/2 to answer one request with headers
// (pairs) and body, then does after.
func h2Answer(fields []hpack.HeaderField, body []byte, after func(c net.Conn)) func(c net.Conn, p *rawPeer) {
	return func(c net.Conn, _ *rawPeer) {
		preface := make([]byte, len(http2.ClientPreface))
		if _, err := io.ReadFull(c, preface); err != nil {
			return
		}
		fr := http2.NewFramer(c, c)
		_ = fr.WriteSettings()
		var stream uint32
		for stream == 0 {
			f, err := fr.ReadFrame()
			if err != nil {
				return
			}
			switch f := f.(type) {
			case *http2.SettingsFrame:
				if !f.IsAck() {
					_ = fr.WriteSettingsAck()
				}
			case *http2.HeadersFrame:
				stream = f.StreamID
			}
		}
		var block bytes.Buffer
		enc := hpack.NewEncoder(&block)
		for _, h := range fields {
			_ = enc.WriteField(h)
		}
		_ = fr.WriteHeaders(http2.HeadersFrameParam{StreamID: stream, BlockFragment: block.Bytes(), EndHeaders: true, EndStream: body == nil})
		if body != nil {
			_ = fr.WriteData(stream, true, body)
		}
		if after != nil {
			after(c)
		}
		_, _ = io.Copy(io.Discard, c)
	}
}

// captureLogs sends the process's loggers (log and slog's default) to a
// buffer for the length of the test.
func captureLogs(t *testing.T) func() string {
	t.Helper()
	var mu sync.Mutex
	var buf bytes.Buffer
	w := writerFunc(func(p []byte) (int, error) {
		mu.Lock()
		defer mu.Unlock()
		return buf.Write(p)
	})
	oldOut, oldFlags, oldDefault := log.Writer(), log.Flags(), slog.Default()
	log.SetOutput(w)
	slog.SetDefault(slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() {
		log.SetOutput(oldOut)
		log.SetFlags(oldFlags)
		slog.SetDefault(oldDefault)
	})
	return func() string {
		mu.Lock()
		defer mu.Unlock()
		return buf.String()
	}
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

// V19 (ADR 0030, finding 12): a played peer that sends unsolicited bytes on
// an idle connection, malformed headers or a failing handshake writes
// nothing of them to any log, the process's logger included; and no error
// the resolver returns holds a URL, a host or a word of the answer.
func TestV19NothingAVendorSendsReachesALog(t *testing.T) {
	logs := captureLogs(t)
	ca := newPKI(t)
	h2 := &tls.Config{Certificates: []tls.Certificate{ca.valid(t, "app.vendor.dev")}, NextProtos: []string{"h2"}}
	ok := []hpack.HeaderField{{Name: ":status", Value: "200"}, {Name: "content-type", Value: "application/json"}, {Name: "cache-control", Value: "max-age=1"}}
	var (
		mu      sync.Mutex
		wg      sync.WaitGroup
		errs    []error
		idleErr error
	)
	// resolve resolves twice at once with the other peers: the second
	// time on the same resolver, after the document went stale, over the
	// connection the peer may have written to while it was idle.
	resolve := func(addr string, at time.Time, first *error) {
		wg.Go(func() {
			clk := &clock{t: at}
			r := New(clientTo(addr, ca), deployment, clk.now)
			r.timeout = 500 * time.Millisecond
			_, err1 := r.Client(context.Background(), clientURL)
			time.Sleep(100 * time.Millisecond)
			clk.add(2 * time.Second)
			_, err2 := r.Client(context.Background(), clientURL)
			mu.Lock()
			defer mu.Unlock()
			r.hc.CloseIdleConnections()
			errs = append(errs, err1, err2)
			if first != nil {
				*first = err1
			}
		})
	}

	// Unsolicited bytes on the idle connection, after a good answer: what
	// Go's HTTP/1.1 transport would log.
	idle := newRawPeer(t, h2, h2Answer(ok, document(clientURL), func(c net.Conn) {
		time.Sleep(20 * time.Millisecond)
		_, _ = io.WriteString(c, "HTTP/1.1 200 OK\r\nX-Leak: "+secret+"\r\n\r\n"+secret)
	}))
	resolve(idle.ln.Addr().String(), time.Now(), &idleErr)

	// Malformed headers: names HTTP/2 forbids, a value with a newline, no
	// :status, an unknown pseudo-header.
	for _, fields := range [][]hpack.HeaderField{
		append(ok, hpack.HeaderField{Name: "X-Upper", Value: secret}),
		append(ok, hpack.HeaderField{Name: "x-bad name", Value: secret}),
		append(ok, hpack.HeaderField{Name: "x-value", Value: secret + "\r\nx-injected: " + secret}),
		{{Name: "content-type", Value: secret}},
		append(ok, hpack.HeaderField{Name: ":" + secret, Value: secret}),
		{{Name: ":status", Value: secret}},
		append(ok, hpack.HeaderField{Name: "x-huge", Value: strings.Repeat(secret, 1000)}),
	} {
		peer := newRawPeer(t, h2, h2Answer(fields, []byte(secret), nil))
		resolve(peer.ln.Addr().String(), time.Now(), nil)
	}

	// A body that is not a document, words of it the vendor's.
	notObject := newRawPeer(t, h2, h2Answer(ok, []byte(secret), nil))
	resolve(notObject.ln.Addr().String(), time.Now(), nil)

	// Garbage frames after the preface.
	frames := newRawPeer(t, h2, func(c net.Conn, _ *rawPeer) {
		preface := make([]byte, len(http2.ClientPreface))
		_, _ = io.ReadFull(c, preface)
		_, _ = io.WriteString(c, secret+strings.Repeat("\x00", 64)+secret)
		_, _ = io.Copy(io.Discard, c)
	})
	resolve(frames.ln.Addr().String(), time.Now(), nil)

	// A failing handshake: plain text where a ServerHello should be, and an
	// alert.
	plain := newRawPeer(t, nil, func(c net.Conn, _ *rawPeer) {
		_, _ = io.WriteString(c, "HTTP/1.1 400 "+secret+"\r\n\r\n"+secret)
	})
	resolve(plain.ln.Addr().String(), time.Now(), nil)
	alert := newRawPeer(t, nil, func(c net.Conn, _ *rawPeer) {
		_, _ = c.Write([]byte{21, 3, 3, 0, 2, 2, 40})
	})
	resolve(alert.ln.Addr().String(), time.Now(), nil)

	wg.Wait()
	time.Sleep(200 * time.Millisecond)
	got := logs()
	for _, word := range []string{secret, "SECRET", "X-Leak", "x-injected", "app.vendor.dev", "/oauth/client.json"} {
		if strings.Contains(got, word) {
			t.Errorf("a log holds %q:\n%s", word, got)
		}
	}
	for _, err := range errs {
		if err == nil {
			continue
		}
		for _, word := range []string{secret, "SECRET", "vendor.dev", "oauth", "127.0.0.1", "http"} {
			if strings.Contains(err.Error(), word) {
				t.Errorf("an error holds %q: %v", word, err)
			}
		}
	}
	if idleErr != nil {
		t.Errorf("the idle peer's good answer: %v", idleErr)
	}
}
