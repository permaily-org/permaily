package inbox_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"hash/fnv"
	"math/rand/v2"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/decide"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/query"
	"github.com/permaily-org/permaily/internal/store"
)

// Matching asks the model (ADR 0028, "Matching asks the model", pull
// request 3): A25 to A27 and A24's "an entry whose item is gone is never
// read", against a played Asker that keeps inbox.Asker's contract.

// playedAsker is an Asker that answers from a table as the service would by
// inbox.Asker's contract: kept answers first (through the real KeptStore
// when kept is set), the text read only when a message atom has no kept
// answer, AskStop with nothing asked when the text cannot be afforded,
// every atom without a kept answer held when the outcome is AskOff or
// AskLimit, and only answers that were kept used.
type playedAsker struct {
	// p is the model's answer for an atom of a message; ok false is a
	// refusal or a failure: the atom stays unknown. nil answers 0.95.
	p func(id string, a query.Atom) (float64, bool)
	// outcome is AskOff or AskLimit to hold a message's atoms; nil or
	// AskAnswered asks them.
	outcome func(id string) inbox.AskOutcome
	kept    inbox.KeptStore
	// onAsk runs first; an error it returns is Ask's.
	onAsk func(ctx context.Context, q *inbox.Question) error

	mu        sync.Mutex
	asked     []string
	questions []inbox.Question
	texts     map[string]inbox.TextState
	got       map[string]string
	calls     int
}

const playedModel = "played"

func sum(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func (a *playedAsker) answer(id string, at query.Atom) (float64, bool) {
	if a.p == nil {
		return 0.95, true
	}
	return a.p(id, at)
}

func keptFor(d *inbox.KeptDecisions, at query.Atom, content string) (float64, bool) {
	if d == nil {
		return 0, false
	}
	for _, k := range d.Answers {
		if k.Content == content && k.Var == string(at.Var) && k.Op == string(at.Op) && k.Topic == at.Topic &&
			k.Model == playedModel && k.Prompt == decide.PromptVersion && !k.Refused {
			return k.P, true
		}
	}
	return 0, false
}

func (a *playedAsker) Ask(ctx context.Context, q *inbox.Question) (*inbox.Asked, error) {
	if a.onAsk != nil {
		if err := a.onAsk(ctx, q); err != nil {
			return nil, err
		}
	}
	a.mu.Lock()
	a.asked = append(a.asked, q.MessageID)
	a.questions = append(a.questions, *q)
	a.mu.Unlock()
	var kept *inbox.KeptDecisions
	if a.kept != nil {
		var err error
		if kept, err = a.kept.Kept(ctx, q.Box, q.MessageID); err != nil {
			return nil, err
		}
	}
	subject := decide.ContentHash(decide.Content{Kind: decide.ContentSubject, Subject: q.Subject})
	view := decide.PromptVersion + "/" + q.View.Key()
	message := ""
	if kept != nil {
		if v, ok := kept.Views[view]; ok && v.Subject == sum(q.Subject) {
			message = v.Content
		}
	}
	contentOf := func(at query.Atom) string {
		if decide.KindOf(at) == decide.ContentSubject {
			return subject
		}
		return message
	}
	out := &inbox.Asked{Answers: query.Answers{}, Outcome: inbox.AskAnswered}
	var missing []query.Atom
	needText := false
	for _, at := range q.Atoms {
		if c := contentOf(at); c != "" {
			if p, ok := keptFor(kept, at, c); ok {
				out.Answers[at] = p
				continue
			}
		}
		missing = append(missing, at)
		needText = needText || decide.KindOf(at) == decide.ContentMessage
	}
	st := inbox.TextState(0)
	if needText {
		text, s, err := q.Text(ctx)
		if err != nil {
			return nil, err
		}
		st = s
		a.mu.Lock()
		if a.texts == nil {
			a.texts, a.got = map[string]inbox.TextState{}, map[string]string{}
		}
		a.texts[q.MessageID], a.got[q.MessageID] = s, text
		a.mu.Unlock()
		if s == inbox.TextUnaffordable {
			out.Outcome = inbox.AskStop
			return out, nil
		}
		if s == inbox.TextRead {
			message = decide.ContentHash(decide.Content{Kind: decide.ContentMessage, Subject: q.Subject, Body: text})
		}
	}
	if a.outcome != nil {
		if o := a.outcome(q.MessageID); o == inbox.AskOff || o == inbox.AskLimit {
			out.Held, out.Outcome = missing, o
			return out, nil
		}
	}
	add := &inbox.KeptDecisions{V: 1, Views: map[string]inbox.KeptView{}}
	if st == inbox.TextRead {
		add.Views[view] = inbox.KeptView{Content: message, Subject: sum(q.Subject)}
	}
	answered := query.Answers{}
	for _, at := range missing {
		if decide.KindOf(at) == decide.ContentMessage && st != inbox.TextRead {
			continue // its text cannot be read: unknown
		}
		a.mu.Lock()
		a.calls++
		a.mu.Unlock()
		out.Called++
		p, ok := a.answer(q.MessageID, at)
		if !ok {
			continue
		}
		answered[at] = p
		add.Answers = append(add.Answers, inbox.KeptAnswer{
			Content: contentOf(at), Var: string(at.Var), Op: string(at.Op), Topic: at.Topic,
			Model: playedModel, Prompt: decide.PromptVersion, P: p, At: qNow,
		})
	}
	if a.kept == nil {
		for at, p := range answered {
			out.Answers[at] = p
		}
		return out, nil
	}
	d, err := a.kept.Keep(ctx, q.Box, q.MessageID, q.ExpiresAt, add)
	if errors.Is(err, inbox.ErrGone) {
		return &inbox.Asked{Answers: query.Answers{}, Outcome: inbox.AskAnswered, Called: out.Called}, nil
	}
	if err != nil {
		return nil, err
	}
	for at := range answered {
		if p, ok := keptFor(d, at, contentOf(at)); ok {
			out.Answers[at] = p
		}
	}
	return out, nil
}

func (a *playedAsker) askedIDs() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.asked)
}

