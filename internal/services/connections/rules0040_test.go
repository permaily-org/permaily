package connections

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/oauth"
	"github.com/permaily-org/permaily/internal/query"
	"github.com/permaily-org/permaily/internal/rules"
	"github.com/permaily-org/permaily/internal/services/accountrules"
	"github.com/permaily-org/permaily/internal/services/accountrules/accountrulestest"
	"github.com/permaily-org/permaily/internal/services/receipts"
	"github.com/permaily-org/permaily/internal/store"
	"github.com/permaily-org/permaily/internal/vendors"
)

// ADR 0040's pull request 5 in the connections service: the rules through
// accountrules.Source (U25, U26), receipts' removals and their size (U24).

const rulesNotice = "2026-11-20"

// sameHost is a second client on otherApp's host, told apart from it by its
// document's path.
const sameHost = "https://tools.example.org/other/client.json"

// ruleEnclave is an enclave whose rules come from src.
func (w *world) ruleEnclave(src accountrules.Source) *Service {
	w.t.Helper()
	cfg := w.config(&store.Anchored{Records: w.mem, Commits: w.commits, Strict: true, Now: w.clock.now})
	cfg.Source, cfg.Rules = src, nil
	s, err := New(cfg)
	if err != nil {
		w.t.Fatal(err)
	}
	return s
}

// storedRule is a rule of the person's holding mail whose subject contains
// word, for scope.
func storedRule(id, word string, scope contracts.RuleScope) rules.Stored {
	return rules.Stored{ID: id, Version: 1, DraftID: "rdr_" + strings.Repeat("a", 26), Draft: contracts.RuleDraft{
		Name: id, Effect: contracts.EffectHold, On: true, Scope: scope,
		Criteria: []contracts.Formula{{V: contracts.FormulaVersion, All: []contracts.Group{{Any: []contracts.Condition{
			{Var: contracts.VarSubject, Op: contracts.OpContains, Value: word},
		}}}}},
	}}
}

// scopedRules is an account's record: one rule for every client, one only
// for claudeCode, one only for otherApp.
func scopedRules() *rules.Record {
	return &rules.Record{V: rules.RecordFormat, Account: accountA, Notice: &contracts.RuleNoticeAgreement{Version: rulesNotice}, Rules: []rules.Stored{
		storedRule("rul_all", "all", contracts.RuleScope{To: contracts.ScopeAll}),
		storedRule("rul_claude", "claude", contracts.RuleScope{To: contracts.ScopeOnly, Clients: []string{claudeCode}}),
		storedRule("rul_tools", "tools", contracts.RuleScope{To: contracts.ScopeOnly, Clients: []string{otherApp}}),
	}}
}

func ruleIDs(rs []contracts.RuleSummary) []string {
	var out []string
	for _, r := range rs {
		out = append(out, r.ID)
	}
	return out
}

func TestU25TheConsentScreenListsTheRulesForThatClient(t *testing.T) {
	w := newWorld(t)
	w.vendors.clients[sameHost] = &vendors.Client{ID: sameHost, Domain: "tools.example.org", Document: oauth.ClientDocument{
		ClientID: sameHost, ClientName: "Tools", RedirectURIs: []string{"https://tools.example.org/back"}}}
	src := &accountrulestest.Fake{Notice: rulesNotice}
	src.Set(accountA, scopedRules())
	s := w.ruleEnclave(src)
	p := w.person(accountA)
	for _, c := range []struct {
		client string
		want   []string
	}{
		{claudeCode, []string{rules.DefaultID, "rul_all", "rul_claude"}},
		{otherApp, []string{rules.DefaultID, "rul_all", "rul_tools"}},
		{sameHost, []string{rules.DefaultID, "rul_all"}},
	} {
		a := w.ask(c.client)
		if c.client == sameHost {
			a.redirect = "https://tools.example.org/back"
		}
		req := w.start(s, p, a)
		if got := ruleIDs(req.Rules); !slices.Equal(got, c.want) {
			t.Fatalf("%s: rules %v, want %v", c.client, got, c.want)
		}
		for _, r := range req.Rules {
			if len(r.Sentence) == 0 || r.Sentence[0].Text == "" {
				t.Fatalf("%s: %s without its sentence", c.client, r.ID)
			}
		}
		if c.client != claudeCode {
			continue
		}
		w.allow(s, p, req)
		consent := w.rcpt.of(receipts.KindConsent)
		got := consent[len(consent)-1].Rules
		if len(got) != 3 || got[2] != (rules.Ref{ID: "rul_claude", Version: 1}) {
			t.Fatalf("the consent's receipt records %v", got)
		}
	}
	// The rules cannot be read: the screen shows nothing to approve.
	src.Fail(accountA, accountrules.ErrUnavailable)
	if _, err := s.Start(context.Background(), p, w.ask(otherApp).query()); err == nil {
		t.Fatal("a screen without the account's rules")
	}
}

