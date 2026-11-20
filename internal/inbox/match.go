package inbox

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/decide"
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
// With an Asker on ctx (WithAsker, ADR 0028, "Matching asks the model"), a
// message that needs a model once its exact sources are read is asked about,
// at most MatchMaxModelMessages a page, one after the other, each under
// decide.MessageDeadline or what is left of ctx's, whichever is first; the
// text it is asked with is read only when the Asker calls Question.Text, the
// raw message's under its own grant, counted in MatchMaxTextReads. A message
// asked is listed by its answers (query.Classify, ForQuery): a match,
// nothing, needing a model when atoms the Asker held (AskOff, AskLimit)
// could still make it true, unknown otherwise. AskStop, and the message past
// MatchMaxModelMessages, end the page before the message, with Next the
// message before it. The page then says Model (MatchModelOff, before
// MatchModelLimit, before MatchModelAsked); without an Asker it has none.
//
// With a share on ctx (WithMatchShare), the call is one of several made at
// once for one page, and its bounds of stored items, raw messages and
// messages asked about are that share of a page's.
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
// not depend on how the language evaluates; the routes (pull request 4) and
// the matcher's tests pass query.Eval, which tests wrap to count or slow.
type Evaluate func(fs []*query.Canonical, m *query.Message, answers query.Answers, mode query.Mode) query.Result

// NewMatcher returns the Matcher on a store, which evaluates with eval
// (query.Eval).
func NewMatcher(st ReadStore, eval Evaluate) Matcher {
	return &storeMatcher{r: &storeReader{st: st, now: time.Now}, eval: eval, bounds: defaultMatchBounds, now: time.Now}
}

// matchBounds are a page's bounds; tests set them low. ask is each asked
// message's own deadline (decide.MessageDeadline).
type matchBounds struct {
	read, textReads, undecided, asked int
	soft, ask                         time.Duration
	// shared is a share of a page's (WithMatchShare).
	shared bool
}

var defaultMatchBounds = matchBounds{read: MatchMaxRead, textReads: MatchMaxTextReads, undecided: MatchMaxUndecided, asked: MatchMaxModelMessages, soft: MatchSoftDeadline, ask: decide.MessageDeadline}

// shareKey is the context key of WithMatchShare.
type shareKey struct{}

// WithMatchShare returns ctx in which a Match call is one of of calls made
// at once for one page (the rule editor's preview matches each of a rule's
// criteria on its own, ADR 0040): its MatchMaxRead, MatchMaxTextReads and
// MatchMaxModelMessages are each divided by of, at least two items, one raw
// message and one message asked about, so every call still considers a
// message and the calls together read and ask no more than one page, for
// of up to MatchMaxTextReads; a scan's batch then takes at most half of
// what is left to read, so the entries of its messages fit beside it. Its soft deadline and undecided messages are a
// page's: the calls run at once. of below 2 changes nothing.
func WithMatchShare(ctx context.Context, of int) context.Context {
	return context.WithValue(ctx, shareKey{}, of)
}

// share is b divided among of calls of one page (WithMatchShare).
func (b matchBounds) share(of int) matchBounds {
	if of < 2 {
		return b
	}
	b.read = max(2, b.read/of)
	b.textReads = max(1, b.textReads/of)
	b.asked = max(1, b.asked/of)
	b.shared = true
	return b
}

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
	p := matchRun{mt: mt, b: b, fs: fs, page: page, bounds: mt.bounds}
	if of, ok := ctx.Value(shareKey{}).(int); ok {
		p.bounds = mt.bounds.share(of)
	}
	p.asking, p.askOK = AskerFrom(ctx)
	for _, f := range fs {
		if f != nil && f.Never {
			return p.answer(ctx) // no message matches it, nor them together
		}
	}
	upper, lower := receivedBounds(fs)
	if upper != "" && (before == "" || upper < before) {
		before = upper
	}
	if lower != "" && before != "" && before <= lower {
		return p.answer(ctx) // everything below before is below since
	}
	start := mt.now()
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
		room := p.bounds.read - p.read
		if room < 2 {
			page.Next = last
			break
		}
		batch := min(store.MaxScanRead, room-1)
		if p.bounds.shared {
			// A share's room is small: a batch takes at most half of it, so
			// the entries of its messages fit beside it.
			batch = max(1, min(batch, room/2))
		}
		scan, err := mt.r.st.ScanMessagesDown(ctx, b.MailboxID, before, batch)
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
			if considered && mt.now().Sub(start) >= p.bounds.soft {
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
		if !crossed && scan.Last != "" {
			// The batch went through whole, expired items included.
			last = scan.Last
		}
		if crossed || !scan.More || scan.Read == 0 {
			break // nothing older can match
		}
		before = scan.Last
		if mt.now().Sub(start) >= p.bounds.soft {
			// Past the soft deadline between batches, as within one: runs
			// of expired items are bounded by it too.
			page.Next = last
			break
		}
	}
	if last != "" {
		page.SearchedTo = timeOfMessageID(last)
	}
	return p.answer(ctx)
}

