package inbox

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/query"
	"github.com/permaily-org/permaily/internal/store"
)

// The seam of ADR 0027 between the query language (internal/query, its pull
// request 1) and stored mail (its pull request 3, which implements Matcher on
// the store next to Reader, makes the reader's raw reads for it go through
// ReadGrants, and writes what delivery adds for it: the List-Id and the size
// in the meta, the meta's format, and whether a search entry's text was cut).

// Matcher is a page of the messages of an opened mailbox that formulas match
// together (ADR 0027, "Evaluation over stored mail"): fs are canonical and
// never simplified together (a consented scope, the request that fits in it,
// a preview's formula), and a message matches when the least of their values
// is True. It scans the box's message items newest first from below before
// ("" for the newest), opens each item's sealed meta before it classifies
// the message, reads the search entry only when the value still depends on a
// body condition, and the raw message only when that body condition depends
// on a text the entry cut. It ends the page when it has limit matches (1 to
// 100), MatchMaxUndecided messages that need a model or whose data could not
// be read, or when it reaches MatchMaxRead, MatchMaxTextReads or
// MatchSoftDeadline. A message is either considered whole on a page or left
// for the next: Next is the last message considered, so pages skip nothing. A
// received condition that bounds every match bounds the scan by the time in
// the message identifiers: before is where the scan starts; since stops it
// after the first batch that crosses it (the scan has no lower bound, so at
// most one batch is read below it), and nothing below it is evaluated or
// listed. Search entries are read by key (ReadStore.GetEntry), never
// scanned.
//
// Each raw read takes its bound from the request's read budget through
// ReadGrants and gives it back as soon as that message's text is taken out,
// before the next raw read, so a page holds one raw message's reservation at
// a time. A read budget in ctx that is not a ReadGrants is refused with
// contracts.ErrTooManyRequests before the first raw read; with no budget in
// ctx, nothing is taken (tests of the reader alone).
//
// The page it answers is the owner's own preview (contracts.MatchPage), never
// a vendor's result. It checks nothing of who asks: mailboxes.Service.Open
// did.
type Matcher interface {
	Match(ctx context.Context, b Box, fs []*query.Canonical, before string, limit int) (*contracts.MatchPage, error)
}

// What a page of matches is bounded by (ADR 0027, "Budgets of a page").
const (
	// MatchMaxRead is how many stored items a page reads at most: message
	// items and search entries, scanned or read one by one, expired ones
	// included.
	MatchMaxRead = 500
	// MatchMaxTextReads is how many raw messages a page reads at most, one
	// at a time, each under its own grant of the read budget, for a body
	// condition whose entry's text was cut.
	MatchMaxTextReads = 10
	// MatchMaxUndecided is how many messages a page lists as needing a model
	// or as unknown.
	MatchMaxUndecided = 100
	// MatchSoftDeadline is how long a page considers messages before it
	// answers what it has, well inside mailboxes.RequestDeadline.
	MatchSoftDeadline = 20 * time.Second
)

// Evaluate is the value of formulas evaluated together on what is known of a
// message: query.Eval, whose contract Matcher reads (ADR 0027, "The
// functions each pull request writes"). It is a parameter so the reader does
// not depend on how the language evaluates, and its tests can hold it to
// that contract alone.
type Evaluate func(fs []*query.Canonical, m *query.Message, answers query.Answers, mode query.Mode) query.Result

// NewMatcher returns the Matcher on a store, which evaluates with eval
// (query.Eval).
func NewMatcher(st ReadStore, eval Evaluate) Matcher {
	return &storeMatcher{r: &storeReader{st: st}, eval: eval, bounds: defaultMatchBounds, now: time.Now}
}

// matchBounds are a page's bounds; tests set them low.
type matchBounds struct {
	read, textReads, undecided int
	soft                       time.Duration
}

var defaultMatchBounds = matchBounds{read: MatchMaxRead, textReads: MatchMaxTextReads, undecided: MatchMaxUndecided, soft: MatchSoftDeadline}

type storeMatcher struct {
	r      *storeReader
	eval   Evaluate
	bounds matchBounds
	now    func() time.Time
}

// matchLimit is the most matches a page may be asked for.
const matchLimit = 100

// What considering one message gives.
type verdict int8

const (
	verdictNone       verdict = iota // false, or passed over: counted, never listed
	verdictMatch                     // true on every formula
	verdictNeedsModel                // depends on a condition a model decides
	verdictUnknown                   // depends on data that could not be read
	verdictStop                      // not affordable on this page: left whole for the next
)

