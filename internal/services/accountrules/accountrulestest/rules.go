package accountrulestest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/rules"
	"github.com/permaily-org/permaily/internal/services/accountrules"
)

// Change is one `rules` receipt the fake wrote, before its change: what was
// asked, of which rule, at which version of the record. Made says the write
// after it landed; a receipt with Made false is a change that lost at its
// version (ADR 0040, U7's two interleavings).
type Change struct {
	Account string
	Kind    string // made, changed, deleted, default_on, default_off, notice_agreed
	Rule    string
	At      uint64
	Made    bool
}

var _ accountrules.Rules = (*Fake)(nil)

func (f *Fake) now() time.Time {
	if f.Now != nil {
		return f.Now()
	}
	return time.Now()
}

func (f *Fake) init() {
	if f.records == nil {
		f.records, f.version = map[string]*rules.Record{}, map[string]uint64{}
	}
}

// List implements accountrules.Rules.
func (f *Fake) List(ctx context.Context, account string) (*contracts.RuleList, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := f.readable(ctx, account); err != nil {
		return nil, err
	}
	rec := f.records[account]
	d := rules.Default()
	out := &contracts.RuleList{
		Default: contracts.RuleDefault{Rule: contracts.RuleSummary{ID: d.ID, Version: d.Version, Sentence: []contracts.Sentence{}}, On: true},
		Rules:   []contracts.Rule{}, Version: f.version[account], Max: contracts.MaxRules,
	}
	if rec == nil {
		return out, nil
	}
	out.Default.On, out.Default.ChangedAt, out.Notice = !rec.DefaultOff, rec.DefaultChangedAt, rec.Notice
	for _, s := range rec.Rules {
		c, err := check(s.Draft)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", accountrules.ErrUnavailable, err)
		}
		r := contracts.Rule{ID: s.ID, Version: s.Version, Check: *c, Notice: s.Notice, CreatedAt: s.CreatedAt, ChangedAt: s.ChangedAt}
		r.ModelOff = c.Model && (rec.Notice == nil || rec.Notice.Version != f.Notice || s.Notice != f.Notice)
		out.Rules = append(out.Rules, r)
	}
	return out, nil
}

func (f *Fake) readable(ctx context.Context, account string) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%w: %w", accountrules.ErrUnavailable, err)
	}
	if err := f.fail[account]; err != nil {
		return fmt.Errorf("%w: %w", accountrules.ErrUnavailable, err)
	}
	return nil
}

// Check implements accountrules.Rules: it refuses what rules.Compile
// refuses, a name past its bound and a scope's clients that do not fit its
// kind, each with one reason; the service names every reason (U6). Its
// sentence is the draft's name; Does, For and Clients are the service's
// shapes in simpler words.
func (f *Fake) Check(ctx context.Context, account string, d contracts.RuleDraft) (*contracts.RuleCheck, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return check(d)
}

func check(d contracts.RuleDraft) (*contracts.RuleCheck, error) {
	if utf8.RuneCountInString(d.Name) > contracts.MaxRuleNameRunes {
		return nil, contracts.InvalidRule(contracts.RuleNameTooLong)
	}
	switch d.Scope.To {
	case contracts.ScopeAll:
		if len(d.Scope.Clients) > 0 {
			return nil, contracts.InvalidRule(contracts.RuleScopeClients)
		}
	case contracts.ScopeOnly, contracts.ScopeAllExcept:
		if len(d.Scope.Clients) == 0 || len(d.Scope.Clients) > contracts.MaxRuleScopeClients {
			return nil, contracts.InvalidRule(contracts.RuleScopeClients)
		}
	default:
		return nil, contracts.InvalidRule(contracts.RuleUnknownScope)
	}
	d.Scope.Clients = slices.Sorted(slices.Values(d.Scope.Clients))
	r, err := rules.Compile(rules.Stored{ID: "rul_check", Draft: d})
	if err != nil {
		return nil, contracts.InvalidRule(contracts.RuleNoCriteria)
	}
	b, err := json.Marshal(d)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(b)
	c := &contracts.RuleCheck{
		DraftID: "rdr_" + hex.EncodeToString(sum[:13]), Draft: d,
		Sentence: contracts.RuleSentence{Lang: "en", Text: d.Name, Lead: []contracts.SentencePart{}, Formulas: []contracts.Sentence{}},
		Does:     "Holds the email back", Model: r.Model(),
	}
	if r.Removal != nil {
		c.Does = "Takes out details"
	}
	var shown []string
	for _, id := range d.Scope.Clients {
		c.Clients = append(c.Clients, contracts.RuleClient{ClientID: id, Shown: contracts.ClientShown(id)})
		shown = append(shown, contracts.ClientShown(id))
	}
	switch d.Scope.To {
	case contracts.ScopeAll:
		c.For = "Every connection"
	case contracts.ScopeAllExcept:
		c.For = "Every connection but " + strings.Join(shown, ", ")
	default:
		c.For = "Only " + strings.Join(shown, ", ")
	}
	return c, nil
}

// change is the start of every change: the proof, the version the screen
// read, and the record as it is.
func (f *Fake) change(ctx context.Context, p accountrules.Person, version uint64, checkVersion bool) (*rules.Record, error) {
	if !f.now().Before(p.ConfirmedUntil) {
		return nil, contracts.ErrConfirmationNeeded
	}
	if err := f.readable(ctx, p.Account); err != nil {
		return nil, err
	}
	f.init()
	if checkVersion && version != f.version[p.Account] {
		return nil, contracts.ErrRulesChanged
	}
	rec := &rules.Record{V: rules.RecordFormat, Account: p.Account, Rules: []rules.Stored{}}
	if old := f.records[p.Account]; old != nil {
		cp := *old
		cp.Rules = slices.Clone(old.Rules)
		rec = &cp
	}
	return rec, nil
}

