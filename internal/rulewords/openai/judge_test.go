package openai

// The judge of the words' eval (U33): whether a draft protects less than its
// phrasing asks. It is apart from eval_test.go, which needs a key and the
// wordseval tag, so that its own tests run with every other.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/query"
)

// wantCondition is a condition the corpus asks for.
type wantCondition struct {
	Var   contracts.Variable `json:"var"`
	Op    contracts.Operator `json:"op"`
	Value string             `json:"value"`
	Stems []string           `json:"stems"`
}

type wordsCase struct {
	ID     string `json:"id"`
	Lang   string `json:"lang"`
	Words  string `json:"words"`
	Expect struct {
		Unusable   bool                  `json:"unusable"`
		Effect     contracts.RuleEffect  `json:"effect"`
		Everything bool                  `json:"everything"`
		Require    []wantCondition       `json:"require"`
		NarrowOK   bool                  `json:"narrow_ok"`
		Shapes     []contracts.ShapeKind `json:"shapes"`
		Parts      []contracts.Part      `json:"parts"`
		Topics     []string              `json:"topics"`
		// OrHold are the conditions of a hold that protects at least as
		// much as the removal asked for (holding every message with
		// attachments in place of removing them).
		OrHold []wantCondition `json:"or_hold"`
	} `json:"expect"`
}

func loadWords(t *testing.T) []wordsCase {
	f, err := os.Open(filepath.Join("..", "testdata", "words.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []wordsCase
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var c wordsCase
		dec := json.NewDecoder(strings.NewReader(sc.Text()))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&c); err != nil {
			t.Fatalf("%s: %v", sc.Text(), err)
		}
		out = append(out, c)
	}
	return out
}

// covers says a condition of a draft holds every message the wanted one
// would, as far as the judge can tell: the same test or a wider one (the
// sender's domain or a parent of it for an address, any attachment for a
// type, a later "before", a smaller "at_least", a shorter word contained,
// "mentions" on the message for "about" there or on the subject), never
// negated, and for about and mentions a topic holding one of the stems;
// values compared without case.
func covers(want wantCondition, got contracts.Condition) bool {
	if got.Not {
		return false
	}
	lower := strings.ToLower
	switch want.Op {
	case contracts.OpAbout, contracts.OpMentions:
		same := got.Var == want.Var && got.Op == want.Op
		wider := got.Var == contracts.VarMessage && got.Op == contracts.OpMentions
		if want.Op == contracts.OpAbout && want.Var == contracts.VarMessage {
			wider = wider || (got.Var == contracts.VarMessage && got.Op == contracts.OpAbout)
		}
		return (same || wider) && slices.ContainsFunc(want.Stems, func(s string) bool {
			return strings.Contains(lower(got.Value), lower(s))
		})
	case contracts.OpContains:
		return got.Var == want.Var && got.Op == want.Op && got.Value != "" &&
			strings.Contains(lower(want.Value), lower(got.Value))
	case contracts.OpBefore:
		w, err1 := time.Parse(time.RFC3339, want.Value)
		g, err2 := time.Parse(time.RFC3339, got.Value)
		return got.Var == want.Var && got.Op == want.Op && err1 == nil && err2 == nil && !g.Before(w)
	case contracts.OpAtLeast:
		w, err1 := strconv.ParseInt(want.Value, 10, 64)
		g, err2 := strconv.ParseInt(got.Value, 10, 64)
		return got.Var == want.Var && got.Op == want.Op && err1 == nil && err2 == nil && g <= w
	case contracts.OpIs:
		if got.Var == want.Var && got.Op == want.Op && strings.EqualFold(got.Value, want.Value) {
			return true
		}
		switch want.Var {
		case contracts.VarFromAddress, contracts.VarFromDomain:
			d := lower(want.Value)
			if at := strings.LastIndexByte(d, '@'); at >= 0 {
				d = d[at+1:]
			}
			p := lower(got.Value)
			return got.Var == contracts.VarFromDomain && got.Op == contracts.OpIs && p != "" && (d == p || strings.HasSuffix(d, "."+p))
		case contracts.VarAttachmentType:
			return got.Var == contracts.VarAttachments && got.Op == contracts.OpExists
		}
		return false
	default:
		return got.Var == want.Var && got.Op == want.Op && strings.EqualFold(got.Value, want.Value)
	}
}

