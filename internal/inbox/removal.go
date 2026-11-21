package inbox

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"slices"
	"strconv"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/query"
	"github.com/permaily-org/permaily/internal/redact"
	"github.com/permaily-org/permaily/internal/rules"
)

// Removing details in the Discloser (ADR 0040, "The order", "Removing by
// meaning"; its pull request 5). Once the rules that hold are decided
// (decide), every rule that removes and applies to a message not held is
// applied on the message's one View: whole parts, fixed shapes and meaning,
// each a set of spans of that View, joined by redact.Union; the formulas are
// then read on what is left (a condition on a part removed whole Unknown,
// their model atoms asked of the reduced text under a view of its own), and
// the parts are cut from it last (vendorMessage, leavingText).

// reduction is what the rules that remove left of one message, and the
// formulas' value on it.
type reduction struct {
	subject string
	// text is the View's text as it leaves; textOK says the text was read,
	// so text is what a read gives (otherwise a read gives nothing).
	text   string
	textOK bool
	// sender and attachments: a rule removed them whole.
	sender, attachments bool
	hide                hidden
	removed             []RemovedBy
	// view names the reduced text for the model's kept answers.
	view TextView
	qm   *query.Message
	fs   query.Result
	// model are the formulas' atoms asked of the reduced text, and asked
	// what the Asker answered for them.
	model []query.Atom
	asked *Asked
}

// hidden is the parts a rule removed whole: a condition of the formulas on
// one of them is Unknown (ADR 0040, "The order", 4), so a vendor's search
// cannot tell what was there.
type hidden struct{ subject, body, sender, attachments bool }

// hides says a condition on variable v, with a model's operator when model,
// reads a part hidden: the subject, the text (a model's message atom reads
// both), the sender (its address, its domain and the signatures of its
// domain) or the attachments.
func (h hidden) hides(v contracts.Variable, model bool) bool {
	switch v {
	case contracts.VarSubject:
		return h.subject
	case contracts.VarBody:
		return h.body
	case contracts.VarMessage:
		return model && (h.subject || h.body)
	case contracts.VarFromAddress, contracts.VarFromDomain, contracts.VarSignedBy:
		return h.sender
	case contracts.VarAttachments, contracts.VarAttachmentName, contracts.VarAttachmentType:
		return h.attachments
	}
	return false
}

// evalHiding is eval with every condition on a part h hides Unknown, never
// read and never asked: each group of each formula is evaluated without
// them, and a group that held one is Unknown where it would be False.
func evalHiding(eval Evaluate, h hidden) Evaluate {
	if h == (hidden{}) {
		return eval
	}
	return func(fs []*query.Canonical, m *query.Message, answers query.Answers, mode query.Mode) query.Result {
		if len(fs) == 0 || slices.Contains(fs, nil) {
			return query.Result{Value: query.Unknown}
		}
		value := query.True
		var needs query.Sources
		var model []query.Atom
		for _, f := range fs {
			if f.Never {
				value = query.False
				continue
			}
			for _, g := range f.Formula.All {
				var keep []contracts.Condition
				hid := false
				for _, c := range g.Any {
					if h.hides(c.Var, c.Op == contracts.OpAbout || c.Op == contracts.OpMentions) {
						hid = true
						continue
					}
					keep = append(keep, c)
				}
				gv := query.False
				var res query.Result
				if len(keep) > 0 {
					one := &query.Canonical{Formula: contracts.Formula{V: f.Formula.V, All: []contracts.Group{{Any: keep}}}}
					res = eval([]*query.Canonical{one}, m, answers, mode)
					gv = res.Value
				}
				if hid && gv == query.False {
					gv = query.Unknown
				}
				if res.Value == query.Unknown && gv == query.Unknown {
					needs |= res.Needs
					for _, a := range res.Model {
						if !slices.Contains(model, a) {
							model = append(model, a)
						}
					}
				}
				value = min(value, gv)
			}
		}
		if value != query.Unknown {
			return query.Result{Value: value}
		}
		return query.Result{Value: value, Needs: needs, Model: model}
	}
}

// heldBy says a rule that holds holds c (exactly, decided, or undecided):
// nothing of it is removed, since nothing of it leaves.
func (p *discloseRun) heldBy(c *candidate) bool {
	if c.exact >= 0 {
		return true
	}
	for ri, r := range p.d.Rules {
		n := c.rules[ri]
		if holds(r) && n.need && (!p.model || n.failed || n.dec.State != DecisionCleared) {
			return true
		}
	}
	return false
}

