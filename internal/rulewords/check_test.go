package rulewords

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/permaily-org/permaily/internal/contracts"
)

// hold is an answer that holds mail from bank.example.
const hold = `{"possible":true,"name":"Bank mail","effect":"hold","criteria":[{"all":[{"any":[{"var":"from_domain","op":"is","value":"bank.example","not":false}]}]}],"removal":null}`

// remove is an answer that removes phone numbers and a topic from all mail.
const remove = `{"possible":true,"name":"No phones","effect":"remove","criteria":[{"all":[]}],"removal":{"parts":[],"shapes":["phone_number","email_address"],"topics":["A Home Address"]}}`

// TestU12AUsableAnswerIsADraftForEveryConnection: a usable answer is a
// draft for every connection, on, its criteria and topics canonical and its
// lists in order; nothing of the answer's but its fields.
func TestU12AUsableAnswerIsADraftForEveryConnection(t *testing.T) {
	d, err := Read(hold)
	if err != nil {
		t.Fatal(err)
	}
	want := contracts.RuleDraft{
		Name: "Bank mail", Effect: contracts.EffectHold, On: true, Scope: contracts.RuleScope{To: contracts.ScopeAll},
		Criteria: []contracts.Formula{{V: 1, All: []contracts.Group{{Any: []contracts.Condition{{Var: contracts.VarFromDomain, Op: contracts.OpIs, Value: "bank.example"}}}}}},
	}
	if !reflect.DeepEqual(d, want) {
		t.Errorf("Read = %+v, want %+v", d, want)
	}
	d, err = Read(remove)
	if err != nil {
		t.Fatal(err)
	}
	if d.Effect != contracts.EffectRemove || d.Removal == nil || d.Removal.Parts != nil ||
		!reflect.DeepEqual(d.Removal.Shapes, []contracts.ShapeKind{contracts.ShapeEmailAddress, contracts.ShapePhoneNumber}) ||
		len(d.Removal.Topics) != 1 || d.Removal.Topics[0] != "a home address" || len(d.Criteria) != 1 || len(d.Criteria[0].All) != 0 {
		t.Errorf("Read = %+v %+v", d, d.Removal)
	}
	if d.Scope.To != contracts.ScopeAll || len(d.Scope.Clients) != 0 || !d.On {
		t.Error("a proposal is for every connection and on")
	}
	// Exists takes no value: null is none.
	d, err = Read(`{"possible":true,"name":"Attachments","effect":"hold","criteria":[{"all":[{"any":[{"var":"attachments","op":"exists","value":null,"not":false}]}]}],"removal":null}`)
	if err != nil || d.Criteria[0].All[0].Any[0].Value != "" {
		t.Errorf("exists with a null value: %v %+v", err, d)
	}
}

