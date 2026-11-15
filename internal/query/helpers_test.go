package query_test

import (
	"encoding/json"
	"testing"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/query"
)

// canon parses a formula that must be accepted.
func canon(t testing.TB, s string) *query.Canonical {
	t.Helper()
	c, err := query.Parse([]byte(s), nil)
	if err != nil {
		t.Fatalf("Parse(%s): %v", s, err)
	}
	return c
}

// canonOf canonicalises a decoded formula that must be accepted.
func canonOf(t testing.TB, f contracts.Formula) *query.Canonical {
	t.Helper()
	c, err := query.Canonicalise(f)
	if err != nil {
		b, _ := json.Marshal(f)
		t.Fatalf("Canonicalise(%s): %v", b, err)
	}
	return c
}

// jsonOf is a canonical form's bytes.
func jsonOf(c *query.Canonical) string {
	b, _ := json.Marshal(c.Formula)
	return string(b)
}

// one is a formula of one group of one condition.
func one(v contracts.Variable, op contracts.Operator, value string, not bool) contracts.Formula {
	return contracts.Formula{V: 1, All: []contracts.Group{{Any: []contracts.Condition{{Var: v, Op: op, Value: value, Not: not}}}}}
}

// full is a message whose item, meta and entry were read.
func full() *query.Message {
	return &query.Message{Have: query.FromItem | query.FromMeta | query.FromEntry, ListRecorded: true}
}

func eval(t testing.TB, c *query.Canonical, m *query.Message) query.Value {
	t.Helper()
	return query.Eval([]*query.Canonical{c}, m, nil, query.ForQuery).Value
}

var valueNames = map[query.Value]string{query.False: "False", query.Unknown: "Unknown", query.True: "True"}

func name(v query.Value) string { return valueNames[v] }
