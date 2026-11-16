package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/decide"
	"github.com/permaily-org/permaily/internal/decide/decidetest"
	"github.com/permaily-org/permaily/internal/decide/openai"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/keys"
	"github.com/permaily-org/permaily/internal/query"
	"github.com/permaily-org/permaily/internal/services/accounts"
	"github.com/permaily-org/permaily/internal/services/decisions"
	"github.com/permaily-org/permaily/internal/store"
	"github.com/permaily-org/permaily/pkg/attestation"
)

// modelWorld is one deployment's storage, keys and played provider, and
// the enclaves composed over them as the image composes them.
type modelWorld struct {
	t        *testing.T
	mem      *store.Memory
	commits  *store.MemoryCommits
	keys     *keys.Local
	tab      *decidetest.Table
	provider *decidetest.Provider
	outbox   *accounts.Outbox
	logs     *lockedLog
	at       atomic.Int64 // the clock, in Unix nanoseconds
}

type lockedLog struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

func newModelWorld(t *testing.T) *modelWorld {
	t.Helper()
	local, err := keys.NewLocal()
	if err != nil {
		t.Fatal(err)
	}
	w := &modelWorld{t: t, mem: store.NewMemory(), commits: store.NewMemoryCommits(), keys: local, tab: devCanaryTable(), outbox: &accounts.Outbox{}, logs: &lockedLog{}}
	w.at.Store(time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC).UnixNano())
	w.mem.Now = w.now
	w.provider = decidetest.NewProvider(t, w.tab)
	return w
}

// devCanaryTable is a table that answers the canary as it was measured.
func devCanaryTable() *decidetest.Table {
	tab := decidetest.NewTable()
	c, err := decide.LoadCanary()
	if err != nil {
		panic(err)
	}
	for _, m := range c.Messages {
		for _, a := range m.Atoms {
			tab.SetFor(m.Subject, m.Body, a.Atom(), a.P)
		}
	}
	return tab
}

func (w *modelWorld) now() time.Time          { return time.Unix(0, w.at.Load()).UTC() }
func (w *modelWorld) advance(d time.Duration) { w.at.Add(int64(d)) }

var e2eModel = modelConfig{organization: "org-a34", project: "proj-a34"}

