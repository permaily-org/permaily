package query

// Compiled is a canonical form read back once, so that a test of containment
// over millions of pairs (Q18) runs the proof without reading each formula
// again for every pair. Contains is compileFormula then the same proof.
type Compiled struct {
	never  bool
	groups [][]cond
}

// Compile reads a canonical form back; ok is false for one built by hand.
func Compile(c *Canonical) (*Compiled, bool) {
	g, ok := compileFormula(c.Formula)
	return &Compiled{c.Never, g}, ok
}

// ContainsCompiled is Contains on compiled forms.
func ContainsCompiled(outer, inner *Compiled) bool {
	ok, _ := containsGroups(outer.never, outer.groups, inner.never, inner.groups)
	return ok
}
