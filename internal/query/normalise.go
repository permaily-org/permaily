package query

import (
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/net/idna"
	"golang.org/x/text/unicode/norm"

	"github.com/permaily-org/permaily/internal/contracts"
)

// How values and the fields of a message are normalised (ADR 0027, "Values,
// and how each is normalised"). Every function here is used for both sides,
// so that comparing is comparing bytes: Parse refuses a value that does not
// normalise, and Eval makes a field that does not normalise Unknown.

// maxChangingRounds is how many rounds of the text normaliser may change a
// value; the round after them must change nothing (ADR 0027, "Text").
const maxChangingRounds = 3

// fixpoint applies round until it changes nothing: at most maxChangingRounds
// rounds that change s, then one that must not. ok is false when s still
// changes in that last round. Its result r is a fixed point of round, so
// fixpoint(r, round) is r: the bounded normaliser is idempotent wherever it
// gives a result.
func fixpoint(s string, round func(string) string) (r string, ok bool) {
	for range maxChangingRounds + 1 {
		next := round(s)
		if next == s {
			return s, true
		}
		s = next
	}
	return "", false
}

// textRound is one round of the text normaliser: NFKC, every default
// ignorable code point removed, simple case folding to one member of each
// orbit, every run of white space made one space and the ends trimmed.
func textRound(s string) string {
	s = norm.NFKC.String(s)
	var b strings.Builder
	b.Grow(len(s))
	space := false
	for _, r := range s {
		switch {
		case ignorable(r):
			continue
		case unicode.Is(unicode.White_Space, r):
			space = true
			continue
		}
		if space && b.Len() > 0 {
			b.WriteByte(' ')
		}
		space = false
		b.WriteRune(fold(r))
	}
	return b.String()
}

// normaliseText is the text of a value or a field, normalised, or ok false
// when it does not settle within its rounds. Invalid UTF-8 becomes U+FFFD
// first: a value's is refused before it gets here.
func normaliseText(s string) (string, bool) {
	return fixpoint(strings.ToValidUTF8(s, "�"), textRound)
}

// ignorable reports a default-ignorable code point, derived as Unicode
// derives Default_Ignorable_Code_Point (UAX #44) from the standard
// library's tables: Other_Default_Ignorable_Code_Point, Cf and
// Variation_Selector, less White_Space, Prepended_Concatenation_Mark and
// U+FFF9 to U+FFFB (the interlinear annotation characters).
func ignorable(r rune) bool {
	if !unicode.In(r, unicode.Other_Default_Ignorable_Code_Point, unicode.Cf, unicode.Variation_Selector) {
		return false
	}
	if r >= 0xFFF9 && r <= 0xFFFB {
		return false
	}
	return !unicode.In(r, unicode.White_Space, unicode.Prepended_Concatenation_Mark)
}

// fold is simple case folding to one member of r's orbit under
// unicode.SimpleFold: the lower case of its smallest member when that is in
// the orbit, otherwise the smallest member. So A and a are a; Σ, σ and ς are
// σ; K and the Kelvin sign are k; ß and ẞ are ß; İ and ı stay themselves.
func fold(r rune) rune {
	smallest := r
	for f := unicode.SimpleFold(r); f != r; f = unicode.SimpleFold(f) {
		smallest = min(smallest, f)
	}
	lower := unicode.ToLower(smallest)
	for f := unicode.SimpleFold(smallest); f != smallest; f = unicode.SimpleFold(f) {
		if f == lower {
			return lower
		}
	}
	return smallest
}

// foldString folds every code point of s.
func foldString(s string) string {
	return strings.Map(fold, s)
}

