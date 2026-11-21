// Package redact takes data with a fixed shape out of a text, and splits a
// text into sentences for a removal by meaning (ADR 0040, "Fixed shapes" and
// "Removing by meaning"). It needs no model: the same text and kinds give the
// same result in every enclave and release of one Version.
//
// This file is the seam, committed with the design and revised after its
// review (ADR 0040, "Changed after the design review", findings 1 and 7):
// the versions, the bounds, the one View of a message every removal is
// decided and applied on, the Span coordinates in it, the SentenceMap a
// meaning decision is bound to, Union and Join, which every removal's spans
// go through, and Redactor, the interface its pull request 2 implements
// (Fixed: Shapes and Sentences, pure, depguard's redact-is-pure) and every
// implementation of which passes redacttest.Contract. The golden cases of
// testdata/shapes.jsonl are what Shapes must answer.
package redact

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/permaily-org/permaily/internal/contracts"
)

// Version is the version of the fixed shapes' patterns. A change of them, a
// fixed false positive included, is a new Version, which receipts name.
const Version = 1

// SplitVersion is the version of the sentence split (Sentences). A meaning
// decision is bound to it (rules.MeaningRef): a sentence's number is never
// read under another split.
const SplitVersion = 1

// The bounds of a removal by meaning (ADR 0040, "Removing by meaning",
// "Cost"): the bytes of the View it reads, the sentences it splits them into
// (the subject's counted), and how long one sentence may be before it is
// split at its last space. What is past them is removed, never sent.
const (
	MeaningTextMax      = 32 << 10
	MeaningSentencesMax = 128
	SentenceRunesMax    = 1000
)

// Span is a piece of a View's Text by byte offsets, Start included and End
// not: the one coordinate every removal is written in, whatever removed it
// (a whole part, a fixed shape, a sentence), so removals combine by Union
// whatever order the rules were made in.
type Span struct{ Start, End int }

// Piece is a Span with a fixed shape, and the kind it was found as: the first
// of contracts.ShapeKinds that matches it, when several do.
type Piece struct {
	Span
	Kind contracts.ShapeKind
}

// View is the one fixed view of a message every rule's removal is decided
// and applied on (ADR 0040, finding 1): its subject, one line break, and its
// text as inbox.MessageText gives it, whichever parts the connection reads
// and whichever other rules apply. Subject and Body are their spans in Text.
type View struct {
	Text    string
	Subject Span
	Body    Span
}

// NewView is the View of a message's subject and text.
func NewView(subject, text string) View {
	s := len(subject)
	return View{Text: subject + "\n" + text, Subject: Span{0, s}, Body: Span{s + 1, s + 1 + len(text)}}
}

// SentenceMap is how Sentences split a View for a removal by meaning:
// Sentences are its sentences in order, each a Span of the View's Text,
// sorted, apart, never empty and never only white space; the white space
// between them belongs to none. Cut is where meaning stops reading (past
// MeaningTextMax bytes or MeaningSentencesMax sentences; len(Text) when it
// reads all of it): everything from Cut on is removed, never sent.
type SentenceMap struct {
	Sentences []Span
	Cut       int
}

// Digest names the map and the text it splits: a meaning decision keeps it
// with the sentences it kept, and a decision whose Digest is not the View's
// now is never applied (the whole text is removed instead).
func (m SentenceMap) Digest(v View) string {
	h := sha256.New()
	h.Write([]byte("permaily sentence map v1\x00"))
	// Each number as a varint, which delimits itself.
	put := func(x int) { h.Write(binary.AppendVarint(nil, int64(x))) }
	put(SplitVersion)
	put(len(v.Text))
	h.Write([]byte(v.Text))
	put(len(m.Sentences))
	for _, s := range m.Sentences {
		put(s.Start)
		put(s.End)
	}
	put(m.Cut)
	return hex.EncodeToString(h.Sum(nil)[:16])
}

