package query

import (
	"errors"
	"reflect"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/permaily-org/permaily/internal/contracts"
)

// parseSentence and parseParts read a sentence back into the formula it was
// written from (ADR 0027, "Faithful, exactly"). They are the check of the
// claim that no two canonical formulas read alike: each finds every formula
// whose sentence the input could be, by trying every template at every
// place and every way a value could end there, keeps those whose sentence
// Render writes is exactly the input, and fails unless there is one. So a
// value that could be read as fixed words, or two formulas or mailboxes that
// read alike, show as more than one reading. Nothing caps the readings
// before that filter, so an ambiguity cannot hide behind readings it drops.
// Test only: nothing in the service reads a sentence to decide anything.
//
// What the read-back proves: for each formula and Context it is given, no
// other formula of the templates, signs and values the presentations can
// write has the same plain text, or the same parts. What it does not: it
// shares with Render the table of templates, escapedQuote, the escapes it
// undoes and mailboxNames, so a quotation mark that both take for an
// ordinary character, a template whose English says something else, or a
// character a person cannot tell from another (U+2800 from U+0020) reads
// back and passes; and it reaches only the formulas it is given. Those are
// for the goldens written by hand (sentence_test.go, sentence_review_test.go)
// and the adversarial generators (sentence_gen_test.go, querytest).

// errNoReading and errAmbiguous say a sentence was not read back as one
// formula.
var (
	errNoReading = errors.New("no formula reads as this sentence")
	errAmbiguous = errors.New("more than one formula reads as this sentence")
)

// parseSentence reads a sentence's plain text.
func parseSentence(text string, c Context) (*Canonical, error) {
	r := newSentenceReader(c, nil)
	var found []*Canonical
	for _, f := range r.readText(text) {
		if Render(f, c).Text == text {
			found = appendNew(found, f)
		}
	}
	return single(found)
}

// parseParts reads a sentence's parts, Lead and Lines: fixed words must be
// fixed parts and every value a value part of its own, read whole.
func parseParts(s contracts.Sentence, c Context) (*Canonical, error) {
	var found []*Canonical
	for _, f := range readParts(s, c) {
		if reflect.DeepEqual(Render(f, c), s) {
			found = appendNew(found, f)
		}
	}
	return single(found)
}

// parseRule reads a rule's plain text: its lead, then each formula's
// sentence, each starting on a line that is not an item of a list.
func parseRule(text string, c Context) ([]*Canonical, error) {
	l := languageOf(c.Lang)
	lines := strings.Split(text, "\n")
	if lines[0] == l.rule {
		lines = lines[1:]
	}
	var chunks []string
	for _, line := range lines {
		if strings.HasPrefix(line, l.item) && len(chunks) > 0 {
			chunks[len(chunks)-1] += "\n" + line
		} else {
			chunks = append(chunks, line)
		}
	}
	var fs []*Canonical
	for _, chunk := range chunks {
		f, err := parseSentence(chunk, c)
		if err != nil {
			return nil, err
		}
		fs = append(fs, f)
	}
	if RenderRule(fs, c).Text != text {
		return nil, errNoReading
	}
	return fs, nil
}

func single(found []*Canonical) (*Canonical, error) {
	switch len(found) {
	case 0:
		return nil, errNoReading
	case 1:
		return found[0], nil
	}
	return nil, errAmbiguous
}

func appendNew(found []*Canonical, f *Canonical) []*Canonical {
	for _, g := range found {
		if sameFormula(g, f) {
			return found
		}
	}
	return append(found, f)
}

// sameFormula compares two formulas as a sentence says them: Never, and the
// groups and conditions in order (an empty All and a nil one alike).
func sameFormula(a, b *Canonical) bool {
	if a.Never || b.Never {
		return a.Never == b.Never
	}
	if len(a.Formula.All) != len(b.Formula.All) {
		return false
	}
	for i := range a.Formula.All {
		if !reflect.DeepEqual(a.Formula.All[i].Any, b.Formula.All[i].Any) {
			return false
		}
	}
	return true
}

// placeholder marks a value part in the string parseParts reads: NUL, the
// part's index, NUL. No fixed text holds a NUL (TestTheTables), and no value
// part does (a NUL in a value is written \u{0}).
const placeholder = "\x00"