// TestU12AnUnusableAnswerProposesNothing: every answer outside the schema,
// past a bound, matching no mail, naming a mailbox, an unknown shape or
// part, or an effect with the wrong removal is contracts.ErrRuleWordsUnusable,
// and no draft.
func TestU12AnUnusableAnswerProposesNothing(t *testing.T) {
	cond := func(c string) string {
		return `{"possible":true,"name":"n","effect":"hold","criteria":[{"all":[{"any":[` + c + `]}]}],"removal":null}`
	}
	criteria := func(n int) string {
		fs := make([]string, n)
		for i := range fs {
			fs[i] = `{"all":[{"any":[{"var":"from_domain","op":"is","value":"b` + string(rune('a'+i)) + `.example","not":false}]}]}`
		}
		return `{"possible":true,"name":"n","effect":"hold","criteria":[` + strings.Join(fs, ",") + `],"removal":null}`
	}
	model := func(n int) string {
		cs := make([]string, n)
		for i := range cs {
			cs[i] = `{"var":"message","op":"mentions","value":"topic ` + string(rune('a'+i)) + `","not":false}`
		}
		return cond(strings.Join(cs, ","))
	}
	removal := func(r string) string {
		return `{"possible":true,"name":"n","effect":"remove","criteria":[{"all":[]}],"removal":` + r + `}`
	}
	for name, text := range map[string]string{
		"not JSON":              `not json`,
		"empty":                 ``,
		"an array":              `[]`,
		"text after it":         hold + ` {}`,
		"two answers":           hold + hold,
		"an unknown member":     strings.Replace(hold, `"possible":true,`, `"possible":true,"why":"because",`, 1),
		"a member missing":      strings.Replace(hold, `"possible":true,`, ``, 1),
		"a member twice":        strings.Replace(hold, `"possible":true,`, `"possible":true,"possible":true,`, 1),
		"not possible":          strings.Replace(hold, `"possible":true`, `"possible":false`, 1),
		"possible null":         strings.Replace(hold, `"possible":true`, `"possible":null`, 1),
		"an empty name":         strings.Replace(hold, `"Bank mail"`, `"  "`, 1),
		"a name too long":       strings.Replace(hold, `"Bank mail"`, `"`+strings.Repeat("n", 61)+`"`, 1),
		"a name with a control": strings.Replace(hold, `"Bank mail"`, `"Bank\u0007"`, 1),
		"a name with U+FFFD":    strings.Replace(hold, `"Bank mail"`, `"Bank �"`, 1),
		"no criteria":           strings.Replace(hold, `[{"all":[{"any":[{"var":"from_domain","op":"is","value":"bank.example","not":false}]}]}]`, `[]`, 1),
		"criteria null":         strings.Replace(hold, `[{"all":[{"any":[{"var":"from_domain","op":"is","value":"bank.example","not":false}]}]}]`, `null`, 1),
		"9 criteria":            criteria(9),
		"9 model conditions":    model(9),
		"an empty group":        `{"possible":true,"name":"n","effect":"hold","criteria":[{"all":[{"any":[]}]}],"removal":null}`,
		"a member of a formula": `{"possible":true,"name":"n","effect":"hold","criteria":[{"v":1,"all":[]}],"removal":null}`,
		"never matches":         `{"possible":true,"name":"n","effect":"hold","criteria":[{"all":[{"any":[{"var":"received","op":"since","value":"2026-03-01T00:00:00Z","not":false}]},{"any":[{"var":"received","op":"before","value":"2026-02-01T00:00:00Z","not":false}]}]}],"removal":null}`,
		"an invented mailbox":   cond(`{"var":"mailbox","op":"is","value":"work","not":false}`),
		"an unknown variable":   cond(`{"var":"sender_name","op":"is","value":"x","not":false}`),
		"an unknown operator":   cond(`{"var":"subject","op":"matches","value":"x","not":false}`),
		"an operator not taken": cond(`{"var":"from_domain","op":"contains","value":"x","not":false}`),
		"a value not its type":  cond(`{"var":"size","op":"at_least","value":"10 MB","not":false}`),
		"a value with U+FFFD":   cond(`{"var":"subject","op":"contains","value":"pay�","not":false}`),
		"a condition member":    cond(`{"var":"subject","op":"contains","value":"x","not":false,"why":"y"}`),
		"a condition short":     cond(`{"var":"subject","op":"contains","value":"x"}`),
		"a hold with removal":   strings.Replace(hold, `"removal":null`, `"removal":{"parts":["body"],"shapes":[],"topics":[]}`, 1),
		"an unknown effect":     strings.Replace(hold, `"hold"`, `"delete"`, 1),
		"a removal missing":     removal(`null`),
		"a removal empty":       removal(`{"parts":[],"shapes":[],"topics":[]}`),
		"an unknown shape":      removal(`{"parts":[],"shapes":["ssn"],"topics":[]}`),
		"a shape twice":         removal(`{"parts":[],"shapes":["link","link"],"topics":[]}`),
		"an unknown part":       removal(`{"parts":["headers"],"shapes":[],"topics":[]}`),
		"a part twice":          removal(`{"parts":["body","body"],"shapes":[],"topics":[]}`),
		"a list null":           removal(`{"parts":null,"shapes":["link"],"topics":[]}`),
		"4 topics":              removal(`{"parts":[],"shapes":[],"topics":["a","b","c","d"]}`),
		"a topic twice":         removal(`{"parts":[],"shapes":[],"topics":["Health","health"]}`),
		"an empty topic":        removal(`{"parts":[],"shapes":[],"topics":[" "]}`),
		"a topic too long":      removal(`{"parts":[],"shapes":[],"topics":["` + strings.Repeat("t", 101) + `"]}`),
		"a topic with U+FFFD":   removal(`{"parts":[],"shapes":[],"topics":["salud�"]}`),
		"a removal member":      removal(`{"parts":[],"shapes":["link"],"topics":[],"all":true}`),
	} {
		d, err := Read(text)
		if !errors.Is(err, contracts.ErrRuleWordsUnusable) || !reflect.DeepEqual(d, contracts.RuleDraft{}) {
			t.Errorf("%s: Read = %+v, %v; want rule_words_unusable and no draft", name, d, err)
		}
	}
}

