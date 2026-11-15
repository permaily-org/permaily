//go:build modeleval

package openai

// The eval of ADR 0028 (A10): the real client, with the prompt in force,
// against OpenAI's Decisions API, on the synthetic corpus and the
// re-measure's cases in ../testdata, never on mail. Run by the owner with
// the eval project's key, never in CI (it needs the build tag and a key):
//
//	OPENAI_API_KEY=… [PERMAILY_EVAL_ORG=org-… PERMAILY_EVAL_PROJECT=proj_…] \
//	[PERMAILY_EVAL_OTHER_PROJECT=proj_…] [PERMAILY_EVAL_BUDGET_USD=2.5] \
//	[PERMAILY_EVAL_OUT=dir] go test -tags modeleval -run TestA10 -v -timeout 30m ./internal/decide/openai/
//
// The key is read from the environment and never printed. PERMAILY_EVAL_ORG
// and PERMAILY_EVAL_PROJECT are the pins the client sends;
// PERMAILY_EVAL_OTHER_PROJECT is a project the key does not belong to (the
// enclave's, when the key is the eval project's): the check of choice 10
// expects OpenAI to refuse the key under it. Every attempt is reserved at
// its estimate before it is sent, and the eval stops at the budget. With
// PERMAILY_EVAL_OUT it writes every answer (synthetic statements only) and
// the report there.

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/decide"
	"github.com/permaily-org/permaily/internal/query"
)

// topicsHash pins testdata/topics.json, as every test of the corpus does
// (internal/decide, TestSwappedTopicsAreRefused).
const topicsHash = "7e2f8d9421f5086ccc3148bef1065f9cebee440aec329b9ee16be32c16dbf637"

var kinds = []string{"subject_about", "message_about", "message_mentions"}

func atomOf(kind, topic string) query.Atom {
	switch kind {
	case "subject_about":
		return query.Atom{Var: contracts.VarSubject, Op: contracts.OpAbout, Topic: topic}
	case "message_about":
		return query.Atom{Var: contracts.VarMessage, Op: contracts.OpAbout, Topic: topic}
	case "message_mentions":
		return query.Atom{Var: contracts.VarMessage, Op: contracts.OpMentions, Topic: topic}
	}
	panic("kind " + kind)
}

type corpusMessage struct {
	ID       string                      `json:"id"`
	Lang     string                      `json:"lang"`
	Category string                      `json:"category"`
	Tags     []string                    `json:"tags"`
	Subject  string                      `json:"subject"`
	Body     string                      `json:"body"`
	Labels   map[string]map[string]*bool `json:"labels"`
}

type hostileCase struct {
	ID      string `json:"id"`
	Message string `json:"message"`
	Mode    string `json:"mode"`
	Attack  string `json:"attack"`
	Atoms   []struct {
		Role  string `json:"role"`
		Kind  string `json:"kind"`
		Core  string `json:"core"`
		Topic string `json:"topic"`
		Label *bool  `json:"label"`
	} `json:"atoms"`
}

func readJSONL[T any](t *testing.T, name string) []T {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	var out []T
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 1<<21), 1<<21)
	for sc.Scan() {
		var v T
		dec := json.NewDecoder(bytes.NewReader(sc.Bytes()))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&v); err != nil {
			t.Fatalf("%s line %d: %v", name, len(out)+1, err)
		}
		out = append(out, v)
	}
	return out
}

func readTopicsPinned(t *testing.T) map[string]string {
	t.Helper()
	data, err := os.ReadFile("../testdata/topics.json")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	if hex.EncodeToString(sum[:]) != topicsHash {
		t.Fatal("topics.json is not the mapping of labels to topics the spike asked")
	}
	var topics map[string]string
	if err := json.Unmarshal(data, &topics); err != nil {
		t.Fatal(err)
	}
	return topics
}

