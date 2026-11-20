package receipts

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"

	"github.com/permaily-org/permaily/internal/contracts"
	"github.com/permaily-org/permaily/internal/store"
)

// The world of the chain's tests: one table, one store of commits (in
// memory, or a bucket like S3's), enclaves over them, a clock the test
// moves, and the accounts service's answers.

const other = "acc_bbbbbbbbbbbbbbbbbbbbbbbbbb"

// fakeAccounts is what the chain asks of accounts.
type fakeAccounts struct {
	mu      sync.Mutex
	removed map[string]bool
	fail    error
	list    []string
	asked   int
	// removing, when set, is called before Removing answers.
	removing func(account string)
}

func (f *fakeAccounts) Removing(_ context.Context, account string) (bool, error) {
	f.mu.Lock()
	hook := f.removing
	f.asked++
	f.mu.Unlock()
	if hook != nil {
		hook(account)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.removed[account], f.fail
}

func (f *fakeAccounts) AccountRecords(_ context.Context, after string, limit int) (*contracts.AccountList, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail != nil {
		return nil, f.fail
	}
	out := &contracts.AccountList{Accounts: []contracts.AccountRecord{}}
	for _, id := range f.list {
		if id > after && len(out.Accounts) < limit {
			out.Accounts = append(out.Accounts, contracts.AccountRecord{ID: id})
		}
	}
	if len(out.Accounts) == limit {
		out.Next = out.Accounts[limit-1].ID
	}
	return out, nil
}

func (f *fakeAccounts) remove(account string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.removed[account] = true
}

type chainWorld struct {
	t        *testing.T
	mem      *store.Memory
	mc       *store.MemoryCommits
	commits  store.Commits
	clock    *clock
	accounts *fakeAccounts
	random   *counter
	// noticing are the services noticed made, whose notices settle waits
	// for.
	noticing []*Service
}

func newChainWorld(t *testing.T) *chainWorld {
	w := &chainWorld{t: t, mem: store.NewMemory(), mc: store.NewMemoryCommits(), clock: &clock{at: start}, accounts: &fakeAccounts{removed: map[string]bool{}}, random: &counter{}}
	w.mc.Group = CommitGroup
	w.commits = w.mc
	w.mem.Now = w.clock.now
	return w
}

// anchored is one enclave's strict store of the chain's records.
func (w *chainWorld) anchored() *store.Anchored {
	return &store.Anchored{Records: ChainTable{Entries: w.mem}, Commits: w.commits, Strict: true, Now: w.clock.now}
}

// enclave is one enclave's chained service over the world's storage: a
// fresh one is an enclave restarted.
func (w *chainWorld) enclave() *Service {
	w.t.Helper()
	s, err := NewChained(w.mem, testKeys{secret: "root"}, w.clock.now, w.anchored(), w.accounts)
	if err != nil {
		w.t.Fatal(err)
	}
	s.random = w.random.read
	return s
}

// receipt is a receipt to write.
func receipt() *Receipt {
	return &Receipt{Kind: KindEnded, ClientID: "https://client.example/meta", Vendor: "client.example", Resource: contracts.ResourceMCP, Ended: &Ended{Reason: contracts.EndedByReplaced}}
}

// write writes a receipt through s and returns its identifier.
func (w *chainWorld) write(s *Service, account string) string {
	w.t.Helper()
	r := receipt()
	if err := s.Write(context.Background(), account, r); err != nil {
		w.t.Fatal(err)
	}
	return r.ID
}

// writes writes n receipts, one at a time.
func (w *chainWorld) writes(s *Service, account string, n int) []string {
	w.t.Helper()
	ids := make([]string, n)
	for i := range ids {
		ids[i] = w.write(s, account)
	}
	return ids
}

// walkAll walks account's chain through s, page by page.
func walkAll(t *testing.T, s *Service, account string, limit int) []WalkedLink {
	t.Helper()
	var out []WalkedLink
	at := Position{}
	for range 100000 {
		p, err := s.Walk(context.Background(), account, at, limit)
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, p.Links...)
		if p.Done {
			return out
		}
		at = p.Next
	}
	t.Fatal("the walk never ends")
	return nil
}

