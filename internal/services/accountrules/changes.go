package accountrules

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/query"
	"github.com/permaily-org/permaily/internal/rules"
	"github.com/permaily-org/permaily/internal/services/receipts"
	"github.com/permaily-org/permaily/internal/store"
)

// The routes' changes (Rules): each with a fresh proof, at the version the
// screen read, recorded first in a `rules` receipt and then written at that
// version (ADR 0040, "Receipts").

// noticeTries is how many times agreeing to the rules' notice is made again
// on the record read again after a write that lost.
const noticeTries = 3

// List implements Rules: the record read strictly and now.
func (s *Service) List(ctx context.Context, account string) (*contracts.RuleList, error) {
	began := s.now()
	rec, at, err := s.read(ctx, account)
	if err != nil {
		return nil, s.unavailable(ctx, err)
	}
	c := cached{rec: rec, began: began}
	if rec != nil {
		c.epoch, c.version = rec.Epoch, at.Version()
	}
	s.keep(account, c)
	mailboxes, err := s.cfg.Mailboxes.Catalogue(ctx, account)
	if err != nil {
		return nil, err
	}
	d := rules.Default()
	out := &contracts.RuleList{
		Default: contracts.RuleDefault{
			Rule: contracts.RuleSummary{ID: d.ID, Version: d.Version, Sentence: query.RenderRule(d.Formulas, query.Context{Lang: "en"}).Formulas},
			On:   true,
		},
		Rules: []contracts.Rule{}, Version: listVersion(rec, at), Max: contracts.MaxRules,
	}
	if rec == nil {
		return out, nil
	}
	out.Default.On, out.Default.ChangedAt, out.Notice = !rec.DefaultOff, rec.DefaultChangedAt, rec.Notice
	agreed := rec.Notice != nil && rec.Notice.Version == s.cfg.Notice
	for _, st := range rec.Rules {
		// Read back with any mailbox its criteria name: one the account
		// disconnected since is named by its identifier.
		c, err := s.check(st.Draft, nil, mailboxes)
		if err != nil {
			return nil, s.unavailable(ctx, errors.Join(errCompile, err))
		}
		c.check.DraftID = st.DraftID
		out.Rules = append(out.Rules, contracts.Rule{
			ID: st.ID, Version: st.Version, Check: *c.check, Notice: st.Notice, CreatedAt: st.CreatedAt, ChangedAt: st.ChangedAt,
			ModelOff: c.check.Model && (!agreed || st.Notice != s.cfg.Notice),
		})
	}
	return out, nil
}

// change is every change once its proof is fresh: the record read strictly and now, the
// version the screen read when named, then apply on a copy of the record
// (which says what its receipt records, or why the change is refused), its
// receipt written, and the record written at the version read. A write that
// lost is contracts.ErrRulesChanged, never made again on another state.
func (s *Service) change(ctx context.Context, p Person, version uint64, named bool, apply func(rec *rules.Record, now time.Time) (*receipts.RulesChange, error)) error {
	began := s.now()
	rec, at, err := s.read(ctx, p.Account)
	if err != nil {
		return s.unavailable(ctx, err)
	}
	if named && version != listVersion(rec, at) {
		return contracts.ErrRulesChanged
	}
	create := rec == nil
	next := &rules.Record{V: rules.RecordFormat, Account: p.Account, Epoch: s.epoch(p.Account), Rules: []rules.Stored{}}
	if !create {
		cp := *rec
		cp.Rules = slices.Clone(rec.Rules)
		next = &cp
	}
	rc, err := apply(next, began)
	if err != nil {
		return err
	}
	if raw, err := json.Marshal(next); err != nil || len(raw) > rules.RecordBytesMax {
		return contracts.ErrTooManyRules
	}
	rc.Epoch, rc.Version = next.Epoch, listVersion(rec, at)
	if rc.Changes == nil {
		rc.Changes = []receipts.RulesChangeKind{}
	}
	if err := s.cfg.Receipts.Write(ctx, p.Account, &receipts.Receipt{V: receipts.Version, Kind: receipts.KindRules, RulesChange: rc}); err != nil {
		return s.failed(ctx, err)
	}
	cur, v, err := s.write(ctx, next, at, create)
	if errors.Is(err, store.ErrChanged) || errors.Is(err, store.ErrExists) {
		return contracts.ErrRulesChanged
	}
	if err != nil {
		return s.failed(ctx, err)
	}
	s.keep(p.Account, cached{rec: cur, epoch: cur.Epoch, version: v, began: began})
	return nil
}

// fresh refuses a change without a fresh proof before anything else is read.
func (s *Service) fresh(p Person) error {
	if !s.now().Before(p.ConfirmedUntil) {
		return contracts.ErrConfirmationNeeded
	}
	return nil
}