// removingDiscloser is the world's discloser whose messages leave with what
// removed says was taken out of them.
type removingDiscloser struct {
	*discloser
	removed func(id string) []inbox.RemovedBy
}

func (d *removingDiscloser) Page(ctx context.Context, b inbox.Box, ds inbox.Disclosure, before string, limit int) (*inbox.DisclosePage, error) {
	p, err := d.discloser.Page(ctx, b, ds, before, limit)
	if err != nil {
		return nil, err
	}
	for _, it := range p.Items {
		if it.Disclosed != nil {
			it.Disclosed.Removed = d.removed(it.Disclosed.ID)
		}
	}
	return p, nil
}

func (d *removingDiscloser) Message(ctx context.Context, b inbox.Box, ds inbox.Disclosure, id string) (inbox.Outcome, error) {
	o, err := d.discloser.Message(ctx, b, ds, id)
	if err == nil && o.Disclosed != nil {
		o.Disclosed.Removed = d.removed(id)
	}
	return o, err
}

// evidence is n kept answers at their largest, for a rule's removal.
func evidence(n int) []inbox.Decided {
	var out []inbox.Decided
	for i := range n {
		out = append(out, inbox.Decided{Atom: topicAtom(i), Evidence: inbox.Evidence{P: 0.29, Model: strings.Repeat("m", 64), Prompt: "s1-2026-11-20", CanaryChecked: "2026-11-20"}})
	}
	return out
}

func topicAtom(i int) query.Atom {
	return query.Atom{Var: contracts.VarMessage, Op: contracts.OpMentions, Topic: strings.Repeat(string(rune('a'+i%26)), 100) + "é\"\\"}
}

func TestU25ThePreviewSaysWhatIsHeldAndRemoved(t *testing.T) {
	w := newWorld(t)
	src := &accountrulestest.Fake{Notice: rulesNotice}
	s := w.ruleEnclave(src)
	w.mbx.msgs[mailboxID(1)][0].Held = true // m001
	rd := &removingDiscloser{discloser: w.disc, removed: func(id string) []inbox.RemovedBy {
		if id == "m002" {
			return []inbox.RemovedBy{{Rule: rules.Ref{ID: "rul_x", Version: 1}, Removed: contracts.RemovedSummary{Parts: []contracts.Part{contracts.PartSender}}}}
		}
		return nil
	}}
	s.cfg.Discloser = rd
	p := w.person(accountA)
	req := w.start(s, p, w.ask(claudeCode))
	d := allMail(req)
	page, err := s.Preview(context.Background(), p, req.ID, contracts.ConsentPreview{Draft: d, Mailbox: d.Mailboxes[0], AskModel: true})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(page.Page.Held, []string{"m001"}) || !slices.Equal(page.Page.Removed, []string{"m002"}) {
		t.Fatalf("held %v, removed %v", page.Page.Held, page.Page.Removed)
	}
	if last := w.disc.last(); len(last.Rules) != 1 || last.Rules[0].ID != rules.DefaultID {
		t.Fatalf("the preview applied %+v", last.Rules)
	}
}

// connected is a token's grant of claudeCode for accountA through s.
func (w *world) connected(s *Service) *oauth.Grant {
	w.t.Helper()
	a := w.ask(claudeCode)
	tok := w.connectAll(s, w.person(accountA), a)
	return w.grant(s, a, tok.AccessToken)
}

