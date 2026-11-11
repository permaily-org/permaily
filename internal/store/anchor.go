package store

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"
)

// Everything the enclave stores is sealed, so whoever controls the storage
// cannot read or forge it. They can still put back an earlier copy: a sealed
// value from last month is as authentic as today's. Anchored closes that for
// the records that go through it.
//
// Each time a record changes, the enclave appends a commit (its name, a
// version and the hash of what was stored) to a store that only grows, where
// nothing can be removed or replaced by anyone in the account (Commits). A
// record is read by its newest commit: the value that was stored then, or
// nothing. Never an earlier one.

// ErrLost means the newest version of a record that was committed is not in
// the storage any more, or is not what was committed. It is an ErrNotFound: a
// record that may be made again (a TLS identity) is. One whose loss must not
// be forgiven checks for ErrLost first.
var ErrLost = fmt.Errorf("%w: the committed version of the record is gone", ErrNotFound)

// ErrRolledBack means the store of commits itself went back: a commit this
// enclave saw or made is not there any more. Nobody can remove one, so this is
// either a failure of that guarantee or another store put in its place.
var ErrRolledBack = errors.New("store: the commits of a record went back")

// Commit says that a record was stored at a version, and what was stored.
type Commit struct {
	Name    string
	Version uint64
	// Digest is the hash of the record as stored (Record.digest).
	Digest [sha256.Size]byte
	At     time.Time
	// LockedUntil is until when the store keeps the commit whatever anyone
	// asks; zero when it does not say.
	LockedUntil time.Time
}

// Commits is a store that only grows.
type Commits interface {
	// Head returns the commit of name with the highest version among those
	// this enclave's deployment made, nil when there is none, and the lowest
	// version nothing was appended at yet.
	Head(ctx context.Context, name string) (latest *Commit, next uint64, err error)
	// Append adds c and returns it as stored, with the date it is kept
	// until. It fails with ErrExists when something is at that version
	// already.
	Append(ctx context.Context, c Commit) (*Commit, error)
}

// Regression is a commit that was there and is not any more.
type Regression struct {
	Name string
	// Had is the version this enclave knew, and Found the newest one the
	// store answered with afterwards (0 for none).
	Had, Found uint64
	At         time.Time
}

// Anchored is Records whose values cannot be put back to an earlier one. The
// zero value is not usable: set Records and Commits.
type Anchored struct {
	// Records holds the values, each version under a name of its own.
	Records Records
	Commits Commits
	Now     func() time.Time

	mu sync.Mutex
	// seen is the newest commit of each record this enclave read or made: what
	// the store of commits must still answer with, at least.
	seen        map[string]Commit
	regressions []Regression
}

// maxRegressions bounds what is remembered of a store that keeps going back.
const maxRegressions = 32

// digest is what a commit says of a record: its name, its wrapped key and
// its value, each with its length so that no two records give the same input.
func (r *Record) digest() [sha256.Size]byte {
	h := sha256.New()
	for _, part := range [][]byte{[]byte(r.Name), r.WrappedKey, r.Value} {
		var n [8]byte
		binary.BigEndian.PutUint64(n[:], uint64(len(part)))
		h.Write(n[:])
		h.Write(part)
	}
	var d [sha256.Size]byte
	copy(d[:], h.Sum(nil))
	return d
}

// versioned is where one version of a record lives. The hash is part of the
// name, so two enclaves that write the same version never overwrite each
// other: the commit says which of the two counts.
func versioned(c *Commit) string {
	return fmt.Sprintf("%s@%020d-%s", c.Name, c.Version, hex.EncodeToString(c.Digest[:8]))
}

func (a *Anchored) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

