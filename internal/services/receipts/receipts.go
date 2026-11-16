// Package receipts is the sealed record of what left (ADR 0019, "Receipts";
// how in ADR 0031): for every request of a connection that reads mail, and
// for every refusal of one, an entry sealed under a key from the root secret,
// bound to where it is stored, written once and before the answer, and kept
// for as long as the account exists. Step 8 chains them by hash, anchors the
// head and shows them on the activity screen, over these very entries in
// their keys' order, without rewriting any (ADR 0031, "How step 8 extends
// it").
//
// This file is the seam, committed with the design; ADR 0030's pull request 5
// writes the service. It knows nothing of HTTP.
package receipts

import (
	"context"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
)

// Version is the version of a receipt's sealed document. Step 8 reads every
// version it finds; a new version is written only with a new field, never a
// changed one.
const Version = 1

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
	KindRefused    Kind = "refused"    // a request for mail that got none: a held message, one outside the scope, a part not approved
	KindConsent    Kind = "consent"    // the person allowed or refused a request; a connection made or replaced
	KindEnded      Kind = "ended"      // a connection ended, and why
)

// Receipt is one entry's sealed document. Everything a receipt names is as it
// was when the request was answered.
type Receipt struct {
	V    int       `json:"v"`
	ID   string    `json:"id"` // "rcp_" and a time-ordered identifier: its key
	At   time.Time `json:"at"`
	Kind Kind      `json:"kind"`
	// Connection, ClientID, Vendor and Resource say who asked; Connection is
	// empty for a refused consent, which made none.
	Connection string             `json:"connection,omitempty"`
	ClientID   string             `json:"client_id"`
	Vendor     string             `json:"vendor"`
	Resource   contracts.Resource `json:"resource"`
	// Formulas are every formula evaluated, by role and canonical
	// identifier; the vendor's search with its canonical form, which only
	// the receipt keeps.
	Formulas []Formula `json:"formulas,omitempty"`
	// Rules are the rules that applied first, by identifier and version.
	Rules     []Rule      `json:"rules,omitempty"`
	Delivered []Delivered `json:"delivered,omitempty"`
	Held      []Held      `json:"held,omitempty"`
	Refused   *Refused    `json:"refused,omitempty"`
	Consent   *Consent    `json:"consent,omitempty"`
	Ended     *Ended      `json:"ended,omitempty"`
	// Chain is left for step 8: nothing writes it in step 5, and step 8
	// chains these entries without it (ADR 0031).
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
	// Formula is the canonical form, kept for a search only (the others are
	// the connection's).
	Formula *contracts.Formula `json:"formula,omitempty"`
}

// Rule is a rule that applied, by identifier and version.
type Rule struct {
	ID      string `json:"id"`
	Version int    `json:"version"`
}

// Delivered is one message that left: where it was, which parts, and, of the
// parts that left, its senders and subject, so the person can still see what
// left once the message is deleted. Never its text or an attachment.
type Delivered struct {
	Mailbox  string                  `json:"mailbox"`
	Message  string                  `json:"message"`
	Received time.Time               `json:"received"`
	Parts    []contracts.Part        `json:"parts"`
	From     []contracts.MailAddress `json:"from,omitempty"`
	Subject  *string                 `json:"subject,omitempty"`
	// Attachment is the index of the attachment that left (KindAttachment).
	Attachment *int `json:"attachment,omitempty"`
	// Model are the model's answers the formulas' value rested on.
	Model []Model `json:"model,omitempty"`
}

// Held is one message a rule held: where it was and which rule; nothing of
// it, which did not leave.
type Held struct {
	Mailbox string  `json:"mailbox"`
	Message string  `json:"message"`
	Rule    string  `json:"rule"`
	Version int     `json:"version"`
	ByModel bool    `json:"by_model"`
	Model   []Model `json:"model,omitempty"`
}

// Model is one answer of the model a decision rested on (ADR 0028).
type Model struct {
	Var           contracts.Variable `json:"var"`
	Op            contracts.Operator `json:"op"`
	Topic         string             `json:"topic"`
	Name          string             `json:"model"`
	Prompt        string             `json:"prompt"`
	CanaryChecked time.Time          `json:"canary_checked,omitzero"`
	CanaryChange  time.Time          `json:"canary_change,omitzero"`
}

// RefusedReason is why a request for mail got none.
type RefusedReason string

const (
	RefusedHeld         RefusedReason = "held"           // a rule holds the message
	RefusedOutsideScope RefusedReason = "outside_scope"  // the message is not one the connection reads, or does not exist
	RefusedPartNotGiven RefusedReason = "part_not_given" // an attachment without PartAttachments
	RefusedFormula      RefusedReason = "invalid_formula"
)

// Refused is a request for mail that got none.
type Refused struct {
	Reason  RefusedReason `json:"reason"`
	Mailbox string        `json:"mailbox,omitempty"`
	Message string        `json:"message,omitempty"`
}

// Consent is what the person was shown and decided (docs/legal.md,
// engineering item 2): the sentences, parts, mail since and end shown, the
// rules and the model's notice in force, and the decision.
type Consent struct {
	Allowed     bool                `json:"allowed"`
	Sentences   []string            `json:"sentences"`
	Parts       []contracts.Part    `json:"parts"`
	Since       contracts.MailSince `json:"since"`
	EndsAt      time.Time           `json:"ends_at,omitzero"`
	Mailboxes   []string            `json:"mailboxes"`
	ModelNotice string              `json:"model_notice"`
	Replaced    string              `json:"replaced,omitempty"`
}

// Ended is a connection that ended.
type Ended struct {
	Reason     contracts.EndReason `json:"reason"`
	ReplacedBy string              `json:"replaced_by,omitempty"`
}

// Writer writes receipts (ADR 0031, "When a receipt is written"). Write
// stores r, sealed, as a new entry (store.Entries.PutEntry at version 0, never
// an update), and returns only once it is stored: nothing of the request
// leaves before it does, and a request whose receipt cannot be written is
// answered 503 with nothing of the mail. It sets r.ID and r.At.
type Writer interface {
	Write(ctx context.Context, account string, r *Receipt) error
}
