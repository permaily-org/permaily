package decisions

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/decide"
	"github.com/permaily-org/permaily/internal/decide/decidetest"
	"github.com/permaily-org/permaily/internal/rulewords"
	wordsclient "github.com/permaily-org/permaily/internal/rulewords/openai"
	"github.com/permaily-org/permaily/pkg/attestation"
)

// The words' calls through the service (ADR 0040, U11 to U13): the real
// client of internal/rulewords/openai against decidetest's played Responses
// API, over HTTPS and HTTP/2, every call reserved through the guard.

// account is a test account whose identifier must never reach the model.
const account = "acc_7QX2WORDSACCOUNTNEVERSENT"

// withWriter makes the enclave's words' client the real one, built as the
// composition builds it, talking to w.
func withWriter(w *decidetest.Writer) option {
	return func(c *Config, _ *enclave) {
		c.NewWriter = func(key decide.Credential) rulewords.Model {
			return wordsclient.New(w.Client(), key, "", "")
		}
	}
}

// withWordsModel makes the enclave's words' client m.
func withWordsModel(m rulewords.Model) option {
	return func(c *Config, _ *enclave) {
		c.NewWriter = func(decide.Credential) rulewords.Model { return m }
	}
}

// share is what account reserved today in e.
func share(e *enclave, account string) int64 {
	e.guard.mu.Lock()
	defer e.guard.mu.Unlock()
	return e.guard.shares[dayOf(e.w.clock())][account]
}

// TestU11TheWordsAndNothingOfTheAccount: what reaches the provider is the
// words, in rulewords.Request's bytes; the account, the key (but in its
// header) and nothing else of the account are in no byte sent; no log line
// holds the words.
func TestU11TheWordsAndNothingOfTheAccount(t *testing.T) {
	w := newWorld(t)
	pw := decidetest.NewWriter(t, false, func(string) decidetest.WriterReply {
		return decidetest.WriterReply{InputTokens: 2000, OutputTokens: 400}
	})
	e := w.keyed(withWriter(pw))
	words := "Hide everything from my bank, bank.example, and my doctor"
	out, err := e.Write(context.Background(), account, words)
	if err != nil {
		t.Fatal(err)
	}
	if out.Prompt != rulewords.PromptVersion || out.Model != rulewords.ModelName || out.Draft.Scope.To != contracts.ScopeAll || !out.Draft.On {
		t.Errorf("written = %+v", out)
	}
	reqs := pw.Requests()
	if len(reqs) != 1 || string(reqs[0].Body) != string(rulewords.Request(words)) {
		t.Fatal("the request is not the words alone")
	}
	for _, r := range reqs {
		all := string(r.Body) + r.Path + r.Host
		for k, vs := range r.Header {
			if k != "Authorization" {
				all += k + strings.Join(vs, "")
			}
		}
		for _, secret := range []string{account, "acc_", testKey} {
			if strings.Contains(all, secret) {
				t.Errorf("%q was sent", secret)
			}
		}
	}
	if logs := w.logs.String(); strings.Contains(logs, "bank.example") || strings.Contains(logs, "doctor") || strings.Contains(logs, account) {
		t.Errorf("the logs hold the words or the account:\n%s", logs)
	}
}

