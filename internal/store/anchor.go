package store

import (
	"bytes"
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
//
// A record is removed for good the same way: a commit says it is gone
// (Commit.Gone), and from then on it is not there, whatever is put back in
// the storage, until it is stored again as a later version.

// ErrLost means the newest version of a record that was committed is not in
// the storage any more, or is not what was committed. It is an ErrNotFound: a
// record that may be made again (a TLS identity) is. One whose loss must not
// be forgiven checks for ErrLost first.
var ErrLost error = &kind{msg: "store: not found: the committed version of the record is gone", wraps: ErrNotFound}

// ErrChanged is returned by PutRecordAt when something was committed for the
// record since it was read: another enclave stored it or removed it. Nothing
// was made the newest version; the caller reads again and decides again.
var ErrChanged error = &kind{msg: "store: the record changed since it was read"}

// ErrRolledBack means the store of commits itself went back: a commit this
// enclave saw or made is not there any more. Nobody can remove one, so this is
// either a failure of that guarantee or another store put in its place.
var ErrRolledBack error = &kind{msg: "store: the commits of a record went back"}

// Commit says that a record was stored at a version, and what was stored.
type Commit struct {
	Name    string
	Version uint64
	// Digest is the hash of the record as stored (Record.digest), or all
	// zeros when the commit says the record is gone.
	Digest [sha256.Size]byte
	At     time.Time
	// LockedUntil is until when the store keeps the commit whatever anyone
	// asks; zero when it does not say.
	LockedUntil time.Time
}

// Gone reports whether the commit says the record was removed. It says so
// with a digest of zeros, which no stored value has: the commit is written
// and authenticated like any other, so nobody can turn one kind into the
// other, and an image from before records could be removed takes it for the
// commit of a value that is not there (ErrLost), never for an earlier one.
func (c *Commit) Gone() bool { return c.Digest == [sha256.Size]byte{} }

// Commits is a store that only grows.
type Commits interface {
	// Head returns the commit of name with the highest version among those
	// this enclave's deployment made, nil when there is none, and where the
	// next commit goes: the lowest version above it that nothing is at. Never
	// a higher one: a commit made above a free version would hide the one an
	// enclave that read earlier then makes there, and neither would know
	// (PutRecordAt).
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
	// Fork marks a strict record with two commits of different digests at
	// one version (ErrForked): Found is the lowest such version, and Had the
	// version this enclave knew (0 for none). It is recorded once per record.
	Fork bool
	// Unlocked marks a strict record with a commit that has no copy locked as
	// Append locks one (History.Unlocked): Found is the lowest such version.
	// The commit is counted all the same; it is recorded once per record.
	Unlocked bool
	// Lost marks a strict record whose newest commit names a value that is
	// not stored (ErrLost): Found is that commit's version. Whoever writes to
	// the table deleted or changed it; the record is refused, never taken
	// for none. It is recorded once per record.
	Lost bool
}

// Anchored is Records whose values cannot be put back to an earlier one. The
// zero value is not usable: set Records and Commits.
type Anchored struct {
	// Records holds the values, each version under a name of its own.
	Records RecordTable
	Commits Commits
	// OnlyNewest deletes the stored value of a version once the one after it
	// is committed, for records whose earlier values must not stay in the
	// storage (an account: a name or a passkey that was removed). Without it
	// every version stays, as the TLS identity's do (ADR 0016).
	OnlyNewest bool
	// Strict reads every version of a record's commits, not only the newest,
	// and refuses the record (ErrForked) wherever two commits of different
	// digests share a version (ADR 0026, "The store"). Commits must then be a
	// HistoryReader. A strict record is made only with CreateNew, never taken
	// from its plain name, and its errors do not hold its name.
	//
	// It holds for this Anchored only, which knows nothing of the names that
	// other instances read: the layer that composes the store guarantees that
	// a strict name is never read or written through an Anchored without
	// Strict, and that strict records are made only with CreateNew.
	Strict bool
	Now    func() time.Time

	mu sync.Mutex
	// seen is the newest commit of each record this enclave read or made: what
	// the store of commits must still answer with, at least.
	seen        map[string]Commit
	regressions []Regression
	// alarmed are the strict records already reported as forked or unlocked.
	alarmed map[string]bool
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
// did not go back from what this enclave knew when it asked. An answer older
// than what the enclave learnt while it was on its way (another read or write
// of the same record, at once) is not the store going back: it is asked again,
// and the enclave never goes on from less than it knows.
func (a *Anchored) head(ctx context.Context, name string) (*Commit, uint64, error) {
	return a.headAs(ctx, name, a.Strict)
}

// headAs is head, reading the whole history when strict.
func (a *Anchored) headAs(ctx context.Context, name string, strict bool) (*Commit, uint64, error) {
	for range maxChanges {
		had, known := a.known(name)
		var (
			head *Commit
			next uint64
			err  error
		)
		if strict {
			head, next, err = a.strictHead(ctx, name, had, known)
		} else {
			head, next, err = a.plainHead(ctx, name, had, known)
		}
		if !errors.Is(err, errOvertaken) {
			return head, next, err
		}
	}
	return nil, 0, a.changingAs(name, strict)
}

// errOvertaken is head's answer that arrived after a newer commit of the
// record was seen: not wrong, only late.
var errOvertaken = errors.New("store: an answer older than what was seen meanwhile")

// known returns the newest commit of name this enclave saw, if any.
func (a *Anchored) known(name string) (Commit, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	had, ok := a.seen[name]
	return had, ok
}

// behind reports whether head is less than had: none, an earlier version, or
// another commit at the same one.
func behind(head *Commit, had Commit) bool {
	return head == nil || head.Version < had.Version || (head.Version == had.Version && head.Digest != had.Digest)
}

// settle decides an answer head of the store of commits, given had (known),
// what this enclave knew of name when it asked: ErrRolledBack if head is
// behind that, errOvertaken if it is behind only what the enclave saw since,
// and otherwise head is remembered. Its error says the record was what (its
// name, or not, for a strict record). The caller holds a.mu.
func (a *Anchored) settle(name, what string, head *Commit, had Commit, known bool) error {
	if known && behind(head, had) {
		found := uint64(0)
		if head != nil {
			found = head.Version
		}
		if len(a.regressions) < maxRegressions {
			a.regressions = append(a.regressions, Regression{Name: name, Had: had.Version, Found: found, At: a.now().UTC()})
		}
		return fmt.Errorf("%w: %s was at version %d, and the store answers %d", ErrRolledBack, what, had.Version, found)
	}
	if now, ok := a.seen[name]; ok && behind(head, now) {
		return errOvertaken
	}
	if head != nil {
		a.remember(*head)
	}
	return nil
}

// plainHead is head for a record that is not strict: its newest commit only.
func (a *Anchored) plainHead(ctx context.Context, name string, had Commit, known bool) (*Commit, uint64, error) {
	head, next, err := a.Commits.Head(ctx, name)
	if err != nil {
		return nil, 0, fmt.Errorf("store: commits of %s: %w", name, err)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.settle(name, name, head, had, known); err != nil {
		return nil, 0, err
	}
	return head, next, nil
}

// strictHead is head for a strict record: the whole history is read, a fork
// at any version refuses the record before anything else, a commit without
// its lock is reported, and then the store of commits must not have gone back
// from what this enclave knew, as for every record. Its errors do not hold
// the record's name, which holds identifiers, and errors reach logs.
func (a *Anchored) strictHead(ctx context.Context, name string, had Commit, known bool) (*Commit, uint64, error) {
	reader, ok := a.Commits.(HistoryReader)
	if !ok {
		return nil, 0, errors.New("store: a strict record needs a store of commits that reads its history")
	}
	h, err := reader.History(ctx, name)
	if err != nil {
		return nil, 0, fmt.Errorf("store: the commits of a strict record: %w", err)
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if len(h.Forks) > 0 {
		a.alarm(Regression{Name: name, Had: a.seen[name].Version, Found: h.Forks[0].Version, Fork: true})
		return nil, 0, fmt.Errorf("%w: at version %d", ErrForked, h.Forks[0].Version)
	}
	if len(h.Unlocked) > 0 {
		a.alarm(Regression{Name: name, Had: a.seen[name].Version, Found: h.Unlocked[0], Unlocked: true})
	}
	if err := a.settle(name, "a strict record", h.Head, had, known); err != nil {
		return nil, 0, err
	}
	return h.Head, h.Next, nil
}

// alarm records a fork, an unlocked commit or a lost value of a strict
// record, once per record and kind. The caller holds a.mu.
func (a *Anchored) alarm(r Regression) {
	kind := "unlocked\x00"
	switch {
	case r.Fork:
		kind = "fork\x00"
	case r.Lost:
		kind = "lost\x00"
	}
	if a.alarmed[kind+r.Name] {
		return
	}
	if a.alarmed == nil {
		a.alarmed = map[string]bool{}
	}
	a.alarmed[kind+r.Name] = true
	r.At = a.now().UTC()
	if len(a.regressions) < maxRegressions {
		a.regressions = append(a.regressions, r)
	}
}

// remember keeps c as what the store of commits must still answer with, at
// least, unless a later commit of the record was seen already: a write that
// remembers its own commit after a read saw the next one does not take the
// enclave back. The caller holds a.mu.
func (a *Anchored) remember(c Commit) {
	if a.seen == nil {
		a.seen = map[string]Commit{}
	}
	if had, ok := a.seen[c.Name]; ok && had.Version > c.Version {
		return
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

// maxChanges is how many times a read or a write starts over because the
// commits of its record changed under it.
const maxChanges = 3

// changing is the error of a read or a write that started over maxChanges
// times. A strict record's does not hold its name, as strictHead's do not.
func (a *Anchored) changing(name string) error {
	return a.changingAs(name, a.Strict)
}

// changingAs is changing for a record read strictly or not.
func (a *Anchored) changingAs(name string, strict bool) error {
	if strict {
		return errors.New("store: the commits of a strict record keep changing")
	}
	return fmt.Errorf("store: the commits of %s keep changing", name)
}

// Mark is where a record was when it was read: what PutRecordAt needs to
// store it only if nothing was committed since. The zero value marks nothing.
type Mark struct {
	name string
	// next is the version the next commit of the record takes.
	next uint64
	// head is the commit the record was read by, if it had one.
	head *Commit
}

// Version is the version of the commit the record was read by: how many
// versions it has, 0 for a record read when it had none.
func (m Mark) Version() uint64 {
	if m.head == nil {
		return 0
	}
	return m.head.Version
}

// GetRecord implements Records: the value of the newest commit, ErrLost when
// that value is gone, ErrNotFound when the record was never stored or was
// removed.
func (a *Anchored) GetRecord(ctx context.Context, name string) (*Record, error) {
	rec, _, err := a.GetRecordAt(ctx, name)
	return rec, err
}

// GetRecordAt is GetRecord that also says where the record was read, for a
// change that must not undo another enclave's: read, change the value, and
// store it with PutRecordAt. A record that is not there (ErrNotFound, which
// ErrLost is) has a mark too: PutRecordAt makes it, if nobody did meanwhile.
func (a *Anchored) GetRecordAt(ctx context.Context, name string) (*Record, Mark, error) {
	var missing *Commit
	for range maxChanges {
		head, next, err := a.head(ctx, name)
		if err != nil {
			return nil, Mark{}, err
		}
		at := Mark{name: name, next: next, head: head}
		if head == nil && a.Strict {
			if err := a.uncommitted(ctx, name); !errors.Is(err, ErrNotFound) {
				return nil, Mark{}, err
			}
			return nil, at, ErrNotFound
		}
		if head == nil {
			adopted, err := a.adopt(ctx, name, next)
			if adopted {
				continue // it has a commit now: read by it
			}
			if !errors.Is(err, ErrNotFound) {
				return nil, Mark{}, err
			}
			return nil, at, err
		}
		if head.Gone() {
			return nil, at, ErrNotFound
		}
		if missing != nil && head.Version == missing.Version && head.Digest == missing.Digest {
			if a.Strict {
				a.mu.Lock()
				a.alarm(Regression{Name: name, Had: head.Version, Found: head.Version, Lost: true})
				a.mu.Unlock()
			}
			return nil, at, ErrLost
		}
		rec, err := a.committed(ctx, head)
		if err == nil {
			return rec, at, nil
		}
		if !errors.Is(err, ErrLost) {
			return nil, Mark{}, err
		}
		// A write or a removal that came between the two reads deletes the
		// value this commit names. The record is lost only if that commit is
		// still the newest.
		missing = head
	}
	return nil, Mark{}, a.changing(name)
}

// adopt commits a record that has no commit yet. One written before commits
// existed is under its plain name: it is taken as it is, once, and committed.
// It stays there for the release that wrote it, should that one have to run
// again. It reports whether the record has a commit now, its own or the one
// another enclave made meanwhile; otherwise the error says why not, and is
// ErrNotFound when there is nothing under the plain name either.
func (a *Anchored) adopt(ctx context.Context, name string, next uint64) (bool, error) {
	rec, err := a.Records.GetRecord(ctx, name)
	if err != nil {
		return false, err
	}
	if _, err := a.commit(ctx, rec, next); err != nil && !errors.Is(err, ErrExists) {
		return false, err
	}
	return true, nil
}

// uncommitted is what a strict record with no commit is: ErrNotFound when
// nothing is under its plain name either. A strict record is never taken
// from its plain name, as adopt takes the others: strict kinds came after
// commits, so a value there with no commit is one whose commits are gone,
// and the record is refused (ErrRolledBack, which is not ErrNotFound, so
// nobody makes it again over that value without noticing; CreateNew answers
// ErrExists).
func (a *Anchored) uncommitted(ctx context.Context, name string) error {
	_, err := a.Records.GetRecord(ctx, name)
	switch {
	case err == nil:
		return fmt.Errorf("%w: a value without a commit where a strict record is", ErrRolledBack)
	case errors.Is(err, ErrNotFound):
		return ErrNotFound
	}
	return err
}

// ErrTooLate is a write made with WriteWithin whose time ran out before it
// stored its value: nothing of it was stored. It is a
// context.DeadlineExceeded.
var ErrTooLate error = &kind{msg: "store: the write's time ran out before it stored its value", wraps: context.DeadlineExceeded}

type writeWithinKey struct{}

type stageByKey struct{}

// WriteWithin bounds each write made with the context it returns
// (PutRecordAt, PutRecord, CreateNew) to d from when it begins, before its
// first read of the record's commits: past d by the Anchored's clock it
// stores no value (ErrTooLate), and every request it makes ends at d. So
// whatever the reads of a long history, their pages, the credentials or the
// retries take, no value of such a write is stored later than d after the
// read of the commits it rests on began (third review of #152, finding 1).
func WriteWithin(ctx context.Context, d time.Duration) context.Context {
	return context.WithValue(ctx, writeWithinKey{}, d)
}

type writeByKey struct{}

// WriteBy bounds each write made with the context it returns (PutRecordAt,
// PutRecord, CreateNew) to an instant by the Anchored's clock, whenever the
// write begins: past by it stores no value (ErrTooLate), and every request
// it makes ends at by. With WriteWithin too, the earlier of the two holds.
// It carries an authority's own deadline into the store: a caller that
// decided to write at some instant, on what it read then, cannot have the
// write begin later and still be given WriteWithin's whole time (the review
// of #170, finding 1).
func WriteBy(ctx context.Context, by time.Time) context.Context {
	if had, ok := ctx.Value(writeByKey{}).(time.Time); ok && had.Before(by) {
		return ctx
	}
	return context.WithValue(ctx, writeByKey{}, by)
}

// bound starts the time of a write made with WriteWithin or WriteBy.
func (a *Anchored) bound(ctx context.Context) (context.Context, context.CancelFunc) {
	d, within := ctx.Value(writeWithinKey{}).(time.Duration)
	by, fixed := ctx.Value(writeByKey{}).(time.Time)
	within = within && d > 0
	if !within && !fixed {
		return ctx, func() {}
	}
	now := a.now()
	if within && (!fixed || now.Add(d).Before(by)) {
		by = now.Add(d)
	}
	ctx, cancel := context.WithTimeout(ctx, by.Sub(now))
	return context.WithValue(ctx, stageByKey{}, by), cancel
}

// commit stores r as a version of its own and appends the commit that makes
// it the newest. The value goes first: a commit never names a value that was
// not stored, and a value without a commit is nothing. It returns the commit
// it tried to make, also when another one took its version (ErrExists). A
// write whose time ran out (WriteWithin) stores nothing.
func (a *Anchored) commit(ctx context.Context, r *Record, version uint64) (*Commit, error) {
	if by, ok := ctx.Value(stageByKey{}).(time.Time); ok && (!a.now().Before(by) || errors.Is(ctx.Err(), context.DeadlineExceeded)) {
		return nil, ErrTooLate
	}
	c := Commit{Name: r.Name, Version: version, Digest: r.digest(), At: a.now().UTC().Truncate(time.Second)}
	value := *r
	value.Name = versioned(&c)
	if err := a.Records.PutRecord(ctx, &value, false); err != nil {
		return nil, err
	}
	stored, err := a.Commits.Append(ctx, c)
	if err != nil {
		return &c, err
	}
	a.mu.Lock()
	a.remember(*stored)
	a.mu.Unlock()
	return stored, nil
}

// PutRecordAt makes r the newest version of its record only if nothing was
// committed for the record since it was read at the mark: no other value, and
// no removal. Otherwise it fails with ErrChanged and the record stays as the
// other enclave left it; a record that was removed meanwhile is not brought
// back.
//
// It rests on the store of commits taking one commit per version: this one
// goes at the version the mark names, the one any other writer that read no
// later would take too. PutRecord and RemoveRecord are not held back by it:
// they write whatever the record is.
func (a *Anchored) PutRecordAt(ctx context.Context, r *Record, at Mark) error {
	ctx, cancel := a.bound(ctx)
	defer cancel()
	if at.next == 0 || at.name != r.Name {
		return errors.New("store: a record is stored at the mark it was read at")
	}
	if a.Strict {
		// Read the history again: nothing is appended to a forked record, nor
		// over a commit made since the mark.
		_, next, err := a.head(ctx, r.Name)
		if err != nil {
			return err
		}
		if next != at.next {
			return ErrChanged
		}
	}
	c, err := a.commit(ctx, r, at.next)
	if errors.Is(err, ErrExists) {
		if err := a.discard(ctx, c); err != nil {
			return err // a strict record forked meanwhile: ErrForked, before ErrChanged
		}
		return ErrChanged
	}
	if err != nil {
		return err
	}
	if a.OnlyNewest && at.head != nil && !at.head.Gone() {
		// As in PutRecord: the write is done whatever happens here.
		_ = a.Records.DeleteRecord(ctx, versioned(at.head))
	}
	return nil
}

// discard deletes the value stored for a commit that was not made, so that it
// does not stay in the storage. It leaves it when it cannot tell that nobody
// reads it: two enclaves that store the same value at the same version store
// it under the same name, and the one whose commit was made needs it. It
// returns ErrForked when the history it reads again has a fork (only a
// strict record's can), and nothing otherwise.
func (a *Anchored) discard(ctx context.Context, c *Commit) error {
	head, _, err := a.head(ctx, c.Name)
	if errors.Is(err, ErrForked) {
		return err
	}
	if err != nil || head == nil {
		return nil
	}
	switch {
	case head.Version == c.Version && versioned(head) == versioned(c):
		return nil // the commit that was made names this very value
	case head.Version > c.Version && !head.Gone() && !a.OnlyNewest:
		return nil // every version is kept, and the one made at this version may be this value
	}
	_ = a.Records.DeleteRecord(ctx, versioned(c))
	return nil
}

// PutRecord implements Records: r becomes the newest version. With create it
// fails with ErrExists when the record has a committed value that is still
// there; a record that was lost or removed can be created again, as a later
// version.
func (a *Anchored) PutRecord(ctx context.Context, r *Record, create bool) error {
	ctx, cancel := a.bound(ctx)
	defer cancel()
	for range maxChanges {
		head, next, err := a.head(ctx, r.Name)
		if err != nil {
			return err
		}
		if create {
			if head != nil {
				if !head.Gone() {
					if _, err := a.committed(ctx, head); err == nil {
						return ErrExists
					} else if !errors.Is(err, ErrLost) {
						return err
					}
				}
			} else if _, err := a.Records.GetRecord(ctx, r.Name); err == nil {
				return ErrExists
			} else if !errors.Is(err, ErrNotFound) {
				return err
			}
		}
		_, err = a.commit(ctx, r, next)
		if err == nil && a.OnlyNewest && head != nil && !head.Gone() {
			// The write is done whatever happens here: a value that stays is
			// not the committed one, and goes when the record is removed.
			_ = a.Records.DeleteRecord(ctx, versioned(head))
		}
		if !errors.Is(err, ErrExists) {
			return err
		}
		if create {
			return ErrExists
		}
		// Another enclave took that version: write after it.
	}
	return a.changing(r.Name)
}

// CreateNew makes a strict record only if no commit of this deployment was
// ever made for its name and nothing is stored under its plain name, so a
// name once made (and removed, or lost) is never made again. In that order:
// ErrForked when its history has a fork (also one found after its append was
// refused), ErrRolledBack when the store went back from what this enclave
// saw, then ErrExists for any commit (a removal included, and one whose value
// is lost) or a value under the plain name. Two enclaves that make one record
// at once make it once: the other is told ErrExists.
//
// A try whose append was refused leaves the value it staged when no commit
// is there to say it is not another enclave's (an orphan: a value no commit
// names, which nothing reads; RemoveRecord deletes it with the rest).
//
// It is the only way a strict record is made, and strictness belongs to the
// Anchored, not to the name: the layer that composes the store must make
// every strict record with CreateNew, and must never read or write a strict
// name through an Anchored that is not Strict (that one would read by Head,
// adopt a plain value and write over a fork).
func (a *Anchored) CreateNew(ctx context.Context, r *Record) error {
	if !a.Strict {
		return errors.New("store: CreateNew is for strict records")
	}
	ctx, cancel := a.bound(ctx)
	defer cancel()
	for range maxChanges {
		head, next, err := a.head(ctx, r.Name)
		if err != nil {
			return err
		}
		if head != nil {
			return ErrExists
		}
		if _, err := a.Records.GetRecord(ctx, r.Name); err == nil {
			return ErrExists
		} else if !errors.Is(err, ErrNotFound) {
			return err
		}
		c, err := a.commit(ctx, r, next)
		if !errors.Is(err, ErrExists) {
			return err
		}
		// Something took the version meanwhile: another enclave's commit, or
		// something that is not a commit, after which this one goes on.
		head, _, err = a.head(ctx, r.Name)
		if err != nil {
			return err
		}
		if head != nil {
			if err := a.discard(ctx, c); err != nil {
				return err
			}
			return ErrExists
		}
		// No commit is there yet, which does not say that none will name the
		// value staged for this try: another enclave making the same record
		// with the same bytes staged it under the same name, and its append
		// may be under way (S3 answers 409 to one while the other's lands).
		// So it stays: a value no commit names is nothing, and costs only its
		// space; one deleted under an acknowledged commit is a record lost.
	}
	return a.changing(r.Name)
}

// maxVersionPages bounds how many pages of stored versions one removal
// deletes: whoever writes to the table can add them faster than they go.
const maxVersionPages = 100

// RemoveRecord removes a record for good. A commit says it is gone, and then
// every version of it that is stored is deleted: its name, an earlier copy
// put back in the table and a commit hidden from a plain request all lead to
// a record that is not there (ErrNotFound, not ErrLost). The commit goes
// first, so a failure in between leaves values nobody reads; calling it again
// deletes them. Removing what is not there removes nothing and succeeds.
func (a *Anchored) RemoveRecord(ctx context.Context, name string) error {
	for range maxChanges {
		head, next, err := a.head(ctx, name)
		if err != nil {
			return err
		}
		if head != nil && head.Gone() {
			return a.deleteVersions(ctx, name)
		}
		stored, err := a.Commits.Append(ctx, Commit{Name: name, Version: next, At: a.now().UTC().Truncate(time.Second)})
		if errors.Is(err, ErrExists) {
			continue // another enclave wrote meanwhile: what it wrote goes too
		}
		if err != nil {
			return err
		}
		a.mu.Lock()
		a.remember(*stored)
		a.mu.Unlock()
		return a.deleteVersions(ctx, name)
	}
	return a.changing(name)
}

// deleteVersions deletes everything stored of a record that was removed.
func (a *Anchored) deleteVersions(ctx context.Context, name string) error {
	// The versions are under the name and an @, which no record's name holds.
	for range maxVersionPages {
		names, err := a.Records.ListRecords(ctx, name+"@", 100)
		if err != nil {
			return err
		}
		if len(names) == 0 {
			// What a release from before commits existed left under the name.
			return a.Records.DeleteRecord(ctx, name)
		}
		for _, n := range names {
			if err := a.Records.DeleteRecord(ctx, n); err != nil {
				return err
			}
		}
	}
	if a.Strict {
		return errors.New("store: the stored versions of a strict record keep coming back")
	}
	return fmt.Errorf("store: the stored versions of %s keep coming back", name)
}

// CheckHistory reads every version of name's commits, as a strict read
// does, whatever a.Strict: ErrForked when two commits of different digests
// share a version anywhere in its history, ErrRolledBack when the store of
// commits went back from what this enclave saw, and an error when Commits
// does not read histories. It reads no value and writes nothing, so it may
// check a record that is not strict, whose reads and writes stay as they
// are: an answer resting on such a record (receipts' Removing, the review
// of #170, finding 2) is then refused on a fork that a read of its newest
// commit alone would pass. Its errors do not hold the record's name.
func (a *Anchored) CheckHistory(ctx context.Context, name string) error {
	_, _, err := a.headAs(ctx, name, true)
	return err
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

// MemoryCommits is Commits in memory, for dev mode and tests. It is also a
// HistoryReader, and holds what S3 can: two commits at one version (Fork) and
// copies of a commit, locked or not (Copy). Every commit Append or Fork makes
// is locked, as Append's are in S3.
type MemoryCommits struct {
	// Group, when set, groups names as S3Commits.Group does, for
	// HistoryRange.
	Group func(name string) (group, member string, ok bool)

	mu      sync.Mutex
	commits map[string][]Commit
	// taken are versions where something that is not a commit of this
	// deployment sits, as anyone who can write to the real store can leave.
	taken map[string]map[uint64]bool
	// copies are commits written again at their version, and whether each
	// copy is locked.
	copies map[string][]commitCopy
}

// NewMemoryCommits returns an empty MemoryCommits.
func NewMemoryCommits() *MemoryCommits {
	return &MemoryCommits{commits: map[string][]Commit{}, taken: map[string]map[uint64]bool{}}
}

// Head implements Commits.
func (m *MemoryCommits) Head(_ context.Context, name string) (*Commit, uint64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	latest, next := m.head(name)
	return latest, next, nil
}

// head is Head with m.mu held.
func (m *MemoryCommits) head(name string) (*Commit, uint64) {
	var latest *Commit
	used := map[uint64]bool{}
	// A copy is a commit at its version as the original is, as S3Commits
	// reads one: it stays when the original is forgotten.
	all := slices.Clone(m.commits[name])
	for _, c := range m.copies[name] {
		all = append(all, c.commit)
	}
	for _, c := range all {
		// Of two commits at one version, the one with the smaller digest, as
		// S3Commits.Head answers.
		if latest == nil || c.Version > latest.Version || (c.Version == latest.Version && bytes.Compare(c.Digest[:], latest.Digest[:]) < 0) {
			latest = &c
		}
		used[c.Version] = true
	}
	for v := range m.taken[name] {
		used[v] = true
	}
	next := uint64(1)
	if latest != nil {
		next = latest.Version + 1
	}
	for used[next] {
		next++
	}
	return latest, next
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
	for _, old := range m.copies[c.Name] {
		if old.commit.Version == c.Version {
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

// Forget removes the newest commit of name, not its copies (Copy). The real
// store lets nobody do that; tests use it to see what an enclave does if it
// happens anyway.
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

// History implements HistoryReader: every commit of name, its forks and the
// versions whose commit has no locked copy.
func (m *MemoryCommits) History(_ context.Context, name string) (*History, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	head, next := m.head(name)
	copies := make([]commitCopy, 0, len(m.commits[name])+len(m.copies[name]))
	for _, c := range m.commits[name] {
		copies = append(copies, commitCopy{c, true})
	}
	copies = append(copies, m.copies[name]...)
	h := history(copies)
	h.Head, h.Next = head, next
	return h, nil
}

// Fork adds a second commit at a version of name, with another digest: what
// a delete marker over the first lets an enclave write in S3. Nothing
// refuses it.
func (m *MemoryCommits) Fork(name string, version uint64, digest [sha256.Size]byte) {
	m.mu.Lock()
	defer m.mu.Unlock()
	at := time.Now().UTC().Truncate(time.Second)
	for _, c := range m.commits[name] {
		if c.Version == version {
			at = c.At
		}
	}
	m.commits[name] = append(m.commits[name], Commit{Name: name, Version: version, Digest: digest, At: at})
}

// Copy adds a copy of the commit at a version of name (the one with the
// smaller digest, at a fork), locked or not: what anyone who writes to the
// bucket can do by writing a commit's bytes again at its key. It is the same
// commit. A version without a commit gets nothing.
func (m *MemoryCommits) Copy(name string, version uint64, locked bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var found *Commit
	for _, c := range m.commits[name] {
		if c.Version == version && (found == nil || bytes.Compare(c.Digest[:], found.Digest[:]) < 0) {
			found = &c
		}
	}
	if found == nil {
		return
	}
	if m.copies == nil {
		m.copies = map[string][]commitCopy{}
	}
	m.copies[name] = append(m.copies[name], commitCopy{*found, locked})
}

// commitCopy is one copy of a commit as a history reads it: the commit, and
// whether this copy is locked as Append locks one.
type commitCopy struct {
	commit Commit
	locked bool
}

// history groups copies of commits by version and digest: the forks, the
// versions whose commit has no locked copy, and the head (the highest
// version; of a fork, the smaller digest; of copies, the best locked one).
// Next is left to the caller.
func history(copies []commitCopy) *History {
	type digest struct {
		commit Commit
		locked bool
	}
	byVersion := map[uint64]map[[sha256.Size]byte]*digest{}
	for _, c := range copies {
		if byVersion[c.commit.Version] == nil {
			byVersion[c.commit.Version] = map[[sha256.Size]byte]*digest{}
		}
		d := byVersion[c.commit.Version][c.commit.Digest]
		if d == nil {
			d = &digest{commit: c.commit}
			byVersion[c.commit.Version][c.commit.Digest] = d
		}
		d.locked = d.locked || c.locked
		if c.commit.LockedUntil.After(d.commit.LockedUntil) {
			d.commit = c.commit
		}
	}
	versions := make([]uint64, 0, len(byVersion))
	for v := range byVersion {
		versions = append(versions, v)
	}
	slices.Sort(versions)
	h := &History{}
	for _, v := range versions {
		digests := make([][sha256.Size]byte, 0, len(byVersion[v]))
		unlocked := false
		for d, c := range byVersion[v] {
			digests = append(digests, d)
			unlocked = unlocked || !c.locked
		}
		slices.SortFunc(digests, func(x, y [sha256.Size]byte) int { return bytes.Compare(x[:], y[:]) })
		if len(digests) > 1 {
			h.Forks = append(h.Forks, Fork{Version: v, Digests: digests})
		}
		if unlocked {
			h.Unlocked = append(h.Unlocked, v)
		}
		head := byVersion[v][digests[0]].commit
		h.Head = &head
	}
	return h
}
