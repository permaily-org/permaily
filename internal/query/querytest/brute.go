package querytest

import (
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/net/idna"
	"golang.org/x/text/unicode/norm"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/query"
)

// Eval is the value of formulas as sent (not canonical, not simplified) on
// a message, by brute force from ADR 0027's definitions ("What a formula
// means", "The table", "Values, and how each is normalised", "Which
// reads"): the least over formulas of the least over groups of the greatest
// over conditions, each condition read straight from the message. A formula
// is read in its version 1 meaning, and must be one Parse accepts.
func Eval(fs []contracts.Formula, m *query.Message, a query.Answers, mode query.Mode) query.Value {
	if m.Have&(query.FromItem|query.FromMeta) != query.FromItem|query.FromMeta {
		return query.Unknown
	}
	if m.Confirmation {
		return query.False
	}
	v := query.True
	for _, f := range fs {
		for _, g := range f.All {
			gv := query.False
			for _, c := range g.Any {
				gv = max(gv, Condition(c, m, a, mode))
			}
			v = min(v, gv)
		}
	}
	return v
}

// Condition is one condition's value on a message whose item and meta were
// read.
func Condition(c contracts.Condition, m *query.Message, a query.Answers, mode query.Mode) query.Value {
	v := positive(c, m, a, mode)
	if c.Not {
		return query.True - v
	}
	return v
}

func positive(c contracts.Condition, m *query.Message, a query.Answers, mode query.Mode) query.Value {
	// The item's conditions, always known.
	switch c.Var {
	case contracts.VarReceived:
		t, _ := time.Parse(time.RFC3339, strings.ToUpper(c.Value))
		if c.Op == contracts.OpSince {
			return of(!m.Received.Before(t))
		}
		return of(m.Received.Before(t))
	case contracts.VarMailbox:
		return of(m.Mailbox == strings.ToLower(c.Value))
	}
	// A header that could not be read: every other condition, a model's
	// too, whatever it answered.
	if m.Unparsed {
		return query.Unknown
	}
	if c.Op == contracts.OpAbout || c.Op == contracts.OpMentions {
		topic, _ := text(c.Value)
		p, ok := a[query.Atom{Var: c.Var, Op: c.Op, Topic: topic}]
		if !ok {
			return query.Unknown
		}
		return query.Classify(p, mode)
	}
	switch c.Var {
	case contracts.VarFromAddress, contracts.VarFromDomain:
		return overAddresses(c, m.From, m.Partial)
	case contracts.VarRecipientAddress, contracts.VarRecipientDomain:
		return overAddresses(c, m.Recipients, m.Partial)
	case contracts.VarSignedBy:
		want, _ := domain(c.Value)
		found, bad := false, false
		for _, s := range m.SignedBy {
			d, ok := domain(s)
			bad = bad || !ok
			found = found || (ok && under(d, want))
		}
		return listValue(found, bad)
	case contracts.VarSubject:
		return containsText(m.Subject, c.Value)
	case contracts.VarBody:
		if m.Have&query.FromText == 0 && (m.Have&query.FromEntry == 0 || m.BodyCut) {
			return query.Unknown
		}
		return containsText(m.Body, c.Value)
	case contracts.VarAttachments:
		return listValue(len(m.Attachments) > 0, m.Partial)
	case contracts.VarAttachmentName:
		want, _ := text(c.Value)
		found, bad := false, false
		for _, x := range m.Attachments {
			n, ok := text(x.Name)
			bad = bad || !ok
			found = found || (ok && strings.Contains(n, want))
		}
		return listValue(found, bad || m.Partial)
	case contracts.VarAttachmentType:
		want := strings.ToLower(c.Value)
		found := slices.ContainsFunc(m.Attachments, func(x query.Attachment) bool { return mediaTypeMatches(x.Type, want) })
		return listValue(found, m.Partial)
	case contracts.VarList:
		if !m.ListRecorded {
			return query.Unknown
		}
		if c.Op == contracts.OpExists {
			return of(len(m.List) > 0)
		}
		want := strings.ToLower(c.Value)
		return of(slices.ContainsFunc(m.List, func(l string) bool { return strings.ToLower(l) == want }))
	case contracts.VarSize:
		if !m.SizeKnown {
			return query.Unknown
		}
		n, _ := strconv.ParseInt(c.Value, 10, 64)
		if c.Op == contracts.OpAtLeast {
			return of(m.Size >= n)
		}
		return of(m.Size < n)
	}
	panic("querytest: a condition outside the language: " + string(c.Var))
}

