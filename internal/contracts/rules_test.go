package contracts

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// U1's contract half (ADR 0040): an invalid rule's details are its reason
// alone, written {"rule"}, read back as written, and told apart from a
// formula's ({"at","reason"}) and an invalid client's ({"reason"}); the two
// older shapes are written exactly as they were.
func TestU1ARulesDetailsAreTheirOwnShape(t *testing.T) {
	for _, p := range ruleProblems {
		d := InvalidRule(p).Details()
		b, err := json.Marshal(d)
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		if want := `{"rule":"` + string(p) + `"}`; string(b) != want {
			t.Errorf("%s written %s, want %s", p, b, want)
		}
		var back ErrorDetails
		if err := json.Unmarshal(b, &back); err != nil || back != d {
			t.Errorf("%s read back as %+v, %v", p, back, err)
		}
		e := InvalidRule(p)
		if !errors.Is(e, ErrInvalidRule) {
			t.Errorf("%s is not ErrInvalidRule", p)
		}
		if want := "invalid_rule: that rule can't be used; its details say why (" + string(p) + ")"; e.Error() != want || e.LogKind() != want {
			t.Errorf("%s says %q and logs %q", p, e.Error(), e.LogKind())
		}
	}
	for _, d := range []ErrorDetails{
		{Rule: "no_such_reason"},
		{Rule: RuleTooLarge, Client: ClientIDInvalid},
		{Rule: RuleTooLarge, FormulaProblem: FormulaProblem{At: "all[0]", Reason: FormulaNotJSON}},
	} {
		if b, err := json.Marshal(d); err == nil {
			t.Errorf("%+v written as %s", d, b)
		}
	}
	for _, s := range []string{`{"rule":"no_such_reason"}`, `{"rule":"too_large","reason":"client_id_invalid"}`, `{"rule":"too_large","at":""}`} {
		var d ErrorDetails
		if err := json.Unmarshal([]byte(s), &d); err == nil {
			t.Errorf("%s read as %+v", s, d)
		}
	}
	for want, d := range map[string]ErrorDetails{
		`{"at":"all[0]","reason":"not_json"}`: {FormulaProblem: FormulaProblem{At: "all[0]", Reason: FormulaNotJSON}},
		`{"reason":"client_id_invalid"}`:      {Client: ClientIDInvalid},
	} {
		b, err := json.Marshal(d)
		if err != nil || string(b) != want {
			t.Errorf("%+v written %s, %v; want %s", d, b, err, want)
		}
	}
}

// U1: every shape kind is valid and listed once, in the order the editor and
// a canonical removal use; nothing else is one.
func TestU1TheShapeKinds(t *testing.T) {
	seen := map[ShapeKind]bool{}
	for _, k := range ShapeKinds {
		if !k.Valid() || seen[k] {
			t.Errorf("%q listed twice or not valid", k)
		}
		seen[k] = true
	}
	if len(seen) != 6 {
		t.Fatalf("%d kinds, ADR 0040 names six", len(seen))
	}
	for _, k := range []ShapeKind{"", "address", "EMAIL_ADDRESS", "iban"} {
		if k.Valid() {
			t.Errorf("%q is valid", k)
		}
	}
}