// widens says a criteria holds every message w would: each of its groups
// (joined by all) holds a condition covering w, so no group narrows it.
// Criteria are joined by any, so one such criteria is enough.
func widens(f contracts.Formula, w wantCondition) bool {
	return len(f.All) > 0 && !slices.ContainsFunc(f.All, func(g contracts.Group) bool {
		return !slices.ContainsFunc(g.Any, func(got contracts.Condition) bool { return covers(w, got) })
	})
}

// less says how a draft protects less than its phrasing asks, or "". Every
// condition the phrasing requires must be met by a criteria at least as
// wide (a criteria of no group matches all mail); with narrow_ok, by a
// criteria with a group that covers it, as the words narrow it themselves.
// A hold protects at least as much as a removal of the same mail (and a
// hold of every message with attachments as removing them); a removal must
// take out every shape and part asked, and a topic asked by one of its
// stems, or the subject, the body and the attachments whole. Topics asked
// as alternatives ("a lawsuit or my lawyer") are each their own condition:
// one condition whose topic covers two of them joins them, and lets out the
// mail about one alone that the meaning's model does not read as the pair.
func less(c wordsCase, d contracts.RuleDraft) string {
	e := c.Expect
	everything := slices.ContainsFunc(d.Criteria, func(f contracts.Formula) bool { return len(f.All) == 0 })
	if e.Effect == contracts.EffectRemove && d.Effect == contracts.EffectHold && len(e.OrHold) > 0 {
		if everything || slices.ContainsFunc(e.OrHold, func(w wantCondition) bool {
			return slices.ContainsFunc(d.Criteria, func(f contracts.Formula) bool { return widens(f, w) })
		}) {
			return ""
		}
		if e.Everything {
			return "holds less than every message the removal asks for"
		}
	}
	if e.Effect == contracts.EffectHold && d.Effect != contracts.EffectHold {
		return "removes where it should hold"
	}
	if e.Everything && !everything {
		return "does not apply to all mail"
	}
	for _, w := range e.Require {
		found := everything || slices.ContainsFunc(d.Criteria, func(f contracts.Formula) bool {
			if e.NarrowOK {
				return slices.ContainsFunc(f.All, func(g contracts.Group) bool {
					return slices.ContainsFunc(g.Any, func(got contracts.Condition) bool { return covers(w, got) })
				})
			}
			return widens(f, w)
		})
		if !found {
			return fmt.Sprintf("misses %s %s %s%s, or narrows it", w.Var, w.Op, w.Value, strings.Join(w.Stems, "|"))
		}
	}
	if j := joined(e.Require, d.Criteria); j != "" {
		return j
	}
	if d.Effect == contracts.EffectRemove {
		r := d.Removal
		if r == nil {
			return "removes nothing"
		}
		for _, k := range e.Shapes {
			if !slices.Contains(r.Shapes, k) {
				return "misses the shape " + string(k)
			}
		}
		for _, p := range e.Parts {
			if !slices.Contains(r.Parts, p) {
				return "misses the part " + string(p)
			}
		}
		whole := slices.Contains(r.Parts, contracts.PartSubject) && slices.Contains(r.Parts, contracts.PartBody) &&
			slices.Contains(r.Parts, contracts.PartAttachments)
		if len(e.Topics) > 0 && !whole && !slices.ContainsFunc(r.Topics, func(t string) bool {
			return slices.ContainsFunc(e.Topics, func(s string) bool { return strings.Contains(strings.ToLower(t), strings.ToLower(s)) })
		}) {
			return "misses the topic it asks to remove"
		}
	}
	return ""
}

// joined names a topic the phrasing asks for apart from another that no
// condition of the criteria covers alone, every one covering it joining
// both, or "".
func joined(require []wantCondition, criteria []contracts.Formula) string {
	var topics []wantCondition
	for _, w := range require {
		if w.Op == contracts.OpAbout || w.Op == contracts.OpMentions {
			topics = append(topics, w)
		}
	}
	if len(topics) < 2 {
		return ""
	}
	for _, w := range topics {
		alone, with := false, ""
		for _, f := range criteria {
			for _, g := range f.All {
				for _, got := range g.Any {
					if !covers(w, got) {
						continue
					}
					if slices.ContainsFunc(topics, func(o wantCondition) bool { return !slices.Equal(o.Stems, w.Stems) && covers(o, got) }) {
						with = got.Value
					} else {
						alone = true
					}
				}
			}
		}
		if !alone && with != "" {
			return fmt.Sprintf("joins topics asked apart in one condition, %q", with)
		}
	}
	return ""
}