// answer is the page, with what became of asking the model when an Asker
// was set (ADR 0028, "What the page says"): off when one message was
// AskOff, otherwise limit when one was AskLimit, otherwise asked.
func (p *matchRun) answer(ctx context.Context) (*contracts.MatchPage, error) {
	if p.askOK {
		switch {
		case p.off:
			p.page.Model = contracts.MatchModelOff
		case p.limit:
			p.page.Model = contracts.MatchModelLimit
		default:
			p.page.Model = contracts.MatchModelAsked
		}
	}
	return answerMatch(ctx, p.page)
}

// answerMatch is a page, unless the request's context ended while it was
// made.
func answerMatch(ctx context.Context, page *contracts.MatchPage) (*contracts.MatchPage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return page, nil
}

// matchRun is one page in progress: what it read and what it listed, and
// what became of asking the model.
type matchRun struct {
	mt              *storeMatcher
	bounds          matchBounds // mt's, or a share of them (WithMatchShare)
	b               Box
	fs              []*query.Canonical
	page            *contracts.MatchPage
	read, textReads int

	asking Asking
	askOK  bool
	// asked is how many messages were asked about; off and limit whether one
	// of them was AskOff or AskLimit; listed how many messages the page
	// considered whole.
	asked, listed int
	off, limit    bool
}

// full reports whether the page has its limit of matches or of undecided
// messages.
func (p *matchRun) full(limit int) bool {
	return len(p.page.Messages) >= limit || len(p.page.NeedsModel)+len(p.page.Unknown) >= p.bounds.undecided
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
	if v != verdictStop {
		p.listed++
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
	qm := QueryMessage(m, meta, nil, nil)
	res := p.mt.eval(p.fs, qm, nil, query.ForQuery)
	src := &textSource{p: p, m: m}
	if meta.Unparsed {
		// Nothing but the item is known of it, and no other source changes
		// that; it is never asked about (ADR 0028, "What is never asked").
		return p.verdictOf(res, qm), meta, nil
	}
	var entry *SearchEntry
	if res.Value == query.Unknown && res.Needs&query.FromEntry != 0 {
		if p.read >= p.bounds.read {
			return verdictStop, meta, nil
		}
		entry, err = p.readEntry(ctx, m)
		if err != nil {
			return verdictNone, meta, err
		}
		qm = QueryMessage(m, meta, entry, nil)
		res = p.mt.eval(p.fs, qm, nil, query.ForQuery)
	}
	if res.Value == query.Unknown && res.Needs&query.FromText != 0 {
		if p.textReads >= p.bounds.textReads {
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
		qm = QueryMessage(m, meta, entry, text)
		res = p.mt.eval(p.fs, qm, nil, query.ForQuery)
		src.raw = text
	}
	v := p.verdictOf(res, qm)
	if v != verdictNeedsModel || !p.askOK {
		return v, meta, nil
	}
	src.entry = entry
	v, err = p.ask(ctx, m, meta, qm, res.Model, src)
	return v, meta, err
}

// readEntry reads a message's search entry, counted in the page's reads:
// one that is missing, or does not open, is a cut text with nothing in it,
// so the body is read from the raw message.
func (p *matchRun) readEntry(ctx context.Context, m *store.Message) (*SearchEntry, error) {
	p.read++
	e, err := p.mt.r.st.GetEntry(ctx, boxPartition(p.b.MailboxID), KeySearch+m.ID)
	switch {
	case err == nil:
		if se, ok := openSearchEntry(p.b, m.ID, e.Value); ok {
			return &se.SearchEntry, nil
		}
	case !errors.Is(err, store.ErrNotFound):
		return nil, err
	}
	return &SearchEntry{Cut: true}, nil
}

// ask asks the request's Asker about a message that needs a model (ADR
// 0028, "What the matcher does"), and lists it by the answers.
func (p *matchRun) ask(ctx context.Context, m *store.Message, meta *StoredMeta, qm *query.Message, atoms []query.Atom, src *textSource) (verdict, error) {
	if p.asked >= p.bounds.asked {
		return verdictStop, nil // the next page asks it
	}
	q := &Question{
		Box: p.b, MessageID: m.ID, ExpiresAt: m.ExpiresAt, Account: p.asking.Account,
		Atoms: slices.Clone(atoms), Subject: meta.Subject, View: ViewOriginal, Text: src.text,
	}
	actx, cancel := context.WithTimeout(ctx, p.bounds.ask)
	asked, err := p.asking.Asker.Ask(actx, q)
	ranOut := errors.Is(actx.Err(), context.DeadlineExceeded) // before cancel, which sets it
	src.close()
	cancel()
	if err != nil {
		if ranOut && ctx.Err() == nil && errors.Is(err, context.DeadlineExceeded) {
			// The message's own deadline ran out, not the request's: it was
			// asked, and nothing came of it. Any other error, a storage
			// call's own context.DeadlineExceeded included, fails the page.
			p.asked++
			return verdictUnknown, nil
		}
		return verdictNone, err
	}
	if asked == nil {
		asked = &Asked{Outcome: AskAnswered}
	}
	if asked.Outcome == AskStop {
		if p.listed == 0 {
			// The first message of a page has every read of a page left: an
			// Asker that stops it anyway would stop every page there, so it
			// is listed as unknown and the pages go on.
			return verdictUnknown, nil
		}
		return verdictStop, nil // nothing was asked or spent: the next page reads it
	}
	p.asked++
	switch asked.Outcome {
	case AskOff:
		p.off = true
	case AskLimit:
		p.limit = true
	}
	held := make(map[query.Atom]bool, len(asked.Held))
	for _, a := range asked.Held {
		held[a] = true
	}
	answers := make(query.Answers, len(asked.Answers))
	for a, pr := range asked.Answers {
		if !held[a] { // a held atom is unknown, whatever probability came with it
			answers[a] = pr
		}
	}
	res := p.mt.eval(p.fs, qm, answers, query.ForQuery)
	switch res.Value {
	case query.True:
		return verdictMatch, nil
	case query.False:
		return verdictNone, nil
	}
	// Unknown: it needs a model only when the atoms held back, which a
	// later page asks again, could still make it true; an atom left unknown
	// for any other reason stays so.
	var open []query.Atom
	for _, a := range res.Model {
		if held[a] {
			open = append(open, a)
		}
	}
	switch {
	case len(open) == 0:
		return verdictUnknown, nil
	case len(open) > maxModelAtoms:
		return verdictNeedsModel, nil
	}
	for bits := range 1 << len(open) {
		for i, a := range open {
			answers[a] = float64(bits >> i & 1)
		}
		if p.mt.eval(p.fs, qm, answers, query.ForQuery).Value == query.True {
			return verdictNeedsModel, nil
		}
	}
	return verdictUnknown, nil
}

// textSource is Question.Text for one message of a page (ADR 0028, "What
// the matcher does"): the text the page already has (the raw message's,
// read for a body condition, or the entry's when it was not cut); otherwise
// the entry, read if the page has not yet, and when its text was cut the
// raw message's, under its own grant, counted in MatchMaxTextReads and
// given back before it returns. It reads once, and nothing once Ask
// returned.
type textSource struct {
	p     *matchRun
	m     *store.Message
	entry *SearchEntry
	raw   *RawText

	mu     sync.Mutex
	done   bool
	closed bool
	got    string
	st     TextState
	err    error
}

func (s *textSource) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
}

func (s *textSource) text(ctx context.Context) (string, TextState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return "", TextUnaffordable, nil // the page has moved on
	}
	if !s.done {
		s.got, s.st, s.err = s.read(ctx)
		s.done = true
	}
	return s.got, s.st, s.err
}

