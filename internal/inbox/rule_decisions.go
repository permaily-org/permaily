package inbox

import (
	"encoding/json"
	"errors"
	"time"

	"github.com/permaily-org/permaily/internal/rules"
)

// The record a protecting rule's decisions are kept in, and its state
// machine (ADR 0031, "Holds that cannot be undone"). services/mailboxes
// keeps one record a mailbox and day as a strict anchored record (ADR 0016,
// 0026), sealed, read and written at the version read; the transitions are
// here, pure, so that its implementation (pull request 4), the fake of
// inbox/disclosetest and the tests of both follow one contract.
//
// A decision goes Pending (begun, before anything is asked) to Held or
// Cleared (settled, final), or is dropped (Abandoned) by the request that
// began it when it used no answer of the model. Final decisions never
// change. A Pending older than PendingGrace is settled Held by whoever
// finds it: its settlement was lost, and what it would have said cannot be
// recovered from the table, so it fails closed.

// RecordRuleDecisions + the mailbox's identifier + "-" + the day (UTC,
// YYYYMMDD, of the message identifier's time) names a record of decisions.
// The day of a message is in its identifier, so which record holds a
// decision cannot be changed by writing to the table; and a day that has
// none was never written, which a strict read tells from one lost.
const RecordRuleDecisions = "rule-decisions-"

// RuleDecisionsName is the record of the decisions about messageID.
func RuleDecisionsName(mailboxID, messageID string) (string, error) {
	day, err := DecisionDay(messageID)
	if err != nil {
		return "", err
	}
	return RecordRuleDecisions + mailboxID + "-" + day, nil
}

// DecisionDay is the day of messageID, UTC, as YYYYMMDD.
func DecisionDay(messageID string) (string, error) {
	if !isMessageIDShape(messageID) {
		return "", ErrNotAMessageID
	}
	return timeOfMessageID(messageID).Format("20060102"), nil
}

// ErrNotAMessageID is an identifier that is not one of a message.
var ErrNotAMessageID = errors.New("inbox: not a message identifier")

// Bounds of one record (finding 11): how many decisions, how many bytes of
// JSON before it is sealed (a DynamoDB item holds 400 KiB with the record's
// other attributes; sealing adds a fixed overhead, not a factor, for a
// binary record), and how many versions (a strict read lists every version
// and fails past 10,000 objects; MaxHistoryReads bounds a cold read, which
// goes on from what it read). Past any of them a new decision is Full: the
// message is held, undecided, for as long as the record has no room.
const (
	DecisionsMax         = 2000
	DecisionsBytesMax    = 300 << 10
	DecisionsVersionsMax = 9000
	// PendingGrace is how long a decision may stay Pending before whoever
	// finds it settles it Held: longer than one message's ask
	// (decide.MessageDeadline, 20 seconds) and its settlement's tries.
	PendingGrace = 2 * time.Minute
	// DecisionTries bounds the reads again after store.ErrChanged.
	DecisionTries = 5
)

// DecisionState is where one decision is.
type DecisionState string

const (
	// Pending: begun and not settled. Stored.
	DecisionPending DecisionState = "pending"
	// Held and Cleared: settled, final. Stored.
	DecisionHeld    DecisionState = "held"
	DecisionCleared DecisionState = "cleared"
	// Begun: Begin's answer for a decision this call began (stored as
	// Pending). The caller may ask the model and must settle it.
	DecisionBegun DecisionState = "begun"
	// Full: Begin's or Settle's answer when the record has no room; nothing
	// was stored for it.
	DecisionFull DecisionState = "full"
	// Abandoned: Settle's input for a decision the caller began and used no
	// answer of the model for; its Pending is dropped. Settle answers it
	// for an id with nothing stored.
	DecisionAbandoned DecisionState = "abandoned"
)

// Decision is a decision as Begin and Settle answer it.
type Decision struct {
	State DecisionState
	// At is when it was begun (Pending, Begun) or settled (Held, Cleared).
	At time.Time
}

// DecisionRecord is one record's document, sealed in the store.
type DecisionRecord struct {
	V       int             `json:"v"`
	Mailbox string          `json:"mailbox"`
	Day     string          `json:"day"`
	Entries []DecisionEntry `json:"entries"`
}

// DecisionEntry is one stored decision.
type DecisionEntry struct {
	Message string        `json:"m"`
	Rule    string        `json:"r"`
	Version int           `json:"rv"`
	State   DecisionState `json:"s"`
	At      time.Time     `json:"at"`
	// Expires is the message's; the entry is dropped from the first version
	// written after it.
	Expires time.Time `json:"x,omitzero"`
}

func (r *DecisionRecord) find(rule rules.Ref, id string) int {
	for i, e := range r.Entries {
		if e.Message == id && e.Rule == rule.ID && e.Version == rule.Version {
			return i
		}
	}
	return -1
}