// TestU12TheWordsAnswerThroughTheService: a usable answer is the draft, for
// every connection and on, with the model and the prompt's version; an
// answer outside the schema, an invented mailbox, a removal on a hold, a
// refusal and a cut answer are rule_words_unusable; each is settled at
// what it reported.
func TestU12TheWordsAnswerThroughTheService(t *testing.T) {
	for name, c := range map[string]struct {
		reply decidetest.WriterReply
		want  error
	}{
		"usable":         {decidetest.WriterReply{}, nil},
		"not the schema": {decidetest.WriterReply{Text: `{"rule":"hold everything"}`}, contracts.ErrRuleWordsUnusable},
		"a mailbox": {decidetest.WriterReply{Text: `{"possible":true,"name":"n","effect":"hold","criteria":[{"all":[{"any":[{"var":"mailbox","op":"is","value":"work","not":false}]}]}],"removal":null}`},
			contracts.ErrRuleWordsUnusable},
		"a hold with a removal": {decidetest.WriterReply{Text: `{"possible":true,"name":"n","effect":"hold","criteria":[{"all":[]}],"removal":{"parts":["body"],"shapes":[],"topics":[]}}`},
			contracts.ErrRuleWordsUnusable},
		"no rule": {decidetest.WriterReply{Text: `{"possible":false,"name":"","effect":"hold","criteria":[],"removal":null}`}, contracts.ErrRuleWordsUnusable},
		"refused": {decidetest.WriterReply{Refusal: true}, contracts.ErrRuleWordsUnusable},
		"cut":     {decidetest.WriterReply{Incomplete: true}, contracts.ErrRuleWordsUnusable},
	} {
		w := newWorld(t)
		c.reply.InputTokens, c.reply.OutputTokens = 3000, 700
		pw := decidetest.NewWriter(t, false, func(string) decidetest.WriterReply { return c.reply })
		e := w.keyed(withWriter(pw))
		out, err := e.Write(context.Background(), account, "hold mail from bank.example")
		if !errors.Is(err, c.want) || (c.want == nil) != (out != nil) {
			t.Errorf("%s: %+v, %v; want %v", name, out, err, c.want)
		}
		if c.want == nil && (out.Draft.Effect != contracts.EffectHold || out.Draft.Criteria[0].All[0].Any[0].Value != "bank.example") {
			t.Errorf("%s: draft %+v", name, out.Draft)
		}
		if got, want := share(e, account), rulewords.Cost(rulewords.Usage{InputTokens: 3000, OutputTokens: 700}); got != want {
			t.Errorf("%s: settled at %d, want %d", name, got, want)
		}
	}
}

// TestU13EachCallIsReservedBeforeItIsSentAndSettledOnce: while the call is
// at the provider, the account's share holds its estimate; after, what the
// answer reported, or the estimate when it reported nothing.
func TestU13EachCallIsReservedBeforeItIsSentAndSettledOnce(t *testing.T) {
	w := newWorld(t)
	var e *enclave
	var during int64
	words := "remove phone numbers"
	pw := decidetest.NewWriter(t, false, func(string) decidetest.WriterReply {
		during = share(e, account)
		return decidetest.WriterReply{InputTokens: 2500, CachedTokens: 500, OutputTokens: 900}
	})
	e = w.keyed(withWriter(pw))
	if _, err := e.Write(context.Background(), account, words); err != nil {
		t.Fatal(err)
	}
	if during != rulewords.Estimate(words) {
		t.Errorf("reserved %d while sent, want the estimate %d", during, rulewords.Estimate(words))
	}
	if got, want := share(e, account), rulewords.Cost(rulewords.Usage{InputTokens: 2500, CachedTokens: 500, OutputTokens: 900}); got != want {
		t.Errorf("settled at %d, want %d", got, want)
	}
	if spent(e) != share(e, account) {
		t.Errorf("the grant was charged %d, the share %d", spent(e), share(e, account))
	}

	// No usage reported: the estimate.
	w2 := newWorld(t)
	pw2 := decidetest.NewWriter(t, false, func(string) decidetest.WriterReply { return decidetest.WriterReply{NoUsage: true} })
	e2 := w2.keyed(withWriter(pw2))
	if _, err := e2.Write(context.Background(), account, words); err != nil {
		t.Fatal(err)
	}
	if got := share(e2, account); got != rulewords.Estimate(words) {
		t.Errorf("an answer with no usage settled at %d, want the estimate", got)
	}
}

