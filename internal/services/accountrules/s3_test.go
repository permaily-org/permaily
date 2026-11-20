package accountrules

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

	"github.com/permaily-org/permaily/internal/rules"
	"github.com/permaily-org/permaily/internal/store"
)

// U2 and U29 on cold histories in a bucket like S3's (not a store of commits
// in memory): an enclave that restarts reads each epoch's whole history,
// which store.S3Commits refuses past store.MaxHistoryReads objects; the
// rollover keeps every epoch below it.

// bucket is an S3 bucket with Object Lock, as the store's tests have it:
// every write adds a version, a delete marker hides an object from plain
// requests only, a write with If-None-Match is refused where a version that
// is not a delete marker is the newest, and listings carry both markers.
type bucket struct {
	mu       sync.Mutex
	versions []bver
	next     int
	gets     int
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
	const size = 1000
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
	b.gets++
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
	return &s3.PutObjectOutput{}, nil
}

// hideNewest puts a delete marker over the newest object whose key holds
// part: what anyone who writes to the bucket can do.
func (b *bucket) hideNewest(part string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for i := len(b.versions) - 1; i >= 0; i-- {
		if strings.Contains(b.versions[i].key, part) && !b.versions[i].marker {
			b.next++
			b.versions = append(b.versions, bver{key: b.versions[i].key, id: fmt.Sprintf("v%06d", b.next), marker: true})
			return
		}
	}
}

func (b *bucket) getsSince(n int) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.gets - n
}

// s3Commits is a store of commits over b with its own memory, as a freshly
// started enclave has.
func (w *TestWorld) s3Commits(b *bucket) *store.S3Commits {
	return &store.S3Commits{
		S3: b, Bucket: "anchors", Retention: 24 * time.Hour,
		Key: func(context.Context) ([]byte, error) { return bytes.Repeat([]byte{7}, 32), nil },
		Now: w.Clock.now,
	}
}

// TestU2ARestartAtARolloverReadsTheNewestEpochOnS3 restarts an enclave at
// and just after a rollover, on commits in a bucket like S3's, and reads the
// newest epoch strictly, each epoch's whole history below
// store.MaxHistoryReads objects, the earlier epoch removed.
func TestU2ARestartAtARolloverReadsTheNewestEpochOnS3(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	w := NewTestWorld(t)
	b := &bucket{}
	w.Commits = w.s3Commits(b)
	s := w.Enclave()
	r := save(t, w, s, accountA, "", hold("a", "statement"))
	for range rules.RecordVersionsMax - 2 {
		toggle(t, w, s, accountA)
	}
	restart := func(wantEpoch uint64) {
		t.Helper()
		w.Commits = w.s3Commits(b)
		fresh := w.Enclave()
		before := b.getsSince(0)
		a, err := fresh.Applying(ctx, accountA, claude)
		if err != nil || a.Epoch != wantEpoch || ids(a)[len(a.Rules)-1] != r.ID {
			t.Fatalf("a restart: %+v, %v", a, err)
		}
		if n := b.getsSince(before); n >= 2*store.MaxHistoryReads {
			t.Fatalf("a cold read fetched %d objects", n)
		}
	}
	restart(0) // just before the write that rolls it over
	toggle(t, w, s, accountA)
	restart(1) // at the rollover
	toggle(t, w, s, accountA)
	restart(1) // just after
	if got := w.storedOf(accountA); len(got) != 1 || got["1"] != 2 {
		t.Fatalf("stored after a rollover: %v", got)
	}

	// A delete marker over the newest commit rolls nothing back: a restart
	// reads the newest still.
	b.hideNewest("")
	restart(1)
}
