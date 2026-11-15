package query

import (
	"slices"
	"strings"

	"github.com/permaily-org/permaily/internal/contracts"
)

// The sentence a person reads of a canonical formula (ADR 0027, "The
// sentence"): written from a table of fixed templates per variable, operator
// and sign, every value written by the presentation of its type (present.go),
// and nothing else. It depends on the formula and the Context only: no
// clock, no state, no model. Nothing reads a sentence back to decide
// anything; the tests do, to check that no two formulas read alike.

// language is a sentence's table: its fixed words, the templates, and what
// values of a language are written with (months, the units of sizes, the
// names of mailboxes). Values themselves are never translated.
type language struct {
	tag string
	// all is the sentence of a formula with no group, never of one no
	// message matches, one leads a formula of one group, several one of
	// several groups, and rule a rule of several formulas.
	all, never, one, several, rule string
	// item starts each line of several groups in plain text; or separates
	// conditions in a group; end ends the sentence; model follows every
	// condition a model decides; utc ends a sentence that holds a time.
	item, or, end, model, utc string
	months                    [12]string
	thousands                 string
	megabytes, kilobytes      string
	byte, bytes               string
	yourMailbox               string
	disconnected              [2]string
	templates                 map[templateKey]template
	// unknown are the templates, positive and negative, of a condition
	// that has none in templates, which no canonical formula holds.
	unknown [2]template
}

// templateKey chooses a condition's template: its variable, operator and
// sign, and for attachment_type whether its value is T/*.
type templateKey struct {
	Var     contracts.Variable
	Op      contracts.Operator
	Not     bool
	AnyType bool
}

// template is the fixed words of a condition, around its values: one piece
// for a condition without a value, two for one with a value (before it and
// after it).
type template []string

// languages are the tables there are, by language tag.
var languages = map[string]*language{"en": &english}

// languageOf is the table of tag, or of its primary language, or English.
func languageOf(tag string) *language {
	if l, ok := languages[tag]; ok {
		return l
	}
	if base, _, ok := strings.Cut(tag, "-"); ok {
		if l, ok := languages[base]; ok {
			return l
		}
	}
	return &english
}

// Render writes the sentence of a canonical formula, in c.Lang (English when
// it has no table), naming mailboxes from c.Mailboxes: "All mail." for a
// formula with no group, "Mail " and its conditions joined by ", or " for one
// group, and for several the lead "Mail that meets all of these:" and a line
// per group. Every condition a model decides is followed by "(a model
// decides)" and its parts are marked. A formula no message matches (Never)
// reads "No mail.".
func Render(f *Canonical, c Context) contracts.Sentence {
	l := languageOf(c.Lang)
	w := writer{l: l, names: mailboxNames(c, l)}
	return w.sentence(f)
}

// RuleSentence is what a person reads of a rule (ADR 0027, "Rules: any of
// their criteria"): the sentence of each of its formulas, introduced by "Any
// of these:" when there are several. Text is the whole as plain text, the
// lead and each formula's text on lines of their own.
type RuleSentence struct {
	Lang     string                   `json:"lang"`
	Text     string                   `json:"text"`
	Lead     []contracts.SentencePart `json:"lead"`
	Formulas []contracts.Sentence     `json:"formulas"`
}

// RenderRule writes the sentence of a rule of one or more canonical formulas,
// as Render writes each. A rule with no formula applies to no mail.
func RenderRule(fs []*Canonical, c Context) RuleSentence {
	l := languageOf(c.Lang)
	w := writer{l: l, names: mailboxNames(c, l)}
	r := RuleSentence{Lang: l.tag, Lead: []contracts.SentencePart{}}
	var texts []string
	if len(fs) > 1 {
		r.Lead = append(r.Lead, fixed(l.rule))
		texts = append(texts, l.rule)
	}
	if len(fs) == 0 {
		fs = []*Canonical{{Never: true}}
	}
	for _, f := range fs {
		s := w.sentence(f)
		r.Formulas = append(r.Formulas, s)
		texts = append(texts, s.Text)
	}
	r.Text = strings.Join(texts, "\n")
	return r
}

// writer writes the sentences of one Context.
type writer struct {
	l     *language
	names map[string]mailboxName
	// times says the sentence being written holds a time, and so ends
	// saying they are in UTC.
	times bool
}

func fixed(s string) contracts.SentencePart { return contracts.SentencePart{Text: s} }

