package query

import (
	"cmp"
	"crypto/sha256"
	"encoding/base32"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
)

// The canonical form (ADR 0027, "Simplification", "The canonical form, and
// its identifier").

// variables are the variables in the order of the canonical form.
var variables = []contracts.Variable{
	contracts.VarFromAddress, contracts.VarFromDomain, contracts.VarRecipientAddress, contracts.VarRecipientDomain,
	contracts.VarSignedBy, contracts.VarSubject, contracts.VarBody, contracts.VarMessage, contracts.VarAttachments,
	contracts.VarAttachmentName, contracts.VarAttachmentType, contracts.VarList, contracts.VarReceived,
	contracts.VarSize, contracts.VarMailbox,
}

// operators are the operators in the order of the canonical form.
var operators = []contracts.Operator{
	contracts.OpIs, contracts.OpContains, contracts.OpExists, contracts.OpSince, contracts.OpBefore,
	contracts.OpAtLeast, contracts.OpBelow, contracts.OpAbout, contracts.OpMentions,
}

// kind is the type of a condition's value, which says how it is normalised.
type kind uint8

const (
	kindNone kind = iota // exists: no value
	kindAddress
	kindDomain
	kindText  // contains: a substring
	kindTopic // about, mentions: a model decides
	kindMediaType
	kindList
	kindTime
	kindSize
	kindMailbox
)

// allowed is, for each variable, its operators and the kind of their value
// (ADR 0027, "The table").
var allowed = map[contracts.Variable]map[contracts.Operator]kind{
	contracts.VarFromAddress:      {contracts.OpIs: kindAddress},
	contracts.VarFromDomain:       {contracts.OpIs: kindDomain},
	contracts.VarRecipientAddress: {contracts.OpIs: kindAddress},
	contracts.VarRecipientDomain:  {contracts.OpIs: kindDomain},
	contracts.VarSignedBy:         {contracts.OpIs: kindDomain},
	contracts.VarSubject:          {contracts.OpContains: kindText, contracts.OpAbout: kindTopic},
	contracts.VarBody:             {contracts.OpContains: kindText},
	contracts.VarMessage:          {contracts.OpAbout: kindTopic, contracts.OpMentions: kindTopic},
	contracts.VarAttachments:      {contracts.OpExists: kindNone},
	contracts.VarAttachmentName:   {contracts.OpContains: kindText},
	contracts.VarAttachmentType:   {contracts.OpIs: kindMediaType},
	contracts.VarList:             {contracts.OpIs: kindList, contracts.OpExists: kindNone},
	contracts.VarReceived:         {contracts.OpSince: kindTime, contracts.OpBefore: kindTime},
	contracts.VarSize:             {contracts.OpAtLeast: kindSize, contracts.OpBelow: kindSize},
	contracts.VarMailbox:          {contracts.OpIs: kindMailbox},
}

// isModel reports an operator a model decides.
func isModel(op contracts.Operator) bool {
	return op == contracts.OpAbout || op == contracts.OpMentions
}

// alwaysKnown reports a variable whose conditions are never Unknown on a
// message that is classified: its item's time and partition, authenticated
// by the meta.
func alwaysKnown(v contracts.Variable) bool {
	return v == contracts.VarReceived || v == contracts.VarMailbox
}

// cond is a condition with its value normalised, and parsed where comparing
// needs more than its bytes.
type cond struct {
	contracts.Condition
	kind   kind
	time   time.Time // kindTime
	size   int64     // kindSize
	local  string    // kindAddress: the local part
	domain string    // kindAddress: the domain; kindDomain: the value
}

