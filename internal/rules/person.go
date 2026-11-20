package rules

import (
	"fmt"
	"slices"
	"strconv"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/query"
)

// The person's own rules (ADR 0040), beside the compiled Default: what the
// account's record of rules holds, which of them apply to a connection's
// client and in what order, and what one removes. Committed with the design
// as the seam ADR 0040's pull requests share: the rules service (pull request
// 1) reads and writes Record, the discloser (pull request 5) evaluates the
// Rules For answers, and delivery (ADR 0039) gets them through the same
// service. Pure, like the rest of the package.

// Remove takes details out of a message the rule applies to (Rule.Removal)
// before the connection's formulas read it; the rest leaves.
const Remove Effect = "remove"

// Removal is what a Remove rule takes out, in this order: whole Parts; every
// piece of the subject and the text with one of Shapes, by internal/redact's
// patterns; then each sentence the model says mentions one of Topics (each an
// atom `message mentions <topic>`, asked sentence by sentence, never as the
// query language asks it). A rule that removes Shapes or Topics removes the
// message's attachments too (ADR 0040, choice 6).
type Removal struct {
	Parts  []contracts.Part
	Shapes []contracts.ShapeKind
	Topics []query.Atom
}

// Attachments says the removal takes the message's attachments out.
func (r *Removal) Attachments() bool {
	return r != nil && (slices.Contains(r.Parts, contracts.PartAttachments) || len(r.Shapes) > 0 || len(r.Topics) > 0)
}

// Scope is which connections a rule is for, by their client (contracts.RuleScope).
type Scope struct {
	To      contracts.RuleScopeTo
	Clients []string
}

// Covers says a connection of client is one the rule is for. A scope this
// release does not know covers every client: a rule protects, so a scope it
// cannot read errs toward applying (ADR 0040, "Scope").
func (s Scope) Covers(client string) bool {
	switch s.To {
	case contracts.ScopeOnly:
		return slices.Contains(s.Clients, client)
	case contracts.ScopeAllExcept:
		return !slices.Contains(s.Clients, client)
	}
	return true
}

// Record is the document of one epoch of an account's record of rules: a
// strict anchored record, sealed, made with CreateNew at the account's first
// change of its rules and written at the version read (ADR 0040, "The rules'
// record"). An account without one has the default on and no rule of its
// own, which is every account before step 7.
type Record struct {
	V       int    `json:"v"`
	Account string `json:"account"`
	Epoch   uint64 `json:"epoch"`
	// Next says the epoch after this one was made and holds the rules now:
	// written when this epoch's versions reach RecordVersionsMax, before
	// the next is made with everything this one held, as the account's
	// list of connections rolls over (ADR 0030, C2).
	Next bool `json:"next,omitzero"`
	// DefaultOff says the person turned the default rule off, and
	// DefaultChangedAt when it was last turned on or off.
	DefaultOff       bool      `json:"default_off,omitzero"`
	DefaultChangedAt time.Time `json:"default_changed_at,omitzero"`
	// Notice is the rules' notice the account agreed to.
	Notice *contracts.RuleNoticeAgreement `json:"notice,omitempty"`
	Rules  []Stored                       `json:"rules"`
}

// RecordFormat is the Record.V written.
const RecordFormat = 1

// Stored is one rule in the record: its identifier ("rul_" and 26 base32
// characters, random), its version, the draft as the person saved it,
// canonical, the identifier of that draft, the rules' notice it was saved
// under when it asks a model, and when it was made and last changed.
type Stored struct {
	ID        string              `json:"id"`
	Version   int                 `json:"version"`
	Draft     contracts.RuleDraft `json:"draft"`
	DraftID   string              `json:"draft_id"`
	Notice    string              `json:"notice,omitempty"`
	CreatedAt time.Time           `json:"created_at"`
	ChangedAt time.Time           `json:"changed_at"`
}

// The record's bounds (ADR 0040, "The rules' record"): its JSON before
// sealing, within a store item with room to spare; and its versions, below
// the strict read's 10,000, after which the record rolls to its next epoch.
const (
	RecordBytesMax    = 300 << 10
	RecordVersionsMax = 9000
)

// RecordName is the name of an account's record of rules in its epoch.
func RecordName(account string, epoch uint64) string {
	return "rules-" + account + "-" + strconv.FormatUint(epoch, 10)
}

