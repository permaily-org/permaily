package inbox_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/inbox/disclosetest"
	"github.com/permaily-org/permaily/internal/inbox/inboxtest"
	"github.com/permaily-org/permaily/internal/query"
	"github.com/permaily-org/permaily/internal/redact"
	"github.com/permaily-org/permaily/internal/rules"
)

// ADR 0040's pull request 5: the discloser's removals on the one view (U14
// to U16, U18, U20 to U22, and the re-review's N1 and N2), over the real
// query.Eval, redact.Fixed and BlockDecisions.



var topicHome = query.Atom{Var: contracts.VarMessage, Op: contracts.OpMentions, Topic: "a home address"}

// removing is a rule that removes rm from every message (or those fs
// apply to).
func removing(id string, rm *rules.Removal, fs ...*query.Canonical) rules.Rule {
	if len(fs) == 0 {
		fs = []*query.Canonical{every}
	}
	return rules.Rule{ID: id, Version: 1, Effect: rules.Remove, Formulas: fs, Removal: rm}
}

// holding is a rule of the person's that holds what fs apply to.
func holding(id string, fs ...*query.Canonical) rules.Rule {
	return rules.Rule{ID: id, Version: 1, Effect: rules.Hold, Formulas: fs}
}

func shapes(kinds ...contracts.ShapeKind) *rules.Removal { return &rules.Removal{Shapes: kinds} }

func topics(ts ...query.Atom) *rules.Removal { return &rules.Removal{Topics: ts} }

func parts(ps ...contracts.Part) *rules.Removal { return &rules.Removal{Parts: ps} }

// with is a disclosure of parts and fs applying rs (and no default rule).
func with(parts []contracts.Part, rs []rules.Rule, fs ...*query.Canonical) inbox.Disclosure {
	d := disclosure(parts, fs...)
	d.Rules = rs
	return d
}

// onSentences is a request's context with an Asker and a SentenceAsker.
func onSentences(a inbox.Asker, s inbox.SentenceAsker) context.Context {
	return inbox.WithAsker(context.Background(), inbox.Asking{Asker: a, Account: "acc_me", Sentences: s})
}

// read is one message read through disc, which must leave.
func (w *dWorld) read(ctx context.Context, disc inbox.Discloser, d inbox.Disclosure, id string) *inbox.Disclosed {
	w.t.Helper()
	o, err := disc.Message(ctx, w.b, d, id)
	if err != nil {
		w.t.Fatalf("read %s: %v", id, err)
	}
	if o.Disclosed == nil {
		w.t.Fatalf("read %s: held %+v", id, o.Withheld)
	}
	return o.Disclosed
}

func subjectOf(d *inbox.Disclosed) string {
	if d.Message.Subject == nil {
		return "<none>"
	}
	return *d.Message.Subject
}

func textOf(d *inbox.Disclosed) string {
	if d.Message.Text == nil {
		return "<none>"
	}
	return *d.Message.Text
}

// seedRaw stores a raw message whose search entry says its text was cut, so
// the discloser takes the text out of the raw message itself (base64,
// quoted-printable, HTML).
func (w *dWorld) seedRaw(n int, subject, raw string) string {
	w.t.Helper()
	q := &qMsg{at: qNow.Add(-time.Hour), n: n, from: []string{"no-reply@example.com"}, subject: subject}
	m := inboxtest.Message{ID: q.id(), ReceivedAt: q.at, RetentionDays: 30, Raw: []byte(raw), Meta: q.meta(),
		Search: &inbox.SearchEntry{Subject: subject, ReceivedAt: q.at, Cut: true}}
	if err := inboxtest.Seed(context.Background(), w.st.Memory, w.st.Memory, w.b, m); err != nil {
		w.t.Fatal(err)
	}
	return q.id()
}

func TestU14AHoldWinsOverARemoval(t *testing.T) {
	w := newDWorld(t)
	id := w.seed(qNow.Add(-time.Hour), 0, "Secret plans", "Call 555 123 4567.")
	other := w.seed(qNow.Add(-2*time.Hour), 0, "Plans", "Call 555 123 4567.")
	d := with(allParts, []rules.Rule{
		removing("rul_phone", shapes(contracts.ShapePhoneNumber)),
		holding("rul_secret", formula(group(cond(contracts.VarSubject, contracts.OpContains, "secret")))),
	})
	p := w.page(on(w.askA), w.discA, d, "", 10)
	left, held := sortOut(t, p.Items)
	if held[id] == nil || held[id].Rule.ID != "rul_secret" || slices.Contains(left, id) {
		t.Fatalf("the hold did not win: left %v, held %+v", left, held)
	}
	if !slices.Contains(left, other) {
		t.Fatalf("the other message: left %v", left)
	}
	for _, o := range p.Items {
		if o.Disclosed != nil && len(o.Disclosed.Removed) != 1 {
			t.Fatalf("removed %+v", o.Disclosed.Removed)
		}
	}
}

