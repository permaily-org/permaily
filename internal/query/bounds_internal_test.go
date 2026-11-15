package query

import (
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/permaily-org/permaily/internal/contracts"
)

// worstFormulas are formulas at the limits (64 conditions) where every pair
// of conditions is related: texts each a substring of the next, domains
// each under the next, ranges, so that every rewrite has work.
func worstFormulas() map[string]contracts.Formula {
	text := func(i int) contracts.Condition {
		return contracts.Condition{Var: contracts.VarSubject, Op: contracts.OpContains, Value: strings.Repeat("a", i+1)}
	}
	domain := func(i int) contracts.Condition {
		return contracts.Condition{Var: contracts.VarFromDomain, Op: contracts.OpIs, Value: strings.Repeat("a.", 63-i) + "example.com"}
	}
	since := func(i int) contracts.Condition {
		return contracts.Condition{Var: contracts.VarReceived, Op: contracts.OpSince, Value: fmt.Sprintf("2026-01-01T00:%02d:00Z", i%60)}
	}
	unrelated := func(i int) contracts.Condition {
		return contracts.Condition{Var: contracts.VarBody, Op: contracts.OpContains, Value: fmt.Sprintf("u%02d", i)}
	}
	shape := func(groups, per int, c func(int) contracts.Condition) contracts.Formula {
		f := contracts.Formula{V: 1}
		for g := range groups {
			var grp contracts.Group
			for k := range per {
				grp.Any = append(grp.Any, c(g*per+k))
			}
			f.All = append(f.All, grp)
		}
		return f
	}
	return map[string]contracts.Formula{
		"two groups of 32 texts":     shape(2, 32, text),
		"32 groups of two texts":     shape(32, 2, text),
		"two groups of 32 domains":   shape(2, 32, domain),
		"32 groups of two domains":   shape(32, 2, domain),
		"32 groups of two times":     shape(32, 2, since),
		"two groups of 32 unrelated": shape(2, 32, unrelated),
		"32 groups of two unrelated": shape(32, 2, unrelated),
		"one group of 32, and more":  shape(2, 32, func(i int) contracts.Condition { return []func(int) contracts.Condition{text, domain}[i%2](i) }),
	}
}

// Q14 of ADR 0027: the worst formula is canonicalised in at most as many
// rewrites as it has conditions and groups, and every pass of a rule makes
// at most 64 × 64 tests of two conditions.
func TestQ14CanonicalisingIsBounded(t *testing.T) {
	check := func(name string, f contracts.Formula) {
		groups, p := checkFormula(f)
		if p != nil {
			t.Fatalf("%s: %v", name, p)
		}
		n := 0
		for _, g := range f.All {
			n += len(g.Any)
		}
		var s stats
		c := canonical(groups, &s)
		if s.rewrites > n+len(f.All) {
			t.Errorf("%s: %d rewrites for %d conditions and %d groups", name, s.rewrites, n, len(f.All))
		}
		most := 0
		for _, k := range s.comparisons {
			most = max(most, k)
		}
		if most > contracts.MaxConditions*contracts.MaxConditions {
			t.Errorf("%s: a pass of %d tests", name, most)
		}
		if name != "" {
			t.Logf("%s: %d rewrites, %d passes of rules, at most %d tests in one, to %d groups", name, s.rewrites, len(s.comparisons), most, len(c.Formula.All))
		}
	}
	for name, f := range worstFormulas() {
		check(name, f)
	}
	// And at the limits, generated.
	r := rand.New(rand.NewPCG(14, 14))
	for range 300 {
		f := contracts.Formula{V: 1}
		left := contracts.MaxConditions
		for left > 0 && len(f.All) < contracts.MaxGroups {
			per := min(left, 1+r.IntN(contracts.MaxGroupConditions))
			var g contracts.Group
			for range per {
				g.Any = append(g.Any, []contracts.Condition{
					{Var: contracts.VarSubject, Op: contracts.OpContains, Value: strings.Repeat("ab", 1+r.IntN(5))},
					{Var: contracts.VarFromDomain, Op: contracts.OpIs, Value: []string{"example.com", "a.example.com", "b.a.example.com"}[r.IntN(3)]},
					{Var: contracts.VarReceived, Op: []contracts.Operator{contracts.OpSince, contracts.OpBefore}[r.IntN(2)], Value: fmt.Sprintf("2026-0%d-01T00:00:00Z", 1+r.IntN(4))},
					{Var: contracts.VarMailbox, Op: contracts.OpIs, Value: []string{"mbx_aaaaaaaaaaaaaaaaaaaaaaaaaa", "mbx_bbbbbbbbbbbbbbbbbbbbbbbbbb"}[r.IntN(2)], Not: r.IntN(2) == 0},
				}[r.IntN(4)])
			}
			left -= per
			f.All = append(f.All, g)
		}
		check("", f)
	}
}

