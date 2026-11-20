package attestation

import (
	"errors"
	"fmt"
	"slices"
)

// PersonRulesStatus is what an enclave publishes of a person's own rules
// (ADR 0040, "/status and permaily-verify"): the design's constants and the
// versions compiled into the image, and this enclave's counts since it
// started. Nothing per account is ever in it.
//
// Record is PersonRulesRecordStrict: an account's rules are a strict
// anchored record, which whoever writes the storage cannot put back.
// Effects are what a rule can do; Shapes the fixed shapes' version and
// kinds; Meaning removing by meaning (the prompt's version, the threshold
// a sentence is kept below, and its bounds); Words the model and prompt
// that turn a person's words into a rule, and this enclave's words calls by
// outcome (PersonRulesWordsCalls); Notice the rules' notice in force;
// FreshSeconds how old another enclave's copy of an account's rules may be;
// Unreadable how many reads of an account's rules this enclave could not
// use (nothing of that account's mail left).
type PersonRulesStatus struct {
	Record       string             `json:"record"`
	Effects      []string           `json:"effects"`
	Shapes       PersonRulesShapes  `json:"shapes"`
	Meaning      PersonRulesMeaning `json:"meaning"`
	Words        PersonRulesWords   `json:"words"`
	Notice       string             `json:"notice"`
	FreshSeconds int                `json:"fresh_seconds"`
	Unreadable   int64              `json:"rules_unreadable"`
}

// PersonRulesShapes is the fixed shapes (internal/redact): the version of
// how they are found, and the kinds, in their order.
type PersonRulesShapes struct {
	Version int      `json:"version"`
	Kinds   []string `json:"kinds"`
}

// PersonRulesMeaning is removing by meaning: the Decisions API's prompt for
// sentences, the probability a sentence is kept below for every topic, the
// version of the split into sentences, at most how many sentences and bytes
// of the message are read (the rest removed, never sent), and at most how
// many topics a rule has.
type PersonRulesMeaning struct {
	Prompt       string  `json:"prompt"`
	Threshold    float64 `json:"threshold"`
	SplitVersion int     `json:"split_version"`
	SentencesMax int     `json:"sentences_max"`
	TextMax      int     `json:"text_max"`
	TopicsMax    int     `json:"topics_max"`
}

// PersonRulesWords is the words' model (the Responses API's model and the
// prompt's version, which names every byte of the developer message and
// the schema), and Calls, this enclave's words calls since it started, by
// every kind of PersonRulesWordsCalls (also at zero).
type PersonRulesWords struct {
	Model  string           `json:"model"`
	Prompt string           `json:"prompt"`
	Calls  map[string]int64 `json:"calls"`
}

// PersonRulesRecordStrict: an account's rules are one strict anchored
// record per epoch, refused when lost, forked or put back.
const PersonRulesRecordStrict = "strict"

// The outcomes of a words call /status counts: a draft proposed; words or
// an answer that make no usable rule; too many calls of the account's day
// in this enclave; and the model unavailable (no key, the spend guard, a
// failure, an answer naming another model, the caller gone).
const (
	WordsProposed    = "proposed"
	WordsUnusable    = "unusable"
	WordsTooMany     = "too_many"
	WordsUnavailable = "unavailable"
)

// PersonRulesWordsCalls lists the outcomes of a words call, in the order
// they are shown.
var PersonRulesWordsCalls = []string{WordsProposed, WordsUnusable, WordsTooMany, WordsUnavailable}

// Check says whether an enclave's s is want, the verifier's own (built from
// the release's source): Record strict; every constant and version equal,
// the lists in their order; the counts of every kind present, none below
// zero and none of a kind the verifier does not know. The first error says
// which differs. A nil s is an enclave that publishes no person's rules.
func (s *PersonRulesStatus) Check(want PersonRulesStatus) error {
	switch {
	case s == nil:
		return errors.New("the enclave publishes nothing of a person's own rules (person_rules)")
	case s.Record != PersonRulesRecordStrict:
		return fmt.Errorf("a person's rules are kept %q, not in strict records: whoever writes the storage could put back rules the person changed", s.Record)
	case !slices.Equal(s.Effects, want.Effects):
		return fmt.Errorf("a rule's effects are %q, not %q", s.Effects, want.Effects)
	case s.Shapes.Version != want.Shapes.Version:
		return fmt.Errorf("the fixed shapes are version %d, not %d", s.Shapes.Version, want.Shapes.Version)
	case !slices.Equal(s.Shapes.Kinds, want.Shapes.Kinds):
		return fmt.Errorf("the fixed shapes' kinds are %q, not %q", s.Shapes.Kinds, want.Shapes.Kinds)
	case s.Meaning != want.Meaning:
		return fmt.Errorf("removing by meaning is %+v, not %+v", s.Meaning, want.Meaning)
	case s.Words.Model != want.Words.Model || s.Words.Prompt != want.Words.Prompt:
		return fmt.Errorf("words are turned into rules by %s under prompt %s, not %s under %s", s.Words.Model, s.Words.Prompt, want.Words.Model, want.Words.Prompt)
	case s.Notice != want.Notice:
		return fmt.Errorf("the rules' notice in force is %q, not %q", s.Notice, want.Notice)
	case s.FreshSeconds != want.FreshSeconds:
		return fmt.Errorf("another enclave may apply an account's rules %d seconds old, not %d", s.FreshSeconds, want.FreshSeconds)
	case s.Unreadable < 0:
		return errors.New("the count of a person's rules that could not be read is below zero")
	}
	for k, n := range s.Words.Calls {
		switch {
		case !slices.Contains(PersonRulesWordsCalls, k):
			return fmt.Errorf("words calls are counted by %q, a kind this verifier does not know", k)
		case n < 0:
			return fmt.Errorf("words calls %q are counted below zero", k)
		}
	}
	for _, k := range PersonRulesWordsCalls {
		if _, ok := s.Words.Calls[k]; !ok {
			return fmt.Errorf("words calls %q are not counted", k)
		}
	}
	return nil
}