func cnd(v contracts.Variable, op contracts.Operator, value string) contracts.Condition {
	return contracts.Condition{Var: v, Op: op, Value: value}
}

func want(v contracts.Variable, op contracts.Operator, value string, stems ...string) wantCondition {
	return wantCondition{Var: v, Op: op, Value: value, Stems: stems}
}

// formula is the conditions' groups joined by all.
func formula(groups ...[]contracts.Condition) contracts.Formula {
	f := contracts.Formula{V: contracts.FormulaVersion, All: []contracts.Group{}}
	for _, g := range groups {
		f.All = append(f.All, contracts.Group{Any: g})
	}
	return f
}

func group(cs ...contracts.Condition) []contracts.Condition { return cs }

func holdDraft(fs ...contracts.Formula) contracts.RuleDraft {
	return contracts.RuleDraft{Effect: contracts.EffectHold, Criteria: fs}
}

func removeDraft(r contracts.RuleRemoval, fs ...contracts.Formula) contracts.RuleDraft {
	return contracts.RuleDraft{Effect: contracts.EffectRemove, Criteria: fs, Removal: &r}
}

// TestU33TheJudgeRefusesWhatProtectsLess: the eval's judge counts as
// protecting less every draft that holds or removes less than its phrasing
// asks (fewer messages, a narrower test, a later bound, another topic,
// fewer parts), and nothing that protects as much or more.
func TestU33TheJudgeRefusesWhatProtectsLess(t *testing.T) {
	const (
		paypal, stripe = "paypal.com", "stripe.com"
		from, domain   = contracts.VarFromAddress, contracts.VarFromDomain
	)
	twoDomains := wordsCase{}
	twoDomains.Expect.Effect = contracts.EffectHold
	twoDomains.Expect.Require = []wantCondition{want(domain, contracts.OpIs, paypal), want(domain, contracts.OpIs, stripe)}

	password := wordsCase{}
	password.Expect.Effect = contracts.EffectHold
	password.Expect.Require = []wantCondition{want(contracts.VarSubject, contracts.OpContains, "password"), want(contracts.VarBody, contracts.OpContains, "password")}

	lawsuit := wordsCase{}
	lawsuit.Expect.Effect = contracts.EffectHold
	lawsuit.Expect.Require = []wantCondition{want(contracts.VarMessage, contracts.OpMentions, "", "lawsuit")}

	medical := wordsCase{}
	medical.Expect.Effect = contracts.EffectHold
	medical.Expect.Require = []wantCondition{want(contracts.VarMessage, contracts.OpAbout, "", "medic", "health")}

	invoices := wordsCase{}
	invoices.Expect.Effect = contracts.EffectHold
	invoices.Expect.Require = []wantCondition{want(contracts.VarSubject, contracts.OpAbout, "", "invoice")}

	before := wordsCase{}
	before.Expect.Effect = contracts.EffectHold
	before.Expect.Require = []wantCondition{want(contracts.VarReceived, contracts.OpBefore, "2025-01-01T00:00:00Z")}

	big := wordsCase{}
	big.Expect.Effect = contracts.EffectHold
	big.Expect.Require = []wantCondition{want(contracts.VarSize, contracts.OpAtLeast, "10485761")}

	address := wordsCase{}
	address.Expect.Effect = contracts.EffectHold
	address.Expect.Require = []wantCondition{want(from, contracts.OpIs, "dr.lopez@clinic.example")}

	pdf := wordsCase{}
	pdf.Expect.Effect = contracts.EffectHold
	pdf.Expect.Require = []wantCondition{want(contracts.VarAttachmentType, contracts.OpIs, "application/pdf")}

	everything := wordsCase{}
	everything.Expect.Effect = contracts.EffectHold
	everything.Expect.Everything = true

	homeAddress := wordsCase{}
	homeAddress.Expect.Effect = contracts.EffectRemove
	homeAddress.Expect.Everything = true
	homeAddress.Expect.Topics = []string{"address"}

	attachments := wordsCase{}
	attachments.Expect.Effect = contracts.EffectRemove
	attachments.Expect.Everything = true
	attachments.Expect.Parts = []contracts.Part{contracts.PartAttachments}
	attachments.Expect.OrHold = []wantCondition{want(contracts.VarAttachments, contracts.OpExists, "")}

	onlySubject := wordsCase{}
	onlySubject.Expect.Effect = contracts.EffectRemove
	onlySubject.Expect.Require = []wantCondition{want(domain, contracts.OpIs, "mybank.example")}
	onlySubject.Expect.Parts = []contracts.Part{contracts.PartSender, contracts.PartBody, contracts.PartAttachments}

	legal := wordsCase{}
	legal.Expect.Effect = contracts.EffectHold
	legal.Expect.Require = []wantCondition{
		want(contracts.VarMessage, contracts.OpMentions, "", "lawsuit"),
		want(contracts.VarMessage, contracts.OpMentions, "", "lawyer"),
	}

	domainTopic := wordsCase{}
	domainTopic.Expect.Effect = contracts.EffectHold
	domainTopic.Expect.Require = []wantCondition{want(domain, contracts.OpIs, "amazon.com")}
	domainTopic.Expect.NarrowOK = true

	all := formula()
	for _, tc := range []struct {
		name string
		c    wordsCase
		d    contracts.RuleDraft
		less bool
	}{
		// Less.
		{"two senders asked, both required at once", twoDomains, holdDraft(formula(group(cnd(domain, contracts.OpIs, paypal)), group(cnd(domain, contracts.OpIs, stripe)))), true},
		{"two senders asked, one given", twoDomains, holdDraft(formula(group(cnd(domain, contracts.OpIs, paypal)))), true},
		{"a word anywhere, the subject only", password, holdDraft(formula(group(cnd(contracts.VarSubject, contracts.OpContains, "password")))), true},
		{"a word anywhere, the body only", password, holdDraft(formula(group(cnd(contracts.VarBody, contracts.OpContains, "password")))), true},
		{"a mention asked, about given", lawsuit, holdDraft(formula(group(cnd(contracts.VarMessage, contracts.OpAbout, "a lawsuit")))), true},
		{"a topic asked, another given", medical, holdDraft(formula(group(cnd(contracts.VarMessage, contracts.OpAbout, "cats")))), true},
		{"the message asked, the subject given", medical, holdDraft(formula(group(cnd(contracts.VarSubject, contracts.OpAbout, "medical appointments")))), true},
		{"a topic negated", medical, holdDraft(formula(group(contracts.Condition{Var: contracts.VarMessage, Op: contracts.OpAbout, Value: "health", Not: true}))), true},
		{"an earlier bound", before, holdDraft(formula(group(cnd(contracts.VarReceived, contracts.OpBefore, "2020-01-01T00:00:00Z")))), true},
		{"a bound a second early", before, holdDraft(formula(group(cnd(contracts.VarReceived, contracts.OpBefore, "2024-12-31T23:59:59Z")))), true},
		{"a larger size", big, holdDraft(formula(group(cnd(contracts.VarSize, contracts.OpAtLeast, "100000000")))), true},
		{"another sender's domain", address, holdDraft(formula(group(cnd(domain, contracts.OpIs, "other.example")))), true},
		{"a sender narrowed", address, holdDraft(formula(group(cnd(from, contracts.OpIs, "dr.lopez@clinic.example")), group(cnd(contracts.VarAttachments, contracts.OpExists, "")))), true},
		{"a topic asked, a shape given", homeAddress, removeDraft(contracts.RuleRemoval{Shapes: []contracts.ShapeKind{contracts.ShapePhoneNumber}}, all), true},
		{"a topic asked, another topic given", homeAddress, removeDraft(contracts.RuleRemoval{Topics: []string{"phone numbers"}}, all), true},
		{"a removal for some mail", homeAddress, removeDraft(contracts.RuleRemoval{Topics: []string{"a home address"}}, formula(group(cnd(contracts.VarAttachments, contracts.OpExists, "")))), true},
		{"three parts asked, one given", onlySubject, removeDraft(contracts.RuleRemoval{Parts: []contracts.Part{contracts.PartBody}}, formula(group(cnd(domain, contracts.OpIs, "mybank.example")))), true},
		{"a hold that is narrower than the removal", attachments, holdDraft(formula(group(cnd(contracts.VarAttachmentType, contracts.OpIs, "application/pdf")))), true},
		{"two topics asked apart, one given", legal, holdDraft(formula(group(cnd(contracts.VarMessage, contracts.OpMentions, "a lawsuit or legal case?")))), true},
		{"two topics asked apart, joined in one", legal, holdDraft(formula(group(cnd(contracts.VarMessage, contracts.OpMentions, "a lawsuit or my lawyer")))), true},
		{"two topics asked apart, both required at once", legal, holdDraft(formula(group(cnd(contracts.VarMessage, contracts.OpMentions, "a lawsuit")), group(cnd(contracts.VarMessage, contracts.OpMentions, "a lawyer")))), true},
		{"removes where it should hold", everything, removeDraft(contracts.RuleRemoval{Parts: []contracts.Part{contracts.PartBody}}, all), true},
		{"a hold of some mail where all is asked", everything, holdDraft(formula(group(cnd(contracts.VarAttachments, contracts.OpExists, "")))), true},
		// As much or more.
		{"two senders in one group", twoDomains, holdDraft(formula(group(cnd(domain, contracts.OpIs, paypal), cnd(domain, contracts.OpIs, stripe)))), false},
		{"two senders in two criteria", twoDomains, holdDraft(formula(group(cnd(domain, contracts.OpIs, paypal))), formula(group(cnd(domain, contracts.OpIs, stripe)))), false},
		{"a word in the subject or the body", password, holdDraft(formula(group(cnd(contracts.VarSubject, contracts.OpContains, "Password"), cnd(contracts.VarBody, contracts.OpContains, "password")))), false},
		{"a shorter word", password, holdDraft(formula(group(cnd(contracts.VarSubject, contracts.OpContains, "passw"), cnd(contracts.VarBody, contracts.OpContains, "passw")))), false},
		{"a mention", lawsuit, holdDraft(formula(group(cnd(contracts.VarMessage, contracts.OpMentions, "lawsuits")))), false},
		{"a mention where about is asked", medical, holdDraft(formula(group(cnd(contracts.VarMessage, contracts.OpMentions, "Health")))), false},
		{"the message where the subject is asked", invoices, holdDraft(formula(group(cnd(contracts.VarMessage, contracts.OpMentions, "invoices")))), false},
		{"the subject as asked", invoices, holdDraft(formula(group(cnd(contracts.VarSubject, contracts.OpAbout, "invoices")))), false},
		{"a later bound", before, holdDraft(formula(group(cnd(contracts.VarReceived, contracts.OpBefore, "2025-01-01T05:00:00Z")))), false},
		{"a smaller size", big, holdDraft(formula(group(cnd(contracts.VarSize, contracts.OpAtLeast, "10000000")))), false},
		{"the sender's whole domain", address, holdDraft(formula(group(cnd(domain, contracts.OpIs, "clinic.example")))), false},
		{"the sender as asked", address, holdDraft(formula(group(cnd(from, contracts.OpIs, "Dr.Lopez@clinic.example")))), false},
		{"any attachment", pdf, holdDraft(formula(group(cnd(contracts.VarAttachments, contracts.OpExists, "")))), false},
		{"everything held", medical, holdDraft(all), false},
		{"the topic removed", homeAddress, removeDraft(contracts.RuleRemoval{Topics: []string{"a home address"}}, all), false},
		{"the text removed whole", homeAddress, removeDraft(contracts.RuleRemoval{Parts: []contracts.Part{contracts.PartSubject, contracts.PartBody, contracts.PartAttachments}}, all), false},
		{"a hold of every message with attachments", attachments, holdDraft(formula(group(cnd(contracts.VarAttachments, contracts.OpExists, "")))), false},
		{"the parts asked", onlySubject, removeDraft(contracts.RuleRemoval{Parts: []contracts.Part{contracts.PartSender, contracts.PartBody, contracts.PartAttachments}}, formula(group(cnd(domain, contracts.OpIs, "mybank.example")))), false},
		{"a hold where a removal is asked", onlySubject, holdDraft(formula(group(cnd(domain, contracts.OpIs, "mybank.example")))), false},
		{"two topics asked apart, one condition each", legal, holdDraft(formula(group(cnd(contracts.VarMessage, contracts.OpMentions, "a lawsuit"), cnd(contracts.VarMessage, contracts.OpMentions, "my lawyer")))), false},
		{"two topics asked apart, joined beside each alone", legal, holdDraft(formula(group(cnd(contracts.VarMessage, contracts.OpMentions, "a lawsuit"), cnd(contracts.VarMessage, contracts.OpMentions, "a lawyer"), cnd(contracts.VarMessage, contracts.OpMentions, "lawsuits and lawyers")))), false},
		{"two topics asked apart, in two criteria", legal, holdDraft(formula(group(cnd(contracts.VarMessage, contracts.OpMentions, "a lawsuit"))), formula(group(cnd(contracts.VarMessage, contracts.OpMentions, "my lawyer")))), false},
		{"narrowed where the words narrow it", domainTopic, holdDraft(formula(group(cnd(domain, contracts.OpIs, "amazon.com")), group(cnd(contracts.VarMessage, contracts.OpAbout, "returns")))), false},
	} {
		if got := less(tc.c, tc.d); (got != "") != tc.less {
			t.Errorf("%s: less = %q, want less %v", tc.name, got, tc.less)
		}
	}
}

