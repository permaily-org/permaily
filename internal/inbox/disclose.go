package inbox

import (
	"context"
	"errors"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/query"
	"github.com/permaily-org/permaily/internal/rules"
)

// What leaves for a connection (ADR 0030, "What a read gives"; ADR 0031). This
// file is the seam, committed with the design; ADR 0030's pull request 4
// implements Discloser on the store (disclose.go's implementation, beside
// match.go) and services/mailboxes implements Holds.

// Disclosure is what one request of a connection may be given from one
// mailbox: the formulas evaluated together for a query (the consented
// scope, the mailboxes, the mail since, and the vendor's search), the rules
// that apply first, each read for a rule, and the parts the connection
// reads.
type Disclosure struct {
	// Formulas are canonical and never simplified together; a message
	// leaves only when the least of their values is True (query.ForQuery).
	Formulas []*query.Canonical
	// Rules apply before the formulas, on every message: a message one of
	// them applies to (a formula of it True or Unknown, query.ForRule) is
	// held and does not leave.
	Rules []rules.Rule
	Parts []contracts.Part
	// Account is whose share of the model's spend the asking counts in.
	Account string
}

// Disclosed is one message that left, with the parts the connection reads,
// and what decided it, for the record (receipts): the atoms a model decided
// and what of the model each answer came from.
type Disclosed struct {
	Message contracts.VendorMessage
	ID      string // the message's identifier in its mailbox
	// Decided are the model's answers the formulas' value rested on.
	Decided []Decided
}

// Decided is one model answer a value rested on (ADR 0028, "Who calls what,
// later": receipts name them).
type Decided struct {
	Atom          query.Atom
	Model, Prompt string
	CanaryChecked time.Time
	CanaryChange  time.Time
}

// Withheld is one message the formulas matched that a rule held: never told
// to the vendor, written in the record.
type Withheld struct {
	ID      string
	Rule    string
	Version int
	// ByModel says the rule's value rested on a model's answer or on its
	// absence (no key, a limit, a failure, an answer in doubt); otherwise
	// exact conditions held it.
	ByModel bool
	Decided []Decided
}

// DisclosePage is a page of what left from one mailbox, newest first. Next
// is the last message considered, as Matcher's is, for the caller to seal in
// the vendor's cursor; it never reaches the vendor as it is.
type DisclosePage struct {
	Messages []Disclosed
	Held     []Withheld
	Next     string
}

// Discloser gives a connection what it may have of one opened mailbox (ADR
// 0030, "What a read gives"). It evaluates as Matcher does, with its bounds
// and its Asker (WithAsker), and also: every message's rules first, read for
// a rule, through the same Asker, the kept answers shared with queries; the
// rule holds of Holds before any message a model's answer would let out; and
// only the parts asked, read at the cost Matcher would (the entry only for
// the text, the raw message only for a text the entry cut or an
// attachment). It checks nothing of who asks: the connections service did,
// and opened the mailbox.
type Discloser interface {
	// Page is a page of the messages that leave, below before ("" for the
	// newest), at most limit (1 to 50).
	Page(ctx context.Context, b Box, d Disclosure, before string, limit int) (*DisclosePage, error)
	// Message is one message, evaluated again now (a rule or a formula may
	// have changed since a page listed it): ErrWithheld, with the Withheld
	// set, when a rule holds it; contracts.ErrNotFound when it does not
	// match, does not exist or is a forwarding confirmation, the same for
	// each. Its text comes with PartBody, at most MessageTextMax.
	Message(ctx context.Context, b Box, d Disclosure, id string) (*Disclosed, *Withheld, error)
	// Attachment is one attachment's name, type and bytes, under the same
	// evaluation and the read budget; contracts.ErrPartNotApproved without
	// PartAttachments.
	Attachment(ctx context.Context, b Box, d Disclosure, id string, index int) (*Disclosed, string, string, []byte, error)
}

// ErrWithheld is a message a rule holds: the caller records it and tells the
// vendor contracts.ErrNotFound.
var ErrWithheld = errors.New("inbox: a rule holds the message")

// DiscloseMaxLimit bounds a page of what leaves.
const DiscloseMaxLimit = 50

// Holds are the holds of rules that protect, kept so they cannot be undone by
// writing to the table (ADR 0031, "Holds that cannot be undone"; ADR 0028's
// prerequisite for step 7, met here). Once a rule held a message on a
// model's kept answer, the hold is committed in the mailbox's strict anchored
// record of holds for the month the message arrived; a later evaluation that
// would let the message out on a model's answer reads it first and holds the
// message again. A record that cannot be read (lost, forked, put back, or past
// its bound) holds every message of its month whose rule value rests on a
// model.
type Holds interface {
	// Held says whether a hold of rule (at any version) is committed for
	// the message; an error is a record that cannot be read, which the
	// caller takes as held.
	Held(ctx context.Context, b Box, id, rule string) (bool, error)
	// Hold commits a hold, before the page that decided it is answered;
	// committing one already there changes nothing. expires is the
	// message's, after which the hold is dropped from later versions.
	Hold(ctx context.Context, b Box, id, rule string, version int, expires time.Time) error
}

// HoldsMax bounds the holds of one mailbox's month; past it Hold fails and
// every message of that month whose rule value rests on a model is held.
const HoldsMax = 5000