// checkText is a text value (of contains, or a topic) as Parse accepts it:
// valid UTF-8, normalised, of 1 to MaxTextRunes characters and at most
// MaxTextBytes bytes, with a letter or a digit, no control, private-use,
// surrogate, unassigned or format code point, and no word whose letters mix
// Latin, Greek and Cyrillic.
func checkText(s string) (string, contracts.FormulaReason) {
	if !utf8.ValidString(s) {
		return "", contracts.FormulaInvalidValue
	}
	n, ok := normaliseText(s)
	if !ok {
		return "", contracts.FormulaInvalidValue
	}
	if len(n) > contracts.MaxTextBytes || utf8.RuneCountInString(n) > contracts.MaxTextRunes {
		return "", contracts.FormulaValueTooLong
	}
	letter := false
	for _, r := range n {
		if !assigned(r) || unicode.In(r, unicode.Cc, unicode.Co, unicode.Cs, unicode.Cf) {
			return "", contracts.FormulaInvalidValue
		}
		letter = letter || unicode.IsLetter(r) || unicode.IsDigit(r)
	}
	if !letter {
		return "", contracts.FormulaInvalidValue
	}
	for word := range strings.SplitSeq(n, " ") {
		if mixedScripts(word) {
			return "", contracts.FormulaConfusableValue
		}
	}
	return n, ""
}

// assigned reports a code point Unicode assigned (in the standard library's
// version of it): one of a general category other than Cn. The standard
// library's C holds Cn too, so its other parts are named.
func assigned(r rune) bool {
	return unicode.In(r, unicode.L, unicode.M, unicode.N, unicode.P, unicode.S, unicode.Z, unicode.Cc, unicode.Cf, unicode.Co, unicode.Cs)
}

// mixedScripts reports whether the letters of s are of more than one of
// Latin, Greek and Cyrillic, the scripts whose letters look alike (UTS 39's
// concern, of which only this case is taken).
func mixedScripts(s string) bool {
	var latin, greek, cyrillic bool
	for _, r := range s {
		if !unicode.IsLetter(r) {
			continue
		}
		latin = latin || unicode.Is(unicode.Latin, r)
		greek = greek || unicode.Is(unicode.Greek, r)
		cyrillic = cyrillic || unicode.Is(unicode.Cyrillic, r)
	}
	n := 0
	for _, b := range []bool{latin, greek, cyrillic} {
		if b {
			n++
		}
	}
	return n > 1
}

// Domains (ADR 0027, "Domains").
const (
	maxDomainBytes = 253
	maxLabelBytes  = 63
)

// normaliseDomain is a domain's canonical ASCII form: a trailing dot
// removed, UTS 46 to ASCII with idna's lookup profile (non-transitional,
// STD3 rules, the hyphen, joiner and bidi checks), lower case, at least two
// labels of at most 63 bytes, at most 253 in all. confusable is set when a
// label's Unicode form mixes Latin, Greek and Cyrillic: a value is refused
// for it, a message's domain is not (it equals no value, and it is still
// under the domains it is under).
func normaliseDomain(s string) (ascii string, confusable bool, reason contracts.FormulaReason) {
	if !utf8.ValidString(s) {
		return "", false, contracts.FormulaInvalidValue
	}
	s = strings.TrimSuffix(s, ".")
	if len(s) > 4*maxDomainBytes { // far past any domain: refused before IDNA reads it
		return "", false, contracts.FormulaValueTooLong
	}
	a, err := idna.Lookup.ToASCII(s)
	if err != nil {
		return "", false, contracts.FormulaInvalidValue
	}
	a = strings.ToLower(a)
	labels := strings.Split(a, ".")
	if len(labels) < 2 {
		return "", false, contracts.FormulaInvalidValue
	}
	for _, l := range labels {
		if l == "" {
			return "", false, contracts.FormulaInvalidValue
		}
		for i := range len(l) {
			c := l[i]
			if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '-' {
				return "", false, contracts.FormulaInvalidValue
			}
		}
		if len(l) > maxLabelBytes {
			return "", false, contracts.FormulaValueTooLong
		}
		if u, err := idna.Lookup.ToUnicode(l); err == nil && mixedScripts(u) {
			confusable = true
		}
	}
	if len(a) > maxDomainBytes {
		return "", false, contracts.FormulaValueTooLong
	}
	return a, confusable, ""
}

// underDomain reports whether domain d is parent or one of its subdomains;
// both canonical.
func underDomain(d, parent string) bool {
	return d == parent || strings.HasSuffix(d, "."+parent)
}

// Addresses (ADR 0027, "Addresses").
const maxLocalBytes = 64

