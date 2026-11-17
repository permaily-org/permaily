package receipts

import (
	"context"
	"math"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/envelope"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/inbox/disclosetest"
	"github.com/permaily-org/permaily/internal/query"
	"github.com/permaily-org/permaily/internal/rules"
)

// written is r as Write stores it, through a service, read back.
func written(t *testing.T, r *Receipt) (Receipt, string) {
	t.Helper()
	w := newWorld()
	s := w.enclave()
	if err := s.Write(context.Background(), account, r); err != nil {
		t.Fatal(err)
	}
	got := listAll(t, s, account)
	if len(got) != 1 {
		t.Fatalf("%d receipts", len(got))
	}
	return got[0], string(stored(t, w, r))
}

// H14: what left is recorded with its parts, and of those only its senders
// and subject; never its text, an attachment's name or its content.
func TestH14WhatLeftIsRecordedWithItsPartsAndNothingElse(t *testing.T) {
	subject, text := "Your order 5541", "Dear customer, the text that left"
	d := inbox.Disclosed{
		ID: "0000018a0b0c0d0e1112131415161718",
		Message: contracts.VendorMessage{
			ID: "0000018a0b0c0d0e1112131415161718", ReceivedAt: start,
			From: []contracts.MailAddress{{Name: "Shop", Address: "orders@shop.example"}}, Subject: &subject, Text: &text,
			Attachments: []contracts.VendorAttachment{{Index: 0, Name: "invoice-5541.pdf", Type: "application/pdf", Size: 1234}},
		},
	}
	for _, c := range []struct {
		name        string
		parts       []contracts.Part
		withSubject bool
	}{
		{"sender only", []contracts.Part{contracts.PartSender}, false},
		{"every part", []contracts.Part{contracts.PartSender, contracts.PartSubject, contracts.PartBody, contracts.PartAttachments}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			r := samples(t)[KindRead]
			r.Delivered = []Delivered{DeliveredOf(mailboxA, &d, c.parts)}
			got, doc := written(t, r)
			dl := got.Delivered[0]
			if dl.Mailbox != mailboxA || dl.Message != d.ID || !dl.Received.Equal(start) || !slices.Equal(dl.Parts, c.parts) {
				t.Fatalf("what left is not where it was and with its parts: %+v", dl)
			}
			if !reflect.DeepEqual(dl.From, d.Message.From) {
				t.Fatal("its senders are not recorded")
			}
			if (dl.Subject != nil) != c.withSubject || strings.Contains(doc, subject) != c.withSubject {
				t.Fatal("its subject is recorded only when it left")
			}
			for _, never := range []string{text, "invoice-5541.pdf", "application/pdf", "text_cut"} {
				if strings.Contains(doc, never) {
					t.Fatalf("the receipt holds %q", never)
				}
			}
		})
	}
	// A subject the request did not give is not recorded even when the
	// message carries one.
	if dl := DeliveredOf(mailboxA, &d, []contracts.Part{contracts.PartSender, contracts.PartBody}); dl.Subject != nil {
		t.Fatal("a subject not given was recorded")
	}
	if dl := DeliveredOf(mailboxA, &d, []contracts.Part{contracts.PartSubject}); dl.From != nil {
		t.Fatal("senders not given were recorded")
	}
}

// H14: what was held is recorded by identifier, with its rule and how, and
// the evidence it rests on only when it rests on the model.
func TestH14WhatWasHeldIsRecordedByIdentifierWithHow(t *testing.T) {
	for _, why := range []inbox.HeldWhy{inbox.HeldExact, inbox.HeldDecided, inbox.HeldRecovered, inbox.HeldUndecided} {
		w := inbox.Withheld{ID: "0000018a0b0c0d0f2122232425262728", Rule: defaultRef, Why: why}
		if why == inbox.HeldDecided {
			w.Decided = defaultEvidence(0.95, 0.40)
		}
		r := samples(t)[KindSearch]
		r.Delivered, r.Held = nil, []Held{HeldOf(mailboxB, &w)}
		got, _ := written(t, r)
		h := got.Held[0]
		if h.Mailbox != mailboxB || h.Message != w.ID || h.Rule != defaultRef || h.Why != string(why) {
			t.Fatalf("a %s hold: %+v", why, h)
		}
		if (len(h.Model) != 0) != (why == inbox.HeldDecided) {
			t.Fatalf("a %s hold names %d answers", why, len(h.Model))
		}
		// A read or an attachment of a held message: refused, with how.
		ref := RefusedHeldOf(mailboxB, &w)
		if ref.Reason != RefusedHeld || ref.Message != w.ID || ref.Mailbox != mailboxB || !reflect.DeepEqual(*ref.Held, HeldOf(mailboxB, &w)) {
			t.Fatalf("a refused %s hold: %+v", why, ref)
		}
	}
}

