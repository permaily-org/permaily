package store

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
)

// S3Commits is Commits in an S3 bucket with Object Lock: each commit is an
// object that S3 keeps, in compliance mode, until a date nobody in the
// account can bring forward, the root user included. So a commit cannot be
// deleted or replaced. Tried on 2026-11-11 with an administrator: deleting a
// locked version, shortening its retention, switching it to governance mode,
// suspending versioning and deleting the bucket are all refused.
//
// What an administrator, or the parent instance with the enclave's
// credentials, can still do is add: another version of an object, an object
// at a version not used yet, and a delete marker, which hides an object from
// a plain listing and a plain GET without removing anything. So Head lists
// every version of every object, reads each by its version id, and counts
// only those that carry this deployment's authentication code.
type S3Commits struct {
	S3     S3CommitsAPI
	Bucket string
	// Key returns the key that authenticates commits. It comes from the root
	// secret, so only enclaves of this deployment can make one.
	Key func(ctx context.Context) ([]byte, error)
	// Retention is how long S3 keeps each commit from when it is written.
	Retention time.Duration
	Now       func() time.Time

	// known is what History read of each object version (by record, key and
	// version id): a commit, or nil for what is not one. S3 never changes the
	// bytes of a version, and a lock can only be extended, so what was read
	// is read again from here; every decision is still made from the full
	// listing of each read, from what each read holds of every object it lists
	// for as long as it runs. The oldest are dropped past maxKnownObjects.
	mu         sync.Mutex
	known      map[string]*Commit
	knownOrder []string
}

// S3CommitsAPI is the subset of the S3 client used by S3Commits.
type S3CommitsAPI interface {
	ListObjectVersions(ctx context.Context, in *s3.ListObjectVersionsInput, opts ...func(*s3.Options)) (*s3.ListObjectVersionsOutput, error)
	GetObject(ctx context.Context, in *s3.GetObjectInput, opts ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	PutObject(ctx context.Context, in *s3.PutObjectInput, opts ...func(*s3.Options)) (*s3.PutObjectOutput, error)
}

// Limits on what Head reads. Whoever can write to the bucket can make Head
// slow or make it fail, which stops the record from being used and gains
// nothing else.
const (
	maxCommitObjects = 10000
	maxCommitReads   = 500
	maxCommitSize    = 4 << 10
	// historyFetches is how many objects History reads at once, and
	// maxKnownObjects how many it keeps in memory.
	historyFetches  = 16
	maxKnownObjects = 100000
	// maxHistoryPages bounds the pages History lists: S3 answers up to 1,000
	// entries a page, so maxCommitObjects fit in far fewer.
	maxHistoryPages = 100
)

// errHistoryTooLong is History's answer to a listing past its bounds: the
// strict record cannot be read until the listing is shorter, which only
// whoever writes to the bucket can make it. It is not ErrForked.
var errHistoryTooLong = errors.New("store: the history of a record is too long")

// commitPrefix is where the commits of a record are. The record's name is
// hashed: names may hold characters an object key should not.
func commitPrefix(name string) string {
	sum := sha256.Sum256([]byte(name))
	return "commits/" + hex.EncodeToString(sum[:16]) + "/"
}

type commitObject struct {
	Name    string `json:"name"`
	Version uint64 `json:"version"`
	Digest  string `json:"digest"`
	At      int64  `json:"at"`
	MAC     string `json:"mac"`
}

// commitMAC authenticates a commit: every field, each of a fixed length or
// with its length before it.
func commitMAC(key []byte, c *Commit) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte("permaily record commit v1\x00"))
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], uint64(len(c.Name)))
	m.Write(n[:])
	m.Write([]byte(c.Name))
	binary.BigEndian.PutUint64(n[:], c.Version)
	m.Write(n[:])
	m.Write(c.Digest[:])
	_ = binary.Write(m, binary.BigEndian, c.At.Unix())
	return m.Sum(nil)
}

