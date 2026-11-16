package cimd

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/hostproxy"
	"github.com/permaily-org/permaily/internal/oauth"
	"github.com/permaily-org/permaily/internal/vendors"
)

const clientURL = "https://app.vendor.dev/oauth/client.json"

func reason(err error) Reason {
	var e *Error
	if errors.As(err, &e) {
		return e.Reason
	}
	return ""
}

// V13 (ADR 0030): a fetch is a bodiless GET of the URL exactly, over
// HTTP/2, with Accept: application/json and no cookie, credential, Referer
// or Accept-Encoding; the document is read and checked.
func TestV13AFetchIsABodilessGETWithNothingOfTheEnclaves(t *testing.T) {
	p := newPlayed(t, documents("Cache-Control", "max-age=300"))
	c, err := p.r.Client(context.Background(), clientURL)
	if err != nil {
		t.Fatal(err)
	}
	if c.ID != clientURL || c.Domain != "app.vendor.dev" || c.Document.ClientName != "Played" || !c.FetchedAt.Equal(p.clk.now()) {
		t.Fatalf("client %+v", c)
	}
	got := p.v.requests()
	if len(got) != 1 {
		t.Fatalf("%d requests", len(got))
	}
	r := got[0]
	if r.Method != http.MethodGet || r.Path != "/oauth/client.json" || r.Proto != "HTTP/2.0" || r.ContentLength > 0 {
		t.Errorf("request %+v", r)
	}
	if r.Header.Get("Accept") != "application/json" {
		t.Errorf("Accept %q", r.Header.Get("Accept"))
	}
	for _, h := range []string{"Cookie", "Authorization", "Proxy-Authorization", "Referer", "Accept-Encoding", "If-None-Match", "If-Modified-Since"} {
		if v, ok := r.Header[h]; ok {
			t.Errorf("%s sent: %q", h, v)
		}
	}
}

// V13: the five documents of 2026-11-16, served by played vendors under
// their own names, resolve; ChatGPT's key set too.
func TestV13TheVendorsDocumentsResolve(t *testing.T) {
	dir := filepath.Join("..", "..", "oauth", "testdata", "vendors")
	files := map[string]string{
		"/oauth/claude-code-client-metadata": "cimd-claude-code.json",
		"/oauth/mcp-oauth-client-metadata":   "cimd-claude-ai.json",
		"/oauth/codex/client.json":           "cimd-codex.json",
		"/oauth/client.json":                 "cimd-chatgpt-connector.json",
		"/oauth/chatgpt/client.json":         "cimd-chatgpt-apps.json",
		"/oauth/jwks.json":                   "jwks-chatgpt.json",
	}
	serve := func(w http.ResponseWriter, r *http.Request) {
		b, err := os.ReadFile(filepath.Join(dir, files[r.URL.Path]))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=300")
		_, _ = w.Write(b)
	}
	p := newPlayed(t, serve, "claude.ai", "chatgpt.com")
	for _, id := range []string{
		"https://claude.ai/oauth/claude-code-client-metadata",
		"https://claude.ai/oauth/mcp-oauth-client-metadata",
		"https://chatgpt.com/oauth/codex/client.json",
		"https://chatgpt.com/oauth/client.json",
		"https://chatgpt.com/oauth/chatgpt/client.json",
	} {
		c, err := p.r.Client(context.Background(), id)
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		if _, ok := vendors.Verified(c.Domain); !ok {
			t.Errorf("%s: domain %q", id, c.Domain)
		}
		if c.Document.TokenEndpointAuthMethod == oauth.AuthPrivateKeyJWT {
			keys, err := p.r.Keys(context.Background(), c, false)
			if err != nil || len(keys) != 1 || keys[0].ID != "cimd-20260428030119" || keys[0].Algorithm != "RS256" {
				t.Fatalf("%s keys: %v %v", id, keys, err)
			}
		}
	}
	// One fetch of the key set for both documents that name it.
	n := 0
	for _, r := range p.v.requests() {
		if r.Path == "/oauth/jwks.json" {
			n++
		}
	}
	if n != 1 {
		t.Errorf("the key set was fetched %d times", n)
	}
}