// H14: the vendor's search is kept in full, and a consent's formulas too;
// the others by identifier.
func TestH14TheSearchAndAConsentsFormulasAreKeptInFull(t *testing.T) {
	all := samples(t)
	search, _ := written(t, all[KindSearch])
	for _, f := range search.Formulas {
		if (f.Formula != nil) != (f.Role == RoleSearch) || !strings.HasPrefix(f.ID, "qry_") {
			t.Fatalf("the search's %s formula: %+v", f.Role, f)
		}
	}
	consent, _ := written(t, all[KindConsent])
	for _, f := range consent.Formulas {
		c, err := query.Canonicalise(*f.Formula)
		if err != nil || c.ID != f.ID {
			t.Fatalf("a consented formula is not kept whole: %v", err)
		}
	}
}

// H14: the model's answers are recorded as the discloser gave them, every
// field, and nothing done to them after the receipt is built changes it: a
// kept answer replaced between the ask and the write is not read again.
func TestH14TheModelsAnswersAreRecordedAsGiven(t *testing.T) {
	refusal := inbox.Evidence{Refused: true, Model: "gpt-6-luna", Prompt: "p2c", CanaryChecked: "2026-11-15", CanaryChange: "2026-11-01"}
	byRule := inbox.Evidence{ByRule: true}
	ds := []inbox.Decided{{Atom: signInCode, Evidence: evidence(0.12)}, {Atom: resetting, Evidence: refusal}, {Atom: invoices, Evidence: byRule}}
	want := []Model{
		{Var: contracts.VarMessage, Op: contracts.OpAbout, Topic: signInCode.Topic, P: 0.12, Name: "gpt-6-luna", Prompt: "p2c", CanaryChecked: "2026-11-15", CanaryChange: "2026-11-01"},
		{Var: contracts.VarMessage, Op: contracts.OpAbout, Topic: resetting.Topic, Refused: true, Name: "gpt-6-luna", Prompt: "p2c", CanaryChecked: "2026-11-15", CanaryChange: "2026-11-01"},
		{Var: contracts.VarSubject, Op: contracts.OpAbout, Topic: invoices.Topic, ByRule: true},
	}
	if got := ModelOf(ds); !reflect.DeepEqual(got, want) {
		t.Fatalf("ModelOf:\n%+v\nwant\n%+v", got, want)
	}
	if ModelOf(nil) != nil {
		t.Fatal("no answer is not nothing")
	}
	d := inbox.Disclosed{ID: "0000018a0b0c0d0e1112131415161718", Message: contracts.VendorMessage{ReceivedAt: start}, Decided: ds,
		Rules: []inbox.RuleCleared{{Rule: defaultRef, At: start, Decided: ds[:1]}}}
	r := samples(t)[KindRead]
	r.Delivered = []Delivered{DeliveredOf(mailboxA, &d, []contracts.Part{contracts.PartSender})}
	// The answer kept for the atom replaced after the ask.
	ds[0].Evidence.P, ds[0].Evidence.Model = 0.99, "another"
	got, doc := written(t, r)
	if !reflect.DeepEqual(got.Delivered[0].Model, want) || !reflect.DeepEqual(got.Delivered[0].Cleared[0].Model, want[:1]) {
		t.Fatal("the receipt is not what the ask answered")
	}
	if got.Delivered[0].Cleared[0].Rule != defaultRef || !got.Delivered[0].Cleared[0].At.Equal(start) {
		t.Fatal("the rule cleared and when")
	}
	if strings.Contains(doc, "another") || strings.Contains(doc, "hash") {
		t.Fatal("the receipt holds what was not given, or a content's hash")
	}
}

