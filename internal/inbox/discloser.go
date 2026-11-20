package inbox

import (
	"context"
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/query"
	"github.com/permaily-org/permaily/internal/redact"
	"github.com/permaily-org/permaily/internal/rules"
	"github.com/permaily-org/permaily/internal/store"
)

// The implementation of Discloser on the store (ADR 0030, "What a read
// gives"; ADR 0031, "Holds that cannot be undone"), beside the seam of
// disclose.go. A page goes in two passes. The first walks the mailbox as
// Matcher does, newest first and under its budgets, reading each message's
// sources only while the formulas or a rule still depend on them, and keeps
// the messages the formulas could match (True, or Unknown on a model alone);
// a rule's value on them is exact (a formula of it True, or Unknown on data
// that cannot be read: held, nothing committed) or rests on the model. The
// second begins the rules' decisions that rest on the model, one call per
// rule and day (Decisions.Begin), before anything is asked for them; asks,
// once per message, the atoms of the rules it began and the formulas' own
// (one ask, one kept entry); settles each decision as SettleAfterAsk says,
// one call per rule and day; and lists each message as it left or was held.
// A message leaves only when the formulas are True and every rule that
// needed the model answered Cleared, from Begin or Settle.

// NewDiscloser returns the Discloser on a store, which evaluates with eval
// (query.Eval) and keeps the rules' decisions in d (services/mailboxes'
// Decisions, BlockDecisions over its sealed records). The Discloser it
// returns also counts what DecisionsFullCounter says.
func NewDiscloser(st ReadStore, eval Evaluate, d Decisions) Discloser {
	return &storeDiscloser{r: &storeReader{st: st, now: time.Now}, eval: eval, decisions: d, redactor: redact.Fixed{}, bounds: defaultMatchBounds, now: time.Now, settle: settleWithin}
}

// DecisionsFullCounter is implemented by the Discloser NewDiscloser returns:
// DecisionsFull is how many messages of the requests it answered were held,
// undecided, because a rule's decision had no room in its day (DecisionFull,
// whether the day is spent or its tail waits for its Pending to settle),
// since it was made. It counts each message once per request it was held in,
// by the final outcome, never by a store's internal retries; a message past
// its expiry (DecisionExpired) is not counted, and a later request that
// decides the message does not undo the count. It is /status's
// decisions_full (ADR 0031, "Bounds", and the ruling of the fifth design
// review): capacity refusals of messages in requests, not a gauge of full
// days, of distinct messages or of stored decisions.
type DecisionsFullCounter interface {
	DecisionsFull() int64
}

// settleWithin bounds the settlement of a page's decisions, which is made
// even once the request's own context has ended: a decision begun and not
// settled is held until PendingGrace and recovered Held, never released, so
// this only spares a held message its grace.
const settleWithin = 10 * time.Second

type storeDiscloser struct {
	r         *storeReader
	eval      Evaluate
	decisions Decisions
	// redactor finds the fixed shapes and splits the one view (ADR 0040).
	redactor redact.Redactor
	bounds   matchBounds
	now      func() time.Time
	settle   time.Duration
	full     atomic.Int64
}

// DecisionsFull implements DecisionsFullCounter.
func (s *storeDiscloser) DecisionsFull() int64 { return s.full.Load() }

// Page implements Discloser.
func (s *storeDiscloser) Page(ctx context.Context, b Box, d Disclosure, before string, limit int) (*DisclosePage, error) {
	if limit < 1 || limit > DiscloseMaxLimit {
		return nil, contracts.ErrInvalidLimit
	}
	if before != "" && !isMessageIDShape(before) {
		return nil, contracts.ErrInvalidCursor
	}
	p := s.run(ctx, b, d, true)
	page := &DisclosePage{Items: []Outcome{}}
	for _, f := range d.Formulas {
		if f != nil && f.Never {
			page.Model = p.modelOutcome()
			return answerDisclosed(ctx, page)
		}
	}
	cands, next, err := p.walk(ctx, before, limit)
	if err != nil {
		return nil, err
	}
	cut, err := p.decide(ctx, cands)
	if err != nil {
		return nil, err
	}
	if cut, err = p.reduce(ctx, cands[:cut]); err != nil {
		return nil, err
	}
	if cut < len(cands) {
		next = cands[cut-1].m.ID // what was not asked is the next page's
		cands = cands[:cut]
	}
	for _, c := range cands {
		if o, ok := p.outcome(c); ok {
			page.Items = append(page.Items, o)
		}
	}
	page.Next = next
	page.Model = p.modelOutcome()
	return answerDisclosed(ctx, page)
}

// Message implements Discloser.
func (s *storeDiscloser) Message(ctx context.Context, b Box, d Disclosure, id string) (Outcome, error) {
	p := s.run(ctx, b, d, false)
	c, err := p.one(ctx, id)
	if err != nil {
		return Outcome{}, err
	}
	o, ok := p.outcome(c)
	if !ok {
		return Outcome{}, contracts.ErrNotFound
	}
	if o.Disclosed != nil && slices.Contains(d.Parts, contracts.PartBody) {
		text, cut, err := p.leavingText(ctx, c)
		if err != nil {
			return Outcome{}, err
		}
		o.Disclosed.Message.Text, o.Disclosed.Message.TextCut = &text, cut
	}
	return o, ctx.Err()
}