func TestU14TheDefaultHoldsBeforeThePersonsRules(t *testing.T) {
	w := newDWorld(t)
	id := w.seed(qNow.Add(-time.Hour), 0, "Your verification code", "Your code is 123456.")
	d := with(allParts, []rules.Rule{rules.Default(), removing("rul_code", shapes(contracts.ShapeNumericCode))})
	o, err := w.discA.Message(on(w.askA), w.b, d, id)
	if err != nil || o.Withheld == nil || o.Withheld.Rule.ID != rules.DefaultID {
		t.Fatalf("%+v, %v", o, err)
	}
}

func TestU14RemovalsAreJoinedOnOneViewInAnyOrder(t *testing.T) {
	w := newDWorld(t)
	id := w.seed(qNow.Add(-time.Hour), 0, "Ana ana@example.com 555 123 4567", "Write to ana@example.com or call 555 123 4567. Thanks.")
	a := removing("rul_a", shapes(contracts.ShapeEmailAddress))
	b := removing("rul_b", shapes(contracts.ShapePhoneNumber))
	one := w.read(on(w.askA), w.discA, with(allParts, []rules.Rule{a, b}), id)
	two := w.read(on(w.askA), w.discA, with(allParts, []rules.Rule{b, a}), id)
	if subjectOf(one) != "Ana [removed] [removed]" || textOf(one) != "Write to [removed] or call [removed]. Thanks." {
		t.Fatalf("%q / %q", subjectOf(one), textOf(one))
	}
	if subjectOf(one) != subjectOf(two) || textOf(one) != textOf(two) {
		t.Fatalf("the order changed it: %q / %q", subjectOf(two), textOf(two))
	}
}

func TestU14TheQueryReadsTheReducedMessage(t *testing.T) {
	w := newDWorld(t)
	id := w.seed(qNow.Add(-time.Hour), 0, "Hello", "Call 5551234567 now.")
	phone := []rules.Rule{removing("rul_phone", shapes(contracts.ShapePhoneNumber))}
	search := formula(group(cond(contracts.VarBody, contracts.OpContains, "5551234567")))
	if _, err := w.discA.Message(on(w.askA), w.b, with(allParts, phone, every, search), id); !errors.Is(err, contracts.ErrNotFound) {
		t.Fatalf("a search for a removed number matched: %v", err)
	}
	// Without the rule it matches: the search is not wrong.
	if o, err := w.discA.Message(on(w.askA), w.b, with(allParts, nil, every, search), id); err != nil || o.Disclosed == nil {
		t.Fatalf("%+v, %v", o, err)
	}
	// Its negation reads the reduced text too: what leaves holds no number.
	not := formula(group(contracts.Condition{Var: contracts.VarBody, Op: contracts.OpContains, Value: "5551234567", Not: true}))
	if got := w.read(on(w.askA), w.discA, with(allParts, phone, every, not), id); textOf(got) != "Call [removed] now." {
		t.Fatalf("%q", textOf(got))
	}
	// A condition on the sender a rule removed is Unknown: neither it nor
	// its negation matches.
	from := cond(contracts.VarFromAddress, contracts.OpIs, "no-reply@example.com")
	notFrom := from
	notFrom.Not = true
	for _, f := range []*query.Canonical{formula(group(from)), formula(group(notFrom))} {
		if _, err := w.discA.Message(on(w.askA), w.b, with(allParts, phone, every, f), id); !errors.Is(err, contracts.ErrNotFound) {
			t.Fatalf("a condition on a removed sender matched: %v", err)
		}
	}
}

