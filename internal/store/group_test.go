package store_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/permaily-org/permaily/internal/store"
)

// The groups of ADR 0037's chain, tested here on names of their own: "g-<n>"
// is member n of group "g", "h-<n>" of group "h", anything else is in no
// group.

func groupOf(name string) (string, string, bool) {
	series, digits, ok := strings.Cut(name, "-")
	n, err := strconv.ParseUint(digits, 10, 64)
	if !ok || err != nil || (series != "g" && series != "h") {
		return "", "", false
	}
	return series, fmt.Sprintf("%016x", n), true
}

func member(n uint64) string { return fmt.Sprintf("%016x", n) }

func gname(n uint64) string { return fmt.Sprintf("g-%d", n) }

// ranged lists a range and returns its members by member, in their order.
func ranged(t *testing.T, r store.RangeReader, from, to string, maxListed int) ([]store.Member, error) {
	t.Helper()
	var out []store.Member
	err := r.HistoryRange(context.Background(), from, to, maxListed, func(m store.Member) error {
		out = append(out, m)
		return nil
	})
	return out, err
}

func members(ms []store.Member) string {
	var b strings.Builder
	for _, m := range ms {
		head := "-"
		if m.History.Head != nil {
			head = fmt.Sprintf("v%d", m.History.Head.Version)
		}
		fmt.Fprintf(&b, "%d:%d:%s:%d ", mustHex(m.Member), m.Objects, head, len(m.History.Forks))
	}
	return strings.TrimSpace(b.String())
}

func mustHex(s string) uint64 {
	n, _ := strconv.ParseUint(s, 16, 64)
	return n
}

func groupedS3(b *lockedBucket) *store.S3Commits {
	c := s3Commits(b, time.Date(2026, 11, 17, 9, 0, 0, 0, time.UTC))
	c.Group = groupOf
	return c
}

// A grouped name's commits are under its group's prefix, by member, and
// read and written there only; a name no group holds keeps its own.
func TestAGroupedNamesCommitsAreKeptTogether(t *testing.T) {
	ctx := context.Background()
	b := &lockedBucket{objectLock: true}
	c := groupedS3(b)
	for _, n := range []uint64{1, 2, 300} {
		if _, err := c.Append(ctx, commitOf(gname(n), 1, "v")); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := c.Append(ctx, commitOf("plain", 1, "v")); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("g"))
	prefix := fmt.Sprintf("commits/g/%x/", sum[:16])
	want := []string{prefix + member(1) + "/00000000000000000001", prefix + member(2) + "/00000000000000000001", prefix + member(300) + "/00000000000000000001"}
	for i, w := range want {
		if b.versions[i].key != w {
			t.Fatalf("commit %d at %s, not %s", i, b.versions[i].key, w)
		}
	}
	if strings.HasPrefix(b.versions[3].key, "commits/g/") {
		t.Fatalf("a name in no group is kept with the groups: %s", b.versions[3].key)
	}
	head, next, err := c.Head(ctx, gname(2))
	if err != nil || head == nil || head.Name != gname(2) || next != 2 {
		t.Fatalf("head %+v next %d: %v", head, next, err)
	}
	h, err := c.History(ctx, gname(300))
	if err != nil || h.Head == nil || h.Head.Name != gname(300) {
		t.Fatalf("history %+v: %v", h, err)
	}
	// Without its group, the same store reads nothing of a grouped name.
	plain := s3Commits(b, time.Now())
	if head, _, err := plain.Head(ctx, gname(2)); err != nil || head != nil {
		t.Fatalf("a store without the group found %+v: %v", head, err)
	}
	// A commit of another member's name copied under this member counts for
	// neither.
	other := b.versions[0]
	b.add(bucketVersion{key: prefix + member(5) + "/00000000000000000001", body: other.body, mode: other.mode, until: other.until})
	ms, err := ranged(t, c, gname(5), gname(5), 0)
	if err != nil || members(ms) != "5:1:-:0" {
		t.Fatalf("members %s: %v", members(ms), err)
	}
}

