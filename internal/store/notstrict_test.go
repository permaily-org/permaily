package store_test

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/permaily-org/permaily/internal/store"
)

// recordingBucket is lockedBucket that writes down every request it gets,
// with the version of the commit each names, and can run something just
// before a write lands (before), as another enclave or whoever writes to the
// bucket would in that moment.
type recordingBucket struct {
	*lockedBucket
	mu     sync.Mutex
	log    []string
	before func(key string)
}

func (r *recordingBucket) write(line string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.log = append(r.log, line)
}

// short names an object by the version of the commit its key holds.
func short(key string) string {
	if i := strings.LastIndex(key, "/"); i >= 0 {
		return "v" + strings.TrimLeft(key[i+1:], "0")
	}
	return key
}

func (r *recordingBucket) ListObjectVersions(ctx context.Context, in *s3.ListObjectVersionsInput, opts ...func(*s3.Options)) (*s3.ListObjectVersionsOutput, error) {
	r.write("list")
	return r.lockedBucket.ListObjectVersions(ctx, in, opts...)
}

func (r *recordingBucket) GetObject(ctx context.Context, in *s3.GetObjectInput, opts ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	r.write("get " + short(aws.ToString(in.Key)) + " " + aws.ToString(in.VersionId))
	return r.lockedBucket.GetObject(ctx, in, opts...)
}

func (r *recordingBucket) PutObject(ctx context.Context, in *s3.PutObjectInput, opts ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	if hook := r.before; hook != nil {
		r.before = nil // once, and not for what the hook itself writes
		body, err := io.ReadAll(in.Body)
		if err != nil {
			return nil, err
		}
		in.Body = strings.NewReader(string(body))
		hook(aws.ToString(in.Key))
	}
	r.write("put " + short(aws.ToString(in.Key)))
	return r.lockedBucket.PutObject(ctx, in, opts...)
}

// hide puts a delete marker over the newest object at key, as whoever writes
// to the bucket can.
func (r *recordingBucket) hide(key string) {
	r.add(bucketVersion{key: key, deleteMarker: true})
}

// countingCommits is a store of commits that counts the strict reads of a
// history it is asked for.
type countingCommits struct {
	store.Commits
	histories int
}

func (c *countingCommits) History(ctx context.Context, name string) (*store.History, error) {
	c.histories++
	return c.Commits.(store.HistoryReader).History(ctx, name)
}

// S2 (ADR 0026): S1 on records that are not strict. A fork made through a
// delete marker under an Anchored with OnlyNewest (accounts') and under one
// with neither option (the TLS identity's) is read by the commit with the
// smaller digest, written above and removed exactly as before strict records
// existed: the same requests to S3 in the same order (recorded on develop at
// 1b210a8, before pull request 0, and written below), no history read, the
// same answers and the same Regressions.
func TestS2ARecordThatIsNotStrictIsReadAsBefore(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 11, 12, 9, 0, 0, 0, time.UTC)
	for _, kind := range []struct {
		name       string
		onlyNewest bool
		want       []string
	}{
		{"account", true, wantAccountTranscript},
		{"tls", false, wantTLSTranscript},
	} {
		t.Run(kind.name, func(t *testing.T) {
			b := &recordingBucket{lockedBucket: &lockedBucket{objectLock: true}}
			commits := &countingCommits{Commits: &store.S3Commits{
				S3: b, Bucket: "anchors", Retention: 24 * time.Hour,
				Key: func(context.Context) ([]byte, error) { return commitKey, nil },
				Now: func() time.Time { return now },
			}}
			records := store.NewMemory()
			enclave := func() *store.Anchored {
				return &store.Anchored{Records: records, Commits: commits, OnlyNewest: kind.onlyNewest, Now: func() time.Time { return now }}
			}
			a, bb := enclave(), enclave()
			var got []string
			step := func(what string, err error) {
				got = append(got, fmt.Sprintf("%s: %v", what, err))
				got = append(got, b.log...)
				got = append(got, fmt.Sprintf("stored %q", names(t, records, kind.name)))
				b.log = nil
			}
			read := func(who string, e *store.Anchored) {
				r, err := e.GetRecord(ctx, kind.name)
				if err == nil {
					got = append(got, fmt.Sprintf("%s reads %q", who, r.Value))
				}
				step(who+" reads", err)
			}
			step("made", a.PutRecord(ctx, rec(kind.name, "made"), true))
			_, first, err := a.GetRecordAt(ctx, kind.name)
			step("A reads at", err)
			_, second, err := bb.GetRecordAt(ctx, kind.name)
			step("B reads at", err)
			step("A writes", a.PutRecordAt(ctx, rec(kind.name, "A's change"), first))
			b.hide(b.versions[len(b.versions)-1].key)
			step("B writes over a delete marker", bb.PutRecordAt(ctx, rec(kind.name, "B's change"), second))
			c := enclave()
			read("A", a)
			read("B", bb)
			read("C", c)
			_, third, err := c.GetRecordAt(ctx, kind.name)
			step("C reads at", err)
			step("C writes above", c.PutRecordAt(ctx, rec(kind.name, "above"), third))
			read("A", a)
			read("B", bb)
			read("C", c)
			step("C removes", c.RemoveRecord(ctx, kind.name))
			read("A", a)
			read("C", c)
			for who, e := range []*store.Anchored{a, bb, c} {
				for _, r := range e.Regressions() {
					got = append(got, fmt.Sprintf("enclave %d regression %s had %d found %d", who, r.Name, r.Had, r.Found))
					if r.Fork || r.Unlocked {
						t.Fatalf("a record that is not strict reported as forked or unlocked: %+v", r)
					}
				}
			}
			if commits.histories != 0 {
				t.Fatalf("%d histories read for a record that is not strict", commits.histories)
			}
			if strings.Join(got, "\n") != strings.Join(kind.want, "\n") {
				t.Fatalf("what the store did changed:\n%s\n\nwant:\n%s", quoted(got), quoted(kind.want))
			}
		})
	}
}