func TestU14ThePartsAreCutAfter(t *testing.T) {
	w := newDWorld(t)
	id := w.seed(qNow.Add(-time.Hour), 0, "Call 555 123 4567", "Or 555 765 4321.")
	rs := []rules.Rule{removing("rul_phone", shapes(contracts.ShapePhoneNumber))}
	for _, c := range []struct {
		parts         []contracts.Part
		subject, text string
	}{
		{allParts, "Call [removed]", "Or [removed]."},
		{[]contracts.Part{contracts.PartSubject}, "Call [removed]", "<none>"},
		{[]contracts.Part{contracts.PartBody}, "<none>", "Or [removed]."},
		{[]contracts.Part{contracts.PartSender}, "<none>", "<none>"},
	} {
		got := w.read(on(w.askA), w.discA, with(c.parts, rs), id)
		if subjectOf(got) != c.subject || textOf(got) != c.text || len(got.Message.From) != 0 {
			t.Fatalf("%v: %q / %q / %v", c.parts, subjectOf(got), textOf(got), got.Message.From)
		}
		p := w.page(on(w.askA), w.discA, with(c.parts, rs), "", 10)
		if len(p.Items) != 1 || p.Items[0].Disclosed == nil || subjectOf(p.Items[0].Disclosed) != c.subject || p.Items[0].Disclosed.Message.Text != nil {
			t.Fatalf("%v: page %+v", c.parts, p.Items)
		}
	}
}

func TestU14DecodedAndHTMLTextAndAnAttachment(t *testing.T) {
	w := newDWorld(t)
	const head = "From: no-reply@example.com\r\nSubject: Hi\r\nMIME-Version: 1.0\r\n"
	b64 := w.seedRaw(1, "Hi", head+"Content-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: base64\r\n\r\nV3JpdGUgdG8gYW5hQGV4YW1wbGUuY29tIG9yIDU1NSAxMjMgNDU2Ny4=\r\n")
	qp := w.seedRaw(2, "Hi", head+"Content-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\nWrite to ana=40example.com or 555 123 =\r\n4567.\r\n")
	html := w.seedRaw(3, "Hi", head+"Content-Type: text/html; charset=utf-8\r\n\r\n<p>Write to ana&#64;example&#12290;com or 555<span> 123 </span>4567.</p>\r\n")
	rs := []rules.Rule{removing("rul_both", shapes(contracts.ShapeEmailAddress, contracts.ShapePhoneNumber))}
	for _, id := range []string{b64, qp, html} {
		got := textOf(w.read(on(w.askA), w.discA, with(allParts, rs), id))
		if strings.Contains(got, "ana") || strings.ContainsAny(got, "0123456789") || !strings.Contains(got, "[removed]") {
			t.Fatalf("%s: %q", id, got)
		}
	}
	att := w.seed(qNow.Add(-3*time.Hour), 0, "Report", "Attached.")
	o, a, err := w.discA.Attachment(on(w.askA), w.b, with(allParts, rs), att, 0)
	if !errors.Is(err, contracts.ErrNotFound) || a != nil || o.Disclosed != nil {
		t.Fatalf("an attachment left under a rule removing shapes: %+v, %v", o, err)
	}
}

func TestU15PartsRemovedWhole(t *testing.T) {
	w := newDWorld(t)
	q := &qMsg{at: qNow.Add(-time.Hour), header: "Ana 555 123 4567 <private@example.com>", subject: "Plans", body: "See you.",
		attach: []contracts.MessageAttachment{{Index: 0, Name: "ana-555-1234567.pdf", Type: "application/pdf", Size: 10}}}
	id := seedQ(t, w.st, w.b, q)
	for _, c := range []struct {
		name          string
		rm            *rules.Removal
		subject, text string
		from, atts    bool
	}{
		{"subject", parts(contracts.PartSubject), "[removed]", "See you.", true, true},
		{"body", parts(contracts.PartBody), "Plans", "[removed]", true, true},
		{"sender", parts(contracts.PartSender), "Plans", "See you.", false, true},
		{"attachments", parts(contracts.PartAttachments), "Plans", "See you.", true, false},
		{"email shapes", shapes(contracts.ShapeEmailAddress), "Plans", "See you.", false, false},
		{"phone shapes", shapes(contracts.ShapePhoneNumber), "Plans", "See you.", false, false},
		{"a topic", topics(topicHome), "Plans", "See you.", false, false},
	} {
		got := w.read(onSentences(w.askA, &disclosetest.Sentences{}), w.discA, with(allParts, []rules.Rule{removing("rul_"+strings.ReplaceAll(c.name, " ", "_"), c.rm)}), id)
		if subjectOf(got) != c.subject || textOf(got) != c.text || (len(got.Message.From) > 0) != c.from || (len(got.Message.Attachments) > 0) != c.atts {
			t.Fatalf("%s: %q / %q / %+v / %+v", c.name, subjectOf(got), textOf(got), got.Message.From, got.Message.Attachments)
		}
		if len(got.Removed) != 1 {
			t.Fatalf("%s: removed %+v", c.name, got.Removed)
		}
		sum := got.Removed[0].Removed
		if !c.from && !slices.Contains(sum.Parts, contracts.PartSender) || !c.atts && sum.Attachments != 1 {
			t.Fatalf("%s: summary %+v", c.name, sum)
		}
	}
}

