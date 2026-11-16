package oauth

import (
	"encoding/json"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/net/idna"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/jws"
)

// reservedNames are the names no vendor's document can be at, with every
// name under them (RFC 6761, 6762, 7686, 8375, and ICANN's .internal): none
// is a public DNS name, and each could reach something on a private network
// if the host resolved it.
var reservedNames = []string{"localhost", "local", "internal", "home.arpa", "arpa", "test", "example", "invalid", "onion"}

// ClientID checks a client_id URL against ADR 0030's "Clients" and returns
// its host, in lower case and an A-label. deployment is the domain whose
// names are refused with it (the service's own); "" refuses none. A refusal
// is invalid_client, client_id_invalid.
func ClientID(raw, deployment string) (host string, err error) {
	host, ok := httpsURL(raw)
	if !ok {
		return "", refuseClient(contracts.ClientIDInvalid)
	}
	host, ok = publicName(host, deployment)
	if !ok {
		return "", refuseClient(contracts.ClientIDInvalid)
	}
	return host, nil
}

// httpsURL checks the shape of an https URL as a document's address must
// have it and returns its host as written: at most ClientIDMaxBytes of
// visible ASCII; "https://", a host with no user, no port (":443" written
// included) and no percent-encoding; a path that is neither empty nor "/",
// with no "." or ".." segment (written or percent-encoded) and no
// percent-encoded "/"; no query, no fragment.
func httpsURL(raw string) (string, bool) {
	if len(raw) > ClientIDMaxBytes || !visibleASCII(raw) || strings.Contains(raw, `\`) {
		return "", false
	}
	rest, ok := strings.CutPrefix(raw, "https://")
	if !ok {
		return "", false
	}
	host, path, ok := strings.Cut(rest, "/")
	if !ok || host == "" || strings.ContainsAny(host, "@:%[]?#") || path == "" || strings.ContainsAny(path, "?#") {
		return "", false
	}
	for _, segment := range strings.Split(path, "/") {
		decoded, err := url.PathUnescape(segment)
		if err != nil || decoded == "." || decoded == ".." || strings.Contains(decoded, "/") {
			return "", false
		}
	}
	// What the URL parser reads must be what was checked.
	if u, err := url.Parse(raw); err != nil || u.Scheme != "https" || u.Host != host || u.User != nil {
		return "", false
	}
	return host, true
}

// publicName checks that a host is a DNS name of at least two labels that is
// no IP address in any spelling, that UTS 46 maps to itself in lower case,
// and that is neither reserved nor the deployment's; it returns it in lower
// case.
func publicName(host, deployment string) (string, bool) {
	name := strings.ToLower(host)
	if _, err := netip.ParseAddr(name); err == nil {
		return "", false
	}
	labels := strings.Split(name, ".")
	if len(labels) < 2 || slices.Contains(labels, "") || numeric(labels[len(labels)-1]) {
		return "", false
	}
	if ascii, err := idna.Lookup.ToASCII(name); err != nil || ascii != name || len(name) > 253 {
		return "", false
	}
	for _, l := range labels {
		if len(l) > 63 {
			return "", false
		}
	}
	d := strings.ToLower(deployment)
	for _, r := range append(slices.Clone(reservedNames), d) {
		if r != "" && (name == r || strings.HasSuffix(name, "."+r)) {
			return "", false
		}
	}
	return name, true
}

// numeric says a label is a number as the URL standard reads the last label
// of an IPv4 address: decimal, octal or "0x" hexadecimal ("127.1",
// "0x7f.1", "2130706433" are each an address to a browser or inet_aton).
func numeric(label string) bool {
	digits := label
	if rest, ok := strings.CutPrefix(label, "0x"); ok {
		return strings.Trim(rest, "0123456789abcdef") == ""
	}
	return strings.Trim(digits, "0123456789") == ""
}

func visibleASCII(s string) bool {
	if s == "" {
		return false
	}
	for i := range len(s) {
		if s[i] < 0x21 || s[i] > 0x7e {
			return false
		}
	}
	return true
}

// Document checks a fetched client metadata document for clientID (ADR
// 0030, "Clients"): one JSON object of at most DocumentMaxBytes, every
// member once and named exactly; unknown members ignored; the members read
// of the type they must be. A refusal is invalid_client,
// client_document_invalid. client_name is made plain text (control and
// formatting characters, bidi controls among them, taken out; spaces
// collapsed) and cut to ClientNameMaxRunes; client_uri and
// application_type are kept only when they are what they say.
//
// A jwks_uri is checked against the same rules as a client_id except the
// deployment's names, which the resolver checks again before fetching it.
func Document(clientID string, body []byte) (*ClientDocument, error) {
	bad := refuseClient(contracts.ClientDocumentInvalid)
	if len(body) > DocumentMaxBytes {
		return nil, bad
	}
	members, ok := jws.Object(body)
	if !ok {
		return nil, bad
	}
	var d ClientDocument
	strs := []struct {
		name string
		to   *string
	}{
		{"client_id", &d.ClientID}, {"client_name", &d.ClientName}, {"client_uri", &d.ClientURI},
		{"token_endpoint_auth_method", &d.TokenEndpointAuthMethod},
		{"token_endpoint_auth_signing_alg", &d.TokenEndpointAuthAlg},
		{"jwks_uri", &d.JWKSURI}, {"application_type", &d.ApplicationType},
	}
	for _, s := range strs {
		if value, has := members[s.name]; has {
			if *s.to, ok = jws.Text(value); !ok {
				return nil, bad
			}
		}
	}
	lists := []struct {
		name string
		to   *[]string
	}{{"redirect_uris", &d.RedirectURIs}, {"grant_types", &d.GrantTypes}, {"response_types", &d.ResponseTypes}}
	for _, l := range lists {
		if value, has := members[l.name]; has {
			if *l.to, ok = texts(value); !ok {
				return nil, bad
			}
		}
	}
	if d.ClientID != clientID {
		return nil, bad
	}
	if len(d.RedirectURIs) == 0 || len(d.RedirectURIs) > MaxRedirectURIs {
		return nil, bad
	}
	for _, r := range d.RedirectURIs {
		if !redirectURI(r) {
			return nil, bad
		}
	}
	// Absent is a public client; there and empty is no method, refused.
	if _, has := members["token_endpoint_auth_method"]; !has {
		d.TokenEndpointAuthMethod = AuthNone
	}
	switch d.TokenEndpointAuthMethod {
	case AuthNone:
	case AuthPrivateKeyJWT:
		if _, err := ClientID(d.JWKSURI, ""); err != nil {
			return nil, bad
		}
	default:
		return nil, bad
	}
	if d.TokenEndpointAuthAlg != "" && d.TokenEndpointAuthAlg != jws.RS256 && d.TokenEndpointAuthAlg != jws.ES256 {
		return nil, bad
	}
	if members["grant_types"] != nil && !slices.Contains(d.GrantTypes, "authorization_code") {
		return nil, bad
	}
	if members["response_types"] != nil && !slices.Contains(d.ResponseTypes, "code") {
		return nil, bad
	}
	d.ClientName = shownName(d.ClientName)
	if !vendorPage(d.ClientURI) {
		d.ClientURI = ""
	}
	if d.ApplicationType != "native" && d.ApplicationType != "web" {
		d.ApplicationType = ""
	}
	return &d, nil
}

// texts is a member that is a JSON array of strings.
func texts(value json.RawMessage) ([]string, bool) {
	var raw []json.RawMessage
	if len(value) == 0 || value[0] != '[' || json.Unmarshal(value, &raw) != nil {
		return nil, false
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		s, ok := jws.Text(item)
		if !ok {
			return nil, false
		}
		out = append(out, s)
	}
	return out, true
}

// vendorPage says s is a page of the vendor as client_uri names one: an
// https URL under httpsURL's rules, or an https origin alone ("https://host"
// or "https://host/").
func vendorPage(s string) bool {
	if _, ok := httpsURL(s); ok {
		return true
	}
	origin := strings.TrimSuffix(s, "/")
	_, ok := httpsURL(origin + "/x")
	return ok && !strings.Contains(strings.TrimPrefix(origin, "https://"), "/")
}

// The hosts of a loopback redirect (RFC 8252, sections 7.3 and 8.3).
var loopbackHosts = []string{"localhost", "127.0.0.1", "[::1]"}

// redirectURI checks one of a document's redirect_uris: an https URL with a
// public host (publicHost), or an http URL whose host is exactly localhost,
// 127.0.0.1 or [::1] with any port; visible ASCII, no user, no fragment.
func redirectURI(r string) bool {
	if len(r) > ClientIDMaxBytes || !visibleASCII(r) || strings.ContainsAny(r, `#\`) {
		return false
	}
	u, err := url.Parse(r)
	if err != nil || u.User != nil || u.Opaque != "" || u.Host == "" {
		return false
	}
	switch {
	case strings.HasPrefix(r, "https://"):
		// No percent-encoding in the authority: net/url decodes it, and a
		// browser then maps the decoded Unicode through IDNA (U+017F to "s",
		// U+3002 to "."), so the host checked would not be the host reached.
		authority, _, _ := strings.Cut(strings.TrimPrefix(r, "https://"), "/")
		authority, _, _ = strings.Cut(authority, "?")
		return !strings.Contains(authority, "%") && publicHost(u)
	case strings.HasPrefix(r, "http://"):
		host, _, ok := loopback(r)
		return ok && host != ""
	}
	return false
}