// H14, N4: a decision's evidence in a receipt is the one its record keeps,
// whichever request made it and through either enclave: a consent
// preview's settlement, then a vendor's read through the other enclave; a
// request that crashed before its receipt; a recovered hold, which names no
// answer.
func TestH14N4AReceiptNamesTheEvidenceTheDecisionsRecordKeeps(t *testing.T) {
	ctx := context.Background()
	at := start
	now := func() time.Time { return at }
	one, other := disclosetest.Enclaves(now)
	a := &inbox.BlockDecisions{Store: one, Now: now}
	b := &inbox.BlockDecisions{Store: other, Now: now}
	box := inbox.Box{MailboxID: mailboxA}
	key := func(t time.Time) inbox.DecisionKey {
		return inbox.DecisionKey{ID: inbox.NewMessageID(t), ExpiresAt: t.Add(30 * 24 * time.Hour)}
	}
	begin := func(d *inbox.BlockDecisions, k inbox.DecisionKey) inbox.Decision {
		t.Helper()
		got, err := d.Begin(ctx, box, defaultRef, []inbox.DecisionKey{k})
		if err != nil {
			t.Fatal(err)
		}
		return got[k.ID]
	}
	settle := func(d *inbox.BlockDecisions, k inbox.DecisionKey, s inbox.Settlement) inbox.Decision {
		t.Helper()
		got, err := d.Settle(ctx, box, defaultRef, map[string]inbox.Settlement{k.ID: s})
		if err != nil {
			t.Fatal(err)
		}
		return got[k.ID]
	}
	service := New(newWorld().mem, testKeys{secret: "root"}, now, (&counter{}).read)
	write := func(r *Receipt) Receipt {
		t.Helper()
		if err := service.Write(ctx, account, r); err != nil {
			t.Fatal(err)
		}
		got := listAll(t, service, account)
		return got[len(got)-1]
	}

	// A consent preview through A clears M2 on 0.04 and 0.02.
	m2 := key(start.Add(-2 * time.Hour))
	preview := defaultEvidence(0.04, 0.02)
	begun := begin(a, m2)
	if begun.State != inbox.DecisionBegun {
		t.Fatalf("begun: %s", begun.State)
	}
	if d := settle(a, m2, inbox.Settlement{State: inbox.DecisionCleared, Evidence: preview, Attempt: begun.Attempt}); d.State != inbox.DecisionCleared {
		t.Fatalf("cleared: %s", d.State)
	}
	at = at.Add(time.Second)
	// The vendor's read through B: the decision as its record keeps it.
	read := begin(b, m2)
	if read.State != inbox.DecisionCleared {
		t.Fatalf("B finds %s", read.State)
	}
	d := inbox.Disclosed{ID: m2.ID, Message: contracts.VendorMessage{ID: m2.ID, ReceivedAt: start.Add(-2 * time.Hour)},
		Rules: []inbox.RuleCleared{{Rule: defaultRef, At: read.At, Decided: read.Evidence}}}
	r := samples(t)[KindRead]
	r.Delivered = []Delivered{DeliveredOf(mailboxA, &d, []contracts.Part{contracts.PartSender})}
	if got := write(r); !reflect.DeepEqual(got.Delivered[0].Cleared[0].Model, ModelOf(preview)) {
		t.Fatalf("the read's receipt does not name the preview's evidence: %+v", got.Delivered[0].Cleared)
	}

	// A request through A held M3 on 0.91 and crashed before its receipt;
	// a read through B is refused with that evidence.
	m3 := key(start.Add(-3 * time.Hour))
	held := defaultEvidence(0.91, 0.05)
	begin(a, m3)
	settle(a, m3, inbox.Settlement{State: inbox.DecisionHeld, Evidence: held})
	found := begin(b, m3)
	if found.State != inbox.DecisionHeld {
		t.Fatalf("B finds %s", found.State)
	}
	r = samples(t)[KindRefused]
	r.Refused = RefusedHeldOf(mailboxA, &inbox.Withheld{ID: m3.ID, Rule: defaultRef, Why: inbox.HeldDecided, Decided: found.Evidence})
	if got := write(r); !reflect.DeepEqual(got.Refused.Held.Model, ModelOf(held)) || got.Refused.Held.Why != "decided" {
		t.Fatalf("the refusal does not name the crashed request's evidence: %+v", got.Refused.Held)
	}

	// A begun M4 whose settlement was lost, found by B past the grace:
	// held, recovered, naming no answer.
	m4 := key(start.Add(-4 * time.Hour))
	begin(a, m4)
	at = at.Add(inbox.PendingGrace + time.Second)
	if found := begin(b, m4); found.State != inbox.DecisionPending {
		t.Fatalf("B finds %s", found.State)
	}
	recovered := settle(b, m4, inbox.Settlement{State: inbox.DecisionHeld})
	if recovered.State != inbox.DecisionHeld || !recovered.Recovered {
		t.Fatalf("recovered: %+v", recovered)
	}
	r = samples(t)[KindRefused]
	r.Refused = RefusedHeldOf(mailboxA, &inbox.Withheld{ID: m4.ID, Rule: defaultRef, Why: inbox.HeldRecovered, Decided: recovered.Evidence})
	if got := write(r); got.Refused.Held.Why != "recovered" || got.Refused.Held.Model != nil {
		t.Fatalf("a recovered hold: %+v", got.Refused.Held)
	}
}