// U1: the routes' shapes, field by field, as web/src/lib/api.ts reads them.
func TestU1TheRulesShapes(t *testing.T) {
	at := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	list := RuleList{
		Default: RuleDefault{Rule: RuleSummary{ID: "default-sign-in-and-security", Version: 1, Sentence: []Sentence{}}, On: true},
		Rules: []Rule{{
			ID: "rul_x", Version: 2, Notice: "2026-11-20", CreatedAt: at, ChangedAt: at, ModelOff: true,
			Check: RuleCheck{
				DraftID: "rdr_x",
				Draft: RuleDraft{
					Name: "Home address", Criteria: []Formula{{V: 1, All: []Group{}}}, Effect: EffectRemove,
					Removal: &RuleRemoval{Parts: []Part{PartAttachments}, Shapes: []ShapeKind{ShapePhoneNumber}, Topics: []string{"a home address"}},
					Scope:   RuleScope{To: ScopeAllExcept, Clients: []string{"https://claude.ai/oauth/mcp-oauth-client-metadata"}},
					On:      true,
				},
				Sentence: RuleSentence{Lang: "en", Text: "All mail", Lead: []SentencePart{}, Formulas: []Sentence{}},
				Does:     "Takes out", For: "Every connection but claude.ai/oauth/mcp-oauth-client-metadata", Model: true,
				Clients: []RuleClient{{ClientID: "https://claude.ai/oauth/mcp-oauth-client-metadata", Shown: "claude.ai/oauth/mcp-oauth-client-metadata", Name: "Claude"}},
			},
		}},
		Notice:  &RuleNoticeAgreement{Version: "2026-11-20", At: at},
		Version: 7, Max: MaxRules,
	}
	b, err := json.Marshal(list)
	if err != nil {
		t.Fatal(err)
	}
	want := `{"default":{"rule":{"id":"default-sign-in-and-security","version":1,"sentence":[]},"on":true},` +
		`"rules":[{"id":"rul_x","version":2,"check":{"draft_id":"rdr_x","draft":{"name":"Home address","criteria":[{"v":1,"all":[]}],` +
		`"effect":"remove","removal":{"parts":["attachments"],"shapes":["phone_number"],"topics":["a home address"]},` +
		`"scope":{"to":"all_except","clients":["https://claude.ai/oauth/mcp-oauth-client-metadata"]},"on":true},` +
		`"sentence":{"lang":"en","text":"All mail","lead":[],"formulas":[]},"does":"Takes out","for":"Every connection but claude.ai/oauth/mcp-oauth-client-metadata",` +
		`"clients":[{"client_id":"https://claude.ai/oauth/mcp-oauth-client-metadata","shown":"claude.ai/oauth/mcp-oauth-client-metadata","name":"Claude"}],"model":true},` +
		`"notice":"2026-11-20","created_at":"2026-11-20T12:00:00Z","changed_at":"2026-11-20T12:00:00Z","model_off":true}],` +
		`"notice":{"version":"2026-11-20","at":"2026-11-20T12:00:00Z"},"version":7,"max":16}`
	if string(b) != want {
		t.Fatalf("written\n%s\nwant\n%s", b, want)
	}
	r, err := json.Marshal(RemovedSummary{Shapes: map[ShapeKind]int{ShapeLink: 2}, Sentences: 1, Of: 9, Attachments: 1})
	if err != nil || string(r) != `{"shapes":{"link":2},"sentences":1,"of":9,"attachments":1}` {
		t.Fatalf("a removal written %s, %v", r, err)
	}
	if w, _ := json.Marshal(RemovedSummary{Whole: true, Why: "undecided"}); string(w) != `{"whole":true,"why":"undecided"}` {
		t.Fatalf("a whole removal written %s", w)
	}
}

// U1 (finding 4): a client is shown so that two documents on one host are
// told apart, never by the domain alone; web/src/lib/api.ts's clientShown
// answers these same cases (U32).
func TestU1AClientIsShownApartFromAnotherOnItsHost(t *testing.T) {
	for in, want := range map[string]string{
		"https://vendor.example/client-a.json":              "vendor.example/client-a.json",
		"https://vendor.example/client-b.json":              "vendor.example/client-b.json",
		"https://vendor.example/v2/client-a.json":           "vendor.example/v2/client-a.json",
		"https://claude.ai/oauth/mcp-oauth-client-metadata": "claude.ai/oauth/mcp-oauth-client-metadata",
		"http://localhost:8080/client":                      "http://localhost:8080/client",
		"https://":                                          "https://",
	} {
		if got := ClientShown(in); got != want {
			t.Errorf("%s shown %q, want %q", in, got, want)
		}
	}
}
