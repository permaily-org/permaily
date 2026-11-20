package receipts

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/query"
)

// What a receipt says of a request, built from what the discloser answered
// (inbox.Outcome) and never from anything read again: a decision's evidence
// is the one its record keeps, whichever request made it (ADR 0031, N4), and
// what left is only what the request's parts gave.

// FormulaOf is one formula of a request by role and canonical identifier;
// with full, its canonical form too, which a receipt keeps for a vendor's
// search and for every formula a consent showed (choice 22).
func FormulaOf(role FormulaRole, c *query.Canonical, full bool) Formula {
	f := Formula{Role: role, ID: c.ID}
	if full {
		form := copyFormula(c.Formula)
		f.Formula = &form
	}
	return f
}

// ModelOf is the model's answers a value or a decision rested on, in order,
// as the Asker or the decision's record gave them: the probability or the
// refusal, the model, the prompt's version and the canary's days. Never the
// hash of what was asked.
func ModelOf(ds []inbox.Decided) []Model {
	if len(ds) == 0 {
		return nil
	}
	out := make([]Model, len(ds))
	for i, d := range ds {
		e := d.Evidence
		out[i] = Model{
			Var: d.Atom.Var, Op: d.Atom.Op, Topic: d.Atom.Topic,
			P: e.P, Refused: e.Refused, ByRule: e.ByRule,
			Name: e.Model, Prompt: e.Prompt, CanaryChecked: e.CanaryChecked, CanaryChange: e.CanaryChange,
		}
	}
	return out
}

// DeliveredOf is one message made available from mailbox with parts (the
// request's effective parts): when it arrived, and, of the parts given, its
// senders and subject; never its text or an attachment's name or content.
func DeliveredOf(mailbox string, d *inbox.Disclosed, parts []contracts.Part) Delivered {
	out := Delivered{
		Mailbox: mailbox, Message: d.ID, Received: d.Message.ReceivedAt,
		Parts: slices.Clone(parts), Model: ModelOf(d.Decided),
	}
	if slices.Contains(parts, contracts.PartSender) {
		out.From = slices.Clone(d.Message.From)
	}
	if slices.Contains(parts, contracts.PartSubject) && d.Message.Subject != nil {
		subject := *d.Message.Subject
		out.Subject = &subject
	}
	for _, c := range d.Rules {
		out.Cleared = append(out.Cleared, Cleared{Rule: c.Rule, At: c.At, Model: ModelOf(c.Decided)})
	}
	// What each rule removed, and the evidence it rests on; never the
	// content (ADR 0040, "Receipts"). The sender and the subject above are
	// those that left.
	for _, r := range d.Removed {
		out.Removed = append(out.Removed, Removal{Rule: r.Rule, Removed: r.Removed, Model: ModelOf(r.Decided)})
	}
	return out
}

// HeldOf is one message of mailbox a rule held: by identifier, with the rule,
// how, and the evidence the hold rests on (none for an exact, undecided or
// recovered hold).
func HeldOf(mailbox string, w *inbox.Withheld) Held {
	return Held{Mailbox: mailbox, Message: w.ID, Rule: w.Rule, Why: string(w.Why), Model: ModelOf(w.Decided)}
}

// RefusedHeldOf is the refusal of a read or an attachment of a message a
// rule held: the vendor is told not_found, the receipt how and why.
func RefusedHeldOf(mailbox string, w *inbox.Withheld) *Refused {
	h := HeldOf(mailbox, w)
	return &Refused{Reason: RefusedHeld, Mailbox: mailbox, Message: w.ID, Held: &h}
}

// largestAt is the instant whose JSON is the longest Write can set (every
// digit of the nanoseconds written): Size counts At at it.
var largestAt = time.Date(9999, 12, 31, 23, 59, 59, 999999999, time.UTC)

// Size is the length of r's document as Write would store it, its V, ID and
// At counted at their largest: a receipt whose Size is at most
// MaxDocumentBytes is never refused as too large.
func Size(r *Receipt) (int, error) {
	doc := *r
	doc.V, doc.ID, doc.At = Version, IDPrefix+strings.Repeat("f", IDLength-len(IDPrefix)), largestAt
	b, err := json.Marshal(&doc)
	if err != nil {
		return 0, fmt.Errorf("receipts: %w", err)
	}
	return len(b), nil
}

// Page builds a search's receipt message by message, counting the whole
// document as Write will store it, and takes no message that would put it
// past MaxDocumentBytes (ADR 0031, finding 10): the caller ends the vendor's
// page before the first message Add refuses, and its cursor continues from
// the last one Add took, so nothing is skipped, nothing recorded is cut, and
// no evidence is dropped.
type Page struct {
	r    Receipt
	size int
}