// largestMessage is a message at the bounds a stored message may hold (ADR
// 0026), every byte escaped as JSON escapes the worst.
func largestMessage(id string) inbox.Disclosed {
	worst := func(n int) string { return strings.Repeat("\x01", n) }
	var from []contracts.MailAddress
	for range 100 {
		from = append(from, contracts.MailAddress{Name: worst(16<<10/100 - 40), Address: strings.Repeat("a", 30) + "@example.org"})
	}
	subject := worst(2 << 10)
	return inbox.Disclosed{ID: id, Message: contracts.VendorMessage{ID: id, ReceivedAt: start, From: from, Subject: &subject}}
}

// worstTopic is a topic at its bound, escaped at the worst.
var worstTopic = strings.Repeat("\x01", contracts.MaxTextRunes)

// worstEvidence is n answers at their largest.
func worstEvidence(n int) []inbox.Decided {
	out := make([]inbox.Decided, n)
	for i := range out {
		out[i] = inbox.Decided{Atom: query.Atom{Var: contracts.VarMessage, Op: contracts.OpMentions, Topic: worstTopic},
			Evidence: inbox.Evidence{P: 0.123456789, Model: "gpt-6-luna-2026-11-15", Prompt: "p2c-2026-11-15", CanaryChecked: "2026-11-15", CanaryChange: "2026-11-01"}}
	}
	return out
}

// worstFormula is a formula at its bounds (MaxConditions conditions, each
// value at MaxTextRunes escaped at the worst), as a receipt keeps it.
func worstFormula(role FormulaRole) Formula {
	f := contracts.Formula{V: contracts.FormulaVersion}
	for range contracts.MaxGroups {
		g := contracts.Group{}
		for range contracts.MaxConditions / contracts.MaxGroups {
			g.Any = append(g.Any, contracts.Condition{Var: contracts.VarAttachmentName, Op: contracts.OpContains, Value: worstTopic, Not: true})
		}
		f.All = append(f.All, g)
	}
	return Formula{Role: role, ID: "qry_" + strings.Repeat("a", 26), Formula: &f}
}

