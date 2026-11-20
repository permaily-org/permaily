// Package query is the query language of connections and rules (ADR 0019,
// how in ADR 0027): reading a formula a vendor sends, normalising its values,
// simplifying it to its canonical form, deciding whether one formula fits
// inside another, the sentence a person reads, and the value of formulas on
// one message.
//
// It is pure: it needs nothing but its arguments, as internal/domain, and
// knows nothing of storage, keys, HTTP or a model. It is a package of its own,
// not part of domain, because it normalises text and domain names with
// golang.org/x/text and golang.org/x/net/idna, which domain may not import;
// depguard's query-is-pure rule lists what it may (.golangci.yml).
//
// This file is the seam, committed with the design: the types its pull
// requests and internal/inbox share, and Classify, which maps a model's
// probability to a value and so is part of what a formula means. The other
// functions are written by ADR 0027's pull requests 1 (Parse, Canonicalise,
// Contains, Eval, Resolve) and 2 (Render); "The contract and the seams" there
// gives their signatures.
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
// their order. Only Canonicalise makes one; a Canonical built by hand is not
// one. Never is the formula no message matches: Parse refuses it for a vendor
// (contracts.FormulaNeverMatches), and Canonicalise returns it.
//
// Two canonical formulas are never simplified together: a connection's
// consented scope, a request accepted without fresh consent because it fits
// inside that scope, and an agent's search are each canonical on their own,
// and evaluated together by Eval, which takes the least of their values (ADR
// 0027, "What a connection widened without consent is evaluated as").
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
// order evaluation reads them (ADR 0027, "Evaluation over stored mail"). A
// message is classified only once FromItem and FromMeta are both read: until
// then Eval answers Unknown with them in Needs, whatever the other sources say.
type Sources uint8

const (
	// FromItem: the message item's identifier and partition: when it was
	// received and the mailbox it is in. Both are bound to the sealed meta
	// (the meta's AAD names the mailbox and the identifier), so they are
	// authenticated once FromMeta is read.
	FromItem Sources = 1 << iota
	// FromEntry: the sealed txt# entry, for the first 16 KiB of the text and
	// whether it was cut there. Its senders and subject are not used: the
	// meta's are.
	FromEntry
	// FromMeta: the sealed meta of the item (inbox.StoredMeta): senders,
	// recipients, subject, attachments, DKIM results, the list, the size as
	// delivered (format 2), and the flags Partial, Unparsed and Confirmation.
	FromMeta
	// FromText: the raw message, read under the read budget and opened, when
	// a body condition depends on a text the entry cut: its text, up to
	// inbox.MessageTextMax, and its length, which is the size when the meta
	// has none. It is never read for the size alone.
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
	// Size is the size of the message as delivered, from the sealed meta
	// (format 2), or the length of the raw message when it was read and
	// opened (FromText). SizeKnown is false otherwise, and every size
	// condition is then Unknown: the size stored beside the item is written
	// by whoever writes the table, and is never used.
	Size      int64
	SizeKnown bool

	// From and Recipients are the addresses of the From header and of the To
	// and Cc headers, as stored. Eval makes one decision per address, on the
	// whole address: one that does not normalise makes every condition over
	// it Unknown, on its address and on its domain alike.
	From       []string
	Recipients []string
	SignedBy   []string // the domain (d=) of each DKIM signature that passed at delivery
	Subject    string
	// Body is the text: the first 16 KiB from the entry, or up to
	// inbox.MessageTextMax from the raw message. BodyCut says the entry's
	// text was cut at 16 KiB (or, for an entry written before ADR 0027, may
	// have been): then every body condition the value depends on is Unknown,
	// and needs FromText, whatever the first 16 KiB hold.
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

// Atom is the identity of a condition a model decides, without its sign:
// its variable, its operator (contracts.OpAbout or contracts.OpMentions) and
// its normalised topic. Two conditions with one Atom are one question, asked
// once per message, and get one answer, wherever they appear and in whichever
// formula: that is what makes "identical conditions are equal" true in
// containment. A kept answer (step 4) is keyed by the message, a hash of the
// content the model saw, the Atom, and the model's and the prompt's versions;
// never by a formula.
type Atom struct {
	Var   contracts.Variable
	Op    contracts.Operator
	Topic string
}

// Answers are a model's probabilities for one message, by Atom. An Atom
// without an answer is Unknown and listed in Result.Model; a probability is
// read by Classify in the mode of the evaluation.
type Answers map[Atom]float64

// Result is the value on one message of the formulas evaluated together, and
// what would change it.
type Result struct {
	Value Value
	// Needs are the sources not read yet that could change an Unknown value;
	// none when Value is True or False, or when only conditions a model
	// decides are Unknown. FromText is named only for a body condition on a
	// text the entry cut, never for a size condition alone: the size of a
	// message stored before ADR 0027 stays Unknown unless its raw message is
	// read for its body anyway, which then makes the size known (ADR 0027,
	// "The exception of old messages").
	Needs Sources
	// Model are the atoms without an answer that an Unknown value still
	// depends on, each once, in the order of the canonical formulas. Step 4
	// asks the model of these, and no other.
	Model []Atom
}

// RuleFalseBelow is the probability below which a condition a model
// decides is False for a rule (ADR 0020): a rule's band, which a removal by
// meaning keeps a sentence below for every topic (ADR 0040), and which
// /status publishes (person_rules.meaning.threshold).
const RuleFalseBelow = 0.30

// Classify is the value of a condition a model decides, from the model's
// probability p that it holds, in a mode (ADR 0020's thresholds, as ADR 0027
// reads them in three values): for a query True at 0.80 or more and False at
// 0.20 or less; for a rule True at 0.70 or more and False below 0.30;
// Unknown between, for a probability that is not a number or outside 0 to
// 1, and for any other mode. The sign of the condition is applied after, so
// a negated condition is True for a query only at 0.20 or less.
func Classify(p float64, mode Mode) Value {
	if p >= 0 && p <= 1 { // false for NaN
		switch mode {
		case ForQuery:
			if p >= 0.80 {
				return True
			}
			if p <= 0.20 {
				return False
			}
		case ForRule:
			if p >= 0.70 {
				return True
			}
			if p < RuleFalseBelow {
				return False
			}
		}
	}
	return Unknown
}

// Context is what a sentence needs besides the formula: its language and
// the account's mailboxes, by identifier, so a condition on a mailbox names
// it (ADR 0027, "The sentence"). Every mailbox of the account is listed, so
// the sentence can tell whether a label is unique; a mailbox of the formula
// that is not listed is one the account disconnected, and the sentence names
// it by its identifier: "a mailbox you disconnected (mbx_…)".
type Context struct {
	Lang      string
	Mailboxes map[string]MailboxName
}

// MailboxName is what a sentence may call a mailbox, after the fixed words
// "your mailbox": its label, shown as a text value is (quoted, with
// quotation marks and backslashes escaped), when it is not empty and no
// other mailbox of the account has it, and otherwise its address. Either is
// the value part of the structured sentence, so no two mailboxes read alike.
type MailboxName struct {
	Label   string
	Address string
}
