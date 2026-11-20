package rulewords

import (
	"encoding/json"
	"slices"
	"strings"
	"unicode"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/query"
)

// errUnusable is every refusal of Read: contracts.ErrRuleWordsUnusable,
// which says nothing of the answer.
var errUnusable = contracts.ErrRuleWordsUnusable

// Read checks the text of an answer and makes it a draft (ADR 0040, "What
// the person confirms"): exactly the schema's JSON, nothing before or after
// it, no member it does not list and none null but a hold's removal and the
// value of exists (a null is never read as its zero value); possible; a name of 1 to
// contracts.MaxRuleNameRunes characters with no control character; 1 to
// contracts.MaxRuleCriteria criteria, each a formula the query language
// accepts (its own limits, never "never", no mailbox: the model knows none
// of the account's) with at most contracts.MaxRuleModelConditions
// conditions a model decides over them all; hold with no removal, or remove
// with a removal of known parts and shapes, each at most once, and at most
// contracts.MaxRuleTopics topics the language accepts, not all three empty;
// within contracts.MaxRuleBytes. Anything else is
// contracts.ErrRuleWordsUnusable.
//
// The draft is for every connection (ScopeAll) and on, its criteria and
// topics canonical and the removal's lists in their canonical order. The
// account's own check (accountrules.Rules.Check: its mailboxes, its rules'
// bounds, the sentence) is the caller's.
func Read(text string) (contracts.RuleDraft, error) {
	var a wireAnswer
	if !strictly([]byte(text), &a, []string{"possible", "name", "effect", "criteria", "removal"}, "removal") {
		return contracts.RuleDraft{}, errUnusable
	}
	if !a.Possible {
		return contracts.RuleDraft{}, errUnusable
	}
	d := contracts.RuleDraft{Scope: contracts.RuleScope{To: contracts.ScopeAll}, On: true}
	var ok bool
	if d.Name, ok = ruleName(a.Name); !ok {
		return contracts.RuleDraft{}, errUnusable
	}
	if d.Criteria, ok = criteria(a.Criteria); !ok {
		return contracts.RuleDraft{}, errUnusable
	}
	d.Effect = contracts.RuleEffect(a.Effect)
	switch d.Effect {
	case contracts.EffectHold:
		if string(a.Removal) != "null" {
			return contracts.RuleDraft{}, errUnusable
		}
	case contracts.EffectRemove:
		if d.Removal, ok = removal(a.Removal); !ok {
			return contracts.RuleDraft{}, errUnusable
		}
	default:
		return contracts.RuleDraft{}, errUnusable
	}
	if b, err := json.Marshal(d); err != nil || len(b) > contracts.MaxRuleBytes {
		return contracts.RuleDraft{}, errUnusable
	}
	return d, nil
}

// The answer as the schema writes it.
type wireAnswer struct {
	Possible bool            `json:"possible"`
	Name     string          `json:"name"`
	Effect   string          `json:"effect"`
	Criteria json.RawMessage `json:"criteria"`
	Removal  json.RawMessage `json:"removal"`
}

type wireFormula struct {
	All json.RawMessage `json:"all"`
}

type wireGroup struct {
	Any json.RawMessage `json:"any"`
}

type wireCondition struct {
	Var   string  `json:"var"`
	Op    string  `json:"op"`
	Value *string `json:"value"`
	Not   bool    `json:"not"`
}

type wireRemoval struct {
	Parts  json.RawMessage `json:"parts"`
	Shapes json.RawMessage `json:"shapes"`
	Topics json.RawMessage `json:"topics"`
}

// strictly decodes one JSON object into v when it has exactly the members
// named, each once, none null but those also named in nullable (the
// schema's only nullable members: the answer's removal and a condition's
// value), and nothing follows it. A null is never read as its zero value.
func strictly(data []byte, v any, members []string, nullable ...string) bool {
	var m map[string]json.RawMessage
	if !one(data, &m) || m == nil || len(m) != len(members) || !distinctMembers(data, len(members)) {
		return false
	}
	for _, k := range members {
		raw, ok := m[k]
		if !ok || (string(raw) == "null" && !slices.Contains(nullable, k)) {
			return false
		}
	}
	return one(data, v)
}

// list decodes a JSON array into out when it is one and holds no null.
func list[T any](raw json.RawMessage, out *[]T) bool {
	var items []json.RawMessage
	if !one(raw, &items) || items == nil {
		return false
	}
	for _, it := range items {
		if string(it) == "null" {
			return false
		}
	}
	return one(raw, out) && *out != nil
}

// one decodes exactly one JSON value into v, with no unknown member and
// nothing after it.
func one(data []byte, v any) bool {
	dec := json.NewDecoder(strings.NewReader(string(data)))
	dec.DisallowUnknownFields()
	if dec.Decode(v) != nil {
		return false
	}
	return strings.Trim(string(data[dec.InputOffset():]), " \t\r\n") == ""
}

// distinctMembers says the object's top level lists n members, so that a
// member written twice (which a map keeps once) is refused.
func distinctMembers(data []byte, n int) bool {
	dec := json.NewDecoder(strings.NewReader(string(data)))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return false
	}
	count := 0
	for dec.More() {
		if _, err := dec.Token(); err != nil { // the key
			return false
		}
		var skip json.RawMessage
		if dec.Decode(&skip) != nil {
			return false
		}
		count++
	}
	return count == n
}