func (a *playedAsker) callCount() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.calls
}

func asking(a inbox.Asker) context.Context {
	return inbox.WithAsker(context.Background(), inbox.Asking{Asker: a, Account: "acc_me"})
}

// byTopic answers by the atom's topic from a table, and refuses a topic
// that is not in it.
func byTopic(table map[string]float64) func(string, query.Atom) (float64, bool) {
	return func(_ string, a query.Atom) (float64, bool) {
		p, ok := table[a.Topic]
		return p, ok
	}
}

// byID answers by the message, from a table.
func byID(table map[string]float64) func(string, query.Atom) (float64, bool) {
	return func(id string, _ query.Atom) (float64, bool) {
		p, ok := table[id]
		return p, ok
	}
}

var (
	subjectAbout = func(topic string) contracts.Condition {
		return cond(contracts.VarSubject, contracts.OpAbout, topic)
	}
	messageAbout = func(topic string) contracts.Condition {
		return cond(contracts.VarMessage, contracts.OpAbout, topic)
	}
)

// A25: with an Asker, a message that needs a model is asked, and its
// answers make it a match, nothing or unknown by Classify(ForQuery);
// subject atoms read neither the entry nor the raw message; without an
// Asker the page is ADR 0027's, with no model.
func TestA25AMessageThatNeedsAModelIsAsked(t *testing.T) {
	st := newQStore()
	b := qBox(t)
	var id []string
	for i := range 4 {
		id = append(id, seedQ(t, st, b, &qMsg{at: qAt(i), n: i, from: []string{"a@example.com"}, subject: "x", body: qLong}))
	}
	fs := []*query.Canonical{formula(group(subjectAbout("invoices")))}
	p := map[string]float64{id[0]: 0.95, id[1]: 0.05, id[2]: 0.5, id[3]: 0.80}
	a := &playedAsker{p: byID(p)}
	mt := inbox.NewMatcherWith(st, query.Eval, inbox.MatchBounds{})
	page, err := mt.Match(asking(a), b, fs, "", 50)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Compare(ids(page.Messages), []string{id[0], id[3]}) != 0 || slices.Compare(page.Unknown, []string{id[2]}) != 0 ||
		len(page.NeedsModel) != 0 || page.Model != contracts.MatchModelAsked {
		t.Fatalf("page %+v", page)
	}
	if len(st.entries) != 0 || len(st.raws) != 0 {
		t.Fatalf("subject atoms read %d entries and %d raw messages", len(st.entries), len(st.raws))
	}
	q := a.questions[0]
	if q.Account != "acc_me" || q.Box.MailboxID != b.MailboxID || q.Subject != "x" || q.View != inbox.ViewOriginal ||
		q.ExpiresAt.IsZero() || !slices.Equal(q.Atoms, []query.Atom{{Var: contracts.VarSubject, Op: contracts.OpAbout, Topic: "invoices"}}) {
		t.Fatalf("question %+v", q)
	}
	// Without an Asker: every one needs a model, and the page says nothing
	// of one.
	st.reset()
	plain, err := mt.Match(context.Background(), b, fs, "", 50)
	if err != nil || slices.Compare(plain.NeedsModel, id) != 0 || len(plain.Unknown) != 0 || len(plain.Messages) != 0 || plain.Model != "" {
		t.Fatalf("page %+v, %v", plain, err)
	}
	out, _ := json.Marshal(plain)
	if strings.Contains(string(out), `"model"`) {
		t.Fatalf("a page without an Asker says %s", out)
	}
	// Nor does an Asking without an Asker.
	if p, _ := mt.Match(inbox.WithAsker(context.Background(), inbox.Asking{Account: "acc_me"}), b, fs, "", 50); p.Model != "" {
		t.Fatal("an Asking without an Asker asked")
	}
	// A formula no message matches: no message is asked, and the page says
	// asked.
	never, err := mt.Match(asking(a), b, []*query.Canonical{{Never: true}}, "", 50)
	if err != nil || never.Model != contracts.MatchModelAsked {
		t.Fatalf("%+v, %v", never, err)
	}
}