// TestU12AnAnswerIsWithinTheRulesBytes: the largest answer the schema's
// bounds allow still fits contracts.MaxRuleBytes or is refused.
func TestU12AnAnswerIsWithinTheRulesBytes(t *testing.T) {
	long := strings.Repeat("w", 100)
	var fs []string
	for range contracts.MaxRuleCriteria {
		var gs []string
		for range 4 {
			gs = append(gs, `{"any":[{"var":"subject","op":"contains","value":"`+long+`","not":false}]}`)
		}
		fs = append(fs, `{"all":[`+strings.Join(gs, ",")+`]}`)
	}
	text := `{"possible":true,"name":"n","effect":"hold","criteria":[` + strings.Join(fs, ",") + `],"removal":null}`
	d, err := Read(text)
	if err == nil {
		b, _ := json.Marshal(d)
		if len(b) > contracts.MaxRuleBytes {
			t.Errorf("a draft of %d bytes was proposed", len(b))
		}
	} else if !errors.Is(err, contracts.ErrRuleWordsUnusable) {
		t.Error(err)
	}
}

// TestU12ANullFieldIsUnusable: a member the schema never lets be null
// (every one but the answer's removal and a condition's value), or a null
// in a list, is refused as malformed rather than read as its zero value;
// a condition's value is null only for exists.
func TestU12ANullFieldIsUnusable(t *testing.T) {
	cond := func(c string) string {
		return `{"possible":true,"name":"n","effect":"hold","criteria":[{"all":[{"any":[` + c + `]}]}],"removal":null}`
	}
	removal := func(r string) string {
		return `{"possible":true,"name":"n","effect":"remove","criteria":[{"all":[]}],"removal":` + r + `}`
	}
	for name, text := range map[string]string{
		"name null":            strings.Replace(hold, `"Bank mail"`, `null`, 1),
		"effect null":          strings.Replace(hold, `"hold"`, `null`, 1),
		"all null":             `{"possible":true,"name":"n","effect":"hold","criteria":[{"all":null}],"removal":null}`,
		"any null":             `{"possible":true,"name":"n","effect":"hold","criteria":[{"all":[{"any":null}]}],"removal":null}`,
		"a formula null":       `{"possible":true,"name":"n","effect":"hold","criteria":[null],"removal":null}`,
		"a group null":         `{"possible":true,"name":"n","effect":"hold","criteria":[{"all":[null]}],"removal":null}`,
		"a condition null":     cond(`null`),
		"var null":             cond(`{"var":null,"op":"is","value":"bank.example","not":false}`),
		"op null":              cond(`{"var":"from_domain","op":null,"value":"bank.example","not":false}`),
		"not null":             cond(`{"var":"from_domain","op":"is","value":"bank.example","not":null}`),
		"value null for is":    cond(`{"var":"from_domain","op":"is","value":null,"not":false}`),
		"value null contains":  cond(`{"var":"subject","op":"contains","value":null,"not":false}`),
		"value null about":     cond(`{"var":"message","op":"about","value":null,"not":false}`),
		"shapes null":          removal(`{"parts":[],"shapes":null,"topics":["health"]}`),
		"topics null":          removal(`{"parts":[],"shapes":["link"],"topics":null}`),
		"a part null":          removal(`{"parts":[null],"shapes":["link"],"topics":[]}`),
		"a shape null":         removal(`{"parts":[],"shapes":["link",null],"topics":[]}`),
		"a topic null":         removal(`{"parts":[],"shapes":["link"],"topics":[null]}`),
		"a hold's removal {}":  strings.Replace(hold, `"removal":null`, `"removal":{}`, 1),
		"a remove's removal 0": removal(`0`),
	} {
		d, err := Read(text)
		if !errors.Is(err, contracts.ErrRuleWordsUnusable) || !reflect.DeepEqual(d, contracts.RuleDraft{}) {
			t.Errorf("%s: Read = %+v, %v; want rule_words_unusable and no draft", name, d, err)
		}
	}
	// Exists, and only exists, takes a null value.
	for _, c := range []string{
		`{"var":"attachments","op":"exists","value":null,"not":false}`,
		`{"var":"list","op":"exists","value":null,"not":true}`,
	} {
		if _, err := Read(cond(c)); err != nil {
			t.Errorf("%s: %v", c, err)
		}
	}
}

