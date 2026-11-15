package query

import (
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/net/idna"
	"golang.org/x/text/unicode/norm"
)

// The presentation of values (ADR 0027, "Presenting values"): every value of
// a sentence is written by one function of its type, and by nothing else, so
// that a value can never be read as the fixed words around it. Texts, topics,
// lists, addresses and mailbox labels are quoted, their quotes and
// backslashes escaped; domains, media types, times, sizes, mailbox addresses
// and identifiers are written in characters that hold no quote, line break or
// separator, and a value that does not have the shape of its type is quoted
// as a text is instead, so a Canonical built by hand cannot inject words
// either.

// openQuote and closeQuote delimit a quoted value.
const (
	openQuote  = "“"
	closeQuote = "”"
)

// escapedQuote reports a quotation mark written with a backslash before it
// inside a quoted value: every code point of Unicode's Quotation_Mark
// property except the single quotes U+0027, U+2018 and U+2019, which names
// and words hold ("O'Neil", "don't").
func escapedQuote(r rune) bool {
	return r != '\'' && r != '‘' && r != '’' && unicode.Is(unicode.Quotation_Mark, r)
}

// shownAsIs reports whether r is written as itself inside quotes: a
// printable code point that shows as itself, distinctly. White space other
// than U+0020, U+2800 (a blank Braille pattern), the Hangul fillers U+115F,
// U+1160, U+3164 and U+FFA0, and every default-ignorable code point read as
// a gap or as nothing, so a person could not tell "a⠀b" from "a b"; they
// and anything not printable, which only a value that was not normalised
// holds, are written as \u{hex}, so they can neither hide nor reorder the
// words around them.
func shownAsIs(r rune) bool {
	switch {
	case r == ' ':
		return true
	case unicode.Is(unicode.White_Space, r), r == '\u2800', r == '\u115f', r == '\u1160', r == '\u3164', r == '\uffa0', ignorable(r):
		return false
	}
	return unicode.IsPrint(r)
}

// escapeText writes s for inside quotes: a backslash before every backslash
// and every quotation mark escapedQuote reports, \u{hex} for a code point
// not shown as itself, and \x{hex} for a byte that is not UTF-8. No escape writes a quotation
// mark, so the first unescaped closing quote ends the value.
func escapeText(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		r, n := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && n == 1:
			fmt.Fprintf(&b, `\x{%02X}`, s[i])
		case r == '\\' || escapedQuote(r):
			b.WriteByte('\\')
			b.WriteString(s[i : i+n])
		case !shownAsIs(r):
			fmt.Fprintf(&b, `\u{%X}`, r)
		default:
			b.WriteString(s[i : i+n])
		}
		i += n
	}
	return b.String()
}

// presentText writes a text, a topic, a list identifier or a label: inside
// “ and ”, escaped.
func presentText(s string) string {
	return openQuote + escapeText(s) + closeQuote
}

// asciiDomain reports whether d has the shape of a canonical domain: lower
// case letters, digits, dots and hyphens, which hold no quote, space or
// separator.
func asciiDomain(d string) bool {
	if d == "" {
		return false
	}
	for i := 0; i < len(d); i++ {
		c := d[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '.' && c != '-' {
			return false
		}
	}
	return true
}

// unicodeDomain is the Unicode form of a canonical domain, or the domain
// itself when it has none: when UTS 46 does not give one, when the Unicode
// form holds anything but letters, marks, digits, dots and hyphens, or when
// it does not give back exactly d, so the form shown is never another
// domain's.
func unicodeDomain(d string) string {
	u, err := idna.Lookup.ToUnicode(d)
	if err != nil || u == d {
		return d
	}
	for _, r := range u {
		if r != '.' && r != '-' && !unicode.In(r, unicode.L, unicode.M, unicode.N) {
			return d
		}
	}
	if back, err := idna.Lookup.ToASCII(u); err != nil || back != d {
		return d
	}
	return u
}

// presentDomain writes a domain: its Unicode form, then its ASCII form in
// brackets, when they differ; the ASCII form otherwise. Never quoted, unless
// it is not of a domain's characters.
func presentDomain(d string) string {
	if !asciiDomain(d) {
		return presentText(d)
	}
	if u := unicodeDomain(d); u != d {
		return u + " (" + d + ")"
	}
	return d
}

// presentAddress writes an address: its local part and its domain inside
// quotes, as a text is, and when the domain has a Unicode form, that form
// inside the quotes and the ASCII form in brackets after them.
func presentAddress(a string) string {
	if at := strings.LastIndexByte(a, '@'); at >= 0 && asciiDomain(a[at+1:]) {
		if u := unicodeDomain(a[at+1:]); u != a[at+1:] {
			return presentText(a[:at+1]+u) + " (" + a[at+1:] + ")"
		}
	}
	return presentText(a)
}

// mediaToken reports whether s is a type or a subtype of a canonical media
// type (RFC 6838's characters, lower case).
func mediaToken(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && !strings.ContainsRune("!#$&^_.+-", rune(c)) {
			return false
		}
	}
	return true
}

// anyMediaType is the type T of a value T/*, when it is one.
func anyMediaType(v string) (string, bool) {
	t, ok := strings.CutSuffix(v, "/*")
	return t, ok && mediaToken(t)
}