// A25: the text a message atom is asked with: the entry's when it was not
// cut (reading the entry if the page had not), the raw message's when it
// was, under its own grant counted in the page's raw reads and given back,
// the raw text a body condition read used again, TextUnavailable when the
// raw message cannot be read, and a message atom with no text left unknown
// while its subject atoms are still asked.
func TestA25TheTextAMessageIsAskedWith(t *testing.T) {
	st := newQStore()
	b := qBox(t)
	short := seedQ(t, st, b, &qMsg{at: qAt(0), n: 0, subject: "s", body: "pay the invoice"})
	long := seedQ(t, st, b, &qMsg{at: qAt(1), n: 1, subject: "s", body: qLong + "invoices at the end"})
	gone := seedQ(t, st, b, &qMsg{at: qAt(2), n: 2, subject: "s", body: qLong + "x"})
	noEntry := seedQ(t, st, b, &qMsg{at: qAt(3), n: 3, subject: "s", body: "short, but its entry is gone", noEntry: true})
	st.onRaw = func(id string) error {
		if id == gone {
			return store.ErrNotFound
		}
		return nil
	}
	fs := []*query.Canonical{formula(group(messageAbout("invoices"), subjectAbout("bills")))}
	a := &playedAsker{p: byTopic(map[string]float64{"invoices": 0.1, "bills": 0.1})}
	g := &qGrants{free: inbox.RawReadMax}
	ctx := inbox.WithReadBudget(asking(a), g)
	page, err := inbox.NewMatcherWith(st, query.Eval, inbox.MatchBounds{}).Match(ctx, b, fs, "", 50)
	if err != nil {
		t.Fatal(err)
	}
	if a.texts[short] != inbox.TextRead || a.got[short] != "pay the invoice" {
		t.Fatalf("short: %v %q", a.texts[short], a.got[short])
	}
	if a.texts[long] != inbox.TextRead || !strings.HasSuffix(a.got[long], "invoices at the end") || len(a.got[long]) <= inbox.SearchTextMax {
		t.Fatalf("long: %v, %d bytes", a.texts[long], len(a.got[long]))
	}
	if a.texts[gone] != inbox.TextUnavailable || a.texts[noEntry] != inbox.TextRead || a.got[noEntry] != "short, but its entry is gone" {
		t.Fatalf("gone %v, no entry %v %q", a.texts[gone], a.texts[noEntry], a.got[noEntry])
	}
	if slices.Compare(st.raws, []string{long, gone, noEntry}) != 0 || g.granted != 3 || g.heldNow() != 0 || g.maxHeld != 1 {
		t.Fatalf("raw reads %v, granted %d, held %d", st.raws, g.granted, g.heldNow())
	}
	// gone: its message atom unknown, its subject atom answered 0.1: the
	// group is unknown, not nothing.
	if slices.Compare(page.Unknown, []string{gone}) != 0 || len(page.Messages) != 0 {
		t.Fatalf("page %+v", page)
	}
	// A body condition that read the raw message: the model is asked with
	// that text, and nothing is read again.
	st.reset()
	a = &playedAsker{p: byTopic(map[string]float64{"invoices": 0.9})}
	body := []*query.Canonical{formula(group(cond(contracts.VarBody, contracts.OpContains, "nowhere"), messageAbout("invoices")))}
	page, err = inbox.NewMatcherWith(st, query.Eval, inbox.MatchBounds{}).Match(asking(a), b, body, "", 50)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Compare(st.raws, []string{long, gone, noEntry}) != 0 || !strings.HasSuffix(a.got[long], "invoices at the end") {
		t.Fatalf("raw reads %v", st.raws)
	}
	if slices.Compare(ids(page.Messages), []string{short, long, noEntry}) != 0 || slices.Compare(page.Unknown, []string{gone}) != 0 {
		t.Fatalf("page %+v", page)
	}
	// Text read twice reads once; read after Ask returned reads nothing.
	st.reset()
	var later func(context.Context) (string, inbox.TextState, error)
	twice := &playedAsker{onAsk: func(ctx context.Context, q *inbox.Question) error {
		later = q.Text
		t1, _, _ := q.Text(ctx)
		t2, _, _ := q.Text(ctx)
		if t1 != t2 {
			t.Errorf("two texts: %q, %q", t1, t2)
		}
		return nil
	}}
	if _, err := inbox.NewMatcherWith(st, query.Eval, inbox.MatchBounds{}).Match(asking(twice), b, fs, short, 1); err != nil {
		t.Fatal(err)
	}
	if len(st.raws) != 1 {
		t.Fatalf("%d raw reads", len(st.raws))
	}
	if _, s, _ := later(context.Background()); s != inbox.TextUnaffordable || len(st.raws) != 1 {
		t.Fatalf("a text read after Ask: %v", s)
	}
}

