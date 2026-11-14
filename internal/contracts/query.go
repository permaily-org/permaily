package contracts

import "time"

// The query language (ADR 0019, how in ADR 0027). What a vendor sends as a
// connection's query or an agent's search, what a person's rule holds, and
// what the system answers about one: its canonical form, its identifier and
// the sentence a person reads. The same shapes travel through the web app's
// API, the vendors' API and MCP.
//
// A formula is an AND of groups, and a group is an OR of conditions. A
// condition is a variable, an operator and a value, and may be negated.
// Nesting is fixed at that: there is no other shape.

// FormulaVersion is the version of the language this release reads. A
// formula names its version (Formula.V); another is refused with
// FormulaUnsupportedVersion.
const FormulaVersion = 1

// Formula is a query, an agent's search or one criteria of a rule. An empty
// All matches every message. A group with no condition is refused
// (FormulaEmptyGroup).
type Formula struct {
	V   int     `json:"v"`
	All []Group `json:"all"`
}

// Group is an OR of conditions.
type Group struct {
	Any []Condition `json:"any"`
}

// Condition is a variable, an operator and a value (absent for
// OpExists), negated when Not is set. Value is always a string, whatever the
// variable: a time is RFC 3339 and a size a decimal number of bytes.
type Condition struct {
	Var   Variable `json:"var"`
	Op    Operator `json:"op"`
	Value string   `json:"value,omitempty"`
	Not   bool     `json:"not,omitempty"`
}

// Variable is what a condition looks at (ADR 0027, "Variables and
// operators"). The order of these constants is the order of the canonical
// form.
type Variable string

const (
	VarFromAddress      Variable = "from_address"      // the From header's addresses
	VarFromDomain       Variable = "from_domain"       // their domains, and every subdomain of the value
	VarRecipientAddress Variable = "recipient_address" // the To and Cc headers' addresses
	VarRecipientDomain  Variable = "recipient_domain"  // their domains, and every subdomain of the value
	VarSignedBy         Variable = "signed_by"         // domains of the DKIM signatures that passed at delivery
	VarSubject          Variable = "subject"           // the subject as stored
	VarBody             Variable = "body"              // the message's text, as a connection would receive it
	VarMessage          Variable = "message"           // the subject and the text together; for a model's operators only
	VarAttachments      Variable = "attachments"       // whether it has attachments
	VarAttachmentName   Variable = "attachment_name"   // the names of its attachments
	VarAttachmentType   Variable = "attachment_type"   // the media types of its attachments
	VarList             Variable = "list"              // the mailing list of its List-Id header
	VarReceived         Variable = "received"          // when Permaily received it
	VarSize             Variable = "size"              // its size as delivered, in bytes, as the sealed meta records it
	VarMailbox          Variable = "mailbox"           // the mailbox it arrived at
)

// Operator is how a condition compares its variable with its value. OpAbout
// and OpMentions are decided by a model (ADR 0020); every other one is exact.
type Operator string

const (
	OpIs       Operator = "is"
	OpContains Operator = "contains"
	OpExists   Operator = "exists"
	OpSince    Operator = "since"    // on or after
	OpBefore   Operator = "before"   // strictly before
	OpAtLeast  Operator = "at_least" // greater than or equal
	OpBelow    Operator = "below"    // strictly less
	OpAbout    Operator = "about"
	OpMentions Operator = "mentions"
)

// The limits of a formula (ADR 0027, "Limits"). A formula past one is
// refused. Formulas evaluated together (a consented scope, a request that
// fits in it, an agent's search) are each within them, and are never merged
// into one.
const (
	// MaxFormulaBytes bounds the JSON of a formula as it is sent.
	MaxFormulaBytes = 64 << 10
	// MaxGroups bounds the groups of a formula.
	MaxGroups = 32
	// MaxGroupConditions bounds the conditions of one group.
	MaxGroupConditions = 32
	// MaxConditions bounds the conditions of a formula, all groups together.
	MaxConditions = 64
	// MaxModelConditions bounds the conditions a model decides: each is a
	// statement put to the model for every message the exact conditions do
	// not settle (ADR 0020).
	MaxModelConditions = 8
	// MaxTextRunes and MaxTextBytes bound a text (the value of contains,
	// matched as a substring) or a topic after normalisation.
	MaxTextRunes = 100
	MaxTextBytes = 400
)

// ErrInvalidFormula refuses a formula this release cannot use. The answer's
// error carries a FormulaProblem as its details, which says where and why,
// in fixed words: never a value of the formula.
var ErrInvalidFormula = &Error{"invalid_formula", "that query can't be used; its details say where and why"}

// FormulaProblem says where a formula was refused and why. At is a path into
// the formula as sent, such as "all[2].any[0].value", or "" for the whole.
type FormulaProblem struct {
	At     string        `json:"at"`
	Reason FormulaReason `json:"reason"`
}

// FormulaReason is why a formula was refused, one of a fixed set.
type FormulaReason string

