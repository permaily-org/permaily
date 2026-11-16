package hostproxy

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/permaily-org/permaily/internal/vendors"
)

// PublicProxy is the vendor egress (ADR 0030, "The egress for vendors'
// documents"): a second CONNECT proxy, on its own vsock port (8004), through
// which the enclave fetches vendors' client metadata documents and key sets
// from hosts nobody listed. It tunnels only to "<name>:443" where name is a
// DNS name, never an IP literal; resolves it once; drops every address that
// is loopback, private, link-local (the instance metadata service), CGNAT,
// multicast, unspecified or otherwise reserved; and dials the first address
// left by number, so nothing can rebind the name between the filter and the
// dial. The exact allowlist of ConnectProxy (port 8000) is not this.
//
// That check is an honest host's: TLS, in the enclave, authenticates the
// vendor's name, not where the peer is (ADR 0030, finding 19). It logs fixed
// kinds only, and a vendor's domain only when it is on the verified list,
// else "other": never a target, a denied one included (finding 12).
type PublicProxy struct {
	// Resolve answers a name's addresses; nil is net.DefaultResolver.
	Resolve func(ctx context.Context, host string) ([]netip.Addr, error)
	// Dial connects to "<address>:443"; nil is a net.Dialer of DialTimeout.
	Dial func(ctx context.Context, network, addr string) (net.Conn, error)
	Log  *slog.Logger
}

// DialTimeout bounds resolving and connecting to a vendor.
const DialTimeout = 10 * time.Second

// The reasons a tunnel is refused or fails, the only words logged of it.
const (
	RefusedMethod  = "method"            // not CONNECT
	RefusedPort    = "port"              // a port other than 443
	RefusedName    = "not_a_name"        // an IP literal, or not a DNS name
	RefusedPrivate = "no_public_address" // every address it resolved to is not public
	FailedResolve  = "resolve"           // the name did not resolve
	FailedDial     = "dial"              // the address did not answer
	FailedHijack   = "hijack"            // the enclave's connection could not be taken over
)

// ServeHTTP implements http.Handler.
func (p *PublicProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodConnect {
		p.refuse(w, RefusedMethod, http.StatusMethodNotAllowed)
		return
	}
	host, port, err := net.SplitHostPort(r.Host)
	if err != nil || port != "443" {
		p.refuse(w, RefusedPort, http.StatusForbidden)
		return
	}
	name, ok := publicName(host)
	if !ok {
		p.refuse(w, RefusedName, http.StatusForbidden)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), DialTimeout)
	defer cancel()
	resolve := p.Resolve
	if resolve == nil {
		resolve = func(ctx context.Context, host string) ([]netip.Addr, error) {
			return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		}
	}
	addrs, err := resolve(ctx, name)
	if err != nil {
		p.failed(w, FailedResolve, http.StatusBadGateway)
		return
	}
	addr, ok := firstPublic(addrs)
	if !ok {
		p.refuse(w, RefusedPrivate, http.StatusForbidden)
		return
	}
	dial := p.Dial
	if dial == nil {
		dial = (&net.Dialer{Timeout: DialTimeout}).DialContext
	}
	upstream, err := dial(ctx, "tcp", netip.AddrPortFrom(addr, 443).String())
	if err != nil {
		p.failed(w, FailedDial, http.StatusBadGateway)
		return
	}
	hj, ok := w.(http.Hijacker)
	if !ok {
		upstream.Close()
		p.failed(w, FailedHijack, http.StatusInternalServerError)
		return
	}
	client, buf, err := hj.Hijack()
	if err != nil {
		upstream.Close()
		p.log().Warn("vendor egress: failed", "reason", FailedHijack)
		return
	}
	if _, err := client.Write([]byte("HTTP/1.1 200 Connection established\r\n\r\n")); err != nil {
		client.Close()
		upstream.Close()
		return
	}
	// Bytes the enclave sent right after the CONNECT request (its ClientHello).
	if n := buf.Reader.Buffered(); n > 0 {
		early, _ := buf.Peek(n)
		if _, err := upstream.Write(early); err != nil {
			client.Close()
			upstream.Close()
			return
		}
	}
	p.log().Info("vendor egress: tunnel", "vendor", vendorOf(name))
	go func() {
		defer client.Close()
		defer upstream.Close()
		pipe(client, upstream)
	}()
}

