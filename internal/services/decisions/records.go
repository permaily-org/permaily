package decisions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/permaily-org/permaily/internal/envelope"
	"github.com/permaily-org/permaily/internal/store"
)

// The service's records are strict anchored records (Config.Records),
// sealed with a key derived from the root secret for each purpose, and
// bound to their name. They are made only with CreateNew and changed only
// at the version read.

// The purposes of the keys the records are sealed with (Keys).
const (
	purposeKey    = "permaily model key v1"
	purposeSpend  = "permaily model spend v1"
	purposeCanary = "permaily model canary v1"
	purposeAlarms = "permaily model alarms v1"
)

// errUnreadable is a record that is lost (store.ErrLost), forked
// (store.ErrForked), refused because its commits went back
// (store.ErrRolledBack), or that does not open: it is never read as zero.
// errForked is the one of a fork, which stays for as long as both commits
// exist.
var (
	errUnreadable = errors.New("decisions: a record of the model cannot be read")
	errForked     = fmt.Errorf("%w: it is forked", errUnreadable)
	// errBusy is a record that kept changing for RecordTries tries.
	errBusy = errors.New("decisions: a record of the model kept changing")
)

// recordAAD binds a sealed record to its name.
func recordAAD(name string) []byte { return envelope.AAD("record", name) }

// sealingKey is the key of a purpose, derived from the root secret.
func (s *Service) sealingKey(ctx context.Context, purpose string) ([]byte, error) {
	key, err := s.cfg.Keys.Derive(ctx, purpose)
	if err != nil {
		return nil, errors.New("decisions: a sealing key could not be derived")
	}
	return key, nil
}

// readRecord reads a record into doc and says where it was read: found
// false (and doc untouched) when it was never made; errForked or
// errUnreadable when it cannot be trusted; another error (which holds no
// name) when the storage failed.
func readRecord[T any](ctx context.Context, s *Service, name, purpose string, doc *T) (store.Mark, bool, error) {
	rec, at, err := s.cfg.Records.GetRecordAt(ctx, name)
	switch {
	case errors.Is(err, store.ErrForked):
		return at, false, errForked
	case errors.Is(err, store.ErrLost), errors.Is(err, store.ErrRolledBack):
		return at, false, errUnreadable
	case errors.Is(err, store.ErrNotFound):
		return at, false, nil
	case err != nil:
		return at, false, errors.New("decisions: a record of the model could not be read")
	}
	key, err := s.sealingKey(ctx, purpose)
	if err != nil {
		return at, false, err
	}
	plain, err := envelope.Open(key, rec.Value, recordAAD(name))
	// Not the error of the JSON: it could repeat what was sealed.
	if err != nil || json.Unmarshal(plain, doc) != nil {
		return at, false, errUnreadable
	}
	return at, true, nil
}

// writeRecord seals doc and stores it as the newest version of its record:
// at the version read, or made with CreateNew when it was never made.
// store.ErrChanged and store.ErrExists say another enclave wrote first.
func writeRecord[T any](ctx context.Context, s *Service, name, purpose string, doc *T, at store.Mark, found bool) error {
	key, err := s.sealingKey(ctx, purpose)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(doc)
	if err != nil {
		return err
	}
	sealed, err := envelope.Seal(key, raw, recordAAD(name))
	if err != nil {
		return err
	}
	r := &store.Record{Name: name, Value: sealed, UpdatedAt: s.now()}
	if !found {
		return s.cfg.Records.CreateNew(ctx, r)
	}
	return s.cfg.Records.PutRecordAt(ctx, r, at)
}

// changeRecord reads a record, lets edit change it (found false for one
// never made, doc then its zero value), and writes it at the version read,
// reading again and editing again when another enclave wrote first, at
// most RecordTries times. edit returns false to write nothing. It returns
// the record as written, or as read when nothing was written.
func changeRecord[T any](ctx context.Context, s *Service, name, purpose string, edit func(doc *T, found bool) bool) (*T, error) {
	for range RecordTries {
		var doc T
		at, found, err := readRecord(ctx, s, name, purpose, &doc)
		if err != nil {
			return nil, err
		}
		if !edit(&doc, found) {
			return &doc, nil
		}
		err = writeRecord(ctx, s, name, purpose, &doc, at, found)
		switch {
		case err == nil:
			return &doc, nil
		case errors.Is(err, store.ErrForked):
			return nil, errForked
		case errors.Is(err, store.ErrChanged), errors.Is(err, store.ErrExists):
			continue
		default:
			return nil, errors.New("decisions: a record of the model could not be written")
		}
	}
	return nil, errBusy
}
