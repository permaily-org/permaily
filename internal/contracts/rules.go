package contracts

import "time"

// The person's own rules (ADR 0019, "Rules"; how in ADR 0040): the shapes of
// the rules routes under /api/v1/rules, in the person's session, and what a
// receipt and a preview say a rule removed. Committed with the design; the
// routes are ADR 0040's pull request 6, the screens its pull request 7.
//
// A rule is the person's filter, applied before any connection's query: it
// holds a message back, or removes details of it, for every connection, every
// one but some, or only those picked. It matches mail by its criteria, each a
// Formula of the query language: a message meets the rule when it meets any
// of them, read for a rule (true or unknown applies: every rule protects).

// RuleEffect is what a rule does to a message it applies to.
type RuleEffect string

const (
	// EffectHold keeps the message from the connections the rule is for: it
	// does not leave, and the vendor is not told it exists.
	EffectHold RuleEffect = "hold"
	// EffectRemove takes details out of it (RuleRemoval) before the
	// connection's query reads it; the rest leaves.
	EffectRemove RuleEffect = "remove"
)

// RuleScopeTo says which connections a rule is for.
type RuleScopeTo string

const (
	ScopeAll       RuleScopeTo = "all"        // every connection, those made later included
	ScopeAllExcept RuleScopeTo = "all_except" // every connection but those of the clients listed, those made later included
	ScopeOnly      RuleScopeTo = "only"       // only the connections of the clients listed
)

// RuleScope names connections by their client (the client_id URL a vendor's
// app is known by, ADR 0030), not by connection: a client has at most one
// active connection, and its replacement, a device added to it, or a later
// connection of the same client keeps the rule's scope (ADR 0040, "Scope").
// Clients is empty for ScopeAll, and holds 1 to MaxRuleScopeClients clients
// otherwise, each at most once.
type RuleScope struct {
	To      RuleScopeTo `json:"to"`
	Clients []string    `json:"clients,omitempty"`
}

// ShapeKind is one kind of data with a fixed shape that a rule removes with
// no model (ADR 0040, "Fixed shapes"): its pattern is compiled into the image
// (internal/redact, at redact.Version) and every match is replaced by
// RemovedMarker.
type ShapeKind string

const (
	ShapeEmailAddress  ShapeKind = "email_address"  // an address with an @ and a domain
	ShapePhoneNumber   ShapeKind = "phone_number"   // a group of 7 to 15 digits
	ShapeCardNumber    ShapeKind = "card_number"    // a group of 12 to 19 digits
	ShapeAccountNumber ShapeKind = "account_number" // a group of 8 to 34 digits, or an IBAN
	ShapeNumericCode   ShapeKind = "numeric_code"   // a group of 4 to 10 digits, years and amounts included
	ShapeLink          ShapeKind = "link"           // a URL with a scheme, one that starts with www., or a domain followed by a path
)

// ShapeKinds are every ShapeKind, in the order the editor lists them and a
// canonical removal holds them.
var ShapeKinds = []ShapeKind{ShapeEmailAddress, ShapePhoneNumber, ShapeCardNumber, ShapeAccountNumber, ShapeNumericCode, ShapeLink}

// RemovedMarker is what stands where a rule removed a piece of a subject or a
// text: fixed words, never the model's, one for each run of what was removed.
const RemovedMarker = "[removed]"

// RuleRemoval is what an EffectRemove rule takes out, in this order (ADR
// 0040, "Removing details"): whole Parts; then every piece of the subject and
// the text that has one of the Shapes; then, sentence by sentence, every
// sentence a model says mentions one of the Topics. At least one of the
// three is set. A rule that removes Shapes or Topics also removes the
// message's attachments, whose content it cannot read (ADR 0040, choice 6).
type RuleRemoval struct {
	Parts  []Part      `json:"parts,omitempty"`
	Shapes []ShapeKind `json:"shapes,omitempty"`
	// Topics are short texts, normalised as a topic of the query language is
	// (ADR 0027), at most MaxRuleTopics.
	Topics []string `json:"topics,omitempty"`
}

// RuleDraft is a rule as the person edits it and confirms it: its name, its
// criteria (each a Formula; a message meets the rule when it meets any of
// them), its effect and, for EffectRemove, what it removes, its scope, and
// whether it is on. Criteria with one Formula whose All is empty applies to
// every message.
type RuleDraft struct {
	Name     string       `json:"name"`
	Criteria []Formula    `json:"criteria"`
	Effect   RuleEffect   `json:"effect"`
	Removal  *RuleRemoval `json:"removal,omitempty"`
	Scope    RuleScope    `json:"scope"`
	On       bool         `json:"on"`
}

