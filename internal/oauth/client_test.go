package oauth

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/permaily-org/permaily/internal/contracts"
)

const deployment = "permaily.com"

func code(t *testing.T, err error, want ErrorCode, reason string) {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) || e.Code != want || e.Reason != reason {
		t.Fatalf("got %v, want %s (%s)", err, want, reason)
	}
}

// V1. A client_id is an https URL of a public name, and nothing else.
func TestV1ClientIDAcceptsTheVendorsAndRefusesTheRest(t *testing.T) {
	for raw, host := range map[string]string{
		"https://claude.ai/oauth/claude-code-client-metadata": "claude.ai",
		"https://claude.ai/oauth/mcp-oauth-client-metadata":   "claude.ai",
		"https://chatgpt.com/oauth/codex/client.json":         "chatgpt.com",
		"https://chatgpt.com/oauth/client.json":               "chatgpt.com",
		"https://chatgpt.com/oauth/chatgpt/client.json":       "chatgpt.com",
		"https://Claude.AI/oauth/x":                           "claude.ai",
		"https://xn--bcher-kva.de/client":                     "xn--bcher-kva.de",
		"https://notpermaily.com/client":                      "notpermaily.com",
		"https://vendor.example.com/a//b/":                    "vendor.example.com",
		"https://a.b.c.vendor.io/%41":                         "a.b.c.vendor.io",
	} {
		got, err := ClientID(raw, deployment)
		if err != nil || got != host {
			t.Errorf("%s: %q, %v", raw, got, err)
		}
	}
	refused := []string{
		"", "http://claude.ai/x", "HTTPS://claude.ai/x", "ftp://claude.ai/x", "//claude.ai/x", "claude.ai/x",
		"https://user@claude.ai/x", "https://user:pw@claude.ai/x", "https://@claude.ai/x",
		"https://claude.ai:443/x", "https://claude.ai:8443/x", "https://claude.ai:/x",
		"https://claude.ai/x#f", "https://claude.ai/x#", "https://claude.ai/x?q=1", "https://claude.ai/x?", "https://claude.ai?x",
		"https://claude.ai", "https://claude.ai/", "https:///x",
		"https://claude.ai/./x", "https://claude.ai/a/../x", "https://claude.ai/a/..", "https://claude.ai/a/.",
		"https://claude.ai/%2e%2e/x", "https://claude.ai/%2E/x", "https://claude.ai/a%2Fb", "https://claude.ai/a%2fb", "https://claude.ai/%zz",
		"https://claude.ai/a\\b", "https://claude.ai/a b", "https://claude.ai/a\tb", "https://claude.ai/é",
		"https://127.0.0.1/x", "https://127.1/x", "https://0x7f.1/x", "https://0x7F.1/x", "https://2130706433/x", "https://0177.0.0.1/x",
		"https://8.8.8.8/x", "https://1.2.3.0x4/x", "https://1.2.3.04/x", "https://example.com.0x/x",
		"https://[::1]/x", "https://[::ffff:127.0.0.1]/x", "https://[fe80::1%25eth0]/x", "https://[2001:db8::1]/x", "https://::1/x",
		"https://%31%32%37.0.0.1/x", "https://claude%2eai/x", "https://a<b.com/x", "https://a{b}.com/x",
		"https://claude/x", "https://com/x",
		"https://localhost/x", "https://a.localhost/x", "https://printer.local/x", "https://x.internal/x", "https://router.home.arpa/x",
		"https://1.0.0.127.in-addr.arpa/x", "https://a.test/x", "https://a.example/x", "https://claude.ai.example/x", "https://a.invalid/x", "https://abc.onion/x",
		"https://permaily.com/x", "https://api.permaily.com/x", "https://APP.Permaily.com/x",
		"https://xn--a.com/x", "https://a_b.com/x", "https://-a.com/x", "https://a-.com/x", "https://ab--c.com/x",
		"https://claude.ai./x", "https://.claude.ai/x", "https://claude..ai/x",
		"https://" + strings.Repeat("a", 64) + ".com/x", "https://" + strings.Repeat("a.", 127) + "com/x",
		"https://claude.ai/" + strings.Repeat("a", 513-len("https://claude.ai/")),
	}
	for _, raw := range refused {
		if host, err := ClientID(raw, deployment); err == nil {
			t.Errorf("%q accepted as %q", raw, host)
		} else {
			code(t, err, ErrInvalidClient, string(contracts.ClientIDInvalid))
		}
	}
	// 512 bytes is the most.
	long := "https://claude.ai/" + strings.Repeat("a", 512-len("https://claude.ai/"))
	if _, err := ClientID(long, deployment); err != nil {
		t.Fatalf("512 bytes: %v", err)
	}
	// With no deployment, no name is the deployment's.
	if _, err := ClientID("https://permaily.com/x", ""); err != nil {
		t.Fatal(err)
	}
}

