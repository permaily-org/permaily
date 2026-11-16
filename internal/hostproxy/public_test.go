package hostproxy

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"
)

// publicAddr stands for a vendor's public address; the tests dial the echo
// server whatever the address, and check the address asked for.
var publicAddr = netip.MustParseAddr("93.184.216.34")

// playedProxy is a PublicProxy whose resolver answers answer (or fails) and
// counts its calls, and whose dial records the address and reaches echo.
type playedProxy struct {
	mu         sync.Mutex
	answers    [][]netip.Addr
	resolveErr error
	resolved   int
	dialed     []string
	dialErr    error
	logs       bytes.Buffer
	srv        *httptest.Server
}

func newPlayedProxy(t *testing.T, answers ...[]netip.Addr) *playedProxy {
	t.Helper()
	echo := echoServer(t)
	pp := &playedProxy{answers: answers}
	proxy := &PublicProxy{
		Resolve: func(_ context.Context, _ string) ([]netip.Addr, error) {
			pp.mu.Lock()
			defer pp.mu.Unlock()
			pp.resolved++
			if pp.resolveErr != nil {
				return nil, pp.resolveErr
			}
			if len(pp.answers) == 0 {
				return nil, nil
			}
			a := pp.answers[0]
			if len(pp.answers) > 1 {
				pp.answers = pp.answers[1:]
			}
			return a, nil
		},
		Dial: func(ctx context.Context, network, addr string) (net.Conn, error) {
			pp.mu.Lock()
			pp.dialed = append(pp.dialed, addr)
			err := pp.dialErr
			pp.mu.Unlock()
			if err != nil {
				return nil, err
			}
			return (&net.Dialer{}).DialContext(ctx, network, echo.Addr().String())
		},
		Log: slog.New(slog.NewJSONHandler(&lockedWriter{w: &pp.logs, mu: &pp.mu}, nil)),
	}
	pp.srv = httptest.NewServer(proxy)
	t.Cleanup(pp.srv.Close)
	return pp
}

