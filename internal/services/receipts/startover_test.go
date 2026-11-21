package receipts

import (
	"bytes"
	"context"
	"crypto/sha256"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/store"
)

// A deployment that starts over (docs/operations.md, "A new deployment, or
// losing everything") keeps the anchors bucket: the earlier deployment's
// commits stay there, locked, and are not this deployment's (their MAC is
// under the key of a root secret that is gone). The new deployment's
// records go on after them, at the version above theirs (store.History's
// Next), so its first record of a number is not at version 1.

// startedOver is an enclave of a deployment started over on w's table and
// bucket b: a new root secret, and so a new key for its commits. A fresh
// one is that enclave restarted.
func (w *chainWorld) startedOver(b *bucket) *Service {
	w.t.Helper()
	commits := &store.S3Commits{
		S3: b, Bucket: "anchors", Retention: 24 * time.Hour,
		Key:   func(context.Context) ([]byte, error) { return bytes.Repeat([]byte{9}, 32), nil },
		Now:   w.clock.now,
		Group: CommitGroup,
	}
	a := &store.Anchored{Records: ChainTable{Entries: w.mem}, Commits: commits, Strict: true, Now: w.clock.now}
	s, err := NewChained(w.mem, testKeys{secret: "started over"}, w.clock.now, a, w.accounts)
	if err != nil {
		w.t.Fatal(err)
	}
	s.random = w.random.read
	return s
}

// L18 (the staging round of ADR 0039 to 0041, 2026-11-20): after a
// deployment starts over on an anchors bucket that holds an earlier
// deployment's passes, catalogue and record of incidents, the new
// deployment's first pass is reported at once, by every enclave and after
// a restart, and its own records of those numbers are read as made once:
// no finding, no incident, and /status's check passes.
func TestL18ANewDeploymentReportsItsFirstPass(t *testing.T) {
	ctx := context.Background()
	w, b := newS3World(t)
	earlier := w.enclaveOver(w.s3Commits(b))
	ids := w.writes(earlier, account, 2)
	w.deleteReceipt(account, ids[0])
	w.clock.set(day(1))
	if p := w.audit(earlier); p.P != 1 || p.Audit.Broken["receipt_missing"] != 1 {
		t.Fatalf("the earlier deployment's pass: %+v", p)
	}
	if n := w.incidents(w.enclaveOver(w.s3Commits(b))); n != 1 {
		t.Fatalf("the earlier deployment's incidents: %d", n)
	}

	// Started over an hour later: the earlier deployment's records are
	// nothing to the new one.
	w.clock.set(day(1).Add(time.Hour))
	s := w.startedOver(b)
	if got, err := s.Audited(ctx); err != nil || got != nil {
		t.Fatalf("a pass before the first: %+v %v", got, err)
	}
	if inc, err := s.Incidents(ctx); err != nil || inc.Recorded != 0 {
		t.Fatalf("incidents before any: %+v %v", inc, err)
	}
	mine := w.writes(s, other, 2)
	p := w.audit(w.startedOver(b))
	if p.P != 1 || p.Audit.Chains != 1 || p.Audit.Catalogue != 1 {
		t.Fatalf("the first pass: %+v", p)
	}
	for k, n := range p.Audit.Broken {
		if n != 0 {
			t.Fatalf("the first pass found %s: %+v", k, p.Audit.Broken)
		}
	}
	for _, r := range []*Service{s, w.startedOver(b)} {
		got, err := r.Audited(ctx)
		if err != nil || got == nil || got.P != p.P || !got.Audit.Began.Equal(p.Audit.Began) {
			t.Fatalf("the first pass is not reported: %+v %v", got, err)
		}
		if err := w.status(r); err != nil {
			t.Fatalf("status: %v", err)
		}
	}
	walked := walkAll(t, w.startedOver(b), other, 10)
	for _, l := range walked {
		if l.Verdict != LinkOK {
			t.Fatalf("link %d %s", l.N, l.Verdict)
		}
	}
	// A pass is not due again until a day after the first began.
	if again, err := w.startedOver(b).Audit(ctx); err != nil || again != nil {
		t.Fatalf("a pass not due: %+v %v", again, err)
	}
	// Its own record of incidents begins at 1, above the earlier one's, and
	// its entries are read as made once: the second names the first, no
	// break between them.
	w.deleteReceipt(other, mine[0])
	w.clock.set(day(2).Add(time.Hour))
	if p := w.audit(w.startedOver(b)); p.P != 2 || p.Audit.Broken["receipt_missing"] != 1 {
		t.Fatalf("the second pass: %+v", p)
	}
	w.deleteReceipt(other, mine[1])
	w.clock.set(day(3).Add(time.Hour))
	w.audit(w.startedOver(b))
	r := w.startedOver(b)
	if n := w.incidents(r); n != 2 {
		t.Fatalf("incidents: %d", n)
	}
	key, err := r.chainKey(ctx)
	if err != nil {
		t.Fatal(err)
	}
	first, err := r.readOne(ctx, key, incidentSeriesOf, 1)
	if err != nil || first.verdict != LinkOK {
		t.Fatalf("incident 1: %s %v", first.verdict, err)
	}
	second, err := r.readOne(ctx, key, incidentSeriesOf, 2)
	if err != nil || second.verdict != LinkOK || second.p.br != "" || second.p.prev == nil || *second.p.prev != first.digest {
		t.Fatalf("incident 2: %s %+v %v", second.verdict, second.p, err)
	}
}

// What is checked of a record made once is still checked when it is not at
// version 1: a second commit of this deployment's at another version of the
// same record (made twice) is forked, its pass is not reported, the next
// pass goes on after it, and a link made twice is counted.
func TestL18ARecordThisDeploymentMadeTwiceIsForked(t *testing.T) {
	ctx := context.Background()
	w := newChainWorld(t)
	s := w.enclave()
	w.writes(s, account, 2)
	w.clock.set(day(1))
	first := w.audit(w.enclave())
	// Something that is not a commit took version 1 of the second pass's
	// number, then this deployment made it twice, at versions 2 and 3.
	w.mc.Take(AuditName(2), 1)
	for v := uint64(2); v <= 3; v++ {
		if _, err := w.mc.Append(ctx, store.Commit{Name: AuditName(2), Version: v, Digest: sha256.Sum256([]byte{byte(v)}), At: day(1)}); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := w.enclave().Audited(ctx); err != nil || got == nil || got.P != first.P {
		t.Fatalf("a pass made twice was reported: %+v %v", got, err)
	}
	if _, err := w.mc.Append(ctx, store.Commit{Name: LinkName(account, 2), Version: 2, Digest: sha256.Sum256([]byte("x")), At: start}); err != nil {
		t.Fatal(err)
	}
	w.clock.set(day(2))
	p := w.audit(w.enclave())
	if p.P != 3 || p.Audit.Broken["link_forked"] != 1 {
		t.Fatalf("%+v", p)
	}
	if got, err := w.enclave().Audited(ctx); err != nil || got == nil || got.P != 3 {
		t.Fatalf("%+v %v", got, err)
	}
}
