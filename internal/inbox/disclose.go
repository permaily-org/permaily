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
	// Rules apply before the formulas, on every message, each read for a
	// rule (a formula of it True or Unknown, query.ForRule, applies it): a
	// message a rule that holds applies to does not leave; then every rule
	// that removes (rules.Remove) and applies takes its details out of the
	// message's one view, and the formulas read what is left (ADR 0040,
	// "The order"). A rule's value that would rest on a model's answer, and
	// which sentences a removal's topics keep, are decided once and
	// committed through Decisions before anything is asked (ADR 0031,
	// "Holds that cannot be undone").
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
	// model's answer, and when, with the evidence their record keeps (so a
	// decision made by an earlier request or a consent preview is recorded
	// with what it rested on, N4); a rule whose exact formulas were all
	// False and which needed no model is not listed.
	Rules []RuleCleared
	// Removed is what each of the person's rules that remove details took
	// out of it (ADR 0040), in the order they applied; empty when none did.
	// Message is as it left, after them.
	Removed []RemovedBy
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
	// Decided are the model's answers, or its kept refusals, the hold rests
	// on: from this request's ask, or, for a decision committed by an
	// earlier request, as its record keeps them (Decision.Evidence, ADR
	// 0031, N4); empty for an exact hold, an undecided one and a recovered
	// one.
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
	// HeldRecovered: the rule's decision was committed Held by whoever found
	// it Pending past PendingGrace: its settlement was lost, and no answer
	// of the model can be recovered for it, which the record says instead
	// of naming one (ADR 0031, N4). It never changes either.
	HeldRecovered HeldWhy = "recovered"
	// HeldUndecided: no decision could be committed or used now (the model
	// off or at a limit, an answer that could not be kept and is left
	// Pending, a decision another request began and has not settled, a
	// record that cannot be read, a day with no room). Nothing final was
	// decided: a later request may decide it, or recover it Held.
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
// before anything is asked, Settle after as SettleAfterAsk says, a message
// leaving only on a decision Settle or Begin returned Cleared, with the
// evidence it returned); and only the parts asked, read
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
// begun, in a block of the strict anchored records of the mailbox's day
// (RecordRuleDecisions, DecisionBlocks), before the model is asked anything
// for it or a kept answer is used for it, and settled Held or Cleared after,
// with the evidence it rests on, which every later Begin answers with it
// (N4); the first settlement is final. A begun decision whose settlement is
// lost (a crash, a write refused at every try, an answer received and not
// kept, N1) is settled Held, Recovered, by the next request that finds it
// older than PendingGrace: it can never be settled Cleared again. Every read
// is strict and made now: an absence or a pending decision is never answered
// from an earlier read (finding 2); a final decision may be.
//
// Implemented once, by BlockDecisions over a DecisionStore (ADR 0031,
// "Changed after the third design review"): services/mailboxes gives it its
// strict anchored records, sealed (pull request 4); inbox/disclosetest gives
// it memory and unsealed strict records, and has the contract every store
// passes.
type Decisions interface {
	// Begin reads the blocks of ids' days and, for every id with no
	// decision of rule, commits one Pending (one write per block changed);
	// what it answers is each id's decision as it is after the write:
	// Begun for those this call began, with their Attempt, which the caller
	// may ask the model for and must settle; Pending for one begun by
	// another request (At says when); Held or Cleared when final, with their
	// Evidence (or Recovered); Full when its day has no room, or none for
	// now (its tail waits for its Pending to settle); Expired when its message
	// has expired by this enclave's clock (nothing stored). An error is a
	// block that cannot be read or written: every id of its day is held,
	// undecided.
	Begin(ctx context.Context, b Box, rule rules.Ref, ids []DecisionKey) (map[string]Decision, error)
	// Settle commits the outcome of what this call's caller began, as
	// SettleAfterAsk says (Held or Cleared with the evidence they rest on,
	// Held too on Unknown with any answer received, or Abandoned only on
	// positive evidence that the model answered no part of it, which drops
	// the pending decision so a later request may decide; a decision
	// SettleAfterAsk leaves Pending is not given), each with the Attempt
	// Begin answered: Abandoned and Cleared change only that attempt's
	// Pending, and are answered Pending on another's,
	// and settles Held, Recovered, every pending decision older than
	// PendingGrace it is given with no evidence; it answers each id's final
	// state as it is after the write, which may be Held where Cleared was
	// asked (another request settled it first). Only an id answered
	// Cleared may leave.
	Settle(ctx context.Context, b Box, rule rules.Ref, settle map[string]Settlement) (map[string]Decision, error)
}

// DecisionKey is a message a decision is for, and when the message
// expires (the decision is dropped from later versions after it).
type DecisionKey struct {
	ID        string
	ExpiresAt time.Time
}