// checkCondition normalises a condition, or says why it is refused and
// where in it (".var", ".op", ".value"). mailboxes, when not nil, are the
// mailboxes a mailbox value may be.
func checkCondition(c contracts.Condition, valueSent bool, mailboxes []string) (cond, string, contracts.FormulaReason) {
	ops, ok := allowed[c.Var]
	if !ok {
		return cond{}, ".var", contracts.FormulaUnknownVariable
	}
	if !slices.Contains(operators, c.Op) {
		return cond{}, ".op", contracts.FormulaUnknownOperator
	}
	k, ok := ops[c.Op]
	if !ok {
		return cond{}, ".op", contracts.FormulaOperatorNotAllowed
	}
	if k == kindNone {
		if valueSent {
			return cond{}, ".value", contracts.FormulaUnexpectedValue
		}
		return cond{Condition: c, kind: k}, "", ""
	}
	if !valueSent {
		return cond{}, ".value", contracts.FormulaMissingValue
	}
	out, reason := compileValue(c, k)
	if reason == "" && k == kindMailbox && mailboxes != nil && !slices.Contains(mailboxes, out.Value) {
		reason = contracts.FormulaUnknownMailbox
	}
	if reason != "" {
		return cond{}, ".value", reason
	}
	if out.Value == "" { // a value that normalises to nothing is no value
		return cond{}, ".value", contracts.FormulaInvalidValue
	}
	return positive(out), "", ""
}

// compileValue normalises c's value as a value of kind k.
func compileValue(c contracts.Condition, k kind) (cond, contracts.FormulaReason) {
	out := cond{Condition: c, kind: k}
	var reason contracts.FormulaReason
	switch k {
	case kindAddress:
		var confusable bool
		out.local, out.domain, confusable, reason = normaliseAddress(c.Value)
		out.Value = out.local + "@" + out.domain
		if reason == "" && confusable {
			reason = contracts.FormulaConfusableValue
		}
	case kindDomain:
		var confusable bool
		out.domain, confusable, reason = normaliseDomain(c.Value)
		out.Value = out.domain
		if reason == "" && confusable {
			reason = contracts.FormulaConfusableValue
		}
	case kindText, kindTopic:
		out.Value, reason = checkText(c.Value)
	case kindMediaType:
		out.Value, reason = checkMediaType(c.Value)
	case kindList:
		out.Value, reason = checkList(c.Value)
	case kindTime:
		out.Value, out.time, reason = checkTime(c.Value)
	case kindSize:
		out.Value, out.size, reason = checkSize(c.Value)
	default: // kindMailbox
		out.Value, reason = checkMailbox(c.Value)
	}
	return out, reason
}

// positive makes a negated received or size condition positive: not since t
// is before t, not at_least n is below n, and the reverse (ADR 0027, "Made
// positive").
func positive(c cond) cond {
	if !c.Not {
		return c
	}
	switch c.Op {
	case contracts.OpSince:
		c.Op, c.Not = contracts.OpBefore, false
	case contracts.OpBefore:
		c.Op, c.Not = contracts.OpSince, false
	case contracts.OpAtLeast:
		c.Op, c.Not = contracts.OpBelow, false
	case contracts.OpBelow:
		c.Op, c.Not = contracts.OpAtLeast, false
	}
	return c
}

// compile reads a condition of a canonical formula back, for Eval and
// Contains. ok is false for one that is not canonical (a Canonical built by
// hand): Eval makes it Unknown and Contains proves nothing with it.
func compile(c contracts.Condition) (cond, bool) {
	out, _, reason := checkCondition(c, c.Value != "", nil)
	return out, reason == "" && out.Condition == c
}

// compileFormula reads every condition of a canonical formula back.
func compileFormula(f contracts.Formula) ([][]cond, bool) {
	groups := make([][]cond, len(f.All))
	ok := true
	for i, g := range f.All {
		groups[i] = make([]cond, len(g.Any))
		for j, c := range g.Any {
			var good bool
			groups[i][j], good = compile(c)
			ok = ok && good
		}
	}
	return groups, ok
}

// compareConds is the order of conditions in the canonical form: variable,
// operator, sign (positive first), the bytes of the value.
func compareConds(a, b cond) int {
	return cmp.Or(
		cmp.Compare(slices.Index(variables, a.Var), slices.Index(variables, b.Var)),
		cmp.Compare(slices.Index(operators, a.Op), slices.Index(operators, b.Op)),
		compareBool(a.Not, b.Not),
		strings.Compare(a.Value, b.Value),
	)
}

func compareBool(a, b bool) int {
	switch {
	case a == b:
		return 0
	case !a:
		return -1
	default:
		return 1
	}
}

// compareGroups orders groups by their sorted conditions, one by one, a
// shorter group first when it is the start of the other.
func compareGroups(a, b []cond) int {
	return slices.CompareFunc(a, b, compareConds)
}

