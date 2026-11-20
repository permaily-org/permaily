package accountrules

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/oauth"
	"github.com/permaily-org/permaily/internal/query"
	"github.com/permaily-org/permaily/internal/rules"
)

// What the system says of a draft (contracts.RuleCheck): checked against
// every bound of a rule (ADR 0040, "The rules' record", U6), made canonical,
// named by the digest of its canonical JSON, and written as the system's own
// sentence, with no model.

// checked is a draft the system accepted: what the screen is shown, and the
// canonical identifiers of its criteria, which its receipt names.
type checked struct {
	check    *contracts.RuleCheck
	criteria []string
	model    int // the conditions a model decides over its criteria
	meaning  bool
}

// partOrder is the canonical order of a removal's parts.
var partOrder = []contracts.Part{contracts.PartSender, contracts.PartSubject, contracts.PartBody, contracts.PartAttachments}

// Check implements Rules.
func (s *Service) Check(ctx context.Context, account string, d contracts.RuleDraft) (*contracts.RuleCheck, error) {
	mailboxes, err := s.cfg.Mailboxes.Catalogue(ctx, account)
	if err != nil {
		return nil, err
	}
	c, err := s.check(d, mailboxes, mailboxes)
	if err != nil {
		return nil, err
	}
	return c.check, nil
}

// check checks d with its criteria naming only mailboxes (nil: any well
// formed identifier, for a rule read back after a mailbox went), and writes
// its sentence naming the mailboxes of names.
func (s *Service) check(d contracts.RuleDraft, mailboxes, names []string) (*checked, error) {
	if !utf8.ValidString(d.Name) || utf8.RuneCountInString(d.Name) > contracts.MaxRuleNameRunes || strings.ContainsFunc(d.Name, unicode.IsControl) {
		return nil, contracts.InvalidRule(contracts.RuleNameTooLong)
	}
	if len(d.Criteria) == 0 || len(d.Criteria) > contracts.MaxRuleCriteria {
		return nil, contracts.InvalidRule(contracts.RuleNoCriteria)
	}
	out := &checked{}
	canon := contracts.RuleDraft{Name: d.Name, Effect: d.Effect, On: d.On}
	var formulas []*query.Canonical
	for _, f := range d.Criteria {
		raw, err := json.Marshal(f)
		if err != nil {
			return nil, contracts.InvalidRule(contracts.RuleNoCriteria)
		}
		c, err := query.Parse(raw, mailboxes)
		if err != nil {
			return nil, err
		}
		if slices.Contains(out.criteria, c.ID) {
			return nil, contracts.InvalidRule(contracts.RuleDuplicate)
		}
		out.criteria = append(out.criteria, c.ID)
		out.model += modelConditions(c)
		formulas = append(formulas, c)
		canon.Criteria = append(canon.Criteria, c.Formula)
	}
	if out.model > contracts.MaxRuleModelConditions {
		return nil, contracts.InvalidRule(contracts.RuleTooManyModel)
	}
	switch d.Effect {
	case contracts.EffectHold:
		if d.Removal != nil {
			return nil, contracts.InvalidRule(contracts.RuleRemovalUnexpected)
		}
	case contracts.EffectRemove:
		rm, err := checkRemoval(d.Removal)
		if err != nil {
			return nil, err
		}
		canon.Removal, out.meaning = rm, len(rm.Topics) > 0
	default:
		return nil, contracts.InvalidRule(contracts.RuleUnknownEffect)
	}
	scope, err := checkScope(d.Scope)
	if err != nil {
		return nil, err
	}
	canon.Scope = scope
	stored := rules.Stored{
		ID: "rul_" + strings.Repeat("a", 26), Version: 1 << 30, Draft: canon, DraftID: "rdr_" + strings.Repeat("a", 26),
		Notice: s.cfg.Notice, CreatedAt: time.Unix(1<<33, 999999999).UTC(), ChangedAt: time.Unix(1<<33, 999999999).UTC(),
	}
	if b, err := json.Marshal(stored); err != nil || len(b) > contracts.MaxRuleBytes {
		return nil, contracts.InvalidRule(contracts.RuleTooLarge)
	}
	r, err := rules.Compile(stored)
	if err != nil {
		return nil, contracts.InvalidRule(contracts.RuleNoCriteria)
	}
	raw, err := json.Marshal(canon)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(raw)
	out.check = &contracts.RuleCheck{
		DraftID: "rdr_" + idEncoding.EncodeToString(sum[:16]), Draft: canon,
		Sentence: sentence(formulas, names), Does: does(canon.Effect, canon.Removal), Model: r.Model(),
	}
	out.check.For, out.check.Clients = forWhom(scope)
	return out, nil
}