// TestU13TheModelUnavailableForWords: no client, no key, a refused key, the
// provider's limit, the account's share, a 5xx, another model, a timeout:
// model_unavailable, nothing proposed, each attempt sent charged its
// estimate, one not sent nothing; a refused key and the provider's limit
// stop the guard.
func TestU13TheModelUnavailableForWords(t *testing.T) {
	words := "hide mail from bank.example"
	est := rulewords.Estimate(words)
	t.Run("no client", func(t *testing.T) {
		e := newWorld(t).keyed()
		if _, err := e.Write(context.Background(), account, words); !errors.Is(err, contracts.ErrModelUnavailable) {
			t.Error(err)
		}
	})
	t.Run("no key", func(t *testing.T) {
		pw := decidetest.NewWriter(t, false, nil)
		e := newWorld(t).enclave(withWriter(pw))
		if _, err := e.Write(context.Background(), account, words); !errors.Is(err, contracts.ErrModelUnavailable) || len(pw.Requests()) != 0 {
			t.Error(err)
		}
	})
	t.Run("no account", func(t *testing.T) {
		pw := decidetest.NewWriter(t, false, nil)
		e := newWorld(t).keyed(withWriter(pw))
		if _, err := e.Write(context.Background(), "", words); !errors.Is(err, contracts.ErrModelUnavailable) || len(pw.Requests()) != 0 {
			t.Error(err)
		}
	})
	t.Run("the account's share", func(t *testing.T) {
		pw := decidetest.NewWriter(t, false, nil)
		l := DefaultLimits
		l.AccountDay = est - 1
		e := newWorld(t).keyed(withWriter(pw), withLimits(l))
		if _, err := e.Write(context.Background(), account, words); !errors.Is(err, contracts.ErrModelUnavailable) || len(pw.Requests()) != 0 {
			t.Error(err)
		}
		if share(e, account) != 0 {
			t.Error("a call held was counted in the share")
		}
	})
	t.Run("the month's limit", func(t *testing.T) {
		pw := decidetest.NewWriter(t, false, nil)
		l := DefaultLimits
		l.Month = l.Block / 2
		e := newWorld(t).keyed(withWriter(pw), withLimits(l))
		if _, err := e.Write(context.Background(), account, words); !errors.Is(err, contracts.ErrModelUnavailable) || len(pw.Requests()) != 0 {
			t.Error(err)
		}
	})
	for name, c := range map[string]struct {
		reply decidetest.WriterReply
		guard string
	}{
		"a refused key":        {decidetest.WriterReply{Status: 401, Code: "invalid_api_key"}, attestation.ModelGuardKeyRefused},
		"the provider's limit": {decidetest.WriterReply{Status: 429, Code: "insufficient_quota"}, attestation.ModelGuardProviderLimit},
		"a 5xx":                {decidetest.WriterReply{Status: 503, Code: "server_error"}, attestation.ModelGuardOK},
		"slow down":            {decidetest.WriterReply{Status: 429, Code: "rate_limit_exceeded"}, attestation.ModelGuardOK},
		"another model":        {decidetest.WriterReply{Model: "gpt-6-sol", InputTokens: 1, OutputTokens: 1}, attestation.ModelGuardOK},
	} {
		t.Run(name, func(t *testing.T) {
			pw := decidetest.NewWriter(t, false, func(string) decidetest.WriterReply { return c.reply })
			e := newWorld(t).keyed(withWriter(pw))
			if _, err := e.Write(context.Background(), account, words); !errors.Is(err, contracts.ErrModelUnavailable) {
				t.Fatal(err)
			}
			if got := share(e, account); got != est {
				t.Errorf("charged %d, want the estimate %d", got, est)
			}
			if got := e.guardState(context.Background()); got != c.guard {
				t.Errorf("guard %s, want %s", got, c.guard)
			}
			if len(pw.Requests()) != 1 {
				t.Errorf("%d requests: one attempt, never tried again", len(pw.Requests()))
			}
			if c.guard != attestation.ModelGuardOK {
				// The guard holds the next call without sending it.
				if _, err := e.Write(context.Background(), account, words); !errors.Is(err, contracts.ErrModelUnavailable) || len(pw.Requests()) != 1 {
					t.Errorf("the next call: %v, %d requests", err, len(pw.Requests()))
				}
			}
		})
	}
	for name, c := range map[string]struct {
		err    error
		charge int64
	}{
		"a timeout":          {&decide.AttemptError{Err: decide.ErrUnavailable, Sent: true, Retry: true}, est},
		"not sent":           {&decide.AttemptError{Err: decide.ErrUnavailable}, 0},
		"a key not sent":     {&decide.AttemptError{Err: decide.ErrKeyRefused}, 0},
		"a malformed answer": {&decide.AttemptError{Err: decide.ErrInvalidAnswer, Sent: true}, est},
		"another error":      {errors.New("not an attempt's"), est},
	} {
		t.Run(name, func(t *testing.T) {
			m := &wordsModel{err: c.err}
			e := newWorld(t).keyed(withWordsModel(m))
			if _, err := e.Write(context.Background(), account, words); !errors.Is(err, contracts.ErrModelUnavailable) {
				t.Fatal(err)
			}
			if got := share(e, account); got != c.charge {
				t.Errorf("charged %d, want %d", got, c.charge)
			}
			if m.calls != 1 {
				t.Errorf("%d attempts, want 1", m.calls)
			}
		})
	}
}

