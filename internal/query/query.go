// Package query is the query language of connections and rules (ADR 0019,
// how in ADR 0027): reading a formula a vendor sends, normalising its values,
// simplifying it to its canonical form, deciding whether one formula fits
// inside another, the conjunction of an agent's search and its scope, the
// sentence a person reads, and the value of a formula on one message.
//
// It is pure: it needs nothing but its arguments, as internal/domain, and
// knows nothing of storage, keys, HTTP or a model. It is a package of its own,
// not part of domain, because it normalises text and domain names with
// golang.org/x/text and golang.org/x/net/idna, which domain may not import;
// depguard's query-is-pure rule lists what it may (.golangci.yml).
//
// This file is the seam, committed with the design: the types its pull
// requests and internal/inbox share. The functions are written by ADR 0027's
// pull requests 1 (Parse, Canonicalise, And, Contains, Eval, Resolve) and 2
// (Render); "The contract and the seams" there gives their signatures.
package query

import (
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
)

// Value is a condition's or a formula's value on one message, in Kleene's
// strong logic of three values: False < Unknown < True. A group is the
// greatest of its conditions' values, a formula the least of its groups'.
// Unknown is a condition whose data could not be read, or that a model
// decides and was not asked, did not answer, or answered inside its band of
// doubt (ADR 0027, "What a formula means").
type Value int8

const (
	False Value = iota
	Unknown
	True
)

// Mode says what Unknown becomes when a value is acted on: for a query (a
// connection's query, an agent's search) it delivers nothing; for a rule,
// every one of which protects, it holds back or removes. "When in doubt,
// nothing leaves" (ADR 0019).
type Mode int8

const (
	ForQuery Mode = iota // True delivers; Unknown and False do not
	ForRule              // True and Unknown apply the rule; False does not
)

// Canonical is a formula in its canonical form (ADR 0027, "Simplification"):
// every value normalised, every rewrite applied, the conditions and groups in
// their order. Only Canonicalise and And make one; a Canonical built by hand
// is not one. Never is the formula no message matches: Parse refuses it for a
// vendor (contracts.FormulaNeverMatches), and And may make it (the search then
// finds nothing).
type Canonical struct {
	Formula contracts.Formula
	// ID is "qry_" and 26 base32 characters of the SHA-256 of the canonical
	// JSON: the same formula has the same identifier in every enclave and
	// release that reads this version. It names the formula in receipts and
	// kept decisions; it is never logged.
	ID    string
	Never bool
}

// Sources are the parts of a stored message a value is read from, in the
// order evaluation reads them (ADR 0027, "Evaluation over stored mail").
type Sources uint8

const (
	// FromItem: the message item's plain fields and its identifier: when it
	// was received, its size, its mailbox. Always read first.
	FromItem Sources = 1 << iota
	// FromEntry: the sealed txt# entry: senders, subject, the first 16 KiB
	// of the text and whether it was cut there.
	FromEntry
	// FromMeta: the sealed meta of the item (inbox.StoredMeta).
	FromMeta
	// FromText: the text taken out of the raw message, read under the read
	// budget, when the first 16 KiB did not settle a condition on the body.
	FromText
)

// Message is what evaluation knows of one stored message. Have says which
// sources were read; a condition whose source was not read is Unknown, and
// Result.Needs says which source would settle it. Every field is as stored;
// Eval normalises the message's side as Parse normalises a value.
type Message struct {
	Have Sources

	Mailbox  string
	Received time.Time
	Size     int64

	From       []string // addresses of the From header, as stored
	Recipients []string // addresses of the To and Cc headers, as stored
	SignedBy   []string // the domain (d=) of each DKIM signature that passed at delivery
	Subject    string
	// Body is the text: the first 16 KiB from the entry, or up to
	// inbox.MessageTextMax from the raw message. BodyCut says the entry's
	// text was cut at 16 KiB (or, for an entry written before ADR 0027, may
	// have been), so a body condition it does not settle needs FromText.
	Body        string
	BodyCut     bool
	Attachments []Attachment
	// List is the identifier of the List-Id header. ListRecorded says the
	// message was stored by a delivery that records it (the meta's format
	// 2); for one stored before, every list condition is Unknown.
	List         []string
	ListRecorded bool

	// Partial: a parse limit was reached (inbox.StoredMeta.Partial), so a
	// list of addresses or attachments may be missing some: an absence is
	// Unknown. Unparsed: the header could not be read; every condition but
	// those on the item is Unknown.
	Partial  bool
	Unparsed bool
	// Confirmation: a forwarding confirmation of Gmail. It never matches a
	// query or a rule: it is Permaily's, and never leaves.
	Confirmation bool
}

// Attachment is one listed attachment: its name and media type as stored.
type Attachment struct {
	Name string
	Type string
}

// Result is a formula's value on one message, and what would change it.
type Result struct {
	Value Value
	// Needs are the sources not read yet that could change an Unknown value;
	// none when Value is True or False, or when only conditions a model
	// decides are Unknown.
	Needs Sources
	// Model are the conditions a model decides that an Unknown value still
	// depends on, by their place in the canonical formula (group, condition),
	// in its order. Step 4 asks the model of these, and no other.
	Model []Place
}

// Place is where a condition is in a canonical formula.
type Place struct {
	Group     int
	Condition int
}

// Context is what a sentence needs besides the formula: its language and
// the account's mailboxes, by identifier, so a condition on a mailbox names
// it (ADR 0027, "The sentence"). Every mailbox of the account is listed, so
// the sentence can tell whether a label is unique.
type Context struct {
	Lang      string
	Mailboxes map[string]MailboxName
}

// MailboxName is what a sentence may call a mailbox: its label when it is
// not empty and no other mailbox of the account has it, and otherwise its
// address.
type MailboxName struct {
	Label   string
	Address string
}