// Save implements Rules.
func (s *Service) Save(ctx context.Context, p Person, id string, in contracts.RuleSave) (*contracts.Rule, error) {
	if err := s.fresh(p); err != nil {
		return nil, err
	}
	if id == rules.DefaultID {
		return nil, contracts.InvalidRule(contracts.RuleDefaultNotEditable)
	}
	mailboxes, err := s.cfg.Mailboxes.Catalogue(ctx, p.Account)
	if err != nil {
		return nil, err
	}
	c, err := s.check(in.Draft, mailboxes, mailboxes)
	if err != nil {
		return nil, err
	}
	if c.check.DraftID != in.DraftID {
		return nil, contracts.ErrRuleChanged
	}
	var out *contracts.Rule
	err = s.change(ctx, p, in.Version, true, func(rec *rules.Record, now time.Time) (*receipts.RulesChange, error) {
		agreed := rec.Notice != nil && rec.Notice.Version == s.cfg.Notice
		if c.check.Model && (!agreed || in.Notice != s.cfg.Notice) {
			return nil, contracts.ErrRulesNoticeNeeded
		}
		st := rules.Stored{Version: 1, Draft: c.check.Draft, DraftID: c.check.DraftID, CreatedAt: now, ChangedAt: now}
		if c.check.Model {
			st.Notice = s.cfg.Notice
		}
		rc := &receipts.RulesChange{DraftID: st.DraftID, Criteria: c.criteria, Notice: st.Notice}
		if id == "" {
			if len(rec.Rules) >= contracts.MaxRules {
				return nil, contracts.ErrTooManyRules
			}
			nid, err := s.newID()
			if err != nil {
				return nil, err
			}
			st.ID = nid
			rec.Rules = append(rec.Rules, st)
			rc.Changes, rc.Rule, rc.After = []receipts.RulesChangeKind{receipts.RulesMade}, st.ID, st.Version
		} else {
			i := slices.IndexFunc(rec.Rules, func(r rules.Stored) bool { return r.ID == id })
			if i < 0 {
				return nil, contracts.ErrNotFound
			}
			old := rec.Rules[i]
			st.ID, st.CreatedAt, st.Version = id, old.CreatedAt, old.Version
			rc.Changes = changed(old.Draft, st.Draft)
			if slices.ContainsFunc(rc.Changes, func(k receipts.RulesChangeKind) bool {
				return k == receipts.RulesCriteria || k == receipts.RulesEffect
			}) {
				st.Version++
			}
			rc.Rule, rc.Before, rc.After = id, old.Version, st.Version
			rec.Rules[i] = st
		}
		if err := s.accountBounds(rec); err != nil {
			return nil, err
		}
		out = &contracts.Rule{ID: st.ID, Version: st.Version, Check: *c.check, Notice: st.Notice, CreatedAt: st.CreatedAt, ChangedAt: st.ChangedAt}
		return rc, nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// accountBounds are the bounds over an account's rules (ADR 0040, "The
// rules' record"): at most MaxMeaningRules rules removing by meaning, and
// at most MaxAccountModelConditions conditions a model decides over the
// rules that are on.
func (s *Service) accountBounds(rec *rules.Record) error {
	meaning, model := 0, 0
	for _, st := range rec.Rules {
		c, err := s.check(st.Draft, nil, nil)
		if err != nil {
			return err
		}
		if c.meaning {
			meaning++
		}
		if st.Draft.On {
			model += c.model
		}
	}
	if meaning > contracts.MaxMeaningRules || model > contracts.MaxAccountModelConditions {
		return contracts.ErrTooManyRules
	}
	return nil
}

// changed says what of a rule's draft a change changes, in the order of
// receipts.RulesChangeKind: never what it was or became.
func changed(a, b contracts.RuleDraft) []receipts.RulesChangeKind {
	same := func(x, y any) bool {
		jx, _ := json.Marshal(x)
		jy, _ := json.Marshal(y)
		return string(jx) == string(jy)
	}
	out := []receipts.RulesChangeKind{}
	if a.Name != b.Name {
		out = append(out, receipts.RulesName)
	}
	if !same(a.Criteria, b.Criteria) {
		out = append(out, receipts.RulesCriteria)
	}
	if a.Effect != b.Effect || !same(a.Removal, b.Removal) {
		out = append(out, receipts.RulesEffect)
	}
	if !same(a.Scope, b.Scope) {
		out = append(out, receipts.RulesScope)
	}
	switch {
	case !a.On && b.On:
		out = append(out, receipts.RulesOn)
	case a.On && !b.On:
		out = append(out, receipts.RulesOff)
	}
	return out
}

// Delete implements Rules.
func (s *Service) Delete(ctx context.Context, p Person, id string, in contracts.RuleDelete) error {
	if err := s.fresh(p); err != nil {
		return err
	}
	if id == rules.DefaultID {
		return contracts.InvalidRule(contracts.RuleDefaultNotEditable)
	}
	return s.change(ctx, p, in.Version, true, func(rec *rules.Record, _ time.Time) (*receipts.RulesChange, error) {
		i := slices.IndexFunc(rec.Rules, func(r rules.Stored) bool { return r.ID == id })
		if i < 0 {
			return nil, contracts.ErrNotFound
		}
		old := rec.Rules[i]
		rec.Rules = slices.Delete(rec.Rules, i, i+1)
		return &receipts.RulesChange{Changes: []receipts.RulesChangeKind{receipts.RulesDeleted}, Rule: id, Before: old.Version, DraftID: old.DraftID}, nil
	})
}

// SetDefault implements Rules.
func (s *Service) SetDefault(ctx context.Context, p Person, in contracts.RuleDefaultSet) (*contracts.RuleDefault, error) {
	if err := s.fresh(p); err != nil {
		return nil, err
	}
	var at time.Time
	err := s.change(ctx, p, in.Version, true, func(rec *rules.Record, now time.Time) (*receipts.RulesChange, error) {
		rec.DefaultOff, rec.DefaultChangedAt, at = !in.On, now, now
		kind := receipts.RulesDefaultOn
		if !in.On {
			kind = receipts.RulesDefaultOff
		}
		return &receipts.RulesChange{Changes: []receipts.RulesChangeKind{kind}, Rule: rules.DefaultID, Before: rules.DefaultVersion, After: rules.DefaultVersion}, nil
	})
	if err != nil {
		return nil, err
	}
	d := rules.Default()
	return &contracts.RuleDefault{
		Rule: contracts.RuleSummary{ID: d.ID, Version: d.Version, Sentence: query.RenderRule(d.Formulas, query.Context{Lang: "en"}).Formulas},
		On:   in.On, ChangedAt: at,
	}, nil
}

// AgreeNotice implements Rules. Agreeing to a notice already agreed writes
// nothing and answers the agreement as it is.
func (s *Service) AgreeNotice(ctx context.Context, p Person, version string) (*contracts.RuleNoticeAgreement, error) {
	if err := s.fresh(p); err != nil {
		return nil, err
	}
	if version != s.cfg.Notice {
		return nil, contracts.ErrRulesNoticeNeeded
	}
	var out *contracts.RuleNoticeAgreement
	for range noticeTries {
		err := s.change(ctx, p, 0, false, func(rec *rules.Record, now time.Time) (*receipts.RulesChange, error) {
			if rec.Notice != nil && rec.Notice.Version == version {
				out = rec.Notice
				return nil, errAgreed
			}
			rec.Notice = &contracts.RuleNoticeAgreement{Version: version, At: now}
			out = rec.Notice
			return &receipts.RulesChange{Changes: []receipts.RulesChangeKind{receipts.RulesNoticeAgreed}, Notice: version}, nil
		})
		switch {
		case err == nil, errors.Is(err, errAgreed):
			return out, nil
		case !errors.Is(err, contracts.ErrRulesChanged):
			return nil, err
		}
	}
	return nil, s.unavailable(ctx, errBusy)
}

// errAgreed is a notice already agreed: nothing to write.
var errAgreed = errors.New("accountrules: the notice is agreed already")

// removeEpochsMax bounds the epochs a removal walks: far more than an
// account's changes can make.
const removeEpochsMax = 1 << 16

// Remove implements Rules: every epoch from 0 is removed, whatever it holds,
// up to the first never written, which is removed too so that its removal's
// commit fences the record (no epoch after it is ever read or made).
func (s *Service) Remove(ctx context.Context, account string) error {
	defer s.forget(account)
	for e := range uint64(removeEpochsMax) {
		name := rules.RecordName(account, e)
		_, at, err := s.cfg.Records.GetRecordAt(ctx, name)
		absent := errors.Is(err, store.ErrNotFound) && !errors.Is(err, store.ErrLost)
		switch {
		case absent && at.Version() == 1:
			return nil // fenced already
		case absent && at.Version() > 1:
			continue // removed already
		case err != nil && !absent && !errors.Is(err, store.ErrLost):
			return err
		}
		if err := s.cfg.Records.RemoveRecord(ctx, name); err != nil {
			return err
		}
		if absent {
			return nil // the fence after the last epoch
		}
	}
	return errBusy
}

// forget drops what this enclave keeps of an account.
func (s *Service) forget(account string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.cache, account)
	delete(s.epochs, account)
	delete(s.pruned, account)
}
