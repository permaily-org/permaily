package inbox_test

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/query"
	"github.com/permaily-org/permaily/internal/query/querytest"
	"github.com/permaily-org/permaily/internal/rules"
	"github.com/permaily-org/permaily/internal/store"
)

// V37: a page gives only messages True on every formula and held by no
// rule; the union of the pages, under budgets set low, is the brute force
// of every source read and every answer known; pages skip nothing and list
// nothing twice, in each of two mailboxes walked in turn.
func TestV37APageGivesOnlyWhatMatchesAndNoRuleHolds(t *testing.T) {
	for seed := range uint64(6) {
		t.Run(fmt.Sprint(seed), func(t *testing.T) {
			r := querytest.New(seed)
			w := newDWorld(t)
			other := qBox(t)
			other.MailboxID = "mbx_other0000000000000000000"
			subjects := []string{"Your verification code", "Lunch on Friday?", "Invoice 482913", "Reset your password", "Travel plans", ""}
			bodies := []string{"See you there", "Here is your number: 482913.", "Para restablecer tu contraseña, pulsa aquí", strings.Repeat("long text ", 2000), "Nothing to see"}
			type seeded struct {
				b  inbox.Box
				id string
				q  *qMsg
			}
			var all []seeded
			for i := range 30 {
				b := w.b
				if i%3 == 0 {
					b = other
				}
				q := &qMsg{
					at: qNow.Add(-time.Duration(i) * time.Hour), n: i, from: []string{[]string{"a@example.com", "b@example.org"}[i%2]},
					subject: subjects[i%len(subjects)], body: bodies[(i/2)%len(bodies)],
				}
				all = append(all, seeded{b: b, id: seedQ(t, w.st, b, q), q: q})
			}
			fs := []*query.Canonical{every}
			if f, err := query.Canonicalise(r.Formula(2, 3)); err == nil && !f.Never {
				fs = append(fs, f)
			}
			// The model's answer for an atom of a message: fixed by both.
			answer := func(id string, a query.Atom) (float64, bool) {
				h := fnv.New32a()
				h.Write([]byte(id + a.Topic + string(a.Var)))
				return []float64{0.02, 0.5, 0.95}[h.Sum32()%3], true
			}
			w.askA.p = answer
			disc := inbox.NewDiscloserWith(w.st, query.Eval, w.decA, inbox.MatchBounds{Read: 7, TextReads: 2, Now: w.clock.now}, 3)
			d := disclosure(allParts, fs...)
			got := map[string]bool{}
			for _, b := range []inbox.Box{w.b, other} {
				before := ""
				for pages := 0; ; pages++ {
					if pages > 100 {
						t.Fatal("more than 100 pages")
					}
					p, err := disc.Page(on(w.askA), b, d, before, 4)
					if err != nil {
						t.Fatal(err)
					}
					for _, o := range p.Items {
						if got[o.ID()] {
							t.Fatalf("%s listed twice", o.ID())
						}
						got[o.ID()] = o.Disclosed != nil
					}
					if p.Next == "" {
						break
					}
					if before != "" && p.Next >= before {
						t.Fatalf("next %s does not go below %s", p.Next, before)
					}
					before = p.Next
				}
			}
			rule := rules.Default()
			for _, s := range all {
				meta := s.q.meta()
				item := &store.Message{MailboxID: s.b.MailboxID, ID: s.id, Size: len(s.q.raw())}
				text, _, _ := inbox.MessageText(context.Background(), s.q.raw(), inbox.MessageTextMax)
				m := inbox.QueryMessage(item, &meta, s.q.entry(), &inbox.RawText{Text: text, Size: int64(len(s.q.raw()))})
				answers := query.Answers{}
				for _, a := range append(query.Eval(fs, m, nil, query.ForQuery).Model, codeAtom, resetAtom) {
					answers[a], _ = answer(s.id, a)
				}
				held := false
				for _, f := range rule.Formulas {
					held = held || query.Resolve(query.Eval([]*query.Canonical{f}, m, answers, query.ForRule).Value, query.ForRule)
				}
				want := !held && query.Eval(fs, m, answers, query.ForQuery).Value == query.True
				if got[s.id] != want {
					t.Errorf("%s (%q): left %v, the brute force says %v", s.id[:8], s.q.subject, got[s.id], want)
				}
			}
		})
	}
}

