package redact

import (
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/net/publicsuffix"
	"golang.org/x/text/unicode/norm"

	"github.com/permaily-org/permaily/internal/contracts"
)

// Fixed is the Redactor of the image (ADR 0040, "Fixed shapes" and
// "Removing by meaning", pull request 2): the six kinds of contracts.ShapeKinds
// found by one scanner over the text's runes, with no model and no regular
// expression, and the sentence split of SplitVersion. It holds nothing: the
// same text gives the same answer in every enclave of one Version.
type Fixed struct{}

var _ Redactor = Fixed{}

// tok is one rune of the text as the scanner reads it: transparent runes are
// left out (they belong to whatever piece is around them and never end one),
// an invalid byte is badRune, and a span already removed is one markRune
// standing where the marker will.
type tok struct {
	r          rune
	start, end int
}

const (
	badRune  rune = -1
	markRune rune = -2
)

// leastDigits is each digit kind's least count of digits; none has a most
// (ADR 0040, finding 2).
var leastDigits = map[contracts.ShapeKind]int{
	contracts.ShapePhoneNumber:   7,
	contracts.ShapeCardNumber:    12,
	contracts.ShapeAccountNumber: 8,
	contracts.ShapeNumericCode:   4,
}

// rescansMax bounds how many times Shapes reads the text again with what it
// found standing as markers, to find what a removal would make readable (a
// domain whose label a number was written into, a word that starts after a
// removed number). One rescan finds every such case the tests know of; the
// bound keeps the scanner linear whatever the input.
const rescansMax = 4

// Shapes implements Redactor.
func (Fixed) Shapes(text string, kinds []contracts.ShapeKind) []Piece {
	want := map[contracts.ShapeKind]bool{}
	for _, k := range kinds {
		if k.Valid() {
			want[k] = true
		}
	}
	if len(want) == 0 {
		return nil
	}
	found := map[contracts.ShapeKind][]Span{}
	t := tokens(text, nil)
	groups := digitGroups(t)
	for k, n := range leastDigits {
		if !want[k] {
			continue
		}
		for _, g := range groups {
			if g.count >= n {
				found[k] = append(found[k], g.span)
			}
		}
	}
	words(t, want, found)
	removed := allSpans(found)
	for range rescansMax {
		if len(removed) == 0 {
			break
		}
		words(tokens(text, removed), want, found)
		again := allSpans(found)
		if slices.Equal(again, removed) {
			break
		}
		removed = again
	}
	return pieces(found)
}

// words finds the kinds that are not digit groups (addresses, IBANs, links)
// in t, adding them to found.
func words(t []tok, want map[contracts.ShapeKind]bool, found map[contracts.ShapeKind][]Span) {
	if want[contracts.ShapeEmailAddress] {
		found[contracts.ShapeEmailAddress] = append(found[contracts.ShapeEmailAddress], emails(t)...)
	}
	if want[contracts.ShapeAccountNumber] {
		found[contracts.ShapeAccountNumber] = append(found[contracts.ShapeAccountNumber], ibans(t)...)
	}
	if want[contracts.ShapeLink] {
		found[contracts.ShapeLink] = append(found[contracts.ShapeLink], links(t)...)
	}
}

func allSpans(found map[contracts.ShapeKind][]Span) []Span {
	var all [][]Span
	for _, k := range contracts.ShapeKinds {
		all = append(all, found[k])
	}
	return Union(all...)
}

// pieces is what Shapes answers: each kind's spans joined, a span found as
// several kinds kept once under the first of contracts.ShapeKinds, sorted by
// Start and then by that order.
func pieces(found map[contracts.ShapeKind][]Span) []Piece {
	var out []Piece
	seen := map[Span]bool{}
	for _, k := range contracts.ShapeKinds {
		for _, s := range Union(found[k]) {
			if !seen[s] {
				seen[s] = true
				out = append(out, Piece{Span: s, Kind: k})
			}
		}
	}
	order := func(k contracts.ShapeKind) int { return slices.Index(contracts.ShapeKinds, k) }
	slices.SortFunc(out, func(a, b Piece) int {
		if a.Start != b.Start {
			return a.Start - b.Start
		}
		return order(a.Kind) - order(b.Kind)
	})
	return out
}

