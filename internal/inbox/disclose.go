package inbox

import (
	"context"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/query"
	"github.com/permaily-org/permaily/internal/rules"
)

// What leaves for a connection (ADR 0030, "What a read gives"; ADR 0031). This
// file is the seam, committed with the design and revised after its review
// (ADR 0030 and 0031, "Changed after the design review (2026-11-16)"); ADR
// 0030's pull request 4 implements Discloser on the store (beside match.go)
// and services/mailboxes implements Decisions on strict anchored records.
// rule_decisions.go is the record those decisions are kept in, and its state
// machine, committed so pull request 4 and its fakes share one contract.

// Disclosure is what one request of a connection may be given from one
// mailbox: the formulas evaluated together for a query (the consented
// scope, the mailboxes, the mail since, the device's own scope when it is
// narrower, and the vendor's search), the rules that apply first, each read
// for a rule, and the parts the request reads.
type Disclosure struct {
	// Formulas are canonical and never simplified together; a message
	// leaves only when the least of their values is True (query.ForQuery).
	Formulas []*query.Canonical
	// Rules apply before the formulas, on every message: a message one of
	// them applies to (a formula of it True or Unknown, query.ForRule) is
	// held and does not leave. A rule's value that would rest on a model's
	// answer is decided once and committed through Decisions before
	// anything is asked (ADR 0031, "Holds that cannot be undone").
	Rules []rules.Rule
	// Parts are the effective parts: the token's scopes, the device's and
	// the connection's parts, intersected (ADR 0030, finding 7).
	Parts []contracts.Part
	// Account is whose share of the model's spend the asking counts in.
	Account string
	// Model says the model may be asked for this request: the connection
	// agreed to the model's notice in force and the guard lets it ask
	// (ADR 0030, the owner's decision 9; finding 18). Without it nothing is
	// asked, a rule's value that needs the model is Unknown, and the
	// message is held. The connections service refuses a request with
	// contracts.ErrModelUnavailable before it gets here when the guard says
	// the model is off or at a limit; Model false here covers a guard that
	// changes during the page.
	Model bool
}

// Disclosed is one message that leaves, with the parts the request reads,
// and what decided it, for the record (receipts).
type Disclosed struct {
	Message contracts.VendorMessage
	ID      string // the message's identifier in its mailbox
	// Decided are the model's answers the formulas' value rested on, as the
	// Asker returned them (Asked.Evidence), never read again.
	Decided []Decided
	// Rules are the rules that were decided Cleared for this message on a
	// model's answer, and when; a rule whose exact formulas were all False
	// and which needed no model is not listed.
	Rules []RuleCleared
}

// RuleCleared is a rule committed Cleared for a message.
type RuleCleared struct {
	Rule    rules.Ref
	At      time.Time
	Decided []Decided
}

// Decided is one model answer a value rested on (ADR 0028, "Who calls what,
// later": receipts name them), as Asked.Evidence gave it.
type Decided struct {
	Atom     query.Atom
	Evidence Evidence
}

// Withheld is one message a rule held: never told to the vendor, written in
// the record.
type Withheld struct {
	ID   string
	Rule rules.Ref
	// Why is how the rule held it.
	Why HeldWhy
	// Decided are the model's answers, or its kept refusal, the hold rested
	// on, when it rested on one this request saw; empty for an exact hold
	// and for a decision committed by an earlier request.
	Decided []Decided
}

// HeldWhy says how a rule held a message.
type HeldWhy string

const (
	// HeldExact: an exact formula of the rule was True, or Unknown (a text
	// that cannot be read); nothing of the model, nothing committed.
	HeldExact HeldWhy = "exact"
	// HeldDecided: the rule's decision for the message is committed Held,
	// by this request or an earlier one; it never changes.
	HeldDecided HeldWhy = "decided"
	// HeldUndecided: no decision could be committed or used now (the model
	// off or at a limit, an answer that could not be kept, a decision
	// another request began and has not settled, a record that cannot be
	// read or is full). Nothing was decided: a later request may decide it.
	HeldUndecided HeldWhy = "undecided"
)

// Outcome is one message as a request considered it: exactly one of
// Disclosed (it leaves) and Withheld (a rule holds it). A message the
// formulas do not match, that does not exist or is a forwarding
// confirmation is in neither: Message and Attachment answer
// contracts.ErrNotFound for it.
type Outcome struct {
	Disclosed *Disclosed
	Withheld  *Withheld
}

// ID is the message the outcome is about.
func (o Outcome) ID() string {
	switch {
	case o.Disclosed != nil:
		return o.Disclosed.ID
	case o.Withheld != nil:
		return o.Withheld.ID
	}
	return ""
}