// Match implements Matcher.
func (mt *storeMatcher) Match(ctx context.Context, b Box, fs []*query.Canonical, before string, limit int) (*contracts.MatchPage, error) {
	if limit < 1 || limit > matchLimit {
		return nil, contracts.ErrInvalidLimit
	}
	if before != "" && !isMessageIDShape(before) {
		return nil, contracts.ErrInvalidCursor
	}
	page := &contracts.MatchPage{Messages: []contracts.MessageSummary{}, NeedsModel: []string{}, Unknown: []string{}}
	for _, f := range fs {
		if f != nil && f.Never {
			return answerMatch(ctx, page) // no message matches it, nor them together
		}
	}
	upper, lower := receivedBounds(fs)
	if upper != "" && (before == "" || upper < before) {
		before = upper
	}
	if lower != "" && before != "" && before <= lower {
		return answerMatch(ctx, page) // everything below before is below since
	}
	start := mt.now()
	p := matchRun{mt: mt, b: b, fs: fs, page: page}
	// last is the identifier of the last item the page consumed: a message
	// considered, or the last item of a batch it went through whole.
	last := ""
	considered := false
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		// One item of the room is kept for the entry of the batch's first
		// message, so every page considers at least one message.
		room := mt.bounds.read - p.read
		if room < 2 {
			page.Next = last
			break
		}
		scan, err := mt.r.st.ScanMessagesDown(ctx, b.MailboxID, before, min(store.MaxScanRead, room-1))
		if err != nil {
			return nil, err
		}
		p.read += scan.Read
		crossed := lower != "" && scan.Last != "" && scan.Last < lower
		stopped := false
		for i := range scan.Messages {
			m := &scan.Messages[i]
			if lower != "" && m.ID < lower {
				break // received before since: never evaluated
			}
			if considered && mt.now().Sub(start) >= mt.bounds.soft {
				page.Next, stopped = last, true
				break
			}
			v, err := p.consider(ctx, m)
			if err != nil {
				return nil, err
			}
			if v == verdictStop {
				page.Next, stopped = last, true
				break
			}
			last, considered = m.ID, true
			if !p.full(limit) {
				continue
			}
			stopped = true
			switch {
			case i < len(scan.Messages)-1 && (lower == "" || scan.Messages[i+1].ID >= lower):
				// Full inside the batch: the rest of it is read again by the
				// next page, never skipped.
				page.Next = m.ID
			case !crossed && scan.More:
				page.Next = scan.Last
				last = scan.Last
			}
			break
		}
		if stopped {
			break
		}
		if crossed || !scan.More || scan.Read == 0 {
			break // nothing older can match
		}
		before, last = scan.Last, scan.Last
	}
	if last != "" {
		page.SearchedTo = timeOfMessageID(last)
	}
	return answerMatch(ctx, page)
}

// answerMatch is a page, unless the request's context ended while it was
// made.
func answerMatch(ctx context.Context, page *contracts.MatchPage) (*contracts.MatchPage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return page, nil
}

// matchRun is one page in progress: what it read and what it listed.
type matchRun struct {
	mt              *storeMatcher
	b               Box
	fs              []*query.Canonical
	page            *contracts.MatchPage
	read, textReads int
}

// full reports whether the page has its limit of matches or of undecided
// messages.
func (p *matchRun) full(limit int) bool {
	return len(p.page.Messages) >= limit || len(p.page.NeedsModel)+len(p.page.Unknown) >= p.mt.bounds.undecided
}

// consider classifies one message, reading its sources in order, each only
// while the value still depends on it (ADR 0027, "Which reads"), and lists
// it. verdictStop when a read it needs is not affordable on this page:
// nothing of it is listed, and the next page starts with it.
func (p *matchRun) consider(ctx context.Context, m *store.Message) (verdict, error) {
	if err := ctx.Err(); err != nil {
		return verdictNone, err
	}
	v, meta, err := p.classify(ctx, m)
	if err != nil {
		return verdictNone, err
	}
	switch v {
	case verdictMatch:
		p.page.Messages = append(p.page.Messages, summaryOf(m, meta))
	case verdictNeedsModel:
		p.page.NeedsModel = append(p.page.NeedsModel, m.ID)
	case verdictUnknown:
		p.page.Unknown = append(p.page.Unknown, m.ID)
	}
	return v, nil
}