// normaliseAddress is an address's canonical form, local part and ASCII
// domain: split at the last @; the domain as normaliseDomain; the local part
// unquoted when it is one quoted string (its quotes and quoting backslashes
// removed), NFC and folded (until it settles), of 1 to 64 bytes, printable,
// with no space, control, quotation mark or backslash left. An address is
// decided once, whole: when either half fails, the address is not valid, and
// a message's address that is not valid makes every condition over it,
// address and domain alike, Unknown. confusable is set when the local part's
// letters or a label of the domain mix Latin, Greek and Cyrillic.
//
// A quotation mark or a backslash left in the local part once unquoted is
// refused (and a message's makes the address not valid): the canonical form
// has no quoting, so it reads back as itself, and a message's address as
// stored (unquoted by net/mail) is never read as another.
func normaliseAddress(s string) (local, domain string, confusable bool, reason contracts.FormulaReason) {
	if !utf8.ValidString(s) {
		return "", "", false, contracts.FormulaInvalidValue
	}
	at := strings.LastIndexByte(s, '@')
	if at < 0 {
		return "", "", false, contracts.FormulaInvalidValue
	}
	l, ok := unquote(s[:at])
	if !ok {
		return "", "", false, contracts.FormulaInvalidValue
	}
	if len(l) > 4*maxLocalBytes {
		return "", "", false, contracts.FormulaValueTooLong
	}
	l, ok = fixpoint(l, func(x string) string { return foldString(norm.NFC.String(x)) })
	if !ok || l == "" {
		return "", "", false, contracts.FormulaInvalidValue
	}
	for _, r := range l {
		if r == ' ' || r == '"' || r == '\\' || !unicode.IsPrint(r) {
			return "", "", false, contracts.FormulaInvalidValue
		}
	}
	d, dc, reason := normaliseDomain(s[at+1:])
	if reason != "" {
		return "", "", false, reason
	}
	if len(l) > maxLocalBytes {
		return "", "", false, contracts.FormulaValueTooLong
	}
	return l, d, dc || mixedScripts(l), ""
}

// unquote takes a local part written as one quoted string out of its quotes
// and its quoting backslashes; any other local part is as it is. ok is false
// for a quoted string that does not end where the local part ends.
func unquote(l string) (string, bool) {
	if len(l) < 2 || l[0] != '"' {
		return l, true
	}
	var b strings.Builder
	for i := 1; i < len(l); i++ {
		switch c := l[i]; c {
		case '\\':
			if i+1 >= len(l) {
				return "", false
			}
			i++
			b.WriteByte(l[i])
		case '"':
			return b.String(), i == len(l)-1
		default:
			b.WriteByte(c)
		}
	}
	return "", false
}

// Media types (ADR 0027, "Media types").
var topLevelTypes = []string{"application", "audio", "font", "image", "message", "model", "multipart", "text", "video"}

const maxSubtypeBytes = 127