// For is the rules a connection of client applies before its formulas, in
// the order they are evaluated: the default first unless the person turned
// it off, then the person's rules that are on and cover client, those that
// hold before those that remove, each kind in the order they were made.
// noticeInForce is the rules' notice of this release: a rule that asks a
// model and was saved under another, or whose account agreed to another, is
// answered with ModelOff set, so it asks nothing and applies wherever it
// would need the model. A stored rule that does not compile is an error, and
// the caller gives nothing for the account: a rule that cannot be read must
// not let mail out.
func (rec *Record) For(client, noticeInForce string) ([]Rule, error) {
	out := []Rule{}
	if rec == nil || !rec.DefaultOff {
		out = append(out, Default())
	}
	if rec == nil {
		return out, nil
	}
	agreed := rec.Notice != nil && rec.Notice.Version == noticeInForce
	var holds, removes []Rule
	for _, s := range rec.Rules {
		if !s.Draft.On || !scopeOf(s.Draft.Scope).Covers(client) {
			continue
		}
		r, err := Compile(s)
		if err != nil {
			return nil, err
		}
		if r.Model() && (!agreed || s.Notice != noticeInForce) {
			r.ModelOff = true
		}
		if r.Effect == Hold {
			holds = append(holds, r)
		} else {
			removes = append(removes, r)
		}
	}
	return append(append(out, holds...), removes...), nil
}

func scopeOf(s contracts.RuleScope) Scope {
	return Scope{To: s.To, Clients: s.Clients}
}

// Compile is a stored rule as the service evaluates it: each criteria made
// canonical on its own (a criteria that matches no mail refused), and, for
// Remove, its removal with each topic normalised as the query language
// normalises one. It checks the shapes of what it reads, not the bounds a
// change is refused for (the rules service's, ADR 0040's pull request 1).
func Compile(s Stored) (Rule, error) {
	r := Rule{ID: s.ID, Version: s.Version}
	switch s.Draft.Effect {
	case contracts.EffectHold:
		r.Effect = Hold
		if s.Draft.Removal != nil {
			return Rule{}, fmt.Errorf("rules: %s: a removal on a rule that holds", s.ID)
		}
	case contracts.EffectRemove:
		r.Effect = Remove
		rm, err := compileRemoval(s.ID, s.Draft.Removal)
		if err != nil {
			return Rule{}, err
		}
		r.Removal = rm
	default:
		return Rule{}, fmt.Errorf("rules: %s: unknown effect", s.ID)
	}
	if len(s.Draft.Criteria) == 0 {
		return Rule{}, fmt.Errorf("rules: %s has no criteria", s.ID)
	}
	for i, f := range s.Draft.Criteria {
		c, err := query.Canonicalise(f)
		if err != nil {
			return Rule{}, fmt.Errorf("rules: %s, criteria %d: %w", s.ID, i, err)
		}
		if c.Never {
			return Rule{}, fmt.Errorf("rules: %s, criteria %d matches no mail", s.ID, i)
		}
		r.Formulas = append(r.Formulas, c)
	}
	return r, nil
}

func compileRemoval(id string, d *contracts.RuleRemoval) (*Removal, error) {
	if d == nil || len(d.Parts)+len(d.Shapes)+len(d.Topics) == 0 {
		return nil, fmt.Errorf("rules: %s removes nothing", id)
	}
	rm := &Removal{Parts: slices.Clone(d.Parts), Shapes: slices.Clone(d.Shapes)}
	for _, p := range d.Parts {
		switch p {
		case contracts.PartSender, contracts.PartSubject, contracts.PartBody, contracts.PartAttachments:
		default:
			return nil, fmt.Errorf("rules: %s removes an unknown part", id)
		}
	}
	for _, k := range d.Shapes {
		if !k.Valid() {
			return nil, fmt.Errorf("rules: %s removes an unknown shape", id)
		}
	}
	for i, t := range d.Topics {
		f := contracts.Formula{V: contracts.FormulaVersion, All: []contracts.Group{{Any: []contracts.Condition{
			{Var: contracts.VarMessage, Op: contracts.OpMentions, Value: t},
		}}}}
		c, err := query.Canonicalise(f)
		if err != nil {
			return nil, fmt.Errorf("rules: %s, topic %d: %w", id, i, err)
		}
		cond := c.Formula.All[0].Any[0]
		rm.Topics = append(rm.Topics, query.Atom{Var: cond.Var, Op: cond.Op, Topic: cond.Value})
	}
	return rm, nil
}

// Model says a model decides some of the rule: a criteria's about or
// mentions, or its removal's topics.
func (r Rule) Model() bool {
	if r.Removal != nil && len(r.Removal.Topics) > 0 {
		return true
	}
	for _, f := range r.Formulas {
		for _, g := range f.Formula.All {
			for _, c := range g.Any {
				if c.Op == contracts.OpAbout || c.Op == contracts.OpMentions {
					return true
				}
			}
		}
	}
	return false
}

// MeaningRef names the decision of which sentences a Remove rule's topics
// take out of a message (ADR 0040, "Removing by meaning"): apart from the
// rule's own decision of whether it applies, and bound to how the text was
// taken out of the message (textVersion, inbox.TextVersion) and what the
// fixed shapes removed before the model read it (shapesVersion,
// redact.Version), since a sentence's number means nothing under another
// split. A change of either is a new decision, never the old one used.
func MeaningRef(r Ref, textVersion, shapesVersion int) Ref {
	return Ref{ID: r.ID + "#meaning-t" + strconv.Itoa(textVersion) + "-s" + strconv.Itoa(shapesVersion), Version: r.Version}
}
