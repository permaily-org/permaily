package httpapi

import (
	"cmp"
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/query"
	"github.com/permaily-org/permaily/internal/redact"
	"github.com/permaily-org/permaily/internal/rules"
	"github.com/permaily-org/permaily/internal/services/accountrules"
	"github.com/permaily-org/permaily/internal/services/mailboxes"
)

// The rule editor's preview (ADR 0040, "Previews and consent"): a page of a
// mailbox's mail a draft's criteria apply to, as the owner's previews are,
// deciding nothing and beginning no decision.

// RulePreviews are a mailbox's previews of a draft, made by
// MailboxesWithRulePreviews from the mailboxes routes' own state, so a
// preview is admitted as a page of matches is: the same mailbox service and
// matcher, the same read budget, the same search slots and the account's
// same search pages a minute.
type RulePreviews struct{ m *mailboxesAPI }

// MailboxesWithRulePreviews returns the mailboxes routes (MailboxesAPI) and
// the rules routes' previews sharing their admission (RulesConfig.Previews);
// nil previews without a Matcher, as the matches route is not served then.
func MailboxesWithRulePreviews(cfg MailboxesConfig) (http.Handler, *RulePreviews) {
	a, h := newMailboxesAPI(cfg)
	if cfg.Matcher == nil {
		return h, nil
	}
	return h, &RulePreviews{m: a}
}

// preview answers POST /api/v1/rules/preview: a page of the mailbox's mail
// the draft's criteria apply to, newest first. In order: 401 (the session,
// an account blocked or gone); the body, read strictly (invalid_body); the
// draft, checked by the rules service (invalid_rule with its reason, or a
// formula's problem); the mailbox, opened as a search opens it (not_found
// for one that is not the account's, terms_needed while the terms in force
// are not agreed, mailbox_unreadable); with ask_model the rules' notice in
// force agreed (rules_notice_needed), and the model asked through the
// mailboxes routes' Asker, under the account's share, its kept answers
// shared, as the owner's previews ask it; the limit (invalid_limit, 1 to
// 100, 50 when absent or 0) and the cursor (invalid_cursor); then the
// account's search pages a minute and a search slot (429).
//
// A rule applies to a message any of its criteria is true or unknown for
// (query.ForRule), and the matcher matches formulas together, so each
// criteria is matched on its own, at once, and the pages joined
// (joinPages). The criteria share one page's bounds (inbox.WithMatchShare):
// each reads, reads raw and asks at most its share of a page's, so a
// preview of eight criteria does no more work than a page of matches under
// the one search slot and search page it was admitted with: Messages are those any criteria matches, NeedsModel and
// Unknown those no criteria matches and one still might (a rule applies on
// unknown, so the screen shows them as mail it may hold); the page ends
// where every criteria considered the mail above it, so pages skip
// nothing. The matcher classifies the model's answers as a query does
// (0.80 and 0.20), so an answer a rule would read as false (below 0.30)
// and a query as doubt (above 0.20) is listed Unknown: the preview may
// list more than the rule applies to, never less.
//
// With message set, the answer is one message of the mailbox as the
// draft's removal would leave it (contracts.RuleRemovalPreview, removal):
// the draft must remove (invalid_rule, removal_missing, for one that
// holds); the mailbox opened as a read of one message opens it.
func (a *rulesAPI) preview(w http.ResponseWriter, r *http.Request, p accountrules.Person) {
	var in contracts.RulePreview
	if !a.standing(w, r, p, false) || !decodeRule(w, r, &in) {
		return
	}
	m := a.cfg.Previews.m
	check, err := a.cfg.Rules.Check(r.Context(), p.Account, in.Draft)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	if in.Message != "" {
		a.removal(w, r, p, in, check)
		return
	}
	c := mailboxes.Caller{Account: p.Account, ConfirmedUntil: p.ConfirmedUntil}
	b, err := m.cfg.Service.Open(r.Context(), c, in.Mailbox, mailboxes.AccessSearch)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	held := &heldBudget{budget: m.budget, wait: m.cfg.Wait}
	defer held.release()
	ctx := inbox.WithRenderAdmission(inbox.WithReadBudget(r.Context(), held), summaryAdmission{m, c.Account})
	if in.AskModel {
		if !a.noticeAgreed(w, r, p, a.cfg.Notice) {
			return
		}
		ctx = inbox.WithAsker(ctx, inbox.Asking{Asker: m.asker(), Account: c.Account})
	}
	mine, err := m.cfg.Service.Catalogue(ctx, c, false)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	criteria := make([]*query.Canonical, 0, len(check.Draft.Criteria))
	for _, f := range check.Draft.Criteria {
		raw, err := json.Marshal(f)
		if err != nil {
			a.fail(w, r, err)
			return
		}
		cf, err := query.Parse(raw, mailboxIDs(mine))
		if err != nil {
			a.fail(w, r, err)
			return
		}
		criteria = append(criteria, cf)
	}
	limit := in.Limit
	switch {
	case limit < 0 || limit > MaxMessagesLimit:
		a.fail(w, r, contracts.ErrInvalidLimit)
		return
	case limit == 0:
		limit = DefaultMessagesLimit
	}
	if in.After != "" && !inbox.IsMessageID(in.After) {
		a.fail(w, r, contracts.ErrInvalidCursor)
		return
	}
	done, ok := m.searchSlot(ctx, w, r, c.Account)
	if !ok {
		return
	}
	defer done()
	pages := make([]*contracts.MatchPage, len(criteria))
	errs := make([]error, len(criteria))
	shared := inbox.WithMatchShare(ctx, len(criteria))
	var wg sync.WaitGroup
	for i, f := range criteria {
		wg.Go(func() {
			pages[i], errs[i] = m.cfg.Matcher.Match(shared, b, []*query.Canonical{f}, in.After, limit)
		})
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			a.fail(w, r, err)
			return
		}
	}
	writeJSON(w, contracts.RulePreviewPage{DraftID: check.DraftID, Page: *joinPages(pages, limit)})
}