// HistoryRange reads every member's history in order, complete only: a
// member whose versions come in two responses is merged, delete markers and
// objects that are not commits count as listed, the range ends at its last
// member, and past maxListed it stops with ErrListedMax, having given the
// members complete before.
func TestHistoryRangeReadsCompleteHistories(t *testing.T) {
	ctx := context.Background()
	for _, pageSize := range []int{1, 2, 3, 1000} {
		b := &lockedBucket{objectLock: true, pageSize: pageSize}
		c := groupedS3(b)
		for n := uint64(1); n <= 6; n++ {
			if _, err := c.Append(ctx, commitOf(gname(n), 1, "v")); err != nil {
				t.Fatal(err)
			}
		}
		prefix := strings.TrimSuffix(b.versions[2].key, member(3)+"/00000000000000000001")
		// Member 3 forked through a delete marker; member 4 has junk; 5 has a
		// key that is no version.
		b.add(bucketVersion{key: b.versions[2].key, deleteMarker: true})
		forged := commitOf(gname(3), 1, "other")
		if _, err := groupedS3(b).Append(ctx, forged); err != nil {
			t.Fatal(err)
		}
		b.add(bucketVersion{key: prefix + member(4) + "/00000000000000000002", body: []byte("junk")})
		b.add(bucketVersion{key: prefix + member(5) + "/x"})

		ms, err := ranged(t, c, gname(2), gname(5), 0)
		if err != nil || members(ms) != "2:1:v1:0 3:3:v1:1 4:2:v1:0 5:2:v1:0" {
			t.Fatalf("page size %d: %s: %v", pageSize, members(ms), err)
		}
		ms, err = ranged(t, c, gname(1), "", 0)
		if err != nil || len(ms) != 6 {
			t.Fatalf("page size %d: to the end %s: %v", pageSize, members(ms), err)
		}
		// Seven objects listed reach member 4's second: members 1 to 3 are
		// given, then ErrListedMax.
		ms, err = ranged(t, c, gname(1), gname(6), 6)
		if !errors.Is(err, store.ErrListedMax) || members(ms) != "1:1:v1:0 2:1:v1:0 3:3:v1:1" {
			t.Fatalf("page size %d: bounded %s: %v", pageSize, members(ms), err)
		}
	}
}

// A commit that lands behind a listing's markers is not in that listing,
// and is in the next one that lists its member.
func TestHistoryRangeSeesALateCommitOnTheNextListing(t *testing.T) {
	ctx := context.Background()
	b := &lockedBucket{objectLock: true, pageSize: 1}
	c := groupedS3(b)
	for n := uint64(1); n <= 3; n++ {
		if _, err := c.Append(ctx, commitOf(gname(n), 1, "v")); err != nil {
			t.Fatal(err)
		}
	}
	late := groupedS3(&lockedBucket{objectLock: true})
	late.S3 = b
	landed := false
	b.listed = func() {
		if b.lists == 2 && !landed {
			landed = true
			b.listed = nil
			b.add(bucketVersion{key: b.versions[0].key, deleteMarker: true})
			if _, err := late.Append(ctx, commitOf(gname(1), 1, "late")); err != nil {
				t.Error(err)
			}
		}
	}
	ms, err := ranged(t, c, gname(1), "", 0)
	if err != nil || members(ms) != "1:1:v1:0 2:1:v1:0 3:1:v1:0" {
		t.Fatalf("%s: %v", members(ms), err)
	}
	ms, err = ranged(t, c, gname(1), "", 0)
	if err != nil || members(ms) != "1:3:v1:1 2:1:v1:0 3:1:v1:0" {
		t.Fatalf("the next listing: %s: %v", members(ms), err)
	}
}

