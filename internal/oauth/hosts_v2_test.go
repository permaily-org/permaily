package oauth

import (
	"net/url"
	"strings"
	"testing"
)

// V2: an https callback whose host a browser would read differently from how
// it is written (percent-encoded Unicode that IDNA maps to localhost or to a
// loopback address, ideographic full stops, an invalid A-label, an IPv6 zone)
// is refused; a percent-encoded host is refused outright, the A-label form
// being the one to register (the second review of pull request 1).
func TestV2AnHTTPSCallbackHostIsReadAsWritten(t *testing.T) {
	cases := []struct {
		uri  string
		want bool
	}{
		{"https://%EF%BC%91%EF%BC%92%EF%BC%97.0.0.1/cb", false},
		{"https://%EF%BD%8C%EF%BD%8F%EF%BD%83%EF%BD%81%EF%BD%8C%EF%BD%88%EF%BD%8F%EF%BD%93%EF%BD%94/cb", false},
		{"https://localho%C5%BFt/cb", false},
		{"https://127%E3%80%820%E3%80%820%E3%80%821/cb", false},
		{"https://localhost%E3%80%82/cb", false},
		{"https://xn--a.com/cb", false},
		{"https://xn--localhost-/cb", false},
		{"https://[::1%25eth0]/cb", false},
		{"https://[::%25eth0]/cb", false},
		{"https://[fe80::1%25eth0]/cb", false},
		{"https://[2001:db8::1%25eth0]/cb", false},
		{"https://[2001:DB8:0:0:0:0:0:1]/cb", true},
		{"https://[::ffff:127.0.0.1]/cb", false},
		{"https://[::ffff:0.0.0.0]/cb", false},
		{"https://[::ffff:203.0.113.7]/cb", true},
		{"https://[2001:db8::1]:65535/cb", true},
		{"https://[2001:db8::1]:65536/cb", false},
		{"https://[2001:db8::1]:/cb", false},
		{"https://[2001:db8::1]:0443/cb", false},
		{"https://[::1]/cb", false},
		{"https://LOCALHOST/cb", false},
		{"https://VENDOR.EXAMPLE.COM/cb", true},
		{"https://XN--BCHER-KVA.example/cb", true},
		{"https://xn--bcher-kva.example/cb", true},
		{"https://b%C3%BCcher.example/cb", false},
		{"https://bücher.example/cb", false},
	}
	for _, c := range cases {
		t.Run(c.uri, func(t *testing.T) {
			d, err := Document(codeID, doc(map[string]any{"redirect_uris": []string{c.uri}}))
			parsed, _ := url.Parse(c.uri)
			t.Logf("parsed-host=%q document=%v allowed=%v", parsed.Hostname(), err, RedirectAllowed([]string{c.uri}, c.uri))
			if err == nil {
				q := request()
				q.Set("redirect_uri", c.uri)
				p, proven, authErr := Authorize(q, d, config)
				t.Logf("proven=%v authorization=%v", proven, authErr)
				if p != nil {
					t.Logf("callback=%s", Redirect(p.RedirectURI, url.Values{"code": {"pmc_probe"}}, config.Issuer))
				}
			}
			if (err == nil) != c.want {
				t.Errorf("document accepted=%v, want=%v", err == nil, c.want)
			}
		})
	}
	for _, pair := range [][2]string{
		{"https://VENDOR.EXAMPLE.COM/cb", "https://vendor.example.com/cb"},
		{"https://XN--BCHER-KVA.example/cb", "https://xn--bcher-kva.example/cb"},
		{"https://[2001:db8::1]/cb", "https://[2001:DB8:0:0:0:0:0:1]/cb"},
	} {
		if RedirectAllowed([]string{pair[0]}, pair[1]) {
			t.Errorf("exact matching widened: %q", pair)
		}
	}
}

// V2: the labels of an https callback host, one by one.
func TestV2HostLabels(t *testing.T) {
	for l, want := range map[string]bool{
		"vendor": true, "a-b": true, "a1": true, "xn--bcher-kva": true,
		strings.Repeat("a", 63): true, strings.Repeat("a", 64): false,
		"-a": false, "a-": false, "a_b": false, "xn--a": false, "xn--localhost-": false,
		"xn--": false, "xn--abc": false, "xn--zca": true, "xn--ss-": false,
	} {
		if got := hostLabel(l); got != want {
			t.Errorf("hostLabel(%q) = %v, want %v", l, got, want)
		}
	}
}