// Canonicalise is Parse's second half, for a formula already decoded (a
// rule's, read back from storage): values normalised, the rewrites applied,
// sorted. It does not refuse "never"; it returns it. A refusal is a
// *Problem, with the path of the first problem in the formula's order.
func Canonicalise(f contracts.Formula) (*Canonical, error) {
	groups, p := checkFormula(f)
	if p != nil {
		return nil, p
	}
	return canonical(groups, nil), nil
}

// checkFormula checks a decoded formula as Parse checks one sent, in its
// order: the version, then each group and condition with the limits.
func checkFormula(f contracts.Formula) ([][]cond, *Problem) {
	if f.V != contracts.FormulaVersion {
		return nil, problem("v", contracts.FormulaUnsupportedVersion)
	}
	var l limits
	groups := make([][]cond, 0, len(f.All))
	for i, g := range f.All {
		at := "all[" + strconv.Itoa(i) + "]"
		if p := l.group(at); p != nil {
			return nil, p
		}
		gs := make([]cond, 0, len(g.Any))
		for j, c := range g.Any {
			cat := at + ".any[" + strconv.Itoa(j) + "]"
			if p := l.condition(cat); p != nil {
				return nil, p
			}
			out, where, reason := checkCondition(c, c.Value != "", nil)
			if reason != "" {
				return nil, problem(cat+where, reason)
			}
			if p := l.model(cat, out); p != nil {
				return nil, p
			}
			gs = append(gs, out)
		}
		if len(gs) == 0 {
			return nil, problem(at+".any", contracts.FormulaEmptyGroup)
		}
		groups = append(groups, gs)
	}
	return groups, nil
}

// limits counts what a formula holds as it is read, in its order.
type limits struct {
	groups, inGroup, conditions, models int
}

func (l *limits) group(at string) *Problem {
	l.groups++
	l.inGroup = 0
	if l.groups > contracts.MaxGroups {
		return problem(at, contracts.FormulaTooManyGroups)
	}
	return nil
}

func (l *limits) condition(at string) *Problem {
	l.inGroup++
	l.conditions++
	if l.inGroup > contracts.MaxGroupConditions || l.conditions > contracts.MaxConditions {
		return problem(at, contracts.FormulaTooManyConditions)
	}
	return nil
}

func (l *limits) model(at string, c cond) *Problem {
	if !isModel(c.Op) {
		return nil
	}
	l.models++
	if l.models > contracts.MaxModelConditions {
		return problem(at, contracts.FormulaTooManyModel)
	}
	return nil
}

// stats counts the work of canonicalising, for Q14.
type stats struct {
	rewrites    int   // conditions and groups taken out
	comparisons []int // tests between two conditions, per pass of a rule
}

func (s *stats) pass(n int) {
	if s != nil {
		s.comparisons = append(s.comparisons, n)
	}
}

func (s *stats) rewrite() {
	if s != nil {
		s.rewrites++
	}
}

// canonical applies the rewrites to checked groups and writes the canonical
// form. The groups are sorted and their duplicates taken out first, so that
// every order and repetition of a formula starts the rewrites from one set,
// in one order.
func canonical(groups [][]cond, s *stats) *Canonical {
	gs := sortGroups(groups)
	never := false
	for changed := true; changed && !never; {
		changed = false
		for _, rule := range []func([][]cond, *stats) ([][]cond, bool){absorbInGroups, dropCovering, absorbGroups, ruleOut} {
			var c bool
			gs, c = rule(gs, s)
			changed = changed || c
			if slices.ContainsFunc(gs, func(g []cond) bool { return len(g) == 0 }) {
				never = true
				break
			}
		}
	}
	f := contracts.Formula{V: contracts.FormulaVersion, All: []contracts.Group{}}
	if never {
		f.All = []contracts.Group{{Any: []contracts.Condition{}}}
	} else {
		for _, g := range sortGroups(gs) {
			cs := make([]contracts.Condition, len(g))
			for i, c := range g {
				cs[i] = c.Condition
			}
			f.All = append(f.All, contracts.Group{Any: cs})
		}
	}
	return &Canonical{Formula: f, ID: formulaID(f), Never: never}
}

