package main

import (
	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/decide"
	"github.com/permaily-org/permaily/internal/domain"
	"github.com/permaily-org/permaily/internal/query"
	"github.com/permaily-org/permaily/internal/redact"
	"github.com/permaily-org/permaily/internal/rulewords"
	"github.com/permaily-org/permaily/pkg/attestation"
)

// personRulesWant is what an enclave of this verifier's release must
// publish of a person's own rules (ADR 0040, "/status and
// permaily-verify"), from the source this verifier is built from: the
// record strict, the effects, the fixed shapes' version and kinds,
// meaning's prompt, threshold, split and bounds, the words' model and
// prompt, the rules' notice in force, and the five seconds of freshness the
// design promises (attestation.PersonRulesFreshSeconds; the verifier links
// no service).
// The counts are the enclave's own, and are only checked for their kinds
// and signs (attestation.PersonRulesStatus.Check).
func personRulesWant() attestation.PersonRulesStatus {
	kinds := make([]string, 0, len(contracts.ShapeKinds))
	for _, k := range contracts.ShapeKinds {
		kinds = append(kinds, string(k))
	}
	return attestation.PersonRulesStatus{
		Record:  attestation.PersonRulesRecordStrict,
		Effects: []string{string(contracts.EffectHold), string(contracts.EffectRemove)},
		Shapes:  attestation.PersonRulesShapes{Version: redact.Version, Kinds: kinds},
		Meaning: attestation.PersonRulesMeaning{
			Prompt: decide.SentencesPromptVersion, Threshold: query.RuleFalseBelow, SplitVersion: redact.SplitVersion,
			SentencesMax: redact.MeaningSentencesMax, TextMax: redact.MeaningTextMax, TopicsMax: contracts.MaxRuleTopics,
		},
		Words:        attestation.PersonRulesWords{Model: rulewords.ModelName, Prompt: rulewords.PromptVersion},
		Notice:       domain.RulesNoticeVersion,
		FreshSeconds: attestation.PersonRulesFreshSeconds,
	}
}
