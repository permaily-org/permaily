package decide

import (
	_ "embed"
	"encoding/json"
	"errors"
	"math"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/query"
)

// The canary (ADR 0028, "The canary"): fixed synthetic messages and
// statements, never mail, put to the model once a day with this prompt
// version. The model cannot be pinned (only gpt-6-luna exists, with no dated
// snapshot) and its answers are repeatable, so an answer that moves by more
// than Tolerance says the model changed under the same name. It alerts the
// operator; it does not stop the service.
//
// canary.json holds four messages of the spike's corpus
// (testdata/corpus.jsonl: inv01, inv08, mkt11 and pwd13), chosen for answers
// inside the bands of doubt and for the weaknesses the spike found (a
// business address, the novel "The Invoice", a Spanish sign-in alert), with
// every one of their 39 atoms and the probability the spike measured for
// each. ADR 0028's pull request 2 measures them again with the enclave's own
// client before the first release that runs it, and commits what it finds.
//
//go:embed canary.json
var canaryJSON []byte

// Canary is the set the canary asks.
type Canary struct {
	V             int             `json:"v"`
	PromptVersion string          `json:"prompt_version"`
	Model         string          `json:"model"`
	Measured      string          `json:"measured"`
	MeasuredBy    string          `json:"measured_by"`
	Tolerance     float64         `json:"tolerance"`
	Messages      []CanaryMessage `json:"messages"`
}

// CanaryMessage is one synthetic message and what is expected of it.
type CanaryMessage struct {
	ID      string       `json:"id"`
	Subject string       `json:"subject"`
	Body    string       `json:"body"`
	Atoms   []CanaryAtom `json:"atoms"`
}

// CanaryAtom is one statement and the probability expected for it.
type CanaryAtom struct {
	Var   contracts.Variable `json:"var"`
	Op    contracts.Operator `json:"op"`
	Topic string             `json:"topic"`
	P     float64            `json:"p"`
}

// Atom is the query language's atom of a.
func (a CanaryAtom) Atom() query.Atom { return query.Atom{Var: a.Var, Op: a.Op, Topic: a.Topic} }

// LoadCanary reads the canary compiled into the image, and refuses one
// that is not for this prompt version or holds a statement no model
// decides.
func LoadCanary() (*Canary, error) {
	var c Canary
	if err := json.Unmarshal(canaryJSON, &c); err != nil {
		return nil, errors.New("decide: the canary does not read")
	}
	if c.V != 1 || c.PromptVersion != PromptVersion || c.Tolerance <= 0 || c.Tolerance >= 0.5 || len(c.Messages) == 0 {
		return nil, errors.New("decide: the canary is not for this prompt")
	}
	for _, m := range c.Messages {
		if len(m.Atoms) == 0 {
			return nil, errors.New("decide: a canary message has no statement")
		}
		for _, a := range m.Atoms {
			if _, ok := Instructions(a.Atom()); !ok || math.IsNaN(a.P) || a.P < 0 || a.P > 1 {
				return nil, errors.New("decide: a canary statement is not one a model decides")
			}
		}
	}
	return &c, nil
}
