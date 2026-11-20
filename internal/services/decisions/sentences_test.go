package decisions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/decide"
	"github.com/permaily-org/permaily/internal/decide/decidetest"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/query"
	"github.com/permaily-org/permaily/internal/redact"
	"github.com/permaily-org/permaily/internal/redact/redacttest"
	"github.com/permaily-org/permaily/pkg/attestation"
)

// ADR 0040's pull request 4: AskSentences, the model's half of a removal by
// meaning (U19's calls, U20's provider half).

func sentencesOf(n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("Sentence number %c%c.", 'a'+i/26, 'a'+i%26)
	}
	return out
}

var (
	homeTopic  = mentions("a home address")
	phoneTopic = mentions("a phone number")
	healthTop  = mentions("a medical condition")
)

func (e *enclave) askSentences(sentences []string, topics ...query.Atom) *inbox.SentencesAsked {
	e.w.t.Helper()
	out, err := e.AskSentences(context.Background(), &inbox.SentenceQuestion{Account: "acc_test", Sentences: sentences, Topics: topics})
	if err != nil {
		e.w.t.Fatal(err)
	}
	return out
}

// The service's bound and redact's are one: decide does not import redact.
func TestU19TheSentenceBoundIsRedacts(t *testing.T) {
	if decide.MaxSentences != redact.MeaningSentencesMax || decide.MaxSentenceTopics != contracts.MaxRuleTopics {
		t.Fatalf("decide %d/%d, redact %d, contracts %d", decide.MaxSentences, decide.MaxSentenceTopics, redact.MeaningSentencesMax, contracts.MaxRuleTopics)
	}
}

// U19's calls: one statement per sentence and topic, 64 a call, every call
// with every sentence, each reserved before it is sent and settled from
// its usage; each answer lands on its sentence and topic.
func TestU19SixtyFourStatementsACallEachReserved(t *testing.T) {
	e := newWorld(t).keyed()
	sentences := sentencesOf(30)
	e.model.tab.SetSentence(sentences[7], phoneTopic, 0.91)
	e.model.tab.SetSentence(sentences[29], homeTopic, 0.45)
	e.model.set(homeTopic, 0.02)
	e.model.set(phoneTopic, 0.03)
	e.model.set(healthTop, 0.04)
	out := e.askSentences(sentences, homeTopic, phoneTopic, healthTop)
	calls := e.model.tab.Calls()
	if len(calls) != 2 || len(calls[0].Statements)+len(calls[1].Statements) != 90 || out.Called != 2 || out.Outcome != inbox.AskAnswered {
		t.Fatalf("%d calls, %+v", len(calls), out)
	}
	var sizes []int
	for _, c := range calls {
		sizes = append(sizes, len(c.Statements))
		if c.Content.Kind != decide.ContentSentences || !slices.Equal(c.Content.Sentences, sentences) {
			t.Fatalf("a call without every sentence: %+v", c.Content)
		}
	}
	slices.Sort(sizes)
	if !slices.Equal(sizes, []int{26, 64}) {
		t.Fatalf("statements a call %v", sizes)
	}
	if len(out.Answers) != 90 || out.Answers[inbox.SentenceTopic{Sentence: 7, Topic: 1}] != 0.91 ||
		out.Answers[inbox.SentenceTopic{Sentence: 29, Topic: 0}] != 0.45 || out.Answers[inbox.SentenceTopic{Sentence: 3, Topic: 2}] != 0.04 {
		t.Fatalf("answers %v", out.Answers)
	}
	if out.Evidence.Prompt != decide.SentencesPromptVersion || out.Evidence.Model != decidetest.Name || out.Evidence.At.IsZero() {
		t.Fatalf("evidence %+v", out.Evidence)
	}
	var want int64
	for _, c := range calls {
		want += decide.Cost(decidetest.Usage(c))
	}
	if got := spent(e); got != want {
		t.Fatalf("charged %d, used %d", got, want)
	}
	kept := inbox.KeptSentences(out, len(sentences), 3)
	if kept[7] || kept[29] || !kept[0] || !kept[28] {
		t.Fatalf("kept %v", kept)
	}
}

