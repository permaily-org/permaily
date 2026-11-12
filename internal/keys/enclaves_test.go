package keys

import (
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/store"
)

// atOnce is the storage as two enclaves that start at once see it: each
// reads the records whose name starts with prefix before either writes them.
type atOnce struct {
	*store.Memory
	prefix string
	mu     sync.Mutex
	seen   map[string]int
	both   map[string]chan struct{}
}

func newAtOnce(mem *store.Memory, prefix string) *atOnce {
	return &atOnce{Memory: mem, prefix: prefix, seen: map[string]int{}, both: map[string]chan struct{}{}}
}

func (s *atOnce) GetRecord(ctx context.Context, name string) (*store.Record, error) {
	rec, err := s.Memory.GetRecord(ctx, name)
	if !strings.HasPrefix(name, s.prefix) {
		return rec, err
	}
	s.mu.Lock()
	if s.both[name] == nil {
		s.both[name] = make(chan struct{})
	}
	wait := s.both[name]
	s.seen[name]++
	first := s.seen[name] <= 2
	if s.seen[name] == 2 {
		close(wait)
	}
	s.mu.Unlock()
	if first {
		<-wait
	}
	return rec, err
}

// bootTwo starts two enclaves of an image over records, at once.
func (w *world) bootTwo(im *image, records store.Records) (*Root, *Root) {
	w.t.Helper()
	a, b := w.boot(im), w.boot(im)
	a.cfg.Records, b.cfg.Records = records, records
	return a, b
}

func TestTwoEnclavesOfOneImageStartingTogetherHaveOneRootSecret(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	img := w.release(0xa1, time.Hour)
	// Both read that there is no root record, and both create one.
	a, b := w.bootTwo(img, newAtOnce(w.store, "root-"))
	var wg sync.WaitGroup
	errs := make([]error, 2)
	wg.Go(func() { errs[0] = a.ready(ctx) })
	wg.Go(func() { errs[1] = b.ready(ctx) })
	wg.Wait()
	if errs[0] != nil || errs[1] != nil {
		t.Fatalf("ready: %v, %v", errs[0], errs[1])
	}
	if !bytes.Equal(a.secret, b.secret) {
		t.Fatal("two enclaves of one image started together with two root secrets")
	}
	dk, err := a.NewDataKey(ctx, keyCtx)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := b.Unwrap(ctx, dk.Wrapped, keyCtx); err != nil || !bytes.Equal(got, dk.Plaintext) {
		t.Fatalf("what one sealed the other cannot open: %v", err)
	}
	// A third, later, takes the same.
	if got, err := w.boot(img).Unwrap(ctx, dk.Wrapped, keyCtx); err != nil || !bytes.Equal(got, dk.Plaintext) {
		t.Fatalf("a later enclave: %v", err)
	}
}

func TestTwoEnclavesHandingOverAtOnceGiveTheNextReleaseOneRecord(t *testing.T) {
	w, ctx := newWorld(t), context.Background()
	a, b := w.release(0xa1, 2*time.Hour), w.release(0xb2, time.Hour)
	first := w.boot(a)
	dk, err := first.NewDataKey(ctx, keyCtx)
	if err != nil {
		t.Fatal(err)
	}
	// Two enclaves of the running release are asked for the handoff at once,
	// and both read that the next release has no record yet.
	x, y := w.bootTwo(a, newAtOnce(w.store, RootRecordName(b.pcr0)))
	for _, r := range []*Root{x, y} {
		if err := r.ready(ctx); err != nil {
			t.Fatal(err)
		}
	}
	var wg sync.WaitGroup
	res := make([]*HandoffResult, 2)
	errs := make([]error, 2)
	wg.Go(func() { res[0], errs[0] = w.handoff(x, a, b) })
	wg.Go(func() { res[1], errs[1] = w.handoff(y, a, b) })
	wg.Wait()
	if errs[0] != nil || errs[1] != nil {
		t.Fatalf("handoff: %v, %v", errs[0], errs[1])
	}
	if res[0].Created == res[1].Created {
		t.Fatalf("created %v and %v: one of the two writes the record", res[0].Created, res[1].Created)
	}

	// Two enclaves of the next release open that one record: one secret.
	n1, n2 := w.boot(b), w.boot(b)
	for _, r := range []*Root{n1, n2} {
		if got, err := r.Unwrap(ctx, dk.Wrapped, keyCtx); err != nil || !bytes.Equal(got, dk.Plaintext) {
			t.Fatalf("the next release does not open what the running one stored: %v", err)
		}
	}
	if !bytes.Equal(n1.secret, n2.secret) || !bytes.Equal(n1.secret, first.secret) {
		t.Fatal("not one root secret across the handoff")
	}
}
