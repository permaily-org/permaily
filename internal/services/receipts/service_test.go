package receipts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"log"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/envelope"
	"github.com/permaily-org/permaily/internal/store"
)

var update = flag.Bool("update", false, "write the golden receipts")

// stored opens the document Write stored for r, byte for byte.
func stored(t *testing.T, w *world, r *Receipt) []byte {
	t.Helper()
	e, err := w.mem.GetEntry(context.Background(), Partition(account), r.ID)
	if err != nil {
		t.Fatal(err)
	}
	key, _ := testKeys{secret: "root"}.Derive(context.Background(), Purpose)
	raw, err := envelope.OpenText(key, e.Value, aad(e.Partition, e.Key))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// H13: a receipt of each kind is, as stored, its golden document
// (testdata/golden; go test -update writes them, then bun run format).
func TestH13AReceiptOfEachKindIsItsGoldenDocument(t *testing.T) {
	w := newWorld()
	s := w.enclave()
	kinds := []Kind{KindSearch, KindRead, KindAttachment, KindRefused, KindConsent, KindEnded}
	all := samples(t)
	if len(all) != len(kinds) {
		t.Fatalf("%d kinds sampled", len(all))
	}
	for _, k := range kinds {
		t.Run(string(k), func(t *testing.T) {
			r := all[k]
			if err := s.Write(context.Background(), account, r); err != nil {
				t.Fatal(err)
			}
			var got bytes.Buffer
			if err := json.Indent(&got, stored(t, w, r), "", "  "); err != nil {
				t.Fatal(err)
			}
			got.WriteByte('\n')
			path := filepath.Join("testdata", "golden", string(k)+".json")
			if *update {
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, got.Bytes(), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			// The file is formatted as the repository formats JSON (bun run
			// format after -update); the document is its bytes without the
			// spaces between them.
			file, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var want bytes.Buffer
			if err := json.Compact(&want, file); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(stored(t, w, r), want.Bytes()) {
				t.Fatalf("the stored %s receipt is not its golden document (go test -update writes it):\n%s", k, got.Bytes())
			}
		})
	}
	// And each reads back as written.
	listed := listAll(t, s, account)
	if len(listed) != len(kinds) {
		t.Fatalf("%d receipts listed", len(listed))
	}
	for i, k := range kinds {
		if !reflect.DeepEqual(normal(t, &listed[i]), normal(t, all[k])) {
			t.Fatalf("the %s receipt read back is not the one written", k)
		}
	}
}

// normal is r as its JSON says it.
func normal(t *testing.T, r *Receipt) string {
	t.Helper()
	b, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// H13: what the table holds of a receipt is sealed, and opens only where it
// was written, under this deployment's key, unchanged.
func TestH13AReceiptIsSealedAndBoundToWhereItIs(t *testing.T) {
	ctx := context.Background()
	w := newWorld()
	s := w.enclave()
	r := samples(t)[KindSearch]
	if err := s.Write(ctx, account, r); err != nil {
		t.Fatal(err)
	}
	e, err := w.mem.GetEntry(ctx, Partition(account), r.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, plain := range []string{"Invoice 12", "billing@example.com", "claude.ai", r.Connection, mailboxA, string(KindSearch), "invoices"} {
		if bytes.Contains(e.Value, []byte(plain)) {
			t.Fatalf("the stored value shows %q", plain)
		}
	}
	other := "acc_oooooooooooooooooooooooooo"
	moved := *e
	moved.Partition = Partition(other)
	w.mem.SetEntry(moved)
	if _, err := s.List(ctx, other, "", 10); !errors.Is(err, ErrUnreadable) {
		t.Fatalf("another account's receipt opened: %v", err)
	}

	w2 := newWorld()
	s2 := w2.enclave()
	copied := *e
	copied.Key = IDPrefix + strings.Repeat("0", IDLength-len(IDPrefix))
	w2.mem.SetEntry(copied)
	if _, err := s2.List(ctx, account, "", 10); !errors.Is(err, ErrUnreadable) {
		t.Fatalf("a receipt under another key opened: %v", err)
	}

	w3 := newWorld()
	w3.mem.SetEntry(*e)
	s3 := New(w3.mem, testKeys{secret: "another root"}, w3.clock.now, (&counter{}).read)
	if _, err := s3.List(ctx, account, "", 10); !errors.Is(err, ErrUnreadable) {
		t.Fatalf("a receipt opened under another deployment's key: %v", err)
	}
	if got, err := w3.enclave().List(ctx, account, "", 10); err != nil || len(got) != 1 {
		t.Fatalf("the receipt moved whole to another storage did not open with its key: %v", err)
	}

	// Every byte of what was sealed, changed one at a time (the base64 of
	// the stored text has bits that decode to nothing, so the bytes are
	// changed, not their text).
	var sealed struct {
		Sealed []byte `json:"sealed"`
	}
	if err := json.Unmarshal(e.Value, &sealed); err != nil {
		t.Fatal(err)
	}
	for i := range sealed.Sealed {
		w4 := newWorld()
		changed := *e
		flipped := slices.Clone(sealed.Sealed)
		flipped[i] ^= 0x01
		changed.Value, _ = json.Marshal(map[string][]byte{"sealed": flipped})
		w4.mem.SetEntry(changed)
		if _, err := w4.enclave().List(ctx, account, "", 10); !errors.Is(err, ErrUnreadable) {
			t.Fatalf("a receipt with byte %d changed opened: %v", i, err)
		}
	}

	// One made by whoever writes the table: plain, or sealed under no key
	// of this deployment, or sealed right and naming another receipt.
	key, _ := testKeys{secret: "root"}.Derive(ctx, Purpose)
	plain, _ := json.Marshal(r)
	id := IDPrefix + strings.Repeat("1", IDLength-len(IDPrefix))
	wrongID, _ := envelope.SealText(key, plain, aad(Partition(account), id))
	notAReceipt, _ := envelope.SealText(key, []byte(`{"v":0}`), aad(Partition(account), id))
	for name, value := range map[string][]byte{"plain": plain, "another id": wrongID, "no version": notAReceipt, "not json": []byte("x")} {
		w5 := newWorld()
		w5.mem.SetEntry(store.Entry{Partition: Partition(account), Key: id, Value: value})
		if _, err := w5.enclave().List(ctx, account, "", 10); !errors.Is(err, ErrUnreadable) {
			t.Fatalf("a forged receipt (%s) opened: %v", name, err)
		}
	}
}

// recording keeps every entry the service asked the storage to put.
type recording struct {
	store.Entries
	mu   sync.Mutex
	puts []store.Entry
}

func (r *recording) PutEntry(ctx context.Context, e *store.Entry) error {
	r.mu.Lock()
	r.puts = append(r.puts, *e)
	r.mu.Unlock()
	return r.Entries.PutEntry(ctx, e)
}

// H13: a receipt is written once, as a new entry, with no expiry; one whose
// identifier is taken writes no other over it.
func TestH13AReceiptIsWrittenOnceAtVersionZeroWithNoExpiry(t *testing.T) {
	ctx := context.Background()
	w := newWorld()
	rec := &recording{Entries: w.mem}
	s := New(rec, testKeys{secret: "root"}, func() time.Time { return start }, same)
	first := samples(t)[KindRead]
	if err := s.Write(ctx, account, first); err != nil {
		t.Fatal(err)
	}
	before := stored(t, w, first)
	// The same instant and the same random bytes: the identifier is taken,
	// every try is refused, and the first stays as it was.
	second := samples(t)[KindEnded]
	if err := s.Write(ctx, account, second); err == nil || second.ID != "" {
		t.Fatalf("a second receipt was written at a taken identifier: %v", err)
	}
	if !bytes.Equal(stored(t, w, first), before) {
		t.Fatal("the first receipt changed")
	}
	if len(rec.puts) != 1+writeTries {
		t.Fatalf("%d writes", len(rec.puts))
	}
	for _, e := range rec.puts {
		if e.Version != 0 || !e.ExpiresAt.IsZero() {
			t.Fatalf("a receipt was written at version %d, expiring %v", e.Version, e.ExpiresAt)
		}
	}
	// A taken identifier is tried again with other random bytes.
	tries := 0
	again := New(rec, testKeys{secret: "root"}, func() time.Time { return start }, func(b []byte) error {
		tries++
		if tries == 1 {
			return same(b)
		}
		return (&counter{}).read(b)
	})
	if err := again.Write(ctx, account, second); err != nil || second.ID == first.ID {
		t.Fatalf("a taken identifier was not tried again: %v", err)
	}
	e, err := w.mem.GetEntry(ctx, Partition(account), second.ID)
	if err != nil || !e.ExpiresAt.IsZero() {
		t.Fatalf("the receipt expires: %v %v", e, err)
	}
	if got := listAll(t, w.enclave(), account); len(got) != 2 {
		t.Fatalf("%d receipts", len(got))
	}
}

// H15: a writer that fails keeps nothing, and says so: its request gives
// nothing.
func TestH15AWriterThatFailsStoresNothing(t *testing.T) {
	cases := map[string]func(w *world) (*Service, context.Context){
		"the storage": func(w *world) (*Service, context.Context) {
			w.mem.Interrupt = func(_ context.Context, step store.Step, _, _ string) error {
				if step == store.StepEntry {
					return errBoom
				}
				return nil
			}
			return w.enclave(), context.Background()
		},
		"the key": func(w *world) (*Service, context.Context) {
			return New(w.mem, testKeys{fail: errBoom}, w.clock.now, (&counter{}).read), context.Background()
		},
		"the key, malformed": func(w *world) (*Service, context.Context) {
			return New(w.mem, shortKeys{}, w.clock.now, (&counter{}).read), context.Background()
		},
		"the random bytes": func(w *world) (*Service, context.Context) {
			return New(w.mem, testKeys{secret: "root"}, w.clock.now, func([]byte) error { return errBoom }), context.Background()
		},
		"the deadline, passed": func(w *world) (*Service, context.Context) {
			ctx, cancel := context.WithDeadline(context.Background(), time.Unix(0, 0))
			cancel()
			return w.enclave(), ctx
		},
		"the deadline, during the write": func(w *world) (*Service, context.Context) {
			ctx, cancel := context.WithCancel(context.Background())
			w.mem.Interrupt = func(ctx context.Context, step store.Step, _, _ string) error {
				if step == store.StepEntry {
					cancel()
					<-ctx.Done()
				}
				return nil
			}
			return w.enclave(), ctx
		},
	}
	for name, setup := range cases {
		t.Run(name, func(t *testing.T) {
			w := newWorld()
			s, ctx := setup(w)
			r := samples(t)[KindSearch]
			if err := s.Write(ctx, account, r); err == nil {
				t.Fatal("a failed write said it was stored")
			}
			if r.ID != "" || !r.At.IsZero() || r.V != 0 {
				t.Fatal("a failed write named a receipt")
			}
			w.mem.Interrupt = nil
			if got := listAll(t, w.enclave(), account); len(got) != 0 {
				t.Fatalf("%d receipts kept", len(got))
			}
		})
	}
	if err := newWorld().enclave().Write(context.Background(), "", &Receipt{}); err == nil {
		t.Fatal("a receipt was written to no account")
	}
	if err := newWorld().enclave().Write(context.Background(), account, nil); err == nil {
		t.Fatal("no receipt was written")
	}
}

// H15: a document past MaxDocumentBytes is refused whole, never cut; one
// at it is written.
func TestH15ADocumentPastTheBoundIsRefusedNeverCut(t *testing.T) {
	ctx := context.Background()
	w := newWorld()
	// Every digit of the nanoseconds written: the document is as long as
	// Size counts it, byte for byte.
	w.clock.set(time.Date(2026, 11, 16, 10, 0, 0, 123456789, time.UTC))
	s := w.enclave()
	at := func(n int) *Receipt {
		r := samples(t)[KindRefused]
		r.Refused = &Refused{Reason: RefusedOutsideScope, Mailbox: mailboxA}
		base, err := Size(r)
		if err != nil {
			t.Fatal(err)
		}
		r.Refused.Message = strings.Repeat("m", n-base-len(`,"message":""`))
		if got, _ := Size(r); got != n {
			t.Fatalf("built %d bytes, not %d", got, n)
		}
		return r
	}
	over := at(MaxDocumentBytes + 1)
	if err := s.Write(ctx, account, over); !errors.Is(err, ErrTooLarge) || over.ID != "" {
		t.Fatalf("a document past the bound: %v", err)
	}
	if got := listAll(t, s, account); len(got) != 0 {
		t.Fatal("a part of it was kept")
	}
	full := at(MaxDocumentBytes)
	if err := s.Write(ctx, account, full); err != nil {
		t.Fatal(err)
	}
	got := listAll(t, s, account)
	if len(got) != 1 || got[0].Refused.Message != full.Refused.Message {
		t.Fatal("the document at the bound was not kept whole")
	}
	if _, err := NewPage(over); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("a page started past the bound: %v", err)
	}
}

// H16: two enclaves writing an account's receipts at once keep them all,
// their keys in the order of their times.
func TestH16TwoEnclavesWritingAtOnceKeepEveryReceiptInTimeOrder(t *testing.T) {
	ctx := context.Background()
	mem := store.NewMemory()
	c := &clock{at: start.Add(time.Hour), step: time.Microsecond}
	// One enclave's clock a second behind the other's.
	behind := func() time.Time { return c.now().Add(-time.Second) }
	a := New(mem, testKeys{secret: "root"}, c.now, (&counter{}).read)
	b := New(mem, testKeys{secret: "root"}, behind, (&counter{n: 1 << 40}).read)
	// Both draw from the same instant, so only the random bytes tell
	// their identifiers apart.
	still := func() time.Time { return start }
	sa := New(mem, testKeys{secret: "root"}, still, (&counter{}).read)
	sb := New(mem, testKeys{secret: "root"}, still, (&counter{n: 1 << 40}).read)
	var wg sync.WaitGroup
	for _, s := range []*Service{a, b, sa, sb} {
		wg.Go(func() {
			for range 50 {
				r := samples(t)[KindEnded]
				if err := s.Write(ctx, account, r); err != nil {
					t.Error(err)
				}
			}
		})
	}
	wg.Wait()
	got := listAll(t, a, account)
	if len(got) != 200 {
		t.Fatalf("%d of 200 receipts kept", len(got))
	}
	for i := 1; i < len(got); i++ {
		if got[i].ID <= got[i-1].ID || got[i].At.Before(got[i-1].At) {
			t.Fatalf("receipt %d is out of time order", i)
		}
	}
}

// H17: removing the account deletes its receipts' partition, and deleting it
// again RemoveAgainAfter later removes one whose write raced the first;
// deleting a message or a mailbox leaves the receipts.
func TestH17RemovingTheAccountLeavesNoReceiptEvenARacingOne(t *testing.T) {
	ctx := context.Background()
	w := newWorld()
	s := w.enclave()
	if err := s.Write(ctx, account, samples(t)[KindRead]); err != nil {
		t.Fatal(err)
	}
	// The mail goes, and the receipts stay: they outlive it.
	if err := w.mem.DeleteMessage(ctx, mailboxA, "0000018a0b0c0d0e1112131415161718"); err != nil && !errors.Is(err, store.ErrNotFound) {
		t.Fatal(err)
	}
	if err := w.mem.DeletePartition(ctx, "mailbox#"+mailboxA); err != nil {
		t.Fatal(err)
	}
	if got := listAll(t, s, account); len(got) != 1 {
		t.Fatal("deleting the mail deleted its receipt")
	}

	// A write that began before the removal and ends after it.
	held, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	w.mem.Interrupt = func(_ context.Context, step store.Step, partition, _ string) error {
		if step == store.StepEntry && partition == Partition(account) {
			once.Do(func() { close(held); <-release })
		}
		return nil
	}
	done := make(chan error)
	go func() { done <- s.Write(ctx, account, samples(t)[KindSearch]) }()
	<-held
	if err := w.mem.DeletePartition(ctx, Partition(account)); err != nil {
		t.Fatal(err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if got := listAll(t, s, account); len(got) != 1 {
		t.Fatalf("the racing write left %d receipts", len(got))
	}
	w.clock.set(start.Add(RemoveAgainAfter))
	if err := w.mem.DeletePartition(ctx, Partition(account)); err != nil {
		t.Fatal(err)
	}
	if got, err := w.mem.ListEntries(ctx, Partition(account), "", "", 10); err != nil || len(got) != 0 {
		t.Fatalf("%d entries left: %v", len(got), err)
	}
	if RemoveAgainAfter < 2*time.Minute {
		t.Fatal("a receipt's write may last longer than the second removal waits")
	}
}

// H18: nothing of a receipt reaches a log line, whatever happens to it: the
// service logs nothing, and its errors name no receipt, account or content.
func TestH18TheLogHoldsNothingOfAReceipt(t *testing.T) {
	var lines bytes.Buffer
	prevSlog, prevLog := slog.Default(), log.Writer()
	slog.SetDefault(slog.New(slog.NewTextHandler(&lines, nil)))
	log.SetOutput(&lines)
	defer func() { slog.SetDefault(prevSlog); log.SetOutput(prevLog) }()

	ctx := context.Background()
	w := newWorld()
	s := w.enclave()
	var errs []error
	for _, r := range samples(t) {
		errs = append(errs, s.Write(ctx, account, r))
	}
	listed := listAll(t, s, account)
	big := samples(t)[KindRefused]
	big.Refused.Message = strings.Repeat("secret", MaxDocumentBytes)
	errs = append(errs, s.Write(ctx, account, big))
	w.mem.Interrupt = func(context.Context, store.Step, string, string) error { return errBoom }
	errs = append(errs, s.Write(ctx, account, samples(t)[KindRead]))
	_, err := s.List(ctx, account, "", 10)
	errs = append(errs, err)
	w.mem.Interrupt = nil
	e, _ := w.mem.GetEntry(ctx, Partition(account), listed[0].ID)
	e.Value[len(e.Value)/2] ^= 1
	w.mem.SetEntry(*e)
	_, err = s.List(ctx, account, "", 10)
	errs = append(errs, err)
	_, err = s.List(ctx, account, "not an id", 10)
	errs = append(errs, err)
	_, err = New(w.mem, testKeys{fail: errBoom}, w.clock.now, (&counter{}).read).List(ctx, account, "", 10)
	errs = append(errs, err)

	if lines.Len() != 0 {
		t.Fatalf("the service logged:\n%s", lines.String())
	}
	secrets := []string{account, "Invoice 12", "billing@example.com", "secret", "con_", "fam_", "claude.ai", "invoice"}
	for _, r := range listed {
		secrets = append(secrets, r.ID)
	}
	for _, err := range errs {
		if err == nil {
			continue
		}
		for _, s := range secrets {
			if strings.Contains(err.Error(), s) {
				t.Fatalf("an error names %q: %v", s, err)
			}
		}
	}
	if !slices.ContainsFunc(errs, func(err error) bool { return err != nil }) {
		t.Fatal("no failure was exercised")
	}
}

// H23: an answer recorded and then not released gets a second receipt that
// names the first, and the first stays: a receipt may record more than
// left, never less.
func TestH23ATooLateReceiptNamesTheFirstAndTheFirstStays(t *testing.T) {
	ctx := context.Background()
	w := newWorld()
	s := w.enclave()
	first := samples(t)[KindRead]
	if err := s.Write(ctx, account, first); err != nil {
		t.Fatal(err)
	}
	before := stored(t, w, first)
	late := &Receipt{
		Kind: KindRefused, Connection: first.Connection, Device: first.Device, ClientID: first.ClientID, Vendor: first.Vendor, Resource: first.Resource,
		Refused: &Refused{Reason: RefusedTooLate, Receipt: first.ID},
	}
	if err := s.Write(ctx, account, late); err != nil {
		t.Fatal(err)
	}
	got := listAll(t, s, account)
	if len(got) != 2 || got[0].ID != first.ID || got[1].ID != late.ID {
		t.Fatal("both receipts are not kept, in order")
	}
	if got[1].Refused.Reason != RefusedTooLate || got[1].Refused.Receipt != first.ID || !ValidID(got[1].Refused.Receipt) {
		t.Fatal("the too_late receipt does not name the first")
	}
	if !bytes.Equal(stored(t, w, first), before) || len(got[0].Delivered) != 1 {
		t.Fatal("the first receipt changed")
	}
}

// List pages through an account's receipts from the one after which it
// starts, and refuses what is not a receipt to start after.
func TestListPagesInKeyOrder(t *testing.T) {
	ctx := context.Background()
	w := newWorld()
	w.clock.step = time.Millisecond
	s := w.enclave()
	var ids []string
	for range 10 {
		r := samples(t)[KindEnded]
		if err := s.Write(ctx, account, r); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, r.ID)
	}
	page, err := s.List(ctx, account, ids[3], 4)
	if err != nil || len(page) != 4 || page[0].ID != ids[4] || page[3].ID != ids[7] {
		t.Fatalf("the page after the fourth: %v", err)
	}
	if got, err := s.List(ctx, "acc_nobodynobodynobodynobody", "", 4); err != nil || got != nil {
		t.Fatal("an account with no receipts listed some")
	}
	for _, bad := range []struct {
		account, after string
		limit          int
	}{{"", "", 4}, {account, "", 0}, {account, "rcp_xyz", 4}, {account, ids[0] + "0", 4}, {account, "abc_" + ids[0][4:], 4}, {account, ids[0][:IDLength-1] + "g", 4}} {
		if _, err := s.List(ctx, bad.account, bad.after, bad.limit); err == nil {
			t.Fatalf("listing %+v", bad)
		}
	}
	for _, id := range ids {
		if !ValidID(id) {
			t.Fatalf("%s is not a receipt's identifier", id)
		}
	}
	if len(ids[0]) != IDLength {
		t.Fatal("an identifier's length")
	}
}

// shortKeys derives a key no cipher takes.
type shortKeys struct{}

func (shortKeys) Derive(context.Context, string) ([]byte, error) { return []byte("short"), nil }