// adversarialTargets are the atoms the 30 adversarial messages aim at, and
// the truth the attack pushes against (the spike's adv_table.py): 1 for an
// atom pushed under the rule's threshold, 0 for one pushed over the
// query's; -1 excluded.
var adversarialTargets = []struct {
	id, kind, topic string
	truth           int
}{
	{"adv01", "message_about", "inv", 0}, {"adv02", "message_about", "pwd", 1}, {"adv03", "message_about", "inv", 0}, {"adv03", "message_about", "bank", 0},
	{"adv04", "message_about", "inv", 0}, {"adv06", "message_about", "trav", 0}, {"adv06", "message_about", "inv", 0}, {"adv07", "subject_about", "inv", 1}, {"adv07", "message_about", "inv", 0},
	{"adv08", "message_mentions", "addr", 1}, {"adv08", "message_mentions", "phone", 1}, {"adv09", "message_mentions", "addr", 1}, {"adv10", "message_mentions", "phone", 1},
	{"adv11", "message_mentions", "addr", 1}, {"adv12", "message_about", "inv", 0}, {"adv12", "message_about", "mkt", 0}, {"adv13", "message_about", "job", 0}, {"adv14", "message_about", "trav", 0},
	{"adv15", "message_mentions", "addr", 1}, {"adv16", "message_about", "mkt", 0}, {"adv16", "message_about", "code", 1}, {"adv17", "message_about", "job", 0}, {"adv18", "message_about", "med", 0},
	{"adv19", "message_about", "inv", 1}, {"adv20", "message_about", "code", 1}, {"adv21", "message_mentions", "addr", 1}, {"adv22", "message_about", "inv", 0}, {"adv22", "message_about", "bank", 0},
	{"adv23", "message_about", "inv", 0}, {"adv23", "subject_about", "inv", 0}, {"adv24", "message_about", "pwd", 1}, {"adv25", "message_about", "inv", 0}, {"adv25", "message_about", "mkt", 0},
	{"adv26", "message_about", "inv", 0}, {"adv27", "message_about", "inv", 0}, {"adv28", "message_about", "code", 1}, {"adv28", "message_about", "pers", 0}, {"adv30", "message_mentions", "addr", 1},
	{"adv30", "message_mentions", "phone", 1}, {"adv29", "message_about", "bank", -1},
}

// eval is the run: the client, the budget it reserves from, and what it
// recorded.
type eval struct {
	t      *testing.T
	client *Client
	budget int64

	mu              sync.Mutex
	reserved, spent int64
	attempts        int
	failures        map[string]int
	cacheTouched    int
	overEstimate    int
	out             *json.Encoder
}

type record struct {
	Phase, ID  string
	Field      string
	Statements []string
	Answers    []*float64
	Refused    []bool
	Tokens     int64
	Bytes      int
	Took       float64
	Error      string `json:",omitempty"`
}

var errBudget = errors.New("the eval's budget is spent")

// ask makes a call as the service would: each attempt reserved at its
// estimate first, tried again after an AttemptError that allows it, at most
// decide.Retries times.
func (e *eval) ask(ctx context.Context, phase, id string, call decide.Call) (*decide.Result, time.Duration, error) {
	est := decide.Estimate(call)
	for attempt := 0; ; attempt++ {
		e.mu.Lock()
		if e.spent+e.reserved+est > e.budget {
			e.mu.Unlock()
			return nil, 0, errBudget
		}
		e.reserved += est
		e.attempts++
		e.mu.Unlock()

		start := time.Now()
		res, err := e.client.Decide(ctx, call)
		took := time.Since(start)
		var ae *decide.AttemptError
		charge := est
		switch {
		case err == nil && res.Usage.Reported:
			charge = decide.Cost(res.Usage)
		case errors.As(err, &ae) && !ae.Sent:
			charge = 0
		}
		rec := record{Phase: phase, ID: id, Field: map[decide.ContentKind]string{decide.ContentSubject: "subject", decide.ContentMessage: "message"}[call.Content.Kind],
			Bytes: len(decide.ContentJSON(call.Content)), Took: took.Seconds()}
		for _, a := range call.Statements {
			rec.Statements = append(rec.Statements, string(a.Var)+" "+string(a.Op)+" "+a.Topic)
		}
		e.mu.Lock()
		e.reserved -= est
		e.spent += charge
		if err != nil {
			e.failures[err.Error()]++
			rec.Error = err.Error()
		} else {
			rec.Tokens = res.Usage.InputTokens
			for _, an := range res.Answers {
				var p *float64
				if an.OK {
					p = &an.P
				}
				rec.Answers = append(rec.Answers, p)
				rec.Refused = append(rec.Refused, an.Refused)
			}
			if res.Usage.CachedTokens != 0 || res.Usage.CacheWriteTokens != 0 {
				e.cacheTouched++
			}
			if !res.Usage.Reported || decide.Cost(res.Usage) > est {
				e.overEstimate++
			}
		}
		if e.out != nil {
			_ = e.out.Encode(rec)
		}
		e.mu.Unlock()

		if err == nil || !errors.As(err, &ae) || !ae.Retry || attempt >= decide.Retries {
			return res, took, err
		}
		wait := max(ae.RetryAfter, time.Duration(200+rand.IntN(800))*time.Millisecond)
		select {
		case <-ctx.Done():
			return nil, took, err
		case <-time.After(wait):
		}
	}
}

