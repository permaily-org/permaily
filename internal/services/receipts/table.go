package receipts

import (
	"bytes"
	"context"
	"errors"

	"github.com/permaily-org/permaily/internal/store"
)

// ChainTable is where the strict store.Anchored of the chain keeps each
// version of its records (ADR 0037, "Where a link is kept"): a
// store.RecordTable over store.Entries, each version at RecordKey, a link's
// in its account's own partition so DeletePartition removes it with the
// receipts. A plain name has no key: it is answered "not found" without a
// read, and nothing is stored under it, so junk written where one would be
// can neither stop a record from being made nor be read as one.
type ChainTable struct {
	Entries store.Entries
}

var _ store.RecordTable = ChainTable{}

// errNoKey refuses a write under a name that is not a stored version of one
// of the chain's records. It holds no name: names hold accounts.
var errNoKey = errors.New("receipts: the chain's table stores only versions of the chain's records")

// errNotListed refuses a listing: the chain's records are never removed, so
// nothing lists their versions.
var errNotListed = errors.New("receipts: the chain's table is not listed")

// GetRecord implements store.Records.
func (t ChainTable) GetRecord(ctx context.Context, name string) (*store.Record, error) {
	partition, key, ok := RecordKey(name)
	if !ok {
		return nil, store.ErrNotFound
	}
	e, err := t.Entries.GetEntry(ctx, partition, key)
	if err != nil {
		return nil, err
	}
	return &store.Record{Name: name, Value: e.Value, UpdatedAt: e.UpdatedAt}, nil
}

// PutRecord implements store.Records: a new entry, or with create false
// one that replaces what is under its key unless it holds the same bytes
// (two enclaves staging the same version, or a retry).
func (t ChainTable) PutRecord(ctx context.Context, r *store.Record, create bool) error {
	partition, key, ok := RecordKey(r.Name)
	if !ok {
		return errNoKey
	}
	e := &store.Entry{Partition: partition, Key: key, Value: r.Value, UpdatedAt: r.UpdatedAt}
	err := t.Entries.PutEntry(ctx, e)
	if !errors.Is(err, store.ErrExists) || create {
		return err
	}
	old, err := t.Entries.GetEntry(ctx, partition, key)
	if err != nil {
		return err
	}
	if bytes.Equal(old.Value, r.Value) {
		return nil
	}
	e.Version = old.Version
	return t.Entries.PutEntry(ctx, e)
}

// ListRecords implements store.RecordTable: never used, refused.
func (ChainTable) ListRecords(context.Context, string, int) ([]string, error) {
	return nil, errNotListed
}

// DeleteRecord implements store.RecordTable: what store.Anchored deletes of
// a value staged and not committed.
func (t ChainTable) DeleteRecord(ctx context.Context, name string) error {
	partition, key, ok := RecordKey(name)
	if !ok {
		return nil
	}
	if err := t.Entries.DeleteEntry(ctx, partition, key); err != nil && !errors.Is(err, store.ErrNotFound) {
		return err
	}
	return nil
}