// V38: a field of a part the request does not read is absent from every
// answer; the text only with the body, from the entry when it was not cut
// and the raw message when it was; attachments only with theirs.
func TestV38OnlyTheEffectivePartsLeave(t *testing.T) {
	w := newDWorld(t)
	short := w.seed(qNow.Add(-time.Hour), 1, "Lunch on Friday?", "See you there")
	long := w.seed(qNow.Add(-2*time.Hour), 1, "Minutes", strings.Repeat("the minutes of the meeting ", 1000)+"THE END")
	ctx := on(w.askA)
	for _, c := range []struct {
		parts                []contracts.Part
		subject, text, files bool
	}{
		{[]contracts.Part{contracts.PartSender}, false, false, false},
		{[]contracts.Part{contracts.PartSender, contracts.PartSubject}, true, false, false},
		{[]contracts.Part{contracts.PartSender, contracts.PartBody}, false, true, false},
		{[]contracts.Part{contracts.PartSender, contracts.PartAttachments}, false, false, true},
		{allParts, true, true, true},
	} {
		d := disclosure(c.parts)
		p := w.page(ctx, w.discA, d, "", 10)
		left, held := sortOut(t, p.Items)
		if len(left) != 2 || len(held) != 0 {
			t.Fatalf("%v: left %v, held %v", c.parts, left, held)
		}
		for _, o := range p.Items {
			m := o.Disclosed.Message
			if m.ID != w.b.MailboxID+"."+o.Disclosed.ID || m.ReceivedAt.IsZero() || len(m.From) != 1 {
				t.Fatalf("%v: %+v", c.parts, m)
			}
			if (m.Subject != nil) != c.subject || (m.Attachments != nil) != c.files || m.Text != nil {
				t.Fatalf("%v: a page's message %+v", c.parts, m)
			}
		}
		for _, id := range []string{short, long} {
			o, err := w.discA.Message(ctx, w.b, d, id)
			if err != nil || o.Disclosed == nil {
				t.Fatal(o, err)
			}
			m := o.Disclosed.Message
			if (m.Text != nil) != c.text || (m.Subject != nil) != c.subject || (m.Attachments != nil) != c.files {
				t.Fatalf("%v: a read %+v", c.parts, m)
			}
			if c.text && id == long && !strings.HasSuffix(*m.Text, "THE END") {
				t.Fatalf("the long text was not read whole from the raw message")
			}
			if c.text && id == short && *m.Text != "See you there" {
				t.Fatalf("text %q", *m.Text)
			}
		}
		if _, _, err := w.discA.Attachment(ctx, w.b, d, short, 0); !c.files && !errors.Is(err, contracts.ErrPartNotApproved) {
			t.Fatalf("%v: an attachment without the part: %v", c.parts, err)
		}
	}
}