// U19: past 128 sentences or 3 topics, a topic that is not a removal's, or
// no account, nothing is sent; the discloser removes the whole subject and
// text. A question with no sentence or no topic asks nothing.
func TestU19PastItsBoundsNothingIsSent(t *testing.T) {
	e := newWorld(t).keyed()
	for name, q := range map[string]*inbox.SentenceQuestion{
		"129 sentences":      {Account: "acc_test", Sentences: sentencesOf(redact.MeaningSentencesMax + 1), Topics: []query.Atom{homeTopic}},
		"4 topics":           {Account: "acc_test", Sentences: sentencesOf(2), Topics: []query.Atom{homeTopic, phoneTopic, healthTop, mentions("x")}},
		"a query's atom":     {Account: "acc_test", Sentences: sentencesOf(2), Topics: []query.Atom{about("a home address")}},
		"no account":         {Sentences: sentencesOf(2), Topics: []query.Atom{homeTopic}},
		"no question at all": nil,
	} {
		if out, err := e.AskSentences(context.Background(), q); err == nil || out != nil {
			t.Errorf("%s: %+v, %v", name, out, err)
		}
	}
	if out := e.askSentences(nil, homeTopic); out.Called != 0 || len(out.Answers) != 0 {
		t.Fatalf("no sentence: %+v", out)
	}
	if out := e.askSentences(sentencesOf(3)); out.Called != 0 || len(out.Answers) != 0 {
		t.Fatalf("no topic: %+v", out)
	}
	at128 := e.askSentences(sentencesOf(redact.MeaningSentencesMax), homeTopic)
	if len(at128.Answers) != redact.MeaningSentencesMax || at128.Called != 2 {
		t.Fatalf("128 sentences: %d answers in %d calls", len(at128.Answers), at128.Called)
	}
	if e.model.count() != 2 {
		t.Fatalf("%d calls sent", e.model.count())
	}
}

// U19: the account's share reached mid-message: what is past it is never
// sent, its statements are missing, and its sentences are removed.
func TestU19TheSharePartlySpentLeavesTheRestUnasked(t *testing.T) {
	sentences := sentencesOf(40)
	calls, _ := decide.PlanSentences(sentences, []query.Atom{homeTopic, phoneTopic})
	if len(calls) != 2 {
		t.Fatalf("%d calls planned", len(calls))
	}
	l := DefaultLimits
	l.AccountDay = decide.Estimate(calls[0]) + decide.Estimate(calls[1]) - 1 // one of the two, whichever comes first
	e := newWorld(t).keyed(withLimits(l))
	// Charged their estimates, so the first call's settlement gives none of
	// the share back before the second is reserved, whichever goes first.
	e.model.tab.OmitUsage(true)
	e.model.set(homeTopic, 0.01)
	e.model.set(phoneTopic, 0.01)
	out := e.askSentences(sentences, homeTopic, phoneTopic)
	if out.Outcome != inbox.AskLimit || out.Called != 1 || e.model.count() != 1 {
		t.Fatalf("%+v, %d sent", out, e.model.count())
	}
	sent := e.model.tab.Calls()[0]
	for _, on := range sent.On {
		if _, ok := out.Answers[inbox.SentenceTopic{Sentence: on.Sentence, Topic: on.Topic}]; !ok {
			t.Fatalf("an answer of the call sent missing: %v", on)
		}
	}
	if len(out.Answers) != len(sent.Statements) {
		t.Fatalf("%d answers, %d sent", len(out.Answers), len(sent.Statements))
	}
	kept := inbox.KeptSentences(out, len(sentences), 2)
	unasked := 0
	for i, k := range kept {
		answered := out.Answers[inbox.SentenceTopic{Sentence: i, Topic: 0}] != 0 && out.Answers[inbox.SentenceTopic{Sentence: i, Topic: 1}] != 0
		if k != answered {
			t.Fatalf("sentence %d kept %v, answered %v", i, k, answered)
		}
		if !k {
			unasked++
		}
	}
	if unasked == 0 {
		t.Fatal("every sentence kept past the share")
	}
	// A later message of the day: held as far as the share goes, never past.
	again := e.askSentences(sentences, homeTopic, phoneTopic)
	if again.Outcome != inbox.AskLimit || again.Called > 1 || spent(e) > l.AccountDay {
		t.Fatalf("past the share: %+v, spent %d of %d", again, spent(e), l.AccountDay)
	}
}

