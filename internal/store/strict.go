package store

import (
	"context"
	"crypto/sha256"
	"errors"
	"time"
)

// Strict records (ADR 0026, "The store"; ADR 0016). A record kind is strict
// when its Anchored is (Anchored.Strict, next to OnlyNewest). Mailboxes and
// accounts' lists of mailboxes are strict; what production stores before
// them (accounts, the TLS identity, the providers' applications) is not, and
// is read and written exactly as before.

// ErrForked means a strict record has a fork (Fork) at some version of its
// history. A commit is written only where nothing is (S3Commits.Append
// creates its object with If-None-Match), so two commits of different
// digests at one version are there only because whoever writes to the bucket
// hid the first behind a delete marker while an enclave wrote the second.
// Every read and write of the record fails with it (GetRecord, GetRecordAt,
// PutRecordAt, RemoveRecord, CreateNew), on every enclave and after every
// restart, for as long as both commits exist: each was written by an enclave
// with its lock, which nobody can remove during the lock period. It is not
// an ErrNotFound.
var ErrForked = errors.New("store: two commits of a record at one version: the store of commits was written to by someone else")

// Fork is two or more commits of one record at one version whose digests
// differ. Copies of one commit (the same digest, however many object
// versions hold it, locked or not) are one commit, never a fork: a copy of a
// commit's bytes written again at its key is not evidence of anything.
type Fork struct {
	Version uint64
	// Digests are the different digests found at Version, in byte order.
	Digests [][sha256.Size]byte
}

// History is what a strict reader learns of a record's commits: every
// version the store of commits lists, not only the newest.
type History struct {
	// Head is the commit at the highest version, nil when there is none.
	// When Forks is not empty a reader uses nothing of it.
	Head *Commit
	// Next is where the next commit goes: the lowest version above Head that
	// nothing is at (Commits.Head).
	Next uint64
	// Forks are the versions with commits of more than one digest, the
	// lowest first.
	Forks []Fork
	// Unlocked are the versions whose commit has no copy locked as Append
	// locks one: in compliance mode, until no earlier than its At plus
	// MinCommitLock. Append always writes the lock in the same request, so a
	// version listed here means the store of commits did not keep a lock it
	// took (an alarm), never something a reader may decide by: the commit is
	// counted all the same.
	Unlocked []uint64
}

// MinCommitLock is the shortest lock an enclave writes on a commit (the
// staging images' one day). A copy that anyone else writes may carry any
// lock or none, so the lock is checked per digest, on its best copy.
const MinCommitLock = 24 * time.Hour

// MaxHistoryReads bounds the objects one strict read fetches that this
// enclave has not read before (object versions are immutable, so what was
// read of one is kept, by key and version id, for the next read). Past it
// the read fails with an error that is not ErrForked, and the next read goes
// on from what was kept.
const MaxHistoryReads = 500

// HistoryReader reads a record's whole history. S3Commits and MemoryCommits
// implement it; Anchored with Strict reads through it instead of Head, and
// refuses a strict record whose Commits does not implement it. It is not part
// of Commits: a store of commits for records that are not strict needs only
// Head and Append.
type HistoryReader interface {
	History(ctx context.Context, name string) (*History, error)
}