// shape is what a walk found, compactly: "3:ok[ok ok] 2:lost 1:ok[missing]".
func shape(links []WalkedLink) string {
	var b strings.Builder
	for i, l := range links {
		if i > 0 {
			b.WriteByte(' ')
		}
		fmt.Fprintf(&b, "%d:%s", l.N, l.Verdict)
		if len(l.Receipts) > 0 {
			b.WriteByte('[')
			for k, r := range l.Receipts {
				if k > 0 {
					b.WriteByte(' ')
				}
				b.WriteString(string(r.Verdict))
			}
			b.WriteByte(']')
		}
	}
	return b.String()
}

// merged is a walk's links with the parts of a link split across pages
// joined.
func merged(links []WalkedLink) []WalkedLink {
	var out []WalkedLink
	for _, l := range links {
		if n := len(out); n > 0 && out[n-1].N == l.N {
			out[n-1].Receipts = append(out[n-1].Receipts, l.Receipts...)
			continue
		}
		out = append(out, l)
	}
	return out
}

// linked is every receipt identifier the readable links of a walk name, by
// how many links name it.
func linked(links []WalkedLink) map[string]int {
	out := map[string]int{}
	for _, l := range merged(links) {
		if l.Link != nil {
			for _, r := range l.Link.Receipts {
				out[r.ID]++
			}
		}
	}
	return out
}

// valueKeys are the keys of the stored values of account's link n.
func (w *chainWorld) valueKeys(account string, n uint64) []string {
	var out []string
	for _, e := range w.mem.AllEntries() {
		if e.Partition == Partition(account) && strings.HasPrefix(e.Key, LinkKeyPrefix(n)+"@") {
			out = append(out, e.Key)
		}
	}
	return out
}

// deleteLink deletes the stored values of account's link n.
func (w *chainWorld) deleteLink(account string, n uint64) {
	w.t.Helper()
	for _, k := range w.valueKeys(account, n) {
		if err := w.mem.DeleteEntry(context.Background(), Partition(account), k); err != nil {
			w.t.Fatal(err)
		}
	}
}

// deleteReceipt deletes a receipt's entry, as whoever writes the table can.
func (w *chainWorld) deleteReceipt(account, id string) {
	w.t.Helper()
	if err := w.mem.DeleteEntry(context.Background(), Partition(account), id); err != nil {
		w.t.Fatal(err)
	}
}

// entry is a stored entry, as whoever writes the table reads it.
func (w *chainWorld) entry(partition, key string) store.Entry {
	w.t.Helper()
	e, err := w.mem.GetEntry(context.Background(), partition, key)
	if err != nil {
		w.t.Fatal(err)
	}
	return *e
}

// incidents is how many entries the record of incidents holds.
func (w *chainWorld) incidents(s *Service) uint64 {
	w.t.Helper()
	got, err := s.Incidents(context.Background())
	if err != nil {
		w.t.Fatal(err)
	}
	return got.Recorded
}

// findings are every finding the record of incidents holds, read by a
// fresh enclave.
func (w *chainWorld) findings() []Finding {
	w.t.Helper()
	ctx := context.Background()
	s := w.enclave()
	key, _ := s.chainKey(ctx)
	head, err := s.findHead(ctx, key, incidentSeriesOf)
	if err != nil {
		w.t.Fatal(err)
	}
	var out []Finding
	for n := uint64(1); n <= head.n; n++ {
		j, err := s.readOne(ctx, key, incidentSeriesOf, n)
		if err != nil {
			w.t.Fatal(err)
		}
		if j.verdict == LinkOK {
			var i Incident
			if err := jsonUnmarshal(j.doc, &i); err != nil {
				w.t.Fatal(err)
			}
			out = append(out, i.Findings...)
		}
	}
	return out
}

