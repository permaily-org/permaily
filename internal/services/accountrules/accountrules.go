// Package accountrules keeps the person's own rules (ADR 0040): the account's
// record of rules, a strict anchored record sealed under the root secret,
// read and written at its version, rolling to a next epoch at its versions'
// bound; turning the default rule off; the rules' notice; and the rules a
// connection applies before its formulas.
//
// This file is the seam, committed with the design and revised after its
// review (finding 7): what the connections service, the consent screen's
// preview and delivery (ADR 0039) ask of it (Source), what the rules routes
// ask of it (Rules), with a fake that keeps both contracts
// (accountrulestest). ADR 0040's pull request 1 writes the service behind
// it.
package accountrules

import (
	"context"
	"errors"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/rules"
)

// Source is the rules a connection applies. It is the one way the
// connections service, the consent screen's preview and delivery's matching
// (ADR 0039: a notice is sent only for a message a read would give) learn
// them, so all three apply the same rules in the same order.
type Source interface {
	// Applying is the rules a connection of client applies now, in the
	// order they are evaluated (rules.Record.For): read strictly, from a
	// cache at most Fresh old in this enclave, and from the record at once
	// after a change made through this enclave. An error (the record lost,
	// forked, unopenable or unreadable, a rule that does not compile, the
	// context's end) means nothing of the account's mail leaves: the caller
	// answers unavailable and gives nothing, never the default alone.
	Applying(ctx context.Context, account, client string) (Applying, error)
}

// Applying is the rules a request applies, and the version of the account's
// record they were read at, which receipts name with each rule's own.
type Applying struct {
	Rules []rules.Rule
	// Epoch and Version are the record's epoch and version read; zero for
	// an account with no record (the default alone, on).
	Epoch   uint64
	Version uint64
}

// Fresh bounds how old the rules a request applies may be in an enclave
// other than the one a change was made through: the same five seconds as a
// revocation (ADR 0030), so a rule added, changed or turned on applies to
// every request that begins five seconds after it is saved, through either
// enclave. A cached read's age counts from when that read began, not from
// when it answered, and a read that fails never makes a cached one younger:
// once its age reaches Fresh it is not used again, and Applying answers
// ErrUnavailable until a read succeeds.
const Fresh = 5 * time.Second

// Rules is what the rules routes (ADR 0040's pull request 6) ask of the
// service; pull request 1 implements it, and accountrulestest.Fake plays it
// within RulesContract. Every change:
//
//   - needs a fresh proof: Person.ConfirmedUntil after now, or
//     contracts.ErrConfirmationNeeded, with nothing written (ADR 0022);
//   - names the record's version the screen read (RuleList.Version): another
//     version, or a write that loses at it, is contracts.ErrRulesChanged,
//     never tried again on another state;
//   - is recorded first, in a `rules` receipt saying what was asked at that
//     version, and then written at that version (ADR 0040, "Receipts"): a
//     receipt that cannot be written makes nothing; a write that loses
//     after its receipt leaves a receipt of a change that was not made,
//     which says more than happened, never a change made and not recorded,
//     as a consent's renewal does (ADR 0030);
//   - answers only after both, and from then on applies through this
//     enclave at once, and through the other within Fresh.
//
// Errors besides those: *contracts.RuleError (contracts.ErrInvalidRule) or a
// formula's own for a draft that cannot be used; contracts.ErrRuleChanged
// when the draft is not the one its DraftID names; contracts.ErrTooManyRules;
// contracts.ErrRulesNoticeNeeded for a draft a model decides any of while
// the account has not agreed to the notice in force, or saved under another;
// contracts.ErrNotFound for a rule that is not the account's; anything that
// wraps ErrUnavailable when the record cannot be read or written.
type Rules interface {
	Source
	// List is the account's rules as the rules page shows them.
	List(ctx context.Context, account string) (*contracts.RuleList, error)
	// Check is the draft made canonical, its identifier and the system's
	// sentence of it (contracts.RuleCheck), with no write and no model; the
	// account's mailboxes are what a criteria may name.
	Check(ctx context.Context, account string, d contracts.RuleDraft) (*contracts.RuleCheck, error)
	// Save makes a rule (id "") or changes one: its version changes with
	// its criteria, effect or removal, and only then.
	Save(ctx context.Context, p Person, id string, in contracts.RuleSave) (*contracts.Rule, error)
	// Delete removes a rule.
	Delete(ctx context.Context, p Person, id string, in contracts.RuleDelete) error
	// SetDefault turns the default rule on or off.
	SetDefault(ctx context.Context, p Person, in contracts.RuleDefaultSet) (*contracts.RuleDefault, error)
	// AgreeNotice records the rules' notice in force agreed (any other
	// version is contracts.ErrRulesNoticeNeeded). It names no version of
	// the record: agreeing is the same whatever the rules are, so a write
	// that loses is made again on the record read again.
	AgreeNotice(ctx context.Context, p Person, version string) (*contracts.RuleNoticeAgreement, error)
	// Remove removes every epoch of the account's record, in the account's
	// removal (no proof: the removal asked for its own).
	Remove(ctx context.Context, account string) error
}

// Person is who asks for a change, as the routes know them: the account,
// and until when their last proof is fresh.
type Person struct {
	Account        string
	ConfirmedUntil time.Time
}

// ErrUnavailable is Applying's answer when the record cannot be used now;
// it wraps the cause, which a log line names by kind only.
var ErrUnavailable = errors.New("accountrules: the account's rules cannot be read now")