type sentenceReader struct {
	l *language
	c Context
	// names are the context's mailbox names, by their fixed words around
	// the value, then by the value: the identifiers named so.
	names map[[2]string]map[string][]string
	// values are the value parts' texts, when reading parts.
	values []string
	memo   map[string]map[int][][]contracts.Condition
}

func newSentenceReader(c Context, values []string) *sentenceReader {
	l := languageOf(c.Lang)
	r := &sentenceReader{l: l, c: c, values: values, names: map[[2]string]map[string][]string{}, memo: map[string]map[int][][]contracts.Condition{}}
	for id, n := range mailboxNames(c, l) {
		k := [2]string{n.before, n.after}
		if r.names[k] == nil {
			r.names[k] = map[string][]string{}
		}
		r.names[k][n.value] = append(r.names[k][n.value], id)
	}
	return r
}

// readText finds every formula the plain text could be the sentence of.
func (r *sentenceReader) readText(text string) []*Canonical {
	l := r.l
	var out []*Canonical
	for _, body := range withoutUTC(text, l) {
		switch {
		case body == l.never:
			out = append(out, &Canonical{Never: true})
		case body == l.all:
			out = append(out, &Canonical{Formula: contracts.Formula{V: contracts.FormulaVersion}})
		case strings.HasPrefix(body, l.several+"\n"):
			items := strings.Split(strings.TrimPrefix(body, l.several), "\n")[1:]
			var lines []string
			for i, item := range items {
				line, ok := strings.CutPrefix(item, l.item)
				if i == len(items)-1 {
					line, ok = strings.CutSuffix(line, l.end)
					ok = ok && strings.HasPrefix(item, l.item)
				}
				if !ok {
					lines = nil
					break
				}
				lines = append(lines, line)
			}
			if len(lines) > 1 {
				out = append(out, r.formulas(lines)...)
			}
		case strings.HasPrefix(body, l.one) && strings.HasSuffix(body, l.end):
			out = append(out, r.formulas([]string{body[len(l.one) : len(body)-len(l.end)]})...)
		}
	}
	return out
}

// readParts finds every formula whose parts the sentence's could be.
func readParts(s contracts.Sentence, c Context) []*Canonical {
	var values []string
	flat := func(parts []contracts.SentencePart) string {
		var b strings.Builder
		for _, p := range parts {
			if p.Value {
				b.WriteString(placeholder + strconv.Itoa(len(values)) + placeholder)
				values = append(values, p.Text)
			} else {
				b.WriteString(p.Text)
			}
		}
		return b.String()
	}
	lead := flat(s.Lead)
	var lines []string
	for _, line := range s.Lines {
		lines = append(lines, flat(line.Parts))
	}
	r := newSentenceReader(c, values)
	l := r.l
	if len(lines) == 0 {
		return nil
	}
	var out []*Canonical
	for _, last := range withoutUTC(lines[len(lines)-1], l) {
		ls := append(append([]string(nil), lines[:len(lines)-1]...), last)
		switch {
		case lead == "" && len(ls) == 1 && ls[0] == l.never:
			out = append(out, &Canonical{Never: true})
		case lead == "" && len(ls) == 1 && ls[0] == l.all:
			out = append(out, &Canonical{Formula: contracts.Formula{V: contracts.FormulaVersion}})
		case lead == "" && len(ls) == 1 && strings.HasPrefix(ls[0], l.one) && strings.HasSuffix(ls[0], l.end):
			out = append(out, r.formulas([]string{ls[0][len(l.one) : len(ls[0])-len(l.end)]})...)
		case lead == l.several && len(ls) > 1 && strings.HasSuffix(last, l.end):
			ls[len(ls)-1] = strings.TrimSuffix(last, l.end)
			out = append(out, r.formulas(ls)...)
		}
	}
	return out
}

// withoutUTC is the text as it is, and without the note on UTC when it ends
// with it: both are tried.
func withoutUTC(text string, l *language) []string {
	if body, ok := strings.CutSuffix(text, l.utc); ok {
		return []string{text, body}
	}
	return []string{text}
}

// formulas is every formula of one group per line, each line read every way
// it can be.
func (r *sentenceReader) formulas(lines []string) []*Canonical {
	combos := [][]contracts.Group{nil}
	for _, line := range lines {
		readings := r.conditions(line, 0)
		var next [][]contracts.Group
		for _, combo := range combos {
			for _, conds := range readings {
				g := append(append([]contracts.Group(nil), combo...), contracts.Group{Any: conds})
				next = append(next, g)
			}
		}
		combos = next
	}
	out := make([]*Canonical, 0, len(combos))
	for _, groups := range combos {
		out = append(out, &Canonical{Formula: contracts.Formula{V: contracts.FormulaVersion, All: groups}})
	}
	return out
}

