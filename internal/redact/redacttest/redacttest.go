// Package redacttest plays redact.Redactor for the tests of its consumers
// (ADR 0040's pull requests 4 and 5, written beside pull request 2), and has
// the contract every Redactor keeps, redact.Fixed included once pull request
// 2 writes it. Never imported outside tests (depguard).
package redacttest

import (
	"slices"
	"strings"
	"testing"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/redact"
)

// Naive is a Redactor that keeps the contract with far simpler shapes than
// ADR 0040's: a run of ASCII digits (no separators) at or above a kind's
// least length, and, for email_address and link, a run of non-space bytes
// holding "@" or "://". Its sentences end after ". ", "! ", "? " and at every
// line break, within redact's bounds. It is for the tests of what consumes
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
	m := redact.SentenceMap{Cut: min(len(v.Text), redact.MeaningTextMax)}
	add := func(start, end int) bool {
		for start < end && space(v.Text[start]) {
			start++
		}
		for end > start && space(v.Text[end-1]) {
			end--
		}
		if end <= start {
			return true
		}
		if len(m.Sentences) == redact.MeaningSentencesMax {
			m.Cut = start
			return false
		}
		m.Sentences = append(m.Sentences, redact.Span{Start: start, End: end})
		return true
	}
	start := 0
	for i := 0; i < m.Cut; i++ {
		c := v.Text[i]
		end := -1
		switch {
		case c == '\n':
			end = i
		case (c == '.' || c == '!' || c == '?') && (i+1 == len(v.Text) || v.Text[i+1] == ' '):
			end = i + 1
		}
		if end < 0 {
			continue
		}
		if !add(start, end) {
			return m
		}
		start = i + 1
	}
	if add(start, m.Cut) && m.Cut < len(v.Text) {
		// A sentence the cut went through is removed with what follows.
		if n := len(m.Sentences); n > 0 && m.Sentences[n-1].End == m.Cut {
			m.Cut = m.Sentences[n-1].Start
			m.Sentences = m.Sentences[:n-1]
		}
	}
	return m
}

func space(c byte) bool { return c == ' ' || c == '\t' || c == '\n' || c == '\r' }

// Contract is what every Redactor keeps (U9, U10, U23's halves that do not
// depend on the patterns): pieces within the text, sorted, of the kinds
// asked, never empty; nothing asked, nothing found; the marker never found;
// a SentenceMap that keeps its invariants, for the empty view, a subject
// alone, past the sentences' bound and past the text's; and Join of what was
// found the input minus pieces, one marker a run.
func Contract(t *testing.T, r redact.Redactor) {
	t.Helper()
	texts := []string{
		"", "Call 5551234567 or ana@example.com today. See https://example.com/a now!",
		"Code 123456 2026 and card 4111111111111111.", contracts.RemovedMarker,
		strings.Repeat("Sentence one. ", 200), strings.Repeat("x", redact.MeaningTextMax+10),
	}
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
		}
	}
}