// Attachment implements Discloser.
func (s *storeDiscloser) Attachment(ctx context.Context, b Box, d Disclosure, id string, index int) (Outcome, *Attachment, error) {
	if !slices.Contains(d.Parts, contracts.PartAttachments) {
		return Outcome{}, nil, contracts.ErrPartNotApproved
	}
	p := s.run(ctx, b, d, false)
	c, err := p.one(ctx, id)
	if err != nil {
		return Outcome{}, nil, err
	}
	o, ok := p.outcome(c)
	switch {
	case !ok:
		return Outcome{}, nil, contracts.ErrNotFound
	case o.Withheld != nil:
		return o, nil, nil // the vendor is told not_found; the record, why
	}
	at := slices.IndexFunc(c.meta.Attachments, func(a contracts.MessageAttachment) bool { return a.Index == index })
	if at < 0 || c.meta.Unparsed || c.reduced != nil && c.reduced.attachments {
		// A rule removed the attachments: none of them leaves (ADR 0040).
		return Outcome{}, nil, contracts.ErrNotFound
	}
	if c.meta.Attachments[at].Size > contracts.VendorAttachmentMax {
		return Outcome{}, nil, contracts.ErrAttachmentTooLarge
	}
	name, data, err := s.r.Attachment(ctx, b, id, index)
	if err != nil {
		return Outcome{}, nil, err
	}
	if len(data) > contracts.VendorAttachmentMax {
		return Outcome{}, nil, contracts.ErrAttachmentTooLarge
	}
	return o, &Attachment{Name: name, Type: c.meta.Attachments[at].Type, Data: data}, ctx.Err()
}

// answerDisclosed is a page, unless the request's context ended while it
// was made.
func answerDisclosed(ctx context.Context, page *DisclosePage) (*DisclosePage, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return page, nil
}

// discloseRun is one request in progress: what it read, what it asked, and
// what became of asking.
type discloseRun struct {
	s      *storeDiscloser
	b      Box
	d      Disclosure
	asking Asking
	// model says the model may be asked: Disclosure.Model, with an Asker on
	// the request's context.
	model bool
	// page: raw reads go through ReadGrants one at a time, as a page of
	// matches' do; a single message's take from ReadBudget, as Reader.Get's.
	page                   bool
	read, textReads, toAsk int
	// sentences asks the model about a removal's topics, sentence by
	// sentence (ADR 0040): Asking.Sentences, or the Asker when it is one.
	sentences  SentenceAsker
	off, limit bool
	start      time.Time
}

func (s *storeDiscloser) run(ctx context.Context, b Box, d Disclosure, page bool) *discloseRun {
	p := &discloseRun{s: s, b: b, d: d, page: page, start: s.now()}
	asking, ok := AskerFrom(ctx)
	p.asking, p.model = asking, ok && d.Model
	if p.model {
		p.sentences = asking.Sentences
		if p.sentences == nil {
			p.sentences, _ = asking.Asker.(SentenceAsker)
		}
	}
	return p
}

// modelOutcome is what became of asking the model (DisclosePage.Model):
// off when it could not be asked at all or one message was AskOff,
// otherwise limit when one was AskLimit, otherwise asked.
func (p *discloseRun) modelOutcome() contracts.MatchModel {
	switch {
	case !p.model || p.off:
		return contracts.MatchModelOff
	case p.limit:
		return contracts.MatchModelLimit
	}
	return contracts.MatchModelAsked
}

// candidate is a message the formulas could match, as the first pass read
// it, and what the second made of it.
type candidate struct {
	m     *store.Message
	meta  *StoredMeta
	entry *SearchEntry
	raw   *RawText
	// rawGone: its raw message cannot be read (missing, past its bound);
	// textCut: the text taken out of it was cut at MessageTextMax.
	rawGone, textCut bool
	qm               *query.Message
	// fs is the formulas' value on the exact sources; fsModel says it is
	// Unknown on the model alone and some answers make it True.
	fs      query.Result
	fsModel bool
	// exact is the first rule that holds it without the model, -1 for none.
	exact int
	rules []ruleNeed
	src   *discloseText

	asked   *Asked
	answers query.Answers
	// expired: a decision found its message past its expiry.
	expired bool
	// removable: a rule that removes details may apply to it, so the
	// formulas are read on what it leaves (ADR 0040, "The order"), never
	// on the original: their model atoms are asked in reduce.
	removable bool
	// view, smap and smapOK are its one View and that view's SentenceMap
	// (ADR 0040), made in reduce when a removal reads its text.
	view   *redact.View
	viewOK bool
	smap   redact.SentenceMap
	smapOK bool
	// reduced is what the rules that remove left of it, nil when none
	// applied.
	reduced *reduction
}