// conditions is every way s[pos:] reads as conditions joined by the
// separator.
func (r *sentenceReader) conditions(s string, pos int) [][]contracts.Condition {
	if m, ok := r.memo[s][pos]; ok {
		return m
	}
	var out [][]contracts.Condition
	for _, m := range r.condition(s, pos) {
		if m.end == len(s) {
			out = append(out, []contracts.Condition{m.cond})
		} else if strings.HasPrefix(s[m.end:], r.l.or) {
			for _, rest := range r.conditions(s, m.end+len(r.l.or)) {
				out = append(out, append([]contracts.Condition{m.cond}, rest...))
			}
		}
	}
	if r.memo[s] == nil {
		r.memo[s] = map[int][][]contracts.Condition{}
	}
	r.memo[s][pos] = out
	return out
}

type match struct {
	cond contracts.Condition
	end  int
}

// condition is every condition s[pos:] starts with: by every template whose
// fixed words it has, and every way its values can end.
func (r *sentenceReader) condition(s string, pos int) []match {
	var out []match
	try := func(key templateKey, tpl template) {
		if !strings.HasPrefix(s[pos:], tpl[0]) {
			return
		}
		for _, v := range r.slots(s, pos+len(tpl[0]), key, tpl[1:]) {
			c := contracts.Condition{Var: key.Var, Op: key.Op, Not: key.Not}
			switch {
			case len(v.values) == 3: // a condition with no template
				c = contracts.Condition{Var: contracts.Variable(v.values[0]), Op: contracts.Operator(v.values[1]), Value: v.values[2], Not: key.Not}
			case len(v.values) == 1 && key.AnyType:
				c.Value = v.values[0] + "/*"
			case len(v.values) == 1:
				c.Value = v.values[0]
			}
			end := v.end
			if c.Op == contracts.OpAbout || c.Op == contracts.OpMentions {
				if !strings.HasPrefix(s[end:], r.l.model) {
					continue
				}
				end += len(r.l.model)
			}
			out = append(out, match{c, end})
		}
	}
	for key, tpl := range r.l.templates {
		try(key, tpl)
	}
	try(templateKey{Not: false}, r.l.unknown[0])
	try(templateKey{Not: true}, r.l.unknown[1])
	return out
}

type filled struct {
	values []string
	end    int
}

// slots reads the values of a template and the fixed words after each.
func (r *sentenceReader) slots(s string, pos int, key templateKey, after []string) []filled {
	if len(after) == 0 {
		return []filled{{end: pos}}
	}
	var out []filled
	for _, v := range r.value(s, pos, key, len(after) > 1 || key.Var == "") {
		if !strings.HasPrefix(s[v.end:], after[0]) {
			continue
		}
		for _, rest := range r.slots(s, v.end+len(after[0]), key, after[1:]) {
			out = append(out, filled{append([]string{v.v}, rest.values...), rest.end})
		}
	}
	return out
}

type cand struct {
	v   string
	end int
}

// value is every value of the key's type that s[pos:] starts with. quoted
// says the slot is one of a condition with no template, always a text.
func (r *sentenceReader) value(s string, pos int, key templateKey, quoted bool) []cand {
	if quoted {
		return r.part(s, pos, scanQuoted)
	}
	switch key.Var {
	case contracts.VarFromAddress, contracts.VarRecipientAddress:
		return r.part(s, pos, scanAddress)
	case contracts.VarFromDomain, contracts.VarRecipientDomain, contracts.VarSignedBy:
		return r.part(s, pos, scanDomain)
	case contracts.VarAttachmentType:
		if key.AnyType {
			return r.part(s, pos, scanAnyType)
		}
		return r.part(s, pos, scanMediaType)
	case contracts.VarReceived:
		return r.part(s, pos, r.scanTime)
	case contracts.VarSize:
		return r.part(s, pos, r.scanSize)
	case contracts.VarMailbox:
		return r.mailbox(s, pos)
	}
	return r.part(s, pos, scanQuoted)
}