// RuleSentence is what a person reads of a rule's criteria (ADR 0027, "Rules:
// any of their criteria"): each formula's sentence, introduced by Lead ("Any
// of these:") when there are several; Text is the whole as plain text.
type RuleSentence struct {
	Lang     string         `json:"lang"`
	Text     string         `json:"text"`
	Lead     []SentencePart `json:"lead"`
	Formulas []Sentence     `json:"formulas"`
}

// RuleCheck is what the system says of a draft it accepted (POST
// /api/v1/rules/check, and inside a proposal): the draft made canonical (each
// formula canonical, the removal's lists in their canonical order, the
// scope's clients sorted), its identifier (DraftID, which saving sends back
// and which names nothing of the account), the sentence of its criteria,
// written by the system with no model, the effect and the scope in the
// system's own words, and whether a model decides any of it (a criteria's
// about or mentions, or the removal's topics).
type RuleCheck struct {
	DraftID  string       `json:"draft_id"`
	Draft    RuleDraft    `json:"draft"`
	Sentence RuleSentence `json:"sentence"`
	// Does is the effect in the system's words: "Holds the email back" or
	// "Takes out" and what the removal lists.
	Does string `json:"does"`
	// For is the scope in the system's words, with the clients' domains.
	For   string `json:"for"`
	Model bool   `json:"model"`
}

// RuleWords is the body of POST /api/v1/rules/words: what the person wrote,
// in plain words, for a model to turn into a draft (ADR 0040, "Plain words"),
// and the version of the rules' notice the screen showed. Words are 1 to
// MaxRuleWordsRunes characters; they are sent to the model and kept nowhere.
type RuleWords struct {
	Words  string `json:"words"`
	Notice string `json:"notice"`
}

// RuleProposal is the answer to RuleWords: the draft the model proposed, as
// the system checked it, which the person reads by its sentence and may
// change before saving; nothing is saved or applies until they do. Its scope
// is ScopeAll and On is true: the model is told nothing of the account's
// connections. Model names the model and Prompt the prompt's version.
type RuleProposal struct {
	Check  RuleCheck `json:"check"`
	Model  string    `json:"model"`
	Prompt string    `json:"prompt"`
}