// ruleNeed is one rule's value on a candidate that rests on the model.
type ruleNeed struct {
	need bool
	// applies: a rule that removes applies without the model (a formula
	// True, Unknown on data that cannot be read, or the model off for it).
	applies bool
	atoms   []query.Atom
	// failed: its day's blocks could not be read or written.
	failed bool
	dec    Decision // what Begin answered, then Settle
	// meaning is the rule's meaning decision (rules.MeaningRef), and
	// meaningFailed says its day's blocks could not be read or written.
	meaning       Decision
	meaningFailed bool
	meaningAsked  *SentencesAsked
}

// walk is the first pass: the candidates of a page in order, and its next.
func (p *discloseRun) walk(ctx context.Context, before string, limit int) ([]*candidate, string, error) {
	bounds := p.s.bounds
	upper, lower := receivedBounds(p.d.Formulas)
	if upper != "" && (before == "" || upper < before) {
		before = upper
	}
	if lower != "" && before != "" && before <= lower {
		return nil, "", nil
	}
	var cands []*candidate
	next, last := "", ""
	considered := false
	for {
		if err := ctx.Err(); err != nil {
			return nil, "", err
		}
		room := bounds.read - p.read
		if room < 2 {
			return cands, last, nil
		}
		scan, err := p.s.r.st.ScanMessagesDown(ctx, p.b.MailboxID, before, min(store.MaxScanRead, room-1))
		if err != nil {
			return nil, "", err
		}
		p.read += scan.Read
		crossed := lower != "" && scan.Last != "" && scan.Last < lower
		for i := range scan.Messages {
			m := &scan.Messages[i]
			if lower != "" && m.ID < lower {
				break
			}
			if considered && p.s.now().Sub(p.start) >= bounds.soft {
				return cands, last, nil
			}
			c, v, err := p.consider(ctx, m)
			if err != nil {
				return nil, "", err
			}
			if v == verdictStop {
				return cands, last, nil
			}
			last, considered = m.ID, true
			if c != nil {
				cands = append(cands, c)
			}
			if len(cands) < limit {
				continue
			}
			switch {
			case i < len(scan.Messages)-1 && (lower == "" || scan.Messages[i+1].ID >= lower):
				next = m.ID
			case !crossed && scan.More:
				next = scan.Last
			}
			return cands, next, nil
		}
		if !crossed && scan.Last != "" {
			last = scan.Last
		}
		if crossed || !scan.More || scan.Read == 0 {
			return cands, "", nil
		}
		before = scan.Last
		if p.s.now().Sub(p.start) >= bounds.soft {
			return cands, last, nil
		}
	}
}