// publicHost says an https redirect's authority names a host a browser
// reaches as written and does not read as this computer: a port, when
// written, of 1 to 65535 without a leading zero; a host with no empty label
// (so no trailing dot, which would make "localhost." another spelling of
// localhost); an IP address only in its one canonical spelling (the URL
// standard reads "127.1", "2130706433", "0x7f000001" and "0177.0.0.1" as
// 127.0.0.1) and neither loopback nor unspecified; and no name that is
// localhost or under it. The URI itself is kept as written and compared
// byte for byte (RedirectAllowed).
func publicHost(u *url.URL) bool {
	host := strings.ToLower(u.Hostname())
	if i := strings.LastIndex(u.Host, ":"); i >= 0 && !strings.HasSuffix(u.Host, "]") && !validPort(u.Host[i+1:]) {
		return false
	}
	if strings.HasPrefix(u.Host, "[") {
		a, err := netip.ParseAddr(host)
		return err == nil && a.Is6() && !a.Unmap().IsLoopback() && !a.Unmap().IsUnspecified()
	}
	labels := strings.Split(host, ".")
	if slices.Contains(labels, "") || !slices.EqualFunc(labels, labels, func(l, _ string) bool { return hostLabel(l) }) {
		return false
	}
	if numeric(labels[len(labels)-1]) {
		a, err := netip.ParseAddr(host)
		return err == nil && a.Is4() && a.String() == host && !a.IsLoopback() && !a.IsUnspecified()
	}
	return host != "localhost" && !strings.HasSuffix(host, ".localhost")
}

