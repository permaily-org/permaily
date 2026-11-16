package enclave

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/url"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/vendors"
	"github.com/permaily-org/permaily/internal/vendors/cimd"
)

// recordDials replaces dialHost with one that records the port asked for
// and fails, for the length of the test.
func recordDials(t *testing.T) func() []uint32 {
	t.Helper()
	var mu sync.Mutex
	var ports []uint32
	old := dialHost
	dialHost = func(_ context.Context, port uint32) (net.Conn, error) {
		mu.Lock()
		defer mu.Unlock()
		ports = append(ports, port)
		return nil, errors.New("no host in a test")
	}
	t.Cleanup(func() { dialHost = old })
	return func() []uint32 {
		mu.Lock()
		defer mu.Unlock()
		return append([]uint32(nil), ports...)
	}
}

// V17 (ADR 0030): the vendor egress client is not the egress client: the
// resolver's transport dials the vendor egress (8004), and the egress
// client dials the allowlisted proxy (8000), whatever the URL.
func TestV17TheVendorEgressClientDialsItsOwnPort(t *testing.T) {
	ports := recordDials(t)
	ctx := context.Background()

	resolver := cimd.New(VendorEgressClient(8004), "permaily.com", time.Now)
	if _, err := resolver.Client(ctx, "https://claude.ai/oauth/claude-code-client-metadata"); !errors.Is(err, vendors.ErrUnreachable) {
		t.Fatalf("resolving without a host: %v", err)
	}
	if got := ports(); len(got) == 0 || slices.ContainsFunc(got, func(p uint32) bool { return p != 8004 }) {
		t.Fatalf("the resolver dialed %v, want only 8004", got)
	}

	before := len(ports())
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "https://kms.us-east-2.amazonaws.com/", nil)
	if resp, err := EgressClient(8000).Do(req); err == nil {
		resp.Body.Close()
		t.Fatal("the egress client reached a host in a test")
	}
	if got := ports()[before:]; len(got) == 0 || slices.ContainsFunc(got, func(p uint32) bool { return p != 8000 }) {
		t.Fatalf("the egress client dialed %v, want only 8000", got)
	}
}

// V13, V18 (ADR 0030): what the vendor egress client is: HTTP/2 only and
// refusing any other protocol at the handshake, TLS 1.2 or later, no
// redirect, no cookie jar, no proxy credentials, no compression asked,
// bounded headers and 5 seconds in all.
func TestVendorEgressClientIsHTTP2OnlyAndBounded(t *testing.T) {
	c := VendorEgressClient(8004)
	tr, ok := c.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport %T", c.Transport)
	}
	if c.Timeout != 5*time.Second || c.Jar != nil {
		t.Errorf("timeout %v, jar %v", c.Timeout, c.Jar)
	}
	if err := c.CheckRedirect(nil, nil); !errors.Is(err, http.ErrUseLastResponse) {
		t.Errorf("a redirect is followed: %v", err)
	}
	if tr.Protocols == nil || !tr.Protocols.HTTP2() || tr.Protocols.HTTP1() || tr.Protocols.UnencryptedHTTP2() {
		t.Errorf("protocols %v", tr.Protocols)
	}
	if !tr.DisableCompression || tr.MaxResponseHeaderBytes != 16<<10 || tr.ProxyConnectHeader != nil || tr.GetProxyConnectHeader != nil {
		t.Errorf("compression disabled %v, header bytes %d, proxy headers %v", tr.DisableCompression, tr.MaxResponseHeaderBytes, tr.ProxyConnectHeader)
	}
	proxy, err := tr.Proxy(&http.Request{URL: &url.URL{Scheme: "https", Host: "claude.ai"}})
	if err != nil || proxy.User != nil {
		t.Errorf("proxy %v, %v", proxy, err)
	}
	cfg := tr.TLSClientConfig
	if cfg.MinVersion != tls.VersionTLS12 || cfg.InsecureSkipVerify || !slices.Equal(cfg.NextProtos, []string{"h2"}) {
		t.Errorf("TLS %+v", cfg)
	}
	for _, p := range []string{"", "http/1.1"} {
		if cfg.VerifyConnection(tls.ConnectionState{NegotiatedProtocol: p}) == nil {
			t.Errorf("a handshake that negotiated %q is accepted", p)
		}
	}
	if err := cfg.VerifyConnection(tls.ConnectionState{NegotiatedProtocol: "h2"}); err != nil {
		t.Errorf("h2 refused: %v", err)
	}
}