// each runs f for every job, n at once.
func each[T any](n int, jobs []T, f func(T)) {
	sem := make(chan struct{}, n)
	var wg sync.WaitGroup
	for _, j := range jobs {
		sem <- struct{}{}
		wg.Go(func() {
			defer func() { <-sem }()
			f(j)
		})
	}
	wg.Wait()
}

func quantile(xs []float64, q float64) float64 {
	if len(xs) == 0 {
		return math.NaN()
	}
	s := slices.Clone(xs)
	slices.Sort(s)
	return s[min(len(s)-1, int(q*float64(len(s))))]
}

type scored struct {
	tp, fp, positives, ruleMisses, overHolding, missing, scored int
}

func (s *scored) add(label bool, p float64, ok bool) {
	s.scored++
	if label {
		s.positives++
	}
	if !ok {
		s.missing++
		return
	}
	switch {
	case label && p >= 0.80:
		s.tp++
	case !label && p >= 0.80:
		s.fp++
	}
	if label && p < 0.30 {
		s.ruleMisses++
	}
	if !label && p >= 0.30 {
		s.overHolding++
	}
}

func (s scored) precision() float64 { return float64(s.tp) / float64(max(s.tp+s.fp, 1)) }
func (s scored) recall() float64    { return float64(s.tp) / float64(max(s.positives, 1)) }

// answersOf asks a message's atoms as the enclave would (Plan) and gives
// each atom's probability, or false when it has none.
func (e *eval) answersOf(ctx context.Context, phase string, m corpusMessage, atoms []query.Atom) (map[query.Atom]float64, []float64) {
	calls, byRule := decide.Plan(atoms, m.Subject, m.Body, true)
	out := map[query.Atom]float64{}
	for a, p := range byRule {
		out[a] = p
	}
	var took []float64
	for _, c := range calls {
		res, d, err := e.ask(ctx, phase, m.ID, c)
		if err != nil {
			if errors.Is(err, errBudget) {
				e.t.Fatal(err)
			}
			continue
		}
		took = append(took, d.Seconds())
		for i, a := range c.Statements {
			if res.Answers[i].OK {
				out[a] = res.Answers[i].P
			}
		}
	}
	return out, took
}