const (
	FormulaNotJSON            FormulaReason = "not_json"             // not one JSON object, or not UTF-8
	FormulaTooLarge           FormulaReason = "too_large"            // more than MaxFormulaBytes
	FormulaUnknownField       FormulaReason = "unknown_field"        // a member this version does not have
	FormulaDuplicateField     FormulaReason = "duplicate_field"      // a member twice in one object
	FormulaWrongType          FormulaReason = "wrong_type"           // a member of the wrong JSON type, or null
	FormulaUnsupportedVersion FormulaReason = "unsupported_version"  // V is not FormulaVersion
	FormulaTooManyGroups      FormulaReason = "too_many_groups"      // more than MaxGroups
	FormulaTooManyConditions  FormulaReason = "too_many_conditions"  // more than MaxGroupConditions or MaxConditions
	FormulaTooManyModel       FormulaReason = "too_many_model"       // more than MaxModelConditions
	FormulaEmptyGroup         FormulaReason = "empty_group"          // a group with no condition
	FormulaUnknownVariable    FormulaReason = "unknown_variable"     // a variable this version does not have
	FormulaUnknownOperator    FormulaReason = "unknown_operator"     // an operator this version does not have
	FormulaOperatorNotAllowed FormulaReason = "operator_not_allowed" // an operator the variable does not take
	FormulaMissingValue       FormulaReason = "missing_value"        // an operator that needs a value, without one
	FormulaUnexpectedValue    FormulaReason = "unexpected_value"     // a value for OpExists
	FormulaInvalidValue       FormulaReason = "invalid_value"        // a value that is not of the variable's type
	FormulaValueTooLong       FormulaReason = "value_too_long"       // past MaxTextRunes or MaxTextBytes, or the type's own bound
	FormulaConfusableValue    FormulaReason = "confusable_value"     // a word, an address's local part or a domain's label that mixes Latin, Greek and Cyrillic letters
	FormulaUnknownMailbox     FormulaReason = "unknown_mailbox"      // a mailbox that is not the account's
	FormulaNeverMatches       FormulaReason = "never_matches"        // a formula that no message can match
)

// FormulaCheck is what the system says of a formula it accepted: its
// canonical form, the identifier of that form, the sentence a person reads,
// and whether a model decides any of it.
type FormulaCheck struct {
	Formula  Formula  `json:"formula"`
	ID       string   `json:"id"`
	Sentence Sentence `json:"sentence"`
	Model    bool     `json:"model"`
}

// Sentence is what a person reads of a canonical formula, written by the
// system from fixed templates (ADR 0027, "The sentence"). Text is the whole
// of it as plain text; Lead and Lines are the same words in pieces, so the
// app shows values apart from fixed words without reading Text. With one
// group Lead is empty and Lines holds the whole sentence; with several,
// Lead introduces one line per group.
type Sentence struct {
	Lang  string         `json:"lang"`
	Text  string         `json:"text"`
	Lead  []SentencePart `json:"lead"`
	Lines []SentenceLine `json:"lines"`
}

// SentenceLine is one group of the formula, or the whole sentence.
type SentenceLine struct {
	Parts []SentencePart `json:"parts"`
}

// SentencePart is fixed words of a template, or a value of the formula
// (Value), which the app shows set apart and isolated (bdi). Model marks the
// parts of a condition a model decides.
type SentencePart struct {
	Text  string `json:"text"`
	Value bool   `json:"value,omitempty"`
	Model bool   `json:"model,omitempty"`
}

// MatchRequest asks for a page of what a formula matches in one mailbox,
// newest first, below After (a message identifier; "" for the newest), of at
// most Limit messages (1 to 100; 50 when 0).
type MatchRequest struct {
	Formula Formula `json:"formula"`
	After   string  `json:"after,omitempty"`
	Limit   int     `json:"limit,omitempty"`
}

// MatchPage is a page of a formula's matches over stored mail (ADR 0027,
// "Evaluation over stored mail"), for the owner's own previews only: the
// rule editor's and the consent screen's, in the owner's session. Its Next,
// SearchedTo, NeedsModel and Unknown name messages outside any approved
// scope (that they exist, their identifiers, when they arrived), so no
// vendor route may answer it: a vendor's result and its continuation are a
// separate contract, designed in step 5, that reveals only approved
// messages. Messages are the ones every exact
// condition settles as a match. NeedsModel are the ones whose answer depends
// on a condition a model decides, not asked yet; Unknown the ones whose
// answer depends on data that could not be read. Next is the identifier of
// the last message the page considered, matched or not, to go on below it;
// absent when nothing older can match. SearchedTo is when that message
// arrived.
type MatchPage struct {
	Messages   []MessageSummary `json:"messages"`
	NeedsModel []string         `json:"needs_model"`
	Unknown    []string         `json:"unknown"`
	Next       string           `json:"next,omitempty"`
	SearchedTo time.Time        `json:"searched_to,omitzero"`
}
