// Package receipts is the sealed record of what left (ADR 0019, "Receipts";
// how in ADR 0031): for every request of a connection that reads mail, and
// for every refusal of one, an entry sealed under a key from the root secret,
// bound to where it is stored, written once and before the answer, and kept
// for as long as the account exists. Each is then named by a link of its
// account's chain (chain.go, ADR 0037, which brings step 8's chaining
// forward), before Write returns, without rewriting any entry; the
// activity screen of step 8 shows them in the chain's order.
//
// A receipt says what was prepared for a vendor and released to the
// connection's answer: written before the answer starts, it records what was
// made available, not that the vendor's side received it (ADR 0031, "What a
// receipt proves").
//
// This file is the seam, committed with the design and revised after its
// review; ADR 0030's pull request 5 writes the service. It knows nothing of
// HTTP.
package receipts

import (
	"context"
	"errors"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/rules"
)

// MaxDocumentBytes bounds one receipt's JSON before it is sealed (finding
// 10): sealed as text it grows by a third and must fit, with the entry's
// other attributes, in one DynamoDB item of 400 KiB. The connections
// service builds a search's receipt message by message and ends the page
// before the message that would pass it, so the vendor's cursor continues
// from the last message recorded and nothing is skipped; Write refuses a
// document past it (ErrTooLarge) rather than cut it. One message's part of
// a receipt is bounded by what a stored message may hold (ADR 0026), far
// below this.
const MaxDocumentBytes = 256 << 10

// ErrTooLarge is a receipt past MaxDocumentBytes: nothing is written, and
// nothing of its request leaves.
var ErrTooLarge = errors.New("receipts: the receipt is larger than one entry holds")

// Version is the version of a receipt's sealed document. Step 8 reads every
// version it finds; a new version is written only with a new field, never a
// changed one. Version 2 adds KindRefusals and its Refusals; a version 1
// document reads as it was written.
const Version = 2

// Partition is where an account's receipts are: one partition per account,
// deleted with it (store.Entries.DeletePartition, when the account is
// removed). The key of each is its ID.
func Partition(account string) string { return "receipts#" + account }

// Purpose names the key that seals receipts (keys.Derive).
const Purpose = "permaily receipts v1"

// Kind is what a receipt records.
type Kind string

const (
	KindSearch     Kind = "search"     // a page of a search: what left, what a rule held
	KindRead       Kind = "read"       // one message read whole
	KindAttachment Kind = "attachment" // one attachment
	KindRefused    Kind = "refused"    // a request for mail that got none: a held message, one outside the scope, a part not approved, the model unavailable, too late to release
	KindConsent    Kind = "consent"    // the person allowed or refused a request, or renewed a connection: a connection made or replaced, or a device added
	KindEnded      Kind = "ended"      // a connection or one of its devices ended, and why
	// KindRefusals counts, in one receipt for a connection's window, the
	// refusals past the ones the window records each in its own refused
	// receipt (Refusals; version 2).
	KindRefusals Kind = "refusals"
)

// Receipt is one entry's sealed document. Everything a receipt names is as it
// was when the request was answered.
type Receipt struct {
	V    int       `json:"v"`
	ID   string    `json:"id"` // "rcp_" and a time-ordered identifier: its key
	At   time.Time `json:"at"`
	Kind Kind      `json:"kind"`
	// Connection, Device, ClientID, Vendor and Resource say who asked;
	// Connection is empty for a refused consent, which made none.
	Connection string             `json:"connection,omitempty"`
	Device     string             `json:"device,omitempty"`
	ClientID   string             `json:"client_id"`
	Vendor     string             `json:"vendor"`
	Resource   contracts.Resource `json:"resource"`
	// Formulas are every formula evaluated, by role and canonical
	// identifier; the vendor's search with its canonical form, which only
	// the receipt keeps.
	Formulas []Formula `json:"formulas,omitempty"`
	// Rules are the rules that applied first, by identifier and version.
	Rules     []rules.Ref `json:"rules,omitempty"`
	Delivered []Delivered `json:"delivered,omitempty"`
	Held      []Held      `json:"held,omitempty"`
	Refused   *Refused    `json:"refused,omitempty"`
	Consent   *Consent    `json:"consent,omitempty"`
	Ended     *Ended      `json:"ended,omitempty"`
	Refusals  *Refusals   `json:"refusals,omitempty"`
	// A receipt holds nothing of its chain: the link that chains it names
	// it by identifier and digest (ADR 0037), so a receipt's document is
	// the same chained or not, and one written before the chain is adopted
	// as it is.
}

// FormulaRole says what a formula was to the request.
type FormulaRole string