// applying is the rules that remove and apply to c, in order: without the
// model, or on a decision that is not Cleared (Held, recovered, undecided,
// failed: a rule whose value is Unknown applies, ADR 0040, "The order", 2).
func (p *discloseRun) applying(c *candidate) []int {
	var out []int
	for ri, r := range p.d.Rules {
		n := c.rules[ri]
		if !holds(r) && (n.applies || n.need && (!p.model || n.failed || n.dec.State != DecisionCleared)) {
			out = append(out, ri)
		}
	}
	return out
}

// reduce applies the removals to the candidates a rule that removes may
// apply to and no rule holds, and reads the formulas on what each leaves.
// It answers how many of cands the page keeps: as decide, a message the
// Asker could not afford ends the page before it.
func (p *discloseRun) reduce(ctx context.Context, cands []*candidate) (int, error) {
	var work []*candidate
	at := map[*candidate]int{}
	for i, c := range cands {
		if c.removable && !c.expired && !p.heldBy(c) {
			work = append(work, c)
			at[c] = i
		}
	}
	if len(work) == 0 {
		return len(cands), nil
	}
	for _, c := range work {
		if slices.ContainsFunc(p.applying(c), func(ri int) bool { return readsView(p.d.Rules[ri]) }) {
			p.makeView(c)
		}
	}
	p.beginMeaning(ctx, work)
	cut, fail := len(cands), error(nil)
	settle := make([]map[string]Settlement, len(p.d.Rules))
	for i := range settle {
		settle[i] = map[string]Settlement{}
	}
	now := p.s.now()
	for _, c := range work {
		for ri, r := range c.rules {
			if r.meaning.State == DecisionPending && !r.meaningFailed && OverdueAt(r.meaning.At, now) {
				settle[ri][c.m.ID] = Settlement{State: DecisionHeld}
			}
		}
	}
asking:
	for _, c := range work {
		for ri := range c.rules {
			if c.rules[ri].meaning.State != DecisionBegun {
				continue
			}
			if at[c] > 0 && p.s.now().Sub(p.start) >= p.s.bounds.soft {
				cut = at[c]
				break asking
			}
			asked, err := p.askSentences(ctx, c, ri)
			if err != nil {
				cut, fail = at[c], err
				break asking
			}
			c.rules[ri].meaningAsked = asked
			s := SettleMeaning(*c.view, c.smap, p.d.Rules[ri].Removal.Topics, asked, nil)
			s.Attempt = c.rules[ri].meaning.Attempt
			settle[ri][c.m.ID] = s
		}
	}
	// What was begun and not asked for: the model answered no part of it.
	for _, c := range work {
		for ri, r := range c.rules {
			if _, ok := settle[ri][c.m.ID]; r.meaning.State == DecisionBegun && !ok {
				settle[ri][c.m.ID] = Settlement{State: DecisionAbandoned, Attempt: r.meaning.Attempt}
			}
		}
	}
	p.settleMeaning(ctx, work, settle)
	if fail != nil {
		return 0, fail
	}
	for _, c := range work {
		if at[c] >= cut {
			break
		}
		red := p.reduceOne(c)
		if red.fs.Value != query.Unknown || red.fs.Needs != 0 || len(red.fs.Model) == 0 || !p.model || c.meta.Unparsed {
			continue
		}
		if at[c] > 0 && p.s.now().Sub(p.start) >= p.s.bounds.soft {
			c.reduced, cut = nil, at[c]
			break
		}
		asked, err := p.askReduced(ctx, c, red)
		if err != nil {
			return 0, err
		}
		if asked.Outcome == AskStop {
			if at[c] > 0 {
				c.reduced, cut = nil, at[c]
				break
			}
			asked = &Asked{Outcome: AskAnswered} // as decide does for the first
		}
		p.answeredReduced(red, asked)
	}
	return cut, nil
}

// makeView makes c's one View from its subject and the text a connection
// receives, and splits it once (redact.Redactor.Sentences). A text that
// cannot be read gives a View with no text, which no meaning decision is
// ever begun on (it would bind the decision to a view a later read does not
// have): its removals by meaning remove the whole subject and text.
func (p *discloseRun) makeView(c *candidate) {
	text, ok := "", true
	switch {
	case c.meta.Unparsed:
	case c.raw != nil:
		text = c.raw.Text
	case c.entry != nil && !entryCut(c.entry):
		text = c.entry.Text
	default:
		ok = false
	}
	v := redact.NewView(c.meta.Subject, text)
	c.view, c.viewOK = &v, ok
	c.smap = p.s.redactor.Sentences(v)
	// A map that breaks its invariants is refused before any decision is
	// asked or applied (ADR 0040's re-review, N1).
	c.smapOK = c.smap.Check(v) == nil
}

