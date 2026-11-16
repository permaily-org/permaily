// Package rules is the person's own filters on what leaves (ADR 0019, how in
// ADR 0031): a rule is one or more formulas of the query language with an
// effect, applied to every message before any connection's query, and it
// applies when one of its formulas is True or Unknown (query.ForRule): every
// rule protects, so when in doubt it holds.
//
// Step 5 of F2 builds one rule, the same for every account and compiled into
// the image: Default, which holds back sign-in codes and password resets. Step
// 7 adds the person's own rules, their editor, removing details and turning
// the default off; the types here are the ones it extends.
//
// It is pure, like the query language: depguard's rules-are-pure lists what
// it may import.
package rules

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/query"
)

// Effect is what a rule does to a message it applies to.
type Effect string

const (
	// Hold keeps the message from every connection the rule is for: it does
	// not leave, and the vendor is not told it exists.
	Hold Effect = "hold"
)

// Rule is a rule as the service evaluates it. ID and Version name it in
// receipts and on /status; a change of a rule's formulas is a new Version.
type Rule struct {
	ID      string
	Version int
	Effect  Effect
	// Formulas are canonical, each on its own (never merged): the rule
	// applies when the greatest of their values, read for a rule, is True or
	// Unknown (query.Resolve with query.ForRule on each).
	Formulas []*query.Canonical
}

// Ref names one version of a rule: what a committed decision, a receipt and
// /status name it by.
type Ref struct {
	ID      string `json:"id"`
	Version int    `json:"version"`
}

// Ref is the rule's name and version.
func (r Rule) Ref() Ref { return Ref{ID: r.ID, Version: r.Version} }

// DefaultID is the identifier of the rule every account has from the start
// (ADR 0019, "A rule from the start"; ADR 0031).
const DefaultID = "default-sign-in-and-security"

// DefaultVersion is the version of its formulas, default.json. Changing the
// file is a new version, and a new receipt's rule.
const DefaultVersion = 1

//go:embed default.json
var defaultJSON []byte

var (
	defaultOnce sync.Once
	defaultRule Rule
	defaultErr  error
)

// Default is the rule that holds back sign-in, verification and one-time
// codes and password resets (ADR 0031, "The default rule"): two formulas of
// exact conditions on the subject and on the text, in English and Spanish,
// and one a model decides. It is the same for every account; it is compiled,
// so /status shows the canonical identifiers of its formulas and
// permaily-verify compares them with the source.
func Default() Rule {
	defaultOnce.Do(func() { defaultRule, defaultErr = parseRule(DefaultID, DefaultVersion, defaultJSON) })
	if defaultErr != nil {
		// default.json is part of the image and its test parses it; a file
		// that does not parse is a build that must not run.
		panic(defaultErr)
	}
	return defaultRule
}

// parseRule reads a rule's formulas from their JSON, a list of formulas, and
// makes each canonical. A formula that is "never" is refused: a rule's
// formula that matches no mail is a mistake.
func parseRule(id string, version int, data []byte) (Rule, error) {
	var fs []contracts.Formula
	if err := json.Unmarshal(data, &fs); err != nil {
		return Rule{}, fmt.Errorf("rules: %s: %w", id, err)
	}
	if len(fs) == 0 {
		return Rule{}, fmt.Errorf("rules: %s has no formula", id)
	}
	r := Rule{ID: id, Version: version, Effect: Hold}
	for i, f := range fs {
		c, err := query.Canonicalise(f)
		if err != nil {
			return Rule{}, fmt.Errorf("rules: %s, formula %d: %w", id, i, err)
		}
		if c.Never {
			return Rule{}, fmt.Errorf("rules: %s, formula %d matches no mail", id, i)
		}
		r.Formulas = append(r.Formulas, c)
	}
	return r, nil
}
