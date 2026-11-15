package inbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/permaily-org/permaily/internal/envelope"
	"github.com/permaily-org/permaily/internal/store"
)

// A message's kept decisions on the store (ADR 0028, "Kept decisions", its
// pull request 3): one entry per message, dec#<id> in its mailbox's
// partition, sealed under the mailbox's key and bound to the message,
// expiring with it; written at the version read, the first answer kept for a
// key the one every enclave uses, and never kept for a message that is gone
// (the mark in GonePartition, read after each write).

// KeptEntries is what NewKeptStore needs of the store: an entry read, written
// at a version, and deleted as it was written. *store.Memory and *store.AWS
// implement it.
type KeptEntries interface {
	GetEntry(ctx context.Context, partition, key string) (*store.Entry, error)
	PutEntry(ctx context.Context, e *store.Entry) error
	DeleteEntryAt(ctx context.Context, e *store.Entry) error
}

// NewKeptStore is the KeptStore on the store.
func NewKeptStore(st KeptEntries) KeptStore { return &keptStore{st: st, now: time.Now} }

type keptStore struct {
	st  KeptEntries
	now func() time.Time
}

// keptFormat is the KeptDecisions.V written.
const keptFormat = 1

// keptMaxViews is how many views one entry keeps: the sixteen
// TestAFullKeptEntryFitsAnItem bounds an entry with. A view past it is not
// added, and an ask for it reads the text each time.
const keptMaxViews = 16

// GoneKey is the key of a message's mark in GonePartition.
func GoneKey(mailboxID, id string) string { return mailboxID + "#" + id }

// errKeepContended is Keep's answer when another writer changed the entry
// KeepTries times in a row: nothing of add is kept.
var errKeepContended = fmt.Errorf("inbox: kept decisions changed %d times while they were written: %w", KeepTries, store.ErrConflict)

func decisionAAD(b Box, id string) []byte { return envelope.AAD(PurposeDecision, b.MailboxID, id) }

// open reads a kept entry's value: nil when it does not open under the box's
// key for this message, or is not KeptDecisions (it is then asked again, and
// what is asked replaces it).
func openKept(b Box, id string, value []byte) *KeptDecisions {
	plain, err := envelope.OpenText(b.Key, value, decisionAAD(b, id))
	if err != nil {
		return nil
	}
	var d KeptDecisions
	if json.Unmarshal(plain, &d) != nil || d.V != keptFormat {
		return nil
	}
	return &d
}

// read is the entry as stored (nil when there is none) and what it holds
// (nil when it holds nothing that opens).
func (k *keptStore) read(ctx context.Context, b Box, id string) (*store.Entry, *KeptDecisions, error) {
	e, err := k.st.GetEntry(ctx, Partition(b.MailboxID), KeyDecision+id)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	return e, openKept(b, id, e.Value), nil
}

// Kept implements KeptStore.
func (k *keptStore) Kept(ctx context.Context, b Box, id string) (*KeptDecisions, error) {
	if !isMessageIDShape(id) {
		return nil, nil
	}
	_, d, err := k.read(ctx, b, id)
	return d, err
}

// answerKey is what one answer is kept by (ADR 0028, "The key of an
// answer"); the message is the entry.
type answerKey struct{ content, v, op, topic, model, prompt string }

func keyOf(a *KeptAnswer) answerKey {
	return answerKey{a.Content, a.Var, a.Op, a.Topic, a.Model, a.Prompt}
}

// merge is cur with what add has that cur has not: an answer whose key is not
// kept, while there is room for it, and a view not kept, while there is room
// for it. It reports whether it added anything. cur is never changed.
func merge(cur, add *KeptDecisions) (*KeptDecisions, bool) {
	out := &KeptDecisions{V: keptFormat, Views: map[string]KeptView{}, Answers: []KeptAnswer{}}
	have := map[answerKey]bool{}
	if cur != nil {
		for name, v := range cur.Views {
			out.Views[name] = v
		}
		for i := range cur.Answers {
			out.Answers = append(out.Answers, cur.Answers[i])
			have[keyOf(&cur.Answers[i])] = true
		}
	}
	changed := false
	if add == nil {
		return out, false
	}
	for name, v := range add.Views {
		if _, ok := out.Views[name]; ok || len(out.Views) >= keptMaxViews {
			continue // the first kept for a view stays
		}
		out.Views[name] = v
		changed = true
	}
	for i := range add.Answers {
		a := add.Answers[i]
		if have[keyOf(&a)] || len(out.Answers) >= KeptMaxAnswers {
			continue // the first kept for a key stays; past the bound nothing is added
		}
		have[keyOf(&a)] = true
		out.Answers = append(out.Answers, a)
		changed = true
	}
	return out, changed
}

// Keep implements KeptStore.
func (k *keptStore) Keep(ctx context.Context, b Box, id string, expires time.Time, add *KeptDecisions) (*KeptDecisions, error) {
	if !isMessageIDShape(id) {
		return nil, ErrGone // no message has this identifier
	}
	for range KeepTries {
		e, cur, err := k.read(ctx, b, id)
		if err != nil {
			return nil, err
		}
		next, changed := merge(cur, add)
		if !changed {
			// Nothing to write: what is kept is what the caller uses, unless
			// the message is being deleted.
			if err := k.gone(ctx, b, id, nil); err != nil {
				return nil, err
			}
			return next, nil
		}
		plain, err := json.Marshal(next)
		if err != nil {
			return nil, err
		}
		sealed, err := envelope.SealText(b.Key, plain, decisionAAD(b, id))
		if err != nil {
			return nil, err
		}
		w := &store.Entry{Partition: Partition(b.MailboxID), Key: KeyDecision + id, Value: sealed, ExpiresAt: expires, UpdatedAt: k.now()}
		if e != nil {
			w.Version = e.Version
		}
		err = k.st.PutEntry(ctx, w)
		if errors.Is(err, store.ErrExists) || errors.Is(err, store.ErrConflict) {
			continue // another writer was first: read what it wrote, add to it
		}
		if err != nil {
			return nil, err
		}
		if err := k.gone(ctx, b, id, w); err != nil {
			return nil, err
		}
		return next, nil
	}
	return nil, errKeepContended
}

// gone reads the message's mark after a write (ADR 0028, "Deleted with the
// message, never written after"): when it is there, what was written (w,
// when something was) is deleted as it was written, and the answer is
// ErrGone. A mark that cannot be read is an error: nothing new is used, and
// what was written stays for the sweep's pass over the marks, if the message
// is being deleted, or for the next ask, if it is not.
func (k *keptStore) gone(ctx context.Context, b Box, id string, w *store.Entry) error {
	_, err := k.st.GetEntry(ctx, GonePartition, GoneKey(b.MailboxID, id))
	if errors.Is(err, store.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if w != nil {
		// ErrConflict: the deletion, or another writer after it, already
		// changed it; the deletion and the sweep's pass delete whatever is
		// left.
		if err := k.st.DeleteEntryAt(ctx, w); err != nil && !errors.Is(err, store.ErrConflict) {
			return err
		}
	}
	return ErrGone
}
