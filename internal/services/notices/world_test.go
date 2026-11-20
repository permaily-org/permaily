package notices_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/envelope"
	"github.com/permaily-org/permaily/internal/services/notices"
	"github.com/permaily-org/permaily/internal/services/notices/noticestest"
	"github.com/permaily-org/permaily/internal/store"
)

var ctx = context.Background()

// signKey is the key the played root derives for notices: made at run
// time, never written as a literal.
var signKey = bytes.Repeat([]byte{7}, envelope.KeySize)

// rootKeys is a played root secret: Err, when set, is what Derive answers.
type rootKeys struct {
	mu  sync.Mutex
	key []byte
	err error
}

func (k *rootKeys) Derive(_ context.Context, purpose string) ([]byte, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if purpose != notices.PurposeSign {
		return nil, fmt.Errorf("asked for %q", purpose)
	}
	if k.err != nil {
		return nil, k.err
	}
	return k.key, nil
}

func (k *rootKeys) set(key []byte, err error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.key, k.err = key, err
}

// syncBuffer is a log both enclaves and their goroutines write to.
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

// fault fails the nth call (from 1) of step on partition from when it was
// set, once.
type fault struct {
	step      store.Step
	partition string
	nth       int
	err       error
	// then, when set, is called instead of failing: what the storage's
	// writer does at that moment.
	then func()
}

// world is one storage two enclaves share, a played clock and the log.
type world struct {
	t     *testing.T
	clock *noticestest.Clock
	mem   *store.Memory
	// entries is what the enclaves are given: mem unless a test wraps it.
	entries notices.Storage
	keys    *rootKeys
	logs    *syncBuffer

	mu     sync.Mutex
	faults []*fault
	seen   map[*fault]int
}

func newWorld(t *testing.T) *world {
	t.Helper()
	w := &world{t: t, clock: noticestest.NewClock(noticestest.SampleAt), mem: store.NewMemory(), keys: &rootKeys{key: signKey}, logs: &syncBuffer{}, seen: map[*fault]int{}}
	w.mem.Now = w.clock.Now
	w.entries = w.mem
	w.mem.Interrupt = func(_ context.Context, step store.Step, partition, _ string) error {
		w.mu.Lock()
		var then func()
		var err error
		for _, f := range w.faults {
			if f.step != step || f.partition != partition {
				continue
			}
			if w.seen[f]++; w.seen[f] == f.nth {
				then, err = f.then, f.err
				w.faults = slices.DeleteFunc(w.faults, func(g *fault) bool { return g == f })
				break
			}
		}
		w.mu.Unlock()
		if then != nil {
			then()
			return nil
		}
		return err
	}
	return w
}

// fail makes the nth next call of step on partition fail with err.
func (w *world) fail(step store.Step, partition string, nth int, err error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.faults = append(w.faults, &fault{step: step, partition: partition, nth: nth, err: err})
}

// at runs then at the nth next call of step on partition, which then goes
// on.
func (w *world) at(step store.Step, partition string, nth int, then func()) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.faults = append(w.faults, &fault{step: step, partition: partition, nth: nth, then: then})
}

// enclave is one enclave's queue over the world, its admissions played:
// admit, when set, is called with each admission's time.
func (w *world) enclave(d notices.Deliverer, admit func(ctx context.Context, at time.Time) error) *notices.Service {
	w.t.Helper()
	s, err := notices.New(notices.Config{Entries: w.entries, Keys: w.keys, Deliverer: d, Now: w.clock.Now, Log: slog.New(slog.NewJSONHandler(w.logs, nil))})
	if err != nil {
		w.t.Fatal(err)
	}
	notices.Play(s, admit)
	w.t.Cleanup(func() { stop(s) })
	return s
}

// stop ends s's life as the end of Run does: what waits for admission
// gives up.
func stop(s *notices.Service) {
	c, cancel := context.WithCancel(ctx)
	cancel()
	s.Run(c)
	_ = notices.Wait(ctx, s)
}

// queued are the entries of the queue as stored.
func (w *world) queued() []store.Entry {
	var out []store.Entry
	for _, e := range w.mem.AllEntries() {
		if e.Partition == notices.PartitionQueue && (e.ExpiresAt.IsZero() || e.ExpiresAt.After(w.clock.Now())) {
			out = append(out, e)
		}
	}
	return out
}

// entry is the stored entry of k, nil when there is none.
func (w *world) entry(partition, k string) *store.Entry {
	for _, e := range w.mem.AllEntries() {
		if e.Partition == partition && e.Key == k {
			return &e
		}
	}
	return nil
}

// markOf is what the mark of k says, "" when there is none.
func (w *world) markOf(k string) string {
	e := w.entry(notices.PartitionSent, k)
	if e == nil {
		return ""
	}
	doc, err := envelope.Verified(signKey, e.Value, []byte(notices.PartitionSent+"\x00"+k))
	if err != nil {
		return "unverified"
	}
	var m struct {
		Outcome string `json:"outcome"`
	}
	_ = json.Unmarshal(doc, &m)
	return m.Outcome
}

// signMark writes a mark of k by hand, signed at at.
func (w *world) signMark(k, outcome string, at time.Time) {
	doc, _ := json.Marshal(map[string]any{"outcome": outcome, "at": at})
	v, err := envelope.Sign(signKey, doc, []byte(notices.PartitionSent+"\x00"+k))
	if err != nil {
		w.t.Fatal(err)
	}
	// Kept by the storage whatever its own time says: its expiry is
	// whoever writes the table's to change.
	w.mem.SetEntry(store.Entry{Partition: notices.PartitionSent, Key: k, Value: v, Version: 1, ExpiresAt: w.clock.Now().Add(notices.KeptFor)})
}

// put writes d under its notice's key as the queue would, at version 0.
func (w *world) put(s *notices.Service, d notices.Entry) {
	w.t.Helper()
	if err := notices.Write(ctx, s, signKey, &store.Entry{Partition: notices.PartitionQueue, Key: d.Notice.Key}, d); err != nil {
		w.t.Fatal(err)
	}
}

// at is Sample(id) at the clock's time.
func sampleNow(w *world, id string) notices.Notice {
	n := noticestest.Sample(id)
	n.At = w.clock.Now()
	n.Facts.EndsAt = n.At.AddDate(0, 0, 90)
	return n
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// count is how many times each key was delivered.
func count(d *noticestest.Recorder) map[string]int {
	out := map[string]int{}
	for _, n := range d.Delivered() {
		out[n.Key]++
	}
	return out
}

// gate is a Deliverer whose attempts block until open, saying each start.
type gate struct {
	started chan string
	open    chan struct{}
	answer  error
	d       *noticestest.Recorder
}

func newGate(answer error) *gate {
	return &gate{started: make(chan string, 64), open: make(chan struct{}), answer: answer, d: &noticestest.Recorder{}}
}

func (g *gate) Deliver(c context.Context, n notices.Notice) error {
	g.started <- n.Key
	<-g.open
	if g.answer != nil {
		return g.answer
	}
	return g.d.Deliver(c, n)
}

var errMail = errors.New("the mail system did not answer for ana@example.org")