// asksMeaning says rule ri's meaning is asked of the model for c: its
// removal has topics, the model may be asked for it, and c's View was read
// and split as the invariants say.
func (p *discloseRun) asksMeaning(c *candidate, ri int) bool {
	r := p.d.Rules[ri]
	return r.Removal != nil && len(r.Removal.Topics) > 0 && !r.ModelOff && p.model && p.sentences != nil &&
		c.view != nil && c.viewOK && c.smapOK
}

// beginMeaning begins, rule by rule and day by day, the meaning decisions
// (rules.MeaningRef) of the rules that remove by meaning and apply.
func (p *discloseRun) beginMeaning(ctx context.Context, work []*candidate) {
	of := byID(work)
	for ri, rule := range p.d.Rules {
		byDay := map[string][]DecisionKey{}
		var order []string
		for _, c := range work {
			if !slices.Contains(p.applying(c), ri) || !p.asksMeaning(c, ri) {
				continue
			}
			day, _ := DecisionDay(c.m.ID)
			if _, ok := byDay[day]; !ok {
				order = append(order, day)
			}
			byDay[day] = append(byDay[day], DecisionKey{ID: c.m.ID, ExpiresAt: lifetimeOf(c.m.ID)})
		}
		ref := rules.MeaningRef(rule.Ref(), TextVersion, redact.SplitVersion)
		for _, day := range order {
			got, err := p.s.decisions.Begin(ctx, p.b, ref, byDay[day])
			for _, k := range byDay[day] {
				c := of[k.ID]
				if err != nil {
					c.rules[ri].meaningFailed = true
					continue
				}
				c.rules[ri].meaning = got[k.ID]
				if got[k.ID].State == DecisionExpired {
					c.expired = true
				}
			}
		}
	}
}

// askSentences asks the request's SentenceAsker about rule ri's topics in
// c's sentences, every fixed shape masked, under the message's own deadline
// or what is left of the request's. A message whose own deadline ran out
// was asked, and nothing came of it.
func (p *discloseRun) askSentences(ctx context.Context, c *candidate, ri int) (*SentencesAsked, error) {
	if len(c.smap.Sentences) == 0 {
		return &SentencesAsked{}, nil
	}
	account := p.d.Account
	if account == "" {
		account = p.asking.Account
	}
	q := &SentenceQuestion{Account: account, Sentences: MaskedSentences(p.s.redactor, *c.view, c.smap), Topics: p.d.Rules[ri].Removal.Topics}
	actx, cancel := context.WithTimeout(ctx, p.s.bounds.ask)
	asked, err := p.sentences.AskSentences(actx, q)
	ranOut := errors.Is(actx.Err(), context.DeadlineExceeded)
	cancel()
	if err != nil {
		if ranOut && ctx.Err() == nil && errors.Is(err, context.DeadlineExceeded) {
			return &SentencesAsked{}, nil
		}
		return nil, err
	}
	if asked == nil {
		asked = &SentencesAsked{}
	}
	switch asked.Outcome {
	case AskOff:
		p.off = true
	case AskLimit:
		p.limit = true
	}
	return asked, nil
}

// settleMeaning commits the meaning decisions' settlements, as settle does
// the rules': even once the request's context has ended, and a day that
// cannot be written leaves its decisions Pending (the whole subject and text
// removed for this request, recovered Held after the grace).
func (p *discloseRun) settleMeaning(ctx context.Context, work []*candidate, settle []map[string]Settlement) {
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), p.s.settle)
	defer cancel()
	of := byID(work)
	for ri, rule := range p.d.Rules {
		byDay := map[string]map[string]Settlement{}
		var order []string
		for id, s := range settle[ri] {
			day, _ := DecisionDay(id)
			if byDay[day] == nil {
				byDay[day] = map[string]Settlement{}
				order = append(order, day)
			}
			byDay[day][id] = s
		}
		slices.Sort(order)
		ref := rules.MeaningRef(rule.Ref(), TextVersion, redact.SplitVersion)
		for _, day := range order {
			got, err := p.s.decisions.Settle(sctx, p.b, ref, byDay[day])
			for id := range byDay[day] {
				if err != nil {
					of[id].rules[ri].meaning = Decision{State: DecisionPending}
					continue
				}
				of[id].rules[ri].meaning = got[id]
			}
		}
	}
}