// enclave is one enclave of the deployment: the real client over the played
// provider, its own strict Anchored over the shared storage.
func (w *modelWorld) enclave() (*decisions.Service, *store.Anchored) {
	w.t.Helper()
	records := &store.Anchored{Records: w.mem, Commits: w.commits, OnlyNewest: true, Strict: true, Now: w.now}
	svc, err := composeModel(modelParts{
		cfg: e2eModel, egress: w.provider.Client(), records: records, entries: w.mem, keys: w.keys,
		mailer: w.outbox, superAdmin: "admin@permaily.test", now: w.now,
		log: slog.New(slog.NewJSONHandler(w.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
	})
	if err != nil {
		w.t.Fatal(err)
	}
	return svc, records
}

// ask asks svc about a fresh message whose subject's atom is the topic.
func (w *modelWorld) ask(svc *decisions.Service, topic string) *inbox.Asked {
	w.t.Helper()
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	id := fmt.Sprintf("msg_%d", w.at.Load())
	w.advance(time.Millisecond)
	q := &inbox.Question{
		Box: inbox.Box{MailboxID: "mbx_a34", Key: key}, MessageID: id, ExpiresAt: w.now().Add(24 * time.Hour), Account: "acc_a34",
		Atoms:   []query.Atom{{Var: contracts.VarSubject, Op: contracts.OpAbout, Topic: topic}},
		Subject: "A subject " + id, View: inbox.ViewOriginal,
		Text: func(context.Context) (string, inbox.TextState, error) { return "", inbox.TextRead, nil },
	}
	out, err := svc.Ask(context.Background(), q)
	if err != nil {
		w.t.Fatal(err)
	}
	return out
}

// sentWith are the keys the played provider's requests carried, from the
// n-th on.
func (w *modelWorld) sentWith(from int) []string {
	var out []string
	for _, r := range w.provider.Requests()[from:] {
		out = append(out, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
	}
	return out
}

// A34. The composition: the enclave asks through openai.New, built by
// NewModel with the egress client and the service's credential; the model's
// records go through one strict Anchored. With the real client and two
// services over one store and one played provider that checks the key: a
// key set through one is used by both; replaced through one, the other
// sends the new one within keyFresh and the old one never after; removed,
// neither sends a request after keyFresh; the record lost, neither sends
// one; no /status field or log line carries the key.
func TestA34TheComposedModelUsesTheKeyOfEitherEnclave(t *testing.T) {
	w := newModelWorld(t)
	a, _ := w.enclave()
	b, _ := w.enclave()
	ctx := context.Background()

	if got := w.ask(a, "invoices"); got.Outcome != inbox.AskOff || len(w.provider.Requests()) != 0 {
		t.Fatalf("no key: %+v, %d requests", got, len(w.provider.Requests()))
	}

	const k1, k2, k3 = "pm-a34-first-key-1c2d", "pm-a34-second-key-3e4f", "pm-a34-third-key-5a6b"
	w.tab.RequireKey(k1)
	if _, err := a.SetKey(ctx, contracts.ModelKeySet{Key: k1}); err != nil {
		t.Fatal(err)
	}
	for name, svc := range map[string]*decisions.Service{"a": a, "b": b} {
		if got := w.ask(svc, "invoices"); got.Outcome != inbox.AskAnswered {
			t.Fatalf("%s with the key set through a: %+v", name, got)
		}
	}
	if got := w.sentWith(0); !slices.Equal(got, []string{k1, k1}) {
		t.Fatalf("the keys sent: %q", got)
	}
	for _, r := range w.provider.Requests() {
		if r.Proto != "HTTP/2.0" || r.Host != decidetest.Host || r.Path != "/v1/decisions" || r.Header.Get("OpenAI-Organization") != "org-a34" || r.Header.Get("OpenAI-Project") != "proj-a34" {
			t.Fatalf("a request: %s %s%s %v", r.Proto, r.Host, r.Path, r.Header)
		}
	}

	// Replaced through a: a sends the new key at once; b, once what it read
	// is older than a minute, and never the old one after.
	w.tab.RequireKey(k2)
	if _, err := a.SetKey(ctx, contracts.ModelKeySet{Key: k2}); err != nil {
		t.Fatal(err)
	}
	if got := w.ask(a, "invoices"); got.Outcome != inbox.AskAnswered {
		t.Fatalf("a with the replaced key: %+v", got)
	}
	w.advance(time.Minute + time.Second)
	from := len(w.provider.Requests())
	for range 2 {
		if got := w.ask(b, "invoices"); got.Outcome != inbox.AskAnswered {
			t.Fatalf("b after the key was replaced: %+v", got)
		}
	}
	if got := w.sentWith(from); !slices.Equal(got, []string{k2, k2}) {
		t.Fatalf("b sent %q after the key was replaced", got)
	}

	// Removed through b: neither sends a request once a minute has passed.
	if err := b.RemoveKey(ctx); err != nil {
		t.Fatal(err)
	}
	w.advance(time.Minute + time.Second)
	from = len(w.provider.Requests())
	for name, svc := range map[string]*decisions.Service{"a": a, "b": b} {
		if got := w.ask(svc, "invoices"); got.Outcome != inbox.AskOff {
			t.Fatalf("%s after the key was removed: %+v", name, got)
		}
	}
	if n := len(w.provider.Requests()); n != from {
		t.Fatalf("%d requests after the key was removed", n-from)
	}

	// The key's record lost (its value deleted from the table): no key.
	w.tab.RequireKey(k3)
	if _, err := a.SetKey(ctx, contracts.ModelKeySet{Key: k3}); err != nil {
		t.Fatal(err)
	}
	names, err := w.mem.ListRecords(ctx, decisions.RecordKey, 100)
	if err != nil || len(names) == 0 {
		t.Fatalf("the key's record: %v %v", names, err)
	}
	for _, name := range names {
		if err := w.mem.DeleteRecord(ctx, name); err != nil {
			t.Fatal(err)
		}
	}
	w.advance(time.Minute + time.Second)
	from = len(w.provider.Requests())
	for name, svc := range map[string]*decisions.Service{"a": a, "b": b} {
		if got := w.ask(svc, "invoices"); got.Outcome != inbox.AskOff {
			t.Fatalf("%s with the key's record lost: %+v", name, got)
		}
	}
	if n := len(w.provider.Requests()); n != from {
		t.Fatalf("%d requests with the key's record lost", n-from)
	}

	// Nothing the enclave says carries a key or its fingerprint.
	st, err := json.Marshal(modelStatus(a, e2eModel)(ctx))
	if err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{k1, k2, k3} {
		sum := sha256.Sum256([]byte(k))
		for _, secret := range []string{k, hex.EncodeToString(sum[:])[:16]} {
			if bytes.Contains(st, []byte(secret)) || strings.Contains(w.logs.String(), secret) {
				t.Fatalf("/status or the log holds %q", secret)
			}
		}
	}
}

// A34. The canary and the alarms run on their own, on their tickers: the
// key is given and the canary's answer moves only once both loops are past
// the run they make when they start, so only a tick runs the canary and
// sends its alarm, by mail to the super admin. The test never calls
// RunCanary or RunAlarms.
func TestA34TheCanaryAndTheAlarmsRunOnTheirTickers(t *testing.T) {
	w := newModelWorld(t)
	svc, _ := w.enclave()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	canaryTick, alarmTick := make(chan time.Time), make(chan time.Time)
	runModel(ctx, svc, slog.New(slog.NewJSONHandler(w.logs, nil)), modelTicks{canary: canaryTick, alarms: alarmTick})
	tick(t, "the alarms' loop", alarmTick)
	tick(t, "the canary's loop", canaryTick)
	const key = "pm-a34-canary-key-7d8e"
	w.tab.RequireKey(key)
	if _, err := svc.SetKey(ctx, contracts.ModelKeySet{Key: key}); err != nil {
		t.Fatal(err)
	}
	// The canary sees one answer move: its alarm is due.
	c, err := decide.LoadCanary()
	if err != nil {
		t.Fatal(err)
	}
	m := c.Messages[0]
	w.tab.SetFor(m.Subject, m.Body, m.Atoms[0].Atom(), 1-m.Atoms[0].P)
	// Without a key the check did not complete: it is tried again once its
	// lease is over.
	w.advance(decisions.CanaryLease + time.Second)
	tick(t, "the canary's loop", canaryTick)
	waitUntil(t, "the canary did not run on its ticker", func() bool {
		st := svc.Status(ctx)
		return st.Canary != nil && st.Canary.State == attestation.CanaryChanged
	})
	tick(t, "the alarms' loop", alarmTick)
	waitUntil(t, "the alarm was not sent on its ticker", func() bool { return len(w.outbox.Sent()) > 0 })
	sent := w.outbox.Sent()
	if len(sent) != 1 || sent[0].To != "admin@permaily.test" || sent[0].Subject != "Permaily: the model's canary saw an answer move" {
		t.Fatalf("the alarm: %+v", sent)
	}
	if n := len(w.provider.Requests()); n != 8 {
		t.Fatalf("the canary is 8 calls, sent %d", n)
	}
}

// tick gives a loop of runModel its next tick, and fails when the loop
// does not take it. Once it is taken, the loop's run when it started is
// over: what it does after comes from a tick.
func tick(t *testing.T, loop string, c chan<- time.Time) {
	t.Helper()
	select {
	case c <- time.Now():
	case <-time.After(10 * time.Second):
		t.Fatalf("%s does not run: it takes no tick", loop)
	}
}

// waitUntil waits for ok, and fails with why after 10 seconds.
func waitUntil(t *testing.T, why string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); !ok(); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal(why)
		}
	}
}