// H24: each kind fits one entry at its bounds, so the service never has a
// receipt it cannot write: a search of the largest message and 50 held
// with their evidence (TestTheLargestMessageFitsOneReceipt, here through
// Page and Write), a read and an attachment with every formula's model
// answers, a refusal of a held message, a consent with every formula in
// full, an end.
func TestH24EachKindFitsAtItsBounds(t *testing.T) {
	head := samples(t)
	formulas := []Formula{worstFormula(RoleConsented), FormulaOf(RoleMailboxes, canonical(t, contracts.Condition{Var: contracts.VarMailbox, Op: contracts.OpIs, Value: mailboxA}), false), FormulaOf(RoleSince, canonical(t, contracts.Condition{Var: contracts.VarReceived, Op: contracts.OpSince, Value: "2026-11-01T00:00:00Z"}), false)}
	formulas[0].Formula = nil
	allParts := []contracts.Part{contracts.PartSender, contracts.PartSubject, contracts.PartBody, contracts.PartAttachments}
	// Every formula's model conditions, and the rule's.
	message := largestMessage("0000018a0b0c0d0e1112131415161718")
	message.Decided = worstEvidence(contracts.MaxModelConditions * 5)
	message.Rules = []inbox.RuleCleared{{Rule: defaultRef, At: start, Decided: worstEvidence(inbox.DecisionEvidenceMax)}}

	t.Run("search", func(t *testing.T) {
		r := head[KindSearch]
		r.Formulas = append(slices.Clone(formulas), worstFormula(RoleSearch))
		r.Delivered, r.Held = nil, nil
		p, err := NewPage(r)
		if err != nil {
			t.Fatal(err)
		}
		if ok, err := p.Add(mailboxA, allParts, inbox.Outcome{Disclosed: &message}); !ok || err != nil {
			t.Fatalf("the largest message did not fit a search's receipt: %v", err)
		}
		for i := range inbox.DiscloseMaxLimit - 1 {
			w := inbox.Withheld{ID: inbox.NewMessageID(start.Add(-time.Duration(i) * time.Second)), Rule: defaultRef, Why: inbox.HeldDecided, Decided: defaultEvidence(0.9, 0.1)}
			if ok, err := p.Add(mailboxA, allParts, inbox.Outcome{Withheld: &w}); !ok || err != nil {
				t.Fatalf("held message %d did not fit: %v", i, err)
			}
		}
		if _, doc := written(t, p.Receipt()); len(doc) > MaxDocumentBytes || len(doc) > p.Size() {
			t.Fatalf("%d bytes, counted %d", len(doc), p.Size())
		}
	})
	t.Run("read", func(t *testing.T) {
		r := head[KindRead]
		r.Formulas = formulas
		r.Delivered = []Delivered{DeliveredOf(mailboxA, &message, allParts)}
		fits(t, r)
	})
	t.Run("attachment", func(t *testing.T) {
		r := head[KindAttachment]
		r.Formulas = formulas
		d := DeliveredOf(mailboxA, &message, allParts)
		i := 999 // more attachments than a stored message holds
		d.Attachment = &i
		r.Delivered = []Delivered{d}
		fits(t, r)
	})
	t.Run("refused", func(t *testing.T) {
		r := head[KindRefused]
		r.Formulas = append(slices.Clone(formulas), worstFormula(RoleSearch))
		r.Refused = RefusedHeldOf(mailboxA, &inbox.Withheld{ID: message.ID, Rule: defaultRef, Why: inbox.HeldDecided, Decided: worstEvidence(inbox.DecisionEvidenceMax)})
		fits(t, r)
	})
	t.Run("consent", func(t *testing.T) {
		r := head[KindConsent]
		mailboxes := contracts.Formula{V: contracts.FormulaVersion, All: []contracts.Group{{}}}
		var ids []string
		for i := range contracts.MaxConditions {
			id := "mbx_" + strings.Repeat(string(rune('a'+i%26)), 26)
			ids = append(ids, id)
			mailboxes.All[0].Any = append(mailboxes.All[0].Any, contracts.Condition{Var: contracts.VarMailbox, Op: contracts.OpIs, Value: id})
		}
		r.Formulas = []Formula{worstFormula(RoleConsented), {Role: RoleMailboxes, ID: "qry_" + strings.Repeat("b", 26), Formula: &mailboxes}, FormulaOf(RoleSince, canonical(t, contracts.Condition{Var: contracts.VarReceived, Op: contracts.OpSince, Value: "2026-11-01T00:00:00Z"}), true)}
		// The sentences of each, at the worst: every value written in them.
		var sentence strings.Builder
		for range contracts.MaxConditions {
			sentence.WriteString("whose attachment's name does not contain \"" + worstTopic + "\", or ")
		}
		r.Consent.Sentences = []string{sentence.String(), strings.Repeat("mailbox name, ", contracts.MaxConditions), "Mail received since 1 November 2026."}
		r.Consent.Mailboxes = ids
		r.Consent.Parts = allParts
		r.Rules = []rules.Ref{defaultRef}
		fits(t, r)
	})
	t.Run("ended", func(t *testing.T) { fits(t, head[KindEnded]) })
	// H31: a summary of refusals at its largest: every operation with every
	// reason, each counted past any count a window can reach.
	t.Run("refusals", func(t *testing.T) {
		r := head[KindRefusals]
		r.Refusals.Counts = nil
		for _, op := range []string{"search", "read", "attachment"} {
			for _, reason := range allRefusedReasons {
				r.Refusals.Counts = append(r.Refusals.Counts, RefusalCount{Operation: op, Reason: reason, Count: math.MaxInt64})
			}
		}
		r.Refusals.Recorded = math.MaxInt64
		fits(t, r)
	})
}

