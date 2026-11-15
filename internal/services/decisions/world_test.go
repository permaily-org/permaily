package decisions

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/decide"
	"github.com/permaily-org/permaily/internal/decide/decidetest"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/query"
	"github.com/permaily-org/permaily/internal/store"
)

// The test world: enclaves over one table and one store of commits, each
// with its own strict Anchored (its own memory of what it read), as two
// enclaves of a deployment are; one KeptStore that keeps its contract; a
// clock the test moves; and a model that answers from a table.
//
// The model is decidetest's Fake behind a hook of this package's
// (fakeModel), or the real client (internal/decide/openai) against
// decidetest's played provider (withProvider).

const testKey = "sk-test-0123456789abcdef-the-model-key"

var (
	t0      = time.Date(2026, 11, 16, 9, 0, 0, 0, time.UTC)
	testBox = inbox.Box{MailboxID: "mbx_test", Key: bytes.Repeat([]byte{7}, 32)}
)

func subj(topic string) query.Atom {
	return query.Atom{Var: contracts.VarSubject, Op: contracts.OpAbout, Topic: topic}
}

func about(topic string) query.Atom {
	return query.Atom{Var: contracts.VarMessage, Op: contracts.OpAbout, Topic: topic}
}

func mentions(topic string) query.Atom {
	return query.Atom{Var: contracts.VarMessage, Op: contracts.OpMentions, Topic: topic}
}

type world struct {
	t       *testing.T
	mu      sync.Mutex
	now     time.Time
	records *store.Memory
	commits *store.MemoryCommits
	kept    *fakeKept
	alarm   *fakeAlarm
	logs    *syncBuffer
}

func newWorld(t *testing.T) *world {
	t.Helper()
	return &world{
		t: t, now: t0,
		records: store.NewMemory(), commits: store.NewMemoryCommits(),
		kept: newFakeKept(), alarm: &fakeAlarm{}, logs: &syncBuffer{},
	}
}

func (w *world) clock() time.Time {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.now
}

func (w *world) advance(d time.Duration) {
	w.mu.Lock()
	w.now = w.now.Add(d)
	w.mu.Unlock()
}

func (w *world) set(t time.Time) {
	w.mu.Lock()
	w.now = t
	w.mu.Unlock()
}

// enclave is one service's view of the storage, with hooks.
type enclave struct {
	*Service
	w        *world
	anchored *store.Anchored
	commits  *hookCommits
	records  *hookRecords
	model    *fakeModel
}

type option func(*Config, *enclave)

func withLimits(l Limits) option { return func(c *Config, _ *enclave) { c.Limits = l } }
func withKeys(k Keys) option     { return func(c *Config, _ *enclave) { c.Keys = k } }
func withAlarm(a Alarm) option   { return func(c *Config, _ *enclave) { c.Alarm = a } }
func withoutModel() option       { return func(c *Config, _ *enclave) { c.NewModel = nil } }

// enclave starts a service over the world's storage.
func (w *world) enclave(opts ...option) *enclave {
	w.t.Helper()
	e := &enclave{w: w}
	e.commits = &hookCommits{inner: w.commits}
	e.anchored = &store.Anchored{Records: w.records, Commits: e.commits, OnlyNewest: true, Strict: true, Now: w.clock}
	e.records = &hookRecords{inner: e.anchored}
	e.model = newFakeModel()
	cfg := Config{
		NewModel: func(key decide.Credential) decide.Model { e.model.key = key; return e.model },
		Kept:     w.kept,
		Records:  e.records,
		Entries:  w.records,
		Keys:     testKeys{},
		Alarm:    w.alarm,
		Limits:   DefaultLimits,
		Log:      slog.New(slog.NewTextHandler(w.logs, &slog.HandlerOptions{Level: slog.LevelDebug})),
		Now:      w.clock,
	}
	for _, o := range opts {
		o(&cfg, e)
	}
	s, err := New(cfg)
	if err != nil {
		w.t.Fatal(err)
	}
	s.sleep = func(ctx context.Context, d time.Duration) error { return ctx.Err() }
	s.jitter = func(time.Duration) time.Duration { return 0 }
	e.Service = s
	return e
}

// keyed is an enclave with the model's key set.
func (w *world) keyed(opts ...option) *enclave {
	w.t.Helper()
	e := w.enclave(opts...)
	if _, err := e.SetKey(context.Background(), contracts.ModelKeySet{Key: testKey}); err != nil {
		w.t.Fatal(err)
	}
	return e
}

