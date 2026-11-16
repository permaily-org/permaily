package cimd

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/idna"

	"github.com/permaily-org/permaily/internal/oauth"
)

// The rules of a client_id URL and of its document are oauth.ClientID and
// oauth.Document, ADR 0030's pull request 1. This pull request (2) is built
// at the same time, so until 1 merges the resolver checks through these
// stand-ins, written to "Clients" of ADR 0030; when 1 merges, checkClientID
// and checkDocument become oauth.ClientID and oauth.Document and this file
// goes ("What truly runs in parallel": 2 builds its URL checks against a
// stand-in until 1 merges).
var (
	checkClientID = standinClientID
	checkDocument = standinDocument
)

var errStandin = errors.New("cimd: refused by the rules of a client")

// reservedNames are the names a client_id's host is never, nor under.
var reservedNames = []string{"localhost", "local", "internal", "home.arpa", "arpa", "test", "example", "invalid", "onion"}

// standinClientID checks raw under "Clients" and returns its host, lower
// case, as an A-label; deployment is the domain whose names are refused.
func standinClientID(raw, deployment string) (string, error) {
	if raw == "" || len(raw) > oauth.ClientIDMaxBytes || !utf8.ValidString(raw) {
		return "", errStandin
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Opaque != "" ||
		u.Port() != "" || strings.HasSuffix(u.Host, ":") ||
		u.Fragment != "" || strings.Contains(raw, "#") ||
		u.RawQuery != "" || u.ForceQuery || strings.Contains(raw, "?") {
		return "", errStandin
	}
	if u.Path == "" || u.Path == "/" || strings.Contains(strings.ToLower(u.EscapedPath()), "%2f") {
		return "", errStandin
	}
	for seg := range strings.SplitSeq(u.Path, "/") {
		if seg == "." || seg == ".." {
			return "", errStandin
		}
	}
	host, err := dnsName(u.Hostname())
	if err != nil {
		return "", err
	}
	for _, r := range reservedNames {
		if host == r || strings.HasSuffix(host, "."+r) {
			return "", errStandin
		}
	}
	if d := strings.ToLower(deployment); d != "" && (host == d || strings.HasSuffix(host, "."+d)) {
		return "", errStandin
	}
	return host, nil
}

// dnsName is h as a DNS name of two labels or more, lower case, as an
// A-label, written as its A-label: never an IP literal in any spelling the URL parser or net/netip
// reads, nor one whose last label is numeric (127.1, 0x7f.1, 2130706433).
func dnsName(h string) (string, error) {
	if h == "" || strings.ContainsAny(h, "[]:%") {
		return "", errStandin
	}
	if _, err := netip.ParseAddr(h); err == nil {
		return "", errStandin
	}
	a, err := idna.Lookup.ToASCII(h)
	if err != nil {
		return "", errStandin
	}
	a = strings.ToLower(a)
	if a != strings.ToLower(h) {
		return "", errStandin
	}
	labels := strings.Split(a, ".")
	if len(labels) < 2 || len(a) > 253 {
		return "", errStandin
	}
	for _, l := range labels {
		if l == "" || len(l) > 63 {
			return "", errStandin
		}
	}
	last := labels[len(labels)-1]
	if strings.HasPrefix(last, "0x") || strings.Trim(last, "0123456789") == "" {
		return "", errStandin
	}
	return a, nil
}

// standinDocument checks a fetched document for clientID under "Clients".
func standinDocument(clientID string, body []byte) (*oauth.ClientDocument, error) {
	if b := bytes.TrimLeft(body, " \t\r\n"); len(b) == 0 || b[0] != '{' {
		return nil, errStandin
	}
	var doc oauth.ClientDocument
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, errStandin
	}
	if doc.ClientID != clientID {
		return nil, errStandin
	}
	if n := len(doc.RedirectURIs); n == 0 || n > oauth.MaxRedirectURIs {
		return nil, errStandin
	}
	for _, r := range doc.RedirectURIs {
		if !redirectURI(r) {
			return nil, errStandin
		}
	}
	switch doc.TokenEndpointAuthMethod {
	case "", oauth.AuthNone:
	case oauth.AuthPrivateKeyJWT:
		if _, err := standinClientID(doc.JWKSURI, ""); err != nil {
			return nil, errStandin
		}
	default:
		return nil, errStandin
	}
	if doc.GrantTypes != nil && !slices.Contains(doc.GrantTypes, "authorization_code") {
		return nil, errStandin
	}
	if doc.ResponseTypes != nil && !slices.Contains(doc.ResponseTypes, "code") {
		return nil, errStandin
	}
	if utf8.RuneCountInString(doc.ClientName) > oauth.ClientNameMaxRunes {
		doc.ClientName = string([]rune(doc.ClientName)[:oauth.ClientNameMaxRunes])
	}
	return &doc, nil
}

// redirectURI is an absolute https URL to a host that is not loopback, or an
// http one to exactly localhost, 127.0.0.1 or [::1]; never with a fragment.
func redirectURI(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Fragment != "" || strings.Contains(raw, "#") || u.User != nil {
		return false
	}
	loopback := slices.Contains([]string{"localhost", "127.0.0.1", "::1"}, u.Hostname())
	switch u.Scheme {
	case "https":
		return !loopback
	case "http":
		return loopback
	}
	return false
}