// DisclosePage is a page of one mailbox, newest first: every message the
// formulas match, each either left or held, in the order considered, so the
// caller can cut it where the receipt's size bound falls (receipts
// .MaxDocumentBytes, finding 10) and continue below the last one it keeps
// without skipping any. Next is the last message considered, as Matcher's
// is, for the caller to seal in the vendor's cursor when it keeps the whole
// page; it never reaches the vendor as it is.
type DisclosePage struct {
	Items []Outcome
	Next  string
	// Model is what became of asking the model on the page (MatchModel's
	// values): the caller says "model_unavailable" to the vendor when it
	// is not MatchModelAsked and nothing left.
	Model contracts.MatchModel
}

// Attachment is one attachment's name, media type and bytes.
type Attachment struct {
	Name string
	Type string
	Data []byte
}

// Discloser gives a connection what it may have of one opened mailbox (ADR
// 0030, "What a read gives"). It evaluates as Matcher does, with its bounds
// and its Asker (WithAsker), and also: every message's rules first, read for
// a rule, through the same Asker (the kept answers shared with queries), a
// rule's value that rests on a model decided once through Decisions (Begin
// before anything is asked, Settle after, a message leaving only on a
// decision Settle or Begin returned Cleared); and only the parts asked, read
// at the cost Matcher would (the entry only for the text, the raw message
// only for a text the entry cut or an attachment). It checks nothing of who
// asks: the connections service did, and opened the mailbox. The consent
// screen's preview with the model uses it too (ADR 0031, "Where it
// applies"), so a decision shown there is the one a vendor later meets.
type Discloser interface {
	// Page is a page of the messages the formulas match, below before (""
	// for the newest), at most limit (1 to DiscloseMaxLimit) of them.
	Page(ctx context.Context, b Box, d Disclosure, before string, limit int) (*DisclosePage, error)
	// Message is one message, evaluated again now (a rule or a formula may
	// have changed since a page listed it): an Outcome, or
	// contracts.ErrNotFound when it does not match, does not exist or is a
	// forwarding confirmation, the same for each. Its text comes with
	// PartBody, at most MessageTextMax.
	Message(ctx context.Context, b Box, d Disclosure, id string) (Outcome, error)
	// Attachment is one attachment, under the same evaluation and the read
	// budget: the Outcome (held, with its evidence, like Message), and the
	// attachment only when it leaves; contracts.ErrPartNotApproved without
	// PartAttachments, contracts.ErrAttachmentTooLarge past
	// contracts.VendorAttachmentMax.
	Attachment(ctx context.Context, b Box, d Disclosure, id string, index int) (Outcome, *Attachment, error)
}

// DiscloseMaxLimit bounds a page of what leaves.
const DiscloseMaxLimit = 50

// Decisions keeps each protecting rule's decision for a message, so that no
// write to the table can undo one (ADR 0031, "Holds that cannot be
// undone"; ADR 0028's prerequisite for step 7, met here). A decision is
// begun, in a strict anchored record of the mailbox's day
// (RecordRuleDecisions), before the model is asked anything for it or a
// kept answer is used for it, and settled Held or Cleared after; the first
// settlement is final. A begun decision whose settlement is lost (a crash, a
// write refused at every try) is settled Held by the next request that finds
// it older than PendingGrace: it can never be settled Cleared again. Every
// read is strict and made now: an absence or a pending decision is never
// answered from an earlier read (finding 2); a final decision may be.
//
// Implemented by services/mailboxes (pull request 4) over the record of
// rule_decisions.go; inbox/disclosetest has the in-memory fake and the
// contract test both pass.
type Decisions interface {
	// Begin reads the records of ids' days and, for every id with no
	// decision of rule, commits one Pending (one write per day record);
	// what it answers is each id's decision as it is after the write:
	// Begun for those this call began, which the caller may ask the model
	// for and must settle; Pending for one begun by another request (At
	// says when); Held or Cleared when final; Full when its record has no
	// room. An error is a record that cannot be read or written: every id
	// of it is held, undecided.
	Begin(ctx context.Context, b Box, rule rules.Ref, ids []DecisionKey) (map[string]Decision, error)
	// Settle commits the outcome of what this call's caller began (Held,
	// Cleared, or Abandoned when it used no answer of the model, which
	// drops the pending decision so a later request may decide), and
	// settles Held every pending decision older than PendingGrace it is
	// given; it answers each id's final state as it is after the write,
	// which may be Held where Cleared was asked (another request settled
	// it first). Only an id answered Cleared may leave.
	Settle(ctx context.Context, b Box, rule rules.Ref, settle map[string]DecisionState) (map[string]Decision, error)
}

// DecisionKey is a message a decision is for, and when the message
// expires (the decision is dropped from later versions after it).
type DecisionKey struct {
	ID        string
	ExpiresAt time.Time
}