// A25: kept answers are used: asked again, a message whose answers were kept
// for its view and subject is answered with no call and no read of its
// text.
func TestA25KeptAnswersAreUsed(t *testing.T) {
	st := newQStore()
	b := qBox(t)
	for i := range 6 {
		seedQ(t, st, b, &qMsg{at: qAt(i), n: i, subject: "s", body: qLong + "invoices"})
	}
	fs := []*query.Canonical{formula(group(messageAbout("invoices"), subjectAbout("bills")))}
	a := &playedAsker{p: byTopic(map[string]float64{"invoices": 0.95, "bills": 0.05}), kept: inbox.NewKeptStore(st.Memory)}
	mt := inbox.NewMatcherWith(st, query.Eval, inbox.MatchBounds{})
	first, err := mt.Match(asking(a), b, fs, "", 50)
	if err != nil || len(first.Messages) != 6 || a.callCount() != 12 || len(st.raws) != 6 {
		t.Fatalf("%+v, %v; %d calls, %d raw reads", first, err, a.callCount(), len(st.raws))
	}
	st.reset()
	second, err := mt.Match(asking(a), b, fs, "", 50)
	if err != nil || slices.Compare(ids(second.Messages), ids(first.Messages)) != 0 || a.callCount() != 12 || len(st.raws) != 0 {
		t.Fatalf("%+v, %v; %d calls, %d raw reads", second, err, a.callCount(), len(st.raws))
	}
	for _, key := range st.entries {
		if !strings.HasPrefix(key, inbox.KeyDecision) {
			t.Fatalf("read %s", key)
		}
	}
}

// A26: the bounds of a page with a model: the 26th message that needs it
// ends the page before it, and the next page asks it; a message whose text
// needs the eleventh raw read ends the page before it (AskStop, nothing
// spent), the next page asks it; body conditions and the model share the
// ten raw reads; at most 25 messages are asked a page.
func TestA26TheBoundsOfAPageWithAModel(t *testing.T) {
	t.Run("25 asked a page", func(t *testing.T) {
		st := newQStore()
		b := qBox(t)
		var all []string
		for i := range 60 {
			all = append(all, seedQ(t, st, b, &qMsg{at: qAt(i), n: i, subject: "s"}))
		}
		a := &playedAsker{p: byTopic(map[string]float64{"invoices": 0.5})}
		fs := []*query.Canonical{formula(group(subjectAbout("invoices")))}
		mt := inbox.NewMatcherWith(st, query.Eval, inbox.MatchBounds{})
		page, err := mt.Match(asking(a), b, fs, "", 100)
		if err != nil || len(page.Unknown) != inbox.MatchMaxModelMessages || page.Next != all[24] || len(a.askedIDs()) != 25 {
			t.Fatalf("%v: %d unknown, next %s, %d asked", err, len(page.Unknown), page.Next, len(a.askedIDs()))
		}
		next, err := mt.Match(asking(a), b, fs, page.Next, 100)
		if err != nil || len(next.Unknown) != 25 || next.Unknown[0] != all[25] {
			t.Fatalf("%v: %+v", err, next)
		}
	})
	t.Run("the eleventh raw read", func(t *testing.T) {
		st := newQStore()
		b := qBox(t)
		var all []string
		for i := range 12 {
			all = append(all, seedQ(t, st, b, &qMsg{at: qAt(i), n: i, subject: "s", body: qLong + "invoices"}))
		}
		a := &playedAsker{p: byTopic(map[string]float64{"invoices": 0.95})}
		fs := []*query.Canonical{formula(group(messageAbout("invoices")))}
		mt := inbox.NewMatcherWith(st, query.Eval, inbox.MatchBounds{})
		page, err := mt.Match(asking(a), b, fs, "", 100)
		if err != nil || len(page.Messages) != 10 || page.Next != all[9] || len(st.raws) != 10 || a.texts[all[10]] != inbox.TextUnaffordable || a.callCount() != 10 {
			t.Fatalf("%v: %d matches, next %s, %d raw reads, %d calls", err, len(page.Messages), page.Next, len(st.raws), a.callCount())
		}
		next, err := mt.Match(asking(a), b, fs, page.Next, 100)
		if err != nil || slices.Compare(ids(next.Messages), all[10:]) != 0 || next.Next != "" {
			t.Fatalf("%v: %+v", err, next)
		}
	})
	t.Run("body conditions and the model share the raw reads", func(t *testing.T) {
		st := newQStore()
		b := qBox(t)
		var all []string
		for i := range 12 {
			body := qLong + "invoices"
			if i%2 == 0 {
				body = qLong + "paid"
			}
			all = append(all, seedQ(t, st, b, &qMsg{at: qAt(i), n: i, subject: "s", body: body}))
		}
		// Even messages match by their body; odd ones need the model,
		// whose text is the raw read the body condition made.
		fs := []*query.Canonical{formula(group(cond(contracts.VarBody, contracts.OpContains, "paid"), messageAbout("invoices")))}
		a := &playedAsker{p: byTopic(map[string]float64{"invoices": 0.95})}
		mt := inbox.NewMatcherWith(st, query.Eval, inbox.MatchBounds{TextReads: 3})
		page, err := mt.Match(asking(a), b, fs, "", 100)
		if err != nil || len(st.raws) != 3 || page.Next != all[2] || len(page.Messages) != 3 {
			t.Fatalf("%v: %d raw reads, next %s, %+v", err, len(st.raws), page.Next, page)
		}
	})
	t.Run("an Asker that fails", func(t *testing.T) {
		st := newQStore()
		b := qBox(t)
		seedQ(t, st, b, &qMsg{at: qAt(0), n: 0, subject: "s"})
		seedQ(t, st, b, &qMsg{at: qAt(1), n: 1, subject: "s"})
		fs := []*query.Canonical{formula(group(subjectAbout("invoices")))}
		mt := inbox.NewMatcherWith(st, query.Eval, inbox.MatchBounds{})
		failing := &playedAsker{onAsk: func(context.Context, *inbox.Question) error { return errQ }}
		if _, err := mt.Match(asking(failing), b, fs, "", 100); !errors.Is(err, errQ) {
			t.Fatalf("a failed ask: %v", err)
		}
		// The message's own deadline is not the request's: unknown, and the
		// page goes on.
		late := &playedAsker{onAsk: func(ctx context.Context, q *inbox.Question) error {
			if dl, ok := ctx.Deadline(); !ok || time.Until(dl) > decide.MessageDeadline {
				t.Errorf("no message deadline: %v", dl)
			}
			if q.MessageID == qMsgID(0) {
				return context.DeadlineExceeded
			}
			return nil
		}}
		page, err := mt.Match(asking(late), b, fs, "", 100)
		if err != nil || slices.Compare(page.Unknown, []string{qMsgID(0)}) != 0 || len(page.Messages) != 1 {
			t.Fatalf("%+v, %v", page, err)
		}
		// An Asker that stops the first message of a page would stop every
		// page there: it is listed as unknown.
		stop := &playedAsker{onAsk: func(_ context.Context, q *inbox.Question) error {
			q.Text = func(context.Context) (string, inbox.TextState, error) { return "", inbox.TextUnaffordable, nil }
			return nil
		}}
		textual := []*query.Canonical{formula(group(messageAbout("invoices")))}
		page, err = mt.Match(asking(stop), b, textual, "", 100)
		if err != nil || slices.Compare(page.Unknown, []string{qMsgID(0)}) != 0 || page.Next != qMsgID(0) {
			t.Fatalf("%+v, %v", page, err)
		}
	})
}