// one is a single message's candidate, decided: contracts.ErrNotFound when
// it does not exist, does not open, is a forwarding confirmation, or the
// formulas cannot match it.
func (p *discloseRun) one(ctx context.Context, id string) (*candidate, error) {
	if !isMessageIDShape(id) {
		return nil, contracts.ErrNotFound
	}
	m, err := p.s.r.st.Item(ctx, p.b.MailboxID, id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, contracts.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	for _, f := range p.d.Formulas {
		if f != nil && f.Never {
			return nil, contracts.ErrNotFound
		}
	}
	// A single message's reads are always affordable: it never stops.
	c, _, err := p.consider(ctx, m)
	switch {
	case err != nil:
		return nil, err
	case c == nil:
		return nil, contracts.ErrNotFound
	}
	if _, err := p.decide(ctx, []*candidate{c}); err != nil {
		return nil, err
	}
	if _, err := p.reduce(ctx, []*candidate{c}); err != nil {
		return nil, err
	}
	return c, nil
}

// consider reads one message's sources in order, each only while the
// formulas or a rule's exact formulas still depend on it, and says what it
// is: a candidate (nil for none: the formulas are False, or cannot be True
// on what can be read), or verdictStop when a read it needs is not
// affordable on this page (the next page starts with it).
func (p *discloseRun) consider(ctx context.Context, m *store.Message) (*candidate, verdict, error) {
	if err := ctx.Err(); err != nil {
		return nil, verdictNone, err
	}
	if !isMessageIDShape(m.ID) {
		return nil, verdictNone, nil // names no message: nothing of it opens
	}
	if !p.s.now().Before(lifetimeOf(m.ID)) {
		return nil, verdictNone, nil // past the longest it may be kept: gone
	}
	meta, err := openMeta(p.b, m)
	if err != nil || meta.Confirmation {
		return nil, verdictNone, nil // nothing of it is known, or it is Permaily's
	}
	c := &candidate{m: m, meta: meta, exact: -1, qm: QueryMessage(m, meta, nil, nil)}
	for {
		needs := p.evaluate(c)
		if c.fs.Value == query.False && !p.mayRemove(c) {
			return nil, verdictNone, nil
		}
		if needs == 0 || meta.Unparsed || c.exact >= 0 {
			break
		}
		if needs&query.FromEntry != 0 && c.entry == nil {
			if p.read >= p.s.bounds.read {
				return nil, verdictStop, nil
			}
			if c.entry, err = p.readEntry(ctx, m); err != nil {
				return nil, verdictNone, err
			}
			c.qm = QueryMessage(m, meta, c.entry, nil)
			continue
		}
		if needs&query.FromText != 0 && c.raw == nil && !c.rawGone {
			if p.textReads >= p.s.bounds.textReads {
				return nil, verdictStop, nil
			}
			p.textReads++
			if err := p.readText(ctx, c); err != nil {
				return nil, verdictNone, err
			}
			if c.raw != nil {
				c.qm = QueryMessage(m, meta, c.entry, c.raw)
				continue
			}
		}
		break
	}
	if !p.classify(c) {
		return nil, verdictNone, nil
	}
	if p.needsAsk(c) {
		if p.page && p.toAsk >= p.s.bounds.asked {
			return nil, verdictStop, nil // the next page asks it
		}
		p.toAsk++
	}
	c.src = &discloseText{p: p, c: c}
	return c, verdictNone, nil
}

// evaluate is the formulas' value and each rule's exact value on what is
// read of c, and the sources that could still change one of them.
func (p *discloseRun) evaluate(c *candidate) query.Sources {
	eval := p.s.eval
	c.fs = eval(p.d.Formulas, c.qm, nil, query.ForQuery)
	var needs query.Sources
	if c.fs.Value == query.Unknown {
		needs |= c.fs.Needs
	}
	c.exact = -1
	view := false
	for i, r := range p.d.Rules {
		applies := false
		for _, f := range r.Formulas {
			res := eval([]*query.Canonical{f}, c.qm, nil, query.ForRule)
			if res.Value == query.True && holds(r) {
				c.exact = i
				return 0 // held whatever the rest says: nothing more is read
			}
			if res.Value != query.False {
				applies = true
			}
			if res.Value == query.Unknown {
				needs |= res.Needs
			}
		}
		view = view || applies && readsView(r)
	}
	// A removal that may apply is decided on the message's text: read it
	// (ADR 0040, "The order").
	if view {
		needs |= query.FromEntry
		if c.entry != nil && entryCut(c.entry) {
			needs |= query.FromText
		}
	}
	return needs
}

// holds says r holds what it applies to; every rule but one that removes
// does (an effect this release does not know protects as a hold does).
func holds(r rules.Rule) bool { return r.Effect != rules.Remove }

// readsView says r, a rule that removes, removes from the subject or the
// text: whole, by shapes or by meaning.
func readsView(r rules.Rule) bool {
	rm := r.Removal
	return !holds(r) && (rm == nil || len(rm.Shapes) > 0 || len(rm.Topics) > 0 ||
		slices.Contains(rm.Parts, contracts.PartSubject) || slices.Contains(rm.Parts, contracts.PartBody))
}

// mayRemove says a rule that removes may apply to c on what is read of it:
// one of its formulas is not False.
func (p *discloseRun) mayRemove(c *candidate) bool {
	for _, r := range p.d.Rules {
		if holds(r) {
			continue
		}
		for _, f := range r.Formulas {
			if p.s.eval([]*query.Canonical{f}, c.qm, nil, query.ForRule).Value != query.False {
				return true
			}
		}
	}
	return false
}

// classify settles what the first pass knows of c once no more is read: it
// says whether the formulas could match it, and for each rule whether it
// holds c exactly or needs a decision, with the model's atoms it needs.
func (p *discloseRun) classify(c *candidate) bool {
	c.rules = make([]ruleNeed, len(p.d.Rules))
	c.removable = p.mayRemove(c)
	switch {
	case c.fs.Value == query.True:
	case c.fs.Value == query.False:
		// Kept only because a removal may apply: the formulas are read
		// again on what it leaves.
	case c.exact >= 0:
		// A rule holds it, read no further: the formulas could match it.
	case c.fs.Needs != 0 || len(c.fs.Model) == 0 || c.meta.Unparsed:
		// Unknown on data that cannot be read: it never leaves. An
		// unparsed message is never asked about (ADR 0028, "What is never
		// asked").
	default:
		if !p.couldBeTrue(c) && !c.removable {
			return false
		}
		c.fsModel = true
	}
	if c.exact >= 0 {
		return true
	}
	for i, r := range p.d.Rules {
		for _, f := range r.Formulas {
			res := p.s.eval([]*query.Canonical{f}, c.qm, nil, query.ForRule)
			if res.Value == query.True && !holds(r) {
				c.rules[i].applies = true
			}
			if res.Value != query.Unknown {
				continue
			}
			if res.Needs != 0 || len(res.Model) == 0 || c.meta.Unparsed || r.ModelOff {
				// Unknown on a text that cannot be read, or on a model the
				// rule may not ask (ADR 0040, "The rules' notice"): it
				// applies, without the model.
				if holds(r) {
					c.exact = i
					return true
				}
				c.rules[i].applies = true
				continue
			}
			c.rules[i].need = true
			for _, a := range res.Model {
				if !slices.Contains(c.rules[i].atoms, a) {
					c.rules[i].atoms = append(c.rules[i].atoms, a)
				}
			}
		}
		if c.rules[i].applies {
			c.rules[i].need, c.rules[i].atoms = false, nil
		}
	}
	return true
}

// couldBeTrue says some answers of 1 or 0 to the formulas' model atoms make
// them True on c (Matcher's verdictOf).
func (p *discloseRun) couldBeTrue(c *candidate) bool {
	atoms := c.fs.Model
	if len(atoms) > maxModelAtoms {
		return true
	}
	answers := make(query.Answers, len(atoms))
	for bits := range 1 << len(atoms) {
		for i, a := range atoms {
			answers[a] = float64(bits >> i & 1)
		}
		if p.s.eval(p.d.Formulas, c.qm, answers, query.ForQuery).Value == query.True {
			return true
		}
	}
	return false
}

// needsAsk says c may be asked about: the model may be asked, no rule holds
// it exactly, and a rule or the formulas need the model.
func (p *discloseRun) needsAsk(c *candidate) bool {
	if !p.model || c.exact >= 0 {
		return false
	}
	if c.fsModel {
		return true
	}
	if c.removable && slices.ContainsFunc(p.d.Rules, func(r rules.Rule) bool { return r.Removal != nil && len(r.Removal.Topics) > 0 }) {
		return true
	}
	return slices.ContainsFunc(c.rules, func(r ruleNeed) bool { return r.need })
}

// readEntry reads a message's search entry, counted in the page's reads:
// one that is missing, or does not open, is a cut text with nothing in it.
func (p *discloseRun) readEntry(ctx context.Context, m *store.Message) (*SearchEntry, error) {
	p.read++
	e, err := p.s.r.st.GetEntry(ctx, boxPartition(p.b.MailboxID), KeySearch+m.ID)
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

// readText reads c's raw message once and takes its text out: on a page
// under its own grant, given back once the text is out; for one message
// from the read budget, as Reader.Get's read. A raw message that cannot be
// read leaves rawGone.
func (p *discloseRun) readText(ctx context.Context, c *candidate) error {
	var raw []byte
	var err error
	if p.page {
		err = p.s.r.grantedRaw(ctx, p.b, c.m, func(r []byte) {
			t, _, cut := MessageText(ctx, r, MessageTextMax)
			c.raw = &RawText{Text: t, Size: int64(len(r))}
			c.rawCut(cut)
		})
	} else if raw, err = p.s.r.raw(ctx, p.b, c.m); err == nil {
		t, _, cut := MessageText(ctx, raw, MessageTextMax)
		c.raw = &RawText{Text: t, Size: int64(len(raw))}
		c.rawCut(cut)
	}
	switch {
	case errors.Is(err, contracts.ErrNotFound), errors.Is(err, errRawTooLarge):
		c.raw, c.rawGone = nil, true
		return nil
	case err != nil:
		return err
	}
	return ctx.Err() // the text may have been cut by the deadline
}

// rawCut keeps whether the text taken out of the raw message was cut at
// MessageTextMax, for the vendor's TextCut.
func (c *candidate) rawCut(cut bool) { c.textCut = cut }

// decide is the second pass over a page's candidates: the rules' decisions
// begun, the model asked, the decisions settled. It answers how many of the
// candidates the page keeps: a message the Asker could not afford ends the
// page before it (its decisions, and those of every one after it, are
// abandoned: nothing was asked for them).
func (p *discloseRun) decide(ctx context.Context, cands []*candidate) (int, error) {
	if p.model {
		p.begin(ctx, cands)
	}
	settle := make([]map[string]Settlement, len(p.d.Rules))
	for i := range settle {
		settle[i] = map[string]Settlement{}
	}
	// Whoever finds a Pending past its grace settles it Held (Recovered).
	now := p.s.now()
	for _, c := range cands {
		for i, r := range c.rules {
			if r.dec.State == DecisionPending && !r.failed && OverdueAt(r.dec.At, now) {
				settle[i][c.m.ID] = Settlement{State: DecisionHeld}
			}
		}
	}
	cut := len(cands)
	var fail error
	for i, c := range cands {
		atoms := p.atomsFor(c)
		if len(atoms) == 0 {
			continue
		}
		if i > 0 && p.s.now().Sub(p.start) >= p.s.bounds.soft {
			cut = i
			break
		}
		asked, err := p.ask(ctx, c, atoms)
		if err != nil {
			cut, fail = i, err
			break
		}
		if asked.Outcome == AskStop {
			if i > 0 {
				cut = i
				break
			}
			// The first message has every read of a page left: an Asker
			// that stops it anyway would stop every page there. Nothing was
			// asked for it: a rule's decision is abandoned and the message
			// held, undecided; the formulas stay Unknown.
			asked = &Asked{Outcome: AskAnswered}
		}
		p.answered(c, asked)
		for ri, r := range c.rules {
			if r.dec.State != DecisionBegun {
				continue
			}
			res := p.ruleValue(ri, c)
			s := SettleAfterAsk(res, r.atoms, asked, nil)
			if s.State == DecisionPending {
				continue // an answer received and not kept: held, recovered after the grace
			}
			s.Attempt = r.dec.Attempt
			settle[ri][c.m.ID] = s
		}
	}
	// What was begun and not asked for: positive evidence that the model
	// answered no part of it.
	for i, c := range cands {
		for ri, r := range c.rules {
			if r.dec.State != DecisionBegun {
				continue
			}
			if _, ok := settle[ri][c.m.ID]; ok {
				continue
			}
			if i >= cut || c.asked == nil {
				settle[ri][c.m.ID] = Settlement{State: DecisionAbandoned, Attempt: r.dec.Attempt}
			}
		}
	}
	p.settle(ctx, cands, settle)
	if fail != nil {
		return 0, fail
	}
	return cut, nil
}

// begin begins, rule by rule and day by day, the decisions the candidates
// need; a candidate a rule before already holds (or leaves undecided) begins
// nothing more.
func (p *discloseRun) begin(ctx context.Context, cands []*candidate) {
	of := byID(cands)
	for _, ri := range p.holdsFirst() {
		rule := p.d.Rules[ri]
		byDay := map[string][]DecisionKey{}
		var order []string
		for _, c := range cands {
			if c.exact >= 0 || !c.rules[ri].need || p.heldBefore(c, ri) {
				continue
			}
			day, _ := DecisionDay(c.m.ID) // its shape was checked (consider)
			if _, ok := byDay[day]; !ok {
				order = append(order, day)
			}
			byDay[day] = append(byDay[day], DecisionKey{ID: c.m.ID, ExpiresAt: lifetimeOf(c.m.ID)})
		}
		for _, day := range order {
			got, err := p.s.decisions.Begin(ctx, p.b, rule.Ref(), byDay[day])
			for _, k := range byDay[day] {
				c := of[k.ID]
				if err != nil {
					c.rules[ri].failed = true // every message of the day held, undecided
					continue
				}
				c.rules[ri].dec = got[k.ID]
				if got[k.ID].State == DecisionExpired {
					c.expired = true
				}
			}
		}
	}
}

// lifetimeOf is the latest a message can expire, from what is authenticated
// of it: its identifier (bound to its sealed meta) says when it arrived, and
// no mailbox keeps mail longer than the longest retention. A decision is
// kept for that lifetime, and no message is disclosed past it. The item's
// ExpiresAt is whoever writes the table's to set: it can end the message
// sooner (an item gone is not read), never a decision about it.
func lifetimeOf(id string) time.Time {
	return timeOfMessageID(id).AddDate(0, 0, slices.Max(contracts.MailboxRetentionDays))
}

// heldBefore says a rule that holds, before ri (every one, for a rule that
// removes), already holds c, or leaves it undecided.
func (p *discloseRun) heldBefore(c *candidate, ri int) bool {
	for j, r := range c.rules {
		if !holds(p.d.Rules[j]) {
			continue
		}
		if holds(p.d.Rules[ri]) && j >= ri {
			break
		}
		if r.need && (r.failed || r.dec.State != DecisionCleared && r.dec.State != DecisionBegun) {
			return true
		}
	}
	return c.expired
}

// holdsFirst is the rules' indices, those that hold before those that
// remove, each in order: what a message is held by is decided before
// anything is decided of what is removed from it.
func (p *discloseRun) holdsFirst() []int {
	var hs, rs []int
	for i, r := range p.d.Rules {
		if holds(r) {
			hs = append(hs, i)
		} else {
			rs = append(rs, i)
		}
	}
	return append(hs, rs...)
}

// byID is the candidates by their message.
func byID(cands []*candidate) map[string]*candidate {
	out := make(map[string]*candidate, len(cands))
	for _, c := range cands {
		out[c.m.ID] = c
	}
	return out
}

// atomsFor are the atoms c is asked about: those of the rules this request
// began for it, unless another rule already holds it, and the formulas' own
// when their value rests on the model and nothing holds it.
func (p *discloseRun) atomsFor(c *candidate) []query.Atom {
	if !p.model || c.exact >= 0 || c.expired {
		return nil
	}
	var atoms []query.Atom
	for ri, r := range c.rules {
		switch {
		case !r.need, r.dec.State == DecisionCleared:
		case (r.failed || r.dec.State != DecisionBegun) && holds(p.d.Rules[ri]):
			return nil // held, undecided or final, whatever is asked
		case r.failed || r.dec.State != DecisionBegun:
			// A removal that applies, or is undecided: it applies.
		default:
			for _, a := range r.atoms {
				if !slices.Contains(atoms, a) {
					atoms = append(atoms, a)
				}
			}
		}
	}
	if c.fsModel && !c.removable {
		for _, a := range c.fs.Model {
			if !slices.Contains(atoms, a) {
				atoms = append(atoms, a)
			}
		}
	}
	return atoms
}

// ask asks the request's Asker about c, under the message's own deadline
// or what is left of the request's (Matcher's ask). A message whose own
// deadline ran out was asked, and nothing came of it: no answer, no error.
func (p *discloseRun) ask(ctx context.Context, c *candidate, atoms []query.Atom) (*Asked, error) {
	account := p.d.Account
	if account == "" {
		account = p.asking.Account
	}
	q := &Question{
		Box: p.b, MessageID: c.m.ID, ExpiresAt: c.m.ExpiresAt, Account: account,
		Atoms: atoms, Subject: c.meta.Subject, View: ViewOriginal, Text: c.src.text,
	}
	actx, cancel := context.WithTimeout(ctx, p.s.bounds.ask)
	asked, err := p.asking.Asker.Ask(actx, q)
	ranOut := errors.Is(actx.Err(), context.DeadlineExceeded)
	c.src.close()
	cancel()
	if err != nil {
		if ranOut && ctx.Err() == nil && errors.Is(err, context.DeadlineExceeded) {
			return &Asked{Outcome: AskAnswered}, nil
		}
		return nil, err
	}
	if asked == nil {
		asked = &Asked{Outcome: AskAnswered}
	}
	return asked, nil
}

// answered keeps what Ask gave for c: its answers, but a held atom's.
func (p *discloseRun) answered(c *candidate, asked *Asked) {
	switch asked.Outcome {
	case AskOff:
		p.off = true
	case AskLimit:
		p.limit = true
	}
	c.asked = asked
	c.answers = make(query.Answers, len(asked.Answers))
	for a, pr := range asked.Answers {
		if !slices.Contains(asked.Held, a) {
			c.answers[a] = pr
		}
	}
}

// ruleValue is rule ri's value on c with its answers: one of its formulas
// True or Unknown applies it (ADR 0027, "Rules: any of their criteria"),
// each read for a rule; Model are the atoms its Unknown formulas still
// need.
func (p *discloseRun) ruleValue(ri int, c *candidate) query.Result {
	out := query.Result{Value: query.False}
	for _, f := range p.d.Rules[ri].Formulas {
		res := p.s.eval([]*query.Canonical{f}, c.qm, c.answers, query.ForRule)
		out.Value = max(out.Value, res.Value)
		if res.Value != query.Unknown {
			continue
		}
		out.Needs |= res.Needs
		for _, a := range res.Model {
			if !slices.Contains(out.Model, a) {
				out.Model = append(out.Model, a)
			}
		}
	}
	if out.Value != query.Unknown {
		out.Needs, out.Model = 0, nil
	}
	return out
}

// settle commits the page's settlements, rule by rule and day by day, even
// once the request's context has ended (settleWithin), and keeps each
// answer in its candidate. A day that cannot be written leaves its
// decisions Pending: held, undecided, recovered after the grace.
func (p *discloseRun) settle(ctx context.Context, cands []*candidate, settle []map[string]Settlement) {
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), p.s.settle)
	defer cancel()
	of := byID(cands)
	for ri, rule := range p.d.Rules {
		byDay := map[string]map[string]Settlement{}
		var order []string
		for id, s := range settle[ri] {
			day, _ := DecisionDay(id) // a candidate's: its shape was checked
			if byDay[day] == nil {
				byDay[day] = map[string]Settlement{}
				order = append(order, day)
			}
			byDay[day][id] = s
		}
		slices.Sort(order)
		for _, day := range order {
			got, err := p.s.decisions.Settle(sctx, p.b, rule.Ref(), byDay[day])
			for id := range byDay[day] {
				c := of[id]
				if err != nil {
					c.rules[ri].dec = Decision{State: DecisionPending}
					continue
				}
				c.rules[ri].dec = got[id]
			}
		}
	}
}

