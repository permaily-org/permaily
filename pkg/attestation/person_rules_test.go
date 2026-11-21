package attestation

import (
	"strings"
	"testing"
)

// want is a person's rules as a verifier's source says them.
func want() PersonRulesStatus {
	return PersonRulesStatus{
		Record: PersonRulesRecordStrict, Effects: []string{"hold", "remove"},
		Shapes:  PersonRulesShapes{Version: 1, Kinds: []string{"email_address", "phone_number"}},
		Meaning: PersonRulesMeaning{Prompt: "s1", Threshold: 0.3, SplitVersion: 1, SentencesMax: 128, TextMax: 32768, TopicsMax: 3},
		Words:   PersonRulesWords{Model: "m", Prompt: "w4"}, Notice: "2026-11-20", FreshSeconds: 5,
	}
}

// published is what an enclave of the same source publishes, with counts.
func published() *PersonRulesStatus {
	s := want()
	s.Words.Calls = map[string]int64{WordsProposed: 3, WordsUnusable: 1, WordsTooMany: 0, WordsUnavailable: 2}
	s.Unreadable = 4
	return &s
}

// U31 (ADR 0040, "/status and permaily-verify"): an enclave's person_rules
// pass when every constant and version is the source's, whatever its
// counts; a change of any of them, a record that is not strict, a section
// absent, and a count below zero, of a kind not known, or missing, fail it,
// each saying what.
func TestU31PersonRulesCheckedAgainstTheSource(t *testing.T) {
	if err := published().Check(want()); err != nil {
		t.Fatalf("the source's own: %v", err)
	}
	var none *PersonRulesStatus
	if err := none.Check(want()); err == nil || !strings.Contains(err.Error(), "person_rules") {
		t.Fatalf("absent: %v", err)
	}
	for name, c := range map[string]struct {
		change func(*PersonRulesStatus)
		says   string
	}{
		"a record not strict":       {func(s *PersonRulesStatus) { s.Record = "cached" }, "not in strict records"},
		"an effect fewer":           {func(s *PersonRulesStatus) { s.Effects = s.Effects[:1] }, "effects"},
		"another shapes' version":   {func(s *PersonRulesStatus) { s.Shapes.Version = 2 }, "version 2"},
		"a kind fewer":              {func(s *PersonRulesStatus) { s.Shapes.Kinds = s.Shapes.Kinds[:1] }, "kinds"},
		"kinds reordered":           {func(s *PersonRulesStatus) { s.Shapes.Kinds = []string{"phone_number", "email_address"} }, "kinds"},
		"another sentences' prompt": {func(s *PersonRulesStatus) { s.Meaning.Prompt = "s2" }, "removing by meaning"},
		"another threshold":         {func(s *PersonRulesStatus) { s.Meaning.Threshold = 0.5 }, "removing by meaning"},
		"another split":             {func(s *PersonRulesStatus) { s.Meaning.SplitVersion = 2 }, "removing by meaning"},
		"more sentences":            {func(s *PersonRulesStatus) { s.Meaning.SentencesMax = 129 }, "removing by meaning"},
		"more text":                 {func(s *PersonRulesStatus) { s.Meaning.TextMax = 1 << 20 }, "removing by meaning"},
		"more topics":               {func(s *PersonRulesStatus) { s.Meaning.TopicsMax = 4 }, "removing by meaning"},
		"another words' model":      {func(s *PersonRulesStatus) { s.Words.Model = "other" }, "words are turned into rules"},
		"another words' prompt":     {func(s *PersonRulesStatus) { s.Words.Prompt = "w5" }, "words are turned into rules"},
		"another notice":            {func(s *PersonRulesStatus) { s.Notice = "2027-01-01" }, "notice"},
		"slower":                    {func(s *PersonRulesStatus) { s.FreshSeconds = 6 }, "6 seconds"},
		"unreadable below zero":     {func(s *PersonRulesStatus) { s.Unreadable = -1 }, "below zero"},
		"calls below zero":          {func(s *PersonRulesStatus) { s.Words.Calls[WordsProposed] = -1 }, "below zero"},
		"calls of an unknown kind":  {func(s *PersonRulesStatus) { s.Words.Calls["leaked"] = 0 }, "does not know"},
		"calls of a kind missing":   {func(s *PersonRulesStatus) { delete(s.Words.Calls, WordsTooMany) }, "not counted"},
	} {
		s := published()
		c.change(s)
		if err := s.Check(want()); err == nil || !strings.Contains(err.Error(), c.says) {
			t.Errorf("%s: %v", name, err)
		}
	}
}