// V39: a message that does not match, does not exist, is a forwarding
// confirmation, or is held, is not given, alike to the vendor; one the item
// and meta exclude reads no entry and no raw message; an attachment of a
// held message is held, with its rule and evidence (finding 15).
func TestV39WhatIsNotGivenIsAlike(t *testing.T) {
	w := newDWorld(t)
	other := seedQ(t, w.st, w.b, &qMsg{at: qNow.Add(-time.Hour), n: 1, from: []string{"x@elsewhere.example"}, subject: "Hi", body: "Hello"})
	conf := seedQ(t, w.st, w.b, &qMsg{at: qNow.Add(-2 * time.Hour), n: 1, from: []string{"forwarding-noreply@google.com"}, subject: "Gmail Forwarding Confirmation", body: "x", confirmation: true})
	code := seedQ(t, w.st, w.b, &qMsg{at: qNow.Add(-3 * time.Hour), n: 1, from: []string{"a@example.com"}, subject: "Hi", body: "Here is your number: 482913.",
		attach: []contracts.MessageAttachment{{Index: 0, Name: "a.pdf", Type: "application/pdf", Size: 3}}})
	w.askA.p = byMessage(map[string]float64{code: 0.9})
	fromExample := formula(group(cond(contracts.VarFromDomain, contracts.OpIs, "example.com")))
	d := disclosure(allParts, fromExample)
	ctx := on(w.askA)
	w.st.reset()
	for _, id := range []string{other, conf, strings.Repeat("0", 32), "not an id"} {
		if _, err := w.discA.Message(ctx, w.b, d, id); !errors.Is(err, contracts.ErrNotFound) {
			t.Fatalf("%s: %v", id, err)
		}
	}
	if len(w.st.entries) != 0 || len(w.st.raws) != 0 || w.askA.asked(other) != 0 {
		t.Fatalf("read what the item and meta exclude: entries %v, raws %v", w.st.entries, w.st.raws)
	}
	o, err := w.discA.Message(ctx, w.b, d, code)
	if err != nil || o.Withheld == nil || o.Withheld.Why != inbox.HeldDecided {
		t.Fatal(o, err)
	}
	o, a, err := w.discA.Attachment(ctx, w.b, d, code, 0)
	if err != nil || a != nil || o.Withheld == nil || o.Withheld.Rule != rules.Default().Ref() || len(o.Withheld.Decided) != 2 || o.Withheld.Decided[0].Evidence.P != 0.9 {
		t.Fatalf("a held attachment: %+v %v %v", o, a, err)
	}
	if _, _, err := w.discA.Attachment(ctx, w.b, d, other, 0); !errors.Is(err, contracts.ErrNotFound) {
		t.Fatal(err)
	}
}

// The default rule's phrases (ADR 0031, "The default rule").
var defaultPhrases = []string{
	"verification code", "security code", "sign-in code", "sign in code", "login code", "log-in code", "one-time code",
	"one-time password", "one time password", "passcode", "authentication code", "two-factor", "2-step verification",
	"password reset", "reset your password", "reset password", "código de verificación", "código de seguridad",
	"código de acceso", "restablecer tu contraseña", "restablecer la contraseña",
}

// H1: through the Discloser, a message with any of the 21 phrases in its
// subject or its text, in any case and width, with invisible characters,
// in English, Spanish or both, is held exactly, and nothing is asked or
// begun for it; one with none and benign numbers is decided by the model:
// held with no answer, in doubt or high, and given only when both answers
// are below 0.30.
func TestH1TheDefaultRuleHoldsCodesWithoutTheModel(t *testing.T) {
	w := newDWorld(t)
	var exact []string
	at := qNow.Add(-time.Hour)
	for i, ph := range defaultPhrases {
		variants := []string{ph, strings.ToUpper(ph), strings.ReplaceAll(ph, "o", "\u200bo"), "Re: Hello — " + ph + " inside"}
		v := variants[i%len(variants)]
		exact = append(exact, w.seed(at.Add(-time.Duration(2*i)*time.Minute), 1, "Hello "+v, "Nothing here"))
		exact = append(exact, w.seed(at.Add(-time.Duration(2*i+1)*time.Minute), 1, "Hello", "Mixed: hola and "+v+" here"))
	}
	exact = append(exact, w.seed(at.Add(-50*time.Minute), 1, "Your ＬＯＧＩＮ ＣＯＤＥ", ""))
	ctx := on(w.askA)
	p := w.page(ctx, w.discA, disclosure(allParts), "", 50)
	_, held := sortOut(t, p.Items)
	for _, id := range exact {
		if h := held[id]; h == nil || h.Why != inbox.HeldExact || len(h.Decided) != 0 {
			t.Fatalf("%s: %+v", id[:8], h)
		}
		if w.askA.asked(id) != 0 || decisionOf(t, w.decA, w.b, id, w.clock.now()).State != inbox.DecisionExpired {
			t.Fatalf("%s: asked or begun for an exact hold", id[:8])
		}
	}
	// No phrase: the model decides.
	cases := []struct {
		name  string
		p     func(string, query.Atom) (float64, bool)
		leave bool
	}{
		{"no answer", func(string, query.Atom) (float64, bool) { return 0, false }, false},
		{"both low", func(string, query.Atom) (float64, bool) { return 0.1, true }, true},
		{"one in doubt", func(_ string, a query.Atom) (float64, bool) {
			return map[bool]float64{true: 0.4, false: 0.1}[a == resetAtom], true
		}, false},
		{"one high", func(_ string, a query.Atom) (float64, bool) {
			return map[bool]float64{true: 0.8, false: 0.1}[a == codeAtom], true
		}, false},
	}
	for i, c := range cases {
		w2 := newDWorld(t)
		id := w2.seed(qNow.Add(-time.Hour), i, "Order 482913", "Call us on +1 555 0100 about order 482913")
		w2.askA.p = c.p
		p := w2.page(on(w2.askA), w2.discA, disclosure(allParts), "", 10)
		left, held := sortOut(t, p.Items)
		if (len(left) == 1) != c.leave || (held[id] != nil) == c.leave {
			t.Fatalf("%s: left %v, held %+v", c.name, left, held[id])
		}
	}
}