func qMsgID(n int) string { q := qMsg{at: qAt(n), n: n}; return q.id() }

// A26: what the page says: needs_model for held atoms, off before limit
// before asked, and a failure listed as unknown.
func TestA26WhatThePageSays(t *testing.T) {
	st := newQStore()
	b := qBox(t)
	var id []string
	for i := range 4 {
		id = append(id, seedQ(t, st, b, &qMsg{at: qAt(i), n: i, subject: "s"}))
	}
	fs := []*query.Canonical{formula(group(subjectAbout("invoices")))}
	mt := inbox.NewMatcherWith(st, query.Eval, inbox.MatchBounds{})
	for _, c := range []struct {
		name     string
		outcomes map[string]inbox.AskOutcome
		want     contracts.MatchModel
	}{
		{"asked", nil, contracts.MatchModelAsked},
		{"limit", map[string]inbox.AskOutcome{id[1]: inbox.AskLimit}, contracts.MatchModelLimit},
		{"off", map[string]inbox.AskOutcome{id[2]: inbox.AskOff}, contracts.MatchModelOff},
		{"off before limit", map[string]inbox.AskOutcome{id[1]: inbox.AskLimit, id[2]: inbox.AskOff, id[3]: inbox.AskLimit}, contracts.MatchModelOff},
	} {
		t.Run(c.name, func(t *testing.T) {
			a := &playedAsker{
				p:       byID(map[string]float64{id[0]: 0.95, id[1]: 0.95, id[2]: 0.95}), // id[3]: a failure
				outcome: func(m string) inbox.AskOutcome { return c.outcomes[m] },
			}
			page, err := mt.Match(asking(a), b, fs, "", 100)
			if err != nil || page.Model != c.want {
				t.Fatalf("%+v, %v", page, err)
			}
			var held []string
			for _, m := range id[1:3] {
				if c.outcomes[m] != 0 {
					held = append(held, m)
				}
			}
			if c.outcomes[id[3]] != 0 {
				held = append(held, id[3])
			}
			if slices.Compare(page.NeedsModel, held) != 0 {
				t.Fatalf("needs a model %v, want %v", page.NeedsModel, held)
			}
			if c.outcomes[id[3]] == 0 && slices.Compare(page.Unknown, []string{id[3]}) != 0 {
				t.Fatalf("a failure: unknown %v", page.Unknown)
			}
		})
	}
	// Held atoms that cannot make it true: unknown, not needing a model;
	// a held atom given a probability anyway is still unknown.
	two := []*query.Canonical{formula(group(subjectAbout("invoices")), group(subjectAbout("bills")))}
	partial := askerFunc(func(_ context.Context, q *inbox.Question) (*inbox.Asked, error) {
		bills := query.Atom{Var: contracts.VarSubject, Op: contracts.OpAbout, Topic: "bills"}
		invoices := query.Atom{Var: contracts.VarSubject, Op: contracts.OpAbout, Topic: "invoices"}
		switch q.MessageID {
		case id[0]: // bills in the band of doubt: whatever invoices is, unknown
			return &inbox.Asked{Answers: query.Answers{bills: 0.5, invoices: 1}, Held: []query.Atom{invoices}, Outcome: inbox.AskLimit}, nil
		case id[1]: // bills true: invoices, held, can make it true
			return &inbox.Asked{Answers: query.Answers{bills: 0.95, invoices: 1}, Held: []query.Atom{invoices}, Outcome: inbox.AskLimit}, nil
		case id[2]: // bills false: nothing, whatever invoices is
			return &inbox.Asked{Answers: query.Answers{bills: 0.05}, Held: []query.Atom{invoices}, Outcome: inbox.AskLimit}, nil
		}
		return nil, nil
	})
	page, err := mt.Match(asking(partial), b, two, "", 100)
	if err != nil || slices.Compare(page.Unknown, []string{id[0], id[3]}) != 0 || slices.Compare(page.NeedsModel, []string{id[1]}) != 0 ||
		len(page.Messages) != 0 || page.Model != contracts.MatchModelLimit {
		t.Fatalf("%+v, %v", page, err)
	}
}

