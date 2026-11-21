package rules

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
)

const (
	claude  = "https://claude.ai/oauth/mcp-oauth-client-metadata"
	chatgpt = "https://chatgpt.com/oauth/client"
	later   = "https://app.example/client"
	notice  = "2026-11-20"
)

func subjectContains(v string) contracts.Formula {
	return contracts.Formula{V: 1, All: []contracts.Group{{Any: []contracts.Condition{{Var: contracts.VarSubject, Op: contracts.OpContains, Value: v}}}}}
}

func stored(id string, effect contracts.RuleEffect, scope contracts.RuleScope, criteria ...contracts.Formula) Stored {
	s := Stored{ID: id, Version: 1, Draft: contracts.RuleDraft{Name: id, Criteria: criteria, Effect: effect, Scope: scope, On: true}}
	if effect == contracts.EffectRemove {
		s.Draft.Removal = &contracts.RuleRemoval{Shapes: []contracts.ShapeKind{contracts.ShapePhoneNumber}}
	}
	return s
}

// U3: which rules a connection applies, and in which order: the default first
// unless off, then the rules that are on and whose scope covers the client,
// those that hold before those that remove; a client connected later is
// covered by "all" and "all but", never by "only".
func TestU3ForIsTheScopeInOrder(t *testing.T) {
	rec := &Record{V: RecordFormat, Account: "acc", Notice: &contracts.RuleNoticeAgreement{Version: notice}, Rules: []Stored{
		stored("rul_remove_all", contracts.EffectRemove, contracts.RuleScope{To: contracts.ScopeAll}, subjectContains("a")),
		stored("rul_hold_only_claude", contracts.EffectHold, contracts.RuleScope{To: contracts.ScopeOnly, Clients: []string{claude}}, subjectContains("b")),
		stored("rul_hold_but_claude", contracts.EffectHold, contracts.RuleScope{To: contracts.ScopeAllExcept, Clients: []string{claude}}, subjectContains("c")),
	}}
	off := stored("rul_off", contracts.EffectHold, contracts.RuleScope{To: contracts.ScopeAll}, subjectContains("d"))
	off.Draft.On = false
	rec.Rules = append(rec.Rules, off)
	for client, want := range map[string][]string{
		claude:  {DefaultID, "rul_hold_only_claude", "rul_remove_all"},
		chatgpt: {DefaultID, "rul_hold_but_claude", "rul_remove_all"},
		later:   {DefaultID, "rul_hold_but_claude", "rul_remove_all"},
	} {
		got, err := rec.For(client, notice)
		if err != nil {
			t.Fatal(err)
		}
		if ids := idsOf(got); strings.Join(ids, " ") != strings.Join(want, " ") {
			t.Errorf("%s: %v, want %v", client, ids, want)
		}
	}
	rec.DefaultOff = true
	got, err := rec.For(claude, notice)
	if err != nil || idsOf(got)[0] == DefaultID {
		t.Fatalf("the default turned off still applies: %v, %v", idsOf(got), err)
	}
	var none *Record
	got, err = none.For(claude, notice)
	if err != nil || len(got) != 1 || got[0].ID != DefaultID {
		t.Fatalf("an account without a record: %v, %v", idsOf(got), err)
	}
}

func idsOf(rs []Rule) []string {
	var ids []string
	for _, r := range rs {
		ids = append(ids, r.ID)
	}
	return ids
}

// U3: a scope this release cannot read covers every client: a rule protects.
func TestU3AnUnknownScopeCoversEveryone(t *testing.T) {
	if !(Scope{To: "everyone_but_tuesdays", Clients: []string{claude}}).Covers(claude) {
		t.Fatal("an unknown scope let a client out")
	}
	if (Scope{To: contracts.ScopeOnly}).Covers(claude) || !(Scope{To: contracts.ScopeAllExcept}).Covers(claude) {
		t.Fatal("an empty list")
	}
}

// U5: a rule a model decides any of asks nothing, and applies, unless the
// account agreed to the rules' notice in force and the rule was saved under
// it; a rule a model decides none of is unaffected; the default never.
func TestU5ARuleUnderAnotherNoticeAsksNothing(t *testing.T) {
	about := contracts.Formula{V: 1, All: []contracts.Group{{Any: []contracts.Condition{{Var: contracts.VarMessage, Op: contracts.OpAbout, Value: "travel"}}}}}
	model := stored("rul_model", contracts.EffectHold, contracts.RuleScope{To: contracts.ScopeAll}, about)
	model.Notice = notice
	exact := stored("rul_exact", contracts.EffectHold, contracts.RuleScope{To: contracts.ScopeAll}, subjectContains("x"))
	meaning := stored("rul_meaning", contracts.EffectRemove, contracts.RuleScope{To: contracts.ScopeAll}, contracts.Formula{V: 1, All: []contracts.Group{}})
	meaning.Draft.Removal = &contracts.RuleRemoval{Topics: []string{"A Home  Address"}}
	meaning.Notice = notice
	for _, c := range []struct {
		name     string
		agreed   string
		inForce  string
		modelOff bool
	}{
		{"agreed and saved under the notice in force", notice, notice, false},
		{"the account agreed to an older one", "2026-11-01", notice, true},
		{"a release changed the notice", notice, "2026-12-01", true},
		{"never agreed", "", notice, true},
	} {
		rec := &Record{Rules: []Stored{model, exact, meaning}}
		if c.agreed != "" {
			rec.Notice = &contracts.RuleNoticeAgreement{Version: c.agreed, At: time.Now()}
		}
		got, err := rec.For(claude, c.inForce)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range got {
			want := c.modelOff && (r.ID == "rul_model" || r.ID == "rul_meaning")
			if r.ModelOff != want {
				t.Errorf("%s: %s ModelOff %v", c.name, r.ID, r.ModelOff)
			}
		}
	}
	r, err := Compile(meaning)
	if err != nil || len(r.Removal.Topics) != 1 || r.Removal.Topics[0].Topic != "a home address" || r.Removal.Topics[0].Op != contracts.OpMentions {
		t.Fatalf("a topic is normalised as the query language's: %+v, %v", r.Removal, err)
	}
	if !r.Removal.Attachments() || !r.Removal.Sender() {
		t.Fatal("a removal by meaning keeps the attachments or the sender it does not read")
	}
	// Finding 3: shapes take the sender out too; only parts that name
	// neither leave it.
	for rm, want := range map[*Removal]bool{
		{Shapes: []contracts.ShapeKind{contracts.ShapeEmailAddress}}: true,
		{Parts: []contracts.Part{contracts.PartSender}}:              true,
		{Parts: []contracts.Part{contracts.PartBody}}:                false,
		nil: false,
	} {
		if rm.Sender() != want {
			t.Errorf("%+v takes the sender out: %v", rm, !want)
		}
	}
}