// TestU12AJoinedOrBrokenTopicIsUnusable: a topic or a value that joins
// alternatives with a disjunction, holds a question mark or JSON's
// punctuation, or mixes scripts is refused as unusable, so the person is
// asked for conditions instead of given a rule that protects less (ADR 0040,
// "Words, as built"). Each case is an answer U33 got from the model.
func TestU12AJoinedOrBrokenTopicIsUnusable(t *testing.T) {
	cond := func(v, op, value string, not bool) string {
		n := "false"
		if not {
			n = "true"
		}
		return `{"possible":true,"name":"n","effect":"hold","criteria":[{"all":[{"any":[{"var":"` + v + `","op":"` + op + `","value":` + strconvQuote(value) + `,"not":` + n + `}]}]}],"removal":null}`
	}
	topic := func(t string) string {
		return `{"possible":true,"name":"n","effect":"remove","criteria":[{"all":[]}],"removal":{"parts":[],"shapes":[],"topics":[` + strconvQuote(t) + `]}}`
	}
	for name, text := range map[string]string{
		// U33's answers (w1, w2, w3).
		"w3 en-hold-legal":       cond("message", "mentions", "a lawsuit or my lawyer?", false),
		"w1 en-hold-legal":       cond("message", "mentions", "a lawsuit or legal case?", false),
		"w2 pt-hold-medico":      cond("message", "mentions", "meu médico or hospital?", false),
		"w3 pt-hold-medico":      cond("message", "mentions", "médico},{", true),
		"w3 en-hold-signin":      cond("message", "about", "password resetsみたいな?」}]}]}],", true),
		"w3 en-hold-two-domains": cond("from_domain", "is", "paypal.com или stripe.com", false),
		"w2 es-hold-hacienda":    cond("message", "about", "hacienda e impuestos?", false),
		"w3 es-hold-juicio":      cond("message", "mentions", "un juicio?", false),
		"w2 en-hold-salary":      cond("message", "about", "salary payslips?", false),
		"w1 health of the user":  cond("message", "about", "health of the user?", false),
		"w1 citas médicas":       cond("message", "about", "citas médicas לפניך", false),
		// The disjunctions of the corpus's languages.
		"or":           cond("message", "about", "debts or loans", false),
		"OR":           cond("subject", "contains", "Invoice OR receipt", false),
		"o":            cond("message", "about", "deudas o préstamos", false),
		"ou":           cond("message", "about", "dívidas ou empréstimos", false),
		"oder":         cond("message", "about", "Schulden oder Kredite", false),
		"или":          cond("message", "about", "долги или кредиты", false),
		"或":            cond("message", "about", "债务或贷款", false),
		"または":          cond("message", "about", "借金または融資", false),
		"a topic's or": topic("a lawsuit or my lawyer"),
		// JSON's punctuation and question marks, wherever they are.
		"a brace":          cond("body", "contains", "pay{ment", false),
		"a bracket":        topic("diagnoses]"),
		"a quote":          cond("subject", "contains", `say "hi"`, false),
		"a colon":          cond("subject", "contains", "Re: invoice", false),
		"a comma":          topic("debts, loans"),
		"a backslash":      topic(`debts\loans`),
		"an opening ¿":     cond("message", "about", "¿deudas", false),
		"a fullwidth ？":    cond("message", "about", "借金？", false),
		"an Arabic ؟":      cond("message", "about", "ديون؟", false),
		"a corner bracket": topic("パスワード」"),
		// Scripts mixed.
		"Latin and Cyrillic": topic("bank счет"),
		"Latin and Han":      cond("message", "about", "salary 工资", false),
		"Greek and Latin":    topic("υγεία health"),
	} {
		d, err := Read(text)
		if !errors.Is(err, contracts.ErrRuleWordsUnusable) || !reflect.DeepEqual(d, contracts.RuleDraft{}) {
			t.Errorf("%s: Read = %+v, %v; want rule_words_unusable and no draft", name, d, err)
		}
	}
	// What U33's usable answers held stays usable: apostrophes, accents,
	// one script with its marks, addresses, media types, dates and sizes.
	for name, text := range map[string]string{
		"an apostrophe":        cond("message", "about", "my kids' school", false),
		"a typographic quote":  topic("my children’s names"),
		"Spanish":              cond("message", "about", "pensión alimenticia", false),
		"an address":           cond("from_address", "is", "dr.lopez@clinicaxyz.com", false),
		"a hyphenated domain":  cond("from_domain", "is", "caixa-ejemplo.es", false),
		"a media type":         cond("attachment_type", "is", "application/pdf", false),
		"a date":               cond("received", "before", "2025-01-01T00:00:00Z", false),
		"a size":               cond("size", "at_least", "10000000", false),
		"Japanese":             cond("message", "about", "パスワードの再設定", false),
		"Russian":              topic("домашний адрес"),
		"Chinese":              cond("message", "about", "工资单", false),
		"or inside a word":     cond("message", "about", "work order", false),
		"o inside a word":      topic("los médicos"),
		"digits with a script": cond("message", "about", "form 1040", false),
	} {
		if _, err := Read(text); err != nil {
			t.Errorf("%s: %v, want a draft", name, err)
		}
	}
}

func strconvQuote(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	return string(b)
}