// outcome is c as the request answers it: left, held, or neither (false:
// the formulas did not match once asked, or its message expired).
func (p *discloseRun) outcome(c *candidate) (Outcome, bool) {
	if c.expired {
		return Outcome{}, false
	}
	fs := c.fs.Value
	if c.fsModel && c.asked != nil && !c.removable {
		fs = p.s.eval(p.d.Formulas, c.qm, c.answers, query.ForQuery).Value
	}
	if c.removable {
		// The formulas are read on what the removals leave (reduce); a
		// message reduce did not reach is listed only when a rule holds it.
		switch {
		case c.reduced != nil:
			fs = c.reduced.fs.Value
		case fs != query.False:
			fs = query.Unknown
		}
	}
	if fs == query.False {
		return Outcome{}, false
	}
	var cleared []RuleCleared
	for ri, r := range p.d.Rules {
		if c.exact == ri {
			return Outcome{Withheld: &Withheld{ID: c.m.ID, Rule: r.Ref(), Why: HeldExact}}, true
		}
		need := c.rules[ri]
		if c.exact >= 0 || !need.need {
			continue
		}
		if !holds(r) {
			if need.dec.State == DecisionCleared && !need.failed && p.model {
				cleared = append(cleared, RuleCleared{Rule: r.Ref(), At: need.dec.At, Decided: need.dec.Evidence})
			}
			continue
		}
		switch {
		case !p.model || need.failed:
			return p.undecided(c, ri), true
		case need.dec.State == DecisionCleared:
			cleared = append(cleared, RuleCleared{Rule: r.Ref(), At: need.dec.At, Decided: need.dec.Evidence})
		case need.dec.State == DecisionHeld && need.dec.Recovered:
			return Outcome{Withheld: &Withheld{ID: c.m.ID, Rule: r.Ref(), Why: HeldRecovered}}, true
		case need.dec.State == DecisionHeld:
			return Outcome{Withheld: &Withheld{ID: c.m.ID, Rule: r.Ref(), Why: HeldDecided, Decided: need.dec.Evidence}}, true
		default:
			return p.undecided(c, ri), true
		}
	}
	if fs != query.True {
		return Outcome{}, false
	}
	out := &Disclosed{Message: p.vendorMessage(c), ID: c.m.ID, Decided: p.formulasDecided(c), Rules: cleared}
	if c.reduced != nil {
		out.Removed = c.reduced.removed
	}
	return Outcome{Disclosed: out}, true
}