// head reads the newest commit of name and checks that the store of commits
// did not go back from what this enclave knows.
func (a *Anchored) head(ctx context.Context, name string) (*Commit, uint64, error) {
	head, next, err := a.Commits.Head(ctx, name)
	if err != nil {
		return nil, 0, fmt.Errorf("store: commits of %s: %w", name, err)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if had, ok := a.seen[name]; ok && (head == nil || head.Version < had.Version || (head.Version == had.Version && head.Digest != had.Digest)) {
		found := uint64(0)
		if head != nil {
			found = head.Version
		}
		if len(a.regressions) < maxRegressions {
			a.regressions = append(a.regressions, Regression{Name: name, Had: had.Version, Found: found, At: a.now().UTC()})
		}
		return nil, 0, fmt.Errorf("%w: %s was at version %d, and the store answers %d", ErrRolledBack, name, had.Version, found)
	}
	if head != nil {
		a.remember(*head)
	}
	return head, next, nil
}

func (a *Anchored) remember(c Commit) {
	if a.seen == nil {
		a.seen = map[string]Commit{}
	}
	a.seen[c.Name] = c
}

// committed returns the value a commit names, or ErrLost.
func (a *Anchored) committed(ctx context.Context, c *Commit) (*Record, error) {
	rec, err := a.Records.GetRecord(ctx, versioned(c))
	if errors.Is(err, ErrNotFound) {
		return nil, ErrLost
	}
	if err != nil {
		return nil, err
	}
	rec.Name = c.Name
	if rec.digest() != c.Digest {
		return nil, ErrLost
	}
	return rec, nil
}

// GetRecord implements Records: the value of the newest commit, ErrLost when
// that value is gone, ErrNotFound when the record was never stored.
func (a *Anchored) GetRecord(ctx context.Context, name string) (*Record, error) {
	head, next, err := a.head(ctx, name)
	if err != nil {
		return nil, err
	}
	if head != nil {
		return a.committed(ctx, head)
	}
	// No commit yet. A record written before commits existed is under its
	// plain name: take it as it is, once, and commit it. It stays there for
	// the release that wrote it, should that one have to run again.
	rec, err := a.Records.GetRecord(ctx, name)
	if err != nil {
		return nil, err
	}
	if err := a.commit(ctx, rec, next); err != nil {
		if errors.Is(err, ErrExists) {
			// Another enclave committed it meanwhile: read what it committed.
			return a.GetRecord(ctx, name)
		}
		return nil, err
	}
	return rec, nil
}

// commit stores r as a version of its own and appends the commit that makes
// it the newest. The value goes first: a commit never names a value that was
// not stored, and a value without a commit is nothing.
func (a *Anchored) commit(ctx context.Context, r *Record, version uint64) error {
	c := Commit{Name: r.Name, Version: version, Digest: r.digest(), At: a.now().UTC().Truncate(time.Second)}
	value := *r
	value.Name = versioned(&c)
	if err := a.Records.PutRecord(ctx, &value, false); err != nil {
		return err
	}
	stored, err := a.Commits.Append(ctx, c)
	if err != nil {
		return err
	}
	a.mu.Lock()
	a.remember(*stored)
	a.mu.Unlock()
	return nil
}

// PutRecord implements Records: r becomes the newest version. With create it
// fails with ErrExists when the record has a committed value that is still
// there; a record that was lost can be created again, as a later version.
func (a *Anchored) PutRecord(ctx context.Context, r *Record, create bool) error {
	for range 3 {
		head, next, err := a.head(ctx, r.Name)
		if err != nil {
			return err
		}
		if create {
			if head != nil {
				if _, err := a.committed(ctx, head); err == nil {
					return ErrExists
				} else if !errors.Is(err, ErrLost) {
					return err
				}
			} else if _, err := a.Records.GetRecord(ctx, r.Name); err == nil {
				return ErrExists
			} else if !errors.Is(err, ErrNotFound) {
				return err
			}
		}
		err = a.commit(ctx, r, next)
		if !errors.Is(err, ErrExists) {
			return err
		}
		if create {
			return ErrExists
		}
		// Another enclave took that version: write after it.
	}
	return fmt.Errorf("store: the commits of %s keep changing", r.Name)
}

// Regressions returns the commits that were there and then were not, as far as
// this enclave saw since it started.
func (a *Anchored) Regressions() []Regression {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]Regression(nil), a.regressions...)
}

// Seen returns the newest commit of every record this enclave read or wrote,
// in the order of their names.
func (a *Anchored) Seen() []Commit {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]Commit, 0, len(a.seen))
	for _, c := range a.seen {
		out = append(out, c)
	}
	slices.SortFunc(out, func(x, y Commit) int { return strings.Compare(x.Name, y.Name) })
	return out
}

// MemoryCommits is Commits in memory, for dev mode and tests.
type MemoryCommits struct {
	mu      sync.Mutex
	commits map[string][]Commit
	// taken are versions where something that is not a commit of this
	// deployment sits, as anyone who can write to the real store can leave.
	taken map[string]map[uint64]bool
}

// NewMemoryCommits returns an empty MemoryCommits.
func NewMemoryCommits() *MemoryCommits {
	return &MemoryCommits{commits: map[string][]Commit{}, taken: map[string]map[uint64]bool{}}
}

// Head implements Commits.
func (m *MemoryCommits) Head(_ context.Context, name string) (*Commit, uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var latest *Commit
	next := uint64(1)
	for _, c := range m.commits[name] {
		if latest == nil || c.Version > latest.Version {
			latest = &c
		}
		next = max(next, c.Version+1)
	}
	for v := range m.taken[name] {
		next = max(next, v+1)
	}
	return latest, next, nil
}

// Append implements Commits.
func (m *MemoryCommits) Append(_ context.Context, c Commit) (*Commit, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.taken[c.Name][c.Version] {
		return nil, ErrExists
	}
	for _, old := range m.commits[c.Name] {
		if old.Version == c.Version {
			return nil, ErrExists
		}
	}
	m.commits[c.Name] = append(m.commits[c.Name], c)
	return &c, nil
}

// Take leaves something that is not a commit at a version of name, the way
// anyone who can write to the real store can: nothing can be appended there.
func (m *MemoryCommits) Take(name string, version uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.taken[name] == nil {
		m.taken[name] = map[uint64]bool{}
	}
	m.taken[name][version] = true
}

// Forget removes the newest commit of name. The real store lets nobody do
// that; tests use it to see what an enclave does if it happens anyway.
func (m *MemoryCommits) Forget(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	list := m.commits[name]
	newest := -1
	for i, c := range list {
		if newest < 0 || c.Version > list[newest].Version {
			newest = i
		}
	}
	if newest >= 0 {
		m.commits[name] = append(list[:newest:newest], list[newest+1:]...)
	}
}