// H2: every read applies the rule before the formulas: a held message is
// never given, and its attachment neither, with how and its evidence; the
// rule's atoms and the query's own share one ask and one kept entry; a
// preview with the model (the same discloser) meets the same decision.
func TestH2TheRuleFirstOneAskOneDecision(t *testing.T) {
	w := newDWorld(t)
	travel := query.Atom{Var: contracts.VarMessage, Op: contracts.OpAbout, Topic: "travel"}
	fs := formula(group(contracts.Condition{Var: contracts.VarMessage, Op: contracts.OpAbout, Value: "travel"}))
	held := w.seed(qNow.Add(-time.Hour), 1, "Trip", "Your code is 482913, enjoy the trip")
	free := w.seed(qNow.Add(-2*time.Hour), 1, "Trip", "Flights to Lisbon")
	w.askA.p = func(id string, a query.Atom) (float64, bool) {
		switch {
		case a == travel:
			return 0.95, true
		case id == held && a == codeAtom:
			return 0.92, true
		}
		return 0.05, true
	}
	d := disclosure(allParts, fs)
	ctx := on(w.askA)
	p := w.page(ctx, w.discA, d, "", 10)
	left, out := sortOut(t, p.Items)
	if !slices.Equal(left, []string{free}) || out[held] == nil || out[held].Why != inbox.HeldDecided || len(out[held].Decided) != 2 {
		t.Fatalf("left %v, held %+v", left, out)
	}
	if w.askA.asked(free) != 1 || len(w.askA.atoms[free]) != 3 {
		t.Fatalf("the rule's and the query's atoms in %d asks: %v", w.askA.asked(free), w.askA.atoms[free])
	}
	o := p.Items[slices.IndexFunc(p.Items, func(o inbox.Outcome) bool { return o.ID() == free })]
	if len(o.Disclosed.Rules) != 1 || o.Disclosed.Rules[0].Rule != rules.Default().Ref() || len(o.Disclosed.Rules[0].Decided) != 2 || len(o.Disclosed.Decided) != 1 || o.Disclosed.Decided[0].Atom != travel {
		t.Fatalf("what it rested on: %+v", o.Disclosed)
	}
	calls := w.askA.modelCalls()
	preview := w.page(on(w.askA), w.discA, disclosure([]contracts.Part{contracts.PartSender, contracts.PartSubject}, fs), "", 10)
	if pl, ph := sortOut(t, preview.Items); !slices.Equal(pl, left) || ph[held] == nil || ph[held].Decided[0].Evidence != out[held].Decided[0].Evidence {
		t.Fatalf("the preview met another decision: %v %+v", pl, ph)
	}
	if w.askA.modelCalls() != calls {
		t.Fatal("the preview asked the model again")
	}
	ah, att, err := w.discA.Attachment(ctx, w.b, d, held, 0)
	if err != nil || att != nil || ah.Withheld == nil || ah.Withheld.Why != inbox.HeldDecided || len(ah.Withheld.Decided) != 2 {
		t.Fatalf("a held message's attachment: %+v %v %v", ah, att, err)
	}
}