// presentMediaType writes a media type as it is, or quoted when it is not
// of a media type's characters.
func presentMediaType(v string) string {
	if t, s, ok := strings.Cut(v, "/"); ok && mediaToken(t) && mediaToken(s) {
		return v
	}
	return presentText(v)
}

// presentTime writes a canonical time in UTC, to the minute or the second
// only when it has them, and reports whether it was a time; anything else is
// quoted.
func presentTime(v string, l *language) (string, bool) {
	t, err := time.Parse(time.RFC3339, v)
	if err != nil || t.Format(canonicalTime) != v {
		return presentText(v), false
	}
	s := strconv.Itoa(t.Day()) + " " + l.months[t.Month()-1] + " " + strconv.Itoa(t.Year())
	switch {
	case t.Second() != 0:
		s += fmt.Sprintf(", %02d:%02d:%02d", t.Hour(), t.Minute(), t.Second())
	case t.Hour() != 0 || t.Minute() != 0:
		s += fmt.Sprintf(", %02d:%02d", t.Hour(), t.Minute())
	}
	return s, true
}

// presentSize writes a canonical size in megabytes when it is a whole number
// of them, in kilobytes when it is one of those, and in bytes otherwise, with
// the thousands grouped: one way to write each number. Anything else is
// quoted.
func presentSize(v string, l *language) string {
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 1 || strconv.FormatInt(n, 10) != v {
		return presentText(v)
	}
	switch {
	case n%1_000_000 == 0:
		return groupThousands(n/1_000_000, l) + l.megabytes
	case n%1_000 == 0:
		return groupThousands(n/1_000, l) + l.kilobytes
	case n == 1:
		return "1" + l.byte
	}
	return groupThousands(n, l) + l.bytes
}

func groupThousands(n int64, l *language) string {
	s := strconv.FormatInt(n, 10)
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteString(l.thousands)
		}
		b.WriteRune(c)
	}
	return b.String()
}

// mailboxID reports whether id has the shape of a mailbox's identifier:
// "mbx_" and 26 base32 characters, lower case.
func mailboxID(id string) bool {
	rest, ok := strings.CutPrefix(id, "mbx_")
	if !ok || len(rest) != 26 {
		return false
	}
	for i := 0; i < len(rest); i++ {
		if c := rest[i]; (c < 'a' || c > 'z') && (c < '2' || c > '7') {
			return false
		}
	}
	return true
}

// mailboxAddress reports whether a has the shape of an address Permaily
// makes for a mailbox: one @, and on each side lower-case letters, digits,
// dots, hyphens, underscores and plus signs.
func mailboxAddress(a string) bool {
	local, domain, ok := strings.Cut(a, "@")
	if !ok || local == "" || !asciiDomain(domain) {
		return false
	}
	for i := 0; i < len(local); i++ {
		c := local[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && !strings.ContainsRune(".-_+", rune(c)) {
			return false
		}
	}
	return true
}

// mailboxLabel reports whether a label can name a mailbox: not empty, and one
// printable line (as domain.MailboxLabel holds every label to).
func mailboxLabel(s string) bool {
	return s != "" && utf8.ValidString(s) && !strings.ContainsFunc(s, func(r rune) bool { return !unicode.IsPrint(r) })
}

// labelKey is what two labels are compared by to tell whether one is
// unique: two labels that read alike once compatibility forms and case are
// set aside are not, and both mailboxes are named by their addresses.
func labelKey(s string) string {
	return strings.ToLower(norm.NFKC.String(s))
}

// mailboxName is what a sentence calls one mailbox: fixed words, a value,
// and fixed words after it.
type mailboxName struct {
	before, value, after string
}

// mailboxNames are the names of a context's mailboxes, by identifier: the
// fixed words "your mailbox" and the label, quoted, when it is not empty and
// no other mailbox has one that reads alike; otherwise its address when it
// has the shape of one Permaily makes and no other mailbox has it; otherwise
// (a context no account gives) its identifier in brackets. So no two mailboxes of the
// context are named alike, and none like a mailbox outside it, which reads
// "a mailbox you disconnected (mbx_…)".
func mailboxNames(c Context, l *language) map[string]mailboxName {
	labels := make(map[string]int, len(c.Mailboxes))
	addresses := make(map[string]int, len(c.Mailboxes))
	for _, m := range c.Mailboxes {
		if mailboxLabel(m.Label) {
			labels[labelKey(m.Label)]++
		}
		addresses[m.Address]++
	}
	names := make(map[string]mailboxName, len(c.Mailboxes))
	for id, m := range c.Mailboxes {
		switch {
		case mailboxLabel(m.Label) && labels[labelKey(m.Label)] == 1:
			names[id] = mailboxName{l.yourMailbox, presentText(m.Label), ""}
		case mailboxAddress(m.Address) && addresses[m.Address] == 1:
			names[id] = mailboxName{l.yourMailbox, m.Address, ""}
		default:
			names[id] = mailboxName{l.yourMailbox + "(", presentMailboxID(id), ")"}
		}
	}
	return names
}

// presentMailboxID writes a mailbox's identifier as it is, or quoted when it
// does not have the shape of one.
func presentMailboxID(id string) string {
	if mailboxID(id) {
		return id
	}
	return presentText(id)
}