func (s *S3Commits) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Head implements Commits.
func (s *S3Commits) Head(ctx context.Context, name string) (*Commit, uint64, error) {
	key, err := s.Key(ctx)
	if err != nil {
		return nil, 0, err
	}
	prefix := commitPrefix(name)
	type object struct {
		version uint64
		key, id string
	}
	var objects []object
	used := map[uint64]bool{}
	in := &s3.ListObjectVersionsInput{Bucket: aws.String(s.Bucket), Prefix: aws.String(prefix)}
	for {
		out, err := s.S3.ListObjectVersions(ctx, in)
		if err != nil {
			return nil, 0, fmt.Errorf("store: list commits: %w", err)
		}
		// Delete markers are not looked at: they remove nothing.
		for _, v := range out.Versions {
			k := aws.ToString(v.Key)
			n, err := strconv.ParseUint(strings.TrimPrefix(k, prefix), 10, 64)
			if err != nil || k != prefix+fmt.Sprintf("%020d", n) || n == 0 {
				continue
			}
			objects = append(objects, object{n, k, aws.ToString(v.VersionId)})
			used[n] = true
		}
		if len(objects) > maxCommitObjects {
			return nil, 0, fmt.Errorf("store: more than %d objects where the commits of a record are", maxCommitObjects)
		}
		if !aws.ToBool(out.IsTruncated) {
			break
		}
		in.KeyMarker, in.VersionIdMarker = out.NextKeyMarker, out.NextVersionIdMarker
	}

	// From the highest version down, the first one with a commit of this
	// deployment is the head. Two enclaves writing at once can leave two at
	// one version: the same one of them counts for everybody.
	slices.SortFunc(objects, func(a, b object) int {
		if a.version != b.version {
			if a.version > b.version {
				return -1
			}
			return 1
		}
		return strings.Compare(a.id, b.id)
	})
	var head *Commit
	for i, o := range objects {
		if head != nil && o.version < head.Version {
			break
		}
		if i >= maxCommitReads {
			return nil, 0, fmt.Errorf("store: more than %d objects that are not commits above the newest commit", maxCommitReads)
		}
		c, err := s.read(ctx, key, name, o.version, o.key, o.id)
		if err != nil {
			return nil, 0, err
		}
		if c != nil && (head == nil || bytes.Compare(c.Digest[:], head.Digest[:]) < 0) {
			head = c
		}
	}
	// The next commit goes at the lowest version above the head that nothing
	// is at, not after everything that is there: an object left above a free
	// version must not make one enclave write past the version another is
	// about to write (Commits.Head).
	next := uint64(1)
	if head != nil {
		next = head.Version + 1
	}
	for used[next] {
		next++
	}
	return head, next, nil
}

// read returns the commit in one version of an object, or nil when it is not
// a commit of this deployment for that record and version.
func (s *S3Commits) read(ctx context.Context, key []byte, name string, version uint64, objectKey, id string) (*Commit, error) {
	out, err := s.S3.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(s.Bucket), Key: aws.String(objectKey), VersionId: aws.String(id)})
	if err != nil {
		return nil, fmt.Errorf("store: read commit: %w", err)
	}
	defer out.Body.Close()
	body, err := io.ReadAll(io.LimitReader(out.Body, maxCommitSize+1))
	if err != nil {
		return nil, fmt.Errorf("store: read commit: %w", err)
	}
	var doc commitObject
	if len(body) > maxCommitSize || json.Unmarshal(body, &doc) != nil {
		return nil, nil
	}
	digest, err1 := hex.DecodeString(doc.Digest)
	mac, err2 := hex.DecodeString(doc.MAC)
	if err1 != nil || err2 != nil || len(digest) != sha256.Size || doc.Name != name || doc.Version != version {
		return nil, nil
	}
	c := &Commit{Name: doc.Name, Version: doc.Version, At: time.Unix(doc.At, 0).UTC()}
	copy(c.Digest[:], digest)
	if !hmac.Equal(mac, commitMAC(key, c)) {
		return nil, nil
	}
	if out.ObjectLockMode == s3types.ObjectLockModeCompliance && out.ObjectLockRetainUntilDate != nil {
		c.LockedUntil = out.ObjectLockRetainUntilDate.UTC()
	}
	return c, nil
}