type askerFunc func(ctx context.Context, q *inbox.Question) (*inbox.Asked, error)

func (f askerFunc) Ask(ctx context.Context, q *inbox.Question) (*inbox.Asked, error) {
	return f(ctx, q)
}

// A26: with a slow Asker, the soft deadline, checked before each message,
// ends the page with next at the last message considered.
func TestA26ASlowAskerEndsThePageAtTheSoftDeadline(t *testing.T) {
	st := newQStore()
	b := qBox(t)
	var all []string
	for i := range 5 {
		all = append(all, seedQ(t, st, b, &qMsg{at: qAt(i), n: i, subject: "s"}))
	}
	var mu sync.Mutex
	clock := qNow
	now := func() time.Time { mu.Lock(); defer mu.Unlock(); return clock }
	a := &playedAsker{onAsk: func(context.Context, *inbox.Question) error {
		mu.Lock()
		defer mu.Unlock()
		clock = clock.Add(15 * time.Second)
		return nil
	}}
	fs := []*query.Canonical{formula(group(subjectAbout("invoices")))}
	page, err := inbox.NewMatcherWith(st, query.Eval, inbox.MatchBounds{Now: now}).Match(asking(a), b, fs, "", 100)
	if err != nil || slices.Compare(ids(page.Messages), all[:2]) != 0 || page.Next != all[1] {
		t.Fatalf("%+v, %v", page, err)
	}
}

// A27: an Unparsed message and a Gmail confirmation are never asked.
func TestA27UnparsedAndConfirmationsAreNeverAsked(t *testing.T) {
	st := newQStore()
	b := qBox(t)
	unparsed := seedQ(t, st, b, &qMsg{at: qAt(0), n: 0, subject: "invoice", unparsed: true})
	seedQ(t, st, b, &qMsg{at: qAt(1), n: 1, subject: "invoice", confirmation: true})
	a := &playedAsker{}
	fs := []*query.Canonical{formula(group(subjectAbout("invoices"), messageAbout("invoices")))}
	mt := inbox.NewMatcherWith(st, query.Eval, inbox.MatchBounds{})
	page, err := mt.Match(asking(a), b, fs, "", 100)
	plain, _ := mt.Match(context.Background(), b, fs, "", 100)
	listed := slices.Concat(page.NeedsModel, page.Unknown)
	if err != nil || len(a.askedIDs()) != 0 || slices.Compare(listed, []string{unparsed}) != 0 || len(page.Messages) != 0 ||
		slices.Compare(page.NeedsModel, plain.NeedsModel) != 0 || slices.Compare(page.Unknown, plain.Unknown) != 0 {
		t.Fatalf("%+v, %v; asked %v", page, err, a.askedIDs())
	}
}

// A24: a kept entry whose message item is gone, or expired, is never read:
// it is read only by the identifier of an item being considered.
func TestA24AnEntryWhoseItemIsGoneIsNeverRead(t *testing.T) {
	ctx := context.Background()
	st := newQStore()
	b := qBox(t)
	live := seedQ(t, st, b, &qMsg{at: qAt(0), n: 0, subject: "s"})
	expired := seedQ(t, st, b, &qMsg{at: qAt(1), n: 1, subject: "s", expired: true})
	ks := inbox.NewKeptStore(st.Memory)
	orphan := qMsgID(2)
	for _, id := range []string{expired, orphan} {
		if _, err := ks.Keep(ctx, b, id, qNow.Add(time.Hour), kAdd(kAnswer("invoices", 0.9))); err != nil {
			t.Fatal(err)
		}
	}
	a := &playedAsker{kept: ks}
	fs := []*query.Canonical{formula(group(subjectAbout("invoices")))}
	if _, err := inbox.NewMatcherWith(st, query.Eval, inbox.MatchBounds{}).Match(asking(a), b, fs, "", 100); err != nil {
		t.Fatal(err)
	}
	if slices.Compare(a.askedIDs(), []string{live}) != 0 {
		t.Fatalf("asked %v", a.askedIDs())
	}
	for _, key := range st.entries {
		if strings.HasSuffix(key, expired) || strings.HasSuffix(key, orphan) {
			t.Fatalf("read %s", key)
		}
	}
}