// allRefusedReasons are every reason a refusal can have.
var allRefusedReasons = []RefusedReason{
	RefusedHeld, RefusedOutsideScope, RefusedPartNotGiven, RefusedFormula, RefusedFormulaPart, RefusedModelUnavailable,
	RefusedTooLarge, RefusedInvalidBody, RefusedInvalidLimit, RefusedInvalidCursor, RefusedRateLimited, RefusedUnavailable,
	RefusedConnectionEnded, RefusedTooLate,
}

// fits writes r and says it was within MaxDocumentBytes with room for its
// sealing in one item.
func fits(t *testing.T, r *Receipt) {
	t.Helper()
	n, err := Size(r)
	if err != nil {
		t.Fatal(err)
	}
	if n > MaxDocumentBytes {
		t.Fatalf("%d bytes", n)
	}
	if _, doc := written(t, r); len(doc) > n {
		t.Fatalf("%d bytes stored, counted %d", len(doc), n)
	}
}

// H24: a receipt at MaxDocumentBytes, sealed as Write seals it, with the
// attributes DynamoDB keeps beside it, fits one item of 400 KiB.
func TestH24AFullReceiptSealsWithinOneItem(t *testing.T) {
	key, _ := testKeys{secret: "root"}.Derive(context.Background(), Purpose)
	id := IDPrefix + strings.Repeat("f", IDLength-len(IDPrefix))
	partition := Partition(account)
	plain := make([]byte, MaxDocumentBytes)
	sealed, err := envelope.SealText(key, plain, aad(partition, id))
	if err != nil {
		t.Fatal(err)
	}
	// pk, sk, text, version and updated_at: names and values.
	item := len("pk") + len(partition) + len("sk") + len(id) + len("text") + len(sealed) + len("version") + 21 + len("updated_at") + 21
	if item > 400<<10 {
		t.Fatalf("a full receipt's item is %d bytes", item)
	}
}