// part reads one value: from the text itself, or, reading parts, from the
// value part at pos, which the scanner must read whole.
func (r *sentenceReader) part(s string, pos int, scan func(string, int) []cand) []cand {
	if r.values == nil {
		return scan(s, pos)
	}
	rest, ok := strings.CutPrefix(s[pos:], placeholder)
	if !ok {
		return nil
	}
	i := strings.Index(rest, placeholder)
	n, err := strconv.Atoi(rest[:max(i, 0)])
	if i < 0 || err != nil || n >= len(r.values) {
		return nil
	}
	end := pos + len(placeholder) + i + len(placeholder)
	var out []cand
	for _, c := range scan(r.values[n], 0) {
		if c.end == len(r.values[n]) {
			out = append(out, cand{c.v, end})
		}
	}
	return out
}

// mailbox reads a mailbox: the fixed words of one of the context's names
// and its value, or the words of a disconnected mailbox and an identifier.
func (r *sentenceReader) mailbox(s string, pos int) []cand {
	var out []cand
	for k, byValue := range r.names {
		if !strings.HasPrefix(s[pos:], k[0]) {
			continue
		}
		scan := func(t string, p int) []cand {
			var cs []cand
			for v := range byValue {
				if strings.HasPrefix(t[p:], v) {
					cs = append(cs, cand{v, p + len(v)})
				}
			}
			return cs
		}
		for _, c := range r.part(s, pos+len(k[0]), scan) {
			if strings.HasPrefix(s[c.end:], k[1]) {
				for _, id := range byValue[c.v] {
					out = append(out, cand{id, c.end + len(k[1])})
				}
			}
		}
	}
	d := r.l.disconnected
	if strings.HasPrefix(s[pos:], d[0]) {
		for _, c := range r.part(s, pos+len(d[0]), scanID) {
			if strings.HasPrefix(s[c.end:], d[1]) {
				out = append(out, cand{c.v, c.end + len(d[1])})
			}
		}
	}
	return out
}

// scanQuoted reads a quoted value: “, escaped text with no unescaped quote,
// ”.
func scanQuoted(s string, pos int) []cand {
	if !strings.HasPrefix(s[pos:], openQuote) {
		return nil
	}
	var b strings.Builder
	for i := pos + len(openQuote); i < len(s); {
		if strings.HasPrefix(s[i:], closeQuote) {
			return []cand{{b.String(), i + len(closeQuote)}}
		}
		r, n := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == '\\':
			e, en, ok := unescape(s[i+1:])
			if !ok {
				return nil
			}
			b.WriteString(e)
			i += 1 + en
		case escapedQuote(r) || r == utf8.RuneError && n == 1:
			return nil
		default:
			b.WriteString(s[i : i+n])
			i += n
		}
	}
	return nil
}

// unescape reads what follows a backslash: a backslash or a double quote,
// \u{hex} or \x{hex}.
func unescape(s string) (string, int, bool) {
	r, n := utf8.DecodeRuneInString(s)
	if r == '\\' || escapedQuote(r) {
		return s[:n], n, true
	}
	if (r == 'u' || r == 'x') && strings.HasPrefix(s[1:], "{") {
		end := strings.IndexByte(s, '}')
		if end < 0 {
			return "", 0, false
		}
		v, err := strconv.ParseUint(s[2:end], 16, 32)
		if err != nil {
			return "", 0, false
		}
		if r == 'x' {
			return string([]byte{byte(v)}), end + 1, v <= 0xff
		}
		return string(rune(v)), end + 1, utf8.ValidRune(rune(v))
	}
	return "", 0, false
}

// bracketed reads " (" and an ASCII domain and ")".
func bracketed(s string, pos int) (string, int, bool) {
	rest, ok := strings.CutPrefix(s[pos:], " (")
	if !ok {
		return "", 0, false
	}
	d, _, ok := strings.Cut(rest, ")")
	if !ok || !asciiDomain(d) {
		return "", 0, false
	}
	return d, pos + 2 + len(d) + 1, true
}

func scanAddress(s string, pos int) []cand {
	out := scanQuoted(s, pos)
	if len(out) == 1 {
		if d, end, ok := bracketed(s, out[0].end); ok {
			c := out[0].v
			if at := strings.LastIndexByte(c, '@'); at >= 0 {
				out = append(out, cand{c[:at+1] + d, end})
			}
		}
	}
	return out
}

