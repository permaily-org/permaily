package store_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"

	"github.com/permaily-org/permaily/internal/store"
)

// lockedBucket behaves like an S3 bucket with Object Lock, as far as the spike
// of 2026-11-11 showed: every write adds a version, nothing removes one, a
// delete marker hides an object from plain requests only, and a write that
// asks for a lock is refused where there is no Object Lock.
type lockedBucket struct {
	objectLock bool
	pageSize   int
	versions   []bucketVersion
	next       int
	failList   error
	failGet    error
	failPut    error
	lists      int
}

type bucketVersion struct {
	key, id      string
	body         []byte
	deleteMarker bool
	mode         s3types.ObjectLockMode
	until        time.Time
}

func (b *lockedBucket) add(v bucketVersion) string {
	b.next++
	v.id = fmt.Sprintf("v%04d", b.next)
	b.versions = append(b.versions, v)
	return v.id
}

// latest is the newest version at a key: what a plain request sees.
func (b *lockedBucket) latest(key string) *bucketVersion {
	for i := len(b.versions) - 1; i >= 0; i-- {
		if b.versions[i].key == key {
			return &b.versions[i]
		}
	}
	return nil
}

func (b *lockedBucket) ListObjectVersions(_ context.Context, in *s3.ListObjectVersionsInput, _ ...func(*s3.Options)) (*s3.ListObjectVersionsOutput, error) {
	b.lists++
	if b.failList != nil {
		return nil, b.failList
	}
	var all []bucketVersion
	for _, v := range b.versions {
		if strings.HasPrefix(v.key, aws.ToString(in.Prefix)) {
			all = append(all, v)
		}
	}
	slices.SortStableFunc(all, func(x, y bucketVersion) int { return strings.Compare(x.key, y.key) })
	start := 0
	if in.KeyMarker != nil {
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
		if v.deleteMarker {
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

func (b *lockedBucket) GetObject(_ context.Context, in *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	if b.failGet != nil {
		return nil, b.failGet
	}
	for _, v := range b.versions {
		if v.key == aws.ToString(in.Key) && v.id == aws.ToString(in.VersionId) && !v.deleteMarker {
			out := &s3.GetObjectOutput{Body: io.NopCloser(bytes.NewReader(v.body)), ObjectLockMode: v.mode}
			if !v.until.IsZero() {
				out.ObjectLockRetainUntilDate = aws.Time(v.until)
			}
			return out, nil
		}
	}
	return nil, &s3types.NoSuchKey{}
}

func (b *lockedBucket) PutObject(_ context.Context, in *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	if b.failPut != nil {
		return nil, b.failPut
	}
	if in.ObjectLockMode != "" && !b.objectLock {
		return nil, &smithy.GenericAPIError{Code: "InvalidRequest", Message: "Bucket is missing ObjectLockConfiguration"}
	}
	key := aws.ToString(in.Key)
	if aws.ToString(in.IfNoneMatch) == "*" {
		if last := b.latest(key); last != nil && !last.deleteMarker {
			return nil, &smithy.GenericAPIError{Code: "PreconditionFailed", Message: "At least one of the pre-conditions you specified did not hold"}
		}
	}
	body, err := io.ReadAll(in.Body)
	if err != nil {
		return nil, err
	}
	id := b.add(bucketVersion{key: key, body: body, mode: in.ObjectLockMode, until: aws.ToTime(in.ObjectLockRetainUntilDate)})
	return &s3.PutObjectOutput{VersionId: aws.String(id)}, nil
}

var commitKey = bytes.Repeat([]byte{7}, 32)

func s3Commits(b *lockedBucket, now time.Time) *store.S3Commits {
	return &store.S3Commits{
		S3: b, Bucket: "anchors", Retention: 24 * time.Hour,
		Key: func(context.Context) ([]byte, error) { return commitKey, nil },
		Now: func() time.Time { return now },
	}
}

func commitOf(name string, version uint64, value string) store.Commit {
	return store.Commit{Name: name, Version: version, Digest: sha256.Sum256([]byte(value)), At: time.Date(2026, 11, 11, 7, 0, 0, 0, time.UTC)}
}

func TestS3Commits(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 11, 8, 0, 0, 0, time.UTC)
	b := &lockedBucket{objectLock: true}
	c := s3Commits(b, now)

	if head, next, err := c.Head(ctx, "tls"); err != nil || head != nil || next != 1 {
		t.Fatalf("an empty store: %+v, %d, %v", head, next, err)
	}
	for v := uint64(1); v <= 3; v++ {
		if err := c.Append(ctx, commitOf("tls", v, fmt.Sprint("value ", v))); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.Append(ctx, commitOf("mailbox", 1, "m")); err != nil {
		t.Fatal(err)
	}
	head, next, err := c.Head(ctx, "tls")
	want := commitOf("tls", 3, "value 3")
	want.LockedUntil = now.Add(24 * time.Hour)
	if err != nil || head == nil || *head != want || next != 4 {
		t.Fatalf("head %+v, next %d, %v; want %+v", head, next, err, want)
	}
	// Every object was written locked, in compliance mode, under a key that
	// does not show the record's name.
	for _, v := range b.versions {
		if v.mode != s3types.ObjectLockModeCompliance || !v.until.Equal(now.Add(24*time.Hour)) || !strings.HasPrefix(v.key, "commits/") || strings.Contains(v.key, "tls") {
			t.Fatalf("object %+v", v)
		}
	}
	if err := c.Append(ctx, commitOf("tls", 3, "another")); !errors.Is(err, store.ErrExists) {
		t.Fatalf("a version that is taken: %v", err)
	}

	// The listing comes in pages.
	b.pageSize = 2
	b.lists = 0
	if head, next, err := c.Head(ctx, "tls"); err != nil || head.Version != 3 || next != 4 || b.lists != 2 {
		t.Fatalf("in pages: %+v, %d, %v, %d requests", head, next, err, b.lists)
	}
	b.pageSize = 0

	// Another deployment (another root secret) made none of these.
	other := s3Commits(b, now)
	other.Key = func(context.Context) ([]byte, error) { return bytes.Repeat([]byte{8}, 32), nil }
	if head, next, err := other.Head(ctx, "tls"); err != nil || head != nil || next != 4 {
		t.Fatalf("with another key: %+v, %d, %v", head, next, err)
	}
}

// What an administrator of the account, or the parent instance with the
// enclave's credentials, can do to the bucket: add. None of it moves the head
// back.
func TestS3CommitsWithAnAdversary(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 11, 8, 0, 0, 0, time.UTC)
	setup := func(t *testing.T) (*lockedBucket, *store.S3Commits, []bucketVersion) {
		t.Helper()
		b := &lockedBucket{objectLock: true}
		c := s3Commits(b, now)
		for v := uint64(1); v <= 3; v++ {
			if err := c.Append(ctx, commitOf("tls", v, fmt.Sprint("value ", v))); err != nil {
				t.Fatal(err)
			}
		}
		return b, c, slices.Clone(b.versions)
	}
	head := func(t *testing.T, c *store.S3Commits, version, next uint64) {
		t.Helper()
		got, n, err := c.Head(ctx, "tls")
		if err != nil || got == nil || got.Version != version || got.Digest != sha256.Sum256(fmt.Append(nil, "value ", version)) || n != next {
			t.Fatalf("head %+v, next %d, %v; want version %d and next %d", got, n, err, version, next)
		}
	}

	t.Run("a delete marker over the newest commit", func(t *testing.T) {
		b, c, written := setup(t)
		b.add(bucketVersion{key: written[2].key, deleteMarker: true})
		head(t, c, 3, 4)
		// The slot looks free to a plain request, and a write there adds a
		// version: the store still answers with one commit for version 3.
		if err := c.Append(ctx, commitOf("tls", 3, "value 3")); err != nil {
			t.Fatal(err)
		}
		head(t, c, 3, 4)
	})
	t.Run("an earlier commit written over the newest", func(t *testing.T) {
		b, c, written := setup(t)
		b.add(bucketVersion{key: written[2].key, body: written[0].body})
		head(t, c, 3, 4)
	})
	t.Run("an earlier commit copied to a later version", func(t *testing.T) {
		b, c, written := setup(t)
		b.add(bucketVersion{key: strings.Replace(written[2].key, "0003", "0009", 1), body: written[0].body})
		head(t, c, 3, 10)
	})
	t.Run("another record's commit copied here", func(t *testing.T) {
		b, c, written := setup(t)
		if err := c.Append(ctx, commitOf("mailbox", 9, "value 9")); err != nil {
			t.Fatal(err)
		}
		theirs := b.versions[len(b.versions)-1]
		b.add(bucketVersion{key: strings.Replace(written[2].key, "0003", "0009", 1), body: theirs.body})
		head(t, c, 3, 10)
	})
	t.Run("things that are not commits", func(t *testing.T) {
		b, c, written := setup(t)
		prefix := strings.TrimSuffix(written[0].key, "00000000000000000001")
		for key, body := range map[string]string{
			prefix + "00000000000000000004": "not JSON",
			prefix + "00000000000000000005": `{"name":"tls","version":5,"digest":"00","mac":"00"}`,
			prefix + "00000000000000000006": `{"name":"tls","version":6,"digest":"` + strings.Repeat("ab", 32) + `","at":1,"mac":"` + strings.Repeat("cd", 32) + `"}`,
			prefix + "00000000000000000007": strings.Repeat("x", 5000),
			prefix + "7":                    string(written[2].body),
			prefix + "latest":               string(written[2].body),
			prefix + "00000000000000000000": string(written[2].body),
		} {
			b.add(bucketVersion{key: key, body: []byte(body)})
		}
		head(t, c, 3, 8)
		// The next commit goes after everything that is there.
		if err := c.Append(ctx, commitOf("tls", 8, "value 8")); err != nil {
			t.Fatal(err)
		}
		head(t, c, 8, 9)
	})
	t.Run("two commits at one version", func(t *testing.T) {
		// Two enclaves of the deployment that wrote at once, each over the
		// other's delete marker or not: every reader takes the same one.
		b, c, written := setup(t)
		twin := commitOf("tls", 3, "the other enclave's")
		b.add(bucketVersion{key: written[2].key, deleteMarker: true})
		if err := c.Append(ctx, twin); err != nil {
			t.Fatal(err)
		}
		first, _, err := c.Head(ctx, "tls")
		if err != nil || first.Version != 3 {
			t.Fatalf("head %+v, %v", first, err)
		}
		for range 3 {
			again, _, err := c.Head(ctx, "tls")
			if err != nil || *again != *first {
				t.Fatalf("head changed: %+v, then %+v", first, again)
			}
		}
	})
	t.Run("more junk than is read", func(t *testing.T) {
		b, c, written := setup(t)
		prefix := strings.TrimSuffix(written[0].key, "00000000000000000001")
		for i := range 600 {
			b.add(bucketVersion{key: fmt.Sprintf("%s%020d", prefix, 100+i), body: []byte("junk")})
		}
		if _, _, err := c.Head(ctx, "tls"); err == nil || !strings.Contains(err.Error(), "not commits above the newest commit") {
			t.Fatalf("got %v", err)
		}
		for i := range 10000 {
			b.add(bucketVersion{key: fmt.Sprintf("%s%020d", prefix, 1000+i), body: []byte("junk")})
		}
		if _, _, err := c.Head(ctx, "tls"); err == nil || !strings.Contains(err.Error(), "more than 10000 objects") {
			t.Fatalf("got %v", err)
		}
	})
}

func TestS3CommitsFailures(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 11, 8, 0, 0, 0, time.UTC)
	down := errors.New("s3 unreachable")

	t.Run("a bucket without Object Lock takes no commit", func(t *testing.T) {
		c := s3Commits(&lockedBucket{}, now)
		if err := c.Append(ctx, commitOf("tls", 1, "v")); err == nil || errors.Is(err, store.ErrExists) || !strings.Contains(err.Error(), "ObjectLockConfiguration") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("S3 fails", func(t *testing.T) {
		b := &lockedBucket{objectLock: true}
		c := s3Commits(b, now)
		if err := c.Append(ctx, commitOf("tls", 1, "v")); err != nil {
			t.Fatal(err)
		}
		b.failList = down
		if _, _, err := c.Head(ctx, "tls"); !errors.Is(err, down) {
			t.Fatalf("list: %v", err)
		}
		b.failList, b.failGet = nil, down
		if _, _, err := c.Head(ctx, "tls"); !errors.Is(err, down) {
			t.Fatalf("get: %v", err)
		}
		b.failGet, b.failPut = nil, down
		if err := c.Append(ctx, commitOf("tls", 2, "v")); !errors.Is(err, down) || errors.Is(err, store.ErrExists) {
			t.Fatalf("put: %v", err)
		}
		b.failPut = &smithy.GenericAPIError{Code: "ConditionalRequestConflict"}
		if err := c.Append(ctx, commitOf("tls", 2, "v")); !errors.Is(err, store.ErrExists) {
			t.Fatalf("a write that lost a race: %v", err)
		}
	})
	t.Run("no key yet", func(t *testing.T) {
		waiting := errors.New("no root secret yet")
		c := s3Commits(&lockedBucket{objectLock: true}, now)
		c.Key = func(context.Context) ([]byte, error) { return nil, waiting }
		if _, _, err := c.Head(ctx, "tls"); !errors.Is(err, waiting) {
			t.Fatalf("head: %v", err)
		}
		if err := c.Append(ctx, commitOf("tls", 1, "v")); !errors.Is(err, waiting) {
			t.Fatalf("append: %v", err)
		}
	})
	t.Run("a commit without a version, or a store without a retention period", func(t *testing.T) {
		c := s3Commits(&lockedBucket{objectLock: true}, now)
		if err := c.Append(ctx, commitOf("tls", 0, "v")); err == nil {
			t.Fatal("version 0 accepted")
		}
		c.Retention = 0
		if err := c.Append(ctx, commitOf("tls", 1, "v")); err == nil {
			t.Fatal("no retention accepted")
		}
	})
	t.Run("a commit that is not locked reports no date", func(t *testing.T) {
		b := &lockedBucket{objectLock: true}
		c := s3Commits(b, now)
		if err := c.Append(ctx, commitOf("tls", 1, "v")); err != nil {
			t.Fatal(err)
		}
		b.versions[0].mode = s3types.ObjectLockModeGovernance
		if head, _, err := c.Head(ctx, "tls"); err != nil || head == nil || !head.LockedUntil.IsZero() {
			t.Fatalf("head %+v, %v", head, err)
		}
	})
}

// The two together: records anchored in a bucket like S3's.
func TestAnchoredOverS3Commits(t *testing.T) {
	ctx := context.Background()
	b := &lockedBucket{objectLock: true}
	records := store.NewMemory()
	a := &store.Anchored{Records: records, Commits: s3Commits(b, time.Now())}
	for _, v := range []string{"one", "two"} {
		if err := a.PutRecord(ctx, rec("tls", v), false); err != nil {
			t.Fatal(err)
		}
	}
	// The operator hides the newest commit and puts the first value back.
	b.add(bucketVersion{key: b.versions[1].key, deleteMarker: true})
	stored := names(t, records, "tls")
	first, err := records.GetRecord(ctx, stored[0])
	if err != nil {
		t.Fatal(err)
	}
	first.Name = stored[1]
	if err := records.PutRecord(ctx, first, false); err != nil {
		t.Fatal(err)
	}
	fresh := &store.Anchored{Records: records, Commits: s3Commits(b, time.Now())}
	if _, err := fresh.GetRecord(ctx, "tls"); !errors.Is(err, store.ErrLost) {
		t.Fatalf("got %v", err)
	}
}