func TestEveryRunsNowAndOnItsTicker(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var n atomic.Int32
	done := make(chan struct{})
	go func() {
		every(ctx, time.Millisecond, nil, func() {
			if n.Add(1) == 3 {
				cancel()
			}
		})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("every did not stop with its context")
	}
	if n.Load() < 3 {
		t.Fatalf("ran %d times", n.Load())
	}
	// With ticks of its own, it runs once when it starts and once a tick.
	ctx, cancel = context.WithCancel(context.Background())
	ticks, ran := make(chan time.Time), make(chan struct{}, 3)
	done = make(chan struct{})
	go func() {
		every(ctx, time.Hour, ticks, func() { ran <- struct{}{} })
		close(done)
	}()
	<-ran
	tick(t, "every's loop", ticks)
	<-ran
	cancel()
	<-done
}

// enclaveJSON is deploy/enclave.json's model section and staging's.
type enclaveJSON struct {
	Model   map[string]any `json:"model"`
	Staging struct {
		Model map[string]any `json:"model"`
	} `json:"staging"`
}

func readEnclaveJSON(t *testing.T) enclaveJSON {
	t.Helper()
	data, err := os.ReadFile("../../deploy/enclave.json")
	if err != nil {
		t.Fatal(err)
	}
	var e enclaveJSON
	if err := json.Unmarshal(data, &e); err != nil {
		t.Fatal(err)
	}
	return e
}