// V13: through the host's vendor egress (hostproxy.PublicProxy), as in the
// enclave: CONNECT to the name on 443, the tunnel to the public address by
// number, TLS to the name and HTTP/2 inside it.
func TestV13ThroughTheVendorEgress(t *testing.T) {
	ca := newPKI(t)
	v := newVendor(t, ca, documents("Cache-Control", "max-age=300"), "app.vendor.dev")
	var dialed []string
	var mu sync.Mutex
	proxy := httptest.NewServer(&hostproxy.PublicProxy{
		Resolve: func(context.Context, string) ([]netip.Addr, error) {
			return []netip.Addr{netip.MustParseAddr("10.0.0.1"), netip.MustParseAddr("93.184.216.34")}, nil
		},
		Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
			mu.Lock()
			dialed = append(dialed, addr)
			mu.Unlock()
			return (&net.Dialer{}).DialContext(ctx, network, v.srv.Listener.Addr().String())
		},
		Log: slog.New(slog.DiscardHandler),
	})
	defer proxy.Close()
	proxyURL, _ := url.Parse(proxy.URL)
	hc := &http.Client{Transport: &http.Transport{
		Proxy:           http.ProxyURL(proxyURL),
		TLSClientConfig: &tls.Config{RootCAs: ca.pool, MinVersion: tls.VersionTLS12},
	}}
	r := New(hc, deployment, newClock().now)
	if _, err := r.Client(context.Background(), clientURL); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(dialed) != 1 || dialed[0] != "93.184.216.34:443" {
		t.Fatalf("dialed %v", dialed)
	}
	if got := v.requests(); len(got) != 1 || got[0].Proto != "HTTP/2.0" {
		t.Fatalf("requests %+v", got)
	}
}

// V13: a 3xx is never followed, wherever it points.
func TestV13ARedirectIsNotFollowed(t *testing.T) {
	target := newPlayed(t, documents(), "app.vendor.dev")
	for _, loc := range []string{
		"http://169.254.169.254/latest/meta-data/iam/security-credentials/",
		"https://localhost/oauth/client.json",
		"https://127.0.0.1/oauth/client.json",
		"https://app.vendor.dev/oauth/other.json",
		target.v.srv.URL + "/oauth/client.json",
	} {
		for _, code := range []int{http.StatusMovedPermanently, http.StatusFound, http.StatusSeeOther, http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
			p := newPlayed(t, func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, loc, code)
			})
			_, err := p.r.Client(context.Background(), clientURL)
			if !errors.Is(err, vendors.ErrUnreachable) || reason(err) != ReasonStatus {
				t.Errorf("%d to %s: %v", code, loc, err)
			}
			if n := p.v.count(); n != 1 {
				t.Errorf("%d to %s: %d requests to the vendor", code, loc, n)
			}
		}
	}
	if n := target.v.count(); n != 0 {
		t.Errorf("a redirect's target was reached %d times", n)
	}
}

// padded is document(id) with spaces inside it, n bytes in all.
func padded(id string, n int) []byte {
	d := document(id)
	return append(append(d[:len(d)-1:len(d)-1], bytes.Repeat([]byte(" "), n-len(d))...), '}')
}

func gzipped(b []byte) []byte {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, _ = zw.Write(b)
	_ = zw.Close()
	return buf.Bytes()
}

// V13: at most 8 KiB of a document after any content decoding: 8 KiB is
// read, 8 KiB and one byte refused, plain or decoded from a gzip answer
// nobody asked for; another content coding is unreachable.
func TestV13ADocumentIsAtMost8KiBAfterDecoding(t *testing.T) {
	const limit = oauth.DocumentMaxBytes
	cases := []struct {
		name   string
		body   []byte
		header []string
		kind   error
	}{
		{"8 KiB", padded(clientURL, limit), nil, nil},
		{"8 KiB and one byte", padded(clientURL, limit+1), nil, vendors.ErrDocument},
		{"8 KiB gzipped", gzipped(padded(clientURL, limit)), []string{"Content-Encoding", "gzip"}, nil},
		{"8 KiB and one byte gzipped", gzipped(padded(clientURL, limit+1)), []string{"Content-Encoding", "gzip"}, vendors.ErrDocument},
		{"a megabyte gzipped", gzipped(padded(clientURL, 1<<20)), []string{"Content-Encoding", "gzip"}, vendors.ErrDocument},
		{"gzip that is not", []byte("not gzip at all"), []string{"Content-Encoding", "gzip"}, vendors.ErrUnreachable},
		{"gzip cut short", gzipped(padded(clientURL, limit))[:100], []string{"Content-Encoding", "gzip"}, vendors.ErrUnreachable},
		{"brotli", padded(clientURL, 1000), []string{"Content-Encoding", "br"}, vendors.ErrUnreachable},
	}
	for _, c := range cases {
		p := newPlayed(t, answer(c.body, c.header...))
		_, err := p.r.Client(context.Background(), clientURL)
		if c.kind == nil && err != nil || c.kind != nil && !errors.Is(err, c.kind) {
			t.Errorf("%s: %v", c.name, err)
		}
	}
	// A length announced past the bound is refused before the body is read.
	p := newPlayed(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Length", "1000000")
		_, _ = w.Write(padded(clientURL, 1000000))
	})
	if _, err := p.r.Client(context.Background(), clientURL); !errors.Is(err, vendors.ErrDocument) || reason(err) != ReasonTooLarge {
		t.Errorf("announced: %v", err)
	}
}

