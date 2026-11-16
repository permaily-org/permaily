package inbox

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/permaily-org/permaily/internal/rules"
	"github.com/permaily-org/permaily/internal/store"
)

// BlockDecisions is the one implementation of Decisions (ADR 0031,
// "Changed after the third design review"): the blocks of each day the call
// touches read strictly and now by following Next from block 0, the
// transitions of rule_decisions.go applied, and the blocks they changed
// written in order, each only if nothing was committed to it since it was
// read; all of it again, read afresh, on store.ErrChanged, at most
// DecisionTries times. services/mailboxes gives it its strict anchored
// records, sealed (pull request 4); inbox/disclosetest gives it memory, and
// strict anchored records unsealed, for the contract both pass. Nothing of
// one call is held between two of its reads: what a write committed is read
// back as any other request's would be, so no request can rely on a block
// it read before another wrote it.
type BlockDecisions struct {
	Store DecisionStore
	Now   func() time.Time
}

// DecisionStore is what BlockDecisions needs of the records its blocks are
// kept in, named by RuleDecisionsName.
type DecisionStore interface {
	// ReadBlock reads one block strictly and now: nil for one never
	// written, an error for one that cannot be read (lost, rolled back,
	// forked: every message of its day is held), and where it was read.
	ReadBlock(ctx context.Context, name string) (*DecisionRecord, DecisionMark, error)
	// WriteBlock stores r only if nothing was committed to it since at, and
	// makes a block read never written only if nobody made it meanwhile:
	// store.ErrChanged otherwise.
	WriteBlock(ctx context.Context, name string, r *DecisionRecord, at DecisionMark) error
}

// DecisionMark is where a block was read: its committed versions (0 for
// one never written), which the transitions bound, and the store's mark.
type DecisionMark struct {
	Versions uint64
	Store    store.Mark
}

// ErrDecisionsChanged is a day whose blocks changed at every try: every
// message of it is held, undecided.
var ErrDecisionsChanged = fmt.Errorf("inbox: a day's decisions changed %d times while they were written: %w", DecisionTries, store.ErrChanged)

// errMisfiled is a block whose document names another place than its own.
var errMisfiled = errors.New("inbox: a block of decisions is not the one its name says")

func (d *BlockDecisions) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

// days groups ids by the day of their identifier, in order.
func days[T any](ids []string, of func(string) T) (map[string][]T, []string, error) {
	by := map[string][]T{}
	var order []string
	for _, id := range ids {
		day, err := DecisionDay(id)
		if err != nil {
			return nil, nil, err
		}
		if _, ok := by[day]; !ok {
			order = append(order, day)
		}
		by[day] = append(by[day], of(id))
	}
	slices.Sort(order)
	return by, order, nil
}

// read reads a day's chain and where each block was read.
func (d *BlockDecisions) read(ctx context.Context, mailbox, day string) (*DecisionBlocks, []DecisionMark, error) {
	blocks := &DecisionBlocks{Mailbox: mailbox, Day: day}
	var marks []DecisionMark
	for i := range DecisionBlocksMax {
		r, at, err := d.Store.ReadBlock(ctx, decisionsName(mailbox, day, i))
		if err != nil {
			return nil, nil, err
		}
		if r == nil {
			break // never written: the day has none yet, or a Next's successor a crash left
		}
		if r.Mailbox != mailbox || r.Day != day || r.Block != i {
			return nil, nil, errMisfiled
		}
		blocks.Blocks = append(blocks.Blocks, r)
		blocks.Versions = append(blocks.Versions, at.Versions)
		marks = append(marks, at)
		if !r.Next {
			break
		}
	}
	return blocks, marks, nil
}

