// Package redacttest plays redact.Redactor for the tests of its consumers
// (ADR 0040's pull requests 4 and 5, written beside pull request 2), and has
// the contract every Redactor keeps, redact.Fixed included once pull request
// 2 writes it. Never imported outside tests (depguard).
package redacttest

import (
	"slices"
	"strings"
	"testing"
	"unicode"
	"unicode/utf8"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/redact"
)

// Naive is a Redactor that keeps the contract with far simpler shapes than
// ADR 0040's: a run of ASCII digits (no separators) at or above a kind's
// least length, and, for email_address and link, a run of non-space bytes
// holding "@" or "://". Its sentences end after ".", "!" or "?" before
// white space and at every line break, and are cut at SentenceRunesMax runes,
// within redact's bounds. It is for the tests of what consumes
// a Redactor, never a reference for what Fixed must answer
// (testdata/shapes.jsonl is).
type Naive struct{}

var _ redact.Redactor = Naive{}

// least is a digit kind's least length (ADR 0040, finding 2: no most).
var least = map[contracts.ShapeKind]int{
	contracts.ShapePhoneNumber: 7, contracts.ShapeCardNumber: 12, contracts.ShapeAccountNumber: 8, contracts.ShapeNumericCode: 4,
}

// Shapes implements redact.Redactor.
func (Naive) Shapes(text string, kinds []contracts.ShapeKind) []redact.Piece {
	var out []redact.Piece
	for _, k := range contracts.ShapeKinds {
		if !slices.Contains(kinds, k) {
			continue
		}
		if n, ok := least[k]; ok {
			for i := 0; i < len(text); {
				j := i
				for j < len(text) && text[j] >= '0' && text[j] <= '9' {
					j++
				}
				if j-i >= n {
					out = append(out, redact.Piece{Span: redact.Span{Start: i, End: j}, Kind: k})
				}
				i = max(j, i+1)
			}
			continue
		}
		mark := "@"
		if k == contracts.ShapeLink {
			mark = "://"
		}
		for i := 0; i < len(text); {
			j := i
			for j < len(text) && text[j] > ' ' {
				j++
			}
			if j > i && strings.Contains(text[i:j], mark) {
				out = append(out, redact.Piece{Span: redact.Span{Start: i, End: j}, Kind: k})
			}
			i = j + 1
		}
	}
	slices.SortStableFunc(out, func(a, b redact.Piece) int { return a.Start - b.Start })
	return out
}

// Sentences implements redact.Redactor.
func (Naive) Sentences(v redact.View) redact.SentenceMap {
	m := redact.SentenceMap{Cut: len(v.Text)}
	open, runes, last := -1, 0, 0
	end := func() {
		if open >= 0 {
			m.Sentences = append(m.Sentences, redact.Span{Start: open, End: last})
		}
		open = -1
	}
	for i := 0; i < len(v.Text); {
		r, n := utf8.DecodeRuneInString(v.Text[i:])
		if i+n > redact.MeaningTextMax {
			// A sentence the cut goes through is removed with what follows.
			m.Cut = i
			if open >= 0 {
				m.Cut = open
			}
			return m
		}
		switch {
		case r == '\n':
			end()
		case unicode.IsSpace(r):
			if open >= 0 && i > 0 && strings.ContainsRune(".!?", rune(v.Text[i-1])) {
				end()
			}
			runes++ // white space inside a sentence counts
		default:
			if open >= 0 && runes >= redact.SentenceRunesMax {
				end()
			}
			if open < 0 {
				if len(m.Sentences) == redact.MeaningSentencesMax {
					m.Cut = i
					return m
				}
				open, runes = i, 0
			}
			runes++
			last = i + n
		}
		i += n
	}
	end()
	return m
}

