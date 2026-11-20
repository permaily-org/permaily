// Package enclave holds the networking glue for running inside a Nitro
// Enclave: the enclave has no network interface, only vsock. Inbound traffic
// arrives on a vsock listener (forwarded by permaily-host) and outbound
// traffic leaves through an HTTP CONNECT proxy that permaily-host exposes on
// vsock. TLS is always end-to-end from inside the enclave, so the host only
// sees ciphertext and the destination hostname.
package enclave

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"io"
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

// WebhookConnectBytesMax bounds the vendor egress's answer to a webhook
// sender's CONNECT.
const WebhookConnectBytesMax = 1 << 10

// errEgressRefused is a vendor egress that did not open the tunnel.
var errEgressRefused = errors.New("enclave: the vendor egress did not open the tunnel")

// WebhookEgressDialer returns the dial of webhooks' sender (ADR 0039,
// "Calling a webhook"; internal/webhook/send): a tunnelled stream to a
// host's port 443 through the host's vendor egress on vsock port proxyPort
// (8004), the CONNECT VendorEgressClient's transport sends, and never the
// allowlisted proxy. The sender frames its own TLS and HTTP/2 over it. The
// host is a lower-case DNS name (webhook.URL's); the CONNECT carries no
// credential, and its answer is read a byte at a time, so nothing past it
// is taken from the tunnel, to at most WebhookConnectBytesMax: the tunnel
// is refused unless its status is 200. ctx bounds the dial and the CONNECT.
func WebhookEgressDialer(proxyPort uint32) func(ctx context.Context, host string) (net.Conn, error) {
	return func(ctx context.Context, host string) (net.Conn, error) {
		if !dnsName(host) {
			return nil, errEgressRefused
		}
		conn, err := dialHost(ctx, proxyPort)
		if err != nil {
			return nil, err
		}
		stop := context.AfterFunc(ctx, func() { _ = conn.SetDeadline(time.Unix(1, 0)) })
		if d, ok := ctx.Deadline(); ok {
			_ = conn.SetDeadline(d)
		}
		target := net.JoinHostPort(host, "443")
		_, err = io.WriteString(conn, "CONNECT "+target+" HTTP/1.1\r\nHost: "+target+"\r\n\r\n")
		if err == nil {
			err = connected(conn)
		}
		if !stop() || err != nil {
			_ = conn.Close()
			if err == nil {
				err = context.Cause(ctx)
			}
			return nil, err
		}
		_ = conn.SetDeadline(time.Time{})
		return conn, nil
	}
}

// connected reads the answer to a CONNECT, a byte at a time to its blank
// line and no further: nil when its status is 200.
func connected(r io.Reader) error {
	var answer []byte
	b := make([]byte, 1)
	for !bytes.HasSuffix(answer, []byte("\r\n\r\n")) {
		if len(answer) == WebhookConnectBytesMax {
			return errEgressRefused
		}
		if _, err := io.ReadFull(r, b); err != nil {
			return err
		}
		answer = append(answer, b[0])
	}
	if !bytes.HasPrefix(answer, []byte("HTTP/1.1 200 ")) && !bytes.HasPrefix(answer, []byte("HTTP/1.0 200 ")) {
		return errEgressRefused
	}
	return nil
}

// dnsName is a lower-case DNS name of letters, digits, dots and hyphens.
func dnsName(h string) bool {
	if h == "" || len(h) > 253 {
		return false
	}
	for i := 0; i < len(h); i++ {
		c := h[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' && c != '.' {
			return false
		}
	}
	return true
}
