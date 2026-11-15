package query

import (
	"math/rand/v2"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/net/idna"

	"github.com/permaily-org/permaily/internal/contracts"
)

// Generators of formulas for the sentence's tests, adversarial in their
// values: labels and values that are the templates' own words, quotes
// escaped and not, backslashes, right-to-left text, the longest values, and
// two mailboxes the context does not have. Formulas over querytest's
// universe, canonicalised, are read back by sentence_querytest_test.go;
// querytest's values are chosen to collide in meaning, not to be read as a
// sentence's words, so these add the values that are. canonical gives them as
// Canonicalise writes them, the formulas a sentence is written from; formula
// builds a Canonical by hand, which may hold what normalisation refuses, and
// which Render must still write so that no two read alike.

// The mailboxes of the generated formulas: the context has the first three,
// and the last two are disconnected.
var genMailboxes = []string{
	"mbx_aaaaaaaaaaaaaaaaaaaaaaaaaa",
	"mbx_bbbbbbbbbbbbbbbbbbbbbbbbbb",
	"mbx_cccccccccccccccccccccccccc",
	"mbx_dddddddddddddddddddddddddd",
	"mbx_eeeeeeeeeeeeeeeeeeeeeeeeee",
}

// fixedWords are every fixed text of a language, as values.
func fixedWords(l *language) []string {
	words := []string{l.all, l.never, l.one, l.several, l.rule, l.item, l.or, l.end, l.model, l.utc, l.yourMailbox, l.disconnected[0], l.disconnected[1], l.megabytes, l.bytes}
	for _, tpl := range l.templates {
		words = append(words, tpl...)
	}
	words = append(words, l.unknown[0]...)
	return append(words, l.months[:]...)
}