// testKeys derives a key from its purpose, as one root secret would.
type testKeys struct{ root string }

func (k testKeys) Derive(_ context.Context, purpose string) ([]byte, error) {
	sum := sha256.Sum256([]byte(k.root + "\x00" + purpose))
	return sum[:], nil
}

type failingKeys struct{}

func (failingKeys) Derive(context.Context, string) ([]byte, error) {
	return nil, errors.New("no root secret")
}

// hookCommits is the world's store of commits with hooks: one that runs
// before an append (the test may make another enclave write there, or
// stand for a write over a delete marker), and one after.
type hookCommits struct {
	inner *store.MemoryCommits
	mu    sync.Mutex
	// append replaces Append once when set: it is given the inner Append.
	append  func(c store.Commit, inner func(store.Commit) (*store.Commit, error)) (*store.Commit, error)
	history func(name string) error
}

func (h *hookCommits) Head(ctx context.Context, name string) (*store.Commit, uint64, error) {
	return h.inner.Head(ctx, name)
}

func (h *hookCommits) Append(ctx context.Context, c store.Commit) (*store.Commit, error) {
	h.mu.Lock()
	hook := h.append
	h.append = nil
	h.mu.Unlock()
	if hook != nil {
		return hook(c, func(c store.Commit) (*store.Commit, error) { return h.inner.Append(ctx, c) })
	}
	return h.inner.Append(ctx, c)
}

func (h *hookCommits) History(ctx context.Context, name string) (*store.History, error) {
	h.mu.Lock()
	hook := h.history
	h.mu.Unlock()
	if hook != nil {
		if err := hook(name); err != nil {
			return nil, err
		}
	}
	return h.inner.History(ctx, name)
}

func (h *hookCommits) onAppend(f func(c store.Commit, inner func(store.Commit) (*store.Commit, error)) (*store.Commit, error)) {
	h.mu.Lock()
	h.append = f
	h.mu.Unlock()
}

// hookRecords is the service's Records with hooks on reads and writes.
type hookRecords struct {
	inner *store.Anchored
	mu    sync.Mutex
	get   func(name string, n int) (handled bool, r *store.Record, at store.Mark, err error)
	put   func(name string) error
	gets  map[string]int
}

func (h *hookRecords) GetRecordAt(ctx context.Context, name string) (*store.Record, store.Mark, error) {
	h.mu.Lock()
	if h.gets == nil {
		h.gets = map[string]int{}
	}
	h.gets[name]++
	n, hook := h.gets[name], h.get
	h.mu.Unlock()
	if hook != nil {
		if handled, r, at, err := hook(name, n); handled {
			return r, at, err
		}
	}
	return h.inner.GetRecordAt(ctx, name)
}

func (h *hookRecords) PutRecordAt(ctx context.Context, r *store.Record, at store.Mark) error {
	if err := h.putHook(r.Name); err != nil {
		return err
	}
	return h.inner.PutRecordAt(ctx, r, at)
}

func (h *hookRecords) CreateNew(ctx context.Context, r *store.Record) error {
	if err := h.putHook(r.Name); err != nil {
		return err
	}
	return h.inner.CreateNew(ctx, r)
}

func (h *hookRecords) putHook(name string) error {
	h.mu.Lock()
	hook := h.put
	h.mu.Unlock()
	if hook != nil {
		return hook(name)
	}
	return nil
}

func (h *hookRecords) onGet(f func(name string, n int) (bool, *store.Record, store.Mark, error)) {
	h.mu.Lock()
	h.get, h.gets = f, map[string]int{}
	h.mu.Unlock()
}

func (h *hookRecords) onPut(f func(name string) error) {
	h.mu.Lock()
	h.put = f
	h.mu.Unlock()
}

// fakeModel is decidetest's Fake, answering from its Table (0 for an atom
// the table does not know), with what decidetest has no word for: a hook
// that answers the nth attempt instead (one that blocks, or fails in a way
// a test arranges), and each attempt's key and deadline as the model saw
// them. Its key is read through the service's credential, once an attempt.
type fakeModel struct {
	key decide.Credential
	tab *decidetest.Table
	mu  sync.Mutex
	// fail answers the nth call (from 1) instead of the table, when set.
	fail  func(ctx context.Context, n int, c decide.Call) (*decide.Result, error)
	calls []decide.Call
	keys  []string
	ctxs  []time.Time // each call's deadline
}

