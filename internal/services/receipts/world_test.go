package receipts

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/query"
	"github.com/permaily-org/permaily/internal/rules"
	"github.com/permaily-org/permaily/internal/store"
)

// The world of these tests: one storage, enclaves over it, a clock the test
// moves and random bytes it chooses.

const account = "acc_aaaaaaaaaaaaaaaaaaaaaaaaaa"

var start = time.Date(2026, 11, 16, 10, 0, 0, 123456000, time.UTC)

// testKeys derives keys from a secret as the root does from its own: the
// same secret, the same keys in every enclave.
type testKeys struct {
	secret string
	fail   error
}

func (k testKeys) Derive(_ context.Context, purpose string) ([]byte, error) {
	if k.fail != nil {
		return nil, k.fail
	}
	sum := sha256.Sum256([]byte(k.secret + "\x00" + purpose))
	return sum[:], nil
}

// clock is a time the test sets; each read advances it by step.
type clock struct {
	mu   sync.Mutex
	at   time.Time
	step time.Duration
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := c.at
	c.at = c.at.Add(c.step)
	return t
}

func (c *clock) set(t time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = t
}

// counter is random bytes that differ on every call and are the same on
// every run.
type counter struct {
	mu sync.Mutex
	n  uint64
}

func (c *counter) read(b []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.n++
	clear(b)
	binary.BigEndian.PutUint64(b[len(b)-8:], c.n)
	return nil
}

// same is random bytes that never change: a broken source.
func same(b []byte) error {
	for i := range b {
		b[i] = 0xab
	}
	return nil
}

type world struct {
	mem   *store.Memory
	clock *clock
}

func newWorld() *world {
	w := &world{mem: store.NewMemory(), clock: &clock{at: start}}
	w.mem.Now = w.clock.now
	return w
}

// enclave is one enclave's service over the world's storage.
func (w *world) enclave() *Service {
	return New(w.mem, testKeys{secret: "root"}, w.clock.now, (&counter{}).read)
}

// listAll lists every receipt of account.
func listAll(t *testing.T, s *Service, account string) []Receipt {
	t.Helper()
	var out []Receipt
	after := ""
	for {
		page, err := s.List(context.Background(), account, after, 7)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 {
			return out
		}
		out = append(out, page...)
		after = page[len(page)-1].ID
	}
}