// uniq is findings without repeats: a finding found by several enclaves,
// or by one restarted, is recorded by each.
func uniq(fs []Finding) []Finding {
	seen := map[Finding]bool{}
	var out []Finding
	for _, f := range fs {
		if !seen[f] {
			seen[f] = true
			out = append(out, f)
		}
	}
	return out
}

// kinds counts findings by kind: "link_lost:1 receipt_missing:2".
func kinds(fs []Finding) string {
	counts := map[string]int{}
	for _, f := range fs {
		counts[f.Kind]++
	}
	var parts []string
	for k, n := range counts {
		parts = append(parts, fmt.Sprintf("%s:%d", k, n))
	}
	slices.Sort(parts)
	return strings.Join(parts, " ")
}

// audit runs a pass of the audit through s, a day after the last.
func (w *chainWorld) audit(s *Service) *AuditPass {
	w.t.Helper()
	p, err := s.Audit(context.Background())
	if err != nil {
		w.t.Fatal(err)
	}
	if p == nil {
		w.t.Fatal("no pass")
	}
	return p
}

// status is what /status reports of receipts through s, as pull request 2
// assembles it.
func (w *chainWorld) status(s *Service) error {
	w.t.Helper()
	ctx := context.Background()
	incidents, err := s.Incidents(ctx)
	if err != nil {
		w.t.Fatal(err)
	}
	pass, err := s.Audited(ctx)
	if err != nil {
		w.t.Fatal(err)
	}
	st := statusOf(true, &incidents, s.Broken(), pass)
	return st.Check(w.clock.now())
}

// bucket is an S3 bucket with Object Lock, as the store's tests have it
// (lockedBucket): every write adds a version, a delete marker hides an
// object from plain requests only, a write with If-None-Match is refused
// where a version that is not a delete marker is the newest, and listings
// carry both of S3's markers.
type bucket struct {
	mu       sync.Mutex
	versions []bver
	next     int
	pageSize int
	lists    int
	// hold, when set, is called before a write lands, without the lock: a
	// write the host holds. lost says the write lands and its answer is
	// lost.
	hold func(key string)
	lost func(key string) bool
	fail error
}

type bver struct {
	key, id string
	body    []byte
	marker  bool
	until   time.Time
}

func (b *bucket) ListObjectVersions(_ context.Context, in *s3.ListObjectVersionsInput, _ ...func(*s3.Options)) (*s3.ListObjectVersionsOutput, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.lists++
	if b.fail != nil {
		return nil, b.fail
	}
	var all []bver
	for _, v := range b.versions {
		if strings.HasPrefix(v.key, aws.ToString(in.Prefix)) {
			all = append(all, v)
		}
	}
	slices.SortStableFunc(all, func(x, y bver) int { return strings.Compare(x.key, y.key) })
	start := 0
	switch {
	case in.KeyMarker != nil && in.VersionIdMarker == nil:
		start = len(all)
		for i, v := range all {
			if v.key > aws.ToString(in.KeyMarker) {
				start = i
				break
			}
		}
	case in.KeyMarker != nil:
		for i, v := range all {
			if v.key == aws.ToString(in.KeyMarker) && v.id == aws.ToString(in.VersionIdMarker) {
				start = i + 1
			}
		}
	}
	size := b.pageSize
	if size == 0 {
		size = 1000
	}
	out := &s3.ListObjectVersionsOutput{}
	for i := start; i < len(all) && i < start+size; i++ {
		v := all[i]
		if v.marker {
			out.DeleteMarkers = append(out.DeleteMarkers, s3types.DeleteMarkerEntry{Key: aws.String(v.key), VersionId: aws.String(v.id)})
		} else {
			out.Versions = append(out.Versions, s3types.ObjectVersion{Key: aws.String(v.key), VersionId: aws.String(v.id)})
		}
		if i == start+size-1 && i < len(all)-1 {
			out.IsTruncated, out.NextKeyMarker, out.NextVersionIdMarker = aws.Bool(true), aws.String(v.key), aws.String(v.id)
		}
	}
	return out, nil
}

