package webhooks_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/services/webhooks"
	"github.com/permaily-org/permaily/internal/services/webhooks/webhookstest"
	"github.com/permaily-org/permaily/internal/store"
	"github.com/permaily-org/permaily/internal/webhook"
	"github.com/permaily-org/permaily/internal/webhook/webhooktest"
)

// The tests of ADR 0039's pull request 3 (K35 to K50, and the re-review's
// N2 and N3) run the real queue over store.Memory against
// webhookstest.Fake's connections, a played clock, and played vendors
// (webhooktest.Vendor) reached through a sender that hands them each call.

var ctx = context.Background()

// start is a Friday.
var start = time.Date(2026, 11, 20, 8, 0, 0, 0, time.UTC)

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func (c *clock) set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = t
}

// keys derives a key from its purpose alone, as keys.Local would from a
// root secret; fail, when set, fails every derivation.
type keys struct {
	mu   sync.Mutex
	fail error
}

func (k *keys) Derive(_ context.Context, purpose string) ([]byte, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.fail != nil {
		return nil, k.fail
	}
	h := sha256.Sum256([]byte(purpose))
	return h[:], nil
}

// publicKey is the key notices are signed with, as a vendor has it.
func publicKey() ed25519.PublicKey {
	h := sha256.Sum256([]byte(webhook.KeyPurpose))
	return ed25519.NewKeyFromSeed(h[:]).Public().(ed25519.PublicKey)
}

// sender hands each call to the played vendor of its URL, as the real
// sender would over HTTP/2, and keeps it. script, while it lasts, answers
// instead of any vendor; lost makes the next calls reach the vendor and
// answer no_answer (its 2xx lost); before and after run around a call.
type sender struct {
	mu      sync.Mutex
	vendors map[string]*webhooktest.Vendor
	script  []webhook.Outcome
	lost    int
	calls   []webhook.Call
	before  func(c webhook.Call)
	after   func(c webhook.Call)
}

