package redact

import (
	"unicode"
	"unicode/utf8"
)

// Sentences implements Redactor: the split of SplitVersion (ADR 0040,
// "Removing by meaning", "The split"), one pass over the View's runes.
//
// A sentence ends after ".", "!", "?", "…", ";" or "؟" followed by white
// space or the text's end; after "。", "！", "？" or "｡" whatever follows
// (the scripts that use them put no space after them); and before every
// line break ("\n", "\r", "\v", "\f", U+0085, U+2028, U+2029), so the
// subject always ends one. A sentence is trimmed of white space, which
// belongs to none; a sentence that would pass SentenceRunesMax runes ends at
// its last white space within the bound, or at the bound when it has none.
// No list of abbreviations: "Dr. Smith" is two sentences.
//
// Meaning reads at most MeaningTextMax bytes and MeaningSentencesMax
// sentences: Cut is where the first sentence it does not read starts (a
// sentence the byte bound goes through is not read, so the model never sees
// part of one), or len(Text) when it reads them all.
func (Fixed) Sentences(v View) SentenceMap {
	s := splitter{m: SentenceMap{Cut: len(v.Text)}, open: -1, split: -1}
	text := v.Text
	for i, n := 0, 0; i < len(text); n++ {
		r, size := utf8.DecodeRuneInString(text[i:])
		if i+size > MeaningTextMax {
			s.m.Cut = i
			if s.open >= 0 {
				s.m.Cut = s.open
			}
			return s.m
		}
		switch {
		case r == utf8.RuneError && size == 1:
			// An invalid byte is a rune of a sentence, never white space.
			if !s.word(i, n, size, -1) {
				return s.m
			}
		case lineBreak(r):
			s.end()
		case unicode.IsSpace(r):
			if s.pending {
				s.end()
			}
			s.spaced = s.open >= 0
		default:
			if !s.word(i, n, size, r) {
				return s.m
			}
		}
		i += size
	}
	s.end()
	return s.m
}

// splitter is Sentences' state between two runes.
type splitter struct {
	m        SentenceMap
	open     int  // where the open sentence starts, -1 when none is
	openRune int  // the number of its first rune
	lastEnd  int  // where its last rune that is not white space ends
	spaced   bool // white space came after that rune
	pending  bool // that rune ends the sentence if white space or the end follows
	// Where the open sentence ends if it is split at its last white space,
	// -1 when it has none, and where (and at which rune) the rest starts.
	split, rest, restRune int
}

// end closes the open sentence, if any, after its last rune that is not
// white space.
func (s *splitter) end() {
	if s.open >= 0 {
		s.m.Sentences = append(s.m.Sentences, Span{s.open, s.lastEnd})
	}
	s.open, s.pending, s.spaced, s.split = -1, false, false, -1
}

// start opens a sentence at i, rune n, unless the map has its every
// sentence: then i is the Cut, and start says false.
func (s *splitter) start(i, n int) bool {
	if len(s.m.Sentences) == MeaningSentencesMax {
		s.m.Cut = i
		return false
	}
	s.open, s.openRune, s.spaced, s.split = i, n, false, -1
	return true
}

// word reads a rune that is not white space, r (-1 for an invalid byte), at
// i, rune n, size bytes long; false when the map is full.
func (s *splitter) word(i, n, size int, r rune) bool {
	switch {
	case s.open < 0:
		if !s.start(i, n) {
			return false
		}
	case s.spaced:
		s.split, s.rest, s.restRune, s.spaced = s.lastEnd, i, n, false
	}
	if n-s.openRune+1 > SentenceRunesMax {
		if s.split >= 0 {
			s.lastEnd = s.split
			rest, restRune := s.rest, s.restRune
			s.end()
			if !s.start(rest, restRune) {
				return false
			}
		} else {
			s.lastEnd = i
			s.end()
			if !s.start(i, n) {
				return false
			}
		}
	}
	s.lastEnd, s.pending = i+size, false
	switch r {
	case '.', '!', '?', '…', ';', '؟':
		s.pending = true
	case '。', '！', '？', '｡':
		s.end()
	}
	return true
}

// lineBreak is a rune that ends a line.
func lineBreak(r rune) bool {
	switch r {
	case '\n', '\r', '\v', '\f', 0x85, 0x2028, 0x2029:
		return true
	}
	return false
}
