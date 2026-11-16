// Package enclave holds the networking glue for running inside a Nitro
// Enclave: the enclave has no network interface, only vsock. Inbound traffic
// arrives on a vsock listener (forwarded by permaily-host) and outbound
// traffic leaves through an HTTP CONNECT proxy that permaily-host exposes on
// vsock. TLS is always end-to-end from inside the enclave, so the host only
// sees ciphertext and the destination hostname.
package enclave

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/mdlayher/vsock"
)

// HostCID is the vsock context ID of the parent EC2 instance. In Nitro
// Enclaves the parent is always CID 3; the generic host CID (2, vsock.Host)
// resets the connection.
const HostCID = 3

// Listen listens on a vsock port inside the enclave.
func Listen(port uint32) (net.Listener, error) {
	return vsock.Listen(port, nil)
}

// dialHost connects to the parent instance on a vsock port; tests replace
// it to see which port a client dials.
var dialHost = func(ctx context.Context, port uint32) (net.Conn, error) {
	return vsock.DialContext(ctx, HostCID, port, nil)
}

// EgressClient returns an HTTP client whose connections go through the host's
// CONNECT proxy on vsock port proxyPort. Certificates are verified with the
// Go fallback roots compiled into the binary (the enclave has no /etc/ssl).
func EgressClient(proxyPort uint32) *http.Client {
	tr := &http.Transport{
		// The URL is only a marker: DialContext ignores the address and always
		// dials the host proxy over vsock. Go sends CONNECT for https targets.
		Proxy: http.ProxyURL(&url.URL{Scheme: "http", Host: "egress.vsock:80"}),
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialHost(ctx, proxyPort)
		},
		TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12},
		ForceAttemptHTTP2:   true,
		MaxIdleConns:        10,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 15 * time.Second,
	}
	return &http.Client{Transport: tr, Timeout: 60 * time.Second}
}

// VendorEgressTimeout bounds one request of the vendor egress client, from
// the connection to the last byte of the answer (ADR 0030, "Clients").
const VendorEgressTimeout = 5 * time.Second

// VendorEgressHeaderMaxBytes bounds the headers of a vendor's answer.
const VendorEgressHeaderMaxBytes = 16 << 10

// VendorEgressClient returns the client of the vendor egress (ADR 0030, "The
// egress for vendors' documents"): its connections go through the host's
// second CONNECT proxy on vsock port proxyPort (8004), which tunnels to any
// public name on 443, and it is used by internal/vendors/cimd and nothing
// else, never in place of EgressClient. HTTPS only, HTTP/2 only (ALPN h2;
// a host that negotiates anything else is refused at the handshake, the
// owner's decision 11, because Go's HTTP/1.1 transport logs what an idle
// peer sends, finding 12), TLS 1.2 or later checked against the roots
// compiled into the image, no redirect followed, no cookie jar, no proxy
// credentials, no compression asked, headers of at most
// VendorEgressHeaderMaxBytes, VendorEgressTimeout in all.
func VendorEgressClient(proxyPort uint32) *http.Client {
	tr := &http.Transport{
		// The URL is only a marker, with no credentials: DialContext always
		// dials the host's vendor egress over vsock.
		Proxy: http.ProxyURL(&url.URL{Scheme: "http", Host: "vendor-egress.vsock:80"}),
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialHost(ctx, proxyPort)
		},
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
			NextProtos: []string{"h2"},
			VerifyConnection: func(cs tls.ConnectionState) error {
				if cs.NegotiatedProtocol != "h2" {
					return errNotHTTP2
				}
				return nil
			},
		},
		Protocols:              new(http.Protocols),
		DisableCompression:     true,
		MaxResponseHeaderBytes: VendorEgressHeaderMaxBytes,
		MaxIdleConns:           vendorIdleConns,
		IdleConnTimeout:        90 * time.Second,
		TLSHandshakeTimeout:    VendorEgressTimeout,
	}
	tr.Protocols.SetHTTP2(true)
	return &http.Client{
		Transport:     tr,
		Timeout:       VendorEgressTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

// vendorIdleConns bounds the idle connections kept to vendors' hosts.
const vendorIdleConns = 16

// errNotHTTP2 refuses a vendor's connection that did not negotiate HTTP/2.
var errNotHTTP2 = errors.New("enclave: the vendor did not negotiate HTTP/2")