func TestU16ARemovalsApplicabilityIsDecidedAsAHoldIs(t *testing.T) {
	w := newDWorld(t)
	id := w.seed(qNow.Add(-time.Hour), 0, "Hello", "Call 555 123 4567.")
	about := query.Atom{Var: contracts.VarMessage, Op: contracts.OpAbout, Topic: "health"}
	r := removing("rul_health", shapes(contracts.ShapePhoneNumber), formula(group(cond(contracts.VarMessage, contracts.OpAbout, "health"))))
	d := with(allParts, []rules.Rule{r})
	// The model says it is not about health: the rule does not apply.
	w.askA.p = func(string, query.Atom) (float64, bool) { return 0.01, true }
	got := w.read(on(w.askA), w.discA, d, id)
	if textOf(got) != "Call 555 123 4567." || len(got.Removed) != 0 || len(got.Rules) != 1 || got.Rules[0].Rule.ID != "rul_health" {
		t.Fatalf("%q, removed %+v, cleared %+v", textOf(got), got.Removed, got.Rules)
	}
	if dec := decisionOfRule(t, w.decA, w.b, r.Ref(), id, w.clock.now()); dec.State != inbox.DecisionCleared {
		t.Fatalf("not committed Cleared: %+v", dec)
	}
	// Another message the model cannot answer for: Unknown applies, and the
	// decision is begun before it is asked.
	other := w.seed(qNow.Add(-2*time.Hour), 0, "Hello", "Call 555 123 4567.")
	w.askA.p = func(id2 string, a query.Atom) (float64, bool) { return 0.5, true }
	w.askA.before = func(q *inbox.Question) error {
		if dec := decisionOfRule(t, w.decA, w.b, r.Ref(), q.MessageID, w.clock.now()); dec.State != inbox.DecisionPending && dec.State != inbox.DecisionCleared {
			t.Errorf("asked before begun: %+v", dec)
		}
		return nil
	}
	got = w.read(on(w.askA), w.discA, d, other)
	if textOf(got) != "Call [removed]." || len(got.Removed) != 1 || len(got.Removed[0].Decided) == 0 || got.Removed[0].Decided[0].Atom != about {
		t.Fatalf("an answer in doubt: %q, %+v", textOf(got), got.Removed)
	}
	// A decision whose settlement was lost is recovered Held after the
	// grace, and then applies.
	third := w.seed(qNow.Add(-3*time.Hour), 0, "Hello", "Call 555 123 4567.")
	if _, err := w.decA.Begin(context.Background(), w.b, r.Ref(), []inbox.DecisionKey{{ID: third, ExpiresAt: qNow.AddDate(0, 0, 30)}}); err != nil {
		t.Fatal(err)
	}
	w.askA.before = nil
	if got := w.read(on(w.askA), w.discA, d, third); textOf(got) != "Call [removed]." {
		t.Fatalf("a Pending decision: %q", textOf(got))
	}
	w.clock.add(inbox.PendingGrace + time.Second)
	w.askA.p = func(string, query.Atom) (float64, bool) { return 0.01, true }
	if got := w.read(on(w.askA), w.discA, d, third); textOf(got) != "Call [removed]." {
		t.Fatalf("recovered: %q", textOf(got))
	}
	if dec := decisionOfRule(t, w.decA, w.b, r.Ref(), third, w.clock.now()); dec.State != inbox.DecisionHeld || !dec.Recovered {
		t.Fatalf("not recovered Held: %+v", dec)
	}
}

// decisionOfRule is what is decided of ref for id now (decisionOf for any
// rule).
func decisionOfRule(t *testing.T, d inbox.Decisions, b inbox.Box, ref rules.Ref, id string, now time.Time) inbox.Decision {
	t.Helper()
	got, err := d.Begin(context.Background(), b, ref, []inbox.DecisionKey{{ID: id, ExpiresAt: now.Add(-time.Hour)}})
	if err != nil {
		t.Fatal(err)
	}
	return got[id]
}