// undecided is c held, undecided, by rule ri; one held for want of room in
// its day is counted (DecisionsFullCounter).
func (p *discloseRun) undecided(c *candidate, ri int) Outcome {
	if slices.ContainsFunc(c.rules, func(r ruleNeed) bool { return r.need && r.dec.State == DecisionFull }) {
		p.s.full.Add(1)
	}
	return Outcome{Withheld: &Withheld{ID: c.m.ID, Rule: p.d.Rules[ri].Ref(), Why: HeldUndecided}}
}

// formulasDecided are the model's answers the formulas' value rested on.
func (p *discloseRun) formulasDecided(c *candidate) []Decided {
	model, asked := c.fs.Model, c.asked
	if c.reduced != nil {
		model, asked = c.reduced.model, c.reduced.asked
	} else if !c.fsModel {
		return nil
	}
	if asked == nil {
		return nil
	}
	var out []Decided
	for _, a := range model {
		if e, ok := asked.Evidence[a]; ok {
			out = append(out, Decided{Atom: a, Evidence: e})
		}
	}
	return out
}

// vendorMessage is c with the parts the request reads and no other: who
// sent it and when it arrived always (the time in its identifier, bound to
// the sealed meta, never the item's plain field), its subject and its
// attachments when they are parts of it. Its text is a read's (Message).
// Its identifier is the vendor's, <mailbox>.<message> (ADR 0030, "The API
// for apps").
func (p *discloseRun) vendorMessage(c *candidate) contracts.VendorMessage {
	vm := contracts.VendorMessage{ID: p.b.MailboxID + "." + c.m.ID, ReceivedAt: timeOfMessageID(c.m.ID), From: orEmpty(c.meta.From)}
	r := c.reduced
	if r != nil && r.sender {
		vm.From = []contracts.MailAddress{}
	}
	if slices.Contains(p.d.Parts, contracts.PartSubject) {
		s := c.meta.Subject
		if r != nil {
			s = r.subject
		}
		vm.Subject = &s
	}
	if slices.Contains(p.d.Parts, contracts.PartAttachments) && r != nil && r.attachments {
		vm.Attachments = []contracts.VendorAttachment{}
	} else if slices.Contains(p.d.Parts, contracts.PartAttachments) {
		vm.Attachments = []contracts.VendorAttachment{}
		for _, a := range c.meta.Attachments {
			vm.Attachments = append(vm.Attachments, contracts.VendorAttachment{Index: a.Index, Name: a.Name, Type: a.Type, Size: int(a.Size)}) //nolint:gosec // G115: a stored message is at most 25 MiB
		}
	}
	return vm
}