// Append implements Commits. The object is written only if nothing is at its
// key, and locked in compliance mode in the same request: a bucket without
// Object Lock refuses it, so a commit is never left where it could be removed.
func (s *S3Commits) Append(ctx context.Context, c Commit) (*Commit, error) {
	if c.Version == 0 || s.Retention <= 0 {
		return nil, errors.New("store: a commit needs a version and a retention period")
	}
	key, err := s.Key(ctx)
	if err != nil {
		return nil, err
	}
	c.At = c.At.UTC().Truncate(time.Second)
	c.LockedUntil = s.now().Add(s.Retention).UTC().Truncate(time.Second)
	body, err := json.Marshal(commitObject{
		Name: c.Name, Version: c.Version, Digest: hex.EncodeToString(c.Digest[:]), At: c.At.Unix(), MAC: hex.EncodeToString(commitMAC(key, &c)),
	})
	if err != nil {
		return nil, err
	}
	checksum := sha256.Sum256(body)
	_, err = s.S3.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(s.Bucket),
		Key:         aws.String(commitPrefix(c.Name) + fmt.Sprintf("%020d", c.Version)),
		Body:        bytes.NewReader(body),
		ContentType: aws.String("application/json"),
		IfNoneMatch: aws.String("*"),
		// S3 takes no write with a lock unless it carries a checksum of the body.
		ChecksumSHA256:            aws.String(base64.StdEncoding.EncodeToString(checksum[:])),
		ObjectLockMode:            s3types.ObjectLockModeCompliance,
		ObjectLockRetainUntilDate: aws.Time(c.LockedUntil),
	})
	if err != nil {
		var api smithy.APIError
		if errors.As(err, &api) && (api.ErrorCode() == "PreconditionFailed" || api.ErrorCode() == "ConditionalRequestConflict") {
			return nil, ErrExists
		}
		return nil, fmt.Errorf("store: write commit: %w", err)
	}
	return &c, nil
}

// historyObject is one object version listed where the commits of a record
// are.
type historyObject struct {
	version uint64
	key, id string
}

func knownKey(name string, o historyObject) string { return name + "\x00" + o.key + "\x00" + o.id }

