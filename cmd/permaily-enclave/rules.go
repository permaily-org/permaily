package main

import (
	"errors"
	"log/slog"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/decide"
	"github.com/permaily-org/permaily/internal/domain"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/query"
	"github.com/permaily-org/permaily/internal/redact"
	"github.com/permaily-org/permaily/internal/rulewords"
	"github.com/permaily-org/permaily/internal/services/accountrules"
	"github.com/permaily-org/permaily/internal/services/decisions"
	"github.com/permaily-org/permaily/internal/store"
	"github.com/permaily-org/permaily/pkg/attestation"
)

// The person's own rules (ADR 0040, pull request 8): the account's record
// of rules in a strict Anchored of its own, sealed under a key from the
// root secret, each change recorded in a `rules` receipt in the account's
// chain before it is written; read by every connection's request, the
// consent screen and delivery (connections.Config.Source), removed with
// the account, and changed through the rules routes.

// errRulesRecords refuses records that are not one strict Anchored keeping
// only the newest value.
var errRulesRecords = errors.New("the rules' records need a strict Anchored that keeps only the newest value")

// rulesParts is what the person's rules are composed from.
type rulesParts struct {
	// records is the one strict Anchored of the rules' records
	// (rules-<account>-<epoch>), with OnlyNewest: no other Anchored reads
	// or writes their names, and it reads no other.
	records   *store.Anchored
	keys      accountrules.Keys
	receipts  *composedReceipts
	mailboxes connectionMailboxes
	log       *slog.Logger
	now       func() time.Time
}

// composeRules is the person's rules service (accountrules.Service).
func composeRules(r rulesParts) (*accountrules.Service, error) {
	if r.records == nil || !r.records.Strict || !r.records.OnlyNewest {
		return nil, errRulesRecords
	}
	if r.receipts == nil || !r.receipts.svc.Chained() {
		return nil, errors.New("the rules need receipts linked in their accounts' chains (ADR 0037)")
	}
	return accountrules.New(accountrules.Config{
		Records:   r.records,
		Keys:      r.keys,
		Receipts:  r.receipts.svc,
		Mailboxes: r.mailboxes,
		Log:       r.log,
		Now:       r.now,
	})
}

// The model's service is what the rules ask of a model: a person's words
// turned into a draft (rulewords.Writer, through the model's egress and
// its spend guard), and a removal's topics decided sentence by sentence
// (inbox.SentenceAsker, which the discloser finds on the connections'
// Asker).
var (
	_ rulewords.Writer    = (*decisions.Service)(nil)
	_ inbox.SentenceAsker = (*decisions.Service)(nil)
)

// personRulesStatus is /status's person_rules (ADR 0040, "/status and
// permaily-verify"): the design's constants and versions as the image
// compiles them, the words calls of this enclave by outcome and the reads
// of an account's rules it could not use. Nothing of an account.
func personRulesStatus(rs *accountrules.Service, model *decisions.Service) *attestation.PersonRulesStatus {
	kinds := make([]string, 0, len(contracts.ShapeKinds))
	for _, k := range contracts.ShapeKinds {
		kinds = append(kinds, string(k))
	}
	return &attestation.PersonRulesStatus{
		Record:  attestation.PersonRulesRecordStrict,
		Effects: []string{string(contracts.EffectHold), string(contracts.EffectRemove)},
		Shapes:  attestation.PersonRulesShapes{Version: redact.Version, Kinds: kinds},
		Meaning: attestation.PersonRulesMeaning{
			Prompt: decide.SentencesPromptVersion, Threshold: query.RuleFalseBelow, SplitVersion: redact.SplitVersion,
			SentencesMax: redact.MeaningSentencesMax, TextMax: redact.MeaningTextMax, TopicsMax: contracts.MaxRuleTopics,
		},
		Words:        attestation.PersonRulesWords{Model: rulewords.ModelName, Prompt: rulewords.PromptVersion, Calls: model.WordsCalls()},
		Notice:       domain.RulesNoticeVersion,
		FreshSeconds: seconds(accountrules.Fresh),
		Unreadable:   rs.Unreadable(),
	}
}
