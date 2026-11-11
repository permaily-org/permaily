package store

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
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
)

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
	binary.BigEndian.PutUint64(n[:], uint64(c.At.Unix()))
	m.Write(n[:])
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
	next := uint64(1)
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
			next = max(next, n+1)
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
func (s *S3Commits) Append(ctx context.Context, c Commit) error {
	if c.Version == 0 || s.Retention <= 0 {
		return errors.New("store: a commit needs a version and a retention period")
	}
	key, err := s.Key(ctx)
	if err != nil {
		return err
	}
	c.At = c.At.UTC().Truncate(time.Second)
	body, err := json.Marshal(commitObject{
		Name: c.Name, Version: c.Version, Digest: hex.EncodeToString(c.Digest[:]), At: c.At.Unix(), MAC: hex.EncodeToString(commitMAC(key, &c)),
	})
	if err != nil {
		return err
	}
	_, err = s.S3.PutObject(ctx, &s3.PutObjectInput{
		Bucket:                    aws.String(s.Bucket),
		Key:                       aws.String(commitPrefix(c.Name) + fmt.Sprintf("%020d", c.Version)),
		Body:                      bytes.NewReader(body),
		ContentType:               aws.String("application/json"),
		IfNoneMatch:               aws.String("*"),
		ObjectLockMode:            s3types.ObjectLockModeCompliance,
		ObjectLockRetainUntilDate: aws.Time(s.now().Add(s.Retention).UTC()),
	})
	if err != nil {
		var api smithy.APIError
		if errors.As(err, &api) && (api.ErrorCode() == "PreconditionFailed" || api.ErrorCode() == "ConditionalRequestConflict") {
			return ErrExists
		}
		return fmt.Errorf("store: write commit: %w", err)
	}
	return nil
}