// Contract is what every Redactor keeps (U9, U10, U23's halves that do not
// depend on the patterns): pieces within the text, sorted, of the kinds
// asked, never empty; nothing asked, nothing found; the marker never found;
// a SentenceMap that keeps its invariants (redact.SentenceMap.Check: every
// byte before the cut in a sentence or white space, rune boundaries, at
// most SentenceRunesMax runes a sentence), for the empty view, a subject
// alone, past the sentences' bound and past the text's, a sentence of more
// runes than the bound, multi-byte runes across the cut, white space of
// every width, invalid bytes; that map spoilt as the re-review's N1 says
// (empty, an interior or the tail sentence missing, a span inside a rune, a
// sentence too long) refused by Check; Join of what was found the input
// minus pieces, one marker a run; and what Join leaves holding nothing
// Shapes finds again.
func Contract(t *testing.T, r redact.Redactor) {
	t.Helper()
	texts := []string{
		"", "Call 5551234567 or ana@example.com today. See https://example.com/a now!",
		"Code 123456 2026 and card 4111111111111111.", contracts.RemovedMarker,
		strings.Repeat("Sentence one. ", 200), strings.Repeat("x", redact.MeaningTextMax+10),
		strings.Repeat("x", 2*redact.SentenceRunesMax+5), strings.Repeat("ñ ", redact.SentenceRunesMax),
		strings.Repeat("€", redact.MeaningTextMax/3+5), "One.\u3000Two.\u00a0\u2028Three!\tFour?\r\nFive",
		"a\xffb. \x80c 5551234567\xfe", "你好。谢谢。 Fin.",
	}
	spoiltAs := map[string]bool{}
	defer func() {
		if len(spoiltAs) != 5 {
			t.Errorf("the maps were spoilt only as %v", spoiltAs)
		}
	}()
	for _, text := range texts {
		if got := r.Shapes(text, nil); len(got) != 0 {
			t.Errorf("no kind asked found %v", got)
		}
		ps := r.Shapes(text, contracts.ShapeKinds)
		for i, p := range ps {
			if p.Start < 0 || p.End > len(text) || p.End <= p.Start || !p.Kind.Valid() || (i > 0 && p.Start < ps[i-1].Start) {
				t.Errorf("a piece out of the text, empty, unknown or out of order: %+v", p)
			}
			if strings.Contains(contracts.RemovedMarker, text[p.Start:p.End]) {
				t.Errorf("the marker was found as a piece: %q", text[p.Start:p.End])
			}
		}
		for _, subject := range []string{"", "Your code 123456"} {
			v := redact.NewView(subject, text)
			m := r.Sentences(v)
			if err := m.Check(v); err != nil {
				t.Errorf("the map of %.40q: %v", text, err)
			}
			if m.Digest(v) != r.Sentences(v).Digest(v) {
				t.Error("the same view split twice is not the same map")
			}
			for name, bad := range spoilt(v, m) {
				spoiltAs[name] = true
				if bad.Check(v) == nil {
					t.Errorf("the map of %.40q %s kept the invariants", text, name)
				}
			}
			var spans []redact.Span
			for _, p := range r.Shapes(v.Text, contracts.ShapeKinds) {
				spans = append(spans, p.Span)
			}
			s, b := redact.Join(v, spans)
			for _, piece := range spans {
				if strings.Contains(s+"\n"+b, v.Text[piece.Start:piece.End]) && !strings.Contains(contracts.RemovedMarker, v.Text[piece.Start:piece.End]) {
					t.Errorf("a piece found was left: %q", v.Text[piece.Start:piece.End])
				}
			}
			if again := r.Shapes(s+"\n"+b, contracts.ShapeKinds); len(again) != 0 {
				t.Errorf("redacted %.40q still has %v", text, again)
			}
		}
	}
}

// spoilt is m made invalid each way the re-review's N1 names, where v's
// text lets it be.
func spoilt(v redact.View, m redact.SentenceMap) map[string]redact.SentenceMap {
	out := map[string]redact.SentenceMap{}
	n := len(m.Sentences)
	without := func(i int) redact.SentenceMap {
		return redact.SentenceMap{Sentences: append(slices.Clone(m.Sentences[:i]), m.Sentences[i+1:]...), Cut: m.Cut}
	}
	if n > 0 {
		out["with no sentence"] = redact.SentenceMap{Cut: m.Cut}
		out["without its tail sentence"] = without(n - 1)
	}
	if n > 2 {
		out["without an interior sentence"] = without(n / 2)
	}
	for i, s := range m.Sentences {
		for k := s.Start + 1; k < s.End; k++ {
			if !utf8.RuneStart(v.Text[k]) && v.Text[k] >= 0x80 && utf8.ValidString(v.Text[s.Start:s.End]) {
				mid := redact.SentenceMap{Sentences: slices.Clone(m.Sentences), Cut: m.Cut}
				mid.Sentences[i].End = k
				out["with a sentence ending inside a rune"] = mid
				break
			}
		}
		if _, ok := out["with a sentence ending inside a rune"]; ok {
			break
		}
	}
	// A sentence as long as the text before the cut, when that is past the
	// bound: one span over two sentences or more.
	if n > 1 && utf8.RuneCountInString(v.Text[m.Sentences[0].Start:m.Sentences[n-1].End]) > redact.SentenceRunesMax {
		out["with a sentence past its runes"] = redact.SentenceMap{Sentences: []redact.Span{{Start: m.Sentences[0].Start, End: m.Sentences[n-1].End}}, Cut: m.Cut}
	}
	return out
}