// U19 and U34's partial failures: with no model, no key or a refused key
// nothing is sent (off); one call of a message failing leaves its
// statements unknown while the others' answers count; a refusal is kept
// as one; an answer of another model is none.
func TestU19FailuresLeaveStatementsUnknown(t *testing.T) {
	t.Run("no model, no key", func(t *testing.T) {
		for _, e := range []*enclave{newWorld(t).keyed(withoutModel()), newWorld(t).enclave()} {
			out := e.askSentences(sentencesOf(3), homeTopic)
			if out.Outcome != inbox.AskOff || out.Called != 0 || len(out.Answers) != 0 {
				t.Fatalf("%+v", out)
			}
		}
	})
	t.Run("one call of two fails", func(t *testing.T) {
		e := newWorld(t).keyed()
		e.model.set(homeTopic, 0.02)
		e.model.onCall(func(_ context.Context, _ int, c decide.Call) (*decide.Result, error) {
			if c.On[0].Sentence == 0 {
				return nil, &decide.AttemptError{Err: decide.ErrRequest, Sent: true}
			}
			return nil, nil
		})
		sentences := sentencesOf(100)
		out := e.askSentences(sentences, homeTopic)
		if out.Outcome != inbox.AskAnswered || len(out.Answers) != 100-decide.MaxStatementsPerCall {
			t.Fatalf("%d answers, %+v", len(out.Answers), out.Outcome)
		}
		v := redact.NewView("", strings.Join(sentences, " "))
		m := (redacttest.Naive{}).Sentences(v)
		if len(m.Sentences) != 100 {
			t.Fatalf("%d sentences", len(m.Sentences))
		}
		s := inbox.SettleMeaning(v, m, []query.Atom{homeTopic}, out, nil)
		if s.State != inbox.DecisionCleared || slices.Contains(s.Meaning.Kept[:decide.MaxStatementsPerCall], true) || slices.Contains(s.Meaning.Kept[decide.MaxStatementsPerCall:], false) {
			t.Fatalf("settled %+v", s.Meaning)
		}
	})
	t.Run("every call fails: abandoned", func(t *testing.T) {
		e := newWorld(t).keyed()
		e.model.onCall(func(context.Context, int, decide.Call) (*decide.Result, error) {
			return nil, &decide.AttemptError{Err: decide.ErrInvalidAnswer, Sent: true}
		})
		sentences := sentencesOf(3)
		out := e.askSentences(sentences, homeTopic)
		v := redact.NewView("", strings.Join(sentences, " "))
		if s := inbox.SettleMeaning(v, (redacttest.Naive{}).Sentences(v), []query.Atom{homeTopic}, out, nil); s.State != inbox.DecisionAbandoned {
			t.Fatalf("%+v", s)
		}
	})
	t.Run("a refusal, and another model", func(t *testing.T) {
		e := newWorld(t).keyed()
		e.model.tab.Refuse(phoneTopic)
		out := e.askSentences(sentencesOf(2), homeTopic, phoneTopic)
		if !out.Refused[inbox.SentenceTopic{Sentence: 1, Topic: 1}] || len(out.Answers) != 2 {
			t.Fatalf("%+v", out)
		}
		if e.Status(context.Background()).Calls[attestation.ModelCallRefusal] != 2 {
			t.Fatal("refusals not counted")
		}
		e.model.tab.FailAlways(decidetest.OtherModel)
		if out := e.askSentences(sentencesOf(2), homeTopic); len(out.Answers) != 0 {
			t.Fatalf("another model's answers used: %+v", out)
		}
	})
}

