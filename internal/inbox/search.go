package inbox

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/envelope"
	"github.com/permaily-org/permaily/internal/store"
)

// Search implements Reader (ADR 0026, "Searching"): the box's txt# entries
// from below before, the newest first, a hundred at a time, each opened and
// matched, until limit matches or SearchMaxRead entries read. Every item the
// store reads counts, an expired one and one that does not open under the
// key included. A match whose message item is gone is left out (it
// counted). Next is the message identifier of the last entry the search
// consumed, matched or not; SearchedTo is when that entry's message arrived.
func (r *storeReader) Search(ctx context.Context, b Box, q, before string, limit int) (*contracts.MessagePage, error) {
	terms := searchTerms(q)
	page := &contracts.MessagePage{Messages: []contracts.MessageSummary{}}
	read := 0
	after := ""
	if before != "" {
		after = KeySearch + before
	}
	// last is the last entry consumed, and when its message arrived.
	var last string
	var lastAt time.Time
	for read < SearchMaxRead {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		scan, err := r.st.ScanEntriesDown(ctx, boxPartition(b.MailboxID), KeySearch, after, min(store.MaxScanRead, SearchMaxRead-read))
		if err != nil {
			return nil, err
		}
		read += scan.Read
		for i := range scan.Entries {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			e := &scan.Entries[i]
			id := strings.TrimPrefix(e.Key, KeySearch)
			entry, ok := openSearchEntry(b, id, e.Value)
			if !ok || !entry.matches(terms) {
				continue
			}
			m, meta, err := r.item(ctx, b, id)
			if err != nil {
				if errors.Is(err, contracts.ErrNotFound) {
					continue // its item is gone or expired: left out, counted
				}
				return nil, err
			}
			page.Messages = append(page.Messages, summaryOf(m, meta))
			if len(page.Messages) == limit && i < len(scan.Entries)-1 {
				// Stopped inside the batch: the rest of it is read again by
				// the next page, never skipped.
				page.Next = id
				page.SearchedTo = searchedTo(id, entry.ReceivedAt)
				return answer(ctx, page)
			}
		}
		if scan.Last != "" {
			last = strings.TrimPrefix(scan.Last, KeySearch)
			lastAt = time.Time{}
			if n := len(scan.Entries); n > 0 && scan.Entries[n-1].Key == scan.Last {
				if entry, ok := openSearchEntry(b, last, scan.Entries[n-1].Value); ok {
					lastAt = entry.ReceivedAt
				}
			}
		}
		if len(page.Messages) == limit {
			if scan.More {
				page.Next = last
			}
			page.SearchedTo = searchedTo(last, lastAt)
			return answer(ctx, page)
		}
		if !scan.More || scan.Read == 0 {
			page.SearchedTo = searchedTo(last, lastAt)
			return answer(ctx, page)
		}
		after = scan.Last
	}
	page.Next = last
	page.SearchedTo = searchedTo(last, lastAt)
	return answer(ctx, page)
}

// searchedTo is when the message of the last entry a page consumed arrived:
// its search entry's time when it opened, otherwise the time its identifier
// carries. Zero when the page consumed none.
func searchedTo(id string, at time.Time) time.Time {
	if id == "" {
		return time.Time{}
	}
	if !at.IsZero() {
		return at.UTC()
	}
	if !isMessageIDShape(id) {
		return time.Time{}
	}
	return timeOfMessageID(id)
}

// searchTerms splits a query on white space, each part case-folded.
func searchTerms(q string) []string {
	return strings.Fields(foldCase(q))
}

// foldCase is s under Unicode simple case folding: each character becomes the
// smallest of its orbit under unicode.SimpleFold (Σ, σ and ς are Σ; K, k and
// the Kelvin sign are K; İ and ı are each only themselves), so two strings
// that differ only in case fold to the same string, and a character folds to
// one character, never to several (ß stays ß, not ss). There is no Unicode
// normalisation: accents, and composed and decomposed forms, stay distinct.
// It allocates once, for the result, and not at all when nothing changes.
func foldCase(s string) string {
	return strings.Map(foldRune, s)
}

func foldRune(r rune) rune {
	if r < utf8.RuneSelf {
		if 'a' <= r && r <= 'z' {
			return r - 'a' + 'A'
		}
		return r
	}
	least := r
	for f := unicode.SimpleFold(r); f != r; f = unicode.SimpleFold(f) {
		least = min(least, f)
	}
	return least
}

type searchEntry struct{ SearchEntry }

func openSearchEntry(b Box, id string, value []byte) (*searchEntry, bool) {
	if !isMessageIDShape(id) {
		return nil, false
	}
	plain, err := envelope.OpenText(b.Key, value, envelope.AAD(PurposeSearch, b.MailboxID, id))
	if err != nil {
		return nil, false
	}
	var e searchEntry
	if json.Unmarshal(plain, &e.SearchEntry) != nil {
		return nil, false
	}
	return &e, true
}

// matches says whether every term (case-folded, searchTerms) appears,
// without regard to case, in one of the senders' names and addresses, the
// subject or the text. Each field is folded once per entry, whatever the
// number of terms.
func (e *searchEntry) matches(terms []string) bool {
	fields := make([]string, 0, 2+2*len(e.From))
	for _, a := range e.From {
		fields = append(fields, foldCase(a.Name), foldCase(a.Address))
	}
	text := e.Text
	if len(text) > SearchTextMax {
		text, _ = cutText(text, SearchTextMax)
	}
	fields = append(fields, foldCase(e.Subject), foldCase(text))
	for _, t := range terms {
		found := false
		for _, f := range fields {
			if strings.Contains(f, t) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