// tokens reads text rune by rune: transparent runes left out, an invalid
// byte badRune, and each of removed (sorted, apart, on rune boundaries) one
// markRune.
func tokens(text string, removed []Span) []tok {
	t := make([]tok, 0, len(text))
	for i := 0; i < len(text); {
		if len(removed) > 0 && i >= removed[0].Start {
			t = append(t, tok{markRune, removed[0].Start, removed[0].End})
			i = max(i, removed[0].End)
			removed = removed[1:]
			continue
		}
		r, n := utf8.DecodeRuneInString(text[i:])
		switch {
		case r == utf8.RuneError && n == 1:
			t = append(t, tok{badRune, i, i + 1})
		case !transparent(r):
			t = append(t, tok{r, i, i + n})
		}
		i += n
	}
	return t
}

// transparent says r is a default-ignorable or format rune (Cf: U+200B to
// U+200D, U+2060, U+FEFF, U+00AD and the rest), a variation selector or the
// combining grapheme joiner: it never ends a piece.
func transparent(r rune) bool {
	return unicode.Is(unicode.Cf, r) || (r >= 0xFE00 && r <= 0xFE0F) || (r >= 0xE0100 && r <= 0xE01EF) ||
		(r >= 0x180B && r <= 0x180F) || r == 0x034F
}

func digit(r rune) bool { return r >= 0 && unicode.IsDigit(r) }

// wordRune is a letter or a digit of any script: what a piece that starts or
// ends a word must not touch.
func wordRune(r rune) bool { return r >= 0 && (unicode.IsLetter(r) || unicode.IsDigit(r)) }

// space is white space as Unicode says (unicode.IsSpace).
func space(r rune) bool { return r >= 0 && unicode.IsSpace(r) }

// separator is what may stand between two digits of one group: a space of
// any width, a dash, a full stop, a comma, a slash or a parenthesis.
func separator(r rune) bool {
	if r < 0 {
		return false
	}
	switch r {
	case '.', ',', '/', '(', ')':
		return true
	}
	return unicode.Is(unicode.Zs, r) || unicode.Is(unicode.Pd, r)
}

// dot is a full stop that joins the labels of a domain: ASCII's, and the
// ideographic, fullwidth and halfwidth ones a domain is also written with.
func dot(r rune) bool { return r == '.' || r == 0x3002 || r == 0xFF0E || r == 0xFF61 }

// atSign is a rune whose NFKC form is "@" (TestTheAtSignsAreThoseNFKCMakesAt
// checks this set against golang.org/x/text over every rune).
func atSign(r rune) bool { return r == '@' || r == 0xFF20 || r == 0xFE6B }

// trailing is the punctuation a run's end gives back to the sentence.
func trailing(r rune) bool {
	switch r {
	case '.', ',', '!', '?', ')', ':':
		return true
	}
	return false
}

type group struct {
	span  Span
	count int
}

// digitGroups is every maximal digit group of t: digits any two consecutive
// of which are at most two separators apart, with a "+" or "(" just before
// the first digit and a ")" just after the last.
func digitGroups(t []tok) []group {
	var out []group
	for i := 0; i < len(t); {
		if !digit(t[i].r) {
			i++
			continue
		}
		first, last, count := i, i, 1
		for {
			k := last + 1
			for seps := 0; k < len(t) && seps < 2 && separator(t[k].r); seps++ {
				k++
			}
			if k >= len(t) || !digit(t[k].r) {
				break
			}
			last, count = k, count+1
		}
		from, to := first, last
		if from > 0 && t[from-1].r == '(' {
			from--
		}
		if from > 0 && t[from-1].r == '+' {
			from--
		}
		if to+1 < len(t) && t[to+1].r == ')' {
			to++
		}
		out = append(out, group{Span{t[from].start, t[to].end}, count})
		i = last + 1
	}
	return out
}

// emailRune may be part of an address's run: not white space, a control,
// one of <>()[]"',;:` or an invalid byte or a marker.
func emailRune(r rune) bool {
	if r < 0 || space(r) || unicode.IsControl(r) {
		return false
	}
	return !strings.ContainsRune("<>()[]\"',;:`", r)
}

// emails is every run of emailRune that holds an at sign with at least one
// rune before it and, after it (up to the next at sign), at least two
// labels joined by a dot; the run's trailing punctuation left out.
func emails(t []tok) []Span {
	var out []Span
	for i := 0; i < len(t); {
		if !emailRune(t[i].r) {
			i++
			continue
		}
		a, b := i, i
		for b+1 < len(t) && emailRune(t[b+1].r) {
			b++
		}
		i = b + 1
		for b >= a && trailing(t[b].r) {
			b--
		}
		if b > a && emailIn(t[a:b+1]) {
			out = append(out, Span{t[a].start, t[b].end})
		}
	}
	return out
}