// NewPage starts a page's receipt from head: who asked, the formulas, the
// rules. Head past MaxDocumentBytes on its own is ErrTooLarge.
func NewPage(head *Receipt) (*Page, error) {
	n, err := Size(head)
	if err != nil {
		return nil, err
	}
	if n > MaxDocumentBytes {
		return nil, ErrTooLarge
	}
	p := &Page{r: copyReceipt(*head), size: n}
	return p, nil
}

// Add records one message of mailbox the discloser considered, made
// available with parts or held, and says whether it fit: false leaves the
// receipt as it was, and the page ends before this message.
func (p *Page) Add(mailbox string, parts []contracts.Part, o inbox.Outcome) (bool, error) {
	var (
		elem  any
		field string
		empty bool
	)
	switch {
	case o.Disclosed != nil && o.Withheld == nil:
		d := DeliveredOf(mailbox, o.Disclosed, parts)
		elem, field, empty = d, "delivered", len(p.r.Delivered) == 0
	case o.Withheld != nil && o.Disclosed == nil:
		h := HeldOf(mailbox, o.Withheld)
		elem, field, empty = h, "held", len(p.r.Held) == 0
	default:
		return false, errors.New("receipts: an outcome is either made available or held")
	}
	b, err := json.Marshal(elem)
	if err != nil {
		return false, fmt.Errorf("receipts: %w", err)
	}
	// Every field after V is written as ,"name":value; a list's first
	// element brings its name and brackets, any later one a comma.
	grow := len(b) + 1
	if empty {
		grow = len(`,"`+field+`":[]`) + len(b)
	}
	if p.size+grow > MaxDocumentBytes {
		return false, nil
	}
	switch v := elem.(type) {
	case Delivered:
		p.r.Delivered = append(p.r.Delivered, v)
	case Held:
		p.r.Held = append(p.r.Held, v)
	}
	p.size += grow
	return true, nil
}

// Size is the receipt's length as Write would store it at its largest.
func (p *Page) Size() int { return p.size }

// Receipt is the page's receipt, for Write.
func (p *Page) Receipt() *Receipt {
	r := copyReceipt(p.r)
	return &r
}

// copyFormula is f with its groups and conditions of its own, so a receipt's
// formula and the canonical one it came from never change each other.
func copyFormula(f contracts.Formula) contracts.Formula {
	f.All = slices.Clone(f.All)
	for i := range f.All {
		f.All[i].Any = slices.Clone(f.All[i].Any)
	}
	return f
}

// copyPtr is a pointer to a copy of *p, or nil.
func copyPtr[T any](p *T) *T {
	if p == nil {
		return nil
	}
	v := *p
	return &v
}

// copyHeld is h with its model answers of its own.
func copyHeld(h Held) Held { h.Model = slices.Clone(h.Model); return h }

// copyReceipt is r with every slice and pointer it holds copied, so a page,
// the head it began from and every receipt it returns share nothing: editing
// one never changes another's evidence or the page's count of bytes.
func copyReceipt(r Receipt) Receipt {
	r.Formulas = slices.Clone(r.Formulas)
	for i := range r.Formulas {
		if r.Formulas[i].Formula != nil {
			f := copyFormula(*r.Formulas[i].Formula)
			r.Formulas[i].Formula = &f
		}
	}
	r.Rules = slices.Clone(r.Rules)
	r.Delivered = slices.Clone(r.Delivered)
	for i := range r.Delivered {
		d := &r.Delivered[i]
		d.Parts = slices.Clone(d.Parts)
		d.From = slices.Clone(d.From)
		d.Subject = copyPtr(d.Subject)
		d.Attachment = copyPtr(d.Attachment)
		d.Model = slices.Clone(d.Model)
		d.Cleared = slices.Clone(d.Cleared)
		for j := range d.Cleared {
			d.Cleared[j].Model = slices.Clone(d.Cleared[j].Model)
		}
	}
	r.Held = slices.Clone(r.Held)
	for i := range r.Held {
		r.Held[i] = copyHeld(r.Held[i])
	}
	r.Refused = copyPtr(r.Refused)
	if r.Refused != nil && r.Refused.Held != nil {
		h := copyHeld(*r.Refused.Held)
		r.Refused.Held = &h
	}
	r.Consent = copyPtr(r.Consent)
	if r.Consent != nil {
		r.Consent.Sentences = slices.Clone(r.Consent.Sentences)
		r.Consent.Parts = slices.Clone(r.Consent.Parts)
		r.Consent.Mailboxes = slices.Clone(r.Consent.Mailboxes)
	}
	r.Ended = copyPtr(r.Ended)
	return r
}
