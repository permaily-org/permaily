package contracts

import (
	"encoding/json"
	"errors"
	"testing"
	"time"
)

// The web app reads these names (web/src/lib/api.ts), and vendors send and
// read them: a change here is a change of the language, in ADR 0027 and
// there too.
func TestQueryShapes(t *testing.T) {
	at := time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		v    any
		want string
	}{
		{"every message", Formula{V: FormulaVersion, All: []Group{}}, `{"v":1,"all":[]}`},
		{"a formula", Formula{V: 1, All: []Group{
			{Any: []Condition{
				{Var: VarFromDomain, Op: OpIs, Value: "holded.com"},
				{Var: VarSubject, Op: OpContains, Value: "invoice"},
			}},
			{Any: []Condition{{Var: VarAttachments, Op: OpExists, Not: true}}},
			{Any: []Condition{{Var: VarMessage, Op: OpAbout, Value: "my car"}}},
		}}, `{"v":1,"all":[{"any":[{"var":"from_domain","op":"is","value":"holded.com"},{"var":"subject","op":"contains","value":"invoice"}]},{"any":[{"var":"attachments","op":"exists","not":true}]},{"any":[{"var":"message","op":"about","value":"my car"}]}]}`},
		{"a problem", FormulaProblem{At: "all[2].any[0].value", Reason: FormulaInvalidValue},
			`{"at":"all[2].any[0].value","reason":"invalid_value"}`},
		{"a check", FormulaCheck{
			Formula: Formula{V: 1, All: []Group{{Any: []Condition{{Var: VarFromAddress, Op: OpIs, Value: "ann@example.org"}}}}},
			ID:      "qry_x",
			Sentence: Sentence{Lang: "en", Text: "Mail sent from “ann@example.org”.", Lead: []SentencePart{}, Lines: []SentenceLine{{Parts: []SentencePart{
				{Text: "Mail sent from "}, {Text: "ann@example.org", Value: true}, {Text: "."},
			}}}},
		}, `{"formula":{"v":1,"all":[{"any":[{"var":"from_address","op":"is","value":"ann@example.org"}]}]},"id":"qry_x","sentence":{"lang":"en","text":"Mail sent from “ann@example.org”.","lead":[],"lines":[{"parts":[{"text":"Mail sent from "},{"text":"ann@example.org","value":true},{"text":"."}]}]},"model":false}`},
		{"a part a model decides", SentencePart{Text: "my car", Value: true, Model: true},
			`{"text":"my car","value":true,"model":true}`},
		{"a match request", MatchRequest{Formula: Formula{V: 1, All: []Group{}}, After: "m9", Limit: 20},
			`{"formula":{"v":1,"all":[]},"after":"m9","limit":20}`},
		{"a page of matches", MatchPage{
			Messages:   []MessageSummary{{ID: "m1", ReceivedAt: at, ExpiresAt: at, Size: 10, From: []MailAddress{{Address: "a@example.org"}}, Subject: "Hi", Snippet: "Hello"}},
			NeedsModel: []string{"m2"}, Unknown: []string{}, Next: "m3", SearchedTo: at,
		}, `{"messages":[{"id":"m1","received_at":"2026-11-14T10:00:00Z","expires_at":"2026-11-14T10:00:00Z","size":10,"from":[{"address":"a@example.org"}],"subject":"Hi","snippet":"Hello","attachments":0,"confirmation":false}],"needs_model":["m2"],"unknown":[],"next":"m3","searched_to":"2026-11-14T10:00:00Z"}`},
		{"the last page of matches", MatchPage{Messages: []MessageSummary{}, NeedsModel: []string{}, Unknown: []string{}},
			`{"messages":[],"needs_model":[],"unknown":[]}`},
	}
	for _, c := range cases {
		got, err := json.Marshal(c.v)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if string(got) != c.want {
			t.Errorf("%s:\n got %s\nwant %s", c.name, got, c.want)
		}
	}
}

func TestInvalidFormulaIsItsCode(t *testing.T) {
	if !errors.Is(&Error{"invalid_formula", "other words"}, ErrInvalidFormula) {
		t.Fatal("an error with the code invalid_formula is ErrInvalidFormula")
	}
	if errors.Is(ErrInvalidQuery, ErrInvalidFormula) {
		t.Fatal("the inbox's invalid_query is not invalid_formula")
	}
}