// homeSentences answers a sentence that names an address high, every other
// low.
func homeSentences() *disclosetest.Sentences {
	return &disclosetest.Sentences{P: func(s string, _ int) (float64, bool) {
		if strings.Contains(strings.ToLower(s), "address") || strings.Contains(s, "Main Street") {
			return 0.9, true
		}
		return 0.02, true
	}}
}

func TestU18AMeaningDecisionThroughTwoEnclaves(t *testing.T) {
	w := newStrictDWorld(t, nil)
	id := w.seed(qNow.Add(-time.Hour), 0, "My home address is 123 Main Street.", "Thank you. See you soon.")
	meaning := removing("rul_home", topics(topicHome))
	d := with(allParts, []rules.Rule{meaning})
	sa := homeSentences()
	got := w.read(onSentences(w.askA, sa), w.discA, d, id)
	if subjectOf(got) != "[removed]" || textOf(got) != "Thank you. See you soon." {
		t.Fatalf("%q / %q", subjectOf(got), textOf(got))
	}
	if len(got.Removed) != 1 || got.Removed[0].Removed.Sentences != 1 || got.Removed[0].Removed.Of != 3 || got.Removed[0].Removed.Whole {
		t.Fatalf("summary %+v", got.Removed)
	}
	ref := rules.MeaningRef(meaning.Ref(), inbox.TextVersion, redact.SplitVersion)
	dec := decisionOfRule(t, w.decB, w.b, ref, id, w.clock.now())
	if dec.State != inbox.DecisionCleared || dec.Meaning == nil {
		t.Fatalf("not settled with its sentences: %+v", dec)
	}
	// Through the other enclave, with a model that now answers low for
	// everything: the decision stands, the same sentence removed, nothing
	// asked; and a body-only and a subject-only connection meet it.
	low := &disclosetest.Sentences{}
	for _, c := range []struct {
		parts []contracts.Part
		want  string
	}{{allParts, "[removed]"}, {[]contracts.Part{contracts.PartSubject}, "[removed]"}} {
		got := w.read(onSentences(w.askB, low), w.discB, with(c.parts, []rules.Rule{meaning}), id)
		if subjectOf(got) != c.want {
			t.Fatalf("%v: %q", c.parts, subjectOf(got))
		}
	}
	if got := w.read(onSentences(w.askB, low), w.discB, with([]contracts.Part{contracts.PartBody}, []rules.Rule{meaning}), id); textOf(got) != "Thank you. See you soon." {
		t.Fatalf("body-only: %q", textOf(got))
	}
	if len(low.Asked) != 0 {
		t.Fatalf("a decided meaning asked again: %+v", low.Asked)
	}
}

func TestU18PartialNothingAndLostAnswers(t *testing.T) {
	w := newStrictDWorld(t, nil)
	meaning := removing("rul_home", topics(topicHome))
	d := with(allParts, []rules.Rule{meaning})
	ref := rules.MeaningRef(meaning.Ref(), inbox.TextVersion, redact.SplitVersion)
	// Partial: the second sentence unanswered is removed, the first kept.
	partial := w.seed(qNow.Add(-time.Hour), 0, "Hi", "First line here. Second line here.")
	sa := &disclosetest.Sentences{P: func(s string, _ int) (float64, bool) { return 0.01, !strings.HasPrefix(s, "Second") }}
	if got := w.read(onSentences(w.askA, sa), w.discA, d, partial); subjectOf(got) != "Hi" || textOf(got) != "First line here. [removed]" {
		t.Fatalf("partial: %q / %q", subjectOf(got), textOf(got))
	}
	// Nothing answered: abandoned, the whole subject and text removed for
	// this request; a later one decides it.
	none := w.seed(qNow.Add(-2*time.Hour), 0, "Hi", "Nothing here.")
	silent := &disclosetest.Sentences{P: func(string, int) (float64, bool) { return 0, false }}
	got := w.read(onSentences(w.askA, silent), w.discA, d, none)
	if subjectOf(got) != "[removed]" || textOf(got) != "[removed]" || !got.Removed[0].Removed.Whole {
		t.Fatalf("nothing answered: %q / %q %+v", subjectOf(got), textOf(got), got.Removed)
	}
	if dec := decisionOfRule(t, w.decA, w.b, ref, none, w.clock.now()); dec.State != inbox.DecisionExpired {
		t.Fatalf("not abandoned: %+v", dec)
	}
	if got := w.read(onSentences(w.askA, &disclosetest.Sentences{}), w.discA, d, none); textOf(got) != "Nothing here." {
		t.Fatalf("decided later: %q", textOf(got))
	}
	// A settlement lost: the whole subject and text removed, recovered Held
	// after the grace, and removed for good.
	lost := w.seed(qNow.Add(-3*time.Hour), 0, "Hi", "Lost one.")
	if _, err := w.decA.Begin(context.Background(), w.b, ref, []inbox.DecisionKey{{ID: lost, ExpiresAt: qNow.AddDate(0, 0, 30)}}); err != nil {
		t.Fatal(err)
	}
	w.clock.add(inbox.PendingGrace + time.Second)
	for range 2 {
		got := w.read(onSentences(w.askB, &disclosetest.Sentences{}), w.discB, d, lost)
		if textOf(got) != "[removed]" || got.Removed[0].Removed.Why == "" {
			t.Fatalf("lost: %q %+v", textOf(got), got.Removed)
		}
	}
	if dec := decisionOfRule(t, w.decB, w.b, ref, lost, w.clock.now()); dec.State != inbox.DecisionHeld || !dec.Recovered {
		t.Fatalf("not recovered: %+v", dec)
	}
}