// vendorText is a read's text, as a connection receives it, at the least
// cost: the search entry's when it was not cut, otherwise the raw message's
// (read once for the formulas, the rules or the model, or now), at most
// MessageTextMax; cut says it was cut there.
func (p *discloseRun) vendorText(ctx context.Context, c *candidate) (string, bool, error) {
	if c.meta.Unparsed {
		return "", false, nil
	}
	if c.raw != nil {
		return c.raw.Text, c.textCut, nil
	}
	if c.entry == nil {
		e, err := p.readEntry(ctx, c.m)
		if err != nil {
			return "", false, err
		}
		c.entry = e
	}
	if !entryCut(c.entry) {
		return c.entry.Text, false, nil
	}
	if err := p.readText(ctx, c); err != nil {
		return "", false, err
	}
	if c.raw == nil {
		return "", false, contracts.ErrNotFound
	}
	return c.raw.Text, c.textCut, nil
}

// discloseText is Question.Text for one candidate (Matcher's textSource):
// the text already read, the entry's when it was not cut, otherwise the raw
// message's under the request's text reads; read once, and nothing once Ask
// returned.
type discloseText struct {
	p *discloseRun
	c *candidate

	mu     sync.Mutex
	done   bool
	closed bool
	got    string
	st     TextState
	err    error
}

func (s *discloseText) close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
}

func (s *discloseText) text(ctx context.Context) (string, TextState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return "", TextUnaffordable, nil
	}
	if !s.done {
		s.got, s.st, s.err = s.read(ctx)
		s.done = true
	}
	return s.got, s.st, s.err
}

func (s *discloseText) read(ctx context.Context) (string, TextState, error) {
	p, c := s.p, s.c
	if c.raw != nil {
		return c.raw.Text, TextRead, nil
	}
	if c.entry == nil {
		if p.page && p.read >= p.s.bounds.read {
			return "", TextUnaffordable, nil
		}
		e, err := p.readEntry(ctx, c.m)
		if err != nil {
			return "", 0, err
		}
		c.entry = e
	}
	if !entryCut(c.entry) {
		return c.entry.Text, TextRead, nil
	}
	if p.page && p.textReads >= p.s.bounds.textReads {
		return "", TextUnaffordable, nil
	}
	p.textReads++
	if err := p.readText(ctx, c); err != nil {
		return "", 0, err
	}
	if c.raw == nil {
		return "", TextUnavailable, nil
	}
	return c.raw.Text, TextRead, nil
}