// H15: a search's page ends before the message that would take its receipt
// past MaxDocumentBytes, keeping every message before it whole with its
// evidence; what Page counts is what Write stores.
func TestH15ASearchsPageEndsBeforeTheMessageThatWouldPassTheBound(t *testing.T) {
	r := samples(t)[KindSearch]
	r.Delivered, r.Held = nil, nil
	p, err := NewPage(r)
	if err != nil {
		t.Fatal(err)
	}
	parts := []contracts.Part{contracts.PartSender, contracts.PartSubject}
	var outcomes []inbox.Outcome
	for i := range inbox.DiscloseMaxLimit {
		id := inbox.NewMessageID(start.Add(-time.Duration(i) * time.Minute))
		if i%3 == 2 {
			outcomes = append(outcomes, inbox.Outcome{Withheld: &inbox.Withheld{ID: id, Rule: defaultRef, Why: inbox.HeldDecided, Decided: defaultEvidence(0.8, 0.2)}})
			continue
		}
		m := largestMessage(id)
		m.Decided = worstEvidence(2)
		outcomes = append(outcomes, inbox.Outcome{Disclosed: &m})
	}
	kept := 0
	for _, o := range outcomes {
		ok, err := p.Add(mailboxA, parts, o)
		if err != nil {
			t.Fatal(err)
		}
		if !ok {
			break
		}
		kept++
		// What Page counts is the document at its largest.
		if n, _ := Size(p.Receipt()); n != p.Size() {
			t.Fatalf("Page counts %d bytes, the document is %d", p.Size(), n)
		}
	}
	if kept == 0 || kept == len(outcomes) {
		t.Fatalf("%d of %d messages kept: the bound did not end the page", kept, len(outcomes))
	}
	before := p.Size()
	if ok, _ := p.Add(mailboxA, parts, outcomes[kept]); ok || p.Size() != before {
		t.Fatal("a message refused changed the receipt")
	}
	got := p.Receipt()
	if len(got.Delivered)+len(got.Held) != kept {
		t.Fatal("a message is missing from the receipt")
	}
	// Every message kept is whole, in order, with its evidence: the
	// vendor's cursor continues from the last one.
	di, hi := 0, 0
	for _, o := range outcomes[:kept] {
		if o.Disclosed != nil {
			if !reflect.DeepEqual(got.Delivered[di], DeliveredOf(mailboxA, o.Disclosed, parts)) {
				t.Fatal("a message kept was cut")
			}
			di++
			continue
		}
		if !reflect.DeepEqual(got.Held[hi], HeldOf(mailboxA, o.Withheld)) {
			t.Fatal("a hold kept was cut")
		}
		hi++
	}
	if _, doc := written(t, got); len(doc) > MaxDocumentBytes {
		t.Fatalf("%d bytes", len(doc))
	}
	never := inbox.Disclosed{ID: "x", Message: contracts.VendorMessage{ReceivedAt: time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)}}
	if ok, err := p.Add(mailboxA, parts, inbox.Outcome{Disclosed: &never}); ok || err == nil {
		t.Fatal("a message that cannot be written was recorded")
	}
	if _, err := p.Add(mailboxA, parts, inbox.Outcome{}); err == nil {
		t.Fatal("an outcome neither made available nor held was recorded")
	}
	both := outcomes[0]
	both.Withheld = outcomes[2].Withheld
	if _, err := p.Add(mailboxA, parts, both); err == nil {
		t.Fatal("an outcome both made available and held was recorded")
	}
	// The page's receipt is its own: changing it changes no later one.
	got.Delivered[0].Message = "changed"
	if p.Receipt().Delivered[0].Message == "changed" {
		t.Fatal("the page's receipt is shared")
	}
}

// Size counts V, ID and At at their largest, whatever r says of them.
func TestSizeCountsTheLargestIdentifierAndTime(t *testing.T) {
	r := samples(t)[KindEnded]
	n, err := Size(r)
	if err != nil {
		t.Fatal(err)
	}
	r.ID, r.At, r.V = "x", time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), 9
	if m, _ := Size(r); m != n {
		t.Fatalf("%d, then %d", n, m)
	}
	if _, doc := written(t, r); len(doc) > n {
		t.Fatalf("%d bytes stored, counted %d", len(doc), n)
	}
	bad := &Receipt{Consent: &Consent{EndsAt: time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)}}
	if _, err := Size(bad); err == nil {
		t.Fatal("a document that cannot be written was counted")
	}
	if _, err := NewPage(bad); err == nil {
		t.Fatal("a page began from a document that cannot be written")
	}
	if err := newWorld().enclave().Write(context.Background(), account, bad); err == nil {
		t.Fatal("a document that cannot be written was written")
	}
}