func TestU18OtherRulesNeverShiftAMeaningDecision(t *testing.T) {
	w := newStrictDWorld(t, nil)
	id := w.seed(qNow.Add(-time.Hour), 0, "Call 555 123 4567 at home.", "My address is 1 Main Street. Thanks, ana@example.com.")
	meaning := removing("rul_home", topics(topicHome))
	other := removing("rul_mail", shapes(contracts.ShapeEmailAddress))
	sa := homeSentences()
	first := w.read(onSentences(w.askA, sa), w.discA, with(allParts, []rules.Rule{other, meaning}), id)
	asked := len(sa.Asked)
	// The other rule edited (a new version that removes phones), then
	// deleted: the meaning decision and its sentences stand.
	edited := removing("rul_mail", shapes(contracts.ShapePhoneNumber))
	edited.Version = 2
	second := w.read(onSentences(w.askB, sa), w.discB, with(allParts, []rules.Rule{meaning, edited}), id)
	third := w.read(onSentences(w.askA, sa), w.discA, with(allParts, []rules.Rule{meaning}), id)
	for _, got := range []*inbox.Disclosed{first, second, third} {
		if textOf(got) != "[removed] Thanks, [removed]." || subjectOf(got) != "Call [removed] at home." {
			t.Fatalf("%q / %q", subjectOf(got), textOf(got))
		}
	}
	if len(sa.Asked) != asked {
		t.Fatal("asked again for another rule's change")
	}
	// Two meaning rules in both orders remove the same text.
	again := removing("rul_home2", topics(query.Atom{Var: contracts.VarMessage, Op: contracts.OpMentions, Topic: "a phone number"}))
	a := w.read(onSentences(w.askA, sa), w.discA, with(allParts, []rules.Rule{meaning, again}), id)
	b := w.read(onSentences(w.askB, sa), w.discB, with(allParts, []rules.Rule{again, meaning}), id)
	if subjectOf(a) != subjectOf(b) || textOf(a) != textOf(b) {
		t.Fatalf("the order changed it: %q|%q against %q|%q", subjectOf(a), textOf(a), subjectOf(b), textOf(b))
	}
}

// brokenMap is redact.Fixed whose split leaves an interior sentence out:
// a map that breaks SentenceMap.Check.
type brokenMap struct{ redact.Fixed }

func (b brokenMap) Sentences(v redact.View) redact.SentenceMap {
	m := b.Fixed.Sentences(v)
	if len(m.Sentences) > 2 {
		m.Sentences = append(m.Sentences[:1], m.Sentences[2:]...)
	}
	return m
}