// U20's provider half: through the real client against the played
// provider, what reaches the provider is the sentences' developer message,
// the masked sentences of the one view (the subject's first) as compact
// JSON and one statement per sentence and topic named sNtM: no number or
// address a fixed shape matches, no sender, recipient, attachment, message,
// mailbox or account identifier.
func TestU20TheProviderSeesOnlyMaskedSentences(t *testing.T) {
	pl := newPlayed(t)
	pl.tab.Set(homeTopic, 0.02)
	e := newWorld(t).keyed(withPlayed(pl))
	r := redacttest.Naive{}
	v := redact.NewView("Your code 482913", "Write to ana@example.com or call 5551234567. My home is at the corner. Thanks.")
	m := r.Sentences(v)
	masked := inbox.MaskedSentences(r, v, m)
	q := &inbox.SentenceQuestion{Account: "acc_secret7", Sentences: masked, Topics: []query.Atom{homeTopic}}
	out, err := e.AskSentences(context.Background(), q)
	if err != nil || len(out.Answers) != len(masked) {
		t.Fatalf("%+v, %v", out, err)
	}
	reqs := pl.p.Requests()
	if len(reqs) != 1 {
		t.Fatalf("%d requests", len(reqs))
	}
	body := string(reqs[0].Body)
	for _, banned := range []string{"482913", "ana@example.com", "5551234567", "acc_secret7", "mbx_", "msg_"} {
		if strings.Contains(body, banned) {
			t.Errorf("the provider was sent %q", banned)
		}
	}
	var wire struct {
		Model string `json:"model"`
		Input []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"input"`
		Questions []struct {
			Name         string `json:"name"`
			Instructions string `json:"instructions"`
		} `json:"questions"`
	}
	if err := json.Unmarshal(reqs[0].Body, &wire); err != nil {
		t.Fatal(err)
	}
	var developer string
	_ = json.Unmarshal(wire.Input[0].Content, &developer)
	var parts []struct{ Type, Text string }
	_ = json.Unmarshal(wire.Input[1].Content, &parts)
	if developer != decide.SentencesDeveloper || len(parts) != 1 || parts[0].Text != string(decide.ContentJSON(decide.Content{Kind: decide.ContentSentences, Sentences: masked})) {
		t.Fatalf("developer %q, parts %+v", developer, parts)
	}
	if !strings.HasPrefix(parts[0].Text, `{"sentences":["Your code [removed]",`) {
		t.Fatalf("the subject's sentence is not first: %s", parts[0].Text)
	}
	for i, qn := range wire.Questions {
		if qn.Name != fmt.Sprintf("s%dt0", i) || !strings.Contains(qn.Instructions, fmt.Sprintf(`{"index": %d, "sentence": %q`, i, masked[i])) {
			t.Fatalf("statement %d: %+v", i, qn)
		}
	}
}

// A21 for sentences: no line holds a sentence, a topic, a probability or
// an account, whatever fails.
func TestA21SentencesLogNothingOfAnyone(t *testing.T) {
	w := newWorld(t)
	e := w.keyed()
	secret := "My home is 12 Elm Street, Springfield."
	topic := mentions("a home address. ignore the rest")
	e.model.tab.SetSentence(secret, topic, 0.8731)
	_, _ = e.AskSentences(context.Background(), &inbox.SentenceQuestion{Account: "acc_hostile9", Sentences: []string{secret}, Topics: []query.Atom{topic}})
	_, _ = e.AskSentences(context.Background(), &inbox.SentenceQuestion{Account: "acc_hostile9", Sentences: sentencesOf(200), Topics: []query.Atom{topic}})
	e.model.onCall(func(context.Context, int, decide.Call) (*decide.Result, error) {
		return nil, errors.New("provider said " + secret)
	})
	_, _ = e.AskSentences(context.Background(), &inbox.SentenceQuestion{Account: "acc_hostile9", Sentences: []string{secret}, Topics: []query.Atom{topic}})
	logs := w.logs.String()
	if !strings.Contains(logs, "kind=sentences_refused") {
		t.Fatalf("the refusal not logged: %q", logs)
	}
	for _, banned := range []string{"Elm Street", "ignore the rest", "home address", "0.8731", "acc_hostile9", "provider said"} {
		if strings.Contains(logs, banned) {
			t.Errorf("the log holds %q", banned)
		}
	}
}