// History implements HistoryReader. Every object version under the record's
// prefix is listed, as Head lists them, within bounds that count everything
// listed (delete markers and keys that are not versions included, at most
// maxCommitObjects) and every page (at most maxHistoryPages, each moving on);
// past them the read fails (errHistoryTooLong). Each object this enclave has
// not read before is read (at most MaxHistoryReads in one call,
// historyFetches at once) and verified as Head verifies it; and the commits
// are grouped by version and digest. A commit is locked when one of its
// copies is in compliance mode until no earlier than its time plus
// MinCommitLock.
func (s *S3Commits) History(ctx context.Context, name string) (*History, error) {
	key, err := s.Key(ctx)
	if err != nil {
		return nil, err
	}
	prefix := commitPrefix(name)
	var objects []historyObject
	used := map[uint64]bool{}
	in := &s3.ListObjectVersionsInput{Bucket: aws.String(s.Bucket), Prefix: aws.String(prefix)}
	// Everything listed counts against the bound, before anything is left
	// out: keys that are not versions and delete markers cost a listing as
	// commits do. So do pages, and a listing must move on.
	entries := 0
	for pages := 1; ; pages++ {
		out, err := s.S3.ListObjectVersions(ctx, in)
		if err != nil {
			return nil, fmt.Errorf("store: list commits: %w", err)
		}
		entries += len(out.Versions) + len(out.DeleteMarkers)
		if entries > maxCommitObjects {
			return nil, fmt.Errorf("%w: more than %d objects and delete markers where its commits are", errHistoryTooLong, maxCommitObjects)
		}
		for _, v := range out.Versions {
			k := aws.ToString(v.Key)
			n, err := strconv.ParseUint(strings.TrimPrefix(k, prefix), 10, 64)
			if err != nil || k != prefix+fmt.Sprintf("%020d", n) || n == 0 {
				continue
			}
			objects = append(objects, historyObject{n, k, aws.ToString(v.VersionId)})
			used[n] = true
		}
		if !aws.ToBool(out.IsTruncated) {
			break
		}
		if pages >= maxHistoryPages {
			return nil, fmt.Errorf("%w: more than %d pages where its commits are", errHistoryTooLong, maxHistoryPages)
		}
		if out.NextKeyMarker == nil || (aws.ToString(out.NextKeyMarker) == aws.ToString(in.KeyMarker) && aws.ToString(out.NextVersionIdMarker) == aws.ToString(in.VersionIdMarker)) {
			return nil, errors.New("store: the listing of a record's commits does not move on")
		}
		in.KeyMarker, in.VersionIdMarker = out.NextKeyMarker, out.NextVersionIdMarker
	}

	// What each listed object is, held by this call from the moment it is
	// known (from memory, or fetched) to the end: the memory may drop it
	// meanwhile, for this record's objects or another's, and a decision never
	// rests on something that went away while it was made.
	results := make(map[string]*Commit, len(objects))
	var unread []historyObject
	s.mu.Lock()
	for _, o := range objects {
		if c, ok := s.known[knownKey(name, o)]; ok {
			results[knownKey(name, o)] = c
		} else {
			unread = append(unread, o)
		}
	}
	s.mu.Unlock()
	if len(unread) > MaxHistoryReads {
		if _, err := s.fetch(ctx, key, name, unread[:MaxHistoryReads]); err != nil {
			return nil, err
		}
		return nil, fmt.Errorf("store: more than %d objects of a record not read yet; the next read goes on", MaxHistoryReads)
	}
	fetched, err := s.fetch(ctx, key, name, unread)
	if err != nil {
		return nil, err
	}
	for i, o := range unread {
		results[knownKey(name, o)] = fetched[i]
	}

	copies := make([]commitCopy, 0, len(objects))
	for _, o := range objects {
		c, ok := results[knownKey(name, o)]
		if !ok {
			return nil, errors.New("store: an object of a record's commits was not read")
		}
		if c != nil {
			locked := !c.LockedUntil.IsZero() && !c.LockedUntil.Before(c.At.Add(MinCommitLock))
			copies = append(copies, commitCopy{*c, locked})
		}
	}
	h := history(copies)
	next := uint64(1)
	if h.Head != nil {
		next = h.Head.Version + 1
	}
	for used[next] {
		next++
	}
	h.Next = next
	return h, nil
}

// fetch reads objects, historyFetches at once, keeps what each is, and
// returns it in their order (nil for what is not a commit).
func (s *S3Commits) fetch(ctx context.Context, key []byte, name string, objects []historyObject) ([]*Commit, error) {
	var wg sync.WaitGroup
	slots := make(chan struct{}, historyFetches)
	errs := make([]error, len(objects))
	read := make([]*Commit, len(objects))
	for i, o := range objects {
		slots <- struct{}{}
		wg.Add(1)
		go func() {
			defer func() { <-slots; wg.Done() }()
			c, err := s.read(ctx, key, name, o.version, o.key, o.id)
			if err != nil {
				errs[i] = err
				return
			}
			read[i] = c
			s.keep(knownKey(name, o), c)
		}()
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	return read, nil
}

func (s *S3Commits) keep(k string, c *Commit) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.known == nil {
		s.known = map[string]*Commit{}
	}
	if _, ok := s.known[k]; ok {
		return
	}
	s.known[k] = c
	s.knownOrder = append(s.knownOrder, k)
	if len(s.knownOrder) > maxKnownObjects {
		delete(s.known, s.knownOrder[0])
		s.knownOrder = s.knownOrder[1:]
	}
}
