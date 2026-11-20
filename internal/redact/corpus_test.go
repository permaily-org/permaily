package redact_test

import (
	"bufio"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/permaily-org/permaily/internal/contracts"
)

// shapeCase is one golden case of testdata/shapes.jsonl: what Shapes (ADR
// 0040's pull request 2) must answer for the kinds and the text.
type shapeCase struct {
	Name   string                      `json:"name"`
	Kinds  []contracts.ShapeKind       `json:"kinds"`
	In     string                      `json:"in"`
	Out    string                      `json:"out"`
	Shapes map[contracts.ShapeKind]int `json:"shapes"`
}

func readCases(t *testing.T) []shapeCase {
	t.Helper()
	f, err := os.Open("testdata/shapes.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var cases []shapeCase
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var c shapeCase
		dec := json.NewDecoder(strings.NewReader(sc.Text()))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&c); err != nil {
			t.Fatalf("case %d: %v", len(cases)+1, err)
		}
		cases = append(cases, c)
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return cases
}

// U8's corpus is well formed, until pull request 2 runs it: every kind is
// one of ADR 0040's six and each is exercised, a case's counts name only its
// own kinds, and its output has as many markers as pieces removed (a case
// that removes nothing keeps its text as it is).
func TestU8TheShapesCorpusIsWellFormed(t *testing.T) {
	cases := readCases(t)
	used := map[contracts.ShapeKind]bool{}
	names := map[string]bool{}
	for _, c := range cases {
		if names[c.Name] {
			t.Errorf("%q twice", c.Name)
		}
		names[c.Name] = true
		pieces := 0
		for k, n := range c.Shapes {
			if !k.Valid() || n < 1 {
				t.Errorf("%q counts %d of %q", c.Name, n, k)
			}
			found := false
			for _, ck := range c.Kinds {
				found = found || ck == k
			}
			if !found {
				t.Errorf("%q counts %q, which it does not ask", c.Name, k)
			}
			used[k] = true
			pieces += n
		}
		markers := strings.Count(c.Out, contracts.RemovedMarker) - strings.Count(c.In, contracts.RemovedMarker)
		if markers != pieces {
			t.Errorf("%q: %d markers added for %d pieces", c.Name, markers, pieces)
		}
		if pieces == 0 && c.Out != c.In {
			t.Errorf("%q removes nothing and changes the text", c.Name)
		}
	}
	for _, k := range contracts.ShapeKinds {
		if !used[k] {
			t.Errorf("no case removes %q", k)
		}
	}
}
