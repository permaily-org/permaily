// Package formula checks the formulas the web app builds by itself against
// the server's parser: tests only, never in the image.
package formula

import (
	"os"
	"testing"

	"github.com/permaily-org/permaily/internal/query"
)

// TestV54ReplacementFormulaParses parses reach.json, the formula the consent
// screen sends to the matches route to tell which mail the connection it
// replaces reads (web/src/pages/consent/preview.ts, reachedBefore; the web
// test "asks which of them the connection's own query matches" checks the
// screen sends exactly this), with the parser the route uses.
func TestV54ReplacementFormulaParses(t *testing.T) {
	data, err := os.ReadFile("reach.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := query.Parse(data, nil); err != nil {
		t.Fatalf("the matches route refuses the consent screen's formula: %v", err)
	}
}

// TestU32RuleEditorFormulaParses parses rule.json, a criteria the rule editor
// builds from its fields (web/src/pages/rules/criteria.ts, toFormula; the web
// test "builds a rule from conditions" checks the editor sends exactly this),
// with the parser the rules routes check a rule's criteria with.
func TestU32RuleEditorFormulaParses(t *testing.T) {
	data, err := os.ReadFile("rule.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := query.Parse(data, nil); err != nil {
		t.Fatalf("the rules routes refuse the rule editor's formula: %v", err)
	}
}
