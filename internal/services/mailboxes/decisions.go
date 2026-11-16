package mailboxes

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/permaily-org/permaily/internal/envelope"
	"github.com/permaily-org/permaily/internal/inbox"
	"github.com/permaily-org/permaily/internal/store"
)

// The records of the rules' decisions about a mailbox's messages (ADR 0031,
// "Holds that cannot be undone"): for each day, a chain of blocks, each a
// strict anchored record of Config.Records named by inbox.RuleDecisionsName,
// sealed (binary) under a key derived from the root secret and bound to its
// name, made only with CreateNew and changed only at the version read.
// inbox.BlockDecisions reads and writes them through DecisionStore; they are
// removed with the mailbox, by walking its days (removeDecisions).

// decisionsPurpose is the purpose of the key the blocks are sealed with.
const decisionsPurpose = "permaily rule decisions v1"

// decisionBlockMax bounds a block as stored: its sealed value within a
// DynamoDB item of 400 KiB with the record's other attributes. A block is
// at most inbox.DecisionsBytesMax of JSON (300 KiB), and sealing adds a
// fixed overhead, so a write past this is a transition that did not keep
// its bounds: refused, never stored.
const decisionBlockMax = 390 << 10

// errDecisionBlock is a block whose sealed value does not open, or whose
// name is not one of a block: never read as one never written.
var errDecisionBlock = errors.New("mailboxes: a block of decisions cannot be read")

// Decisions is the rules' decisions of this deployment's mailboxes:
// inbox.BlockDecisions over DecisionStore, on the service's clock.
func (s *Service) Decisions() inbox.Decisions {
	return &inbox.BlockDecisions{Store: s.DecisionStore(), Now: s.now}
}

// DecisionStore is the sealed store of the blocks of decisions.
func (s *Service) DecisionStore() inbox.DecisionStore { return decisionStore{s: s} }

type decisionStore struct{ s *Service }

func (d decisionStore) key(ctx context.Context) ([]byte, error) {
	k, err := d.s.cfg.Keys.Derive(ctx, decisionsPurpose)
	if err != nil {
		return nil, fmt.Errorf("mailboxes: the key of the decisions: %w", err)
	}
	return k, nil
}

// ReadBlock implements inbox.DecisionStore: a strict read, made now. A block
// never written (or removed) is nil; one lost, forked or rolled back is the
// store's error; one that does not open is errDecisionBlock.
func (d decisionStore) ReadBlock(ctx context.Context, name string) (*inbox.DecisionRecord, inbox.DecisionMark, error) {
	if !strings.HasPrefix(name, inbox.RecordRuleDecisions) {
		return nil, inbox.DecisionMark{}, errDecisionBlock
	}
	rec, at, err := d.s.cfg.Records.GetRecordAt(ctx, name)
	switch {
	case errors.Is(err, store.ErrLost):
		// Lost is also ErrNotFound: never read as a block never written.
		return nil, inbox.DecisionMark{}, err
	case errors.Is(err, store.ErrNotFound):
		return nil, inbox.DecisionMark{Store: at}, nil
	case err != nil:
		return nil, inbox.DecisionMark{}, err
	}
	key, err := d.key(ctx)
	if err != nil {
		return nil, inbox.DecisionMark{}, err
	}
	plain, err := envelope.Open(key, rec.Value, envelope.AAD("record", name))
	var r inbox.DecisionRecord
	// Not the error of the JSON: it could repeat what was sealed.
	if err != nil || json.Unmarshal(plain, &r) != nil {
		return nil, inbox.DecisionMark{}, errDecisionBlock
	}
	return &r, inbox.DecisionMark{Versions: at.Version(), Store: at}, nil
}

// WriteBlock implements inbox.DecisionStore: a block never written is made
// with CreateNew (store.ErrChanged when anybody made it meanwhile, or it was
// ever made and removed: a removed mailbox's blocks are never made again),
// any other written with PutRecordAt at the mark it was read at.
func (d decisionStore) WriteBlock(ctx context.Context, name string, r *inbox.DecisionRecord, at inbox.DecisionMark) error {
	if !strings.HasPrefix(name, inbox.RecordRuleDecisions) {
		return errDecisionBlock
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	key, err := d.key(ctx)
	if err != nil {
		return err
	}
	sealed, err := envelope.Seal(key, raw, envelope.AAD("record", name))
	if err != nil {
		return err
	}
	if len(sealed) > decisionBlockMax {
		return errors.New("mailboxes: a block of decisions is larger than an item")
	}
	rec := &store.Record{Name: name, Value: sealed, UpdatedAt: d.s.now()}
	if at.Versions == 0 {
		if err := d.s.cfg.Records.CreateNew(ctx, rec); errors.Is(err, store.ErrExists) {
			return store.ErrChanged
		} else if err != nil {
			return err
		}
		return nil
	}
	return d.s.cfg.Records.PutRecordAt(ctx, rec, at.Store)
}

// decisionsBefore is how far before its removal the days of a mailbox
// whose creation is not known (its record lost) are walked: its longest
// retention, a day, and the decisions' hour (inbox.DecisionKeptAfter).
const decisionsBefore = (365 + 2) * 24 * time.Hour

// removeDecisions removes every block of the mailbox's decisions, by
// walking its days from its creation (from) to now, and each day's blocks
// by their Next from block 0, never an index (ADR 0031, "How long"): the
// last block of a day first, so a removal stopped half way leaves block 0,
// from which the next sweep walks again. A block that is forked takes no
// removal and stays (as a forked mailbox's record does); the walk goes on
// past it, and past a lost one, to the block after. It runs in the sweep's
// last pass of a mailbox that is leaving, once no request can write.
func (s *Service) removeDecisions(ctx context.Context, id string, from time.Time) error {
	if from.IsZero() {
		from = s.now().Add(-decisionsBefore)
	}
	end := s.now().Add(24 * time.Hour)
	for day := from.UTC().Truncate(24 * time.Hour); !day.After(end); day = day.AddDate(0, 0, 1) {
		if err := ctx.Err(); err != nil {
			return err
		}
		names, err := s.dayBlocks(ctx, id, day)
		if err != nil {
			return err
		}
		for i := len(names) - 1; i >= 0; i-- {
			if err := s.cfg.Records.RemoveRecord(ctx, names[i]); err != nil && !errors.Is(err, store.ErrForked) {
				return err
			}
		}
	}
	return nil
}

// dayBlocks are the names of a day's blocks of decisions that exist.
func (s *Service) dayBlocks(ctx context.Context, id string, day time.Time) ([]string, error) {
	at := inbox.NewMessageID(day) // a message of that day, whose day names its blocks
	ds := s.DecisionStore()
	var names []string
	for i := range inbox.DecisionBlocksMax {
		name, err := inbox.RuleDecisionsName(id, at, i)
		if err != nil {
			return nil, err
		}
		r, _, err := ds.ReadBlock(ctx, name)
		switch {
		case err == nil && r == nil:
			return names, nil // never written, or removed: the chain ends
		case err == nil:
			names = append(names, name)
			if !r.Next {
				return names, nil
			}
		case errors.Is(err, store.ErrForked):
			// Taken no removal; its Next cannot be read: the next is tried.
		case errors.Is(err, store.ErrLost), errors.Is(err, store.ErrRolledBack), errors.Is(err, errDecisionBlock):
			names = append(names, name) // removed by its history; its Next cannot be read
		default:
			return nil, err
		}
	}
	return names, nil
}
