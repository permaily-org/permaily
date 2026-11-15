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

// ReadsBack reports whether the sentence of f in c, in its plain text and in
// its parts, is read back as f and as no other formula, by the test reader
// (sentence_parse_test.go), for the tests that generate with querytest.
func ReadsBack(f *Canonical, c Context) error {
	s := Render(f, c)
	for _, read := range []func() (*Canonical, error){
		func() (*Canonical, error) { return parseSentence(s.Text, c) },
		func() (*Canonical, error) { return parseParts(s, c) },
	} {
		got, err := read()
		if err != nil {
			return err
		}
		if !sameFormula(got, f) {
			return errNoReading
		}
	}
	return nil
}