func (s *textSource) read(ctx context.Context) (string, TextState, error) {
	p := s.p
	if s.raw != nil {
		return s.raw.Text, TextRead, nil
	}
	if s.entry == nil {
		if p.read >= p.bounds.read {
			return "", TextUnaffordable, nil
		}
		e, err := p.readEntry(ctx, s.m)
		if err != nil {
			return "", 0, err
		}
		s.entry = e
	}
	if !entryCut(s.entry) {
		return s.entry.Text, TextRead, nil
	}
	if p.textReads >= p.bounds.textReads {
		return "", TextUnaffordable, nil
	}
	p.textReads++
	var text string
	err := p.mt.r.grantedRaw(ctx, p.b, s.m, func(raw []byte) {
		text, _, _ = MessageText(ctx, raw, MessageTextMax)
	})
	switch {
	case errors.Is(err, contracts.ErrNotFound), errors.Is(err, errRawTooLarge):
		return "", TextUnavailable, nil
	case err != nil:
		return "", 0, err
	}
	if err := ctx.Err(); err != nil {
		return "", 0, err // the text may have been cut by the deadline
	}
	return text, TextRead, nil
}

// maxModelAtoms is how many unanswered atoms verdictOf tries every answer
// of: a formula has at most eight (ADR 0027), so 256 evaluations.
const maxModelAtoms = 8

// verdictOf lists a value of formulas on what is known of m: True a match,
// False nothing, Unknown that a model can make true (no source left to read,
// and some answers of 1 or 0 to the atoms of res.Model make the formulas
// True) needing a model, and any other Unknown unknown: an exact condition
// that could not be read holds it whatever a model says. Past maxModelAtoms
// atoms (formulas together may have more) it is listed as needing a model
// without trying them.
func (p *matchRun) verdictOf(res query.Result, m *query.Message) verdict {
	switch {
	case res.Value == query.True:
		return verdictMatch
	case res.Value == query.False:
		return verdictNone
	case res.Needs != 0 || len(res.Model) == 0:
		return verdictUnknown
	case len(res.Model) > maxModelAtoms:
		return verdictNeedsModel
	}
	answers := make(query.Answers, len(res.Model))
	for bits := range 1 << len(res.Model) {
		for i, a := range res.Model {
			answers[a] = float64(bits >> i & 1)
		}
		if p.mt.eval(p.fs, m, answers, query.ForQuery).Value == query.True {
			return verdictNeedsModel
		}
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