// joined is how a message is listed on a joined page, in the order a
// message any page lists more surely wins: matched, then needing the model,
// then unknown.
type joined int

const (
	joinedUnknown joined = iota
	joinedNeedsModel
	joinedMatched
)

// joinPages joins the pages of a draft's criteria, each matched on its own
// from the same cursor with the same limit, into the page of the rule: a
// message any criteria matches is a match; one no criteria matches is
// needing the model when one says so, else unknown. Each page considered
// the mail from the cursor down to its Next (all of it when Next is
// empty), so the joined page keeps only what every page considered, down to
// the newest of their Nexts, and ends once it holds limit matches or
// inbox.MatchMaxUndecided undecided messages, its Next the last message it
// keeps: the next page goes on below it, and nothing is skipped. Held and
// Removed are never on a page of the matcher, and stay absent. Model says
// the least that became of asking the model on any page: off, before the
// spend's limit, before asked.
func joinPages(pages []*contracts.MatchPage, limit int) *contracts.MatchPage {
	cut := ""
	for _, p := range pages {
		if p.Next != "" && p.Next > cut {
			cut = p.Next
		}
	}
	how := map[string]joined{}
	summaries := map[string]contracts.MessageSummary{}
	note := func(id string, j joined) {
		if cut != "" && id < cut {
			return // below what every criteria considered: the next page's
		}
		if old, ok := how[id]; !ok || j > old {
			how[id] = j
		}
	}
	model := contracts.MatchModel("")
	for _, p := range pages {
		for _, s := range p.Messages {
			summaries[s.ID] = s
			note(s.ID, joinedMatched)
		}
		for _, id := range p.NeedsModel {
			note(id, joinedNeedsModel)
		}
		for _, id := range p.Unknown {
			note(id, joinedUnknown)
		}
		model = leastAsked(model, p.Model)
	}
	ids := make([]string, 0, len(how))
	for id := range how {
		ids = append(ids, id)
	}
	slices.SortFunc(ids, func(x, y string) int { return cmp.Compare(y, x) }) // newest first
	out := &contracts.MatchPage{Messages: []contracts.MessageSummary{}, NeedsModel: []string{}, Unknown: []string{}, Model: model, Next: cut}
	undecided := 0
	for i, id := range ids {
		switch how[id] {
		case joinedMatched:
			out.Messages = append(out.Messages, summaries[id])
		case joinedNeedsModel:
			out.NeedsModel = append(out.NeedsModel, id)
			undecided++
		default:
			out.Unknown = append(out.Unknown, id)
			undecided++
		}
		if (len(out.Messages) >= limit || undecided >= inbox.MatchMaxUndecided) && (i < len(ids)-1 || cut != "") {
			out.Next = id
			break
		}
	}
	if out.Next != "" {
		out.SearchedTo = arrivedAt(out.Next)
	}
	return out
}