// token is the run of s from pos of runes keep holds.
func token(s string, pos int, keep func(rune) bool) string {
	end := pos
	for end < len(s) {
		r, n := utf8.DecodeRuneInString(s[end:])
		if !keep(r) {
			break
		}
		end += n
	}
	return s[pos:end]
}

func scanDomain(s string, pos int) []cand {
	out := scanQuoted(s, pos)
	tok := token(s, pos, func(r rune) bool { return r != ' ' && r != ',' && r != '\n' && r != 0 && !escapedQuote(r) })
	if tok == "" {
		return out
	}
	if asciiDomain(tok) {
		out = append(out, cand{tok, pos + len(tok)})
	}
	if d, end, ok := bracketed(s, pos+len(tok)); ok {
		out = append(out, cand{d, end})
	}
	return out
}

func mediaRune(r rune) bool {
	return r < utf8.RuneSelf && (r == '/' || r == '*' || mediaToken(string(r)))
}

// prefixes are the candidates of an unquoted token that may be followed by
// one of its own characters (a media type's "." before the sentence's).
func prefixes(tok string, pos int) []cand {
	var out []cand
	for i := 1; i <= len(tok); i++ {
		out = append(out, cand{tok[:i], pos + i})
	}
	return out
}

func scanMediaType(s string, pos int) []cand {
	return append(scanQuoted(s, pos), prefixes(token(s, pos, mediaRune), pos)...)
}

func scanAnyType(s string, pos int) []cand {
	return prefixes(token(s, pos, func(r rune) bool { return r != '/' && r != '*' && mediaRune(r) }), pos)
}

func scanID(s string, pos int) []cand {
	tok := token(s, pos, func(r rune) bool { return r == '_' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' })
	return append(scanQuoted(s, pos), prefixes(tok, pos)...)
}

func digits(s string, pos int) string {
	return token(s, pos, func(r rune) bool { return r >= '0' && r <= '9' })
}

// scanTime reads a time as presentTime writes it: day, month and year, and
// the time of day to the minute or the second.
func (r *sentenceReader) scanTime(s string, pos int) []cand {
	out := scanQuoted(s, pos)
	day := digits(s, pos)
	p := pos + len(day)
	if day == "" || !strings.HasPrefix(s[p:], " ") {
		return out
	}
	p++
	month := -1
	for i, m := range r.l.months {
		if strings.HasPrefix(s[p:], m+" ") {
			month, p = i, p+len(m)+1
			break
		}
	}
	year := digits(s, p)
	if month < 0 || year == "" {
		return out
	}
	p += len(year)
	d, _ := strconv.Atoi(day)
	y, _ := strconv.Atoi(year)
	at := func(h, m, sec, end int) {
		t := time.Date(y, time.Month(month+1), d, h, m, sec, 0, time.UTC)
		out = append(out, cand{t.Format(canonicalTime), end})
	}
	at(0, 0, 0, p)
	clock := func(q int) (int, int, bool) {
		n := digits(s, q)
		v, _ := strconv.Atoi(n)
		return v, q + len(n), len(n) == 2
	}
	if !strings.HasPrefix(s[p:], ", ") {
		return out
	}
	h, q, ok1 := clock(p + 2)
	if !ok1 || !strings.HasPrefix(s[q:], ":") {
		return out
	}
	m, q, ok2 := clock(q + 1)
	if !ok2 {
		return out
	}
	at(h, m, 0, q)
	if strings.HasPrefix(s[q:], ":") {
		if sec, q2, ok := clock(q + 1); ok {
			at(h, m, sec, q2)
		}
	}
	return out
}

// scanSize reads a size as presentSize writes it.
func (r *sentenceReader) scanSize(s string, pos int) []cand {
	out := scanQuoted(s, pos)
	num := token(s, pos, func(c rune) bool { return c >= '0' && c <= '9' || strings.ContainsRune(r.l.thousands, c) })
	n, err := strconv.ParseInt(strings.ReplaceAll(num, r.l.thousands, ""), 10, 64)
	if err != nil {
		return out
	}
	p := pos + len(num)
	for unit, times := range map[string]int64{r.l.megabytes: 1_000_000, r.l.kilobytes: 1_000, r.l.bytes: 1, r.l.byte: 1} {
		if strings.HasPrefix(s[p:], unit) {
			out = append(out, cand{strconv.FormatInt(n*times, 10), p + len(unit)})
		}
	}
	return out
}