func newFakeModel() *fakeModel {
	return &fakeModel{tab: decidetest.NewTable()}
}

func (m *fakeModel) Name() string { return decidetest.Name }

func (m *fakeModel) set(a query.Atom, p float64) { m.tab.Set(a, p) }

func (m *fakeModel) onCall(f func(ctx context.Context, n int, c decide.Call) (*decide.Result, error)) {
	m.mu.Lock()
	m.fail = f
	m.mu.Unlock()
}

func (m *fakeModel) Decide(ctx context.Context, c decide.Call) (*decide.Result, error) {
	key, ok := m.key(ctx)
	if !ok {
		return nil, &decide.AttemptError{Err: decide.ErrKeyRefused}
	}
	if ctx.Err() != nil {
		return nil, &decide.AttemptError{Err: decide.ErrUnavailable, Retry: true}
	}
	m.mu.Lock()
	m.calls = append(m.calls, c)
	m.keys = append(m.keys, key)
	d, _ := ctx.Deadline()
	m.ctxs = append(m.ctxs, d)
	n, fail := len(m.calls), m.fail
	m.mu.Unlock()
	if fail != nil {
		if r, err := fail(ctx, n, c); r != nil || err != nil {
			return r, err
		}
	}
	return decidetest.NewFake(m.tab, func(context.Context) (string, bool) { return key, true }).Decide(ctx, c)
}

func (m *fakeModel) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.calls)
}

// fakeKept is a KeptStore that keeps its contract: one entry per message,
// at most KeptMaxAnswers answers, the first answer kept for a key stays,
// written at the version read (read again at most KeepTries times), the
// entry as written returned, ErrGone when a mark is there.
type fakeKept struct {
	mu      sync.Mutex
	entries map[string][]byte
	version map[string]int
	gone    map[string]bool
	// before runs before each write, with the try (from 1): the test may
	// write the entry as another enclave.
	before func(id string, try int)
	fail   error
	keeps  int
}

func newFakeKept() *fakeKept {
	return &fakeKept{entries: map[string][]byte{}, version: map[string]int{}, gone: map[string]bool{}}
}

func answerKey(a inbox.KeptAnswer) string {
	return strings.Join([]string{a.Content, a.Var, a.Op, a.Topic, a.Model, a.Prompt}, "\x00")
}

func (k *fakeKept) read(id string) (*inbox.KeptDecisions, int) {
	k.mu.Lock()
	defer k.mu.Unlock()
	raw, ok := k.entries[id]
	if !ok {
		return nil, k.version[id]
	}
	var d inbox.KeptDecisions
	_ = json.Unmarshal(raw, &d)
	return &d, k.version[id]
}

func (k *fakeKept) Kept(_ context.Context, b inbox.Box, id string) (*inbox.KeptDecisions, error) {
	d, _ := k.read(b.MailboxID + "/" + id)
	return d, nil
}

func merge(d *inbox.KeptDecisions, add *inbox.KeptDecisions) *inbox.KeptDecisions {
	if d == nil {
		d = &inbox.KeptDecisions{V: 1}
	}
	if d.Views == nil {
		d.Views = map[string]inbox.KeptView{}
	}
	for k, v := range add.Views {
		if _, ok := d.Views[k]; !ok {
			d.Views[k] = v
		}
	}
	have := map[string]bool{}
	for _, a := range d.Answers {
		have[answerKey(a)] = true
	}
	for _, a := range add.Answers {
		if have[answerKey(a)] || len(d.Answers) >= inbox.KeptMaxAnswers {
			continue
		}
		have[answerKey(a)] = true
		d.Answers = append(d.Answers, a)
	}
	return d
}

func (k *fakeKept) Keep(_ context.Context, b inbox.Box, id string, _ time.Time, add *inbox.KeptDecisions) (*inbox.KeptDecisions, error) {
	key := b.MailboxID + "/" + id
	k.mu.Lock()
	k.keeps++
	fail, before := k.fail, k.before
	k.mu.Unlock()
	if fail != nil {
		return nil, fail
	}
	for try := 1; try <= inbox.KeepTries; try++ {
		d, v := k.read(key)
		d = merge(d, add)
		if before != nil {
			before(key, try)
		}
		k.mu.Lock()
		if k.version[key] != v {
			k.mu.Unlock()
			continue
		}
		raw, _ := json.Marshal(d)
		k.entries[key], k.version[key] = raw, v+1
		gone := k.gone[key]
		if gone {
			delete(k.entries, key)
		}
		k.mu.Unlock()
		if gone {
			return nil, inbox.ErrGone
		}
		out, _ := k.read(key)
		return out, nil
	}
	return nil, errors.New("fake kept: the entry kept changing")
}

