package mailhtml

import (
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/net/idna"

	"github.com/permaily-org/permaily/internal/contracts"
)

// A link's address, read as ADR 0026 ("Links") says: the WHATWG trimming,
// then a grammar this package checks itself before anything else sees the
// address, so that what the server writes is what every browser's URL parser
// reads back (the agreement table, testdata/urls.json).

// whatwgTrim removes what the WHATWG URL parser removes before it reads an
// address: leading and trailing C0 controls and spaces, and every tab and
// newline inside.
func whatwgTrim(s string) string {
	s = strings.TrimFunc(s, func(r rune) bool { return r <= 0x20 })
	if strings.ContainsAny(s, "\t\n\r") {
		s = strings.Map(func(r rune) rune {
			if r == '\t' || r == '\n' || r == '\r' {
				return -1
			}
			return r
		}, s)
	}
	return s
}

// canonicalURL is an http or https address that this package keeps: the
// canonical URL, and its host as the WHATWG URL's host attribute writes it.
type canonicalURL struct {
	URL  string
	Host string // hostname, and ":port" when the port is not the scheme's default
	Name string // hostname alone
}

// parseLink reads an href (already trimmed) and says whether it is a kept
// link, as ADR 0026's grammar says:
//
//	scheme "://" host [":" port] [path] ["?" query] ["#" fragment]
//
// scheme http or https in any case; no userinfo, no "\" anywhere, no "%" in
// the host; the host a DNS name after idna.Lookup.ToASCII whose last label
// is letters (or xn--), which refuses every IP address in every spelling;
// the port 1 to 65535 without a leading zero, dropped when it is the
// default; path, query and fragment written again in ASCII that a browser's
// parser reads as itself.
func parseLink(s string) (canonicalURL, bool) {
	if len(s) > contracts.MessageHTMLMaxURLBytes || len(s) == 0 {
		return canonicalURL{}, false
	}
	if strings.ContainsRune(s, '\\') {
		return canonicalURL{}, false
	}
	colon := strings.IndexByte(s, ':')
	if colon < 0 {
		return canonicalURL{}, false
	}
	scheme := strings.ToLower(s[:colon])
	if scheme != "http" && scheme != "https" {
		return canonicalURL{}, false
	}
	rest := s[colon+1:]
	if !strings.HasPrefix(rest, "//") {
		return canonicalURL{}, false
	}
	rest = rest[2:]
	end := strings.IndexAny(rest, "/?#")
	if end < 0 {
		end = len(rest)
	}
	authority, tail := rest[:end], rest[end:]
	if authority == "" || strings.ContainsAny(authority, "@%[]") {
		return canonicalURL{}, false
	}
	host, port := authority, ""
	if i := strings.LastIndexByte(authority, ':'); i >= 0 {
		host, port = authority[:i], authority[i+1:]
		if !validPort(port) {
			return canonicalURL{}, false
		}
		if (scheme == "http" && port == "80") || (scheme == "https" && port == "443") {
			port = ""
		}
	}
	name, ok := dnsName(host)
	if !ok {
		return canonicalURL{}, false
	}
	hostAttr := name
	if port != "" {
		hostAttr += ":" + port
	}
	path, query, fragment, hasQuery, hasFragment := splitTail(tail)
	var b strings.Builder
	b.WriteString(scheme)
	b.WriteString("://")
	b.WriteString(hostAttr)
	b.WriteString(normalisePath(path))
	if hasQuery {
		b.WriteByte('?')
		b.WriteString(encodeComponent(query, false))
	}
	if hasFragment {
		b.WriteByte('#')
		b.WriteString(encodeComponent(fragment, true))
	}
	out := b.String()
	if len(out) > contracts.MessageHTMLMaxURLBytes {
		return canonicalURL{}, false
	}
	return canonicalURL{URL: out, Host: hostAttr, Name: name}, true
}

// validPort is 1 to 65535 in decimal, without a leading zero.
func validPort(p string) bool {
	if p == "" || len(p) > 5 || p[0] == '0' {
		return false
	}
	for i := range len(p) {
		if p[i] < '0' || p[i] > '9' {
			return false
		}
	}
	n, err := strconv.Atoi(p)
	return err == nil && n >= 1 && n <= 65535
}

