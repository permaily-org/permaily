package mailhtml

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// The URL agreement table of ADR 0026 (tests 24 and 25): inputs, Go's
// verdict and, when kept, the canonical URL and what the WHATWG URL parser
// must say of it. This test writes testdata/urls.json (-update) and checks
// it; the web app's Vitest (Node's URL) and the browser suite (each
// engine's new URL()) read it and assert the same.

type urlCase struct {
	Input string `json:"input"`
	Kept  bool   `json:"kept"`
	// For a kept URL: what new URL(URL) must give.
	URL      string `json:"url,omitempty"`
	Protocol string `json:"protocol,omitempty"`
	Hostname string `json:"hostname,omitempty"`
	Port     string `json:"port,omitempty"`
	Host     string `json:"host,omitempty"`
	// IPv4: a refused spelling that a browser reads as an IPv4 address,
	// which is why it is refused (test 25).
	IPv4 bool `json:"ipv4,omitempty"`
}

type urlTable struct {
	About string    `json:"about"`
	URLs  []urlCase `json:"urls"`
}

// urlInputs are items 10 and 16 and the valid shapes around them.
var urlInputs = []struct {
	in   string
	ipv4 bool
}{
	{"http://2130706433:8080/x", true}, {"http://0x7f000001:8080/x", true}, {"http://0177.0.0.1/", true},
	{"http://127.1/", true}, {"http://127.0.0.1/", true}, {"http://[::1]/", false},
	{"http://user:pw@example.com/", false}, {`https://evil.example\@good.example/`, false}, {"http://exa%6Dple.com/", false},
	{"http://example.com./", false}, {"http://example.0x10/", false}, {"http://example.com:080/", false},
	{"http://localhost/", false}, {"https://192.168.1.1/", true}, {"http://0x7F.1/", true}, {"http://1.2.3.4.5/", false},
	{"https://bank.example/login", false}, {"https://xn--pypal-4ve.example/", false}, {"https://раураl.example/", false},
	{"https://bücher.example/", false}, {"https://ＥＸＡＭＰＬＥ.com/", false}, {"HTTPS://Example.COM", false},
	{"https://example.com:443/", false}, {"http://example.com:80/a", false}, {"https://example.com:8443/x", false},
	{"http://example.com:65535/", false}, {"https://example.com/a/./b/../c", false}, {"https://example.com/a/%2e%2E/b/.", false},
	{"https://example.com/%7Euser/caf%C3%A9", false}, {"https://example.com/é ' \"<>`{}|^[]", false},
	{"https://example.com/?q=a b&r='&s=é#f g#h", false}, {"https://example.com/%zz%41", false}, {"https://example.com?x", false},
	{"https://example.com#", false}, {"https://example.com/p?", false}, {"https://a.b.c.example.museum/", false},
	{"https://sub-domain.example.co.uk/path;params?q=1&r=2#frag", false}, {"https://example.com/a//b", false},
	{"https://example.com/../../a", false}, {"https://example.com/~!$&()*+,;=:@/", false},
	{"https://shop.example/orders/100-2041?utm_source=email&utm_medium=receipt", false},
	{"https://example.com/\u0000", false}, {"https://example.com/\u200b", false}, {"https://xn--bcher-kva.example/", false},
}

func buildURLTable() urlTable {
	tbl := urlTable{About: "ADR 0026 tests 24 and 25: written by go test ./internal/mailhtml -run TestURLTable -update; read by web/tests and tests/message-frame"}
	for _, c := range urlInputs {
		u, ok := parseLink(whatwgTrim(c.in))
		row := urlCase{Input: c.in, Kept: ok, IPv4: c.ipv4}
		if ok {
			row.URL, row.Host, row.Hostname = u.URL, u.Host, u.Name
			row.Protocol = u.URL[:strings.Index(u.URL, ":")+1]
			if i := strings.LastIndexByte(u.Host, ':'); i >= 0 {
				row.Port = u.Host[i+1:]
			}
		}
		tbl.URLs = append(tbl.URLs, row)
	}
	return tbl
}

func TestURLTable(t *testing.T) {
	tbl := buildURLTable()
	for _, row := range tbl.URLs {
		if row.IPv4 && row.Kept {
			t.Errorf("%s: an IPv4 address kept", row.Input)
		}
	}
	got, err := json.MarshalIndent(tbl, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	if *update {
		if err := os.WriteFile("testdata/urls.json", got, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile("testdata/urls.json")
	if err != nil || !bytes.Equal(want, got) {
		t.Fatalf("testdata/urls.json is not what Go decides now (%v); run with -update and review the diff", err)
	}
}
