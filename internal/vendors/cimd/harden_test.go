package cimd

import (
	"crypto/tls"
	"net"
	"net/http"
	"testing"
	"time"
)

// A transport with no TLS configuration is hardened like any other and
// never panics: it is given its own configuration, at TLS 1.2 or later,
// that refuses a host negotiating anything but HTTP/2. Clone fills in a
// TLS configuration only for a transport it sets HTTP/2 up on, which it
// does not for one with its own dialer (an egress's) and no
// ForceAttemptHTTP2; that one was left nil and panicked (PR #147, ruling
// 1). http.DefaultTransport (New's nil client) and a bare transport are
// checked too.
func TestHardenATransportWithoutTLSConfiguration(t *testing.T) {
	def, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		t.Fatal("http.DefaultTransport is not an *http.Transport")
	}
	dial := (&net.Dialer{}).DialContext
	for name, rt := range map[string]http.RoundTripper{
		"nil (http.DefaultTransport)": nil,
		"a bare transport":            &http.Transport{},
		"a transport with its dialer": &http.Transport{DialContext: dial},
	} {
		t.Run(name, func(t *testing.T) {
			tr, ok := Harden(rt).(*http.Transport)
			if !ok {
				t.Fatal("Harden did not give an *http.Transport")
			}
			if tr == rt || tr == def {
				t.Fatal("Harden changed the transport it was given")
			}
			c := tr.TLSClientConfig
			if c == nil || c.MinVersion < tls.VersionTLS12 || c.VerifyConnection == nil {
				t.Fatalf("the hardened transport's TLS: %+v", c)
			}
			if err := c.VerifyConnection(tls.ConnectionState{NegotiatedProtocol: "http/1.1"}); err == nil {
				t.Fatal("a host that negotiated HTTP/1.1 was accepted")
			}
			if err := c.VerifyConnection(tls.ConnectionState{NegotiatedProtocol: "h2"}); err != nil {
				t.Fatal(err)
			}
			if tr.Protocols == nil || tr.Protocols.HTTP1() || !tr.Protocols.HTTP2() || !tr.DisableCompression || tr.MaxResponseHeaderBytes != HeaderMaxBytes {
				t.Fatalf("the hardened transport: protocols %v, compression off %v, headers %d", tr.Protocols, tr.DisableCompression, tr.MaxResponseHeaderBytes)
			}
		})
	}
	for _, hc := range []*http.Client{nil, {}, {Transport: &http.Transport{}}, {Transport: &http.Transport{DialContext: dial}}} {
		if New(hc, "permaily.com", time.Now) == nil {
			t.Fatal("New gave no resolver")
		}
	}
}