// U3: a stored rule that does not compile is an error for the whole account,
// never a rule skipped.
func TestU3ARuleThatCannotBeReadGivesNothing(t *testing.T) {
	never := contracts.Formula{V: 1, All: []contracts.Group{{Any: []contracts.Condition{
		{Var: contracts.VarMailbox, Op: contracts.OpIs, Value: "mbx_aaaaaaaaaaaaaaaaaaaaaaaaaa"},
	}}, {Any: []contracts.Condition{
		{Var: contracts.VarMailbox, Op: contracts.OpIs, Value: "mbx_bbbbbbbbbbbbbbbbbbbbbbbbbb"},
	}}}}
	for name, s := range map[string]Stored{
		"no criteria":            {ID: "r", Draft: contracts.RuleDraft{Effect: contracts.EffectHold, On: true}},
		"a criteria never true":  stored("r", contracts.EffectHold, contracts.RuleScope{To: contracts.ScopeAll}, never),
		"an unknown effect":      {ID: "r", Draft: contracts.RuleDraft{Effect: "blur", On: true, Criteria: []contracts.Formula{subjectContains("x")}}},
		"a removal that is none": {ID: "r", Draft: contracts.RuleDraft{Effect: contracts.EffectRemove, Removal: &contracts.RuleRemoval{}, On: true, Criteria: []contracts.Formula{subjectContains("x")}}},
		"an unknown shape":       {ID: "r", Draft: contracts.RuleDraft{Effect: contracts.EffectRemove, Removal: &contracts.RuleRemoval{Shapes: []contracts.ShapeKind{"iban"}}, On: true, Criteria: []contracts.Formula{subjectContains("x")}}},
		"an unknown part":        {ID: "r", Draft: contracts.RuleDraft{Effect: contracts.EffectRemove, Removal: &contracts.RuleRemoval{Parts: []contracts.Part{"headers"}}, On: true, Criteria: []contracts.Formula{subjectContains("x")}}},
		"a hold that removes":    {ID: "r", Draft: contracts.RuleDraft{Effect: contracts.EffectHold, Removal: &contracts.RuleRemoval{Parts: []contracts.Part{contracts.PartBody}}, On: true, Criteria: []contracts.Formula{subjectContains("x")}}},
	} {
		if _, err := (&Record{Rules: []Stored{s}}).For(claude, notice); err == nil {
			t.Errorf("%s: answered rules", name)
		}
	}
}

// U2's names and bounds: the record's name in its epoch; a meaning decision's
// reference apart from the rule's own, bound to the text's and the shapes'
// versions; the record's document as written.
func TestU2TheRecordsNamesAndShape(t *testing.T) {
	if n := RecordName("acc_x", 3); n != "rules-acc_x-3" {
		t.Fatalf("named %q", n)
	}
	r := Ref{ID: "rul_x", Version: 4}
	m := MeaningRef(r, 1, 2)
	if m == r || m.Version != 4 || m.ID != "rul_x#meaning-t1-p2" || MeaningRef(r, 1, 3) == m || MeaningRef(r, 2, 2) == m {
		t.Fatalf("meaning ref %+v", m)
	}
	if MeaningRef(Ref{ID: "rul_x", Version: 5}, 1, 2) == m {
		t.Fatal("a new version of the rule met the old one's meaning decision")
	}
	// Finding 6: an epoch rolls over below the strict read's cold bound,
	// with room for the write that rolls it and its removal.
	if !Rotate(RecordVersionsMax-1) || Rotate(RecordVersionsMax-2) || RecordVersionsMax+2 > 500 {
		t.Fatalf("rotate at %d", RecordVersionsMax)
	}
	b, err := json.Marshal(Record{V: RecordFormat, Account: "acc_x", Rules: []Stored{}})
	if err != nil || string(b) != `{"v":1,"account":"acc_x","epoch":0,"rules":[]}` {
		t.Fatalf("written %s, %v", b, err)
	}
	if RecordBytesMax < contracts.MaxRules*contracts.MaxRuleBytes+16<<10 {
		t.Fatal("the record's bound does not hold every rule at its largest")
	}
}