func emailIn(run []tok) bool {
	at, labels, label := -1, 0, 0
	ok := func() bool { return at > 0 && labels+min(label, 1) >= 2 }
	for k, x := range run {
		switch {
		case atSign(x.r):
			if ok() {
				return true
			}
			at, labels, label = k, 0, 0
		case dot(x.r):
			labels += min(label, 1)
			label = 0
		default:
			label++
		}
	}
	return ok()
}

func upper(r rune) bool      { return r >= 'A' && r <= 'Z' }
func asciiDigit(r rune) bool { return r >= '0' && r <= '9' }
func ibanRune(r rune) bool   { return upper(r) || asciiDigit(r) }

// ibans is every IBAN of t: two uppercase letters and two digits, then 11
// to 30 uppercase letters and digits, either written together or in groups
// of up to four joined by single spaces; it starts and ends a word.
func ibans(t []tok) []Span {
	var out []Span
	wordAt := func(k int) bool { return k >= len(t) || !wordRune(t[k].r) }
	// run is how many IBAN runes start at k, up to five past the bound.
	run := func(k, most int) int {
		n := 0
		for k+n < len(t) && n <= most && ibanRune(t[k+n].r) {
			n++
		}
		return n
	}
	for i := 0; i < len(t); i++ {
		if !upper(t[i].r) || (i > 0 && wordRune(t[i-1].r)) {
			continue
		}
		n := run(i, 34)
		if n < 4 || n > 34 || !wordAt(i+n) || !upper(t[i+1].r) || !asciiDigit(t[i+2].r) || !asciiDigit(t[i+3].r) {
			continue
		}
		total, last := n, i+n-1
		if n == 4 {
			for j := i + 4; j+1 < len(t) && unicode.Is(unicode.Zs, t[j].r); {
				g := run(j+1, 4)
				if g == 0 || g > 4 || !wordAt(j+1+g) || total+g > 34 {
					break
				}
				total, last, j = total+g, j+g, j+1+g
			}
		}
		if total >= 15 {
			out = append(out, Span{t[i].start, t[last].end})
			i = last
		}
	}
	return out
}

// linkRune may be part of a link's run: anything but white space, an
// invalid byte (a marker is part of it: the text around a removal reads on).
func linkRune(r rune) bool { return r == markRune || (r >= 0 && !space(r)) }

func labelRune(r rune) bool {
	return r >= 0 && (unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsMark(r) || r == '-')
}

func schemeRune(r rune) bool {
	return r >= 0 && r < utf8.RuneSelf && (unicode.IsLetter(r) || unicode.IsDigit(r) || r == '+' || r == '.' || r == '-')
}

// links is every link of t: in each run of linkRune (its trailing
// punctuation left out), from a scheme and "://", "mailto:", "tel:" or
// "www." to the run's end; and before that, each domain of two or more
// labels whose last is under an ICANN suffix of the public suffix list,
// with what follows it to the run's end when that starts with "/", "?",
// "#" or ":".
func links(t []tok) []Span {
	var out []Span
	for i := 0; i < len(t); {
		if !linkRune(t[i].r) {
			i++
			continue
		}
		a, b := i, i
		for b+1 < len(t) && linkRune(t[b+1].r) {
			b++
		}
		i = b + 1
		for b >= a && trailing(t[b].r) {
			b--
		}
		if b < a {
			continue
		}
		run := t[a : b+1]
		p := prefixAt(run)
		out = append(out, domains(run[:p], run)...)
		if p < len(run) {
			out = append(out, Span{run[p].start, run[len(run)-1].end})
		}
	}
	return out
}

// prefixAt is the index in run of the first scheme, "mailto:", "tel:" or
// "www." that starts a word (len(run) when none does).
func prefixAt(run []tok) int {
	first := len(run)
	for k := 0; k+2 < len(run); k++ {
		if run[k].r != ':' || run[k+1].r != '/' || run[k+2].r != '/' {
			continue
		}
		s := k
		for s > 0 && schemeRune(run[s-1].r) {
			s--
		}
		for l := s; l < k; l++ {
			if unicode.IsLetter(run[l].r) {
				first = s
				break
			}
		}
		if first < len(run) {
			break
		}
	}
	for k := 0; k < first; k++ {
		if k > 0 && wordRune(run[k-1].r) {
			continue
		}
		if hasFold(run[k:], "www.") || hasFold(run[k:], "mailto:") || hasFold(run[k:], "tel:") {
			return k
		}
	}
	return first
}

func hasFold(run []tok, s string) bool {
	if len(run) < len(s) {
		return false
	}
	for k := range len(s) {
		if unicode.ToLower(run[k].r) != rune(s[k]) {
			return false
		}
	}
	return true
}