// H3: the model off (Disclosure.Model false, or no Asker): nothing begun,
// nothing asked, every message the model would decide held undecided, an
// exact hold still exact; back on, the next request decides them. The
// provider failing: held, abandoned (nothing pending), decided by a later
// request. A text that cannot be read: held, exactly.
func TestH3TheModelOffHoldsEverythingItWouldDecide(t *testing.T) {
	w := newDWorld(t)
	m := w.seed(qNow.Add(-time.Hour), 1, "Lunch", "See you")
	code := w.seed(qNow.Add(-2*time.Hour), 1, "Your verification code", "")
	off := disclosure(allParts)
	off.Model = false
	for _, ctx := range []context.Context{on(w.askA), context.Background()} {
		d := off
		if ctx == context.Background() {
			d.Model = true // no Asker: the same
		}
		p := w.page(ctx, w.discA, d, "", 10)
		left, held := sortOut(t, p.Items)
		if len(left) != 0 || held[m] == nil || held[m].Why != inbox.HeldUndecided || held[code].Why != inbox.HeldExact || p.Model != contracts.MatchModelOff {
			t.Fatalf("left %v held %+v model %q", left, held, p.Model)
		}
	}
	if w.records.Blocks(w.b, m) != 0 || w.askA.modelCalls() != 0 {
		t.Fatal("something was begun or asked while the model was off")
	}
	// The provider failing: held, abandoned.
	w.askA.p = func(string, query.Atom) (float64, bool) { return 0, false }
	p := w.page(on(w.askA), w.discA, disclosure(allParts), "", 10)
	if _, held := sortOut(t, p.Items); held[m] == nil || held[m].Why != inbox.HeldUndecided {
		t.Fatalf("held %+v", held)
	}
	if got := decisionOf(t, w.decA, w.b, m, w.clock.now()); got.State != inbox.DecisionExpired {
		t.Fatalf("a failed ask left %+v", got)
	}
	w.askA.p = nil
	p = w.page(on(w.askA), w.discA, disclosure(allParts), "", 10)
	if left, _ := sortOut(t, p.Items); !slices.Equal(left, []string{m}) || p.Model != contracts.MatchModelAsked {
		t.Fatalf("back on: left %v (%q)", left, p.Model)
	}
	// A text that cannot be read: its body conditions are Unknown, which
	// holds exactly.
	gone := seedQ(t, w.st, w.b, &qMsg{at: qNow.Add(-3 * time.Hour), n: 1, from: []string{"a@example.com"}, subject: "Hello", body: strings.Repeat("x", 20000), rawTooLarge: true})
	p = w.page(on(w.askA), w.discA, disclosure(allParts), "", 10)
	if _, held := sortOut(t, p.Items); held[gone] == nil || held[gone].Why != inbox.HeldExact {
		t.Fatalf("an unreadable text: %+v", held[gone])
	}
}