// Rule is one of the person's rules as the list and a rule's page show it.
// Version is the rule's own: a change of its criteria, effect or removal is
// a new version, whose decisions are made afresh; a change of its name,
// scope or switch is not. Notice is the version of the rules' notice it was
// saved under (empty when it asks no model).
type Rule struct {
	ID        string    `json:"id"`
	Version   int       `json:"version"`
	Check     RuleCheck `json:"check"`
	Notice    string    `json:"notice,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	ChangedAt time.Time `json:"changed_at"`
	// ModelOff says the rule asks a model and the notice it was saved under
	// is not the one in force, or the account has not agreed to the one in
	// force: it then asks nothing and applies wherever its value would need
	// the model (it holds, or removes the whole text), until the person
	// agrees and saves it again.
	ModelOff bool `json:"model_off,omitempty"`
}

// RuleDefault is the rule every account has from the start (ADR 0031): its
// summary, and whether the person turned it off.
type RuleDefault struct {
	Rule RuleSummary `json:"rule"`
	On   bool        `json:"on"`
	// ChangedAt is when it was last turned on or off, absent when never.
	ChangedAt time.Time `json:"changed_at,omitzero"`
}

// RuleNoticeAgreement is the rules' notice the account agreed to, and when.
type RuleNoticeAgreement struct {
	Version string    `json:"version"`
	At      time.Time `json:"at"`
}

// RuleList is GET /api/v1/rules: the default rule, the person's rules in the
// order they were made, the notice agreed, and Version, the version of the
// account's rules as read, which every change sends back (ErrRulesChanged
// when another change came first).
type RuleList struct {
	Default RuleDefault          `json:"default"`
	Rules   []Rule               `json:"rules"`
	Notice  *RuleNoticeAgreement `json:"notice,omitempty"`
	Version uint64               `json:"version"`
	Max     int                  `json:"max"`
}

// RuleSave is the body of POST /api/v1/rules (a new rule) and PUT
// /api/v1/rules/{id} (a change, the switch included): the draft the person
// confirmed, the DraftID its check answered (worked out again and refused
// with ErrRuleChanged when it differs), the rules' Version the screen read,
// and, for a draft a model decides any of, the rules' notice the screen
// showed. Saving needs a fresh proof (ADR 0022).
type RuleSave struct {
	DraftID string    `json:"draft_id"`
	Draft   RuleDraft `json:"draft"`
	Version uint64    `json:"version"`
	Notice  string    `json:"notice,omitempty"`
}

// RuleDelete is the body of POST /api/v1/rules/{id}/delete, and RuleDefaultSet
// of POST /api/v1/rules/default: each with the rules' Version the screen
// read, and each needing a fresh proof.
type RuleDelete struct {
	Version uint64 `json:"version"`
}

// RuleDefaultSet turns the default rule on or off.
type RuleDefaultSet struct {
	On      bool   `json:"on"`
	Version uint64 `json:"version"`
}

// RulePreview asks, in the person's session, for a page of a mailbox's mail
// the draft's criteria apply to, read for a rule, newest first, as
// MatchRequest pages do (POST /api/v1/rules/preview); with AskModel the
// model is asked as the owner's previews ask it (kept answers shared, the
// account's share of the spend), and nothing is committed: a preview makes
// no decision. Message, when set, asks instead what one message would look
// like once the draft's removal took its details out (RuleRemovalPreview).
type RulePreview struct {
	Draft    RuleDraft `json:"draft"`
	Mailbox  string    `json:"mailbox"`
	After    string    `json:"after,omitempty"`
	Limit    int       `json:"limit,omitempty"`
	AskModel bool      `json:"ask_model,omitempty"`
	Message  string    `json:"message,omitempty"`
}

// RulePreviewPage is a preview's page: the draft's identifier and the page,
// whose Messages are the mail the rule applies to, Unknown included (a rule
// applies on unknown), and NeedsModel what only the model can decide.
type RulePreviewPage struct {
	DraftID string    `json:"draft_id"`
	Page    MatchPage `json:"page"`
}

// RuleRemovalPreview is one message as a draft's removal leaves it, for the
// person only: its subject and text (each absent when removed whole, or when
// the text is not read), and what was removed.
type RuleRemovalPreview struct {
	DraftID string         `json:"draft_id"`
	Subject *string        `json:"subject,omitempty"`
	Text    *string        `json:"text,omitempty"`
	Removed RemovedSummary `json:"removed"`
}

// RemovedSummary says what rules removed from one message: never the removed
// content itself. It is what a receipt keeps of a removal, and what a preview
// says.
type RemovedSummary struct {
	// Parts are the parts removed whole.
	Parts []Part `json:"parts,omitempty"`
	// Shapes counts the pieces removed by kind (a piece with two kinds is
	// counted once, under the first in ShapeKinds).
	Shapes map[ShapeKind]int `json:"shapes,omitempty"`
	// Sentences counts the sentences removed by meaning, Of how many the
	// text was split into; Whole says the text (and the subject, when the
	// rule reads it) was removed whole instead, because a decision could
	// not be made or used, or the text is past what meaning reads (ADR 0040,
	// "Removing by meaning"): Why then says which, in HeldWhy's words.
	Sentences int    `json:"sentences,omitempty"`
	Of        int    `json:"of,omitempty"`
	Whole     bool   `json:"whole,omitempty"`
	Why       string `json:"why,omitempty"`
	// Attachments counts the attachments removed, by a rule removing the
	// part or a rule removing shapes or topics.
	Attachments int `json:"attachments,omitempty"`
}

// The bounds of the person's rules (ADR 0040, "The rules' record").
const (
	// MaxRules bounds the rules of an account, the default not counted.
	MaxRules = 16
	// MaxRuleBytes bounds a rule's canonical JSON (its draft and its own
	// fields), so the account's rules fit one record.
	MaxRuleBytes = 16 << 10
	// MaxRuleCriteria bounds a rule's criteria, each a formula within the
	// query language's own limits.
	MaxRuleCriteria = 8
	// MaxRuleModelConditions bounds the conditions a model decides over
	// all of a rule's criteria, as MaxModelConditions does a formula's.
	MaxRuleModelConditions = 8
	// MaxAccountModelConditions bounds them over every rule of the account
	// that is on, so every rule's statements about a message fit one call
	// to the model with the default rule's and a query's.
	MaxAccountModelConditions = 32
	// MaxRuleTopics bounds a removal's topics, and MaxMeaningRules the
	// account's rules that remove by meaning.
	MaxRuleTopics   = 3
	MaxMeaningRules = 4
	// MaxRuleScopeClients bounds a scope's clients.
	MaxRuleScopeClients = 16
	// MaxRuleNameRunes bounds a rule's name, and MaxRuleWordsRunes what
	// the person writes for the model.
	MaxRuleNameRunes  = 60
	MaxRuleWordsRunes = 1000
)

// Errors of the rules routes, besides the common ones (ErrNotFound for a rule
// that is not the account's; ErrConfirmationNeeded for a change without a
// fresh proof; ErrModelUnavailable for words while the model cannot be
// asked; ErrInvalidFormula, with its FormulaProblem, for a criteria that
// cannot be used).
var (
	// ErrInvalidRule refuses a draft past its bounds or not of its shapes;
	// its details' rule says why (RuleProblem).
	ErrInvalidRule = &Error{"invalid_rule", "that rule can't be used; its details say why"}
	// ErrRuleChanged: what saving would save is not what the screen
	// checked; check it again.
	ErrRuleChanged = &Error{"rule_changed", "that rule changed since this screen was shown; read it again"}
	// ErrRulesChanged: another change of the account's rules came first.
	ErrRulesChanged = &Error{"rules_changed", "your rules changed in another window; read them again"}
	// ErrTooManyRules: the account has MaxRules rules, or as many rules as
	// MaxMeaningRules remove by meaning, or the conditions a model decides
	// over its rules would pass MaxAccountModelConditions.
	ErrTooManyRules = &Error{"too_many_rules", "you have as many rules as an account can; delete or change one first"}
	// ErrRulesNoticeNeeded: words, or a rule a model decides any of, need
	// the rules' notice in force agreed first.
	ErrRulesNoticeNeeded = &Error{"rules_notice_needed", "read and agree to what Permaily sends to its model for rules first"}
	// ErrRuleWordsUnusable: the model's answer to the words is not a rule
	// the system can check; nothing was proposed.
	ErrRuleWordsUnusable = &Error{"rule_words_unusable", "Permaily couldn't turn that into a rule; write it another way, or build it with the conditions below"}
)

// RuleProblem says why a draft was refused (ErrInvalidRule), one of a fixed
// set.
type RuleProblem string

const (
	RuleNameTooLong        RuleProblem = "name_too_long"        // past MaxRuleNameRunes, or not text
	RuleNoCriteria         RuleProblem = "no_criteria"          // no criteria, or more than MaxRuleCriteria
	RuleTooLarge           RuleProblem = "too_large"            // past MaxRuleBytes
	RuleTooManyModel       RuleProblem = "too_many_model"       // past MaxRuleModelConditions
	RuleUnknownEffect      RuleProblem = "unknown_effect"       // not a RuleEffect
	RuleRemovalMissing     RuleProblem = "removal_missing"      // EffectRemove without a removal, or with an empty one
	RuleRemovalUnexpected  RuleProblem = "removal_unexpected"   // a removal with EffectHold
	RuleUnknownPart        RuleProblem = "unknown_part"         // a removal's part that is not a Part
	RuleUnknownShape       RuleProblem = "unknown_shape"        // a removal's shape that is not a ShapeKind
	RuleInvalidTopic       RuleProblem = "invalid_topic"        // a topic the query language would refuse, or more than MaxRuleTopics
	RuleUnknownScope       RuleProblem = "unknown_scope"        // not a RuleScopeTo
	RuleScopeClients       RuleProblem = "scope_clients"        // clients with ScopeAll, none otherwise, past MaxRuleScopeClients, twice, or not a client_id
	RuleDuplicate          RuleProblem = "duplicate"            // a value listed twice
	RuleDefaultNotEditable RuleProblem = "default_not_editable" // the default rule can only be turned on or off
)

// ruleProblems are every RuleProblem.
var ruleProblems = []RuleProblem{
	RuleNameTooLong, RuleNoCriteria, RuleTooLarge, RuleTooManyModel, RuleUnknownEffect, RuleRemovalMissing,
	RuleRemovalUnexpected, RuleUnknownPart, RuleUnknownShape, RuleInvalidTopic, RuleUnknownScope, RuleScopeClients,
	RuleDuplicate, RuleDefaultNotEditable,
}

// Valid says p is one of the fixed reasons.
func (p RuleProblem) Valid() bool {
	for _, q := range ruleProblems {
		if p == q {
			return true
		}
	}
	return false
}

// RuleError is ErrInvalidRule with its reason, as the routes answer it.
type RuleError struct{ Problem RuleProblem }

// InvalidRule is ErrInvalidRule for p.
func InvalidRule(p RuleProblem) *RuleError { return &RuleError{Problem: p} }

func (e *RuleError) Error() string { return ErrInvalidRule.Error() + " (" + string(e.Problem) + ")" }

// Unwrap makes errors.Is(err, ErrInvalidRule) hold.
func (e *RuleError) Unwrap() error { return ErrInvalidRule }

// LogKind is the fixed words a log line may carry of it.
func (e *RuleError) LogKind() string { return e.Error() }

// Details are the error's details: its reason alone.
func (e *RuleError) Details() ErrorDetails { return ErrorDetails{Rule: e.Problem} }

// Valid says k is one of ShapeKinds.
func (k ShapeKind) Valid() bool {
	for _, s := range ShapeKinds {
		if k == s {
			return true
		}
	}
	return false
}
