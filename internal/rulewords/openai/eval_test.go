//go:build wordseval

package openai

// The words' eval of ADR 0040 (U33): the real client, with the prompt in
// force, against OpenAI's Responses API, on the synthetic phrasings of
// ../testdata/words.jsonl, never on anyone's words. Run by the owner with
// the eval project's key, never in CI (it needs the build tag and a key):
//
//	OPENAI_API_KEY=… [PERMAILY_EVAL_BUDGET_USD=2] [PERMAILY_EVAL_OUT=dir] \
//	go test -tags wordseval -run TestU33 -v -timeout 30m ./internal/rulewords/openai/
//
// The key is read from the environment and never printed. Every call is
// reserved at rulewords.Estimate before it is sent, and the eval stops
// before a call that would pass the budget (2 USD unless set). A phrasing
// is usable when rulewords.Read accepts the answer; it protects less than
// asked when its effect, criteria or removal leave out what the phrasing
// asks (expect in the corpus); the gate is at least 90% usable of the
// phrasings that ask for a rule, none protecting less, and the phrasings
// that ask for no rule refused.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/rulewords"
)

type wordsCase struct {
	ID     string `json:"id"`
	Lang   string `json:"lang"`
	Words  string `json:"words"`
	Expect struct {
		Unusable   bool                  `json:"unusable"`
		Effect     contracts.RuleEffect  `json:"effect"`
		Everything bool                  `json:"everything"`
		Require    []contracts.Condition `json:"require"`
		NarrowOK   bool                  `json:"narrow_ok"`
		Shapes     []contracts.ShapeKind `json:"shapes"`
		Parts      []contracts.Part      `json:"parts"`
		Topics     bool                  `json:"topics"`
		// OrHold are the conditions of a hold that protects at least as
		// much as the removal asked for (holding every message with
		// attachments in place of removing them).
		OrHold []contracts.Condition `json:"or_hold"`
	} `json:"expect"`
}

type wordsResult struct {
	ID      string               `json:"id"`
	Words   string               `json:"words"`
	Usable  bool                 `json:"usable"`
	Why     string               `json:"why,omitempty"`
	Less    string               `json:"less,omitempty"`
	Draft   *contracts.RuleDraft `json:"draft,omitempty"`
	Cost    int64                `json:"cost_nano_usd"`
	Took    time.Duration        `json:"took"`
	Refused bool                 `json:"refused,omitempty"`
}

func loadWords(t *testing.T) []wordsCase {
	f, err := os.Open(filepath.Join("..", "testdata", "words.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []wordsCase
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var c wordsCase
		dec := json.NewDecoder(strings.NewReader(sc.Text()))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&c); err != nil {
			t.Fatalf("%s: %v", sc.Text(), err)
		}
		out = append(out, c)
	}
	return out
}

// matches says a condition of a draft is the one the corpus requires:
// "*any*" any variable, "*text*" the subject, the body or the message,
// "*model*" about or mentions; values compared without case.
func matches(want, got contracts.Condition) bool {
	switch want.Var {
	case "*any*":
	case "*text*":
		if got.Var != contracts.VarSubject && got.Var != contracts.VarBody && got.Var != contracts.VarMessage {
			return false
		}
	default:
		if got.Var != want.Var {
			return false
		}
	}
	if want.Op == "*model*" {
		if got.Op != contracts.OpAbout && got.Op != contracts.OpMentions {
			return false
		}
	} else if got.Op != want.Op {
		return false
	}
	return !got.Not && (want.Value == "" || strings.EqualFold(want.Value, got.Value))
}

// less says how a draft protects less than its phrasing asks, or "".
func less(c wordsCase, d contracts.RuleDraft) string {
	e := c.Expect
	if e.Effect == contracts.EffectRemove && d.Effect == contracts.EffectHold && len(e.OrHold) > 0 {
		for _, f := range d.Criteria {
			if len(f.All) == 0 {
				return ""
			}
			if len(f.All) == 1 && slices.ContainsFunc(f.All[0].Any, func(got contracts.Condition) bool {
				return slices.ContainsFunc(e.OrHold, func(w contracts.Condition) bool { return matches(w, got) })
			}) {
				return ""
			}
		}
	}
	if e.Effect == contracts.EffectHold && d.Effect != contracts.EffectHold {
		return "removes where it should hold"
	}
	everything := slices.ContainsFunc(d.Criteria, func(f contracts.Formula) bool { return len(f.All) == 0 })
	if e.Everything && !everything {
		return "does not apply to all mail"
	}
	// A required condition is met by a criteria that holds it and whose
	// every group holds a required condition: another group would narrow
	// it (unless the phrasing asks for that). Other criteria only widen
	// the rule, as criteria are joined by "any".
	plain := func(f contracts.Formula) bool {
		if e.NarrowOK {
			return true
		}
		for _, g := range f.All {
			if !slices.ContainsFunc(g.Any, func(got contracts.Condition) bool {
				return slices.ContainsFunc(e.Require, func(w contracts.Condition) bool { return matches(w, got) })
			}) {
				return false
			}
		}
		return true
	}
	for _, w := range e.Require {
		found := everything
		for _, f := range d.Criteria {
			for _, g := range f.All {
				if slices.ContainsFunc(g.Any, func(got contracts.Condition) bool { return matches(w, got) }) && plain(f) {
					found = true
				}
			}
		}
		if !found {
			return "misses a condition it asks for, or narrows it"
		}
	}
	if d.Effect == contracts.EffectRemove {
		r := d.Removal
		for _, k := range e.Shapes {
			if !slices.Contains(r.Shapes, k) {
				return "misses a shape it asks for"
			}
		}
		for _, p := range e.Parts {
			if !slices.Contains(r.Parts, p) {
				return "misses a part it asks for"
			}
		}
		if e.Topics && len(r.Topics) == 0 && len(r.Shapes) == 0 && len(r.Parts) == 0 {
			return "removes nothing by meaning"
		}
	}
	return ""
}