func TestHistoryRangeFailures(t *testing.T) {
	ctx := context.Background()
	b := &lockedBucket{objectLock: true}
	c := groupedS3(b)
	if _, err := c.Append(ctx, commitOf(gname(1), 1, "v")); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Append(ctx, commitOf(gname(2), 1, "v")); err != nil {
		t.Fatal(err)
	}
	none := func(store.Member) error { return nil }
	for what, run := range map[string]func() error{
		"no group":      func() error { return s3Commits(b, time.Now()).HistoryRange(ctx, gname(1), "", 0, none) },
		"not grouped":   func() error { return c.HistoryRange(ctx, "plain", "", 0, none) },
		"another group": func() error { return c.HistoryRange(ctx, gname(1), "h-2", 0, none) },
		"backwards":     func() error { return c.HistoryRange(ctx, gname(2), gname(1), 0, none) },
		"to no group":   func() error { return c.HistoryRange(ctx, gname(1), "plain", 0, none) },
		"key": func() error {
			k := groupedS3(b)
			k.Key = func(context.Context) ([]byte, error) { return nil, errBoomStore }
			return k.HistoryRange(ctx, gname(1), "", 0, none)
		},
		"list": func() error {
			b.failList = errBoomStore
			defer func() { b.failList = nil }()
			return c.HistoryRange(ctx, gname(1), "", 0, none)
		},
		"get": func() error {
			b.failGet = errBoomStore
			defer func() { b.failGet = nil }()
			return groupedS3(b).HistoryRange(ctx, gname(1), "", 0, none)
		},
		"each": func() error {
			return c.HistoryRange(ctx, gname(1), "", 0, func(store.Member) error { return errBoomStore })
		},
		"each at the end of a range": func() error {
			return c.HistoryRange(ctx, gname(1), gname(1), 0, func(store.Member) error { return errBoomStore })
		},
		"stuck": func() error {
			s := groupedS3(b)
			s.S3 = &stuckListing{recordingBucket: &recordingBucket{lockedBucket: b}}
			return s.HistoryRange(ctx, gname(1), "", 0, none)
		},
	} {
		if err := run(); err == nil {
			t.Errorf("%s: no error", what)
		}
	}
	// each's error from the member before a later one stops the listing.
	b2 := &lockedBucket{objectLock: true, pageSize: 1}
	c2 := groupedS3(b2)
	for n := uint64(1); n <= 2; n++ {
		if _, err := c2.Append(ctx, commitOf(gname(n), 1, "v")); err != nil {
			t.Fatal(err)
		}
	}
	calls := 0
	err := c2.HistoryRange(ctx, gname(1), "", 0, func(store.Member) error { calls++; return errBoomStore })
	if !errors.Is(err, errBoomStore) || calls != 1 {
		t.Fatalf("%d calls: %v", calls, err)
	}
}

var errBoomStore = errors.New("boom")