// commit writes a change's receipt, then the record at the version read:
// FailNextReceipt makes nothing; LoseNext leaves the receipt of a change not
// made and answers ErrRulesChanged, as a write that lost would.
func (f *Fake) commit(account, kind, rule string, rec *rules.Record) error {
	if f.FailNextReceipt {
		f.FailNextReceipt = false
		return errors.New("accountrulestest: the receipt could not be written")
	}
	f.Changes = append(f.Changes, Change{Account: account, Kind: kind, Rule: rule, At: f.version[account]})
	if f.LoseNext {
		f.LoseNext = false
		return contracts.ErrRulesChanged
	}
	f.Changes[len(f.Changes)-1].Made = true
	f.records[account] = rec
	f.version[account]++
	return nil
}

// Save implements accountrules.Rules.
func (f *Fake) Save(ctx context.Context, p accountrules.Person, id string, in contracts.RuleSave) (*contracts.Rule, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	rec, err := f.change(ctx, p, in.Version, true)
	if err != nil {
		return nil, err
	}
	c, err := check(in.Draft)
	if err != nil {
		return nil, err
	}
	if c.DraftID != in.DraftID {
		return nil, contracts.ErrRuleChanged
	}
	if c.Model && (rec.Notice == nil || rec.Notice.Version != f.Notice || in.Notice != f.Notice) {
		return nil, contracts.ErrRulesNoticeNeeded
	}
	now := f.now()
	s := rules.Stored{Version: 1, Draft: c.Draft, DraftID: c.DraftID, CreatedAt: now, ChangedAt: now}
	if c.Model {
		s.Notice = f.Notice
	}
	kind := "made"
	if id == "" {
		if len(rec.Rules) >= contracts.MaxRules {
			return nil, contracts.ErrTooManyRules
		}
		f.ids++
		s.ID = "rul_" + fmt.Sprintf("%026s", strconv.FormatUint(f.ids, 32))
		rec.Rules = append(rec.Rules, s)
	} else {
		i := slices.IndexFunc(rec.Rules, func(r rules.Stored) bool { return r.ID == id })
		if i < 0 {
			return nil, contracts.ErrNotFound
		}
		old := rec.Rules[i]
		s.ID, s.CreatedAt, s.Version = id, old.CreatedAt, old.Version
		if !sameEffect(old.Draft, s.Draft) {
			s.Version++
		}
		rec.Rules[i], kind = s, "changed"
	}
	if err := f.commit(p.Account, kind, s.ID, rec); err != nil {
		return nil, err
	}
	return &contracts.Rule{ID: s.ID, Version: s.Version, Check: *c, Notice: s.Notice, CreatedAt: s.CreatedAt, ChangedAt: s.ChangedAt}, nil
}

// sameEffect says two drafts decide alike: the same criteria, effect and
// removal (their name, scope and switch aside).
func sameEffect(a, b contracts.RuleDraft) bool {
	ja, _ := json.Marshal([]any{a.Criteria, a.Effect, a.Removal})
	jb, _ := json.Marshal([]any{b.Criteria, b.Effect, b.Removal})
	return string(ja) == string(jb)
}

// Delete implements accountrules.Rules.
func (f *Fake) Delete(ctx context.Context, p accountrules.Person, id string, in contracts.RuleDelete) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	rec, err := f.change(ctx, p, in.Version, true)
	if err != nil {
		return err
	}
	i := slices.IndexFunc(rec.Rules, func(r rules.Stored) bool { return r.ID == id })
	if i < 0 {
		return contracts.ErrNotFound
	}
	rec.Rules = slices.Delete(rec.Rules, i, i+1)
	return f.commit(p.Account, "deleted", id, rec)
}

// SetDefault implements accountrules.Rules.
func (f *Fake) SetDefault(ctx context.Context, p accountrules.Person, in contracts.RuleDefaultSet) (*contracts.RuleDefault, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	rec, err := f.change(ctx, p, in.Version, true)
	if err != nil {
		return nil, err
	}
	rec.DefaultOff, rec.DefaultChangedAt = !in.On, f.now()
	kind := "default_on"
	if !in.On {
		kind = "default_off"
	}
	if err := f.commit(p.Account, kind, rules.DefaultID, rec); err != nil {
		return nil, err
	}
	d := rules.Default()
	return &contracts.RuleDefault{Rule: contracts.RuleSummary{ID: d.ID, Version: d.Version, Sentence: []contracts.Sentence{}}, On: in.On, ChangedAt: rec.DefaultChangedAt}, nil
}

// AgreeNotice implements accountrules.Rules.
func (f *Fake) AgreeNotice(ctx context.Context, p accountrules.Person, version string) (*contracts.RuleNoticeAgreement, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if version != f.Notice {
		return nil, contracts.ErrRulesNoticeNeeded
	}
	rec, err := f.change(ctx, p, 0, false)
	if err != nil {
		return nil, err
	}
	rec.Notice = &contracts.RuleNoticeAgreement{Version: version, At: f.now()}
	if err := f.commit(p.Account, "notice_agreed", "", rec); err != nil {
		return nil, err
	}
	return rec.Notice, nil
}

// Remove implements accountrules.Rules.
func (f *Fake) Remove(ctx context.Context, account string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	f.init()
	delete(f.records, account)
	delete(f.fail, account)
	f.version[account] = 0
	return nil
}