// H4: a decision is begun before the Asker is called (a played Asker that
// fails the test when called for a message without its Pending); settled
// Held on a high answer, one in doubt, a kept refusal or an answer beside a
// missing one; Cleared on low answers; Abandoned when the model answered no
// part of it (the guard, every call failed, the Asker failing before
// asking); left Pending on an answer received and not kept, recovered Held
// after the grace and never cleared by a later low answer; an exact hold
// begins nothing; the evidence answered is the decision's.
func TestH4BegunBeforeAskedSettledAsTheAskSays(t *testing.T) {
	type c struct {
		name      string
		p         func(string, query.Atom) (float64, bool)
		refuse    func(string, query.Atom) bool
		unsettled func(string, query.Atom) bool
		outcome   func(string) inbox.AskOutcome
		fail      bool
		want      inbox.DecisionState // after the request
		why       inbox.HeldWhy
	}
	low := func(string, query.Atom) (float64, bool) { return 0.05, true }
	only := func(at query.Atom, p float64) func(string, query.Atom) (float64, bool) {
		return func(_ string, a query.Atom) (float64, bool) { return p, a == at }
	}
	for _, k := range []c{
		{name: "high", p: only(codeAtom, 0.9), want: inbox.DecisionHeld, why: inbox.HeldDecided},
		{name: "in doubt", p: func(string, query.Atom) (float64, bool) { return 0.5, true }, want: inbox.DecisionHeld, why: inbox.HeldDecided},
		{name: "a refusal", p: low, refuse: func(_ string, a query.Atom) bool { return a == codeAtom }, want: inbox.DecisionHeld, why: inbox.HeldDecided},
		{name: "low beside a missing one", p: only(codeAtom, 0.05), want: inbox.DecisionHeld, why: inbox.HeldDecided},
		{name: "low", p: low, want: inbox.DecisionCleared},
		{name: "the guard", p: low, outcome: func(string) inbox.AskOutcome { return inbox.AskLimit }, want: inbox.DecisionExpired, why: inbox.HeldUndecided},
		{name: "every call failed", p: func(string, query.Atom) (float64, bool) { return 0, false }, want: inbox.DecisionExpired, why: inbox.HeldUndecided},
		{name: "the Asker failed", p: low, fail: true, want: inbox.DecisionExpired},
		{name: "received, not kept", p: low, unsettled: func(string, query.Atom) bool { return true }, want: inbox.DecisionPending, why: inbox.HeldUndecided},
	} {
		t.Run(k.name, func(t *testing.T) {
			w := newDWorld(t)
			id := w.seed(qNow.Add(-time.Hour), 1, "Hello", "Here is your number: 482913.")
			exact := w.seed(qNow.Add(-2*time.Hour), 1, "Your sign-in code", "")
			a := w.askA
			a.p, a.refuse, a.unsettled, a.outcome = k.p, k.refuse, k.unsettled, k.outcome
			a.before = func(q *inbox.Question) error {
				if q.MessageID == exact {
					t.Error("an exact hold was asked about")
				}
				if got := decisionOf(t, w.decA, w.b, q.MessageID, w.clock.now()); got.State != inbox.DecisionPending {
					t.Errorf("asked before its decision was begun: %+v", got)
				}
				if k.fail {
					return errQ
				}
				return nil
			}
			p, err := w.discA.Page(on(a), w.b, disclosure(allParts), "", 10)
			if k.fail {
				if !errors.Is(err, errQ) {
					t.Fatal(err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				left, held := sortOut(t, p.Items)
				switch {
				case k.want == inbox.DecisionCleared:
					if !slices.Equal(left, []string{id}) {
						t.Fatalf("left %v", left)
					}
				case held[id] == nil || held[id].Why != k.why:
					t.Fatalf("held %+v", held[id])
				case k.why == inbox.HeldDecided && len(held[id].Decided) == 0:
					t.Fatal("held without its evidence")
				}
			}
			if got := decisionOf(t, w.decA, w.b, id, w.clock.now()); got.State != k.want {
				t.Fatalf("decision %+v, want %s", got, k.want)
			}
			if w.records.Blocks(w.b, exact) > 0 && decisionOf(t, w.decA, w.b, exact, w.clock.now()).State != inbox.DecisionExpired {
				t.Fatal("an exact hold was begun")
			}
			if k.want != inbox.DecisionPending {
				return
			}
			// Recovered after the grace, whatever is asked then.
			a.before, a.unsettled = nil, nil
			w.kept.erase(id)
			p = w.page(on(a), w.discA, disclosure(allParts), "", 10)
			if _, held := sortOut(t, p.Items); held[id] == nil || held[id].Why != inbox.HeldUndecided {
				t.Fatalf("within the grace: %+v", held[id])
			}
			w.clock.add(inbox.PendingGrace)
			p = w.page(on(a), w.discA, disclosure(allParts), "", 10)
			if _, held := sortOut(t, p.Items); held[id] == nil || held[id].Why != inbox.HeldRecovered {
				t.Fatalf("past the grace: %+v", held[id])
			}
		})
	}
}