func quoted(lines []string) string {
	var out strings.Builder
	for _, l := range lines {
		fmt.Fprintf(&out, "\t%q,\n", l)
	}
	return out.String()
}

var wantAccountTranscript = []string{
	"made: <nil>",
	"list",
	"put v1",
	"stored [\"account@00000000000000000001-a90793365c2b54e6\"]",
	"A reads at: <nil>",
	"list",
	"get v1 v0001",
	"stored [\"account@00000000000000000001-a90793365c2b54e6\"]",
	"B reads at: <nil>",
	"list",
	"get v1 v0001",
	"stored [\"account@00000000000000000001-a90793365c2b54e6\"]",
	"A writes: <nil>",
	"put v2",
	"stored [\"account@00000000000000000002-da7b20616a9efc80\"]",
	"B writes over a delete marker: <nil>",
	"put v2",
	"stored [\"account@00000000000000000002-981683e55ee47554\" \"account@00000000000000000002-da7b20616a9efc80\"]",
	"A reads: store: the commits of a record went back: account was at version 2, and the store answers 2",
	"list",
	"get v2 v0002",
	"get v2 v0004",
	"stored [\"account@00000000000000000002-981683e55ee47554\" \"account@00000000000000000002-da7b20616a9efc80\"]",
	"B reads \"B's change\"",
	"B reads: <nil>",
	"list",
	"get v2 v0002",
	"get v2 v0004",
	"stored [\"account@00000000000000000002-981683e55ee47554\" \"account@00000000000000000002-da7b20616a9efc80\"]",
	"C reads \"B's change\"",
	"C reads: <nil>",
	"list",
	"get v2 v0002",
	"get v2 v0004",
	"stored [\"account@00000000000000000002-981683e55ee47554\" \"account@00000000000000000002-da7b20616a9efc80\"]",
	"C reads at: <nil>",
	"list",
	"get v2 v0002",
	"get v2 v0004",
	"stored [\"account@00000000000000000002-981683e55ee47554\" \"account@00000000000000000002-da7b20616a9efc80\"]",
	"C writes above: <nil>",
	"put v3",
	"stored [\"account@00000000000000000002-da7b20616a9efc80\" \"account@00000000000000000003-b1ac4589eb3d51d1\"]",
	"A reads \"above\"",
	"A reads: <nil>",
	"list",
	"get v3 v0005",
	"stored [\"account@00000000000000000002-da7b20616a9efc80\" \"account@00000000000000000003-b1ac4589eb3d51d1\"]",
	"B reads \"above\"",
	"B reads: <nil>",
	"list",
	"get v3 v0005",
	"stored [\"account@00000000000000000002-da7b20616a9efc80\" \"account@00000000000000000003-b1ac4589eb3d51d1\"]",
	"C reads \"above\"",
	"C reads: <nil>",
	"list",
	"get v3 v0005",
	"stored [\"account@00000000000000000002-da7b20616a9efc80\" \"account@00000000000000000003-b1ac4589eb3d51d1\"]",
	"C removes: <nil>",
	"list",
	"get v3 v0005",
	"put v4",
	"stored []",
	"A reads: store: not found",
	"list",
	"get v4 v0006",
	"stored []",
	"C reads: store: not found",
	"list",
	"get v4 v0006",
	"stored []",
	"enclave 0 regression account had 2 found 2",
}