// write writes the changed blocks in order and answers those it wrote.
func (d *BlockDecisions) write(ctx context.Context, blocks *DecisionBlocks, marks []DecisionMark, changed []int) ([]int, error) {
	var done []int
	for _, i := range changed {
		var at DecisionMark
		if i < len(marks) {
			at = marks[i]
		}
		if err := d.Store.WriteBlock(ctx, decisionsName(blocks.Mailbox, blocks.Day, i), blocks.Blocks[i], at); err != nil {
			return done, err
		}
		done = append(done, i)
	}
	return done, nil
}

// Begin implements Decisions.
func (d *BlockDecisions) Begin(ctx context.Context, b Box, rule rules.Ref, ids []DecisionKey) (map[string]Decision, error) {
	keyOf := map[string]DecisionKey{}
	var names []string
	for _, k := range ids {
		if _, seen := keyOf[k.ID]; !seen {
			names = append(names, k.ID)
		}
		keyOf[k.ID] = k
	}
	by, order, err := days(names, func(id string) DecisionKey { return keyOf[id] })
	if err != nil {
		return nil, err
	}
	out := make(map[string]Decision, len(names))
	for _, day := range order {
		got, err := d.beginDay(ctx, b.MailboxID, day, rule, by[day])
		if err != nil {
			return nil, err
		}
		for id, dec := range got {
			out[id] = dec
		}
	}
	return out, nil
}

// beginDay is Begin on one day. A try whose later block was refused after
// an earlier one was written began what that one holds: the next try
// answers them Begun still, so its caller asks for them and settles them.
func (d *BlockDecisions) beginDay(ctx context.Context, mailbox, day string, rule rules.Ref, keys []DecisionKey) (map[string]Decision, error) {
	mine := map[string]time.Time{}
	for range DecisionTries {
		blocks, marks, err := d.read(ctx, mailbox, day)
		if err != nil {
			return nil, err
		}
		got, changed := blocks.Begin(rule, keys, d.now())
		for id, at := range mine {
			if g := got[id]; g.State == DecisionPending && g.At.Equal(at) {
				got[id] = Decision{State: DecisionBegun, At: at}
			}
		}
		done, err := d.write(ctx, blocks, marks, changed)
		for _, i := range done {
			for id, g := range got {
				if g.State == DecisionBegun && blocks.Blocks[i].find(rule, id) >= 0 {
					mine[id] = g.At
				}
			}
		}
		if errors.Is(err, store.ErrChanged) {
			continue
		}
		if err != nil {
			return nil, err
		}
		return got, nil
	}
	return nil, ErrDecisionsChanged
}

// Settle implements Decisions.
func (d *BlockDecisions) Settle(ctx context.Context, b Box, rule rules.Ref, settle map[string]Settlement) (map[string]Decision, error) {
	names := make([]string, 0, len(settle))
	for id := range settle {
		names = append(names, id)
	}
	slices.Sort(names)
	by, order, err := days(names, func(id string) string { return id })
	if err != nil {
		return nil, err
	}
	out := make(map[string]Decision, len(settle))
	for _, day := range order {
		s := make(map[string]Settlement, len(by[day]))
		for _, id := range by[day] {
			s[id] = settle[id]
		}
		got, err := d.settleDay(ctx, b.MailboxID, day, rule, s)
		if err != nil {
			return nil, err
		}
		for id, dec := range got {
			out[id] = dec
		}
	}
	return out, nil
}

// settleDay is Settle on one day; a try again finds what an earlier one
// wrote final, and answers it as it is.
func (d *BlockDecisions) settleDay(ctx context.Context, mailbox, day string, rule rules.Ref, settle map[string]Settlement) (map[string]Decision, error) {
	for range DecisionTries {
		blocks, marks, err := d.read(ctx, mailbox, day)
		if err != nil {
			return nil, err
		}
		got, changed := blocks.Settle(rule, settle, d.now())
		_, err = d.write(ctx, blocks, marks, changed)
		if errors.Is(err, store.ErrChanged) {
			continue
		}
		if err != nil {
			return nil, err
		}
		return got, nil
	}
	return nil, ErrDecisionsChanged
}