// checkMediaType is a media type value: lower case, a listed top-level type
// and a subtype that is * or 1 to 127 of a-z, 0-9 and !#$&^_.+-.
func checkMediaType(s string) (string, contracts.FormulaReason) {
	s = strings.ToLower(s)
	t, sub, ok := strings.Cut(s, "/")
	if !ok || !isTopLevel(t) || sub == "" {
		return "", contracts.FormulaInvalidValue
	}
	if sub == "*" {
		return s, ""
	}
	if len(sub) > maxSubtypeBytes {
		return "", contracts.FormulaValueTooLong
	}
	for i := range len(sub) {
		c := sub[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && !strings.ContainsRune("!#$&^_.+-", rune(c)) {
			return "", contracts.FormulaInvalidValue
		}
	}
	return s, ""
}

func isTopLevel(t string) bool {
	for _, x := range topLevelTypes {
		if t == x {
			return true
		}
	}
	return false
}

// messageMediaType is a message's media type as it is compared: cut at its
// first ';', trimmed and lowered. It is never refused: an unlisted or
// malformed one matches only the conditions it equals.
func messageMediaType(s string) string {
	s, _, _ = strings.Cut(s, ";")
	return strings.ToLower(strings.TrimSpace(s))
}

// typeMatches reports whether a message's media type (messageMediaType)
// satisfies the canonical value v: equal, or v is T/* and the message's type
// is well formed and of top-level type T.
func typeMatches(m, v string) bool {
	if m == v {
		return true
	}
	t, ok := strings.CutSuffix(v, "/*")
	if !ok {
		return false
	}
	mt, _, _ := strings.Cut(m, "/")
	_, reason := checkMediaType(m)
	return reason == "" && mt == t
}

// List identifiers (ADR 0027, "List identifiers").
const maxListBytes = 255

// checkList is a list identifier value: lower case, 1 to 255 bytes of the
// characters RFC 2919 allows in one (dot-atom-text: atext and '.').
func checkList(s string) (string, contracts.FormulaReason) {
	s = strings.ToLower(s)
	if s == "" {
		return "", contracts.FormulaInvalidValue
	}
	if len(s) > maxListBytes {
		return "", contracts.FormulaValueTooLong
	}
	for i := range len(s) {
		c := s[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && !strings.ContainsRune("!#$%&'*+-/=?^_`{|}~.", rune(c)) {
			return "", contracts.FormulaInvalidValue
		}
	}
	return s, ""
}

// Times (ADR 0027, "Times").
var (
	earliestTime = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	latestTime   = time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)
)

// canonicalTime is the layout of a canonical time.
const canonicalTime = "2006-01-02T15:04:05Z"

// checkTime is a time value: RFC 3339 with seconds and an offset (Z or
// ±hh:mm), no fraction, from 2000-01-01 to 2100-01-01; canonical in UTC.
func checkTime(s string) (string, time.Time, contracts.FormulaReason) {
	// yyyy-mm-ddThh:mm:ss then Z or ±hh:mm
	if len(s) != 20 && len(s) != 25 {
		return "", time.Time{}, contracts.FormulaInvalidValue
	}
	for i := range len(s) {
		c := s[i]
		var ok bool
		switch i {
		case 4, 7:
			ok = c == '-'
		case 10:
			ok = c == 'T' || c == 't'
		case 13, 16:
			ok = c == ':'
		case 19:
			ok = (len(s) == 20 && (c == 'Z' || c == 'z')) || (len(s) == 25 && (c == '+' || c == '-'))
		case 22:
			ok = c == ':'
		default:
			ok = c >= '0' && c <= '9'
		}
		if !ok {
			return "", time.Time{}, contracts.FormulaInvalidValue
		}
	}
	if len(s) == 25 && (s[20:22] > "23" || s[23:25] > "59") {
		return "", time.Time{}, contracts.FormulaInvalidValue
	}
	t, err := time.Parse(time.RFC3339, strings.ToUpper(s)) // RFC 3339 allows t and z
	if err != nil {
		return "", time.Time{}, contracts.FormulaInvalidValue
	}
	t = t.UTC()
	if t.Before(earliestTime) || t.After(latestTime) {
		return "", time.Time{}, contracts.FormulaInvalidValue
	}
	return t.Format(canonicalTime), t, ""
}

// Sizes (ADR 0027, "Sizes").
const maxSize = 32 << 20

// checkSize is a size value: a decimal integer from 1 to 32 MiB, without
// sign, leading zero or separator.
func checkSize(s string) (string, int64, contracts.FormulaReason) {
	if s == "" || len(s) > 8 || s[0] == '0' {
		return "", 0, contracts.FormulaInvalidValue
	}
	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return "", 0, contracts.FormulaInvalidValue
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 1 || n > maxSize {
		return "", 0, contracts.FormulaInvalidValue
	}
	return s, n, ""
}

// Mailboxes (ADR 0027, "Mailboxes").
const mailboxIDLen = len("mbx_") + 26

// checkMailbox is a mailbox value: mbx_ and 26 characters of the lower-case
// base32 the service writes them in (a-z, 2-7; and 0, 1, 8, 9, which the
// test fakes write), lowered.
func checkMailbox(s string) (string, contracts.FormulaReason) {
	s = strings.ToLower(s)
	if len(s) != mailboxIDLen || !strings.HasPrefix(s, "mbx_") {
		return "", contracts.FormulaInvalidValue
	}
	for i := len("mbx_"); i < len(s); i++ {
		if c := s[i]; (c < 'a' || c > 'z') && (c < '0' || c > '9') {
			return "", contracts.FormulaInvalidValue
		}
	}
	return s, ""
}