// TestU33TheCorpusSaysWhatItAsks: every phrasing of the corpus that asks
// for a rule says what it asks in terms the judge reads: known variables and
// operators, a topic's stems for about and mentions, a value for every
// other condition but exists, and the topics' stems when it removes by
// meaning.
func TestU33TheCorpusSaysWhatItAsks(t *testing.T) {
	cases := loadWords(t)
	if len(cases) < 70 {
		t.Fatalf("%d phrasings", len(cases))
	}
	ids := map[string]bool{}
	for _, c := range cases {
		if ids[c.ID] {
			t.Errorf("%s twice", c.ID)
		}
		ids[c.ID] = true
		e := c.Expect
		if e.Unusable {
			continue
		}
		if e.Effect != contracts.EffectHold && e.Effect != contracts.EffectRemove {
			t.Errorf("%s: effect %q", c.ID, e.Effect)
		}
		if !e.Everything && len(e.Require) == 0 {
			t.Errorf("%s: asks for no mail", c.ID)
		}
		if e.Effect == contracts.EffectRemove && len(e.Shapes)+len(e.Parts)+len(e.Topics) == 0 {
			t.Errorf("%s: removes nothing", c.ID)
		}
		for i, a := range e.Require {
			for _, b := range e.Require[i+1:] {
				for _, sa := range a.Stems {
					for _, sb := range b.Stems {
						if strings.Contains(strings.ToLower(sa), strings.ToLower(sb)) || strings.Contains(strings.ToLower(sb), strings.ToLower(sa)) {
							t.Errorf("%s: the stems %q and %q of topics asked apart overlap, so one topic could stand for both", c.ID, sa, sb)
						}
					}
				}
			}
		}
		for _, w := range append(slices.Clone(e.Require), e.OrHold...) {
			probe := contracts.Condition{Var: w.Var, Op: w.Op, Value: w.Value}
			if len(w.Stems) > 0 {
				probe.Value = w.Stems[0]
			}
			if _, err := query.Canonicalise(contracts.Formula{V: contracts.FormulaVersion, All: []contracts.Group{{Any: []contracts.Condition{probe}}}}); err != nil {
				t.Errorf("%s: %s %s %q is not a condition: %v", c.ID, w.Var, w.Op, probe.Value, err)
			}
			switch w.Op {
			case contracts.OpAbout, contracts.OpMentions:
				if len(w.Stems) == 0 || w.Value != "" {
					t.Errorf("%s: %s %s needs stems and no value", c.ID, w.Var, w.Op)
				}
			case contracts.OpExists:
				if w.Value != "" || len(w.Stems) > 0 {
					t.Errorf("%s: exists takes nothing", c.ID)
				}
			default:
				if w.Value == "" || len(w.Stems) > 0 {
					t.Errorf("%s: %s %s needs a value and no stems", c.ID, w.Var, w.Op)
				}
			}
		}
	}
}