// overAddresses is a condition on a list of addresses: one decision per
// address, whole; True when a valid one satisfies it, otherwise Unknown
// when one is not valid or the list may be cut, otherwise False.
func overAddresses(c contracts.Condition, list []string, partial bool) query.Value {
	found, bad := false, false
	for _, s := range list {
		local, d, ok := storedAddress(s)
		if !ok {
			bad = true
			continue
		}
		switch c.Var {
		case contracts.VarFromAddress, contracts.VarRecipientAddress:
			wl, wd, _ := sentAddress(c.Value)
			found = found || (local == wl && d == wd)
		default:
			want, _ := domain(c.Value)
			found = found || under(d, want)
		}
	}
	return listValue(found, bad || partial)
}

func listValue(found, maybeMissing bool) query.Value {
	switch {
	case found:
		return query.True
	case maybeMissing:
		return query.Unknown
	}
	return query.False
}

func of(b bool) query.Value {
	if b {
		return query.True
	}
	return query.False
}

// containsText is contains on one field: Unknown when the field does not
// settle.
func containsText(field, value string) query.Value {
	f, ok := text(field)
	if !ok {
		return query.Unknown
	}
	v, _ := text(value)
	return of(strings.Contains(f, v))
}

// Text, as ADR 0027 defines it. One round: NFKC; every default-ignorable
// code point removed; simple case folding to one member of each orbit;
// every run of white space one space, the ends trimmed. Rounds until one
// changes nothing, at most three that change it: ok is false for a text
// that still changes in its fourth.
func text(s string) (string, bool) {
	s = strings.ToValidUTF8(s, "�")
	for range 4 {
		next := textRound(s)
		if next == s {
			return s, true
		}
		s = next
	}
	return "", false
}

func textRound(s string) string {
	var out []string
	for _, word := range strings.FieldsFunc(norm.NFKC.String(s), func(r rune) bool { return unicode.Is(unicode.White_Space, r) }) {
		var b strings.Builder
		for _, r := range word {
			if !defaultIgnorable(r) {
				b.WriteRune(orbitMember(r))
			}
		}
		if b.Len() > 0 {
			out = append(out, b.String())
		}
	}
	return strings.Join(out, " ")
}

// defaultIgnorable is Unicode's Default_Ignorable_Code_Point derived as
// UAX #44 derives it (DerivedCoreProperties.txt): Other_Default_Ignorable,
// Cf and Variation_Selector, less White_Space, the prepended concatenation
// marks and U+FFF9 to U+FFFB.
func defaultIgnorable(r rune) bool {
	switch {
	case 0xFFF9 <= r && r <= 0xFFFB, unicode.Is(unicode.White_Space, r), unicode.Is(unicode.Prepended_Concatenation_Mark, r):
		return false
	}
	return unicode.Is(unicode.Other_Default_Ignorable_Code_Point, r) || unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Variation_Selector, r)
}

// orbitMember is r's simple case folding to one member of its orbit under
// unicode.SimpleFold: the lower case of the orbit's smallest member when
// that is in the orbit, otherwise the smallest member.
func orbitMember(r rune) rune {
	orbit := []rune{r}
	for f := unicode.SimpleFold(r); f != r; f = unicode.SimpleFold(f) {
		orbit = append(orbit, f)
	}
	smallest := slices.Min(orbit)
	if lower := unicode.ToLower(smallest); slices.Contains(orbit, lower) {
		return lower
	}
	return smallest
}

// domain is a domain as ADR 0027 defines it: its trailing dot removed, UTS
// 46 to ASCII with the lookup profile, lower case, two labels or more, each
// of 1 to 63 bytes of a-z, 0-9 and -, at most 253 in all, and no label
// whose Unicode form mixes Latin, Greek and Cyrillic. ok is false for one
// that is not. The input is bounded first as Eval bounds it (more than four
// times the longest domain is refused before IDNA reads it): the ADR does
// not name that bound, and it only ever makes a field Unknown.
func domain(s string) (string, bool) {
	if !utf8.ValidString(s) {
		return "", false
	}
	s = strings.TrimSuffix(s, ".")
	if len(s) > 4*253 {
		return "", false
	}
	a, err := idna.Lookup.ToASCII(s)
	if err != nil {
		return "", false
	}
	a = strings.ToLower(a)
	labels := strings.Split(a, ".")
	if len(a) > 253 || len(labels) < 2 {
		return "", false
	}
	for _, l := range labels {
		if l == "" || len(l) > 63 || strings.Trim(l, "abcdefghijklmnopqrstuvwxyz0123456789-") != "" {
			return "", false
		}
		if u, err := idna.Lookup.ToUnicode(l); err == nil && mixed(u) {
			return "", false
		}
	}
	return a, true
}