func (p *PublicProxy) log() *slog.Logger {
	if p.Log == nil {
		return slog.New(slog.DiscardHandler)
	}
	return p.Log
}

func (p *PublicProxy) refuse(w http.ResponseWriter, reason string, status int) {
	p.log().Warn("vendor egress: refused", "reason", reason)
	http.Error(w, "target not allowed", status)
}

func (p *PublicProxy) failed(w http.ResponseWriter, reason string, status int) {
	p.log().Warn("vendor egress: failed", "reason", reason)
	http.Error(w, "upstream unreachable", status)
}

// vendorOf is how a log line names a vendor: its domain when the image's
// list has it, else "other" (a self-hosted document's host can name a
// person).
func vendorOf(name string) string {
	if _, ok := vendors.Verified(name); ok {
		return name
	}
	return "other"
}

// publicName is host, lower case, when it is a DNS name of two labels or
// more in letters, digits and hyphens: never an IP literal in any spelling
// (127.1, 0x7f.1 and 2130706433 end in a numeric label), never with a
// trailing dot.
func publicName(host string) (string, bool) {
	if _, err := netip.ParseAddr(host); err == nil {
		return "", false
	}
	name := strings.ToLower(host)
	if len(name) > 253 {
		return "", false
	}
	labels := strings.Split(name, ".")
	if len(labels) < 2 {
		return "", false
	}
	for _, l := range labels {
		if l == "" || len(l) > 63 || l[0] == '-' || l[len(l)-1] == '-' {
			return "", false
		}
		for i := 0; i < len(l); i++ {
			c := l[i]
			if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
				return "", false
			}
		}
	}
	last := labels[len(labels)-1]
	if strings.HasPrefix(last, "0x") || strings.Trim(last, "0123456789") == "" {
		return "", false
	}
	return name, true
}

// firstPublic is the first of addrs that is public.
func firstPublic(addrs []netip.Addr) (netip.Addr, bool) {
	for _, a := range addrs {
		if Public(a) {
			return a.Unmap(), true
		}
	}
	return netip.Addr{}, false
}

// notPublic are the ranges that are not the public internet beyond what
// netip's predicates say: shared address space (CGNAT), IETF and
// documentation ranges, benchmarking, 6to4 relays, the reserved class E and
// broadcast; in IPv6 the translation and transition prefixes that embed an
// IPv4 address the host does not check, discard-only, documentation and
// the deprecated site-local.
var notPublic = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
	netip.MustParsePrefix("100::/64"),
	netip.MustParsePrefix("2001::/23"),
	netip.MustParsePrefix("2001:db8::/32"),
	netip.MustParsePrefix("2002::/16"),
	netip.MustParsePrefix("fec0::/10"),
}

// Public says a is an address of the public internet the vendor egress may
// dial: not loopback, private (RFC 1918, ULA), link-local (169.254.0.0/16
// and fe80::/10, which hold the instance metadata service), CGNAT,
// multicast, unspecified or reserved. An IPv4-mapped IPv6 address is
// judged as its IPv4 address.
func Public(a netip.Addr) bool {
	a = a.Unmap()
	if !a.IsValid() || a.Zone() != "" || a.IsLoopback() || a.IsPrivate() || a.IsUnspecified() ||
		a.IsLinkLocalUnicast() || a.IsMulticast() || a.IsLinkLocalMulticast() || a.IsInterfaceLocalMulticast() {
		return false
	}
	for _, p := range notPublic {
		if p.Contains(a) {
			return false
		}
	}
	return true
}
