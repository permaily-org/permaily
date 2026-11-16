package receipts

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/query"
	"github.com/permaily-org/permaily/internal/rules"
)

func TestH15APageAndItsReceiptsShareNothing(t *testing.T) {
	t.Run("input head", func(t *testing.T) {
		head := samples(t)[KindSearch]
		p, err := NewPage(head)
		if err != nil {
			t.Fatal(err)
		}
		before := normal(t, p.Receipt())
		head.Formulas[0].ID = "changed"
		head.Rules[0].Version++
		*head.Delivered[0].Subject = "changed"
		if got := normal(t, p.Receipt()); got != before {
			t.Fatal("mutating the supplied head changed the page's receipt")
		}
	})
	t.Run("returned nested evidence", func(t *testing.T) {
		head := samples(t)[KindSearch]
		head.Delivered, head.Held = nil, nil
		p, err := NewPage(head)
		if err != nil {
			t.Fatal(err)
		}
		d := inbox.Disclosed{ID: "message", Decided: defaultEvidence(0.04, 0.02)}
		if ok, err := p.Add(mailboxA, nil, inbox.Outcome{Disclosed: &d}); !ok || err != nil {
			t.Fatalf("Add: %v %v", ok, err)
		}
		snapshot := p.Receipt()
		snapshot.Delivered[0].Model[0].P = 0.99
		if got := p.Receipt().Delivered[0].Model[0].P; got != 0.04 {
			t.Fatalf("editing a returned receipt changed the page's original evidence to %v", got)
		}
	})
	t.Run("returned subject invalidates size", func(t *testing.T) {
		head := samples(t)[KindSearch]
		head.Delivered, head.Held = nil, nil
		p, err := NewPage(head)
		if err != nil {
			t.Fatal(err)
		}
		subject := "original"
		d := inbox.Disclosed{ID: "message", Message: contracts.VendorMessage{Subject: &subject}}
		if ok, err := p.Add(mailboxA, []contracts.Part{contracts.PartSubject}, inbox.Outcome{Disclosed: &d}); !ok || err != nil {
			t.Fatalf("Add: %v %v", ok, err)
		}
		// Fill a page with at most 50 individually bounded messages. Leave
		// 64 bytes of room, then change only a detached snapshot's subject
		// to another legal 2 KiB subject.
		parts := []contracts.Part{contracts.PartSender, contracts.PartSubject}
		for i := range 2 {
			large := largestMessage(inbox.NewMessageID(start.Add(-time.Duration(i+1) * time.Second)))
			if ok, err := p.Add(mailboxA, parts, inbox.Outcome{Disclosed: &large}); !ok || err != nil {
				t.Fatalf("large message: %v %v", ok, err)
			}
		}
		count := 3
		for count < inbox.DiscloseMaxLimit {
			one := "y"
			small := inbox.Disclosed{ID: inbox.NewMessageID(start.Add(-time.Duration(count+1) * time.Second)), Message: contracts.VendorMessage{Subject: &one, ReceivedAt: start, From: []contracts.MailAddress{{Address: "sender@example.com"}}}}
			encoded, err := json.Marshal(DeliveredOf(mailboxA, &small, parts))
			if err != nil {
				t.Fatal(err)
			}
			room := MaxDocumentBytes - 64 - p.Size()
			growth := len(encoded) + 1
			if room < growth {
				break
			}
			one = strings.Repeat("y", min(2<<10, 1+room-growth))
			if ok, err := p.Add(mailboxA, parts, inbox.Outcome{Disclosed: &small}); !ok || err != nil {
				t.Fatalf("small message: %v %v", ok, err)
			}
			count++
		}
		if MaxDocumentBytes-p.Size() != 64 {
			t.Fatalf("fixture has %d bytes of room", MaxDocumentBytes-p.Size())
		}
		snapshot := p.Receipt()
		*snapshot.Delivered[0].Subject = strings.Repeat("x", 2<<10)
		t.Logf("messages=%d; changed the returned snapshot to a valid 2 KiB subject", count)
		actual, err := Size(p.Receipt())
		if err != nil {
			t.Fatal(err)
		}
		writeErr := newWorld().enclave().Write(context.Background(), account, p.Receipt())
		t.Logf("cached size=%d, actual size=%d, Write=%v", p.Size(), actual, writeErr)
		if actual != p.Size() || errors.Is(writeErr, ErrTooLarge) {
			t.Fatal("the returned receipt mutated the page and invalidated its byte budget")
		}
	})
}

func TestH14AFormulaOfARequestIsACopy(t *testing.T) {
	c := canonical(t, contracts.Condition{Var: contracts.VarSubject, Op: contracts.OpContains, Value: "invoice"})
	f := FormulaOf(RoleSearch, c, true)
	f.Formula.All[0].Any[0].Value = "password"
	again, err := query.Canonicalise(c.Formula)
	if err != nil {
		t.Fatal(err)
	}
	if again.ID != c.ID {
		t.Fatalf("editing the receipt changed the original canonical formula but kept its old ID: before=%s after=%s", c.ID, again.ID)
	}
}