// V13: a 200 whose body is not a JSON object is unreachable; one the rules
// refuse is a document refused; any status but 200 is unreachable.
func TestV13OnlyAJSONObjectIsRead(t *testing.T) {
	for _, body := range []string{"", "[]", `"client"`, "null", "42", "{", `{"client_id":`, "<html></html>", "\xef\xbb\xbf{}"} {
		p := newPlayed(t, answer([]byte(body)))
		if _, err := p.r.Client(context.Background(), clientURL); !errors.Is(err, vendors.ErrUnreachable) || reason(err) != ReasonNotObject {
			t.Errorf("%q: %v", body, err)
		}
	}
	for _, body := range []string{"{}", `{"client_id":"https://app.vendor.dev/oauth/client.json/"}`, string(document("https://app.vendor.dev/oauth/other.json"))} {
		p := newPlayed(t, answer([]byte(body)))
		if _, err := p.r.Client(context.Background(), clientURL); !errors.Is(err, vendors.ErrDocument) || reason(err) != ReasonRules {
			t.Errorf("%q: %v", body, err)
		}
	}
	for _, code := range []int{http.StatusNoContent, http.StatusNotFound, http.StatusInternalServerError, http.StatusServiceUnavailable} {
		p := newPlayed(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(code)
			_, _ = w.Write(document(clientURL))
		})
		if _, err := p.r.Client(context.Background(), clientURL); !errors.Is(err, vendors.ErrUnreachable) {
			t.Errorf("%d: %v", code, err)
		}
	}
}

// V13: a client_id the rules refuse is never fetched.
func TestV13AClientIDTheRulesRefuseIsNotFetched(t *testing.T) {
	p := newPlayed(t, documents())
	for _, id := range []string{
		"http://app.vendor.dev/oauth/client.json",
		"https://app.vendor.dev:443/oauth/client.json",
		"https://app.vendor.dev/",
		"https://user@app.vendor.dev/oauth/client.json",
		"https://app.vendor.dev/oauth/client.json?x=1",
		"https://app.vendor.dev/oauth/client.json#x",
		"https://app.vendor.dev/oauth/../client.json",
		"https://app.vendor.dev/oauth%2Fclient.json",
		"https://127.0.0.1/oauth/client.json",
		"https://[::1]/oauth/client.json",
		"https://127.1/oauth/client.json",
		"https://0x7f.1/oauth/client.json",
		"https://2130706433/oauth/client.json",
		"https://localhost/oauth/client.json",
		"https://vendor/oauth/client.json",
		"https://app.permaily.com/oauth/client.json",
		"https://permaily.com/oauth/client.json",
		"https://printer.local/client.json",
		"https://metadata.internal/client.json",
		"https://bücher.example.org/client.json",
		"https://" + strings.Repeat("a", 500) + ".dev/client.json",
		"",
	} {
		if _, err := p.r.Client(context.Background(), id); !errors.Is(err, vendors.ErrClientID) {
			t.Errorf("%q: %v", id, err)
		}
	}
	if n := p.v.count(); n != 0 {
		t.Fatalf("%d fetches", n)
	}
}