// modelArgs are the entry point's arguments for a model section, as
// nix/eif.nix writes them: one per line, an empty value left out with its
// flag, the boolean in one argument.
func modelArgs(t *testing.T, m map[string]any) []string {
	t.Helper()
	str := func(k string) string {
		v, ok := m[k].(string)
		if !ok {
			t.Fatalf("model.%s is not a string", k)
		}
		return v
	}
	num := func(k string) string {
		v, ok := m[k].(float64)
		if !ok || v <= 0 {
			t.Fatalf("model.%s is not an amount", k)
		}
		if v == float64(int64(v)) {
			return fmt.Sprint(int64(v))
		}
		return fmt.Sprintf("%f", v) // Nix's toString of a float
	}
	zdr, ok := m["zero_data_retention"].(bool)
	if !ok {
		t.Fatal("model.zero_data_retention is not a boolean")
	}
	var args []string
	for _, f := range [][2]string{{"-model-organization", str("organization")}, {"-model-project", str("project")}} {
		if f[1] != "" {
			args = append(args, f[0], f[1])
		}
	}
	return append(args, fmt.Sprintf("-model-zero-data-retention=%t", zdr),
		"-model-month-limit-usd", num("month_limit_usd"), "-model-day-alarm-usd", num("day_alarm_usd"), "-model-account-day-usd", num("account_day_usd"))
}

func parseModel(t *testing.T, args []string) modelConfig {
	t.Helper()
	fs := flag.NewFlagSet("permaily-enclave", flag.ContinueOnError)
	var m modelConfig
	modelFlags(fs, &m)
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		t.Fatalf("%q: %v", args, err)
	}
	return m
}

// The image's model section: the owner's figures, zero data retention not
// approved, and the organisation and project, which the owner fills before
// the release (empty pins none). Staging lowers the day's alarm and the
// month's limit, and changes nothing else.
func TestTheImageModelSection(t *testing.T) {
	e := readEnclaveJSON(t)
	prod := parseModel(t, modelArgs(t, e.Model))
	if prod.limits != decisions.DefaultLimits || prod.zeroDataRetention {
		t.Fatalf("production: %+v", prod)
	}
	merged := map[string]any{}
	for k, v := range e.Model {
		merged[k] = v
	}
	for k, v := range e.Staging.Model {
		if k != "month_limit_usd" && k != "day_alarm_usd" {
			t.Errorf("staging changes model.%s", k)
		}
		merged[k] = v
	}
	staging := parseModel(t, modelArgs(t, merged))
	if staging.limits.Month >= prod.limits.Month || staging.limits.DayAlarm >= prod.limits.DayAlarm || staging.limits.DayAlarm > staging.limits.Month ||
		staging.limits.AccountDay != prod.limits.AccountDay || staging.limits.Block != prod.limits.Block || staging.organization != prod.organization || staging.project != prod.project {
		t.Fatalf("staging: %+v, production %+v", staging, prod)
	}
	// Every amount Nix may write reads back.
	for in, want := range map[string]int64{"50": 50_000_000_000, "0.500000": 500_000_000, "2.5": 2_500_000_000} {
		var got int64
		if err := usdFlag(&got)(in); err != nil || got != want {
			t.Errorf("%q: %d %v", in, got, err)
		}
	}
	for _, in := range []string{"", "0", "-1", "NaN", "Inf", "1e7", "ten"} {
		var got int64
		if err := usdFlag(&got)(in); err == nil {
			t.Errorf("%q read as %d", in, got)
		}
	}
}