func (b *bucket) GetObject(_ context.Context, in *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, v := range b.versions {
		if v.key == aws.ToString(in.Key) && v.id == aws.ToString(in.VersionId) && !v.marker {
			return &s3.GetObjectOutput{Body: io.NopCloser(bytes.NewReader(v.body)), ObjectLockMode: s3types.ObjectLockModeCompliance, ObjectLockRetainUntilDate: aws.Time(v.until)}, nil
		}
	}
	return nil, &s3types.NoSuchKey{}
}

func (b *bucket) PutObject(_ context.Context, in *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	key := aws.ToString(in.Key)
	body, err := io.ReadAll(in.Body)
	if err != nil {
		return nil, err
	}
	if sum := sha256.Sum256(body); aws.ToString(in.ChecksumSHA256) != base64.StdEncoding.EncodeToString(sum[:]) {
		return nil, errors.New("no checksum")
	}
	if b.hold != nil {
		b.hold(key)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if aws.ToString(in.IfNoneMatch) == "*" {
		for i := len(b.versions) - 1; i >= 0; i-- {
			if b.versions[i].key == key {
				if !b.versions[i].marker {
					return nil, &smithy.GenericAPIError{Code: "PreconditionFailed"}
				}
				break
			}
		}
	}
	b.next++
	b.versions = append(b.versions, bver{key: key, id: fmt.Sprintf("v%06d", b.next), body: body, until: aws.ToTime(in.ObjectLockRetainUntilDate)})
	if b.lost != nil && b.lost(key) {
		return nil, errors.New("the answer was lost")
	}
	return &s3.PutObjectOutput{}, nil
}

// hide puts a delete marker over key's newest version.
func (b *bucket) hide(key string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.next++
	b.versions = append(b.versions, bver{key: key, id: fmt.Sprintf("v%06d", b.next), marker: true})
}

// expire deletes every version of the objects whose key ends with suffix,
// as S3 lets anyone once their lock ended.
func (b *bucket) expire(suffix string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.versions = slices.DeleteFunc(b.versions, func(v bver) bool { return strings.HasSuffix(v.key, suffix) })
}

func (b *bucket) listings() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.lists
}

// s3World is a chainWorld whose commits are in a bucket like S3's.
func newS3World(t *testing.T) (*chainWorld, *bucket) {
	w := newChainWorld(t)
	b := &bucket{}
	w.commits = &store.S3Commits{
		S3: b, Bucket: "anchors", Retention: 24 * time.Hour,
		Key:   func(context.Context) ([]byte, error) { return bytes.Repeat([]byte{7}, 32), nil },
		Now:   w.clock.now,
		Group: CommitGroup,
	}
	return w, b
}

// s3Commits is a store of commits of its own over b, as another enclave
// has: its own memory of what it read.
func (w *chainWorld) s3Commits(b *bucket) *store.S3Commits {
	return &store.S3Commits{
		S3: b, Bucket: "anchors", Retention: 24 * time.Hour,
		Key:   func(context.Context) ([]byte, error) { return bytes.Repeat([]byte{7}, 32), nil },
		Now:   w.clock.now,
		Group: CommitGroup,
	}
}

// enclaveOver is an enclave whose commits are commits.
func (w *chainWorld) enclaveOver(commits store.Commits) *Service {
	w.t.Helper()
	a := &store.Anchored{Records: ChainTable{Entries: w.mem}, Commits: commits, Strict: true, Now: w.clock.now}
	s, err := NewChained(w.mem, testKeys{secret: "root"}, w.clock.now, a, w.accounts)
	if err != nil {
		w.t.Fatal(err)
	}
	s.random = w.random.read
	return s
}

// memberKey is the end of the key of link n's commits at version 1.
func memberKey(n uint64) string { return fmt.Sprintf("/%016x/%020d", n, 1) }