// leastAsked is the least of what became of asking the model on two pages:
// off, before limit, before asked; absent only when neither says.
func leastAsked(a, b contracts.MatchModel) contracts.MatchModel {
	rank := func(m contracts.MatchModel) int {
		switch m {
		case contracts.MatchModelOff:
			return 3
		case contracts.MatchModelLimit:
			return 2
		case contracts.MatchModelAsked:
			return 1
		}
		return 0
	}
	if rank(b) > rank(a) {
		return b
	}
	return a
}

// arrivedAt is when a message arrived, from its identifier (inbox's own
// reading of it, MatchPage.SearchedTo): its first sixteen hexadecimal
// characters are nanoseconds since 1970.
func arrivedAt(id string) time.Time {
	if len(id) < 16 {
		return time.Time{}
	}
	n, err := strconv.ParseUint(id[:16], 16, 63)
	if err != nil {
		return time.Time{}
	}
	return time.Unix(0, int64(n)).UTC() //nolint:gosec // 63 bits fit
}

// previewRuleID names the draft compiled for a removal's preview: no rule
// of the account, and nothing is kept under it.
const previewRuleID = "rul_preview"

// removal answers one message as the checked draft's removal would leave it
// (inbox.PreviewRemoval, on the message's one view, as a connection's read
// removes it): opened as the person's read of one message is
// (mailboxes.AccessReadMessage), its text read under the mailboxes routes'
// read budget; with ask_model the rules' notice in force agreed and the
// sentences asked through the mailboxes routes' Asker in the account's
// share, without it a removal by meaning removes the whole subject and text
// (undecided). It begins and settles no decision, and keeps nothing.
func (a *rulesAPI) removal(w http.ResponseWriter, r *http.Request, p accountrules.Person, in contracts.RulePreview, check *contracts.RuleCheck) {
	m := a.cfg.Previews.m
	rule, err := rules.Compile(rules.Stored{ID: previewRuleID, Version: 1, Draft: check.Draft})
	if err != nil || rule.Effect != rules.Remove || rule.Removal == nil {
		a.fail(w, r, contracts.InvalidRule(contracts.RuleRemovalMissing))
		return
	}
	c := mailboxes.Caller{Account: p.Account, ConfirmedUntil: p.ConfirmedUntil}
	b, err := m.cfg.Service.Open(r.Context(), c, in.Mailbox, mailboxes.AccessReadMessage)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	held := &heldBudget{budget: m.budget, wait: m.cfg.Wait}
	defer held.release()
	ctx := inbox.WithRenderAdmission(inbox.WithReadBudget(r.Context(), held), summaryAdmission{m, c.Account})
	var asker inbox.SentenceAsker
	if in.AskModel && len(rule.Removal.Topics) > 0 {
		if !a.noticeAgreed(w, r, p, a.cfg.Notice) {
			return
		}
		asker, _ = m.asker().(inbox.SentenceAsker)
	}
	msg, err := m.cfg.Reader.Get(ctx, b, in.Message)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	shown, err := inbox.PreviewRemoval(ctx, redact.Fixed{}, msg.Subject, msg.Text, len(msg.Attachments), rule.Removal, asker, c.Account)
	if err != nil {
		a.fail(w, r, err)
		return
	}
	writeJSON(w, contracts.RuleRemovalPreview{DraftID: check.DraftID, Subject: shown.Subject, Text: shown.Text, Removed: shown.Removed})
}