func TestU33TheWordsEval(t *testing.T) {
	key := os.Getenv("OPENAI_API_KEY")
	if key == "" {
		t.Skip("OPENAI_API_KEY is not set")
	}
	budget := int64(2_000_000_000)
	if b := os.Getenv("PERMAILY_EVAL_BUDGET_USD"); b != "" {
		f, err := strconv.ParseFloat(b, 64)
		if err != nil || f <= 0 || f > 2 {
			t.Fatal("PERMAILY_EVAL_BUDGET_USD must be a number above 0 and at most 2")
		}
		budget = int64(f * 1e9)
	}
	cases := loadWords(t)
	c := New(nil, func(context.Context) (string, bool) { return key, true }, os.Getenv("PERMAILY_EVAL_ORG"), os.Getenv("PERMAILY_EVAL_PROJECT"))

	var mu sync.Mutex
	reserved, spent := int64(0), int64(0)
	results := make([]wordsResult, len(cases))
	sem := make(chan struct{}, 4)
	var wg sync.WaitGroup
	for i, wc := range cases {
		est := rulewords.Estimate(wc.Words)
		mu.Lock()
		if reserved+est > budget {
			mu.Unlock()
			t.Fatalf("the budget would be passed before %s; stopped", wc.ID)
		}
		reserved += est
		mu.Unlock()
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			r := wordsResult{ID: wc.ID, Words: wc.Words}
			start := time.Now()
			ans, err := c.Write(context.Background(), wc.Words)
			r.Took = time.Since(start)
			cost := est
			switch {
			case err != nil:
				r.Why = "failed: " + err.Error()
			default:
				if ans.Usage.Reported {
					cost = rulewords.Cost(ans.Usage)
				}
				r.Refused = ans.Refused
				if ans.Refused || ans.Incomplete {
					r.Why = "refused or cut"
					break
				}
				d, err := rulewords.Read(ans.Text)
				if err != nil {
					r.Why = "outside the schema or its bounds: " + ans.Text
					break
				}
				r.Usable, r.Draft = true, &d
			}
			r.Cost = cost
			mu.Lock()
			reserved += cost - est
			spent += cost
			mu.Unlock()
			results[i] = r
		}()
	}
	wg.Wait()

	asked, usable, lessCount, refusedRight, refusals := 0, 0, 0, 0, 0
	var took []time.Duration
	for i, wc := range cases {
		r := &results[i]
		took = append(took, r.Took)
		if wc.Expect.Unusable {
			refusals++
			if !r.Usable {
				refusedRight++
			} else {
				t.Logf("%s: proposed a rule for words that ask for none", wc.ID)
			}
			continue
		}
		asked++
		if !r.Usable {
			t.Logf("%s: unusable (%s)", wc.ID, r.Why)
			continue
		}
		usable++
		if r.Less = less(wc, *r.Draft); r.Less != "" {
			lessCount++
			b, _ := json.Marshal(r.Draft)
			t.Logf("%s: protects less: %s: %s", wc.ID, r.Less, b)
		}
	}
	slices.Sort(took)
	report := fmt.Sprintf("phrasings %d (%d asking for a rule, %d for none); usable %d of %d (%.1f%%); protecting less %d; no rule refused %d of %d; cost %.4f USD (%.6f a call); latency p50 %s, max %s; model %s, prompt %s",
		len(cases), asked, refusals, usable, asked, 100*float64(usable)/float64(asked), lessCount, refusedRight, refusals,
		float64(spent)/1e9, float64(spent)/1e9/float64(len(cases)), took[len(took)/2].Round(10*time.Millisecond),
		took[len(took)-1].Round(10*time.Millisecond), rulewords.ModelName, rulewords.PromptVersion)
	t.Log(report)
	if dir := os.Getenv("PERMAILY_EVAL_OUT"); dir != "" {
		b, _ := json.MarshalIndent(results, "", "  ")
		if err := os.WriteFile(filepath.Join(dir, "words-results.json"), b, 0o600); err != nil {
			t.Error(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "words-report.txt"), []byte(report+"\n"), 0o600); err != nil {
			t.Error(err)
		}
	}
	if float64(usable) < 0.9*float64(asked) || lessCount > 0 || refusedRight != refusals {
		t.Errorf("the gate is not met: %s", report)
	}
}