func TestH24APagesSizeIsItsReceiptsWithItemsAlreadyIn(t *testing.T) {
	for _, seed := range []int{0, 1, 2} {
		head := samples(t)[KindSearch]
		if seed == 0 {
			head.Delivered, head.Held = nil, nil
		}
		if seed == 1 {
			head.Held = nil
		}
		p, err := NewPage(head)
		if err != nil {
			t.Fatal(err)
		}
		for i := range 50 {
			subject := strings.Repeat("\x00\n\"\\<>&\u2028\u2029ñ𐍈", i+1)
			d := inbox.Disclosed{ID: "m", Message: contracts.VendorMessage{Subject: &subject}}
			outcome := inbox.Outcome{Disclosed: &d}
			if i%2 == 1 {
				outcome = inbox.Outcome{Withheld: &inbox.Withheld{ID: "held", Rule: defaultRef, Why: inbox.HeldDecided, Decided: defaultEvidence(0.9, 0.4)}}
			}
			ok, err := p.Add(mailboxA, []contracts.Part{contracts.PartSubject}, outcome)
			if err != nil {
				t.Fatal(err)
			}
			if !ok {
				break
			}
			actual, err := Size(p.Receipt())
			if err != nil || actual != p.Size() {
				t.Fatalf("seed=%d i=%d measured=%d cached=%d err=%v", seed, i, actual, p.Size(), err)
			}
		}
		got, _ := written(t, p.Receipt())
		if !reflect.DeepEqual(got.Delivered, p.Receipt().Delivered) || !reflect.DeepEqual(got.Held, p.Receipt().Held) {
			t.Fatal("contents changed through Write/List")
		}
	}
}

// H15: a copy of a receipt with every field filled shares no slice or pointer
// with it: editing everything the copy holds leaves the original as it was.
func TestH15ACopyOfAReceiptSharesNothing(t *testing.T) {
	subject, attachment := "s", 1
	model := []Model{{Var: contracts.Variable("message"), Op: contracts.Operator("about"), Topic: "t", P: 0.5}}
	form := contracts.Formula{V: 1, All: []contracts.Group{{Any: []contracts.Condition{{Var: contracts.Variable("subject"), Op: contracts.Operator("contains"), Value: "x"}}}}}
	held := Held{Mailbox: "m", Message: "1", Rule: rules.Ref{ID: "r", Version: 1}, Why: "w", Model: slices.Clone(model)}
	orig := Receipt{
		Formulas:  []Formula{{Role: RoleSearch, ID: "q", Formula: &form}, {Role: RoleSince, ID: "s"}},
		Rules:     []rules.Ref{{ID: "r", Version: 1}},
		Delivered: []Delivered{{Parts: []contracts.Part{contracts.Part("subject")}, From: []contracts.MailAddress{{Address: "a@example.com"}}, Subject: &subject, Attachment: &attachment, Model: slices.Clone(model), Cleared: []Cleared{{Rule: rules.Ref{ID: "r"}, Model: slices.Clone(model)}}}},
		Held:      []Held{held},
		Refused:   &Refused{Reason: RefusedHeld, Held: &held},
		Consent:   &Consent{Sentences: []string{"x"}, Parts: []contracts.Part{contracts.Part("subject")}, Mailboxes: []string{"m"}},
		Ended:     &Ended{Reason: contracts.EndReason("revoked")},
	}
	before, err := json.Marshal(orig)
	if err != nil {
		t.Fatal(err)
	}
	c := copyReceipt(orig)
	c.Formulas[0].ID, c.Formulas[0].Formula.All[0].Any[0].Value = "q2", "y"
	c.Rules[0].ID = "r2"
	d := &c.Delivered[0]
	d.Parts[0], d.From[0].Address, *d.Subject, *d.Attachment = contracts.Part("body"), "b@example.com", "s2", 2
	d.Model[0].P, d.Cleared[0].Model[0].P, d.Cleared[0].Rule.ID = 0.9, 0.9, "r2"
	c.Held[0].Model[0].P, c.Held[0].Why = 0.9, "w2"
	c.Refused.Reason, c.Refused.Held.Model[0].P = RefusedTooLate, 0.9
	c.Consent.Sentences[0], c.Consent.Parts[0], c.Consent.Mailboxes[0] = "y", contracts.Part("body"), "m2"
	c.Ended.Reason = contracts.EndReason("expired")
	after, err := json.Marshal(orig)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatalf("editing the copy changed the original:\n%s\n%s", before, after)
	}
	if empty := copyReceipt(Receipt{}); !reflect.DeepEqual(empty, Receipt{}) {
		t.Fatalf("a copy of an empty receipt is %+v", empty)
	}
}