// ruleName is the answer's name, trimmed, when it is 1 to
// contracts.MaxRuleNameRunes characters with no unwritten rune.
func ruleName(s string) (string, bool) {
	s = strings.TrimSpace(s)
	n := 0
	for _, r := range s {
		if unwritten(r) {
			return "", false
		}
		n++
	}
	return s, n > 0 && n <= contracts.MaxRuleNameRunes
}

// unwritten says r is a rune no answer of the model's should hold: a
// control or format character, or U+FFFD, which stands for bytes that were
// not text.
func unwritten(r rune) bool {
	return unicode.IsControl(r) || unicode.Is(unicode.Cf, r) || r == '\uFFFD'
}

// clean says s holds no unwritten rune.
func clean(s string) bool { return strings.IndexFunc(s, unwritten) < 0 }

// criteria are the answer's criteria as canonical formulas.
func criteria(raw json.RawMessage) ([]contracts.Formula, bool) {
	var formulas []json.RawMessage
	if !list(raw, &formulas) || len(formulas) == 0 || len(formulas) > contracts.MaxRuleCriteria {
		return nil, false
	}
	out := make([]contracts.Formula, 0, len(formulas))
	model := 0
	for _, fr := range formulas {
		var wf wireFormula
		var groups []json.RawMessage
		if !strictly(fr, &wf, []string{"all"}) || !list(wf.All, &groups) {
			return nil, false
		}
		f := contracts.Formula{V: contracts.FormulaVersion, All: []contracts.Group{}}
		for _, gr := range groups {
			var wg wireGroup
			var conds []json.RawMessage
			if !strictly(gr, &wg, []string{"any"}) || !list(wg.Any, &conds) {
				return nil, false
			}
			g := contracts.Group{Any: []contracts.Condition{}}
			for _, cr := range conds {
				var wc wireCondition
				if !strictly(cr, &wc, []string{"var", "op", "value", "not"}, "value") ||
					(wc.Value == nil && contracts.Operator(wc.Op) != contracts.OpExists) {
					return nil, false
				}
				c := contracts.Condition{Var: contracts.Variable(wc.Var), Op: contracts.Operator(wc.Op), Not: wc.Not}
				if wc.Value != nil {
					c.Value = *wc.Value
				}
				if !clean(c.Value) {
					return nil, false
				}
				if !offered(c.Var) {
					return nil, false
				}
				g.Any = append(g.Any, c)
			}
			f.All = append(f.All, g)
		}
		c, err := query.Canonicalise(f)
		if err != nil || c.Never {
			return nil, false
		}
		for _, g := range c.Formula.All {
			for _, cond := range g.Any {
				if cond.Op == contracts.OpAbout || cond.Op == contracts.OpMentions {
					model++
				}
			}
		}
		out = append(out, c.Formula)
	}
	return out, model <= contracts.MaxRuleModelConditions
}

// offered says v is one of the schema's variables.
func offered(v contracts.Variable) bool {
	for _, w := range variables {
		if v == w {
			return true
		}
	}
	return false
}

// removal is the answer's removal, its lists in their canonical order.
func removal(raw json.RawMessage) (*contracts.RuleRemoval, bool) {
	var wr wireRemoval
	var ps, shapes, topics []string
	if !strictly(raw, &wr, []string{"parts", "shapes", "topics"}) ||
		!list(wr.Parts, &ps) || !list(wr.Shapes, &shapes) || !list(wr.Topics, &topics) {
		return nil, false
	}
	if len(ps)+len(shapes)+len(topics) == 0 || len(topics) > contracts.MaxRuleTopics {
		return nil, false
	}
	r := &contracts.RuleRemoval{}
	var ok bool
	if r.Parts, ok = inOrder(ps, parts); !ok {
		return nil, false
	}
	if r.Shapes, ok = inOrder(shapes, contracts.ShapeKinds); !ok {
		return nil, false
	}
	seen := map[string]bool{}
	for _, t := range topics {
		if !clean(t) {
			return nil, false
		}
		f := contracts.Formula{V: contracts.FormulaVersion, All: []contracts.Group{{Any: []contracts.Condition{
			{Var: contracts.VarMessage, Op: contracts.OpMentions, Value: t},
		}}}}
		c, err := query.Canonicalise(f)
		if err != nil || c.Never || len(c.Formula.All) != 1 || len(c.Formula.All[0].Any) != 1 {
			return nil, false
		}
		topic := c.Formula.All[0].Any[0].Value
		if seen[topic] {
			return nil, false
		}
		seen[topic] = true
		r.Topics = append(r.Topics, topic)
	}
	return r, true
}

// inOrder is values in the order of known, or false when one is not known
// or is given twice; nil when there are none.
func inOrder[T ~string](values []string, known []T) ([]T, bool) {
	given := map[string]bool{}
	for _, v := range values {
		if given[v] {
			return nil, false
		}
		given[v] = true
	}
	var out []T
	for _, k := range known {
		if given[string(k)] {
			out = append(out, k)
			delete(given, string(k))
		}
	}
	return out, len(given) == 0
}