func (r *DecisionRecord) prune(now time.Time) {
	kept := r.Entries[:0]
	for _, e := range r.Entries {
		if e.Expires.IsZero() || now.Before(e.Expires) {
			kept = append(kept, e)
		}
	}
	r.Entries = kept
}

// fits says the record is within its count and its bytes.
func (r *DecisionRecord) fits() bool {
	if len(r.Entries) > DecisionsMax {
		return false
	}
	b, err := json.Marshal(r)
	return err == nil && len(b) <= DecisionsBytesMax
}

// Begin is the transition of Decisions.Begin on one record read at
// versions (the record's committed versions, 0 for none): it answers each
// id and says whether the record changed and must be written at the version
// read (a write refused with store.ErrChanged is read again and begun
// again, at most DecisionTries times; a write that fails otherwise holds
// every id of it, undecided). ids must all be of this record's day.
func (r *DecisionRecord) Begin(rule rules.Ref, ids []DecisionKey, now time.Time, versions uint64) (map[string]Decision, bool) {
	out := make(map[string]Decision, len(ids))
	var added []string
	for _, k := range ids {
		if _, seen := out[k.ID]; seen {
			continue
		}
		if i := r.find(rule, k.ID); i >= 0 {
			out[k.ID] = Decision{State: r.Entries[i].State, At: r.Entries[i].At}
			continue
		}
		if versions+1 > DecisionsVersionsMax {
			out[k.ID] = Decision{State: DecisionFull}
			continue
		}
		r.Entries = append(r.Entries, DecisionEntry{Message: k.ID, Rule: rule.ID, Version: rule.Version, State: DecisionPending, At: now, Expires: k.ExpiresAt})
		added = append(added, k.ID)
		out[k.ID] = Decision{State: DecisionBegun, At: now}
	}
	if len(added) == 0 {
		return out, false
	}
	r.prune(now)
	for len(added) > 0 && !r.fits() {
		last := added[len(added)-1]
		added = added[:len(added)-1]
		r.Entries = r.Entries[:len(r.Entries)-1]
		out[last] = Decision{State: DecisionFull}
	}
	return out, len(added) > 0
}

// Settle is the transition of Decisions.Settle on one record read at
// versions. For each id: a Pending becomes what settle says (Held, Cleared,
// or dropped for Abandoned); a final decision stays as it is and is
// answered; an id with nothing stored is answered Abandoned, and for Held
// a final Held is stored (it fails closed). A caller settles Held a Pending
// it did not begin only once it is older than PendingGrace (OverdueAt).
func (r *DecisionRecord) Settle(rule rules.Ref, settle map[string]DecisionState, now time.Time, versions uint64) (map[string]Decision, bool) {
	out := make(map[string]Decision, len(settle))
	writable := versions+1 <= DecisionsVersionsMax
	changed := false
	for id, want := range settle {
		i := r.find(rule, id)
		switch {
		case i >= 0 && r.Entries[i].State != DecisionPending:
			out[id] = Decision{State: r.Entries[i].State, At: r.Entries[i].At}
		case !writable:
			if i >= 0 {
				out[id] = Decision{State: DecisionPending, At: r.Entries[i].At}
			} else {
				out[id] = Decision{State: DecisionFull}
			}
		case i >= 0 && want == DecisionAbandoned:
			r.Entries = append(r.Entries[:i], r.Entries[i+1:]...)
			out[id] = Decision{State: DecisionAbandoned}
			changed = true
		case i >= 0 && (want == DecisionHeld || want == DecisionCleared):
			r.Entries[i].State, r.Entries[i].At = want, now
			out[id] = Decision{State: want, At: now}
			changed = true
		case i < 0 && want == DecisionHeld:
			r.Entries = append(r.Entries, DecisionEntry{Message: id, Rule: rule.ID, Version: rule.Version, State: DecisionHeld, At: now})
			if r.fits() {
				out[id] = Decision{State: DecisionHeld, At: now}
				changed = true
			} else {
				r.Entries = r.Entries[:len(r.Entries)-1]
				out[id] = Decision{State: DecisionFull}
			}
		case i >= 0:
			out[id] = Decision{State: DecisionPending, At: r.Entries[i].At}
		default:
			out[id] = Decision{State: DecisionAbandoned}
		}
	}
	if changed {
		r.prune(now)
	}
	return out, changed
}

// OverdueAt says a Pending decision begun at began is to be settled Held by
// whoever finds it at now.
func OverdueAt(began, now time.Time) bool { return !now.Before(began.Add(PendingGrace)) }

// Leaves says a message may leave on this decision: only a final Cleared.
func (d Decision) Leaves() bool { return d.State == DecisionCleared }
