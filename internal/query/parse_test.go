package query_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/query"
)

// Q1 of ADR 0027: valid formulas, each with the canonical JSON it gives.
func TestQ1ValidFormulasAndTheirCanonicalForm(t *testing.T) {
	for _, c := range []struct{ name, in, want string }{
		{"every message", `{"v":1,"all":[]}`, `{"v":1,"all":[]}`},
		{"the ADR's example",
			`{"v": 1, "all": [
			  {"any": [{"var": "from_domain", "op": "is", "value": "holded.com"},
			           {"var": "subject", "op": "contains", "value": "invoice"}]},
			  {"any": [{"var": "received", "op": "since", "value": "2026-01-01T00:00:00Z"}]},
			  {"any": [{"var": "attachments", "op": "exists", "not": true}]}
			]}`,
			`{"v":1,"all":[{"any":[{"var":"from_domain","op":"is","value":"holded.com"},{"var":"subject","op":"contains","value":"invoice"}]},{"any":[{"var":"attachments","op":"exists","not":true}]},{"any":[{"var":"received","op":"since","value":"2026-01-01T00:00:00Z"}]}]}`},
		{"values normalised",
			`{"v":1,"all":[{"any":[{"var":"from_address","op":"is","value":"\"Ann+Tag\"@Bücher.Example."},{"var":"signed_by","op":"is","value":"MAIL.Example.com"},{"var":"subject","op":"contains","value":"  Ｉｎ­voice\n 2026 "}]}]}`,
			`{"v":1,"all":[{"any":[{"var":"from_address","op":"is","value":"ann+tag@xn--bcher-kva.example"},{"var":"signed_by","op":"is","value":"mail.example.com"},{"var":"subject","op":"contains","value":"invoice 2026"}]}]}`},
		{"times in UTC, sizes, types, lists, mailboxes",
			`{"v":1,"all":[{"any":[{"var":"received","op":"before","value":"2026-03-01T09:30:00+01:00"}]},{"any":[{"var":"size","op":"at_least","value":"1048576"}]},{"any":[{"var":"attachment_type","op":"is","value":"Application/*"},{"var":"list","op":"is","value":"News.Example.com"}]},{"any":[{"var":"mailbox","op":"is","value":"mbx_ajcz4qajcz4qajcz4qajcz4qaj"}]}]}`,
			`{"v":1,"all":[{"any":[{"var":"attachment_type","op":"is","value":"application/*"},{"var":"list","op":"is","value":"news.example.com"}]},{"any":[{"var":"received","op":"before","value":"2026-03-01T08:30:00Z"}]},{"any":[{"var":"size","op":"at_least","value":"1048576"}]},{"any":[{"var":"mailbox","op":"is","value":"mbx_ajcz4qajcz4qajcz4qajcz4qaj"}]}]}`},
		{"negated ranges made positive",
			`{"v":1,"all":[{"any":[{"var":"received","op":"since","value":"2026-01-01T00:00:00Z","not":true}]},{"any":[{"var":"size","op":"below","value":"10","not":true}]}]}`,
			`{"v":1,"all":[{"any":[{"var":"received","op":"before","value":"2026-01-01T00:00:00Z"}]},{"any":[{"var":"size","op":"at_least","value":"10"}]}]}`},
		{"model conditions, not false omitted",
			`{"v":1,"all":[{"any":[{"var":"message","op":"mentions","value":"Home Address","not":false},{"var":"message","op":"about","value":"Travel"},{"var":"subject","op":"about","value":"travel","not":true}]}]}`,
			`{"v":1,"all":[{"any":[{"var":"subject","op":"about","value":"travel","not":true},{"var":"message","op":"about","value":"travel"},{"var":"message","op":"mentions","value":"home address"}]}]}`},
		{"every variable once", everyVariable,
			`{"v":1,"all":[{"any":[{"var":"from_address","op":"is","value":"a@example.com"},{"var":"from_domain","op":"is","value":"example.org"},{"var":"recipient_address","op":"is","value":"b@example.com"},{"var":"recipient_domain","op":"is","value":"example.net"},{"var":"signed_by","op":"is","value":"example.com"},{"var":"subject","op":"contains","value":"x1"},{"var":"body","op":"contains","value":"x2"},{"var":"message","op":"about","value":"x3"},{"var":"attachments","op":"exists"},{"var":"list","op":"exists"},{"var":"received","op":"since","value":"2026-01-01T00:00:00Z"},{"var":"size","op":"below","value":"5"},{"var":"mailbox","op":"is","value":"mbx_aaaaaaaaaaaaaaaaaaaaaaaaaa"}]}]}`},
		{"a value with quotes and a backslash", `{"v":1,"all":[{"any":[{"var":"subject","op":"contains","value":"say \"hi\" \\ “there”"}]}]}`,
			`{"v":1,"all":[{"any":[{"var":"subject","op":"contains","value":"say \"hi\" \\ “there”"}]}]}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := canon(t, c.in)
			if s := jsonOf(got); s != c.want {
				t.Errorf("canonical form\n got %s\nwant %s", s, c.want)
			}
			if !strings.HasPrefix(got.ID, "qry_") || len(got.ID) != 30 || got.Never {
				t.Errorf("ID %q, Never %v", got.ID, got.Never)
			}
		})
	}
}

// Every variable, with operators and values that no rewrite relates.
const everyVariable = `{"v":1,"all":[{"any":[
	{"var":"from_address","op":"is","value":"a@example.com"},
	{"var":"from_domain","op":"is","value":"example.org"},
	{"var":"recipient_address","op":"is","value":"b@example.com"},
	{"var":"recipient_domain","op":"is","value":"example.net"},
	{"var":"signed_by","op":"is","value":"example.com"},
	{"var":"subject","op":"contains","value":"x1"},
	{"var":"body","op":"contains","value":"x2"},
	{"var":"message","op":"about","value":"x3"},
	{"var":"attachments","op":"exists"},
	{"var":"list","op":"exists"},
	{"var":"received","op":"since","value":"2026-01-01T00:00:00Z"},
	{"var":"size","op":"below","value":"5"},
	{"var":"mailbox","op":"is","value":"mbx_aaaaaaaaaaaaaaaaaaaaaaaaaa"}]}]}`

// refused asserts that Parse refuses in with reason at at, with the fixed
// message and nothing of the formula's values in the error.
func refused(t *testing.T, in []byte, mailboxes []string, at string, reason contracts.FormulaReason, secrets ...string) {
	t.Helper()
	c, err := query.Parse(in, mailboxes)
	if err == nil {
		t.Fatalf("Parse accepted %q: %s", in, jsonOf(c))
	}
	if !errors.Is(err, contracts.ErrInvalidFormula) {
		t.Fatalf("Parse error %v is not ErrInvalidFormula", err)
	}
	var p *query.Problem
	if !errors.As(err, &p) {
		t.Fatalf("Parse error %T is not a *query.Problem", err)
	}
	if p.At != at || p.Reason != reason {
		t.Errorf("Parse(%.120q) = {at %q, %s}, want {at %q, %s}", in, p.At, p.Reason, at, reason)
	}
	want := contracts.ErrInvalidFormula.Error() + " (" + string(reason) + ` at "` + at + `")`
	if err.Error() != want {
		t.Errorf("error %q, want %q", err.Error(), want)
	}
	for _, s := range secrets {
		if strings.Contains(err.Error(), s) || strings.Contains(p.At, s) {
			t.Errorf("the error %q holds %q", err.Error(), s)
		}
	}
}

func formula(conds ...string) []byte {
	return []byte(`{"v":1,"all":[{"any":[` + strings.Join(conds, ",") + `]}]}`)
}

// Q2: one formula per refusal reason, with its at; the message fixed and
// holding no value.
func TestQ2EveryReasonWithItsPath(t *testing.T) {
	big := bytes.Repeat([]byte(" "), contracts.MaxFormulaBytes)
	groups := func(n int) []byte {
		gs := make([]string, n)
		for i := range gs {
			gs[i] = fmt.Sprintf(`{"any":[{"var":"size","op":"at_least","value":"%d"}]}`, i+1)
		}
		return []byte(`{"v":1,"all":[` + strings.Join(gs, ",") + `]}`)
	}
	conds := func(n int, c string) []byte {
		cs := make([]string, n)
		for i := range cs {
			cs[i] = fmt.Sprintf(c, i+1)
		}
		return formula(cs...)
	}
	mbx := "mbx_aaaaaaaaaaaaaaaaaaaaaaaaaa"
	for _, c := range []struct {
		name   string
		in     []byte
		at     string
		reason contracts.FormulaReason
		secret string
	}{
		{"not_json", []byte(`{"v":1,"all":[`), "", contracts.FormulaNotJSON, ""},
		{"too_large", append([]byte(`{"v":1,"all":[]}`), big...), "", contracts.FormulaTooLarge, ""},
		{"unknown_field", []byte(`{"v":1,"all":[{"any":[{"var":"subject","op":"contains","value":"x","secretkey":1}]}]}`), "all[0].any[0]", contracts.FormulaUnknownField, "secretkey"},
		{"duplicate_field", []byte(`{"v":1,"all":[{"any":[{"var":"subject","op":"contains","value":"x","value":"y"}]}]}`), "all[0].any[0].value", contracts.FormulaDuplicateField, ""},
		{"wrong_type", []byte(`{"v":1,"all":[{"any":[{"var":"subject","op":"contains","value":7}]}]}`), "all[0].any[0].value", contracts.FormulaWrongType, ""},
		{"unsupported_version", []byte(`{"v":2,"all":[]}`), "v", contracts.FormulaUnsupportedVersion, ""},
		{"too_many_groups", groups(contracts.MaxGroups + 1), "all[32]", contracts.FormulaTooManyGroups, ""},
		{"too_many_conditions", conds(contracts.MaxGroupConditions+1, `{"var":"size","op":"at_least","value":"%d"}`), "all[0].any[32]", contracts.FormulaTooManyConditions, ""},
		{"too_many_model", conds(contracts.MaxModelConditions+1, `{"var":"message","op":"about","value":"t%d"}`), "all[0].any[8]", contracts.FormulaTooManyModel, ""},
		{"empty_group", []byte(`{"v":1,"all":[{"any":[]}]}`), "all[0].any", contracts.FormulaEmptyGroup, ""},
		{"unknown_variable", formula(`{"var":"secretvar","op":"is","value":"x"}`), "all[0].any[0].var", contracts.FormulaUnknownVariable, "secretvar"},
		{"unknown_operator", formula(`{"var":"subject","op":"secretop","value":"x"}`), "all[0].any[0].op", contracts.FormulaUnknownOperator, "secretop"},
		{"operator_not_allowed", formula(`{"var":"subject","op":"is","value":"x"}`), "all[0].any[0].op", contracts.FormulaOperatorNotAllowed, ""},
		{"missing_value", formula(`{"var":"subject","op":"contains"}`), "all[0].any[0].value", contracts.FormulaMissingValue, ""},
		{"unexpected_value", formula(`{"var":"attachments","op":"exists","value":"secretvalue"}`), "all[0].any[0].value", contracts.FormulaUnexpectedValue, "secretvalue"},
		{"invalid_value", formula(`{"var":"from_domain","op":"is","value":"secret_value!"}`), "all[0].any[0].value", contracts.FormulaInvalidValue, "secret_value"},
		{"value_too_long", formula(`{"var":"subject","op":"contains","value":"` + strings.Repeat("secret", 17) + `"}`), "all[0].any[0].value", contracts.FormulaValueTooLong, "secret"},
		{"confusable_value", formula(`{"var":"subject","op":"contains","value":"secret invоice"}`), "all[0].any[0].value", contracts.FormulaConfusableValue, "secret"},
		{"unknown_mailbox", formula(`{"var":"mailbox","op":"is","value":"mbx_secretsecretsecretsecret12"}`), "all[0].any[0].value", contracts.FormulaUnknownMailbox, "mbx_secret"},
		{"never_matches", []byte(`{"v":1,"all":[{"any":[{"var":"received","op":"since","value":"2026-03-01T00:00:00Z"}]},{"any":[{"var":"received","op":"before","value":"2026-02-01T00:00:00Z"}]}]}`), "", contracts.FormulaNeverMatches, "2026"},
	} {
		t.Run(c.name, func(t *testing.T) {
			var secrets []string
			if c.secret != "" {
				secrets = append(secrets, c.secret)
			}
			refused(t, c.in, []string{mbx}, c.at, c.reason, secrets...)
		})
	}
}

// The first problem in the order of the formula as sent is the one
// answered, whatever order the checks run in.
func TestQ2TheFirstProblemInTheFormulasOrder(t *testing.T) {
	for _, c := range []struct {
		in     string
		at     string
		reason contracts.FormulaReason
	}{
		{`{"all":[{"any":[{"var":"x","op":"is","value":"y"}]}],"v":2}`, "all[0].any[0].var", contracts.FormulaUnknownVariable},
		{`{"v":2,"all":[{"any":[{"var":"x","op":"is","value":"y"}]}]}`, "v", contracts.FormulaUnsupportedVersion},
		{`{"v":1,"all":[{"any":[{"var":"subject","op":"contains","value":"ok"}]},{"any":[]},{"any":[{"var":"x"}]}]}`, "all[1].any", contracts.FormulaEmptyGroup},
		{`{"v":1,"all":[{"any":[{"value":"secret_value!","op":"zz","var":"from_domain"}]}]}`, "all[0].any[0].op", contracts.FormulaUnknownOperator},
		{`{"v":1,"all":[{"any":[{"value":"x","var":"subject","op":"contains","value":"y"}]}]}`, "all[0].any[0].value", contracts.FormulaDuplicateField},
		{`{"v":1,"all":[{"any":[{"var":"subject","op":"contains","value":"ok"}]}],"extra":1}`, "", contracts.FormulaUnknownField},
		{`{"v":1}`, "all", contracts.FormulaWrongType},
		{`{"all":[]}`, "v", contracts.FormulaUnsupportedVersion},
		{`{"v":1,"all":[{}]}`, "all[0].any", contracts.FormulaWrongType},
		{`{"v":1,"all":[{"any":[{"op":"is","value":"y"}]}]}`, "all[0].any[0].var", contracts.FormulaUnknownVariable},
		{`{"v":1,"all":[{"any":[{"var":"subject","value":"y"}]}]}`, "all[0].any[0].op", contracts.FormulaUnknownOperator},
		{`{"v":"1","all":[]}`, "v", contracts.FormulaWrongType},
		{`{"v":1.0,"all":[]}`, "v", contracts.FormulaUnsupportedVersion},
		{`{"v":1,"all":[{"any":[{"var":"subject","op":"contains","value":""}]}]}`, "all[0].any[0].value", contracts.FormulaInvalidValue},
		{`{"v":1,"all":[{"any":[{"var":"subject","op":"contains","value":"x","not":"yes"}]}]}`, "all[0].any[0].not", contracts.FormulaWrongType},
		{`[]`, "", contracts.FormulaNotJSON},
		{``, "", contracts.FormulaNotJSON},
	} {
		refused(t, []byte(c.in), nil, c.at, c.reason)
	}
}

// Q3: strictness. Nothing here panics, and each is refused.
func TestQ3Strictness(t *testing.T) {
	ok := `{"v":1,"all":[{"any":[{"var":"subject","op":"contains","value":"x"}]}]}`
	for _, c := range []struct {
		name   string
		in     string
		at     string
		reason contracts.FormulaReason
	}{
		{"a member twice", `{"v":1,"v":2,"all":[]}`, "v", contracts.FormulaDuplicateField},
		{"a member twice, spelled with an escape", `{"v":1,"v":1,"all":[]}`, "v", contracts.FormulaDuplicateField},
		{"all twice", `{"v":1,"all":[],"all":[]}`, "all", contracts.FormulaDuplicateField},
		{"any twice", `{"v":1,"all":[{"any":[{"var":"subject","op":"contains","value":"x"}],"any":[]}]}`, "all[0].any", contracts.FormulaDuplicateField},
		{"an unknown member", `{"v":1,"all":[],"x":1}`, "", contracts.FormulaUnknownField},
		{"an unknown member of a group", `{"v":1,"all":[{"any":[],"x":1}]}`, "all[0].any", contracts.FormulaEmptyGroup},
		{"an unknown member of a group first", `{"v":1,"all":[{"x":1,"any":[]}]}`, "all[0]", contracts.FormulaUnknownField},
		{"null for v", `{"v":null,"all":[]}`, "v", contracts.FormulaWrongType},
		{"null for all", `{"v":1,"all":null}`, "all", contracts.FormulaWrongType},
		{"null for a group", `{"v":1,"all":[null]}`, "all[0]", contracts.FormulaWrongType},
		{"null for any", `{"v":1,"all":[{"any":null}]}`, "all[0].any", contracts.FormulaWrongType},
		{"null for a condition", `{"v":1,"all":[{"any":[null]}]}`, "all[0].any[0]", contracts.FormulaWrongType},
		{"null for var", `{"v":1,"all":[{"any":[{"var":null}]}]}`, "all[0].any[0].var", contracts.FormulaWrongType},
		{"null for value", `{"v":1,"all":[{"any":[{"var":"subject","op":"contains","value":null}]}]}`, "all[0].any[0].value", contracts.FormulaWrongType},
		{"null for not", `{"v":1,"all":[{"any":[{"var":"subject","op":"contains","value":"x","not":null}]}]}`, "all[0].any[0].not", contracts.FormulaWrongType},
		{"a number for value", `{"v":1,"all":[{"any":[{"var":"size","op":"at_least","value":10}]}]}`, "all[0].any[0].value", contracts.FormulaWrongType},
		{"an object for var", `{"v":1,"all":[{"any":[{"var":{"a":1}}]}]}`, "all[0].any[0].var", contracts.FormulaWrongType},
		{"a group inside a group", `{"v":1,"all":[{"any":[{"any":[{"var":"subject","op":"contains","value":"x"}]}]}]}`, "all[0].any[0].any", contracts.FormulaWrongType},
		{"an array as a group", `{"v":1,"all":[[{"var":"subject","op":"contains","value":"x"}]]}`, "all[0]", contracts.FormulaWrongType},
		{"an array as a condition", `{"v":1,"all":[{"any":[[1]]}]}`, "all[0].any[0]", contracts.FormulaWrongType},
		{"an object as all", `{"v":1,"all":{}}`, "all", contracts.FormulaWrongType},
		{"a byte order mark", "\xef\xbb\xbf" + ok, "", contracts.FormulaNotJSON},
		{"invalid UTF-8 in a value", strings.Replace(ok, `"x"`, "\"x\xff\"", 1), "", contracts.FormulaNotJSON},
		{"invalid UTF-8 in a key", strings.Replace(ok, `"var"`, "\"va\xc3\"", 1), "", contracts.FormulaNotJSON},
		{"a lone low surrogate escaped", strings.Replace(ok, `"x"`, `"x\udc00"`, 1), "", contracts.FormulaNotJSON},
		{"a lone high surrogate escaped", strings.Replace(ok, `"x"`, `"x\ud800y"`, 1), "", contracts.FormulaNotJSON},
		{"a high surrogate before another escape", strings.Replace(ok, `"x"`, `"x\ud800A"`, 1), "", contracts.FormulaNotJSON},
		{"a high surrogate at the end", `{"v":1,"all":[{"any":[{"var":"subject","op":"contains","value":"\ud800`, "", contracts.FormulaNotJSON},
		{"a broken escape", `{"v":1,"all":[{"any":[{"var":"subject","op":"contains","value":"\u00zz"}]}]}`, "", contracts.FormulaNotJSON},
		{"trailing data", ok + `{}`, "", contracts.FormulaNotJSON},
		{"trailing garbage", ok + ` x`, "", contracts.FormulaNotJSON},
		{"a string", `"x"`, "", contracts.FormulaNotJSON},
		{"two commas", `{"v":1,,"all":[]}`, "", contracts.FormulaNotJSON},
		{"unclosed", `{"v":1,"all":[{"any":[{"var":"subject","op":"contains","value":"x"}]}`, "", contracts.FormulaNotJSON},
		{"unclosed condition", `{"v":1,"all":[{"any":[{"var":"subject"`, "", contracts.FormulaNotJSON},
		{"unclosed in v", `{"v":`, "", contracts.FormulaNotJSON},
		{"unclosed in any", `{"v":1,"all":[{"any":`, "", contracts.FormulaNotJSON},
		{"unclosed in a group", `{"v":1,"all":[{`, "", contracts.FormulaNotJSON},
		{"unclosed in a member's value", `{"v":1,"all":[{"any":[{"var":`, "", contracts.FormulaNotJSON},
		{"unclosed after a condition", `{"v":1,"all":[{"any":[{"var":"subject","op":"contains","value":"x"}`, "", contracts.FormulaNotJSON},
	} {
		t.Run(c.name, func(t *testing.T) { refused(t, []byte(c.in), nil, c.at, c.reason) })
	}
	// A pair of surrogates escaped is one code point, accepted.
	got := canon(t, strings.Replace(ok, `"x"`, `"x😀"`, 1))
	if v := got.Formula.All[0].Any[0].Value; v != "x😀" {
		t.Errorf("escaped pair read as %q", v)
	}
	// Exactly 64 KiB is read; one byte more is too large.
	pad := contracts.MaxFormulaBytes - len(ok)
	canon(t, ok+strings.Repeat(" ", pad))
	refused(t, []byte(ok+strings.Repeat(" ", pad+1)), nil, "", contracts.FormulaTooLarge)
}

// Q5: every limit at its value and one past it.
func TestQ5EveryLimitAtItsValueAndOnePast(t *testing.T) {
	cond := func(i int) string { return fmt.Sprintf(`{"var":"size","op":"at_least","value":"%d"}`, i+1) }
	groupsOf := func(n, per int) []byte {
		gs := make([]string, n)
		k := 0
		for i := range gs {
			cs := make([]string, per)
			for j := range cs {
				// Different variables in each group, so no rewrite merges them.
				cs[j] = fmt.Sprintf(`{"var":"subject","op":"contains","value":"g%02dc%02d"}`, i, j)
				k++
			}
			gs[i] = `{"any":[` + strings.Join(cs, ",") + `]}`
		}
		return []byte(`{"v":1,"all":[` + strings.Join(gs, ",") + `]}`)
	}
	// Groups: 32 accepted, 33 refused.
	if c := canon(t, string(groupsOf(contracts.MaxGroups, 1))); len(c.Formula.All) != contracts.MaxGroups {
		t.Errorf("32 groups gave %d", len(c.Formula.All))
	}
	refused(t, groupsOf(contracts.MaxGroups+1, 1), nil, "all[32]", contracts.FormulaTooManyGroups)
	// Conditions in a group: 32 accepted, 33 refused.
	canon(t, string(groupsOf(1, contracts.MaxGroupConditions)))
	refused(t, groupsOf(1, contracts.MaxGroupConditions+1), nil, "all[0].any[32]", contracts.FormulaTooManyConditions)
	// Conditions in a formula: 64 accepted (two groups of 32), 65 refused.
	if c := canon(t, string(groupsOf(2, 32))); len(c.Formula.All) != 2 || len(c.Formula.All[1].Any) != 32 {
		t.Errorf("64 conditions gave %s", jsonOf(c))
	}
	refused(t, append(groupsOf(2, 32)[:len(groupsOf(2, 32))-2], []byte(`,{"any":[`+cond(0)+`]}]}`)...), nil, "all[2].any[0]", contracts.FormulaTooManyConditions)
	// Conditions a model decides: 8 accepted, 9 refused.
	model := func(n int) []byte {
		cs := make([]string, n)
		for i := range cs {
			cs[i] = fmt.Sprintf(`{"var":"message","op":"mentions","value":"topic %d"}`, i)
		}
		return formula(cs...)
	}
	canon(t, string(model(contracts.MaxModelConditions)))
	refused(t, model(contracts.MaxModelConditions+1), nil, "all[0].any[8]", contracts.FormulaTooManyModel)
	// A text: 100 characters and 400 bytes accepted; one more of either refused.
	text := func(s string) []byte { return formula(`{"var":"subject","op":"contains","value":"` + s + `"}`) }
	canon(t, string(text(strings.Repeat("a", 100))))
	refused(t, text(strings.Repeat("a", 101)), nil, "all[0].any[0].value", contracts.FormulaValueTooLong)
	canon(t, string(text(strings.Repeat("𝒜", 100)))) // 4 bytes each: 400 bytes
	refused(t, text(strings.Repeat("𝒜", 100)+"a"), nil, "all[0].any[0].value", contracts.FormulaValueTooLong)
	canon(t, string(text(strings.Repeat("ab ", 33)+"a")))
	// A topic, likewise.
	topic := func(s string) []byte { return formula(`{"var":"message","op":"about","value":"` + s + `"}`) }
	canon(t, string(topic(strings.Repeat("t", 100))))
	refused(t, topic(strings.Repeat("t", 101)), nil, "all[0].any[0].value", contracts.FormulaValueTooLong)
	// A domain: labels of 63, 253 in all.
	dom := func(s string) []byte { return formula(`{"var":"from_domain","op":"is","value":"` + s + `"}`) }
	l63 := strings.Repeat("a", 63)
	canon(t, string(dom(l63+".com")))
	refused(t, dom(l63+"a.com"), nil, "all[0].any[0].value", contracts.FormulaValueTooLong)
	d253 := strings.Join([]string{l63, l63, l63, strings.Repeat("b", 61)}, ".")
	canon(t, string(dom(d253)))
	refused(t, dom(d253+"b"), nil, "all[0].any[0].value", contracts.FormulaValueTooLong)
	refused(t, dom(strings.Repeat("a.", 600)+"com"), nil, "all[0].any[0].value", contracts.FormulaValueTooLong)
	// An address: a local part of 64 bytes.
	addr := func(s string) []byte { return formula(`{"var":"from_address","op":"is","value":"` + s + `"}`) }
	canon(t, string(addr(strings.Repeat("l", 64)+"@example.com")))
	refused(t, addr(strings.Repeat("l", 65)+"@example.com"), nil, "all[0].any[0].value", contracts.FormulaValueTooLong)
	refused(t, addr(strings.Repeat("l", 300)+"@example.com"), nil, "all[0].any[0].value", contracts.FormulaValueTooLong)
	// A media type: a subtype of 127.
	typ := func(s string) []byte { return formula(`{"var":"attachment_type","op":"is","value":"application/` + s + `"}`) }
	canon(t, string(typ(strings.Repeat("x", 127))))
	refused(t, typ(strings.Repeat("x", 128)), nil, "all[0].any[0].value", contracts.FormulaValueTooLong)
	// A list: 255 bytes.
	list := func(s string) []byte { return formula(`{"var":"list","op":"is","value":"` + s + `"}`) }
	canon(t, string(list(strings.Repeat("x", 255))))
	refused(t, list(strings.Repeat("x", 256)), nil, "all[0].any[0].value", contracts.FormulaValueTooLong)
	// A size: 32 MiB, and one more.
	size := func(s string) []byte { return formula(`{"var":"size","op":"below","value":"` + s + `"}`) }
	canon(t, string(size("33554432")))
	refused(t, size("33554433"), nil, "all[0].any[0].value", contracts.FormulaInvalidValue)
}

// Q4: FuzzParse. Never panics; every accepted input's canonical JSON parses
// to itself, byte for byte; the canonical JSON and identifier are
// deterministic.
func FuzzParse(f *testing.F) {
	for _, s := range []string{
		`{"v":1,"all":[]}`, everyVariable,
		`{"v":1,"all":[{"any":[{"var":"from_address","op":"is","value":"\"Ann\"@Bücher.Example."}]}]}`,
		`{"v":1,"all":[{"any":[{"var":"received","op":"since","value":"2026-01-01T00:00:00+05:00","not":true},{"var":"received","op":"since","value":"2026-01-01T00:00:00Z"}]}]}`,
		`{"v":1,"all":[{"any":[{"var":"subject","op":"contains","value":"invoice"},{"var":"subject","op":"contains","value":"invoices"}]},{"any":[{"var":"subject","op":"contains","value":"invoice"}]}]}`,
		`{"v":1,"all":[{"any":[{"var":"mailbox","op":"is","value":"mbx_aaaaaaaaaaaaaaaaaaaaaaaaaa"}]},{"any":[{"var":"mailbox","op":"is","value":"mbx_bbbbbbbbbbbbbbbbbbbbbbbbbb"},{"var":"size","op":"below","value":"9"}]}]}`,
		`{"v":1,"all":[{"any":[{"var":"attachment_type","op":"is","value":"image/*"},{"var":"attachment_type","op":"is","value":"image/png"},{"var":"attachments","op":"exists"}]}]}`,
		`{"v":1,"all":[{"any":[{"var":"subject","op":"contains","value":"a­́"}]}]}`,
	} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		c, err := query.Parse(data, nil)
		if err != nil {
			var p *query.Problem
			if !errors.As(err, &p) || !errors.Is(err, contracts.ErrInvalidFormula) {
				t.Fatalf("a refusal that is not a Problem: %v", err)
			}
			return
		}
		b := jsonOf(c)
		again, err := query.Parse([]byte(b), nil)
		if err != nil {
			t.Fatalf("the canonical form %s does not parse: %v", b, err)
		}
		if jsonOf(again) != b || again.ID != c.ID {
			t.Fatalf("the canonical form %s parses to %s", b, jsonOf(again))
		}
		if c2, _ := query.Parse(data, nil); jsonOf(c2) != b || c2.ID != c.ID {
			t.Fatalf("two parses of one input differ")
		}
		var f contracts.Formula
		if err := json.Unmarshal([]byte(b), &f); err != nil {
			t.Fatal(err)
		}
		if c3 := canonOf(t, f); jsonOf(c3) != b {
			t.Fatalf("Canonicalise of the canonical form differs: %s", jsonOf(c3))
		}
	})
}