// classify is consider's reading and evaluation; it also returns the meta it
// opened.
func (p *matchRun) classify(ctx context.Context, m *store.Message) (verdict, *StoredMeta, error) {
	meta, err := openMeta(p.b, m)
	if err != nil {
		return verdictUnknown, nil, nil // its meta does not open: nothing of it is known
	}
	if meta.Confirmation {
		return verdictNone, meta, nil // Gmail's forwarding confirmation is Permaily's, and never leaves
	}
	res := p.mt.eval(p.fs, QueryMessage(m, meta, nil, nil), nil, query.ForQuery)
	if meta.Unparsed {
		// Nothing but the item is known of it, and no other source changes
		// that.
		return verdictOf(res), meta, nil
	}
	var entry *SearchEntry
	if res.Value == query.Unknown && res.Needs&query.FromEntry != 0 {
		if p.read >= p.mt.bounds.read {
			return verdictStop, meta, nil
		}
		p.read++
		e, err := p.mt.r.st.GetEntry(ctx, boxPartition(p.b.MailboxID), KeySearch+m.ID)
		switch {
		case err == nil:
			if se, ok := openSearchEntry(p.b, m.ID, e.Value); ok {
				entry = &se.SearchEntry
			}
		case !errors.Is(err, store.ErrNotFound):
			return verdictNone, meta, err
		}
		if entry == nil {
			// Missing, or does not open: the body is read from the raw
			// message, as a cut text's is.
			entry = &SearchEntry{Cut: true}
		}
		res = p.mt.eval(p.fs, QueryMessage(m, meta, entry, nil), nil, query.ForQuery)
	}
	if res.Value == query.Unknown && res.Needs&query.FromText != 0 {
		if p.textReads >= p.mt.bounds.textReads {
			return verdictStop, meta, nil
		}
		p.textReads++
		var text *RawText
		err := p.mt.r.grantedRaw(ctx, p.b, m, func(raw []byte) {
			t, _, _ := MessageText(ctx, raw, MessageTextMax)
			text = &RawText{Text: t, Size: int64(len(raw))}
		})
		switch {
		case errors.Is(err, contracts.ErrNotFound), errors.Is(err, errRawTooLarge):
			return verdictUnknown, meta, nil // its raw message cannot be read
		case err != nil:
			return verdictNone, meta, err
		}
		if err := ctx.Err(); err != nil {
			return verdictNone, meta, err // the text may have been cut by the deadline
		}
		res = p.mt.eval(p.fs, QueryMessage(m, meta, entry, text), nil, query.ForQuery)
	}
	return verdictOf(res), meta, nil
}

// verdictOf lists a value: True a match, False nothing, Unknown that only a
// model can settle (no source left to read, atoms to ask) needing a model,
// and any other Unknown unknown.
func verdictOf(res query.Result) verdict {
	switch {
	case res.Value == query.True:
		return verdictMatch
	case res.Value == query.False:
		return verdictNone
	case res.Needs == 0 && len(res.Model) > 0:
		return verdictNeedsModel
	}
	return verdictUnknown
}

// receivedBounds are the message identifiers that bound the scan (ADR 0027,
// "What is indexed, and what is scanned"): upper, the smallest identifier of
// the earliest t of a group of one "received before t" of any formula (no
// message at or after it is true on that formula, so on them together), and
// lower, the smallest identifier of the latest s of a group of one
// "received since s" (no message below it is). "" for none. Canonical forms
// make received conditions positive.
func receivedBounds(fs []*query.Canonical) (upper, lower string) {
	for _, f := range fs {
		if f == nil {
			continue
		}
		for _, g := range f.Formula.All {
			if len(g.Any) != 1 || g.Any[0].Var != contracts.VarReceived || g.Any[0].Not {
				continue
			}
			t, err := time.Parse(time.RFC3339, g.Any[0].Value)
			if err != nil || t.UnixNano() < 0 {
				continue
			}
			id := idAt(t)
			switch g.Any[0].Op {
			case contracts.OpBefore:
				if upper == "" || id < upper {
					upper = id
				}
			case contracts.OpSince:
				if id > lower {
					lower = id
				}
			}
		}
	}
	return upper, lower
}

// idAt is the smallest message identifier of the nanosecond t: its time in
// sixteen hexadecimal digits, then zeros.
func idAt(t time.Time) string {
	return fmt.Sprintf("%016x", uint64(t.UnixNano())) + "0000000000000000" //nolint:gosec // not before 1970, checked
}
