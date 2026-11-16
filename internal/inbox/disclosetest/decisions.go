// Package disclosetest is the shared stand-in for what ADR 0030's pull
// request 4 builds of disclosure, for the pull requests written at the same
// time against its seam (ADR 0030, "Implementation pull requests"): the one
// implementation of inbox.Decisions (inbox.BlockDecisions) over blocks kept
// in memory, each its own record changed only at the version read, as
// strict records are; the same over strict anchored records (Anchored); and
// the contract every store of blocks passes, services/mailboxes' sealed one
// included. Never imported outside tests (depguard).
package disclosetest

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"sync"
	"time"

	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/store"
)

// ItemMax is the largest sealed item the fake's store takes, as DynamoDB's
// 400 KiB: a block whose JSON (sealing adds a fixed overhead) is past it is
// a write that fails, so a transition that did not keep its bounds is
// caught here as it would be in the store.
const ItemMax = 400 << 10

// Records is an in-memory inbox.DecisionStore: each block its own record
// with its count of versions, read and written one at a time (never a day
// at once: two calls interleave between their reads and their writes as
// two enclaves do), a write refused with store.ErrChanged when the block
// was committed since it was read, and each written block checked against
// ItemMax.
type Records struct {
	mu     sync.Mutex
	blocks map[string]block
	// fail, when set, is returned by every read and write.
	fail error
}

type block struct {
	data     []byte
	versions uint64
}

// NewRecords is an empty Records.
func NewRecords() *Records { return &Records{blocks: map[string]block{}} }

// errTooLarge is a block past ItemMax.
var errTooLarge = errors.New("disclosetest: a block is larger than an item")

// Fail makes every later read and write fail with err (nil: none).
func (r *Records) Fail(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.fail = err
}

// ReadBlock implements inbox.DecisionStore.
func (r *Records) ReadBlock(_ context.Context, name string) (*inbox.DecisionRecord, inbox.DecisionMark, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fail != nil {
		return nil, inbox.DecisionMark{}, r.fail
	}
	b, ok := r.blocks[name]
	if !ok {
		return nil, inbox.DecisionMark{}, nil
	}
	var rec inbox.DecisionRecord
	if err := json.Unmarshal(b.data, &rec); err != nil {
		return nil, inbox.DecisionMark{}, err
	}
	return &rec, inbox.DecisionMark{Versions: b.versions}, nil
}

// WriteBlock implements inbox.DecisionStore.
func (r *Records) WriteBlock(_ context.Context, name string, rec *inbox.DecisionRecord, at inbox.DecisionMark) error {
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	if len(data) > ItemMax {
		return errTooLarge
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.fail != nil {
		return r.fail
	}
	if r.blocks[name].versions != at.Versions {
		return store.ErrChanged
	}
	r.blocks[name] = block{data: data, versions: at.Versions + 1}
	return nil
}

// Age gives a block versions committed versions, as that many writes
// would have: for a schedule that spends a block's history without making
// each write (inbox's own tests make every one).
func (r *Records) Age(name string, versions uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	b := r.blocks[name]
	b.versions = versions
	r.blocks[name] = b
}

// Decisions is inbox.BlockDecisions over Records.
type Decisions struct {
	inbox.BlockDecisions
	Records *Records
}

// NewDecisions is an empty Decisions on the clock now.
func NewDecisions(now func() time.Time) *Decisions {
	r := NewRecords()
	return &Decisions{BlockDecisions: inbox.BlockDecisions{Store: r, Now: now}, Records: r}
}

// Blocks is how many blocks the day of id has.
func (d *Decisions) Blocks(b inbox.Box, id string) int {
	n := 0
	for ; n < inbox.DecisionBlocksMax; n++ {
		name, err := inbox.RuleDecisionsName(b.MailboxID, id, n)
		if err != nil {
			return 0
		}
		d.Records.mu.Lock()
		_, ok := d.Records.blocks[name]
		d.Records.mu.Unlock()
		if !ok {
			break
		}
	}
	return n
}

// Versions is how many versions block of the day of id has.
func (d *Decisions) Versions(b inbox.Box, id string, block int) uint64 {
	name, err := inbox.RuleDecisionsName(b.MailboxID, id, block)
	if err != nil {
		return 0
	}
	d.Records.mu.Lock()
	defer d.Records.mu.Unlock()
	return d.Records.blocks[name].versions
}

// Anchored is an inbox.DecisionStore on strict anchored records, the
// blocks as plain JSON: the store services/mailboxes builds, without its
// sealing, so the contract and the schedules run on the real strict store
// (two enclaves are two Anchored over one Memory and one MemoryCommits).
type Anchored struct {
	Records *store.Anchored
}

// ReadBlock implements inbox.DecisionStore.
func (a Anchored) ReadBlock(ctx context.Context, name string) (*inbox.DecisionRecord, inbox.DecisionMark, error) {
	rec, at, err := a.Records.GetRecordAt(ctx, name)
	if errors.Is(err, store.ErrLost) {
		return nil, inbox.DecisionMark{}, err
	}
	if errors.Is(err, store.ErrNotFound) {
		return nil, inbox.DecisionMark{Store: at}, nil
	}
	if err != nil {
		return nil, inbox.DecisionMark{}, err
	}
	var r inbox.DecisionRecord
	if err := json.Unmarshal(rec.Value, &r); err != nil {
		return nil, inbox.DecisionMark{}, err
	}
	return &r, inbox.DecisionMark{Versions: at.Version(), Store: at}, nil
}

// WriteBlock implements inbox.DecisionStore: a block never written is made
// with CreateNew, any other written with PutRecordAt at its mark.
func (a Anchored) WriteBlock(ctx context.Context, name string, r *inbox.DecisionRecord, at inbox.DecisionMark) error {
	data, err := json.Marshal(r)
	if err != nil {
		return err
	}
	if len(data) > ItemMax {
		return errTooLarge
	}
	rec := &store.Record{Name: name, Value: data}
	if at.Versions == 0 {
		if err := a.Records.CreateNew(ctx, rec); errors.Is(err, store.ErrExists) {
			return store.ErrChanged
		} else if err != nil {
			return err
		}
		return nil
	}
	return a.Records.PutRecordAt(ctx, rec, at.Store)
}

// Enclaves is two enclaves' strict anchored stores of blocks over one
// storage, on the clock now.
func Enclaves(now func() time.Time) (inbox.DecisionStore, inbox.DecisionStore) {
	records, commits := store.NewMemory(), store.NewMemoryCommits()
	one := func() inbox.DecisionStore {
		return Anchored{Records: &store.Anchored{Records: records, Commits: commits, Strict: true, OnlyNewest: true, Now: now}}
	}
	return one(), one()
}

// Shared is two enclaves' Records: the same one, whose blocks are each
// their own record.
func Shared(func() time.Time) (inbox.DecisionStore, inbox.DecisionStore) {
	r := NewRecords()
	return r, r
}

// blockName is block of a day by its parts, for seeding a schedule.
func blockName(mailbox, day string, block int) string {
	return inbox.RecordRuleDecisions + mailbox + "-" + day + "-" + strconv.Itoa(block)
}