type lockedWriter struct {
	mu *sync.Mutex
	w  *bytes.Buffer
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

func (pp *playedProxy) log() string {
	pp.mu.Lock()
	defer pp.mu.Unlock()
	return pp.logs.String()
}

func (pp *playedProxy) set(f func()) {
	pp.mu.Lock()
	defer pp.mu.Unlock()
	f()
}

func (pp *playedProxy) resolutions() int {
	pp.mu.Lock()
	defer pp.mu.Unlock()
	return pp.resolved
}

func (pp *playedProxy) dials() []string {
	pp.mu.Lock()
	defer pp.mu.Unlock()
	return append([]string(nil), pp.dialed...)
}

// connect sends "<method> target" to the proxy with "ping" right after it,
// and answers the status and, when tunnelled, what came back through it.
func (pp *playedProxy) connect(t *testing.T, method, target string) (int, string) {
	t.Helper()
	c, err := net.Dial("tcp", pp.srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	fmt.Fprintf(c, "%s %s HTTP/1.1\r\nHost: %s\r\n\r\nping\n", method, target, target)
	br := bufio.NewReader(c)
	resp, err := http.ReadResponse(br, &http.Request{Method: method})
	if err != nil {
		t.Fatalf("%s %s: %v", method, target, err)
	}
	if resp.StatusCode != http.StatusOK {
		return resp.StatusCode, ""
	}
	line, err := br.ReadString('\n')
	if err != nil {
		t.Fatalf("through the tunnel: %v", err)
	}
	return resp.StatusCode, line
}

// V16 (ADR 0030): the vendor egress tunnels a name on 443 to its public
// address, by number.
func TestV16PublicProxyTunnelsANameOn443(t *testing.T) {
	pp := newPlayedProxy(t, []netip.Addr{publicAddr})
	status, echoed := pp.connect(t, http.MethodConnect, "Vendor.Example.NET:443")
	if status != http.StatusOK || echoed != "ping\n" {
		t.Fatalf("status %d, echoed %q", status, echoed)
	}
	if d := pp.dials(); len(d) != 1 || d[0] != "93.184.216.34:443" {
		t.Fatalf("dialed %v, want the address by number on 443", d)
	}
}

// V16: refused before anything is resolved or dialed: another port, an IP
// literal in any spelling, a name that is not one, another method.
func TestV16PublicProxyRefusesWhatIsNotANameOn443(t *testing.T) {
	cases := []struct {
		method, target string
		status         int
	}{
		{http.MethodGet, "vendor.example.net:443", http.StatusMethodNotAllowed},
		{http.MethodConnect, "vendor.example.net:8443", http.StatusForbidden},
		{http.MethodConnect, "vendor.example.net:80", http.StatusForbidden},
		{http.MethodConnect, "vendor.example.net", http.StatusForbidden},
		{http.MethodConnect, "93.184.216.34:443", http.StatusForbidden},
		{http.MethodConnect, "[2606:4700::1111]:443", http.StatusForbidden},
		{http.MethodConnect, "[::ffff:127.0.0.1]:443", http.StatusForbidden},
		{http.MethodConnect, "127.1:443", http.StatusForbidden},
		{http.MethodConnect, "0x7f.1:443", http.StatusForbidden},
		{http.MethodConnect, "0x7f000001:443", http.StatusForbidden},
		{http.MethodConnect, "2130706433:443", http.StatusForbidden},
		{http.MethodConnect, "169.254.169.254:443", http.StatusForbidden},
		{http.MethodConnect, "localhost:443", http.StatusForbidden},
		{http.MethodConnect, "vendor.example.net.:443", http.StatusForbidden},
		{http.MethodConnect, "-vendor.example.net:443", http.StatusForbidden},
		{http.MethodConnect, "vendor_x.example.net:443", http.StatusForbidden},
		{http.MethodConnect, strings.Repeat("a", 64) + ".example.net:443", http.StatusForbidden},
		{http.MethodConnect, strings.Repeat("abcdefghi.", 26) + "net:443", http.StatusForbidden},
	}
	for _, c := range cases {
		pp := newPlayedProxy(t, []netip.Addr{publicAddr})
		status, _ := pp.connect(t, c.method, c.target)
		if status != c.status {
			t.Errorf("%s %s: status %d, want %d", c.method, c.target, status, c.status)
		}
		if pp.resolutions() != 0 || len(pp.dials()) != 0 {
			t.Errorf("%s %s: resolved %d times, dialed %v", c.method, c.target, pp.resolutions(), pp.dials())
		}
	}
}

// V16: a name that resolves only to addresses that are not public is
// refused, and nothing is dialed: loopback, RFC 1918, ULA, link-local (the
// instance metadata service included), CGNAT, multicast, unspecified,
// reserved, and each of them IPv4-mapped.
func TestV16PublicProxyRefusesPrivateAddresses(t *testing.T) {
	private := []string{
		"127.0.0.1", "127.255.255.254", "::1",
		"10.0.0.1", "172.16.0.1", "172.31.255.255", "192.168.1.1",
		"fd00::1", "fc00::1",
		"169.254.169.254", "169.254.0.1", "fe80::1", "fd00:ec2::254",
		"100.64.0.1", "100.127.255.254",
		"224.0.0.1", "239.255.255.250", "ff02::1", "ff05::2",
		"0.0.0.0", "0.1.2.3", "::",
		"240.0.0.1", "255.255.255.255", "192.0.2.1", "198.51.100.7", "203.0.113.9", "198.18.0.1", "192.0.0.8", "192.88.99.1",
		"64:ff9b::a9fe:a9fe", "64:ff9b:1::1", "2002:a9fe:a9fe::1", "2001::1", "2001:db8::1", "100::1", "fec0::1",
		"::ffff:127.0.0.1", "::ffff:169.254.169.254", "::ffff:10.0.0.1",
	}
	for _, s := range private {
		a := netip.MustParseAddr(s)
		if Public(a) {
			t.Errorf("%s is public", s)
		}
		pp := newPlayedProxy(t, []netip.Addr{a, a})
		if status, _ := pp.connect(t, http.MethodConnect, "vendor.example.net:443"); status != http.StatusForbidden {
			t.Errorf("%s: status %d", s, status)
		}
		if d := pp.dials(); len(d) != 0 {
			t.Errorf("%s: dialed %v", s, d)
		}
	}
	if pp := newPlayedProxy(t); true {
		if status, _ := pp.connect(t, http.MethodConnect, "vendor.example.net:443"); status != http.StatusForbidden {
			t.Errorf("no address: status %d", status)
		}
	}
	for _, s := range []string{"1.1.1.1", "8.8.8.8", "93.184.216.34", "2606:4700:4700::1111", "2a00:1450:4001:80b::200e", "::ffff:1.1.1.1"} {
		if !Public(netip.MustParseAddr(s)) {
			t.Errorf("%s is not public", s)
		}
	}
	if Public(netip.Addr{}) || Public(netip.MustParseAddr("2606:4700::1%eth0")) {
		t.Error("an invalid or zoned address is public")
	}
}

// V16 (finding 19): of a mixed answer the first public address is dialed,
// by number; the name is resolved once, so a resolver that answers a public
// address first and a private one after is never asked again.
func TestV16PublicProxyDialsTheFirstPublicAddressByNumberAndResolvesOnce(t *testing.T) {
	mixed := []netip.Addr{netip.MustParseAddr("10.0.0.7"), netip.MustParseAddr("169.254.169.254"), publicAddr, netip.MustParseAddr("1.1.1.1")}
	pp := newPlayedProxy(t, mixed)
	if status, echoed := pp.connect(t, http.MethodConnect, "vendor.example.net:443"); status != http.StatusOK || echoed != "ping\n" {
		t.Fatalf("mixed: status %d, %q", status, echoed)
	}
	if d := pp.dials(); len(d) != 1 || d[0] != "93.184.216.34:443" {
		t.Fatalf("mixed: dialed %v", d)
	}

	rebinding := newPlayedProxy(t, []netip.Addr{publicAddr}, []netip.Addr{netip.MustParseAddr("127.0.0.1")})
	if status, _ := rebinding.connect(t, http.MethodConnect, "rebind.example.net:443"); status != http.StatusOK {
		t.Fatalf("rebinding: status %d", status)
	}
	if rebinding.resolutions() != 1 {
		t.Fatalf("resolved %d times, want once", rebinding.resolutions())
	}
	if d := rebinding.dials(); len(d) != 1 || d[0] != "93.184.216.34:443" {
		t.Fatalf("rebinding: dialed %v, want the filtered number", d)
	}

	mapped := newPlayedProxy(t, []netip.Addr{netip.MustParseAddr("::ffff:93.184.216.34")})
	if status, _ := mapped.connect(t, http.MethodConnect, "vendor.example.net:443"); status != http.StatusOK {
		t.Fatalf("mapped: status %d", status)
	}
	if d := mapped.dials(); len(d) != 1 || d[0] != "93.184.216.34:443" {
		t.Fatalf("mapped: dialed %v", d)
	}
	v6 := newPlayedProxy(t, []netip.Addr{netip.MustParseAddr("2606:4700:4700::1111")})
	if status, _ := v6.connect(t, http.MethodConnect, "vendor.example.net:443"); status != http.StatusOK {
		t.Fatalf("v6: status %d", status)
	}
	if d := v6.dials(); len(d) != 1 || d[0] != "[2606:4700:4700::1111]:443" {
		t.Fatalf("v6: dialed %v", d)
	}
}

// V16: a name that does not resolve, or an address that does not answer,
// is a 502.
func TestV16PublicProxyFailures(t *testing.T) {
	pp := newPlayedProxy(t)
	pp.set(func() { pp.resolveErr = errors.New("no such host vendor.example.net") })
	if status, _ := pp.connect(t, http.MethodConnect, "vendor.example.net:443"); status != http.StatusBadGateway {
		t.Errorf("resolve: status %d", status)
	}
	pp = newPlayedProxy(t, []netip.Addr{publicAddr})
	pp.set(func() { pp.dialErr = errors.New("dial tcp 93.184.216.34:443: connection refused") })
	if status, _ := pp.connect(t, http.MethodConnect, "vendor.example.net:443"); status != http.StatusBadGateway {
		t.Errorf("dial: status %d", status)
	}
	// Without a hijacker, nothing is tunnelled.
	rec := httptest.NewRecorder()
	(&PublicProxy{
		Resolve: func(context.Context, string) ([]netip.Addr, error) { return []netip.Addr{publicAddr}, nil },
		Dial: func(context.Context, string, string) (net.Conn, error) {
			a, b := net.Pipe()
			b.Close()
			return a, nil
		},
	}).ServeHTTP(rec, httptest.NewRequest(http.MethodConnect, "vendor.example.net:443", nil))
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("no hijacker: status %d", rec.Code)
	}
}

// V19 (ADR 0030, finding 12): the host logs fixed kinds, and a vendor's
// domain only when it is on the verified list, else "other": never a
// target, a denied one included, nor what the resolver or the dial said.
func TestV19PublicProxyLogsKindsAndVerifiedDomainsOnly(t *testing.T) {
	const secret = "ana-garcia-private"
	pp := newPlayedProxy(t, []netip.Addr{publicAddr})
	if status, _ := pp.connect(t, http.MethodConnect, "claude.ai:443"); status != http.StatusOK {
		t.Fatalf("claude.ai: %d", status)
	}
	if !strings.Contains(pp.log(), `"vendor":"claude.ai"`) {
		t.Errorf("a verified vendor is not named: %s", pp.log())
	}
	for _, target := range []string{secret + ".example.net:443", secret + ".example.net:8443", "www.claude.ai.example.net:443"} {
		pp.connect(t, http.MethodConnect, target)
	}
	if !strings.Contains(pp.log(), `"vendor":"other"`) {
		t.Errorf("another vendor is not other: %s", pp.log())
	}
	private := newPlayedProxy(t, []netip.Addr{netip.MustParseAddr("10.1.2.3")})
	private.connect(t, http.MethodConnect, secret+".example.net:443")
	failing := newPlayedProxy(t)
	failing.set(func() { failing.resolveErr = errors.New("lookup " + secret + ".example.net: no such host") })
	failing.connect(t, http.MethodConnect, secret+".example.net:443")
	dialing := newPlayedProxy(t, []netip.Addr{publicAddr})
	dialing.set(func() { dialing.dialErr = errors.New("dial " + secret + ": refused") })
	dialing.connect(t, http.MethodConnect, secret+".example.net:443")
	for _, l := range []string{pp.log(), private.log(), failing.log(), dialing.log()} {
		for _, word := range []string{secret, "example.net", "93.184.216.34", "10.1.2.3", "8443", "www.claude.ai"} {
			if strings.Contains(l, word) {
				t.Errorf("the host's log holds %q: %s", word, l)
			}
		}
	}
	for _, kind := range []string{RefusedPort, RefusedPrivate, FailedResolve, FailedDial} {
		if !strings.Contains(pp.log()+private.log()+failing.log()+dialing.log(), `"reason":"`+kind+`"`) {
			t.Errorf("no line of kind %s", kind)
		}
	}
}