func TestU24ReceiptsKeepWhatEachRuleRemovedAndThePageEndsInTime(t *testing.T) {
	w := newWorld(t)
	s := w.ruleEnclave(&accountrulestest.Fake{Notice: rulesNotice})
	// Every message carries 16 rules' removals with 8 answers each at
	// their largest: far fewer than 50 fit a receipt.
	big := func(string) []inbox.RemovedBy {
		var out []inbox.RemovedBy
		for i := range contracts.MaxRules {
			out = append(out, inbox.RemovedBy{Rule: rules.Ref{ID: "rul_" + strings.Repeat(string(rune('a'+i)), 26), Version: 1 << 30},
				Removed: contracts.RemovedSummary{Parts: []contracts.Part{contracts.PartSender}, Shapes: map[contracts.ShapeKind]int{contracts.ShapePhoneNumber: 3}, Sentences: 2, Of: 128, Attachments: 1},
				Decided: evidence(8)})
		}
		return out
	}
	for i := 7; i <= 60; i++ {
		w.mbx.add(mail{ID: "m" + strings.Repeat("0", 3-len(strconv.Itoa(i))) + strconv.Itoa(i), Mailbox: mailboxID(1), Received: start.Add(-1), From: "s@example.com", Subject: "s", Body: "b"})
	}
	s.cfg.Discloser = &removingDiscloser{discloser: w.disc, removed: big}
	g := w.connected(s)
	seen := map[string]bool{}
	cursor := ""
	for range 20 {
		page, rel, err := s.Search(context.Background(), g, contracts.VendorSearch{Limit: 50, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		if rel != nil {
			rel.Finish(false)
		}
		if len(page.Messages) == 0 || len(page.Messages) >= 50 && page.Cursor != "" {
			t.Fatalf("a page of %d", len(page.Messages))
		}
		rc := w.rcpt.of(receipts.KindSearch)
		last := rc[len(rc)-1]
		if n, err := receipts.Size(&last); err != nil || n > receipts.MaxDocumentBytes {
			t.Fatalf("a receipt of %d bytes: %v", n, err)
		}
		for _, d := range last.Delivered {
			if len(d.Removed) != contracts.MaxRules || len(d.Removed[0].Model) != 8 || d.Removed[0].Removed.Of != 128 {
				t.Fatalf("removals dropped: %+v", d.Removed)
			}
		}
		for _, m := range page.Messages {
			if seen[m.ID] {
				t.Fatalf("%s twice", m.ID)
			}
			seen[m.ID] = true
		}
		if cursor = page.Cursor; cursor == "" {
			break
		}
	}
	if len(seen) != 61 { // 60 in the first mailbox, one in the second
		t.Fatalf("%d messages given, some skipped", len(seen))
	}
}

func TestU24AMessageThatCannotFitAnEmptyPageIsRefused(t *testing.T) {
	w := newWorld(t)
	s := w.ruleEnclave(&accountrulestest.Fake{Notice: rulesNotice})
	s.cfg.Discloser = &removingDiscloser{discloser: w.disc, removed: func(string) []inbox.RemovedBy {
		return []inbox.RemovedBy{{Rule: rules.Ref{ID: "rul_big", Version: 1}, Removed: contracts.RemovedSummary{Whole: true, Why: strings.Repeat("x", receipts.MaxDocumentBytes)}}}
	}}
	g := w.connected(s)
	page, _, err := s.Search(context.Background(), g, contracts.VendorSearch{Limit: 10})
	if !errors.Is(err, ErrUnavailable) || page != nil {
		t.Fatalf("%+v, %v", page, err)
	}
	refused := w.rcpt.of(receipts.KindRefused)
	if len(refused) == 0 || refused[len(refused)-1].Refused.Reason != receipts.RefusedUnavailable {
		t.Fatalf("refusals %+v", refused)
	}
}

func TestU26TheSeamWithDelivery(t *testing.T) {
	w := newWorld(t)
	src := &accountrulestest.Fake{Notice: rulesNotice}
	src.Set(accountA, scopedRules())
	s := w.ruleEnclave(src)
	g := w.connected(s)
	d, ap, err := s.Disclosure(context.Background(), accountA, g.Connection)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, r := range d.Rules {
		got = append(got, r.ID)
	}
	if !slices.Equal(got, []string{rules.DefaultID, "rul_all", "rul_claude"}) || len(ap.Rules) != 3 || !d.Model || d.Account != accountA || len(d.Formulas) == 0 {
		t.Fatalf("%+v", d)
	}
	// A message a rule holds is never Disclosed with it: no notice.
	w.mbx.msgs[mailboxID(1)][0].Held = true
	box, _ := w.mbx.OpenForConnection(context.Background(), accountA, mailboxID(1))
	if o, err := w.disc.Message(context.Background(), box, d, "m001"); err != nil || o.Disclosed != nil || o.Withheld == nil {
		t.Fatalf("%+v, %v", o, err)
	}
	// The account's rules cannot be read: no disclosure, and a read gives
	// nothing either, the default alone included.
	src.Fail(accountA, accountrules.ErrUnavailable)
	if _, _, err := s.Disclosure(context.Background(), accountA, g.Connection); !errors.Is(err, ErrUnavailable) || !errors.Is(err, accountrules.ErrUnavailable) {
		t.Fatalf("%v", err)
	}
	if _, _, err := s.Search(context.Background(), g, contracts.VendorSearch{Limit: 10}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("a read without the rules: %v", err)
	}
	src.Fail(accountA, nil)
	// Another account's connection, and the model off.
	if _, _, err := s.Disclosure(context.Background(), accountB, g.Connection); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("another account's: %v", err)
	}
	w.gate.set(inbox.AskLimit)
	if _, _, err := s.Disclosure(context.Background(), accountA, g.Connection); !errors.Is(err, contracts.ErrModelUnavailable) {
		t.Fatalf("the model off: %v", err)
	}
}