// MemoryCommits lists a group as S3Commits does: every commit, copy and
// object that is not a commit counts.
func TestMemoryCommitsListAGroup(t *testing.T) {
	ctx := context.Background()
	m := store.NewMemoryCommits()
	m.Group = groupOf
	for n := uint64(1); n <= 5; n++ {
		if _, err := m.Append(ctx, commitOf(gname(n), 1, "v")); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := m.Append(ctx, commitOf("h-1", 1, "v")); err != nil {
		t.Fatal(err)
	}
	m.Fork(gname(2), 1, sha256.Sum256([]byte("other")))
	m.Copy(gname(3), 1, false)
	m.Take(gname(4), 2)
	m.Take(gname(4), 3)
	m.Forget(gname(5))
	m.Take("plain", 1)
	ms, err := ranged(t, m, gname(1), "", 0)
	if err != nil || members(ms) != "1:1:v1:0 2:2:v1:1 3:2:v1:0 4:3:v1:0" {
		t.Fatalf("%s: %v", members(ms), err)
	}
	if len(ms[2].History.Unlocked) != 0 {
		t.Fatal("a locked commit with an unlocked copy is unlocked")
	}
	ms, err = ranged(t, m, gname(2), gname(3), 0)
	if err != nil || members(ms) != "2:2:v1:1 3:2:v1:0" {
		t.Fatalf("%s: %v", members(ms), err)
	}
	ms, err = ranged(t, m, gname(1), "", 4)
	if !errors.Is(err, store.ErrListedMax) || members(ms) != "1:1:v1:0 2:2:v1:1" {
		t.Fatalf("%s: %v", members(ms), err)
	}
	if err := m.HistoryRange(ctx, gname(1), "", 0, func(store.Member) error { return errBoomStore }); !errors.Is(err, errBoomStore) {
		t.Fatal(err)
	}
	if err := store.NewMemoryCommits().HistoryRange(ctx, gname(1), "", 0, nil); err == nil {
		t.Fatal("a range without a group")
	}
	m.Take(gname(9), 1)
	ms, err = ranged(t, m, gname(9), "", 0)
	if err != nil || members(ms) != "9:1:-:0" {
		t.Fatalf("%s: %v", members(ms), err)
	}
}

// ReadRange reads a group strictly from one listing, reports a fork and a
// commit without its lock, and ReadListed reads each value by its commit:
// ErrNotFound with no commit, ErrForked, ErrLost (reported), or the value.
func TestReadRangeReadsAGroupStrictly(t *testing.T) {
	ctx := context.Background()
	commits := store.NewMemoryCommits()
	commits.Group = groupOf
	records := store.NewMemory()
	a := &store.Anchored{Records: records, Commits: commits, Strict: true}
	for n := uint64(1); n <= 4; n++ {
		if err := a.CreateNew(ctx, &store.Record{Name: gname(n), Value: []byte("value " + strconv.FormatUint(n, 10))}); err != nil {
			t.Fatal(err)
		}
	}
	commits.Fork(gname(2), 1, sha256.Sum256([]byte("other")))
	commits.Copy(gname(3), 1, false)
	commits.Forget(gname(3))
	for _, name := range records.RecordNames() {
		if strings.HasPrefix(name, gname(4)+"@") {
			if err := records.DeleteRecord(ctx, name); err != nil {
				t.Fatal(err)
			}
		}
	}
	commits.Take(gname(5), 1)
	reader := &store.Anchored{Records: records, Commits: commits, Strict: true}
	var got []string
	err := reader.ReadRange(ctx, gname(1), "", 0, func(m store.Member) error {
		rec, err := reader.ReadListed(ctx, m)
		switch {
		case err == nil:
			got = append(got, string(rec.Value))
		case errors.Is(err, store.ErrLost):
			got = append(got, "lost")
		case errors.Is(err, store.ErrForked):
			got = append(got, "forked")
		case errors.Is(err, store.ErrNotFound):
			got = append(got, "none")
		default:
			t.Fatal(err)
		}
		return nil
	})
	if err != nil || strings.Join(got, ",") != "value 1,forked,value 3,lost,none" {
		t.Fatalf("%v: %v", got, err)
	}
	var fork, unlocked, lost bool
	for _, r := range reader.Regressions() {
		fork = fork || (r.Fork && r.Name == gname(2))
		unlocked = unlocked || (r.Unlocked && r.Name == gname(3))
		lost = lost || (r.Lost && r.Name == gname(4))
	}
	if !fork || !unlocked || !lost {
		t.Fatalf("regressions %+v", reader.Regressions())
	}
	if _, err := reader.ReadListed(ctx, store.Member{}); !errors.Is(err, store.ErrNotFound) {
		t.Fatal(err)
	}
	failing := &store.Anchored{Records: failingRecords{records}, Commits: commits, Strict: true}
	err = failing.ReadRange(ctx, gname(1), gname(1), 0, func(m store.Member) error {
		_, err := failing.ReadListed(ctx, m)
		return err
	})
	if !errors.Is(err, errBoomStore) {
		t.Fatal(err)
	}
	if err := (&store.Anchored{Records: records, Commits: commits}).ReadRange(ctx, gname(1), "", 0, nil); err == nil {
		t.Fatal("a range read without Strict")
	}
	if err := (&store.Anchored{Records: records, Commits: plainCommits{commits}, Strict: true}).ReadRange(ctx, gname(1), "", 0, nil); err == nil {
		t.Fatal("a range read from a store that lists no group")
	}
}

type failingRecords struct{ *store.Memory }

func (failingRecords) GetRecord(context.Context, string) (*store.Record, error) {
	return nil, errBoomStore
}

// plainCommits is a store of commits that lists no group.
type plainCommits struct{ m *store.MemoryCommits }

func (p plainCommits) Head(ctx context.Context, name string) (*store.Commit, uint64, error) {
	return p.m.Head(ctx, name)
}

func (p plainCommits) Append(ctx context.Context, c store.Commit) (*store.Commit, error) {
	return p.m.Append(ctx, c)
}

// Forget drops a record from what the enclave remembers of it.
func TestForgetDropsWhatWasSeen(t *testing.T) {
	ctx := context.Background()
	commits := store.NewMemoryCommits()
	a := &store.Anchored{Records: store.NewMemory(), Commits: commits, Strict: true}
	for _, name := range []string{"a", "b"} {
		if err := a.CreateNew(ctx, &store.Record{Name: name, Value: []byte("v")}); err != nil {
			t.Fatal(err)
		}
	}
	a.Forget("a")
	a.Forget("never")
	seen := a.Seen()
	if len(seen) != 1 || seen[0].Name != "b" {
		t.Fatalf("seen %+v", seen)
	}
	// Forgotten, its commit going is not taken for the store going back.
	commits.Forget("a")
	if _, err := a.GetRecord(ctx, "a"); !errors.Is(err, store.ErrNotFound) {
		t.Fatal(err)
	}
}
