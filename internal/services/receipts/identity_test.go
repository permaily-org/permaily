package receipts

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/envelope"
	"github.com/permaily-org/permaily/internal/store"
)

// lostAnswers stores every entry and then, while lose is set, answers an
// error, as storage whose answer never came back does.
type lostAnswers struct {
	store.Entries
	lose bool
}

func (l *lostAnswers) PutEntry(ctx context.Context, e *store.Entry) error {
	if err := l.Entries.PutEntry(ctx, e); err != nil {
		return err
	}
	if l.lose {
		return errBoom
	}
	return nil
}

// H35 (the review of pull request 161, finding 1): a receipt that comes
// with its identity is stored under it, once. Written again after a write
// whose answer was lost, it is the receipt already there and Write
// succeeds, storing nothing more; another document under that identity is
// refused, and nothing is stored over it.
func TestH35AReceiptWithItsIdentityIsStoredOnce(t *testing.T) {
	w := newWorld()
	lost := &lostAnswers{Entries: w.mem, lose: true}
	s := New(lost, testKeys{secret: "root"}, w.clock.now, (&counter{}).read)
	at := start.Add(-time.Minute)
	id, err := NewID(at)
	if err != nil || !ValidID(id) {
		t.Fatalf("an identity: %q, %v", id, err)
	}
	r := samples(t)[KindRefusals]
	r.ID, r.At = id, at
	if err := s.Write(context.Background(), account, r); err == nil {
		t.Fatal("a lost answer was taken for a stored receipt")
	}
	lost.lose = false
	for range 3 {
		again := *samples(t)[KindRefusals]
		again.ID, again.At = id, at
		if err := s.Write(context.Background(), account, &again); err != nil {
			t.Fatalf("the same receipt written again: %v", err)
		}
		if again.V != Version || !again.At.Equal(at) || again.ID != id {
			t.Fatalf("the receipt written again: %+v", again)
		}
	}
	got := listAll(t, w.enclave(), account)
	if len(got) != 1 || got[0].ID != id || !got[0].At.Equal(at) {
		t.Fatalf("%d receipts stored, want the one", len(got))
	}
	// Another document under the identity is not this receipt.
	other := *samples(t)[KindRefusals]
	other.ID, other.At = id, at
	other.Refusals = &Refusals{From: at, Until: at.Add(time.Minute), Recorded: 1}
	if err := s.Write(context.Background(), account, &other); !errors.Is(err, ErrIdentityTaken) {
		t.Fatalf("another document under a taken identity: %v", err)
	}
	if got := listAll(t, w.enclave(), account); len(got) != 1 || !reflect.DeepEqual(got[0].Refusals, samples(t)[KindRefusals].Refusals) {
		t.Fatal("the stored receipt changed")
	}
	// An identity needs its time, and an identifier's shape.
	for _, bad := range []Receipt{{ID: id}, {ID: "rcp_x", At: at}} {
		if err := s.Write(context.Background(), account, &bad); err == nil {
			t.Fatalf("a receipt with %+v was written", bad)
		}
	}
	// Past the bound, or with the storage, the key or the context failing,
	// nothing is stored.
	big := *samples(t)[KindRefusals]
	big.ID, big.At, big.ClientID = id, at, string(make([]byte, MaxDocumentBytes))
	if err := s.Write(context.Background(), "acc_bbbbbbbbbbbbbbbbbbbbbbbbbb", &big); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("a receipt past the bound: %v", err)
	}
	keyless := New(w.mem, testKeys{fail: errBoom}, w.clock.now, nil)
	if err := keyless.Write(context.Background(), "acc_bbbbbbbbbbbbbbbbbbbbbbbbbb", r); err == nil {
		t.Fatal("written without its key")
	}
	if err := New(w.mem, shortKeys{}, w.clock.now, nil).Write(context.Background(), "acc_bbbbbbbbbbbbbbbbbbbbbbbbbb", r); err == nil {
		t.Fatal("written under a key no cipher takes")
	}
	unwritable := &Receipt{ID: id, At: at, Consent: &Consent{EndsAt: time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC)}}
	if err := s.Write(context.Background(), "acc_bbbbbbbbbbbbbbbbbbbbbbbbbb", unwritable); err == nil {
		t.Fatal("a document that cannot be written was written")
	}
	w.mem.Interrupt = func(_ context.Context, step store.Step, _, _ string) error {
		if step == store.StepEntry {
			return errBoom
		}
		return nil
	}
	if err := s.Write(context.Background(), "acc_bbbbbbbbbbbbbbbbbbbbbbbbbb", r); err == nil {
		t.Fatal("written while the storage failed")
	}
	w.mem.Interrupt = nil
	broken := New(&lostAnswers{Entries: failingGets{w.mem}}, testKeys{secret: "root"}, w.clock.now, nil)
	if err := broken.Write(context.Background(), account, r); err == nil || errors.Is(err, ErrIdentityTaken) {
		t.Fatalf("a taken identity that could not be read: %v", err)
	}
	if got := listAll(t, w.enclave(), "acc_bbbbbbbbbbbbbbbbbbbbbbbbbb"); len(got) != 0 {
		t.Fatalf("%d receipts stored by failed writes", len(got))
	}
}

// failingGets is storage whose reads of an entry fail.
type failingGets struct{ store.Entries }

func (failingGets) GetEntry(context.Context, string, string) (*store.Entry, error) {
	return nil, errBoom
}

// H13 (the review of pull request 161): a version 1 document, as step 5's
// first release wrote them, reads as it was written under version 2.
func TestH13AVersionOneDocumentReadsAsWritten(t *testing.T) {
	w := newWorld()
	s := w.enclave()
	r := samples(t)[KindRead]
	if err := s.Write(context.Background(), account, r); err != nil {
		t.Fatal(err)
	}
	e, err := w.mem.GetEntry(context.Background(), Partition(account), r.ID)
	if err != nil {
		t.Fatal(err)
	}
	old := *r
	old.V = 1
	raw, err := json.Marshal(&old)
	if err != nil {
		t.Fatal(err)
	}
	key, err := testKeys{secret: "root"}.Derive(context.Background(), Purpose)
	if err != nil {
		t.Fatal(err)
	}
	e.Value, err = envelope.SealText(key, raw, aad(e.Partition, e.Key))
	if err != nil {
		t.Fatal(err)
	}
	got, err := open(key, e)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, &old) {
		t.Fatalf("version one changed: %#v", got)
	}
}