// Check says m keeps a SentenceMap's invariants for v: every sentence within
// Text and before Cut, sorted, apart, not empty, not only white space, at
// most SentenceRunesMax runes, and none past MeaningSentencesMax; every
// sentence and Cut on a rune boundary (an invalid byte being a rune of its
// own); Cut within Text and at most MeaningTextMax; and every byte before
// Cut in no sentence white space, so nothing meaning reads is left out of
// what it asks about (the re-review's N1). A consumer refuses a map that
// fails it before any decision is asked or applied, and removes the whole
// subject and text instead.
func (m SentenceMap) Check(v View) error {
	if m.Cut < 0 || m.Cut > len(v.Text) || m.Cut > MeaningTextMax {
		return errors.New("redact: the map's cut is outside the text or past what meaning reads")
	}
	if len(m.Sentences) > MeaningSentencesMax {
		return errors.New("redact: more sentences than meaning reads")
	}
	text, at := v.Text, 0
	// gap walks white space from at up to end, landing on it.
	gap := func(end int) error {
		for at < end {
			r, n := utf8.DecodeRuneInString(text[at:])
			if !unicode.IsSpace(r) {
				return errors.New("redact: text before the cut in no sentence")
			}
			at += n
		}
		if at != end {
			return errors.New("redact: a sentence or the cut inside a rune")
		}
		return nil
	}
	for _, s := range m.Sentences {
		if s.Start < at || s.End <= s.Start || s.End > m.Cut {
			return errors.New("redact: a sentence out of order, empty, or past the cut")
		}
		if err := gap(s.Start); err != nil {
			return err
		}
		runes, blank := 0, true
		for at < s.End {
			r, n := utf8.DecodeRuneInString(text[at:])
			blank = blank && unicode.IsSpace(r)
			at += n
			runes++
		}
		switch {
		case at != s.End:
			return errors.New("redact: a sentence or the cut inside a rune")
		case blank:
			return errors.New("redact: a sentence of white space alone")
		case runes > SentenceRunesMax:
			return errors.New("redact: a sentence longer than SentenceRunesMax runes")
		}
	}
	return gap(m.Cut)
}

// Redactor finds what a removal takes out, with no model. Pull request 2
// implements it (Fixed); redacttest.Naive plays it for the tests of its
// consumers, and every implementation passes redacttest.Contract.
type Redactor interface {
	// Shapes is every piece of text that has one of kinds, sorted by
	// Start, as "Fixed shapes" defines them: pieces of different kinds
	// may overlap; Union joins them.
	Shapes(text string, kinds []contracts.ShapeKind) []Piece
	// Sentences splits v as "Removing by meaning" defines it, within its
	// bounds.
	Sentences(v View) SentenceMap
}

// Union is spans sorted and joined: two that overlap or touch become one.
// Empty spans are dropped.
func Union(spans ...[]Span) []Span {
	var all []Span
	for _, l := range spans {
		for _, s := range l {
			if s.End > s.Start {
				all = append(all, s)
			}
		}
	}
	slices.SortFunc(all, func(a, b Span) int { return a.Start - b.Start })
	var out []Span
	for _, s := range all {
		if n := len(out); n > 0 && s.Start <= out[n-1].End {
			out[n-1].End = max(out[n-1].End, s.End)
			continue
		}
		out = append(out, s)
	}
	return out
}

// Join is the subject and the text of v as they leave once removed (spans of
// v.Text, as Union answers them, or any spans: Join unions them itself) is
// taken out: each run of what was removed, clipped to its part, becomes one
// contracts.RemovedMarker; everything else is kept byte for byte, so the
// output is the input minus pieces (U23). A span that cuts a valid rune is
// widened to the whole rune; an invalid byte is a rune of its own, so a span
// beside one never takes it.
func Join(v View, removed []Span) (subject, text string) {
	u := Union(removed)
	return cut(v.Text, v.Subject, u), cut(v.Text, v.Body, u)
}

func cut(text string, part Span, removed []Span) string {
	var b strings.Builder
	at, marked := part.Start, false
	for _, s := range removed {
		start, end := max(s.Start, part.Start), min(s.End, part.End)
		if end <= start {
			continue
		}
		if p, ok := runeAround(text, start, part.Start); ok {
			start = p
		}
		if p, ok := runeAround(text, end, part.Start); ok && end < part.End {
			_, n := utf8.DecodeRuneInString(text[p:])
			end = min(p+n, part.End)
		}
		if marked && start <= at {
			// Widened into the run before: the same run, one marker.
			at = max(at, end)
			continue
		}
		b.WriteString(text[at:start])
		b.WriteString(contracts.RemovedMarker)
		at, marked = end, true
	}
	b.WriteString(text[at:part.End])
	return b.String()
}

// runeAround is where the valid rune that i is inside of starts, when i is
// inside one (not at its start) and that start is not before from. An
// invalid byte is a rune of its own, so a span beside one never takes it.
func runeAround(text string, i, from int) (int, bool) {
	if i >= len(text) || utf8.RuneStart(text[i]) {
		return 0, false
	}
	for p := i - 1; p >= max(from, i-utf8.UTFMax+1); p-- {
		if utf8.RuneStart(text[p]) {
			r, n := utf8.DecodeRuneInString(text[p:])
			return p, p+n > i && (r != utf8.RuneError || n > 1)
		}
	}
	return 0, false
}
