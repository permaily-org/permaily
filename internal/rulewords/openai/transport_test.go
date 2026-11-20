package openai

import (
	"crypto/tls"
	"errors"
	"net/http"
	"strings"
	"testing"
)

// roundTripper is a transport that is not an *http.Transport.
type roundTripper struct{}

func (roundTripper) RoundTrip(*http.Request) (*http.Response, error) { return nil, errors.New("never") }

// TestTheWordsClientSpeaksOnlyHTTP2: the client's copy of the egress
// refuses a handshake without h2 after the given check, which still runs
// and refuses first; a transport of no TLS config of its own gets the
// check too; nil is http.DefaultTransport's copy; a transport that is not
// an *http.Transport is kept as it is; and New with no client still makes
// one that follows no redirect and keeps no cookie.
func TestTheWordsClientSpeaksOnlyHTTP2(t *testing.T) {
	refused := errors.New("refused by the given check")
	var seen int
	given := &http.Transport{TLSClientConfig: &tls.Config{VerifyConnection: func(cs tls.ConnectionState) error {
		seen++
		if cs.ServerName == "refuse" {
			return refused
		}
		return nil
	}}}
	verify := http2Only(given).(*http.Transport).TLSClientConfig.VerifyConnection
	for _, c := range []struct {
		cs   tls.ConnectionState
		want error
	}{
		{tls.ConnectionState{ServerName: "refuse", NegotiatedProtocol: "h2"}, refused},
		{tls.ConnectionState{NegotiatedProtocol: "h2"}, nil},
		{tls.ConnectionState{NegotiatedProtocol: "http/1.1"}, errNotHTTP2},
	} {
		if err := verify(c.cs); !errors.Is(err, c.want) {
			t.Errorf("%+v: %v, want %v", c.cs, err, c.want)
		}
	}
	if seen != 3 {
		t.Errorf("the given check ran %d times, want 3", seen)
	}
	if err := given.TLSClientConfig.VerifyConnection(tls.ConnectionState{}); err != nil {
		t.Error("http2Only changed the given transport's check")
	}
	if tr := http2Only(&http.Transport{}).(*http.Transport); tr.TLSClientConfig.VerifyConnection(tls.ConnectionState{}) == nil {
		t.Error("a transport with no TLS config is not refused a handshake without h2")
	}
	if tr, ok := http2Only(nil).(*http.Transport); !ok || tr == http.DefaultTransport || !tr.Protocols.HTTP2() || tr.Protocols.HTTP1() {
		t.Error("nil is not an HTTP/2-only copy of the default transport")
	}
	if rt := http2Only(roundTripper{}); rt != (roundTripper{}) {
		t.Error("a transport of another kind was changed")
	}
	c := New(nil, keyed, "", "")
	if c.hc.Jar != nil || c.hc.CheckRedirect == nil || c.hc.CheckRedirect(nil, nil) != http.ErrUseLastResponse {
		t.Error("a client made without one follows redirects or keeps cookies")
	}
	if c.Name() != "gpt-6-luna" || len(Hosts()) != 1 || Hosts()[0] != "api.openai.com:443" {
		t.Errorf("the client names %q and reaches %q", c.Name(), Hosts())
	}
}

// TestAKeyIsSentOnlyWhenItFitsAHeader: printable ASCII without a space, at
// most 1 KiB; and an error's code is read only from the provider's JSON.
func TestAKeyIsSentOnlyWhenItFitsAHeader(t *testing.T) {
	for k, want := range map[string]bool{
		key: true, "": false, "sk two": false, "sk-é": false, "sk-\x7f": false,
		strings.Repeat("k", 1<<10): true, strings.Repeat("k", 1<<10+1): false,
	} {
		if validKey(k) != want {
			t.Errorf("validKey(%.20q) is %v", k, !want)
		}
	}
	for body, want := range map[string]string{
		`{"error":{"code":"insufficient_quota"}}`: "insufficient_quota",
		`{"error":{}}`:                            "",
		`not json`:                                "",
	} {
		if got := code([]byte(body)); got != want {
			t.Errorf("code(%s) is %q, want %q", body, got, want)
		}
	}
}