// hostileTexts are values chosen to be read as something else.
var hostileTexts = []string{
	"invoice", "Invoices 2026", "x", ", or ", ".", "- ", "“", "”", `\“`, `\`, `\\`, `"`, "«»„‟⹂〝〞〟", "'",
	"a”, or whose body contains the text “b",
	"A”, or that arrived at your mailbox “B",
	"x” (a model decides), or about “y",
	"x”.\n- sent from “y",
	`\u{41}`, `\x{FF}`, `\”`, "”“", "{v}", "%s", "$1", "<b>x</b>", "&amp;",
	"חשבונית", "فاتورة", "abc אבג def", "1 January 2026", "1 MB", "your mailbox “Work”",
	"(xn--bcher-kva.example)", "mbx_aaaaaaaaaaaaaaaaaaaaaaaaaa", "a@b.c", "发票", "ﬁle",
	strings.Repeat("”", 100), strings.Repeat(`\`, 100), strings.Repeat("〝", 100), strings.Repeat("𠀀", 100),
	strings.Repeat("a", 100),
}

// notNormalised are code points normalisation removes or refuses, which a
// Canonical built by hand may still hold: line breaks, bidirectional and
// other format characters, controls, a NUL, a byte that is not UTF-8.
var notNormalised = []string{"\n", "\r\n", "\u2028", "\u202e", "\u2066", "\u200b", "\u00ad", "\x00", "\x1b", "\xff", "\U000E0001", "\u3164", "\ufe0f", "\u00a0", "\t"}

// longestUnicodeDomain is the canonical domain whose Unicode form is the
// longest: four labels of the 4-byte U+20000, each as many as its ASCII
// label of 63 bytes holds, 253 bytes in all.
func longestUnicodeDomain() string {
	label := func(maxASCII int) string {
		best := ""
		for n := 1; ; n++ {
			a, err := idna.Lookup.ToASCII(strings.Repeat("𠀀", n))
			if err != nil || len(a) > maxASCII {
				return best
			}
			best = a
		}
	}
	l63 := label(63)
	d := l63 + "." + l63 + "." + l63
	return d + "." + label(253-len(d)-1)
}

type gen struct {
	r       *rand.Rand
	l       *language
	hostile bool // values a Canonical built by hand may hold, past what normalisation leaves
	longest string
}

func newGen(seed uint64, hostile bool) *gen {
	return &gen{r: rand.New(rand.NewPCG(seed, seed^0x5eed)), l: &english, hostile: hostile, longest: longestUnicodeDomain()}
}

func (g *gen) pick(xs []string) string { return xs[g.r.IntN(len(xs))] }

// truncate cuts s to at most n runes and m bytes, on a rune.
func truncate(s string, n, m int) string {
	runes, bytes := 0, 0
	for i, r := range s {
		size := utf8.RuneLen(r)
		if runes == n || bytes+max(size, 1) > m {
			return s[:i]
		}
		runes, bytes = runes+1, bytes+max(size, 1)
	}
	return s
}

// text is a value of contains, a topic or a list: one to three pieces of
// the hostile texts and the templates' words, within the limits.
func (g *gen) text() string {
	pool := append(append([]string(nil), hostileTexts...), fixedWords(g.l)...)
	var b strings.Builder
	for range 1 + g.r.IntN(3) {
		b.WriteString(g.pick(pool))
		if g.hostile && g.r.IntN(3) == 0 {
			b.WriteString(g.pick(notNormalised))
		}
	}
	s := truncate(b.String(), contracts.MaxTextRunes, contracts.MaxTextBytes)
	if s == "" {
		return "x"
	}
	return s
}

func (g *gen) domain() string {
	pool := []string{"example.com", "sub.example.com", "holded.com", "xn--bcher-kva.example", "xn--mnchen-3ya.de", "xn--zz.example", "a.b", "a.com.", g.longest}
	if g.hostile {
		pool = append(pool, "Example.COM", "a b.com", "”, or x", "bücher.example", "a.com (b.com)", "x\u202e.com")
	}
	return g.pick(pool)
}

func (g *gen) address() string {
	locals := []string{"ann", "a”b", `a\b`, "“x”", "a@b", "x+tag", "Ann", strings.Repeat(`\`, 64), strings.Repeat("”", 21), "a, or b", ""}
	if g.hostile && g.r.IntN(5) == 0 {
		return g.pick([]string{"no-at-sign", "x@", "@", "x@Not A Domain", "a\n@b.com"})
	}
	return g.pick(locals) + "@" + g.domain()
}

func (g *gen) mediaType() string {
	pool := []string{"application/pdf", "image/png", "image/*", "audio/*", "application/vnd.ms-excel", "application/x.", "text/x+y.", "application/x-y_z!#$&^"}
	if g.hostile {
		pool = append(pool, "a b/c", "image", "*/*", "x”/y", "image/*/*", "/", "a/b c")
	}
	return g.pick(pool)
}

func (g *gen) time() string {
	t := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(g.r.Int64N(int64(100 * 365 * 24 * time.Hour))))
	switch g.r.IntN(3) {
	case 0:
		t = t.Truncate(24 * time.Hour)
	case 1:
		t = t.Truncate(time.Minute)
	}
	if g.hostile && g.r.IntN(5) == 0 {
		return g.pick([]string{"2026-01-01T00:00:00+01:00", "tomorrow", "2026-01-01T00:00:00.5Z", "1 January 2026", ""})
	}
	return t.Truncate(time.Second).Format(canonicalTime)
}

func (g *gen) size() string {
	n := 1 + g.r.Int64N(32<<20)
	switch g.r.IntN(4) {
	case 0:
		n = (1 + g.r.Int64N(33)) * 1_000_000
	case 1:
		n = (1 + g.r.Int64N(33_000)) * 1_000
	case 2:
		n = 1 + g.r.Int64N(2_000)
	}
	if g.hostile && g.r.IntN(5) == 0 {
		return g.pick([]string{"0", "007", "-5", "1,000", "1 MB", "x"})
	}
	return strconv.FormatInt(n, 10)
}

func (g *gen) mailbox() string {
	if g.hostile && g.r.IntN(6) == 0 {
		return g.pick([]string{"mbx_”x", "Work", "mbx_AAAAAAAAAAAAAAAAAAAAAAAAAA", ""})
	}
	return g.pick(genMailboxes)
}

// context is an account's mailboxes as a sentence sees them: the first three
// of genMailboxes, labelled adversarially (labels that are sentences,
// labels two mailboxes share or that read alike, an empty label), and the
// last two left out, so they read as disconnected.
func (g *gen) context() Context {
	labelSets := [][3]string{
		{"A”, or that arrived at your mailbox “B", "A", "B"},
		{"Work", "Work", "Personal"},
		{"", "Bills", "your mailbox “Bills”"},
		{"ＷＯＲＫ", "work", `\`},
		{"עבודה", "Times are in UTC.", "a mailbox you disconnected (mbx_dddddddddddddddddddddddddd)"},
		{"”", "“", ", or "},
	}
	labels := labelSets[g.r.IntN(len(labelSets))]
	addresses := [3]string{"k7m2p9q4r8s3t6v5w2x9@permaily.com", "a2b3c4d5e6f7g8h9i2j3@permaily.com", "z9y8x7w6v5u4t3s2r9q8@permaily.com"}
	if g.hostile && g.r.IntN(4) == 0 {
		addresses[1] = g.pick([]string{addresses[0], "Not An Address", "", "a”b@permaily.com"})
	}
	c := Context{Lang: "en", Mailboxes: map[string]MailboxName{}}
	for i := range 3 {
		c.Mailboxes[genMailboxes[i]] = MailboxName{Label: labels[i], Address: addresses[i]}
	}
	return c
}

// conditionKinds are the variable, operator pairs of the templates, and
// whether they may be negated (received and size are made positive).
var conditionKinds = []struct {
	v   contracts.Variable
	op  contracts.Operator
	neg bool
}{
	{contracts.VarFromAddress, contracts.OpIs, true},
	{contracts.VarFromDomain, contracts.OpIs, true},
	{contracts.VarRecipientAddress, contracts.OpIs, true},
	{contracts.VarRecipientDomain, contracts.OpIs, true},
	{contracts.VarSignedBy, contracts.OpIs, true},
	{contracts.VarSubject, contracts.OpContains, true},
	{contracts.VarBody, contracts.OpContains, true},
	{contracts.VarAttachments, contracts.OpExists, true},
	{contracts.VarAttachmentName, contracts.OpContains, true},
	{contracts.VarAttachmentType, contracts.OpIs, true},
	{contracts.VarList, contracts.OpExists, true},
	{contracts.VarList, contracts.OpIs, true},
	{contracts.VarReceived, contracts.OpSince, false},
	{contracts.VarReceived, contracts.OpBefore, false},
	{contracts.VarSize, contracts.OpAtLeast, false},
	{contracts.VarSize, contracts.OpBelow, false},
	{contracts.VarMailbox, contracts.OpIs, true},
	{contracts.VarSubject, contracts.OpAbout, true},
	{contracts.VarMessage, contracts.OpAbout, true},
	{contracts.VarMessage, contracts.OpMentions, true},
}

func (g *gen) condition() contracts.Condition {
	if g.hostile && g.r.IntN(40) == 0 { // no template: rendered by the fallback
		return contracts.Condition{Var: contracts.Variable(g.pick([]string{"subject", "x”y", "body"})), Op: contracts.Operator(g.pick([]string{"is", "about", "”, or"})), Value: g.text(), Not: g.r.IntN(2) == 0}
	}
	k := conditionKinds[g.r.IntN(len(conditionKinds))]
	c := contracts.Condition{Var: k.v, Op: k.op, Not: k.neg && g.r.IntN(2) == 0}
	switch k.v {
	case contracts.VarAttachments:
	case contracts.VarList:
		if k.op == contracts.OpIs {
			c.Value = g.text()
		}
	case contracts.VarFromAddress, contracts.VarRecipientAddress:
		c.Value = g.address()
	case contracts.VarFromDomain, contracts.VarRecipientDomain, contracts.VarSignedBy:
		c.Value = g.domain()
	case contracts.VarAttachmentType:
		c.Value = g.mediaType()
	case contracts.VarReceived:
		c.Value = g.time()
	case contracts.VarSize:
		c.Value = g.size()
	case contracts.VarMailbox:
		c.Value = g.mailbox()
	default:
		c.Value = g.text()
	}
	return c
}

// formula is a formula of one to four groups of one to four conditions
// mostly, and one time in twenty at the limits: 32 groups of 2, or 2 of 32.
func (g *gen) formula() *Canonical {
	groups, per := 1+g.r.IntN(4), func() int { return 1 + g.r.IntN(4) }
	switch g.r.IntN(20) {
	case 0:
		groups, per = contracts.MaxGroups, func() int { return contracts.MaxConditions / contracts.MaxGroups }
	case 1:
		groups, per = 2, func() int { return contracts.MaxGroupConditions }
	case 2:
		return &Canonical{Formula: contracts.Formula{V: contracts.FormulaVersion}}
	}
	f := contracts.Formula{V: contracts.FormulaVersion}
	for range groups {
		var grp contracts.Group
		for range per() {
			grp.Any = append(grp.Any, g.condition())
		}
		f.All = append(f.All, grp)
	}
	return &Canonical{Formula: f}
}

// canonical is a generated formula as Canonicalise writes it, with the
// adversarial values normalisation accepts; one it refuses is drawn again.
func (g *gen) canonical() *Canonical {
	for {
		if c, err := Canonicalise(g.formula().Formula); err == nil {
			return c
		}
	}
}

// next is a formula of the generator's kind: canonical, or built by hand
// with values past what normalisation leaves.
func (g *gen) next() *Canonical {
	if g.hostile {
		return g.formula()
	}
	return g.canonical()
}