// domains is every domain in head (the part of run before a prefix), each
// with what follows it to run's end when that starts a path.
func domains(head, run []tok) []Span {
	var out []Span
	for i := 0; i < len(head); {
		if !labelRune(head[i].r) && !dot(head[i].r) {
			i++
			continue
		}
		j := i
		for j < len(head) && (labelRune(head[j].r) || dot(head[j].r)) {
			j++
		}
		// head[i:j] is labels and dots; each part between two dots in a row
		// is a candidate of its own.
		for x := i; x < j; {
			y := x
			for y < j && (!dot(head[y].r) || y+1 >= j || !dot(head[y+1].r)) {
				y++
			}
			if y < j {
				y++ // the first of the two dots
			}
			if s, e, ok := domain(head[x:y]); ok {
				end := head[x+e-1].end
				if next := x + e; next < len(run) && strings.ContainsRune("/?#:", run[next].r) {
					end = run[len(run)-1].end
				}
				out = append(out, Span{head[x+s].start, end})
			}
			x = y
		}
		i = j
	}
	return out
}

// domain says whether c (labels and dots, no two dots in a row but at its
// end) less its dots at either end is a domain under an ICANN suffix, and
// where, in c, that domain starts and ends.
func domain(c []tok) (start, end int, ok bool) {
	start, end = 0, len(c)
	for start < end && dot(c[start].r) {
		start++
	}
	for end > start && dot(c[end-1].r) {
		end--
	}
	last := end
	for last > start && !dot(c[last-1].r) {
		last--
	}
	if last == start {
		return 0, 0, false // one label
	}
	var b strings.Builder
	for _, x := range c[last:end] {
		b.WriteRune(x.r)
	}
	return start, end, icannTLD(b.String())
}

// icannTLD says label (a domain's last) is a top-level domain of the public
// suffix list's ICANN section, read as a domain name is (NFKC, lower case,
// punycode).
func icannTLD(label string) bool {
	label = strings.ToLower(norm.NFKC.String(label))
	if label == "" || strings.ContainsFunc(label, func(r rune) bool { return r == '.' || dot(r) }) {
		return false
	}
	if a, ok := punycode(label); ok {
		label = a
	} else {
		return false
	}
	// Asked under a label of its own, so a TLD whose rule is a wildcard
	// ("*.ck") is found, and a private rule on the TLD alone is not.
	_, icann := publicsuffix.PublicSuffix("a0." + label)
	return icann
}

// punycode is label as an IDNA A-label ("xn--" and RFC 3492's encoding),
// or label itself when it is ASCII.
func punycode(label string) (string, bool) {
	const (
		base, tmin, tmax, skew, damp = 36, 1, 26, 38, 700
		initialBias, initialN        = 72, 128
	)
	rs := []rune(label)
	var b strings.Builder
	basic := 0
	for _, r := range rs {
		if r < utf8.RuneSelf {
			b.WriteRune(r)
			basic++
		}
	}
	if basic == len(rs) {
		return label, true
	}
	if len(rs) > 63 {
		return "", false
	}
	if basic > 0 {
		b.WriteByte('-')
	}
	const digits = "abcdefghijklmnopqrstuvwxyz0123456789"
	digitOf := func(d int) byte { return digits[d] }
	adapt := func(delta, points int, first bool) int {
		if first {
			delta /= damp
		} else {
			delta /= 2
		}
		delta += delta / points
		k := 0
		for delta > ((base-tmin)*tmax)/2 {
			delta /= base - tmin
			k += base
		}
		return k + (base-tmin+1)*delta/(delta+skew)
	}
	n, delta, bias, h := initialN, 0, initialBias, basic
	for h < len(rs) {
		m := rune(0x7fffffff)
		for _, r := range rs {
			if int(r) >= n && r < m {
				m = r
			}
		}
		delta += (int(m) - n) * (h + 1)
		n = int(m)
		for _, r := range rs {
			if int(r) < n {
				delta++
			}
			if int(r) != n {
				continue
			}
			q := delta
			for k := base; ; k += base {
				tk := min(max(k-bias, tmin), tmax)
				if q < tk {
					break
				}
				b.WriteByte(digitOf(tk + (q-tk)%(base-tk)))
				q = (q - tk) / (base - tk)
			}
			b.WriteByte(digitOf(q))
			bias = adapt(delta, h+1, h == basic)
			delta = 0
			h++
		}
		delta++
		n++
	}
	return "xn--" + b.String(), true
}