func (w *writer) sentence(f *Canonical) contracts.Sentence {
	w.times = false
	l := w.l
	s := contracts.Sentence{Lang: l.tag, Lead: []contracts.SentencePart{}}
	var lines [][]contracts.SentencePart
	switch {
	case f == nil || f.Never || slices.ContainsFunc(f.Formula.All, func(g contracts.Group) bool { return len(g.Any) == 0 }):
		// A group of no condition holds on no message (no canonical
		// formula has one).
		lines = [][]contracts.SentencePart{{fixed(l.never)}}
	case len(f.Formula.All) == 0:
		lines = [][]contracts.SentencePart{{fixed(l.all)}}
	case len(f.Formula.All) == 1:
		line := append([]contracts.SentencePart{fixed(l.one)}, w.group(f.Formula.All[0])...)
		lines = [][]contracts.SentencePart{append(line, fixed(l.end))}
	default:
		s.Lead = append(s.Lead, fixed(l.several))
		for _, g := range f.Formula.All {
			lines = append(lines, w.group(g))
		}
		lines[len(lines)-1] = append(lines[len(lines)-1], fixed(l.end))
	}
	if w.times {
		lines[len(lines)-1] = append(lines[len(lines)-1], fixed(l.utc))
	}
	var text strings.Builder
	for _, p := range s.Lead {
		text.WriteString(p.Text)
	}
	for _, line := range lines {
		if len(s.Lead) > 0 {
			text.WriteString("\n" + l.item)
		}
		for _, p := range line {
			text.WriteString(p.Text)
		}
		s.Lines = append(s.Lines, contracts.SentenceLine{Parts: line})
	}
	s.Text = text.String()
	return s
}

func (w *writer) group(g contracts.Group) []contracts.SentencePart {
	var parts []contracts.SentencePart
	for i, c := range g.Any {
		if i > 0 {
			parts = append(parts, fixed(w.l.or))
		}
		parts = append(parts, w.condition(c)...)
	}
	return parts
}

// positive is a received or size condition made positive, as Canonicalise
// makes it: not since is before, not at_least is below, and the reverse.
func positive(c contracts.Condition) contracts.Condition {
	if !c.Not {
		return c
	}
	flip := map[contracts.Operator]contracts.Operator{
		contracts.OpSince: contracts.OpBefore, contracts.OpBefore: contracts.OpSince,
		contracts.OpAtLeast: contracts.OpBelow, contracts.OpBelow: contracts.OpAtLeast,
	}
	if op, ok := flip[c.Op]; ok && (c.Var == contracts.VarReceived || c.Var == contracts.VarSize) {
		c.Op, c.Not = op, false
	}
	return c
}

func (w *writer) condition(c contracts.Condition) []contracts.SentencePart {
	l := w.l
	c = positive(c)
	key := templateKey{Var: c.Var, Op: c.Op, Not: c.Not}
	value := c.Value
	if c.Var == contracts.VarAttachmentType && c.Op == contracts.OpIs {
		if t, ok := anyMediaType(c.Value); ok {
			key.AnyType, value = true, t
		}
	}
	tpl, ok := l.templates[key]
	var values [][]contracts.SentencePart
	switch {
	case !ok:
		tpl = l.unknown[0]
		if c.Not {
			tpl = l.unknown[1]
		}
		for _, v := range []string{string(c.Var), string(c.Op), c.Value} {
			values = append(values, []contracts.SentencePart{{Text: presentText(v), Value: true}})
		}
	case len(tpl) == 1:
	default:
		values = [][]contracts.SentencePart{w.value(c, key, value)}
	}
	parts := []contracts.SentencePart{fixed(tpl[0])}
	for i, v := range values {
		parts = append(parts, v...)
		parts = append(parts, fixed(tpl[i+1]))
	}
	model := c.Op == contracts.OpAbout || c.Op == contracts.OpMentions
	if model {
		parts = append(parts, fixed(l.model))
	}
	out := parts[:0]
	for _, p := range parts {
		if p.Text != "" {
			p.Model = model
			out = append(out, p)
		}
	}
	return out
}

// value is the parts of a condition's value: one value part, written by the
// presentation of its type, and for a mailbox the fixed words around it.
func (w *writer) value(c contracts.Condition, key templateKey, v string) []contracts.SentencePart {
	var text string
	switch c.Var {
	case contracts.VarFromAddress, contracts.VarRecipientAddress:
		text = presentAddress(v)
	case contracts.VarFromDomain, contracts.VarRecipientDomain, contracts.VarSignedBy:
		text = presentDomain(v)
	case contracts.VarAttachmentType:
		text = v // a T of T/*, a media token
		if !key.AnyType {
			text = presentMediaType(v)
		}
	case contracts.VarReceived:
		var isTime bool
		text, isTime = presentTime(v, w.l)
		w.times = w.times || isTime
	case contracts.VarSize:
		text = presentSize(v, w.l)
	case contracts.VarMailbox:
		name, ok := w.names[v]
		if !ok {
			name = mailboxName{w.l.disconnected[0], presentMailboxID(v), w.l.disconnected[1]}
		}
		return []contracts.SentencePart{fixed(name.before), {Text: name.value, Value: true}, fixed(name.after)}
	default:
		text = presentText(v)
	}
	return []contracts.SentencePart{{Text: text, Value: true}}
}