// sortGroups sorts each group's conditions and the groups, and takes out
// duplicates of both (rules 1 and 4 on the sorted set).
func sortGroups(groups [][]cond) [][]cond {
	out := make([][]cond, 0, len(groups))
	for _, g := range groups {
		g = slices.Clone(g)
		slices.SortFunc(g, compareConds)
		out = append(out, slices.CompactFunc(g, func(a, b cond) bool { return compareConds(a, b) == 0 }))
	}
	slices.SortFunc(out, compareGroups)
	return slices.CompactFunc(out, func(a, b []cond) bool { return compareGroups(a, b) == 0 })
}

// absorbInGroups is rules 1 and 2: of two conditions of a group where one
// implies the other, the one that implies goes (their OR is the greater). In
// the canonical order, one at a time, so that the result does not depend on
// which pair is seen first.
func absorbInGroups(gs [][]cond, s *stats) ([][]cond, bool) {
	n, changed := 0, false
	for gi, g := range gs {
		for i := 0; i < len(g); {
			absorbed := false
			for j := range g {
				if j == i {
					continue
				}
				n++
				if implies(g[i], g[j]) {
					absorbed = true
					break
				}
			}
			if absorbed {
				g = slices.Delete(g, i, i+1)
				s.rewrite()
				changed = true
				continue
			}
			i++
		}
		gs[gi] = g
	}
	s.pass(n)
	return gs, changed
}

// dropCovering is rule 3: a group that holds two covering conditions of a
// variable that is always known always holds, and goes.
func dropCovering(gs [][]cond, s *stats) ([][]cond, bool) {
	n, changed := 0, false
	out := gs[:0]
	for _, g := range gs {
		covered := false
		for i := 0; i < len(g) && !covered; i++ {
			for j := i + 1; j < len(g); j++ {
				n++
				if covering(g[i], g[j]) {
					covered = true
					break
				}
			}
		}
		if covered {
			s.rewrite()
			changed = true
			continue
		}
		out = append(out, g)
	}
	s.pass(n)
	return out, changed
}

// absorbGroups is rules 4 and 5: a group implied by another (every
// condition of the other implies one of it) goes, one at a time in the
// canonical order.
func absorbGroups(gs [][]cond, s *stats) ([][]cond, bool) {
	n, changed := 0, false
	for i := 0; i < len(gs); {
		absorbed := false
		for j := range gs {
			if j == i {
				continue
			}
			implied, tests := groupImplies(gs[j], gs[i])
			n += tests
			if implied {
				absorbed = true
				break
			}
		}
		if absorbed {
			gs = slices.Delete(gs, i, i+1)
			s.rewrite()
			changed = true
			continue
		}
		i++
	}
	s.pass(n)
	return gs, changed
}

// groupImplies reports whether every condition of a implies some condition
// of b, and how many tests it took.
func groupImplies(a, b []cond) (bool, int) {
	n := 0
	for _, ca := range a {
		found := false
		for _, cb := range b {
			n++
			if implies(ca, cb) {
				found = true
				break
			}
		}
		if !found {
			return false, n
		}
	}
	return true, n
}

// ruleOut is rule 6: a group of one condition of a variable that is always
// known takes every condition disjoint from it out of every other group. A
// group left empty makes the formula "never".
func ruleOut(gs [][]cond, s *stats) ([][]cond, bool) {
	n, changed := 0, false
	for li, lone := range gs {
		if len(lone) != 1 || !alwaysKnown(lone[0].Var) {
			continue
		}
		for gi, g := range gs {
			if gi == li {
				continue
			}
			for i := 0; i < len(g); {
				n++
				if disjoint(lone[0], g[i]) {
					g = slices.Delete(g, i, i+1)
					s.rewrite()
					changed = true
					continue
				}
				i++
			}
			gs[gi] = g
		}
	}
	s.pass(n)
	return gs, changed
}

// formulaID is "qry_" and the first 16 bytes of the SHA-256 of the canonical
// JSON, in lower-case base32 without padding.
func formulaID(f contracts.Formula) string {
	sum := sha256.Sum256(canonicalJSON(f))
	return "qry_" + idEncoding.EncodeToString(sum[:16])
}

var idEncoding = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)

// canonicalJSON is the formula as encoding/json writes it from its struct,
// with no space: the same bytes in every enclave and release of version 1.
func canonicalJSON(f contracts.Formula) []byte {
	b, _ := json.Marshal(f) // a Formula of strings, bools and an int always encodes
	return b
}