// dnsName is host as a DNS name in ASCII, lower case, or false: two to 127
// labels of [a-z0-9-], each 1 to 63 characters, not starting or ending with
// "-", 253 characters at most, no trailing dot, and a last label of letters
// (2 to 63) or xn--. That last rule refuses every IPv4 address in every
// spelling the WHATWG parser accepts ("ends in a number checker").
func dnsName(host string) (string, bool) {
	if host == "" || !utf8.ValidString(host) || strings.HasSuffix(host, ".") {
		return "", false
	}
	ascii, err := idna.Lookup.ToASCII(host)
	if err != nil {
		return "", false
	}
	ascii = strings.ToLower(ascii)
	if len(ascii) > 253 || strings.HasSuffix(ascii, ".") {
		return "", false
	}
	labels := strings.Split(ascii, ".")
	if len(labels) < 2 || len(labels) > 127 {
		return "", false
	}
	for _, l := range labels {
		if len(l) < 1 || len(l) > 63 || l[0] == '-' || l[len(l)-1] == '-' {
			return "", false
		}
		for i := range len(l) {
			c := l[i]
			if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
				return "", false
			}
		}
	}
	last := labels[len(labels)-1]
	if strings.HasPrefix(last, "xn--") {
		return ascii, len(last) > 4
	}
	if len(last) < 2 {
		return "", false
	}
	for i := range len(last) {
		if last[i] < 'a' || last[i] > 'z' {
			return "", false
		}
	}
	return ascii, true
}

// splitTail splits what follows the authority into path, query and fragment.
func splitTail(t string) (path, query, fragment string, hasQuery, hasFragment bool) {
	if i := strings.IndexByte(t, '#'); i >= 0 {
		t, fragment, hasFragment = t[:i], t[i+1:], true
	}
	if i := strings.IndexByte(t, '?'); i >= 0 {
		t, query, hasQuery = t[:i], t[i+1:], true
	}
	return t, query, fragment, hasQuery, hasFragment
}

// normalisePath writes the path as the WHATWG parser leaves it for a special
// scheme: "/" when empty, its dot segments resolved (".", "%2e", "..",
// ".%2e" and so on), each segment percent-encoded.
func normalisePath(p string) string {
	if p == "" {
		return "/"
	}
	segs := strings.Split(p[1:], "/") // p starts with "/"
	var out []string
	for i, s := range segs {
		last := i == len(segs)-1
		switch {
		case isDoubleDot(s):
			if len(out) > 0 {
				out = out[:len(out)-1]
			}
			if last {
				out = append(out, "")
			}
		case isSingleDot(s):
			if last {
				out = append(out, "")
			}
		default:
			out = append(out, encodeComponent(s, false))
		}
	}
	return "/" + strings.Join(out, "/")
}

func isSingleDot(s string) bool { return s == "." || strings.EqualFold(s, "%2e") }

func isDoubleDot(s string) bool {
	switch strings.ToLower(s) {
	case "..", ".%2e", "%2e.", "%2e%2e":
		return true
	}
	return false
}

// urlSafe are the ASCII bytes left as they are in a path, query or fragment:
// the WHATWG parser percent-encodes none of them in any of the three, so a
// browser reads what is written as itself. "'" is not among them (a special
// URL's query encodes it), nor "%" unless it starts an escape.
func urlSafe(c byte) bool {
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		return true
	}
	return strings.IndexByte("-._~!$&()*+,;=:@/?", c) >= 0
}

const hexUpper = "0123456789ABCDEF"

// encodeComponent percent-encodes every byte of s that urlSafe does not
// keep, and every byte of a non-ASCII character, as UTF-8. A "%" followed by
// two hexadecimal digits is an escape and stays; any other is written %25.
// In a fragment, "#" is written %23 too.
func encodeComponent(s string, fragment bool) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '%' && i+2 < len(s) && isHex(s[i+1]) && isHex(s[i+2]):
			b.WriteByte('%')
		case urlSafe(c) && (c != '#' || !fragment):
			b.WriteByte(c)
		default:
			b.WriteByte('%')
			b.WriteByte(hexUpper[c>>4])
			b.WriteByte(hexUpper[c&15])
		}
	}
	return b.String()
}

func isHex(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}

// textHost is the host a link's text names, for MessageLink.Shown: the text
// (trimmed) is an http or https address, or starts with "www.", or is one
// DNS name and nothing else. Anything else names no host.
func textHost(text string) string {
	t := strings.TrimSpace(text)
	if t == "" || len(t) > contracts.MessageHTMLMaxURLBytes {
		return ""
	}
	lower := strings.ToLower(t)
	if strings.HasPrefix(lower, "http://") || strings.HasPrefix(lower, "https://") {
		if c, ok := parseLink(whatwgTrim(t)); ok {
			return c.Name
		}
		rest := t[strings.Index(t, "//")+2:]
		return hostWord(rest)
	}
	if strings.HasPrefix(lower, "www.") || !strings.ContainsAny(t, " \t\n") {
		return hostWord(t)
	}
	return ""
}

// hostWord is the DNS name at the start of s, up to the first "/", "?",
// "#", ":" or space, when it is one with at least one dot.
func hostWord(s string) string {
	end := strings.IndexAny(s, "/?#: \t\n")
	if end >= 0 {
		s = s[:end]
	}
	if !strings.Contains(s, ".") {
		return ""
	}
	name, ok := dnsName(s)
	if !ok {
		return ""
	}
	return name
}