// reduceOne applies every rule that removes and applies to c, joins their
// spans on its View, and reads the formulas on what is left.
func (p *discloseRun) reduceOne(c *candidate) *reduction {
	red := &reduction{subject: c.meta.Subject, view: ViewOriginal}
	var spans [][]redact.Span
	h := sha256.New()
	h.Write([]byte("permaily reduced view v1\x00"))
	put := func(x int) { h.Write(binary.AppendVarint(nil, int64(x))) }
	put(redact.Version)
	put(redact.SplitVersion)
	reduced := false
	for _, ri := range p.applying(c) {
		r, n := p.d.Rules[ri], c.rules[ri]
		rm := r.Removal
		if rm == nil {
			// A rule that removes and says nothing of what: everything.
			rm = &rules.Removal{Parts: []contracts.Part{contracts.PartSender, contracts.PartSubject, contracts.PartBody, contracts.PartAttachments}}
		}
		by := RemovedBy{Rule: r.Ref()}
		if n.need && n.dec.State == DecisionHeld {
			by.Decided = append(by.Decided, n.dec.Evidence...)
		}
		sum := &by.Removed
		var mine []redact.Span
		for _, part := range rm.Parts {
			switch part {
			case contracts.PartSubject:
				red.hide.subject = true
				if c.view != nil {
					mine = append(mine, c.view.Subject)
				}
			case contracts.PartBody:
				red.hide.body = true
				if c.view != nil {
					mine = append(mine, c.view.Body)
				}
			}
			if !slices.Contains(sum.Parts, part) && part != contracts.PartAttachments {
				sum.Parts = append(sum.Parts, part)
			}
		}
		if rm.Sender() {
			red.sender, red.hide.sender = true, true
			if !slices.Contains(sum.Parts, contracts.PartSender) {
				sum.Parts = append(sum.Parts, contracts.PartSender)
			}
		}
		if rm.Attachments() {
			red.attachments, red.hide.attachments = true, true
			sum.Attachments = len(c.meta.Attachments)
			if slices.Contains(rm.Parts, contracts.PartAttachments) {
				sum.Parts = append(sum.Parts, contracts.PartAttachments)
			}
		}
		if len(rm.Shapes) > 0 && c.view != nil {
			for _, pc := range p.s.redactor.Shapes(c.view.Text, rm.Shapes) {
				mine = append(mine, pc.Span)
				count(sum, pc.Kind)
			}
		}
		if len(rm.Topics) > 0 {
			sp, kept := p.meaningOf(c, ri, &by)
			mine = append(mine, sp...)
			h.Write([]byte(r.ID + "\x00" + strconv.Itoa(r.Version) + "\x00"))
			if kept != nil {
				h.Write([]byte(n.meaning.Meaning.Map + "\x00" + n.meaning.Meaning.Encode() + "\x00"))
			} else {
				red.hide.subject, red.hide.body = true, true
			}
			reduced = true
		}
		spans = append(spans, mine)
		if !zeroSummary(by.Removed) {
			red.removed = append(red.removed, by)
		}
	}
	qm := *c.qm
	u := redact.Union(spans...)
	if c.view != nil {
		red.subject, red.text = redact.Join(*c.view, u)
		red.textOK = c.viewOK
		red.hide.subject = red.hide.subject || covers(u, c.view.Subject)
		red.hide.body = red.hide.body || covers(u, c.view.Body)
		qm.Subject = red.subject
		if red.textOK {
			qm.Have |= query.FromText
			qm.Body, qm.BodyCut = red.text, false
		}
	}
	for _, s := range u {
		put(s.Start)
		put(s.End)
	}
	for _, b := range []bool{red.hide.subject, red.hide.body, red.hide.sender, red.hide.attachments} {
		if b {
			put(1)
		} else {
			put(0)
		}
	}
	if reduced || len(u) > 0 || red.hide != (hidden{}) {
		red.view = TextView{Name: "r-" + hex.EncodeToString(h.Sum(nil))[:16], Version: TextVersion}
	}
	red.qm = &qm
	red.fs = evalHiding(p.s.eval, red.hide)(p.d.Formulas, red.qm, nil, query.ForQuery)
	c.reduced = red
	return red
}