// modelConditions counts the conditions a model decides in a formula.
func modelConditions(c *query.Canonical) int {
	n := 0
	for _, g := range c.Formula.All {
		for _, cond := range g.Any {
			if cond.Op == contracts.OpAbout || cond.Op == contracts.OpMentions {
				n++
			}
		}
	}
	return n
}

// checkRemoval is a removal made canonical: its parts in partOrder, its
// shapes in contracts.ShapeKinds' order, its topics normalised as the query
// language normalises one, sorted.
func checkRemoval(d *contracts.RuleRemoval) (*contracts.RuleRemoval, error) {
	if d == nil || len(d.Parts)+len(d.Shapes)+len(d.Topics) == 0 {
		return nil, contracts.InvalidRule(contracts.RuleRemovalMissing)
	}
	out := &contracts.RuleRemoval{}
	for i, p := range d.Parts {
		if !slices.Contains(partOrder, p) {
			return nil, contracts.InvalidRule(contracts.RuleUnknownPart)
		}
		if slices.Contains(d.Parts[:i], p) {
			return nil, contracts.InvalidRule(contracts.RuleDuplicate)
		}
	}
	for _, p := range partOrder {
		if slices.Contains(d.Parts, p) {
			out.Parts = append(out.Parts, p)
		}
	}
	for i, k := range d.Shapes {
		if !k.Valid() {
			return nil, contracts.InvalidRule(contracts.RuleUnknownShape)
		}
		if slices.Contains(d.Shapes[:i], k) {
			return nil, contracts.InvalidRule(contracts.RuleDuplicate)
		}
	}
	for _, k := range contracts.ShapeKinds {
		if slices.Contains(d.Shapes, k) {
			out.Shapes = append(out.Shapes, k)
		}
	}
	if len(d.Topics) > contracts.MaxRuleTopics {
		return nil, contracts.InvalidRule(contracts.RuleInvalidTopic)
	}
	for _, t := range d.Topics {
		c, err := query.Canonicalise(contracts.Formula{V: contracts.FormulaVersion, All: []contracts.Group{{Any: []contracts.Condition{
			{Var: contracts.VarMessage, Op: contracts.OpMentions, Value: t},
		}}}})
		if err != nil || c.Never || len(c.Formula.All) != 1 || len(c.Formula.All[0].Any) != 1 {
			return nil, contracts.InvalidRule(contracts.RuleInvalidTopic)
		}
		topic := c.Formula.All[0].Any[0].Value
		if slices.Contains(out.Topics, topic) {
			return nil, contracts.InvalidRule(contracts.RuleDuplicate)
		}
		out.Topics = append(out.Topics, topic)
	}
	slices.Sort(out.Topics)
	return out, nil
}