const (
	RoleConsented FormulaRole = "consented" // the vendor's query the person consented to
	RoleMailboxes FormulaRole = "mailboxes" // the mailboxes the connection reads
	RoleSince     FormulaRole = "since"     // the mail since
	RoleSearch    FormulaRole = "search"    // the vendor's search of this request
)

// Formula is one formula evaluated.
type Formula struct {
	Role FormulaRole `json:"role"`
	ID   string      `json:"id"`
	// Formula is the canonical form: kept for a search, and in a consent
	// for every formula consented, so the record of a connection's scope
	// outlives the connection's own record (choice 22).
	Formula *contracts.Formula `json:"formula,omitempty"`
}

// Delivered is one message made available: where it was, which parts, and,
// of the parts given, its senders and subject, so the person can still see
// what left once the message is deleted. Never its text or an attachment.
type Delivered struct {
	Mailbox  string                  `json:"mailbox"`
	Message  string                  `json:"message"`
	Received time.Time               `json:"received"`
	Parts    []contracts.Part        `json:"parts"`
	From     []contracts.MailAddress `json:"from,omitempty"`
	Subject  *string                 `json:"subject,omitempty"`
	// Attachment is the index of the attachment that left (KindAttachment).
	Attachment *int `json:"attachment,omitempty"`
	// Model are the model's answers the formulas' value rested on, and
	// Cleared the rules decided Cleared for it on the model's answers, as
	// the discloser returned them (inbox.Disclosed): a rule's from the
	// decision's own record, so a decision made by an earlier request or a
	// consent preview is recorded with what it rested on, never with an
	// answer asked again (ADR 0031, N4).
	Model   []Model   `json:"model,omitempty"`
	Cleared []Cleared `json:"cleared,omitempty"`
}

// Cleared is a rule committed Cleared for a message (ADR 0031).
type Cleared struct {
	Rule  rules.Ref `json:"rule"`
	At    time.Time `json:"at"`
	Model []Model   `json:"model,omitempty"`
}

// Held is one message a rule held: where it was, which rule and how;
// nothing of it, which did not leave.
type Held struct {
	Mailbox string    `json:"mailbox"`
	Message string    `json:"message"`
	Rule    rules.Ref `json:"rule"`
	// Why is inbox.HeldWhy: "exact", "decided", "recovered" (a decision
	// whose settlement was lost, held with no answer to name) or
	// "undecided". Model is the evidence of a decided hold, from this
	// request's ask or from the decision's record (N4).
	Why   string  `json:"why"`
	Model []Model `json:"model,omitempty"`
}

// Model is one answer of the model, or its kept refusal, a decision rested
// on (ADR 0028), as the Asker used it (inbox.Evidence): its probability, its
// model, prompt version and the canary's days (UTC, YYYY-MM-DD). Never the
// hash of the content it was asked about.
type Model struct {
	Var           contracts.Variable `json:"var"`
	Op            contracts.Operator `json:"op"`
	Topic         string             `json:"topic"`
	P             float64            `json:"p"`
	Refused       bool               `json:"refused,omitempty"`
	ByRule        bool               `json:"by_rule,omitempty"`
	Name          string             `json:"model,omitempty"`
	Prompt        string             `json:"prompt,omitempty"`
	CanaryChecked string             `json:"canary_checked,omitempty"`
	CanaryChange  string             `json:"canary_change,omitempty"`
}

// RefusedReason is why a request for mail got none.
type RefusedReason string

const (
	RefusedHeld             RefusedReason = "held"           // a rule holds the message (Held says which and how)
	RefusedOutsideScope     RefusedReason = "outside_scope"  // the message is not one the connection reads, or does not exist
	RefusedPartNotGiven     RefusedReason = "part_not_given" // an attachment without PartAttachments
	RefusedFormula          RefusedReason = "invalid_formula"
	RefusedFormulaPart      RefusedReason = "formula_part_not_approved" // a search on what the request's parts do not give
	RefusedModelUnavailable RefusedReason = "model_unavailable"         // nothing left: the model could not be asked (the owner's decision 9)
	RefusedTooLarge         RefusedReason = "attachment_too_large"
	// RefusedInvalidBody: the adapter could not read the request of a
	// valid token (a body that is not one JSON object of the operation, a
	// JSON-RPC call it cannot read), so it never reached the service
	// (connections.API.Refuse; PR #131's review, finding 2).
	RefusedInvalidBody RefusedReason = "invalid_body"
	// The refusals of the connections service of a request a valid token
	// attributed that are none of the above (PR #136's review, finding 8):
	// a search's limit or cursor it cannot use, a connection asking more
	// often than its limits allow, and the storage or the authority that
	// could not be read in time, so the vendor is answered 503.
	RefusedInvalidLimit  RefusedReason = "invalid_limit"
	RefusedInvalidCursor RefusedReason = "invalid_cursor"
	RefusedRateLimited   RefusedReason = "rate_limited"
	RefusedUnavailable   RefusedReason = "unavailable"
	// RefusedConnectionEnded: the authority of a request a valid token
	// attributed no longer allows it when the service reads it before
	// preparing anything (PR #136's second review, finding 4): the
	// connection or its device revoked or ended, the account closing, or
	// its record or its device's scope gone or not opening; the vendor is
	// answered invalid_token. A record that could not be read is
	// RefusedUnavailable.
	RefusedConnectionEnded RefusedReason = "connection_ended"
	// RefusedTooLate: the answer of Receipt was prepared and recorded, and
	// then not released (the grant's deadline passed or this enclave
	// revoked it, finding 5). The earlier receipt stays: a receipt may
	// record more than left, never less.
	RefusedTooLate RefusedReason = "too_late"
)