func (s *sender) Send(ctx context.Context, c webhook.Call) webhook.Outcome {
	s.mu.Lock()
	s.calls = append(s.calls, c)
	before, after := s.before, s.after
	var scripted webhook.Outcome
	if len(s.script) > 0 {
		scripted, s.script = s.script[0], s.script[1:]
	}
	v := s.vendors[c.URL]
	lost := s.lost > 0
	if lost && scripted == "" {
		s.lost--
	}
	s.mu.Unlock()
	if before != nil {
		before(c)
	}
	if scripted != "" {
		return scripted
	}
	if ctx.Err() != nil {
		return webhook.OutcomeCut
	}
	o := webhook.OutcomeSent
	if v != nil {
		r := httptest.NewRequest(http.MethodPost, c.URL, bytes.NewReader(c.Body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set(contracts.WebhookHeaderID, c.ID)
		r.Header.Set(contracts.WebhookHeaderTimestamp, c.Timestamp)
		r.Header.Set(contracts.WebhookHeaderSignature, c.Signature)
		rec := httptest.NewRecorder()
		v.ServeHTTP(rec, r)
		switch code := rec.Code; {
		case code == http.StatusGone:
			o = webhook.OutcomeGone
		case code >= 300:
			o = webhook.OutcomeStatus
		}
	}
	if after != nil {
		after(c)
	}
	if lost {
		return webhook.OutcomeNoAnswer
	}
	return o
}

func (s *sender) set(f func(s *sender)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f(s)
}

func (s *sender) all() []webhook.Call {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.calls)
}

// notices are the bodies of the calls made, opened.
func (s *sender) notices(t *testing.T) []contracts.WebhookNotice {
	t.Helper()
	var out []contracts.WebhookNotice
	for _, c := range s.all() {
		n, err := webhook.Open(c.Body, c.ID, c.URL, time.Unix(mustInt(t, c.Timestamp), 0))
		if err != nil {
			t.Fatalf("a body sent does not open: %v", err)
		}
		out = append(out, *n)
	}
	return out
}

func mustInt(t *testing.T, s string) int64 {
	t.Helper()
	var n int64
	if _, err := fmt.Sscan(s, &n); err != nil {
		t.Fatal(err)
	}
	return n
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

// world is the store, the clock, the fake connections and the vendors that
// one or two enclaves share.
type world struct {
	t      *testing.T
	clock  *clock
	mem    *store.Memory
	fake   *webhookstest.Fake
	conns  webhooks.Connections
	keys   *keys
	sender *sender
	logs   *lockedBuffer
	n      int
	// tolerate: a failure of Subscribe is part of the test.
	tolerate bool

	mu        sync.Mutex
	messages  map[string][]webhookstest.Message
	interrupt func(ctx context.Context, step store.Step, partition, key string) error
}

func newWorld(t *testing.T) *world {
	w := &world{
		t: t, clock: &clock{t: start}, mem: store.NewMemory(), keys: &keys{},
		sender: &sender{vendors: map[string]*webhooktest.Vendor{}}, logs: &lockedBuffer{},
		messages: map[string][]webhookstest.Message{},
	}
	w.mem.Now = w.clock.now
	w.mem.Interrupt = func(ctx context.Context, step store.Step, partition, key string) error {
		w.mu.Lock()
		f := w.interrupt
		w.mu.Unlock()
		if f == nil {
			return nil
		}
		return f(ctx, step, partition, key)
	}
	w.fake = &webhookstest.Fake{Now: w.clock.now}
	w.conns = w.fake
	return w
}

func (w *world) setInterrupt(f func(ctx context.Context, step store.Step, partition, key string) error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.interrupt = f
}

// enclave is a webhooks service over the world, as one enclave runs it.
func (w *world) enclave(opts ...webhooks.Option) *webhooks.Service {
	w.t.Helper()
	return w.enclaveWith(w.conns, opts...)
}

// enclaveWith is an enclave that reads its connections through conns.
func (w *world) enclaveWith(conns webhooks.Connections, opts ...webhooks.Option) *webhooks.Service {
	w.t.Helper()
	opts = append([]webhooks.Option{webhooks.WithLog(slog.New(slog.NewJSONHandler(w.logs, &slog.HandlerOptions{Level: slog.LevelDebug})), "verified.example")}, opts...)
	s, err := webhooks.New(webhooks.Config{Entries: w.mem, Keys: w.keys, Connections: conns, Sender: w.sender, Now: w.clock.now}, opts...)
	if err != nil {
		w.t.Fatal(err)
	}
	return s
}

func (w *world) next(prefix string) string {
	w.n++
	return fmt.Sprintf("%s%026d", prefix, w.n)
}

// conn is a connection made in the world.
type conn struct {
	webhooks.Subscription
	vendor *webhooktest.Vendor
}

type made struct {
	host     string
	newMail  bool
	schedule *contracts.WebhookSchedule
	zone     string
	account  string
	// mailboxes, id and clientID replace what make would choose.
	mailboxes []string
	id        string
	clientID  string
}

// make makes an active connection of a vendor on host whose webhook is
// as m says, with a played vendor at its URL, and subscribes it through s.
func (w *world) make(s *webhooks.Service, m made) *conn {
	w.t.Helper()
	if m.host == "" {
		m.host = "trip-app.example"
	}
	if m.zone == "" {
		m.zone = "Europe/Madrid"
	}
	if m.account == "" {
		m.account = w.next("acc_")
	}
	c := w.next("con_")
	if m.mailboxes == nil {
		m.mailboxes = []string{w.next("mbx_"), w.next("mbx_")}
	}
	if m.id == "" {
		m.id = w.next(webhook.SubscriptionPrefix)
	}
	if m.clientID == "" {
		m.clientID = "https://" + m.host + "/oauth/client.json"
	}
	sub := webhooks.Subscription{
		Connection: c, Account: m.account, ID: m.id, ClientID: m.clientID,
		URL: "https://" + m.host + "/hooks/permaily/" + strings.TrimPrefix(c, "con_"), NewMail: m.newMail, Mailboxes: m.mailboxes,
		Schedule: m.schedule, TimeZone: m.zone, MadeAt: w.clock.now(),
	}
	w.fake.Make(sub)
	v := &webhooktest.Vendor{Keys: []ed25519.PublicKey{publicKey()}, Endpoint: sub.ID, Audience: sub.URL, Now: w.clock.now}
	w.sender.set(func(s *sender) { s.vendors[sub.URL] = v })
	read, err := w.fake.Subscription(ctx, c)
	if err != nil {
		w.t.Fatal(err)
	}
	if err := s.Subscribe(ctx, *read); err != nil && !w.tolerate {
		w.t.Fatal(err)
	}
	return &conn{Subscription: *read, vendor: v}
}

// deliver stores a message in the connection's mailbox number i, received
// now, and tells s of it (nil tells nobody: a hint lost).
func (w *world) deliver(s *webhooks.Service, c *conn, i int, leaves bool) webhooks.Cursor {
	return w.deliverAt(s, c, i, w.clock.now(), leaves)
}

func (w *world) deliverAt(s *webhooks.Service, c *conn, i int, received time.Time, leaves bool) webhooks.Cursor {
	m := webhookstest.Message{Mailbox: c.Mailboxes[i], ID: w.next("msg_"), Received: received, Leaves: leaves}
	w.fake.Deliver(c.Connection, m)
	w.mu.Lock()
	w.messages[c.Connection] = append(w.messages[c.Connection], m)
	w.mu.Unlock()
	if s != nil {
		s.Arrived(m.Mailbox, received)
	}
	return webhooks.Cursor{Received: m.Received, Mailbox: m.Mailbox, Message: m.ID}
}

// fetch is what a vendor's fetch since a notice's minute finds of the
// connection's mail that leaves.
func (w *world) fetch(c *conn, since time.Time) []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	var out []string
	for _, m := range w.messages[c.Connection] {
		if !m.Received.Before(since) {
			out = append(out, m.ID)
		}
	}
	return out
}

// run is what the enclaves do now: their hints, then a pass each.
func (w *world) run(enclaves ...*webhooks.Service) {
	for _, s := range enclaves {
		webhooks.Hints(ctx, s)
		webhooks.Drain(ctx, s)
	}
}

// every runs the enclaves every step for d, the clock moving.
func (w *world) every(step, d time.Duration, enclaves ...*webhooks.Service) {
	for end := w.clock.now().Add(d); w.clock.now().Before(end); {
		w.clock.add(step)
		w.run(enclaves...)
	}
}

// entries are the entries of a partition (all shards for the state).
func (w *world) entries(partition string) []store.Entry {
	w.t.Helper()
	var out []store.Entry
	parts := []string{partition}
	if partition == webhooks.PartitionState {
		parts = nil
		for i := range webhooks.Shards {
			parts = append(parts, webhooks.PartitionState+fmt.Sprintf("%02x", i))
		}
	}
	for _, p := range parts {
		got, err := w.mem.ListEntries(ctx, p, "", "", 10000)
		if err != nil {
			w.t.Fatal(err)
		}
		out = append(out, got...)
	}
	return out
}

// dueEntries are the due entries of the minutes around now, by minute.
func (w *world) dueEntries(back, ahead time.Duration) map[string][]string {
	out := map[string][]string{}
	now := w.clock.now().Truncate(time.Minute)
	for m := now.Add(-back); !m.After(now.Add(ahead)); m = m.Add(time.Minute) {
		for _, e := range w.entries(webhooks.PartitionDue + m.Format("200601021504")) {
			out[e.Key] = append(out[e.Key], m.Format("15:04"))
		}
	}
	return out
}

// skipped is s's count of a reason.
func skipped(s *webhooks.Service, why webhooks.Skip) int64 { return s.Status().Skipped[string(why)] }

func outcomes(s *webhooks.Service, o webhook.Outcome) int64 { return s.Status().Outcomes[string(o)] }

func contains(haystack string, needles ...string) string {
	for _, n := range needles {
		if n != "" && strings.Contains(haystack, n) {
			return n
		}
	}
	return ""
}
