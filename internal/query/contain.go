package query

// Contains reports whether every message inner matches is one outer matches;
// false whenever it cannot prove it ("Containment" in ADR 0027). outer is a
// consented scope, never a request accepted because it fit.
//
// It proves group by group: inner fits inside outer when every group of
// outer is implied by one group of inner, condition by condition (every
// condition of that group implies some condition of outer's). That is sound
// in three values: wherever inner is True outer is True, and wherever inner
// is Unknown outer is Unknown or True. It is not complete: "false" asks the
// person again, and never lets mail leave.
func Contains(outer, inner *Canonical) bool {
	ok, _ := contains(outer, inner)
	return ok
}

// contains is Contains, and how many tests of two conditions it made (Q19).
func contains(outer, inner *Canonical) (bool, int) {
	if outer == nil || inner == nil {
		return false, 0
	}
	a, okA := compileFormula(outer.Formula)
	b, okB := compileFormula(inner.Formula)
	if !okA || !okB {
		return false, 0
	}
	return containsGroups(outer.Never, a, inner.Never, b)
}

// containsGroups is the proof, on compiled canonical forms.
func containsGroups(outerNever bool, a [][]cond, innerNever bool, b [][]cond) (bool, int) {
	switch {
	case innerNever:
		return true, 0
	case outerNever:
		return false, 0
	case len(a) == 0:
		return true, 0
	case len(b) == 0:
		return false, 0
	}
	n := 0
	for _, ga := range a {
		found := false
		for _, gb := range b {
			implied, tests := groupImplies(gb, ga)
			n += tests
			if implied {
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