func canonical(t testing.TB, conds ...contracts.Condition) *query.Canonical {
	t.Helper()
	f := contracts.Formula{V: contracts.FormulaVersion}
	for _, c := range conds {
		f.All = append(f.All, contracts.Group{Any: []contracts.Condition{c}})
	}
	c, err := query.Canonicalise(f)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

const (
	mailboxA = "mbx_aaaaaaaaaaaaaaaaaaaaaaaaaa"
	mailboxB = "mbx_bbbbbbbbbbbbbbbbbbbbbbbbbb"
)

var (
	defaultRef = rules.Ref{ID: rules.DefaultID, Version: rules.DefaultVersion}
	signInCode = query.Atom{Var: contracts.VarMessage, Op: contracts.OpAbout, Topic: "a sign-in, login or verification code, or a one-time password"}
	resetting  = query.Atom{Var: contracts.VarMessage, Op: contracts.OpAbout, Topic: "resetting a password or recovering an account"}
	invoices   = query.Atom{Var: contracts.VarSubject, Op: contracts.OpAbout, Topic: "invoices"}
)

func evidence(p float64) inbox.Evidence {
	return inbox.Evidence{P: p, Model: "gpt-6-luna", Prompt: "p2c", At: start.Add(-time.Hour), CanaryChecked: "2026-11-15", CanaryChange: "2026-11-01"}
}

func defaultEvidence(p1, p2 float64) []inbox.Decided {
	return []inbox.Decided{{Atom: signInCode, Evidence: evidence(p1)}, {Atom: resetting, Evidence: evidence(p2)}}
}

// heads of the receipts of each kind, as the connections service would
// give them to Write: the golden documents of H13.
func samples(t testing.TB) map[Kind]*Receipt {
	consented := canonical(t, contracts.Condition{Var: contracts.VarSubject, Op: contracts.OpContains, Value: "invoice"})
	mailboxes := canonical(t, contracts.Condition{Var: contracts.VarMailbox, Op: contracts.OpIs, Value: mailboxA})
	since := canonical(t, contracts.Condition{Var: contracts.VarReceived, Op: contracts.OpSince, Value: "2026-11-01T00:00:00Z"})
	search := canonical(t, contracts.Condition{Var: contracts.VarSubject, Op: contracts.OpAbout, Value: "invoices"})
	who := Receipt{
		Connection: "con_cccccccccccccccccccccccccc", Device: "fam_dddddddddddddddddddddddddd",
		ClientID: "https://claude.ai/oauth/claude-code-client-metadata", Vendor: "claude.ai", Resource: contracts.ResourceMCP,
	}
	scope := []Formula{FormulaOf(RoleConsented, consented, false), FormulaOf(RoleMailboxes, mailboxes, false), FormulaOf(RoleSince, since, false)}
	subject := "Invoice 12 <for> \"you\""
	parts := []contracts.Part{contracts.PartSender, contracts.PartSubject}
	text := "the text never goes in a receipt"
	left := inbox.Disclosed{
		ID: "0000018a0b0c0d0e1112131415161718",
		Message: contracts.VendorMessage{
			ID: "0000018a0b0c0d0e1112131415161718", ReceivedAt: start.Add(-48 * time.Hour),
			From: []contracts.MailAddress{{Name: "Billing", Address: "billing@example.com"}}, Subject: &subject, Text: &text,
		},
		Decided: []inbox.Decided{{Atom: invoices, Evidence: evidence(0.91)}},
		Rules:   []inbox.RuleCleared{{Rule: defaultRef, At: start.Add(-time.Minute), Decided: defaultEvidence(0.02, 0.01)}},
	}
	held := inbox.Withheld{ID: "0000018a0b0c0d0f2122232425262728", Rule: defaultRef, Why: inbox.HeldDecided, Decided: defaultEvidence(0.97, 0.03)}
	exact := inbox.Withheld{ID: "0000018a0b0c0d0f3132333435363738", Rule: defaultRef, Why: inbox.HeldExact}

	out := map[Kind]*Receipt{}

	fresh := func() *Receipt { r := who; return &r }

	r := fresh()
	r.Kind, r.Formulas, r.Rules = KindSearch, append(scope[:3:3], FormulaOf(RoleSearch, search, true)), []rules.Ref{defaultRef}
	r.Delivered = []Delivered{DeliveredOf(mailboxA, &left, parts)}
	r.Held = []Held{HeldOf(mailboxA, &held), HeldOf(mailboxB, &exact)}
	out[KindSearch] = r

	r = fresh()
	r.Kind, r.Formulas, r.Rules = KindRead, scope, []rules.Ref{defaultRef}
	r.Delivered = []Delivered{DeliveredOf(mailboxA, &left, append(parts, contracts.PartBody))}
	out[KindRead] = r

	r = fresh()
	r.Kind, r.Formulas, r.Rules = KindAttachment, scope, []rules.Ref{defaultRef}
	d := DeliveredOf(mailboxA, &left, append(parts, contracts.PartAttachments))
	index := 1
	d.Attachment = &index
	r.Delivered = []Delivered{d}
	out[KindAttachment] = r

	r = fresh()
	r.Kind, r.Formulas, r.Rules = KindRefused, scope, []rules.Ref{defaultRef}
	r.Refused = RefusedHeldOf(mailboxA, &held)
	out[KindRefused] = r

	r = fresh()
	r.Kind, r.Rules = KindConsent, []rules.Ref{defaultRef}
	r.Formulas = []Formula{FormulaOf(RoleConsented, consented, true), FormulaOf(RoleMailboxes, mailboxes, true), FormulaOf(RoleSince, since, true)}
	r.Consent = &Consent{
		Allowed: true, Change: contracts.ChangeReplace,
		Sentences: []string{"Mail whose subject contains \"invoice\".", "Mail at mailbox A.", "Mail received since 1 November 2026."},
		Parts:     []contracts.Part{contracts.PartSender, contracts.PartSubject, contracts.PartBody},
		Since:     contracts.MailSince{At: time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)}, EndsAt: start.Add(90 * 24 * time.Hour),
		Mailboxes: []string{mailboxA}, ModelNotice: "2026-11-15", Replaced: "con_bbbbbbbbbbbbbbbbbbbbbbbbbb",
	}
	out[KindConsent] = r

	r = fresh()
	r.Kind = KindEnded
	r.Ended = &Ended{Reason: contracts.EndedByReplaced, ReplacedBy: "con_eeeeeeeeeeeeeeeeeeeeeeeeee"}
	out[KindEnded] = r

	r = fresh()
	r.Kind, r.Device = KindRefusals, ""
	r.Refusals = &Refusals{
		From: start.Truncate(time.Minute), Until: start.Truncate(time.Minute).Add(time.Minute), Recorded: 30,
		Counts: []RefusalCount{{Operation: "read", Reason: RefusedRateLimited, Count: 412}, {Operation: "search", Reason: RefusedInvalidBody, Count: 7}},
	}
	out[KindRefusals] = r

	return out
}

var errBoom = errors.New("boom")