// Q19: two formulas at their limits are compared in at most 4,096 tests of
// two conditions.
func TestQ19ContainmentIsBounded(t *testing.T) {
	worst := 0
	fs := worstFormulas()
	// Formulas whose groups no rewrite merges: every group is tested
	// against every group.
	wide := func(prefix string) contracts.Formula {
		f := contracts.Formula{V: 1}
		for g := range 2 {
			var grp contracts.Group
			for k := range 31 {
				grp.Any = append(grp.Any, contracts.Condition{Var: contracts.VarFromDomain, Op: contracts.OpIs, Value: fmt.Sprintf("d%02d%d.example", k, g)})
			}
			grp.Any = append(grp.Any, contracts.Condition{Var: contracts.VarSubject, Op: contracts.OpContains, Value: prefix + fmt.Sprint(g)})
			f.All = append(f.All, grp)
		}
		return f
	}
	narrow := contracts.Formula{V: 1}
	for g := range 2 {
		var grp contracts.Group
		for k := range 32 {
			grp.Any = append(grp.Any, contracts.Condition{Var: contracts.VarSubject, Op: contracts.OpContains, Value: fmt.Sprintf("x%d%c%c", g, 'a'+k/26, 'a'+k%26)})
		}
		narrow.All = append(narrow.All, grp)
	}
	fs["wide"] = wide("x")
	fs["narrow"] = narrow
	for na, a := range fs {
		for nb, b := range fs {
			ca, err := Canonicalise(a)
			if err != nil {
				t.Fatal(err)
			}
			cb, err := Canonicalise(b)
			if err != nil {
				t.Fatal(err)
			}
			_, n := contains(ca, cb)
			if n > contracts.MaxConditions*contracts.MaxConditions {
				t.Errorf("Contains(%s, %s): %d tests", na, nb, n)
			}
			worst = max(worst, n)
		}
	}
	t.Logf("the most tests of one comparison: %d of at most %d", worst, contracts.MaxConditions*contracts.MaxConditions)
	if worst < 1000 {
		t.Errorf("the worst case tested only %d: not a worst case", worst)
	}
}

// BenchmarkCanonicaliseWorst reports the time of the worst formulas; it
// asserts nothing.
func BenchmarkCanonicaliseWorst(b *testing.B) {
	for name, f := range worstFormulas() {
		b.Run(name, func(b *testing.B) {
			for b.Loop() {
				if _, err := Canonicalise(f); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

// BenchmarkContainsWorst reports the time of comparing two formulas at
// their limits; it asserts nothing.
func BenchmarkContainsWorst(b *testing.B) {
	fs := worstFormulas()
	a, _ := Canonicalise(fs["two groups of 32 domains"])
	c, _ := Canonicalise(fs["32 groups of two domains"])
	for b.Loop() {
		Contains(a, c)
	}
}

// BenchmarkParse and BenchmarkEval report the time of reading the worst
// formula and of evaluating it on one message.
func BenchmarkParse(b *testing.B) {
	data := []byte(`{"v":1,"all":[{"any":[` + strings.Repeat(`{"var":"subject","op":"contains","value":"Ｉｎｖｏｉｃｅ 2026 for your account"},`, 31) + `{"var":"from_domain","op":"is","value":"Bücher.example"}]}]}`)
	for b.Loop() {
		if _, err := Parse(data, nil); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkEval(b *testing.B) {
	f, _ := Canonicalise(worstFormulas()["one group of 32, and more"])
	m := &Message{Have: FromItem | FromMeta | FromEntry, From: []string{"a@x.example.com"}, Subject: strings.Repeat("an invoice, ", 100), Body: strings.Repeat("text ", 3000)}
	for b.Loop() {
		Eval([]*Canonical{f}, m, nil, ForQuery)
	}
}