// V13: slow headers and a slow body are cut at the fetch's bound (5
// seconds; shortened here), as unreachable.
func TestV13ASlowVendorIsCut(t *testing.T) {
	if FetchTimeout != 5*time.Second || New(nil, deployment, time.Now).hc.Timeout != 5*time.Second {
		t.Fatal("the fetch's bound is not 5 seconds")
	}
	release := make(chan struct{})
	defer close(release)
	slowHeaders := func(w http.ResponseWriter, _ *http.Request) {
		select {
		case <-release:
		case <-time.After(10 * time.Second):
		}
	}
	slowBody := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"client_id":`))
		w.(http.Flusher).Flush()
		select {
		case <-release:
		case <-time.After(10 * time.Second):
		}
	}
	for name, serve := range map[string]func(http.ResponseWriter, *http.Request){"headers": slowHeaders, "body": slowBody} {
		p := newPlayed(t, serve)
		p.r.timeout = 300 * time.Millisecond
		start := time.Now()
		_, err := p.r.Client(context.Background(), clientURL)
		if !errors.Is(err, vendors.ErrUnreachable) || reason(err) != ReasonTransport {
			t.Errorf("%s: %v", name, err)
		}
		if d := time.Since(start); d > 3*time.Second {
			t.Errorf("%s: cut after %v", name, d)
		}
	}
	// A caller that gives up is answered at once.
	p := newPlayed(t, slowHeaders)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := p.r.Client(ctx, clientURL); !errors.Is(err, vendors.ErrUnreachable) {
		t.Errorf("caller gave up: %v", err)
	}
}

// V13: at most vendors.FetchesInFlight (8) fetches at once, and one of one
// URL however many ask for it.
func TestV13AtMost8FetchesAtOnceAndOnePerURL(t *testing.T) {
	var inFlight, most atomic.Int32
	gate := make(chan struct{})
	p := newPlayed(t, func(w http.ResponseWriter, r *http.Request) {
		n := inFlight.Add(1)
		for {
			m := most.Load()
			if n <= m || most.CompareAndSwap(m, n) {
				break
			}
		}
		<-gate
		inFlight.Add(-1)
		documents("Cache-Control", "max-age=300")(w, r)
	})
	var wg sync.WaitGroup
	errs := make(chan error, 40)
	for i := range 20 {
		for range 2 {
			wg.Go(func() {
				_, err := p.r.Client(context.Background(), "https://app.vendor.dev/oauth/client-"+string(rune('a'+i))+".json")
				errs <- err
			})
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for inFlight.Load() < vendors.FetchesInFlight && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	time.Sleep(100 * time.Millisecond)
	if n := inFlight.Load(); n != vendors.FetchesInFlight {
		t.Errorf("%d fetches at once, want %d", n, vendors.FetchesInFlight)
	}
	close(gate)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Error(err)
		}
	}
	if m := most.Load(); m != vendors.FetchesInFlight {
		t.Errorf("at most %d at once, want %d", m, vendors.FetchesInFlight)
	}
	if n := p.v.count(); n != 20 {
		t.Errorf("%d fetches for 20 URLs asked twice each at once", n)
	}
}

// Harden leaves a transport that is not an *http.Transport as it is, and a
// client given is not changed.
func TestHardenCopies(t *testing.T) {
	rt := roundTripper(func(*http.Request) (*http.Response, error) { return nil, errors.New("x") })
	if _, ok := Harden(rt).(roundTripper); !ok {
		t.Error("a round tripper of another kind was replaced")
	}
	tr := &http.Transport{}
	hc := &http.Client{Transport: tr}
	New(hc, deployment, time.Now)
	if hc.Transport != tr || tr.DisableCompression || tr.Protocols != nil || hc.CheckRedirect != nil || hc.Timeout != 0 {
		t.Error("the client given was changed")
	}
	if h, ok := Harden(nil).(*http.Transport); !ok || !h.DisableCompression || h.Protocols.HTTP1() {
		t.Error("the default transport is not hardened")
	}
}

type roundTripper func(*http.Request) (*http.Response, error)

func (f roundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// V13: a vendor's headers are at most 16 KiB.
func TestV13HeadersAreAtMost16KiB(t *testing.T) {
	for size, ok := range map[int]bool{8 << 10: true, 20 << 10: false, 64 << 10: false} {
		p := newPlayed(t, documents("Cache-Control", "max-age=300", "X-Padding", strings.Repeat("a", size)))
		_, err := p.r.Client(context.Background(), clientURL)
		if ok && err != nil || !ok && !errors.Is(err, vendors.ErrUnreachable) {
			t.Errorf("%d bytes of headers: %v", size, err)
		}
	}
}