// meaningOf is what rule ri removes from c by meaning, as spans of its
// View (MeaningSpans), and the sentences kept (nil when the whole subject
// and text are removed instead), with its summary and evidence in by.
func (p *discloseRun) meaningOf(c *candidate, ri int, by *RemovedBy) ([]redact.Span, []bool) {
	n := c.rules[ri]
	sum := &by.Removed
	whole := func(why HeldWhy) ([]redact.Span, []bool) {
		sum.Whole, sum.Why = true, string(why)
		if c.view == nil {
			return nil, nil
		}
		return []redact.Span{c.view.Subject, c.view.Body}, nil
	}
	if c.view == nil || !c.viewOK || !c.smapOK || !p.asksMeaning(c, ri) || n.meaningFailed {
		return whole(HeldUndecided)
	}
	d := n.meaning
	switch {
	case d.State == DecisionHeld && d.Recovered:
		return whole(HeldRecovered)
	case d.State == DecisionHeld:
		return whole(HeldDecided)
	case d.State != DecisionCleared:
		return whole(HeldUndecided)
	}
	kept, ok := d.Meaning.For(*c.view, c.smap)
	if !ok {
		return whole(HeldUndecided)
	}
	by.Decided = append(by.Decided, d.Evidence...)
	removed, of := 0, len(c.smap.Sentences)
	for _, k := range kept {
		if !k {
			removed++
		}
	}
	if c.smap.Cut < len(c.view.Text) {
		removed, of = removed+1, of+1
	}
	sum.Sentences, sum.Of = removed, of
	// Every piece masked from the model is removed too (N2): counted with
	// the shapes.
	for _, pc := range p.s.redactor.Shapes(c.view.Text, contracts.ShapeKinds) {
		count(sum, pc.Kind)
	}
	return MeaningSpans(p.s.redactor, *c.view, c.smap, kept), kept
}

// count adds one piece of kind to s.
func count(s *contracts.RemovedSummary, kind contracts.ShapeKind) {
	if s.Shapes == nil {
		s.Shapes = map[contracts.ShapeKind]int{}
	}
	s.Shapes[kind]++
}

// zeroSummary says a rule removed nothing.
func zeroSummary(s contracts.RemovedSummary) bool {
	return len(s.Parts) == 0 && len(s.Shapes) == 0 && s.Sentences == 0 && !s.Whole && s.Attachments == 0
}

// covers says the spans u, joined, cover part entirely, and part is not
// empty: a part removed whole whatever removed it.
func covers(u []redact.Span, part redact.Span) bool {
	if part.End <= part.Start {
		return false
	}
	for _, s := range u {
		if s.Start <= part.Start && s.End >= part.End {
			return true
		}
	}
	return false
}

// askReduced asks the request's Asker the formulas' model atoms about what
// the removals left of c, under the reduced view's own name, so an answer
// kept for the original is never used for it nor the reverse (ADR 0040,
// "The order", 4). The text it gives is exactly the original's less what
// was removed: what the View's spans left; or, when no rule that applies
// read the View (none removes the subject, the text or a span of them; only
// the sender or the attachments), the text as it came, read as late as the
// original's would be. A View whose text could not be read gives none.
func (p *discloseRun) askReduced(ctx context.Context, c *candidate, red *reduction) (*Asked, error) {
	account := p.d.Account
	if account == "" {
		account = p.asking.Account
	}
	src := &discloseText{p: p, c: c}
	text := func(ctx context.Context) (string, TextState, error) {
		switch {
		case red.textOK:
			return red.text, TextRead, nil
		case red.view == ViewOriginal, c.view == nil && !red.hide.subject && !red.hide.body:
			return src.text(ctx)
		}
		return "", TextUnavailable, nil
	}
	q := &Question{
		Box: p.b, MessageID: c.m.ID, ExpiresAt: c.m.ExpiresAt, Account: account,
		Atoms: red.fs.Model, Subject: red.subject, View: red.view, Text: text,
	}
	actx, cancel := context.WithTimeout(ctx, p.s.bounds.ask)
	asked, err := p.asking.Asker.Ask(actx, q)
	ranOut := errors.Is(actx.Err(), context.DeadlineExceeded)
	src.close()
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

// answeredReduced keeps what Ask gave for the reduced message, and reads
// the formulas on it again.
func (p *discloseRun) answeredReduced(red *reduction, asked *Asked) {
	switch asked.Outcome {
	case AskOff:
		p.off = true
	case AskLimit:
		p.limit = true
	}
	answers := make(query.Answers, len(asked.Answers))
	for a, pr := range asked.Answers {
		if !slices.Contains(asked.Held, a) {
			answers[a] = pr
		}
	}
	red.model, red.asked = red.fs.Model, asked
	red.fs = evalHiding(p.s.eval, red.hide)(p.d.Formulas, red.qm, answers, query.ForQuery)
}

// leavingText is a read's text: what the removals left of it, or the text
// as it came when no rule removed from it. A text a removal was to read and
// could not is never given.
func (p *discloseRun) leavingText(ctx context.Context, c *candidate) (string, bool, error) {
	if r := c.reduced; r != nil && c.view != nil {
		if !r.textOK {
			return "", false, contracts.ErrNotFound
		}
		return r.text, c.textCut, nil
	}
	return p.vendorText(ctx, c)
}