// askModelPool are the conditions a model decides that the union test adds.
var askModelPool = []contracts.Condition{
	subjectAbout("invoices"), subjectAbout("hello"), messageAbout("invoices"),
	cond(contracts.VarMessage, contracts.OpMentions, "a phone number"),
}

func hashOf(parts ...string) uint64 {
	h := fnv.New64a()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return h.Sum64()
}

// genAnswer is the played model's answer for a generated message's atom:
// on both sides of each threshold, in the band of doubt, or none.
func genAnswer(id string, a query.Atom) (float64, bool) {
	switch hashOf(id, string(a.Var), string(a.Op), a.Topic) % 5 {
	case 0:
		return 0.05, true
	case 1:
		return 0.5, true
	case 2:
		return 0.95, true
	case 3:
		return 0.2, true
	}
	return 0, false
}

func genOutcome(id string) inbox.AskOutcome {
	switch h := hashOf(id) % 26; {
	case h == 0:
		return inbox.AskOff
	case h < 3:
		return inbox.AskLimit
	}
	return inbox.AskAnswered
}

// askedReference is where the whole read lists q when every message that
// needs a model is asked (A26's brute force): reference's listing for every
// other, and for one that needs a model, the value with the played model's
// answers: needing a model when its atoms were held, unknown for an atom
// whose text cannot be read.
func askedReference(b inbox.Box, fs []*query.Canonical, q *qMsg) listing {
	want, _, readRaw := reference(b, fs, q)
	if want != listedNeedsModel || q.unparsed {
		return want
	}
	if o := genOutcome(q.id()); o == inbox.AskOff || o == inbox.AskLimit {
		return listedNeedsModel
	}
	meta := q.meta()
	item := &store.Message{MailboxID: b.MailboxID, ID: q.id(), Size: len(q.raw())}
	entry := q.entry()
	if q.noEntry || q.badEntry {
		entry = &inbox.SearchEntry{Cut: true}
	}
	textOK := readRaw || !inbox.QueryMessage(item, &meta, entry, nil).BodyCut || !q.unreadable()
	var m *query.Message
	if hasBody(fs) {
		var text *inbox.RawText
		if readRaw {
			t, _, _ := inbox.MessageText(context.Background(), q.raw(), inbox.MessageTextMax)
			text = &inbox.RawText{Text: t, Size: int64(len(q.raw()))}
		}
		m = inbox.QueryMessage(item, &meta, entry, text)
	} else {
		m = inbox.QueryMessage(item, &meta, nil, nil)
	}
	res := query.Eval(fs, m, nil, query.ForQuery)
	answers := query.Answers{}
	for _, a := range res.Model {
		if decide.KindOf(a) == decide.ContentMessage && !textOK {
			continue
		}
		if p, ok := genAnswer(q.id(), a); ok {
			answers[a] = p
		}
	}
	switch query.Eval(fs, m, answers, query.ForQuery).Value {
	case query.True:
		return listedMatch
	case query.False:
		return listedNone
	}
	return listedUnknown
}

// A26 (Q28 extended with a played Asker): pages skip nothing. Over
// generated mailboxes and formulas with conditions a model decides, with
// every bound of a page set low (items read, raw reads, messages asked,
// undecided messages, the limit), the union of successive pages is the
// brute force's with every budget unbounded.
func TestA26PagesSkipNothing(t *testing.T) {
	t.Parallel() // its own stores, boxes and generator
	rng := rand.New(rand.NewPCG(26, 1))
	for _, n := range []int{0, 1, 9, 40, 300} {
		st := newQStore()
		b := qBox(t)
		var msgs []*qMsg
		for i := range n {
			q := genMessage(rng, i, true)
			msgs = append(msgs, q)
			seedQ(t, st, b, q)
		}
		for trial := range 8 {
			fs := genFormulas(rng, b, false)
			if rng.IntN(4) != 0 {
				var g []contracts.Condition
				for range 1 + rng.IntN(2) {
					c := askModelPool[rng.IntN(len(askModelPool))]
					if rng.IntN(4) == 0 {
						c = not(c)
					}
					g = append(g, c)
				}
				fs = append(fs, formula(g))
			}
			st.PageItems = rng.IntN(4)
			a := &playedAsker{p: genAnswer, outcome: genOutcome}
			mt := inbox.NewMatcherAsking(st, query.Eval, inbox.MatchBounds{Read: 7 + rng.IntN(6), TextReads: 1 + rng.IntN(3), Undecided: 1 + rng.IntN(5)}, 1+rng.IntN(4))
			got, all := pagesAsking(t, mt, asking(a), b, fs, func() int { return 1 + rng.IntN(5) }, 4*n+10)
			for _, p := range all {
				if p.Model == "" {
					t.Fatal("a page with an Asker says nothing of the model")
				}
			}
			asked := map[string]int{}
			for _, id := range a.askedIDs() {
				asked[id]++
			}
			for _, q := range msgs {
				l, listed := got[q.id()]
				if q.expired {
					if listed {
						t.Fatalf("%d messages, trial %d: an expired message listed", n, trial)
					}
					continue
				}
				if want := askedReference(b, fs, q); l != want {
					t.Fatalf("%d messages, trial %d, message %d: listed %v, want %v", n, trial, q.n, l, want)
				}
				if (q.unparsed || q.confirmation) && asked[q.id()] != 0 {
					t.Fatalf("message %d asked though unparsed or a confirmation", q.n)
				}
			}
		}
	}
}