// TestU13TheCallerGivingUp: a context done before the call is its own
// error, nothing sent; one that ends while the call is out is its own
// error, the attempt charged its estimate.
func TestU13TheCallerGivingUp(t *testing.T) {
	words := "hide x"
	m := &wordsModel{}
	e := newWorld(t).keyed(withWordsModel(m))
	done, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := e.Write(done, account, words); !errors.Is(err, context.Canceled) || m.calls != 0 {
		t.Errorf("a context done: %v, %d calls", err, m.calls)
	}
	ctx, cancel2 := context.WithCancel(context.Background())
	m.during = cancel2
	m.err = &decide.AttemptError{Err: decide.ErrUnavailable, Sent: true}
	if _, err := e.Write(ctx, account, words); !errors.Is(err, context.Canceled) {
		t.Errorf("a context ended while out: %v", err)
	}
	if got := share(e, account); got != rulewords.Estimate(words) {
		t.Errorf("charged %d", got)
	}
	// Every slot taken: the caller's context ends the wait.
	e2 := newWorld(t).keyed(withWordsModel(&wordsModel{}))
	for range cap(e2.slots) {
		e2.slots <- struct{}{}
	}
	short, cancel3 := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel3()
	if _, err := e2.Write(short, account, words); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("no slot: %v", err)
	}
}

// TestU13TheThirtyFirstCallOfADay: an account's 31st words call of a day in
// one enclave is refused (too_many_requests) with nothing sent, whatever
// became of the 30 before; words that may not be sent are not counted;
// another account, and the next day, start again.
func TestU13TheThirtyFirstCallOfADay(t *testing.T) {
	w := newWorld(t)
	m := &wordsModel{err: &decide.AttemptError{Err: decide.ErrUnavailable, Sent: true}}
	e := w.keyed(withWordsModel(m))
	for _, bad := range []string{"", "  ", strings.Repeat("w", 1001), "a\xffb"} {
		if _, err := e.Write(context.Background(), account, bad); !errors.Is(err, contracts.ErrRuleWordsUnusable) {
			t.Errorf("words %.10q: %v", bad, err)
		}
	}
	for i := range rulewords.CallsPerDay {
		if _, err := e.Write(context.Background(), account, "hide x"); !errors.Is(err, contracts.ErrModelUnavailable) {
			t.Fatalf("call %d: %v", i+1, err)
		}
	}
	if _, err := e.Write(context.Background(), account, "hide x"); !errors.Is(err, contracts.ErrTooManyRequests) {
		t.Errorf("the 31st call: %v", err)
	}
	if m.calls != rulewords.CallsPerDay {
		t.Errorf("%d attempts, want %d", m.calls, rulewords.CallsPerDay)
	}
	if _, err := e.Write(context.Background(), "acc_other", "hide x"); !errors.Is(err, contracts.ErrModelUnavailable) {
		t.Errorf("another account: %v", err)
	}
	w.advance(24 * time.Hour)
	if _, err := e.Write(context.Background(), account, "hide x"); !errors.Is(err, contracts.ErrModelUnavailable) {
		t.Errorf("the next day: %v", err)
	}
	// /status counts every call by its outcome (ADR 0040, pull request 8):
	// the four unusable words, the 30 and two more the model was not
	// there for, the 31st refused.
	want := map[string]int64{attestation.WordsProposed: 0, attestation.WordsUnusable: 4, attestation.WordsTooMany: 1, attestation.WordsUnavailable: int64(rulewords.CallsPerDay) + 2}
	if got := e.WordsCalls(); len(got) != len(want) || got[attestation.WordsUnusable] != 4 || got[attestation.WordsTooMany] != 1 ||
		got[attestation.WordsUnavailable] != want[attestation.WordsUnavailable] || got[attestation.WordsProposed] != 0 {
		t.Errorf("the words calls counted %v, want %v", got, want)
	}
}

// wordsModel is a words' client that answers err, or HoldText, and counts
// its calls; during, when set, runs while a call is out.
type wordsModel struct {
	mu     sync.Mutex
	err    error
	calls  int
	during func()
}

func (m *wordsModel) Name() string { return rulewords.ModelName }

func (m *wordsModel) Write(context.Context, string) (*rulewords.Answer, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.calls++
	if m.during != nil {
		m.during()
	}
	if m.err != nil {
		return nil, m.err
	}
	return &rulewords.Answer{Text: decidetest.HoldText, Model: rulewords.ModelName}, nil
}