// Refused is a request for mail that got none. Only requests attributed to a
// connection by a valid token are recorded (choice 34): a refusal before
// that is counted on /status, never written to an account guessed from what
// the vendor sent.
type Refused struct {
	Reason  RefusedReason `json:"reason"`
	Mailbox string        `json:"mailbox,omitempty"`
	Message string        `json:"message,omitempty"`
	Held    *Held         `json:"held,omitempty"`
	// Receipt is the receipt a RefusedTooLate is about.
	Receipt string `json:"receipt,omitempty"`
}

// Refusals is a summary of one connection's refusals in one enclave (ADR
// 0031, "Refusals past a window's bound"): of the refused requests of the
// connection from From to Until, Recorded were written each in its own
// refused receipt, and the others are only counted here, by operation and
// reason. It names no message and no receipt, and no device: the requests it
// counts may come from any of the connection's devices. It covers one
// window, or more only when an earlier window's summary could not be
// written and was carried into the next.
type Refusals struct {
	From     time.Time      `json:"from"`
	Until    time.Time      `json:"until"`
	Recorded int            `json:"recorded"`
	Counts   []RefusalCount `json:"counts"`
}

// RefusalCount is how many refusals of one operation and reason a summary
// counts. Operation is the request's: "search", "read" or "attachment".
type RefusalCount struct {
	Operation string        `json:"operation"`
	Reason    RefusedReason `json:"reason"`
	Count     int           `json:"count"`
}

// Consent is what the person was shown and decided (docs/legal.md,
// engineering item 2): the sentences, parts, mail since and end shown, the
// draft's mailboxes, the rules and the model's notice in force, what it did
// to the client's connection, and the decision. The receipt's Formulas hold
// every formula consented in full.
type Consent struct {
	Allowed     bool                       `json:"allowed"`
	Renewal     bool                       `json:"renewal,omitempty"`
	Change      contracts.ConnectionChange `json:"change,omitempty"`
	Sentences   []string                   `json:"sentences"`
	Parts       []contracts.Part           `json:"parts"`
	Since       contracts.MailSince        `json:"since"`
	EndsAt      time.Time                  `json:"ends_at,omitzero"`
	Mailboxes   []string                   `json:"mailboxes"`
	ModelNotice string                     `json:"model_notice"`
	Replaced    string                     `json:"replaced,omitempty"`
}

// Ended is a connection, or one device of it (the receipt's Device), that
// ended.
type Ended struct {
	Reason     contracts.EndReason `json:"reason"`
	ReplacedBy string              `json:"replaced_by,omitempty"`
}

// Writer writes receipts (ADR 0031, "When a receipt is written"). Write
// stores r, sealed, as a new entry (store.Entries.PutEntry at version 0, never
// an update), and returns only once it is stored: nothing of the request
// leaves before it does, and a request whose receipt cannot be written is
// answered 503 with nothing of the mail. It sets r.ID and r.At; it refuses
// r past MaxDocumentBytes (ErrTooLarge) and honours ctx's deadline, which
// the service sets to the grant's. A receipt that comes with its identity
// (r.ID from NewID, and r.At) is stored under it, and writing it again is
// stored once: a caller that did not learn a write's outcome makes it again
// rather than drawing a new receipt.
type Writer interface {
	Write(ctx context.Context, account string, r *Receipt) error
}

// RemoveAgainAfter is how long after an account's removal its receipts'
// partition is deleted once more (accounts' removal sweep), for a receipt
// whose write began before the removal and ended after it (choice 32): no
// request of a connection lasts longer.
const RemoveAgainAfter = 10 * time.Minute