// checkScope is a scope made canonical, its clients sorted: none for
// ScopeAll; 1 to MaxRuleScopeClients clients otherwise, each once and each a
// client_id a connection could have (oauth.ClientID).
func checkScope(d contracts.RuleScope) (contracts.RuleScope, error) {
	switch d.To {
	case contracts.ScopeAll:
		if len(d.Clients) > 0 {
			return contracts.RuleScope{}, contracts.InvalidRule(contracts.RuleScopeClients)
		}
		return contracts.RuleScope{To: d.To}, nil
	case contracts.ScopeOnly, contracts.ScopeAllExcept:
	default:
		return contracts.RuleScope{}, contracts.InvalidRule(contracts.RuleUnknownScope)
	}
	if len(d.Clients) == 0 || len(d.Clients) > contracts.MaxRuleScopeClients {
		return contracts.RuleScope{}, contracts.InvalidRule(contracts.RuleScopeClients)
	}
	for i, c := range d.Clients {
		if _, err := oauth.ClientID(c, ""); err != nil || slices.Contains(d.Clients[:i], c) {
			return contracts.RuleScope{}, contracts.InvalidRule(contracts.RuleScopeClients)
		}
	}
	return contracts.RuleScope{To: d.To, Clients: slices.Sorted(slices.Values(d.Clients))}, nil
}

// sentence is the system's sentence of a rule's criteria.
func sentence(fs []*query.Canonical, mailboxes []string) contracts.RuleSentence {
	names := map[string]query.MailboxName{}
	for _, m := range mailboxes {
		names[m] = query.MailboxName{Address: m}
	}
	r := query.RenderRule(fs, query.Context{Lang: "en", Mailboxes: names})
	return contracts.RuleSentence{Lang: r.Lang, Text: r.Text, Lead: r.Lead, Formulas: r.Formulas}
}

// The effect in the system's fixed words, each kind of data with what it
// takes out that may not be meant (ADR 0040, "Fixed shapes", choice 8).
var (
	partWords = map[contracts.Part]string{
		contracts.PartSender: "the sender", contracts.PartSubject: "the subject",
		contracts.PartBody: "the text", contracts.PartAttachments: "the attachments",
	}
	shapeWords = map[contracts.ShapeKind]string{
		contracts.ShapeEmailAddress:  "email addresses",
		contracts.ShapePhoneNumber:   "phone numbers (every number of 7 digits or more, with whatever numbers are written next to it)",
		contracts.ShapeCardNumber:    "card numbers (every number of 12 digits or more, with whatever numbers are written next to it)",
		contracts.ShapeAccountNumber: "account numbers (every number of 8 digits or more, with whatever numbers are written next to it, and IBANs)",
		contracts.ShapeNumericCode:   "numeric codes (every number of 4 digits or more, years, amounts and numbers written next to it included)",
		contracts.ShapeLink:          "links (and every word joined to another by a dot under a real domain ending)",
	}
)

// does is what a rule does, in the system's words.
func does(effect contracts.RuleEffect, rm *contracts.RuleRemoval) string {
	if effect == contracts.EffectHold || rm == nil {
		return "Holds the email back"
	}
	var what []string
	for _, p := range rm.Parts {
		what = append(what, partWords[p])
	}
	for _, k := range rm.Shapes {
		what = append(what, shapeWords[k])
	}
	for _, t := range rm.Topics {
		what = append(what, "every sentence that mentions "+quoted(t)+" (a model decides)")
	}
	out := "Takes out " + strings.Join(what, ", ")
	if len(rm.Shapes) > 0 || len(rm.Topics) > 0 {
		out += ", and the sender and attachments, which it does not read"
	}
	return out
}

// quoted shows a person's value between quotation marks, as a sentence
// of the query language shows a text value.
func quoted(s string) string {
	return "“" + s + "”"
}

// forWhom is a scope in the system's words, each client by
// contracts.ClientShown, never its domain alone.
func forWhom(scope contracts.RuleScope) (string, []contracts.RuleClient) {
	var clients []contracts.RuleClient
	var shown []string
	for _, id := range scope.Clients {
		clients = append(clients, contracts.RuleClient{ClientID: id, Shown: contracts.ClientShown(id)})
		shown = append(shown, contracts.ClientShown(id))
	}
	switch scope.To {
	case contracts.ScopeAllExcept:
		return "Every connection but those of " + strings.Join(shown, ", "), clients
	case contracts.ScopeOnly:
		return "Only the connections of " + strings.Join(shown, ", "), clients
	}
	return "Every connection, those made later included", nil
}