// sentAddress is an address as a vendor writes it: a local part that is one
// quoted string is taken out of its quotes and quoting backslashes, then
// the address is checked as a stored one is.
func sentAddress(s string) (string, string, bool) {
	at := strings.LastIndexByte(s, '@')
	if at < 0 {
		return "", "", false
	}
	local := s[:at]
	if strings.HasPrefix(local, `"`) && len(local) >= 2 {
		var b strings.Builder
		escaped, closed := false, false
		for i, r := range local[1:] {
			switch {
			case escaped:
				b.WriteRune(r)
				escaped = false
			case r == '\\':
				escaped = true
			case r == '"':
				if i != len(local)-2 {
					return "", "", false
				}
				closed = true
			default:
				b.WriteRune(r)
			}
		}
		if !closed {
			return "", "", false
		}
		local = b.String()
	}
	return addressOf(local, s[at+1:])
}

// storedAddress is an address of a message as stored: already decoded, so
// a quotation mark or a backslash in its local part is a character of it,
// and the address is not valid.
func storedAddress(s string) (string, string, bool) {
	at := strings.LastIndexByte(s, '@')
	if at < 0 || strings.ContainsAny(s[:at], `"\`) {
		return "", "", false
	}
	return addressOf(s[:at], s[at+1:])
}

// addressOf checks an address's halves once its local part is out of any
// quoting: the local part NFC then folded, until it settles (at most three
// changing rounds), of 1 to 64 bytes, printable, with no space, quotation
// mark or backslash, its letters of one of Latin, Greek and Cyrillic; the
// domain as domain says. The local part is bounded first as Eval bounds it
// (four times its longest).
func addressOf(local, d string) (string, string, bool) {
	if !utf8.ValidString(local) || len(local) > 4*64 {
		return "", "", false
	}
	settled := false
	for range 4 {
		var b strings.Builder
		for _, r := range norm.NFC.String(local) {
			b.WriteRune(orbitMember(r))
		}
		if b.String() == local {
			settled = true
			break
		}
		local = b.String()
	}
	if !settled || local == "" || len(local) > 64 || mixed(local) {
		return "", "", false
	}
	for _, r := range local {
		if r == ' ' || r == '"' || r == '\\' || !unicode.IsPrint(r) {
			return "", "", false
		}
	}
	dom, ok := domain(d)
	return local, dom, ok
}

// mixed reports letters of more than one of Latin, Greek and Cyrillic.
func mixed(s string) bool {
	scripts := map[string]bool{}
	for _, r := range s {
		for name, table := range map[string]*unicode.RangeTable{"Latin": unicode.Latin, "Greek": unicode.Greek, "Cyrillic": unicode.Cyrillic} {
			if unicode.IsLetter(r) && unicode.Is(table, r) {
				scripts[name] = true
			}
		}
	}
	return len(scripts) > 1
}

// mediaTypeMatches is a message's media type against a value, lowered: the
// message's cut at its first ';', trimmed and lowered; equal to the value,
// or the value is T/* and the message's is well formed (a listed top-level
// type, a subtype of 1 to 127 of a-z, 0-9 and !#$&^_.+-) of type T.
func mediaTypeMatches(messageType, want string) bool {
	t, _, _ := strings.Cut(messageType, ";")
	t = strings.ToLower(strings.TrimSpace(t))
	if t == want {
		return true
	}
	top, sub, ok := strings.Cut(t, "/")
	wtop, wsub, _ := strings.Cut(want, "/")
	listed := slices.Contains([]string{"application", "audio", "font", "image", "message", "model", "multipart", "text", "video"}, top)
	wellFormed := sub != "" && len(sub) <= 127 && strings.Trim(sub, "abcdefghijklmnopqrstuvwxyz0123456789!#$&^_.+-") == ""
	return ok && listed && wellFormed && wsub == "*" && top == wtop
}

func under(d, parent string) bool {
	return d == parent || strings.HasSuffix(d, "."+parent)
}

// Contained reports whether inner ≤ outer on every message of ms with each
// message's answers in both modes: brute-force containment in three values.
func Contained(outer, inner contracts.Formula, ms []*query.Message, as []query.Answers) bool {
	for i, m := range ms {
		for _, mode := range []query.Mode{query.ForQuery, query.ForRule} {
			if Eval([]contracts.Formula{inner}, m, as[i], mode) > Eval([]contracts.Formula{outer}, m, as[i], mode) {
				return false
			}
		}
	}
	return true
}