// hostLabel says l is a label a browser reads as written: letters, digits
// and inner hyphens, at most 63 bytes, and an A-label ("xn--") only when IDNA
// decodes it and encodes it back to itself, so "xn--a" and "xn--localhost-"
// are refused.
func hostLabel(l string) bool {
	if len(l) > 63 || strings.HasPrefix(l, "-") || strings.HasSuffix(l, "-") {
		return false
	}
	for _, c := range l {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
			return false
		}
	}
	if !strings.HasPrefix(l, "xn--") {
		return true
	}
	u, err := idna.Lookup.ToUnicode(l)
	if err != nil || u == l {
		return false
	}
	a, err := idna.Lookup.ToASCII(u)
	return err == nil && a == l
}

// loopback splits an http loopback URL into its host as written, its port
// and the rest (path and query): ok only for http, an exact loopback host
// and a port that is empty or a number of 1 to 65535.
func loopback(r string) (host, rest string, ok bool) {
	after, ok := strings.CutPrefix(r, "http://")
	if !ok {
		return "", "", false
	}
	authority, rest := after, ""
	if i := strings.IndexAny(after, "/?"); i >= 0 {
		authority, rest = after[:i], after[i:]
	}
	host, port := authority, ""
	if i := strings.LastIndex(authority, ":"); i >= 0 && !strings.HasSuffix(authority, "]") {
		host, port = authority[:i], authority[i+1:]
		if !validPort(port) {
			return "", "", false
		}
	}
	if !slices.Contains(loopbackHosts, host) || strings.Contains(r, "#") {
		return "", "", false
	}
	return host, rest, true
}

func validPort(p string) bool {
	if p == "" || len(p) > 5 || strings.Trim(p, "0123456789") != "" || p[0] == '0' {
		return false
	}
	n := 0
	for i := range len(p) {
		n = n*10 + int(p[i]-'0')
	}
	return n <= 65535
}

// RedirectAllowed says whether got is one of registered: exactly, or, for a
// loopback redirect (http to localhost, 127.0.0.1 or [::1]), equal but for
// the port, which is not compared whether the registered one names one or
// not (RFC 8252, section 7.3; ADR 0030, choice 4). The host, the scheme, the
// path and the query are compared byte for byte: localhost is not
// 127.0.0.1.
func RedirectAllowed(registered []string, got string) bool {
	if !redirectURI(got) {
		return false
	}
	gotHost, gotRest, gotLoop := loopback(got)
	for _, r := range registered {
		if r == got {
			return true
		}
		if host, rest, ok := loopback(r); ok && gotLoop && host == gotHost && rest == gotRest {
			return true
		}
	}
	return false
}

// shownName is a client_name as the consent screen shows it: plain text, no
// control or formatting character (bidi controls are formatting), spaces
// collapsed, at most ClientNameMaxRunes runes; "" when nothing is left,
// which the screen shows as the domain.
func shownName(name string) string {
	var b strings.Builder
	space, n := false, 0
	for _, r := range name {
		switch {
		case unicode.IsSpace(r):
			space = b.Len() > 0
			continue
		case unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Co, unicode.Cs) || r == utf8.RuneError:
			continue
		}
		if n == ClientNameMaxRunes {
			break
		}
		if space {
			if n+1 == ClientNameMaxRunes {
				break
			}
			b.WriteByte(' ')
			n++
			space = false
		}
		b.WriteRune(r)
		n++
	}
	return b.String()
}