func TestN1AnInvalidSentenceMapRemovesTheWholeSubjectAndText(t *testing.T) {
	w := newDWorld(t)
	id := w.seed(qNow.Add(-time.Hour), 0, "Plans", "One. Two. Three.")
	inbox.SetRedactor(w.discA, brokenMap{})
	sa := &disclosetest.Sentences{}
	meaning := removing("rul_home", topics(topicHome))
	got := w.read(onSentences(w.askA, sa), w.discA, with(allParts, []rules.Rule{meaning}), id)
	if subjectOf(got) != "[removed]" || textOf(got) != "[removed]" || !got.Removed[0].Removed.Whole {
		t.Fatalf("%q / %q %+v", subjectOf(got), textOf(got), got.Removed)
	}
	if len(sa.Asked) != 0 {
		t.Fatal("asked about a map that breaks its invariants")
	}
	ref := rules.MeaningRef(meaning.Ref(), inbox.TextVersion, redact.SplitVersion)
	if dec := decisionOfRule(t, w.decA, w.b, ref, id, w.clock.now()); dec.State != inbox.DecisionExpired {
		t.Fatalf("a decision begun on it: %+v", dec)
	}
}

func TestU20WhatTheModelSeesForMeaning(t *testing.T) {
	w := newDWorld(t)
	q := &qMsg{at: qNow.Add(-time.Hour), header: "Ana Private <private@example.com>", subject: "Reach me at ana@example.com",
		body: "Call 555 123 4567. Card 4111 1111 1111 1111. See https://example.com/x.", to: []string{"me@example.org"},
		attach: []contracts.MessageAttachment{{Index: 0, Name: "secret.pdf", Type: "application/pdf", Size: 3}}}
	id := seedQ(t, w.st, w.b, q)
	sa := &disclosetest.Sentences{}
	meaning := removing("rul_home", topics(topicHome))
	// A subject-only connection: the text's sentences are asked too.
	w.read(onSentences(w.askA, sa), w.discA, with([]contracts.Part{contracts.PartSubject}, []rules.Rule{removing("rul_phone", shapes(contracts.ShapePhoneNumber)), meaning}), id)
	if len(sa.Asked) != 1 {
		t.Fatalf("asked %d times", len(sa.Asked))
	}
	all := strings.Join(sa.Asked[0].Sentences, "|")
	if len(sa.Asked[0].Sentences) != 4 || !strings.HasPrefix(all, "Reach me at [removed]|") {
		t.Fatalf("sentences %q", sa.Asked[0].Sentences)
	}
	for _, never := range []string{"ana@", "555", "4111", "example.com", "Private", "private@", "me@example.org", "secret.pdf", id, w.b.MailboxID} {
		if strings.Contains(all, never) {
			t.Fatalf("the model saw %q: %q", never, all)
		}
	}
}

func TestN2ATopicRuleRemovesEveryPieceMaskedFromTheModel(t *testing.T) {
	w := newDWorld(t)
	id := w.seed(qNow.Add(-time.Hour), 0, "ana@example.com", "Call 555 123 4567. Card 4111 1111 1111 1111. Visit https://example.com/x. Thanks.")
	low := &disclosetest.Sentences{P: func(string, int) (float64, bool) { return 0, true }}
	meaning := removing("rul_home", topics(topicHome))
	const want = "Call [removed]. Card [removed]. Visit [removed]. Thanks."
	for _, rs := range [][]rules.Rule{
		{meaning},
		{removing("rul_phone", shapes(contracts.ShapePhoneNumber)), meaning},
		{meaning, removing("rul_mail", shapes(contracts.ShapeEmailAddress))},
	} {
		got := w.read(onSentences(w.askA, low), w.discA, with(allParts, rs), id)
		if subjectOf(got) != "[removed]" || textOf(got) != want {
			t.Fatalf("%d rules: %q / %q", len(rs), subjectOf(got), textOf(got))
		}
	}
	// Two messages alike once masked are answered alike, and both lose
	// their pieces whatever the model said.
	a := w.seed(qNow.Add(-2*time.Hour), 0, "Hi", "Reach me at ana@example.com.")
	b := w.seed(qNow.Add(-3*time.Hour), 0, "Hi", "Reach me at 555 123 4567.")
	for _, m := range []string{a, b} {
		if got := w.read(onSentences(w.askA, low), w.discA, with(allParts, []rules.Rule{meaning}), m); textOf(got) != "Reach me at [removed]." {
			t.Fatalf("%q", textOf(got))
		}
	}
}

// viewAsker answers by the view it is asked under, and records what it is
// given.
type viewAsker struct {
	mu   sync.Mutex
	p    map[string]float64 // by view name; absent answers 0.05
	seen []inbox.Question
	text []string
}