func vendorDoc(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "vendors", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

const codeID = "https://claude.ai/oauth/claude-code-client-metadata"

// doc writes a document from members, client_id and redirect_uris set unless
// given.
func doc(members map[string]any) []byte {
	m := map[string]any{"client_id": codeID, "redirect_uris": []string{"http://localhost/callback"}}
	for k, v := range members {
		if v == nil {
			delete(m, k)
			continue
		}
		m[k] = v
	}
	b, _ := json.Marshal(m)
	return b
}

// V2. The vendors' five documents, byte for byte, are taken; a document that
// breaks a rule is not.
func TestV2DocumentTakesTheVendorsAndRefusesTheRest(t *testing.T) {
	for file, want := range map[string]ClientDocument{
		"cimd-claude-code.json": {ClientID: codeID, ClientName: "Claude Code", ClientURI: "https://claude.ai", RedirectURIs: []string{"http://localhost/callback", "http://127.0.0.1/callback"}, TokenEndpointAuthMethod: AuthNone},
		"cimd-claude-ai.json":   {ClientID: "https://claude.ai/oauth/mcp-oauth-client-metadata", ClientName: "Claude", ClientURI: "https://claude.ai", RedirectURIs: []string{"https://claude.ai/api/mcp/auth_callback"}, TokenEndpointAuthMethod: AuthNone},
		"cimd-codex.json":       {ClientID: "https://chatgpt.com/oauth/codex/client.json", ClientName: "Codex", ClientURI: "https://chatgpt.com/codex", ApplicationType: "native", RedirectURIs: []string{"http://127.0.0.1/callback", "http://localhost/callback"}, TokenEndpointAuthMethod: AuthNone},
		"cimd-chatgpt-connector.json": {ClientID: "https://chatgpt.com/oauth/client.json", ClientName: "ChatGPT", ClientURI: "https://chatgpt.com/", RedirectURIs: []string{"https://chatgpt.com/connector_platform_oauth_redirect"},
			TokenEndpointAuthMethod: AuthPrivateKeyJWT, TokenEndpointAuthAlg: "RS256", JWKSURI: "https://chatgpt.com/oauth/jwks.json"},
		"cimd-chatgpt-apps.json": {ClientID: "https://chatgpt.com/oauth/chatgpt/client.json", ClientName: "ChatGPT", ClientURI: "https://chatgpt.com/", RedirectURIs: []string{"https://chatgpt.com/connector/oauth/chatgpt"},
			TokenEndpointAuthMethod: AuthPrivateKeyJWT, TokenEndpointAuthAlg: "RS256", JWKSURI: "https://chatgpt.com/oauth/jwks.json"},
	} {
		got, err := Document(want.ClientID, vendorDoc(t, file))
		if err != nil {
			t.Fatalf("%s: %v", file, err)
		}
		got.GrantTypes, got.ResponseTypes = nil, nil
		g, _ := json.Marshal(got)
		w, _ := json.Marshal(want)
		if string(g) != string(w) {
			t.Errorf("%s:\n got %s\nwant %s", file, g, w)
		}
	}
	seventeen := make([]string, 17)
	for i := range seventeen {
		seventeen[i] = "https://vendor.example.com/cb"
	}
	for name, body := range map[string][]byte{
		"client_id differing by a byte":          doc(map[string]any{"client_id": codeID[:len(codeID)-1] + "X"}),
		"client_id with a trailing slash":        doc(map[string]any{"client_id": codeID + "/"}),
		"no client_id":                           doc(map[string]any{"client_id": nil}),
		"client_id in another case":              doc(map[string]any{"client_id": nil, "Client_ID": codeID}),
		"no redirect_uris":                       doc(map[string]any{"redirect_uris": nil}),
		"empty redirect_uris":                    doc(map[string]any{"redirect_uris": []string{}}),
		"17 redirect_uris":                       doc(map[string]any{"redirect_uris": seventeen}),
		"a redirect with a fragment":             doc(map[string]any{"redirect_uris": []string{"https://vendor.example.com/cb#x"}}),
		"an http redirect not loopback":          doc(map[string]any{"redirect_uris": []string{"http://vendor.example.com/cb"}}),
		"an http redirect to 127.0.0.2":          doc(map[string]any{"redirect_uris": []string{"http://127.0.0.2/cb"}}),
		"an https redirect to localhost":         doc(map[string]any{"redirect_uris": []string{"https://localhost/cb"}}),
		"an https redirect to 127.0.0.1":         doc(map[string]any{"redirect_uris": []string{"https://127.0.0.1:8443/cb"}}),
		"an https redirect to [::1]":             doc(map[string]any{"redirect_uris": []string{"https://[::1]/cb"}}),
		"an https redirect under localhost":      doc(map[string]any{"redirect_uris": []string{"https://app.localhost/cb"}}),
		"an ftp redirect":                        doc(map[string]any{"redirect_uris": []string{"ftp://vendor.example.com/cb"}}),
		"a custom scheme":                        doc(map[string]any{"redirect_uris": []string{"com.vendor.app:/cb"}}),
		"a redirect with a user":                 doc(map[string]any{"redirect_uris": []string{"https://u@vendor.example.com/cb"}}),
		"a redirect that is no URL":              doc(map[string]any{"redirect_uris": []string{"https://vendor.example.com/%zz"}}),
		"a relative redirect":                    doc(map[string]any{"redirect_uris": []string{"/cb"}}),
		"a loopback redirect on port 0":          doc(map[string]any{"redirect_uris": []string{"http://localhost:0/cb"}}),
		"a loopback redirect on port 65536":      doc(map[string]any{"redirect_uris": []string{"http://localhost:65536/cb"}}),
		"a loopback redirect on an empty port":   doc(map[string]any{"redirect_uris": []string{"http://localhost:/cb"}}),
		"a loopback redirect on port 080":        doc(map[string]any{"redirect_uris": []string{"http://localhost:080/cb"}}),
		"a loopback redirect on port x":          doc(map[string]any{"redirect_uris": []string{"http://localhost:x/cb"}}),
		"a redirect of 513 bytes":                doc(map[string]any{"redirect_uris": []string{"https://vendor.example.com/" + strings.Repeat("a", 486)}}),
		"redirect_uris as text":                  doc(map[string]any{"redirect_uris": "https://vendor.example.com/cb"}),
		"redirect_uris holding a number":         doc(map[string]any{"redirect_uris": []any{1}}),
		"client_secret_basic":                    doc(map[string]any{"token_endpoint_auth_method": "client_secret_basic"}),
		"client_secret_post":                     doc(map[string]any{"token_endpoint_auth_method": "client_secret_post"}),
		"private_key_jwt without jwks_uri":       doc(map[string]any{"token_endpoint_auth_method": "private_key_jwt"}),
		"private_key_jwt with an inline jwks":    doc(map[string]any{"token_endpoint_auth_method": "private_key_jwt", "jwks": map[string]any{"keys": []any{}}}),
		"a jwks_uri over http":                   doc(map[string]any{"token_endpoint_auth_method": "private_key_jwt", "jwks_uri": "http://chatgpt.com/oauth/jwks.json"}),
		"a jwks_uri at an address":               doc(map[string]any{"token_endpoint_auth_method": "private_key_jwt", "jwks_uri": "https://169.254.169.254/latest"}),
		"an HS256 signing alg":                   doc(map[string]any{"token_endpoint_auth_signing_alg": "HS256"}),
		"grant_types without authorization_code": doc(map[string]any{"grant_types": []string{"refresh_token"}}),
		"empty grant_types":                      doc(map[string]any{"grant_types": []string{}}),
		"response_types without code":            doc(map[string]any{"response_types": []string{"token"}}),
		"client_name not text":                   doc(map[string]any{"client_name": 7}),
		"client_name null":                       doc(map[string]any{"client_name": json.RawMessage("null")}),
		"a member twice":                         []byte(`{"client_id":"` + codeID + `","client_id":"` + codeID + `","redirect_uris":["http://localhost/cb"]}`),
		"not an object":                          []byte(`["` + codeID + `"]`),
		"something after the object":             append(doc(nil), []byte(" {}")...),
		"not UTF-8":                              []byte("{\"client_id\":\"" + codeID + "\",\"client_name\":\"\xff\",\"redirect_uris\":[\"http://localhost/cb\"]}"),
		"8 KiB and one byte":                     doc(map[string]any{"padding": strings.Repeat("a", DocumentMaxBytes)}),
	} {
		if _, err := Document(codeID, body); err == nil {
			t.Errorf("%s: accepted", name)
		} else {
			code(t, err, ErrInvalidClient, string(contracts.ClientDocumentInvalid))
		}
	}
	// Unknown members are ignored; the most a document may be is taken.
	extra := map[string]any{"padding": "", "logo_uri": "https://x.example.com/l.png", "nested": map[string]any{"a": []int{1}}}
	extra["padding"] = strings.Repeat("a", DocumentMaxBytes-len(doc(extra)))
	full := doc(extra)
	if len(full) != DocumentMaxBytes {
		t.Fatalf("%d bytes", len(full))
	}
	d, err := Document(codeID, full)
	if err != nil {
		t.Fatal(err)
	}
	if d.TokenEndpointAuthMethod != AuthNone || d.ClientName != "" || d.ClientURI != "" || d.ApplicationType != "" {
		t.Fatalf("%+v", d)
	}
}

// V2 (choice 23). A name is shown as plain text and cut; client_uri and
// application_type are kept only when they are what they say.
func TestV2TheNameIsPlainTextAndCut(t *testing.T) {
	for name, want := range map[string]string{
		strings.Repeat("a", 65):                   strings.Repeat("a", 64),
		strings.Repeat("é", 70):                   strings.Repeat("é", 64),
		strings.Repeat("x", 8000):                 strings.Repeat("x", 64),
		"\u202eevil\u202c\u2066 name\x00\x1b[31m": "evil name[31m",
		"  Claude \t\n Code  ":                    "Claude Code",
		"\u200b\u200f\ufeff":                      "",
		"a" + strings.Repeat(" ", 10) + "b":       "a b",
		strings.Repeat("a", 63) + " b":            strings.Repeat("a", 63),
		strings.Repeat("a", 62) + " b":            strings.Repeat("a", 62) + " b",
		"\U0001F600 Mail \U000F0000":              "\U0001F600 Mail",
	} {
		d, err := Document(codeID, doc(map[string]any{"client_name": name}))
		if err != nil {
			t.Fatalf("%.40q: %v", name, err)
		}
		if d.ClientName != want || utf8.RuneCountInString(d.ClientName) > ClientNameMaxRunes {
			t.Errorf("%.40q: %q, want %q", name, d.ClientName, want)
		}
	}
	// A name of 10,000 characters is cut the same; a document that holds
	// one is past DocumentMaxBytes.
	if got := shownName(strings.Repeat("\u202ex", 10000)); got != strings.Repeat("x", 64) {
		t.Fatalf("%q", got)
	}
	if _, err := Document(codeID, doc(map[string]any{"client_name": strings.Repeat("x", 10000)})); err == nil {
		t.Fatal("a document of more than 8 KiB")
	}
	for uri, kept := range map[string]bool{
		"https://vendor.example.com": true, "https://vendor.example.com/": true, "https://vendor.example.com/app": true,
		"http://vendor.example.com": false, "javascript:alert(1)": false, "https://vendor.example.com/a?b": false,
	} {
		d, err := Document(codeID, doc(map[string]any{"client_uri": uri, "application_type": "spaceship"}))
		if err != nil {
			t.Fatal(err)
		}
		if (d.ClientURI == uri) != kept || (!kept && d.ClientURI != "") || d.ApplicationType != "" {
			t.Errorf("%q: %+v", uri, d)
		}
	}
	d, err := Document(codeID, doc(map[string]any{"application_type": "web", "client_name": "\xff"}))
	if err != nil || d.ApplicationType != "web" {
		t.Fatalf("%+v %v", d, err)
	}
}

// V3. A loopback redirect is taken on any port, whether the document's names
// one or not (RFC 8252, section 7.3); everything else exactly.
func TestV3RedirectAllowed(t *testing.T) {
	registered := []string{"http://localhost/callback", "http://127.0.0.1:8080/cb", "http://[::1]/cb?x=1", "https://claude.ai/api/mcp/auth_callback"}
	for got, want := range map[string]bool{
		"http://localhost/callback":                   true,
		"http://localhost:53682/callback":             true,
		"http://localhost:1/callback":                 true,
		"http://localhost:65535/callback":             true,
		"http://127.0.0.1/cb":                         true,
		"http://127.0.0.1:9999/cb":                    true,
		"http://[::1]:4000/cb?x=1":                    true,
		"https://claude.ai/api/mcp/auth_callback":     true,
		"http://127.0.0.1:5000/callback":              false, // localhost is not 127.0.0.1
		"http://localhost:5000/other":                 false,
		"http://localhost:5000/callback/":             false,
		"http://localhost:5000/Callback":              false,
		"http://LOCALHOST:5000/callback":              false,
		"https://localhost:5000/callback":             false,
		"http://localhost:5000/callback?x":            false,
		"http://localhost:5000/callback#x":            false,
		"http://localhost:0/callback":                 false,
		"http://localhost:65536/callback":             false,
		"http://user@localhost:5000/callback":         false,
		"http://[::1]:4000/cb":                        false,
		"http://[::1]:4000/cb?x=2":                    false,
		"https://claude.ai:443/api/mcp/auth_callback": false,
		"https://Claude.ai/api/mcp/auth_callback":     false,
		"https://claude.ai/api/mcp/auth_callback/":    false,
		"https://claude.ai/api/mcp/auth_callback?a":   false,
		"http://claude.ai/api/mcp/auth_callback":      false,
		"https://claude.ai/api/mcp/auth_callback#":    false,
		"": false,
	} {
		if RedirectAllowed(registered, got) != want {
			t.Errorf("%q: %v", got, !want)
		}
	}
	if RedirectAllowed(nil, "http://localhost/callback") {
		t.Fatal("nothing registered")
	}
}