// A32. /status's model section, golden: what the image compiles in, the
// limits of deploy/enclave.json, the canary's baseline, every kind of call
// at zero, and no key, fingerprint or amount spent.
func TestA32TheModelOnStatus(t *testing.T) {
	w := newModelWorld(t)
	svc, _ := w.enclave()
	ctx := context.Background()
	cfg := parseModel(t, modelArgs(t, readEnclaveJSON(t).Model))
	c, err := decide.LoadCanary()
	if err != nil {
		t.Fatal(err)
	}
	golden := func(st *attestation.ModelStatus, keySet string) string {
		calls := make([]string, 0, len(attestation.ModelCalls))
		for _, k := range attestation.ModelCalls {
			calls = append(calls, fmt.Sprintf("%q:0", k))
		}
		slices.Sort(calls)
		pinned := ""
		if cfg.organization != "" {
			pinned += `"organization":"` + cfg.organization + `",`
		}
		if cfg.project != "" {
			pinned += `"project":"` + cfg.project + `",`
		}
		return `{"provider":"openai","endpoint":"https://api.openai.com/v1/decisions","model":"gpt-6-luna","prompt_version":"` + decide.PromptVersion + `",` +
			`"prompt_cache":"off",` + pinned + `"zero_data_retention":false,"month_limit_usd":50,"day_alarm_usd":10,"account_day_usd":0.5,` + keySet +
			`"canary":{"baseline":"` + c.Baseline + `","state":"none"},"since":"` + st.Since.Format(time.RFC3339Nano) + `","calls":{` + strings.Join(calls, ",") + `}}`
	}
	st := modelStatus(svc, cfg)(ctx)
	got, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	if want := golden(st, `"key_set":false,"guard":"key_refused",`); string(got) != want {
		t.Fatalf("/status's model:\n got %s\nwant %s", got, want)
	}
	if openai.Endpoint != st.Endpoint || st.Model != openai.Model {
		t.Fatalf("the compiled address and model: %+v", st)
	}
	// With a key: when it was set, never the key or its fingerprint.
	const key = "pm-a32-status-key-9f0a"
	if _, err := svc.SetKey(ctx, contracts.ModelKeySet{Key: key}); err != nil {
		t.Fatal(err)
	}
	st = modelStatus(svc, cfg)(ctx)
	got, _ = json.Marshal(st)
	sum := sha256.Sum256([]byte(key))
	if want := golden(st, `"key_set":true,"key_set_at":"`+st.KeySetAt.Format(time.RFC3339Nano)+`","guard":"ok",`); string(got) != want || bytes.Contains(got, []byte(key)) || bytes.Contains(got, []byte(hex.EncodeToString(sum[:])[:16])) {
		t.Fatalf("/status's model with a key:\n got %s\nwant %s", got, want)
	}
	// The organisation and project, when the image pins them.
	st = modelStatus(svc, e2eModel)(ctx)
	if st.Organization != "org-a34" || st.Project != "proj-a34" {
		t.Fatalf("pinned: %+v", st)
	}
}

// The composition refuses what would break what the image promises: the
// model's records through an Anchored that is not strict or keeps every
// version, and an image that says zero data retention is approved while
// the notice in force says it is not.
func TestComposeModelRefuses(t *testing.T) {
	w := newModelWorld(t)
	for name, records := range map[string]*store.Anchored{
		"none":          nil,
		"not strict":    {Records: w.mem, Commits: w.commits, OnlyNewest: true},
		"every version": {Records: w.mem, Commits: w.commits, Strict: true},
	} {
		if _, err := composeModel(modelParts{records: records, entries: w.mem, keys: w.keys}); err == nil {
			t.Errorf("%s: composed", name)
		}
	}
	records := &store.Anchored{Records: w.mem, Commits: w.commits, OnlyNewest: true, Strict: true}
	if _, err := composeModel(modelParts{cfg: modelConfig{zeroDataRetention: true}, records: records, entries: w.mem, keys: w.keys}); !errors.Is(err, errZeroDataRetention) {
		t.Fatalf("zero data retention: %v", err)
	}
	if _, err := composeModel(modelParts{records: records}); err == nil {
		t.Fatal("composed without its storage")
	}
}