// pagesAsking is pages under ctx.
func pagesAsking(t testing.TB, mt inbox.Matcher, ctx context.Context, b inbox.Box, fs []*query.Canonical, limit func() int, maxPages int) (map[string]listing, []*contracts.MatchPage) {
	t.Helper()
	got := map[string]listing{}
	var all []*contracts.MatchPage
	before := ""
	for range maxPages {
		page, err := mt.Match(ctx, b, fs, before, limit())
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, page)
		add := func(id string, l listing) {
			if prev, ok := got[id]; ok {
				t.Fatalf("%s listed twice: %v then %v", id, prev, l)
			}
			got[id] = l
		}
		for _, m := range page.Messages {
			add(m.ID, listedMatch)
		}
		for _, id := range page.NeedsModel {
			add(id, listedNeedsModel)
		}
		for _, id := range page.Unknown {
			add(id, listedUnknown)
		}
		if page.Next == "" {
			return got, all
		}
		if before != "" && page.Next >= before {
			t.Fatalf("next %s does not go below %s", page.Next, before)
		}
		before = page.Next
	}
	t.Fatalf("more than %d pages", maxPages)
	return nil, nil
}

// A26: more held atoms than verdictOf tries (two formulas of five) leave a
// message needing a model without trying them.
func TestA26ManyHeldAtomsNeedAModel(t *testing.T) {
	st := newQStore()
	b := qBox(t)
	id := seedQ(t, st, b, &qMsg{at: qAt(0), n: 0, subject: "s"})
	var f1, f2 [][]contracts.Condition
	for i := range 5 {
		f1 = append(f1, group(subjectAbout("one "+string(rune('a'+i)))))
		f2 = append(f2, group(subjectAbout("two "+string(rune('a'+i)))))
	}
	fs := []*query.Canonical{formula(f1...), formula(f2...)}
	a := &playedAsker{outcome: func(string) inbox.AskOutcome { return inbox.AskLimit }}
	page, err := inbox.NewMatcherWith(st, query.Eval, inbox.MatchBounds{}).Match(asking(a), b, fs, "", 10)
	if err != nil || slices.Compare(page.NeedsModel, []string{id}) != 0 || len(a.questions[0].Atoms) != 10 {
		t.Fatalf("%+v, %v", page, err)
	}
}

// A26: the text's own reads within the page: an entry the page has no read
// left for ends the page before the message; a store that fails, a grant
// refused and a request that ends while the text is taken out fail the
// page.
func TestA26TheTextsReadsWithinThePage(t *testing.T) {
	fs := []*query.Canonical{formula(group(messageAbout("invoices")))}
	t.Run("no read left for the entry", func(t *testing.T) {
		st := newQStore()
		b := qBox(t)
		var all []string
		for i := range 3 {
			all = append(all, seedQ(t, st, b, &qMsg{at: qAt(i), n: i, subject: "s", body: "short"}))
		}
		a := &playedAsker{}
		page, err := inbox.NewMatcherWith(st, query.Eval, inbox.MatchBounds{Read: 3}).Match(asking(a), b, fs, "", 10)
		if err != nil || slices.Compare(ids(page.Messages), all[:1]) != 0 || page.Next != all[0] || a.texts[all[1]] != inbox.TextUnaffordable {
			t.Fatalf("%+v, %v", page, err)
		}
	})
	st := newQStore()
	b := qBox(t)
	seedQ(t, st, b, &qMsg{at: qAt(0), n: 0, subject: "s", body: qLong})
	mt := inbox.NewMatcherWith(st, query.Eval, inbox.MatchBounds{})
	st.onEntry = func(string) error { return errQ }
	if _, err := mt.Match(asking(&playedAsker{}), b, fs, "", 10); !errors.Is(err, errQ) {
		t.Fatalf("a failed entry: %v", err)
	}
	st.onEntry = nil
	if _, err := mt.Match(inbox.WithReadBudget(asking(&playedAsker{}), &qGrants{refuse: errQ}), b, fs, "", 10); !errors.Is(err, errQ) {
		t.Fatalf("a refused grant: %v", err)
	}
	ctx, cancel := context.WithCancel(asking(&playedAsker{}))
	st.onRaw = func(string) error { cancel(); return nil }
	if _, err := mt.Match(ctx, b, fs, "", 10); !errors.Is(err, context.Canceled) {
		t.Fatalf("an ended request: %v", err)
	}
}