// put writes an entry as another writer would.
func (k *fakeKept) put(id string, d *inbox.KeptDecisions) {
	raw, _ := json.Marshal(d)
	k.mu.Lock()
	k.entries[testBox.MailboxID+"/"+id] = raw
	k.version[testBox.MailboxID+"/"+id]++
	k.mu.Unlock()
}

func (k *fakeKept) remove(id string) {
	k.mu.Lock()
	delete(k.entries, testBox.MailboxID+"/"+id)
	k.version[testBox.MailboxID+"/"+id]++
	k.mu.Unlock()
}

// fakeAlarm records what it raised, and fails while failing is set.
type fakeAlarm struct {
	mu      sync.Mutex
	raised  []string
	failing bool
}

func (a *fakeAlarm) Raise(_ context.Context, kind AlarmKind, day string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.failing {
		return errors.New("ses: refused, to hello@permaily.com")
	}
	a.raised = append(a.raised, string(kind)+" "+day)
	return nil
}

func (a *fakeAlarm) fail(on bool) {
	a.mu.Lock()
	a.failing = on
	a.mu.Unlock()
}

func (a *fakeAlarm) sent() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.raised...)
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// question is a question about message id with these atoms, its text given
// by text (TextRead), counting its reads.
type question struct {
	*inbox.Question
	reads int
	mu    sync.Mutex
}

func newQuestion(id, subject, text string, atoms ...query.Atom) *question {
	q := &question{}
	q.Question = &inbox.Question{
		Box: testBox, MessageID: id, ExpiresAt: t0.Add(90 * 24 * time.Hour), Account: "acc_one",
		Atoms: atoms, Subject: subject, View: inbox.ViewOriginal,
		Text: func(context.Context) (string, inbox.TextState, error) {
			q.mu.Lock()
			q.reads++
			q.mu.Unlock()
			return text, inbox.TextRead, nil
		},
	}
	return q
}

func (q *question) textReads() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.reads
}

// ask asks e and fails the test on an error.
func (e *enclave) ask(q *question) *inbox.Asked {
	e.w.t.Helper()
	out, err := e.Ask(context.Background(), q.Question)
	if err != nil {
		e.w.t.Fatal(err)
	}
	return out
}

// spend is the month's record as stored, read through a fresh enclave.
func (w *world) spend(month string) *spendDoc {
	w.t.Helper()
	e := w.enclave()
	var d spendDoc
	if _, _, err := readRecord(context.Background(), e.Service, RecordSpend+month, purposeSpend, &d); err != nil {
		w.t.Fatalf("the month's record: %v", err)
	}
	return &d
}

// seedSpend writes grants of blocks to the month's record, as other
// enclaves would have.
func (w *world) seedSpend(month, day string, blocks int64) {
	w.t.Helper()
	e := w.enclave()
	if _, err := changeRecord(context.Background(), e.Service, RecordSpend+month, purposeSpend, func(d *spendDoc, found bool) bool {
		if !found {
			*d = spendDoc{V: 1, Month: month}
		}
		d.Grants = append(d.Grants, grantDoc{ID: randomID(), Blocks: blocks, Day: day})
		return true
	}); err != nil {
		w.t.Fatal(err)
	}
}

// alarms is the record of alarms as stored.
func (w *world) alarms() *alarmsDoc {
	w.t.Helper()
	e := w.enclave()
	var d alarmsDoc
	if _, _, err := readRecord(context.Background(), e.Service, RecordAlarms, purposeAlarms, &d); err != nil {
		w.t.Fatalf("the record of alarms: %v", err)
	}
	return &d
}

func (d *alarmsDoc) has(kind AlarmKind, day string) (alarmEntry, bool) {
	for _, e := range d.Entries {
		if e.Kind == kind && e.Day == day {
			return e, true
		}
	}
	return alarmEntry{}, false
}

func (d *alarmsDoc) count(kind AlarmKind) int {
	n := 0
	for _, e := range d.Entries {
		if e.Kind == kind {
			n++
		}
	}
	return n
}

// versions are the stored versions of a record's value.
func (w *world) versions(name string) []string {
	names, _ := w.records.ListRecords(context.Background(), name+"@", 100)
	return names
}

var _ = fmt.Sprintf