var wantTLSTranscript = []string{
	"made: <nil>",
	"list",
	"put v1",
	"stored [\"tls@00000000000000000001-39e277f23c868ba5\"]",
	"A reads at: <nil>",
	"list",
	"get v1 v0001",
	"stored [\"tls@00000000000000000001-39e277f23c868ba5\"]",
	"B reads at: <nil>",
	"list",
	"get v1 v0001",
	"stored [\"tls@00000000000000000001-39e277f23c868ba5\"]",
	"A writes: <nil>",
	"put v2",
	"stored [\"tls@00000000000000000001-39e277f23c868ba5\" \"tls@00000000000000000002-e7f5943e2c16ad34\"]",
	"B writes over a delete marker: <nil>",
	"put v2",
	"stored [\"tls@00000000000000000001-39e277f23c868ba5\" \"tls@00000000000000000002-3e0f3a0e76d81b92\" \"tls@00000000000000000002-e7f5943e2c16ad34\"]",
	"A reads: store: the commits of a record went back: tls was at version 2, and the store answers 2",
	"list",
	"get v2 v0002",
	"get v2 v0004",
	"stored [\"tls@00000000000000000001-39e277f23c868ba5\" \"tls@00000000000000000002-3e0f3a0e76d81b92\" \"tls@00000000000000000002-e7f5943e2c16ad34\"]",
	"B reads \"B's change\"",
	"B reads: <nil>",
	"list",
	"get v2 v0002",
	"get v2 v0004",
	"stored [\"tls@00000000000000000001-39e277f23c868ba5\" \"tls@00000000000000000002-3e0f3a0e76d81b92\" \"tls@00000000000000000002-e7f5943e2c16ad34\"]",
	"C reads \"B's change\"",
	"C reads: <nil>",
	"list",
	"get v2 v0002",
	"get v2 v0004",
	"stored [\"tls@00000000000000000001-39e277f23c868ba5\" \"tls@00000000000000000002-3e0f3a0e76d81b92\" \"tls@00000000000000000002-e7f5943e2c16ad34\"]",
	"C reads at: <nil>",
	"list",
	"get v2 v0002",
	"get v2 v0004",
	"stored [\"tls@00000000000000000001-39e277f23c868ba5\" \"tls@00000000000000000002-3e0f3a0e76d81b92\" \"tls@00000000000000000002-e7f5943e2c16ad34\"]",
	"C writes above: <nil>",
	"put v3",
	"stored [\"tls@00000000000000000001-39e277f23c868ba5\" \"tls@00000000000000000002-3e0f3a0e76d81b92\" \"tls@00000000000000000002-e7f5943e2c16ad34\" \"tls@00000000000000000003-c2cf54c1a1470712\"]",
	"A reads \"above\"",
	"A reads: <nil>",
	"list",
	"get v3 v0005",
	"stored [\"tls@00000000000000000001-39e277f23c868ba5\" \"tls@00000000000000000002-3e0f3a0e76d81b92\" \"tls@00000000000000000002-e7f5943e2c16ad34\" \"tls@00000000000000000003-c2cf54c1a1470712\"]",
	"B reads \"above\"",
	"B reads: <nil>",
	"list",
	"get v3 v0005",
	"stored [\"tls@00000000000000000001-39e277f23c868ba5\" \"tls@00000000000000000002-3e0f3a0e76d81b92\" \"tls@00000000000000000002-e7f5943e2c16ad34\" \"tls@00000000000000000003-c2cf54c1a1470712\"]",
	"C reads \"above\"",
	"C reads: <nil>",
	"list",
	"get v3 v0005",
	"stored [\"tls@00000000000000000001-39e277f23c868ba5\" \"tls@00000000000000000002-3e0f3a0e76d81b92\" \"tls@00000000000000000002-e7f5943e2c16ad34\" \"tls@00000000000000000003-c2cf54c1a1470712\"]",
	"C removes: <nil>",
	"list",
	"get v3 v0005",
	"put v4",
	"stored []",
	"A reads: store: not found",
	"list",
	"get v4 v0006",
	"stored []",
	"C reads: store: not found",
	"list",
	"get v4 v0006",
	"stored []",
	"enclave 0 regression tls had 2 found 2",
}