func (a *viewAsker) Ask(ctx context.Context, q *inbox.Question) (*inbox.Asked, error) {
	text, _, err := q.Text(ctx)
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.seen = append(a.seen, *q)
	a.text = append(a.text, text)
	p, ok := a.p[q.View.Name]
	if !ok {
		p = 0.05
	}
	out := &inbox.Asked{Answers: query.Answers{}, Outcome: inbox.AskAnswered, Evidence: map[query.Atom]inbox.Evidence{}, Called: 1}
	for _, at := range q.Atoms {
		out.Answers[at] = p
		out.Evidence[at] = inbox.Evidence{P: p, Model: playedModel, At: qNow}
	}
	return out, nil
}

func TestU21AQuerysModelAtomsAreAskedOfTheReducedView(t *testing.T) {
	w := newDWorld(t)
	id := w.seed(qNow.Add(-time.Hour), 0, "Invoice", "Pay to 555 123 4567 today.")
	about := formula(group(cond(contracts.VarMessage, contracts.OpAbout, "a payment")))
	asker := &viewAsker{p: map[string]float64{inbox.ViewOriginal.Name: 0.95, "": 0.95}}
	rs := []rules.Rule{removing("rul_phone", shapes(contracts.ShapePhoneNumber))}
	_, err := w.discA.Message(on(asker), w.b, with(allParts, rs, every, about), id)
	if !errors.Is(err, contracts.ErrNotFound) {
		t.Fatalf("the original's answer was used for the reduced text: %v", err)
	}
	if len(asker.seen) != 1 || !strings.HasPrefix(asker.seen[0].View.Name, "r-") || asker.text[0] != "Pay to [removed] today." || asker.seen[0].Subject != "Invoice" {
		t.Fatalf("asked %+v %q", asker.seen, asker.text)
	}
	reduced := asker.seen[0].View.Name
	asker.p[reduced] = 0.95
	if got := w.read(on(asker), w.discA, with(allParts, rs, every, about), id); textOf(got) != "Pay to [removed] today." || len(got.Decided) != 1 {
		t.Fatalf("%q %+v", textOf(got), got.Decided)
	}
	// Without a removal the original view is asked, and the reduced
	// answer is never used for it.
	asker.p[inbox.ViewOriginal.Name] = 0.01
	if _, err := w.discA.Message(on(asker), w.b, with(allParts, nil, every, about), id); !errors.Is(err, contracts.ErrNotFound) {
		t.Fatalf("the reduced answer was used for the original: %v", err)
	}
	if last := asker.seen[len(asker.seen)-1]; last.View != inbox.ViewOriginal {
		t.Fatalf("asked under %+v", last.View)
	}
}

func TestU22ModelOffRulesAndTheModelOff(t *testing.T) {
	w := newDWorld(t)
	id := w.seed(qNow.Add(-time.Hour), 0, "Hello there", "Some text. More text.")
	sa := &disclosetest.Sentences{}
	off := removing("rul_home", topics(topicHome))
	off.ModelOff = true
	got := w.read(onSentences(w.askA, sa), w.discA, with(allParts, []rules.Rule{off}), id)
	if subjectOf(got) != "[removed]" || textOf(got) != "[removed]" || len(sa.Asked) != 0 || w.askA.modelCalls() != 0 {
		t.Fatalf("model off: %q / %q, asked %d", subjectOf(got), textOf(got), len(sa.Asked))
	}
	hold := holding("rul_about", formula(group(cond(contracts.VarMessage, contracts.OpAbout, "health"))))
	hold.ModelOff = true
	if o, err := w.discA.Message(on(w.askA), w.b, with(allParts, []rules.Rule{hold}), id); err != nil || o.Withheld == nil || w.askA.modelCalls() != 0 {
		t.Fatalf("a hold with the model off for it: %+v, %v", o, err)
	}
	// The request's model off: a topic removes the whole text, a removal
	// whose applicability needs the model applies.
	d := with(allParts, []rules.Rule{removing("rul_home", topics(topicHome)), removing("rul_x", parts(contracts.PartSender), formula(group(cond(contracts.VarMessage, contracts.OpAbout, "health"))))})
	d.Model = false
	got = w.read(onSentences(w.askA, sa), w.discA, d, id)
	if textOf(got) != "[removed]" || len(got.Message.From) != 0 || len(sa.Asked) != 0 || len(got.Removed) != 2 {
		t.Fatalf("the request's model off: %q %+v", textOf(got), got.Removed)
	}
}