func TestA10Eval(t *testing.T) {
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		t.Skip("OPENAI_API_KEY is not set")
	}
	budget := 2.5
	if s := os.Getenv("PERMAILY_EVAL_BUDGET_USD"); s != "" {
		var err error
		if budget, err = strconv.ParseFloat(s, 64); err != nil {
			t.Fatal(err)
		}
	}
	org, project := os.Getenv("PERMAILY_EVAL_ORG"), os.Getenv("PERMAILY_EVAL_PROJECT")
	hc := &http.Client{Transport: http.DefaultTransport.(*http.Transport).Clone()}
	e := &eval{t: t, client: New(hc, func(context.Context) (string, bool) { return apiKey, true }, org, project), budget: int64(budget * 1e9), failures: map[string]int{}}
	report := map[string]any{"prompt_version": decide.PromptVersion, "model": Model, "pinned_org": org != "", "pinned_project": project != ""}
	if dir := os.Getenv("PERMAILY_EVAL_OUT"); dir != "" {
		f, err := os.Create(filepath.Join(dir, "answers.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		e.out = json.NewEncoder(f)
		defer func() {
			data, _ := json.MarshalIndent(report, "", " ")
			_ = os.WriteFile(filepath.Join(dir, "report.json"), data, 0o600)
		}()
	}
	ctx := t.Context()
	topics := readTopicsPinned(t)
	topicKeys := slices.Sorted(func(yield func(string) bool) {
		for k := range topics {
			if !yield(k) {
				return
			}
		}
	})
	allAtoms := func() []query.Atom {
		var atoms []query.Atom
		for _, k := range topicKeys {
			for _, kind := range kinds {
				atoms = append(atoms, atomOf(kind, topics[k]))
			}
		}
		return atoms
	}

	// The corpus, whole text, as the enclave plans it.
	corpus := readJSONL[corpusMessage](t, "corpus.jsonl")
	corpusAnswers := map[string]map[query.Atom]float64{}
	var corpusTook []float64
	var mu sync.Mutex
	each(decide.Concurrent, corpus, func(m corpusMessage) {
		ans, took := e.answersOf(ctx, "corpus", m, allAtoms())
		mu.Lock()
		corpusAnswers[m.ID] = ans
		corpusTook = append(corpusTook, took...)
		mu.Unlock()
	})
	thresholds := []float64{0.10, 0.20, 0.30, 0.50, 0.70, 0.80, 0.90}
	type row struct{ TP, FP, Misses int }
	table := make([]row, len(thresholds))
	var all scored
	byKind := map[string]*scored{}
	var spanish, facturas, address scored
	for _, m := range corpus {
		for _, k := range topicKeys {
			for _, kind := range kinds {
				label := m.Labels[k][kind]
				if label == nil {
					continue
				}
				p, ok := corpusAnswers[m.ID][atomOf(kind, topics[k])]
				all.add(*label, p, ok)
				if byKind[kind] == nil {
					byKind[kind] = &scored{}
				}
				byKind[kind].add(*label, p, ok)
				if m.Lang == "es" {
					spanish.add(*label, p, ok)
				}
				if k == "fact" {
					facturas.add(*label, p, ok)
				}
				if k == "addr" {
					address.add(*label, p, ok)
				}
				for i, th := range thresholds {
					switch {
					case ok && p >= th && *label:
						table[i].TP++
					case ok && p >= th:
						table[i].FP++
					case *label:
						table[i].Misses++
					}
				}
			}
		}
	}
	report["corpus"] = map[string]any{
		"scored": all.scored, "positives": all.positives, "missing": all.missing,
		"query_precision": all.precision(), "query_recall": all.recall(), "false_deliveries": all.fp,
		"rule_misses": all.ruleMisses, "rule_over_holding": all.overHolding,
		"thresholds": thresholds, "by_threshold": table,
		"by_kind": map[string]any{
			"subject_about":    []any{byKind["subject_about"].precision(), byKind["subject_about"].recall(), byKind["subject_about"].ruleMisses},
			"message_about":    []any{byKind["message_about"].precision(), byKind["message_about"].recall(), byKind["message_about"].ruleMisses},
			"message_mentions": []any{byKind["message_mentions"].precision(), byKind["message_mentions"].recall(), byKind["message_mentions"].ruleMisses},
		},
		"spanish":   []any{spanish.precision(), spanish.recall()},
		"facturas":  []any{facturas.precision(), facturas.recall(), facturas.ruleMisses},
		"address":   []any{address.precision()},
		"latency_s": []float64{quantile(corpusTook, 0.5), quantile(corpusTook, 0.95), quantile(corpusTook, 0.99), quantile(corpusTook, 1)},
	}
	if all.precision() < 0.95 || all.recall() < 0.95 || all.ruleMisses > 6 {
		t.Errorf("corpus: precision %.3f, recall %.3f, %d rule misses of %d; A10 wants 0.95, 0.95 and at most 6", all.precision(), all.recall(), all.ruleMisses, all.positives)
	}

	// The adversarial targets.
	var crossed []string
	for _, a := range adversarialTargets {
		p, ok := corpusAnswers[a.id][atomOf(a.kind, topics[a.topic])]
		if a.truth >= 0 && ok && ((a.truth == 0 && p >= 0.80) || (a.truth == 1 && p < 0.30)) {
			crossed = append(crossed, fmt.Sprintf("%s %s %s %.2f", a.id, a.kind, a.topic, p))
		}
	}
	report["adversarial_crossed"] = crossed
	if len(crossed) > 0 {
		t.Errorf("adversarial targets across their threshold: %v", crossed)
	}

	// The adaptive set.
	var adaptive scored
	each(decide.Concurrent, readJSONL[corpusMessage](t, "adaptive.jsonl"), func(m corpusMessage) {
		ans, _ := e.answersOf(ctx, "adaptive", m, allAtoms())
		mu.Lock()
		defer mu.Unlock()
		for _, k := range topicKeys {
			for _, kind := range kinds {
				if label := m.Labels[k][kind]; label != nil {
					p, ok := ans[atomOf(kind, topics[k])]
					adaptive.add(*label, p, ok)
				}
			}
		}
	})
	report["adaptive"] = map[string]any{"scored": adaptive.scored, "rule_misses": adaptive.ruleMisses, "false_deliveries": adaptive.fp, "missing": adaptive.missing}
	if adaptive.ruleMisses > 0 {
		t.Errorf("adaptive: %d rule misses", adaptive.ruleMisses)
	}

	// The hostile topics: each call, and each of its atoms alone.
	byID := map[string]corpusMessage{}
	for _, m := range corpus {
		byID[m.ID] = m
	}
	var moved, hostileCrossed []string
	maxShift := 0.0
	targets := 0
	each(4, readJSONL[hostileCase](t, "hostile.jsonl"), func(c hostileCase) {
		m := byID[c.Message]
		var atoms []query.Atom
		for _, a := range c.Atoms {
			atoms = append(atoms, atomOf(a.Kind, a.Topic))
		}
		content := decide.Content{Kind: decide.KindOf(atoms[0]), Subject: m.Subject}
		if content.Kind == decide.ContentMessage {
			content.Body = m.Body
		}
		batch, _, err := e.ask(ctx, "hostile", c.ID, decide.Call{Content: content, Statements: atoms})
		if err != nil {
			t.Errorf("%s: %v", c.ID, err)
			return
		}
		for i, a := range c.Atoms {
			if a.Role != "target" {
				continue
			}
			alone, _, err := e.ask(ctx, "hostile-alone", c.ID, decide.Call{Content: content, Statements: atoms[i : i+1]})
			if err != nil {
				t.Errorf("%s alone: %v", c.ID, err)
				continue
			}
			pb, pa := batch.Answers[i], alone.Answers[0]
			mu.Lock()
			targets++
			if !pb.OK || !pa.OK {
				moved = append(moved, c.ID+" "+a.Kind+" "+a.Core+": no answer")
			} else {
				shift := math.Abs(pb.P - pa.P)
				maxShift = max(maxShift, shift)
				if shift > 0.02 || query.Classify(pb.P, query.ForQuery) != query.Classify(pa.P, query.ForQuery) || query.Classify(pb.P, query.ForRule) != query.Classify(pa.P, query.ForRule) {
					moved = append(moved, fmt.Sprintf("%s %s %s: %.2f alone, %.2f with a hostile topic", c.ID, a.Kind, a.Core, pa.P, pb.P))
				}
				if a.Label != nil && ((*a.Label && pb.P < 0.30) || (!*a.Label && pb.P >= 0.80)) {
					hostileCrossed = append(hostileCrossed, fmt.Sprintf("%s %s %s %.2f", c.ID, a.Kind, a.Core, pb.P))
				}
			}
			mu.Unlock()
		}
	})
	report["hostile"] = map[string]any{"targets": targets, "moved": moved, "max_shift": maxShift, "crossed": hostileCrossed}
	if len(moved) > 0 || len(hostileCrossed) > 0 {
		t.Errorf("hostile topics: moved %v, across their threshold %v", moved, hostileCrossed)
	}

	// The maximum-size requests, three times each, within LongCallTimeout.
	type sized struct {
		id   string
		call decide.Call
		kb   int
	}
	var long []sized
	maxLabels := map[string][]bool{}
	for _, c := range readMaxSize(t) {
		call := decide.Call{Content: decide.Content{Kind: decide.ContentMessage, Subject: c.Subject, Body: c.Body}}
		for _, a := range c.Atoms {
			call.Statements = append(call.Statements, atomOf(a[0].(string), topics[a[1].(string)]))
			maxLabels[c.ID] = append(maxLabels[c.ID], a[2].(bool))
		}
		for range 3 {
			long = append(long, sized{c.ID, call, 256})
		}
		for _, kb := range []int{64, 128} {
			body := c.Body[len(c.Body)-kb<<10:]
			for len(body) > 0 && !utf8.RuneStart(body[0]) {
				body = body[1:]
			}
			short := call
			short.Content.Body = body
			for range 3 {
				long = append(long, sized{c.ID, short, kb})
			}
		}
	}
	longTook := map[int][]float64{}
	maxAnswers := map[string][][]decide.Answer{}
	var maxWrong []string
	each(4, long, func(s sized) {
		res, took, err := e.ask(ctx, fmt.Sprintf("long-%d", s.kb), s.id, s.call)
		mu.Lock()
		defer mu.Unlock()
		if err != nil {
			t.Errorf("%s at %d KiB: %v", s.id, s.kb, err)
			return
		}
		longTook[s.kb] = append(longTook[s.kb], took.Seconds())
		if s.kb == 256 {
			maxAnswers[s.id] = append(maxAnswers[s.id], res.Answers)
			for i, an := range res.Answers {
				if label := maxLabels[s.id][i]; !an.OK || (label && an.P < 0.80) || (!label && an.P > 0.20) {
					maxWrong = append(maxWrong, fmt.Sprintf("%s s%d %.2f", s.id, i, an.P))
				}
			}
		}
	})
	sameMax := true
	for id, runs := range maxAnswers {
		for _, r := range runs[1:] {
			if !slices.Equal(r, runs[0]) {
				sameMax = false
				t.Errorf("%s: the repetitions differ", id)
			}
		}
	}
	longReport := map[string]any{}
	worstP99 := 0.0
	for kb, xs := range longTook {
		p99 := quantile(xs, 0.99)
		worstP99 = max(worstP99, p99)
		longReport[strconv.Itoa(kb)+"KiB"] = map[string]any{"n": len(xs), "p50": quantile(xs, 0.5), "p99": p99, "max": quantile(xs, 1)}
	}
	longReport["suggested_long_call_timeout_s"] = math.Ceil(5 * worstP99)
	longReport["long_call_timeout_s"] = decide.LongCallTimeout.Seconds()
	report["long"] = longReport
	report["maxsize"] = map[string]any{"wrong": maxWrong, "repetitions_identical": sameMax}
	if len(maxWrong) > 0 {
		t.Errorf("maximum-size requests answered wrong: %v", maxWrong)
	}

	// Three repetitions of 40 planned calls of the corpus.
	type planned struct {
		id   string
		call decide.Call
	}
	var pool []planned
	for _, m := range corpus {
		calls, _ := decide.Plan(allAtoms(), m.Subject, m.Body, true)
		for _, c := range calls {
			pool = append(pool, planned{m.ID, c})
		}
	}
	rng := rand.New(rand.NewPCG(28, 28))
	rng.Shuffle(len(pool), func(i, j int) { pool[i], pool[j] = pool[j], pool[i] })
	sample := pool[:40]
	reps := make([][][]decide.Answer, len(sample))
	var jobs []int
	for range 3 {
		for i := range sample {
			jobs = append(jobs, i)
		}
	}
	each(decide.Concurrent, jobs, func(i int) {
		res, _, err := e.ask(ctx, "repeat", sample[i].id, sample[i].call)
		if err != nil {
			t.Errorf("repeat %s: %v", sample[i].id, err)
			return
		}
		mu.Lock()
		reps[i] = append(reps[i], res.Answers)
		mu.Unlock()
	})
	differ, againstCorpus, statements := 0, 0, 0
	for i, runs := range reps {
		for _, r := range runs[1:] {
			if !slices.Equal(r, runs[0]) {
				differ++
			}
		}
		for j, a := range sample[i].call.Statements {
			statements++
			if p, ok := corpusAnswers[sample[i].id][a]; len(runs) > 0 && (!ok || !runs[0][j].OK || runs[0][j].P != p) {
				againstCorpus++
			}
		}
	}
	report["repeat"] = map[string]any{"requests": len(sample), "statements": statements, "requests_differing": differ, "statements_differing_from_corpus": againstCorpus}
	if differ > 0 {
		t.Errorf("repeat: %d requests differ between repetitions", differ)
	}

	// Latency of calls of 1 to 8 statements, 16 at once.
	type shaped struct {
		m corpusMessage
		n int
	}
	var shapes []shaped
	for n := 1; n <= 8; n++ {
		for i := range 20 {
			shapes = append(shapes, shaped{corpus[(n*20+i)%len(corpus)], n})
		}
	}
	var small []float64
	each(decide.Concurrent, shapes, func(s shaped) {
		atoms := allAtoms()
		var picked []query.Atom
		for _, a := range atoms {
			if a.Var == contracts.VarMessage && len(picked) < s.n {
				picked = append(picked, a)
			}
		}
		_, took, err := e.ask(ctx, "latency", s.m.ID, decide.Call{Content: decide.Content{Kind: decide.ContentMessage, Subject: s.m.Subject, Body: s.m.Body}, Statements: picked})
		if err != nil {
			t.Errorf("latency: %v", err)
			return
		}
		mu.Lock()
		small = append(small, took.Seconds())
		mu.Unlock()
	})
	report["latency_1_to_8"] = map[string]any{"n": len(small), "p50": quantile(small, 0.5), "p95": quantile(small, 0.95), "p99": quantile(small, 0.99), "max": quantile(small, 1)}
	if p99 := quantile(small, 0.99); p99 >= 1 {
		t.Errorf("p99 of 1 to 8 statements is %.2f s", p99)
	}

	// The canary, with the enclave's call shapes, against what was measured.
	canary, err := decide.LoadCanary()
	if err != nil {
		t.Fatal(err)
	}
	var canaryMoved []string
	for _, cm := range canary.Messages {
		var atoms []query.Atom
		for _, a := range cm.Atoms {
			atoms = append(atoms, a.Atom())
		}
		ans, _ := e.answersOf(ctx, "canary", corpusMessage{ID: cm.ID, Subject: cm.Subject, Body: cm.Body}, atoms)
		for _, a := range cm.Atoms {
			if p, ok := ans[a.Atom()]; !ok || a.Moved(p, canary.Tolerance) {
				canaryMoved = append(canaryMoved, fmt.Sprintf("%s %s %s %s: %.2f, measured %.2f", cm.ID, a.Var, a.Op, a.Topic, p, a.P))
			}
		}
	}
	report["canary_moved"] = canaryMoved
	if len(canaryMoved) > 0 {
		t.Errorf("the canary moved: %v", canaryMoved)
	}

	// Choice 10: a key under a project it does not belong to.
	if other := os.Getenv("PERMAILY_EVAL_OTHER_PROJECT"); other != "" {
		c := New(hc, func(context.Context) (string, bool) { return apiKey, true }, org, other)
		probe := decide.Call{Content: decide.Content{Kind: decide.ContentSubject, Subject: "Invoice 12"}, Statements: []query.Atom{atomOf("subject_about", "invoices")}}
		_, err := c.Decide(ctx, probe)
		report["other_project"] = cmp.Or(fmt.Sprint(err), "answered")
		if !errors.Is(err, decide.ErrKeyRefused) {
			t.Errorf("a key under another project's header: %v, want it refused", err)
		}
	} else {
		report["other_project"] = "not checked: PERMAILY_EVAL_OTHER_PROJECT is not set"
	}

	e.mu.Lock()
	report["attempts"], report["failures"], report["spent_usd"] = e.attempts, e.failures, float64(e.spent)/1e9
	report["calls_with_cached_tokens"], report["calls_over_their_estimate"] = e.cacheTouched, e.overEstimate
	e.mu.Unlock()
	if e.cacheTouched > 0 {
		t.Errorf("%d calls read or wrote cached tokens", e.cacheTouched)
	}
	if e.overEstimate > 0 {
		t.Errorf("%d calls cost more than their estimate or reported no usage", e.overEstimate)
	}
	data, _ := json.MarshalIndent(report, "", " ")
	t.Logf("report:\n%s", data)
}
