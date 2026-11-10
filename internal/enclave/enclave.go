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

// EgressClient returns an HTTP client whose connections go through the host's
// CONNECT proxy on vsock port proxyPort. Certificates are verified with the
// Go fallback roots compiled into the binary (the enclave has no /etc/ssl).
func EgressClient(proxyPort uint32) *http.Client {
	tr := &http.Transport{
		// The URL is only a marker: DialContext ignores the address and always
		// dials the host proxy over vsock. Go sends CONNECT for https targets.
		Proxy: http.ProxyURL(&url.URL{Scheme: "http", Host: "egress.vsock:80"}),
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return vsock.DialContext(ctx, HostCID, proxyPort, nil)
		},
		TLSClientConfig:     &tls.Config{MinVersion: tls.VersionTLS12},
		ForceAttemptHTTP2:   true,
		MaxIdleConns:        10,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 15 * time.Second,
	}
	return &http.Client{Transport: tr, Timeout: 60 * time.Second}
}
